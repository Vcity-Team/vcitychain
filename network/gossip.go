package network

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

const (
	// subscribeOutputBufferSize is the size of subscribe output buffer in go-libp2p-pubsub
	// we should have enough capacity of the queue
	// because when queue is full, if the consumer does not read fast enough, new messages are dropped
	// 增加订阅输出缓冲区大小，防止消息丢失
	subscribeOutputBufferSize = 8192

	// 消息处理优化常量
	maxMessageHandlers    = 50               // 减少最大消息处理器数量
	messageHandlerTimeout = 10 * time.Second // 减少消息处理超时时间
)

type Topic struct {
	logger hclog.Logger

	topic     *pubsub.Topic
	typ       reflect.Type
	closeCh   chan struct{}
	closed    atomic.Bool
	waitGroup sync.WaitGroup

	// 消息处理监控
	activeHandlers int64            // 当前活跃的消息处理器数量
	maxHandlers    int64            // 最大消息处理器数量
	handlerTimeout time.Duration    // 消息处理超时时间
	messageStats   map[string]int64 // 消息统计
	statsMutex     sync.RWMutex     // 统计锁

	// 实际使用的protoID（可能被重命名）
	actualProtoID string
}

func (t *Topic) createObj() proto.Message {
	if t.typ == nil {
		return nil
	}

	message, ok := reflect.New(t.typ).Interface().(proto.Message)
	if !ok {
		return nil
	}

	return message
}

func (t *Topic) Close() {
	if t.closed.Swap(true) {
		// Already closed.
		return
	}

	close(t.closeCh)   // close all subscribers
	t.waitGroup.Wait() // wait for all the subscribers to finish

	// if all subscribers are finished, close the topic
	if t.topic != nil {
		pt := t.topic
		t.topic = nil
		if err := pt.Close(); err != nil {
			// subscription 未注销时 pubsub 会拒绝 Close 且不释放 topic，后续 Join 报 topic already exists（Plan B 重建 syncer 时易触发）。
			t.logger.Warn("pubsub Topic.Close returned error", "err", err)
		}
	}
}

func (t *Topic) Publish(obj proto.Message) error {
	// 检查消息对象是否为空
	if obj == nil {
		return fmt.Errorf("cannot publish nil message")
	}

	// 检查topic是否为空
	if t.topic == nil {
		return fmt.Errorf("cannot publish to nil topic")
	}

	data, err := proto.Marshal(obj)
	if err != nil {
		return err
	}

	metrics.SetGauge([]string{networkMetrics, "egress_bytes"}, float32(len(data)))

	// 只对状态广播和签名相关的topic使用INFO级别日志
	if t.topic != nil {
		topicString := t.topic.String()
		if topicString == "syncer/status/0.1" ||
			strings.Contains(topicString, "dpos-signature") {
			//t.logger.Info("gossip发布消息", "topic", topicString, "消息大小", len(data))
		} else {
			//t.logger.Debug("gossip发布消息", "topic", topicString, "消息大小", len(data))
		}
	}

	// 添加网络状态监控
	if err := t.topic.Publish(context.Background(), data); err != nil {
		// 记录详细的网络错误信息
		topicString := "unknown"
		if t.topic != nil {
			topicString = t.topic.String()
		}
		t.logger.Error("网络发布失败",
			"topic", topicString,
			"消息大小", len(data),
			"错误", err)

		// 检查是否是缓冲区满的错误
		if strings.Contains(err.Error(), "buffer") ||
			strings.Contains(err.Error(), "queue") ||
			strings.Contains(err.Error(), "full") {
			t.logger.Error("网络缓冲区已满，消息丢失",
				"topic", topicString,
				"建议增加缓冲区大小或检查网络负载")
		}

		return err
	}

	return nil
}

func (t *Topic) Subscribe(handler func(obj interface{}, from peer.ID)) error {
	sub, err := t.topic.Subscribe(pubsub.WithBufferSize(subscribeOutputBufferSize))
	if err != nil {
		return err
	}

	// Mark topic active.
	t.closed.Store(false)

	go t.readLoop(sub, handler)

	return nil
}

func (t *Topic) readLoop(sub *pubsub.Subscription, handler func(obj interface{}, from peer.ID)) {
	t.waitGroup.Add(1)
	defer func() {
		// 必须 Cancel subscription，否则底层 Topic.Close 失败，Join 同名 topic 会一直报 topic already exists。
		sub.Cancel()
		t.waitGroup.Done()
	}()

	ctx, cancelFn := context.WithCancel(context.Background())

	go func() {
		<-t.closeCh
		cancelFn()
	}()

	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			// Above cancelFn() called.
			if errors.Is(err, ctx.Err()) {
				break
			}

			t.logger.Error("failed to get topic", "err", err)
			metrics.IncrCounter([]string{networkMetrics, "bad_messages"}, float32(1))

			continue
		}

		// 检查活跃处理器数量
		activeCount := atomic.LoadInt64(&t.activeHandlers)
		if activeCount >= t.maxHandlers {
			t.logger.Warn("消息处理器数量已达上限，跳过消息处理",
				"activeHandlers", activeCount,
				"maxHandlers", t.maxHandlers,
				"from", msg.GetFrom().String())
			metrics.IncrCounter([]string{networkMetrics, "dropped_messages"}, float32(1))
			continue
		}

		// 增加活跃处理器计数
		atomic.AddInt64(&t.activeHandlers, 1)

		go func() {
			defer atomic.AddInt64(&t.activeHandlers, -1)

			// 添加超时保护
			done := make(chan struct{})
			go func() {
				defer close(done)

				obj := t.createObj()
				if err := proto.Unmarshal(msg.Data, obj); err != nil {
					t.logger.Error("failed to unmarshal topic", "err", err)
					metrics.IncrCounter([]string{networkMetrics, "bad_messages"}, float32(1))
					return
				}

				// 验证消息的有效性，防止零值消息被传递给处理器
				if !t.isValidMessage(obj) {
					topicString := "unknown"
					if t.topic != nil {
						topicString = t.topic.String()
					}
					t.logger.Debug("收到无效消息，跳过处理",
						"topic", topicString,
						"from", msg.GetFrom().String())
					metrics.IncrCounter([]string{networkMetrics, "invalid_messages"}, float32(1))
					return
				}

				metrics.SetGauge([]string{networkMetrics, "ingress_bytes"}, float32(len(msg.Data)))

				// 状态广播消息不记录详细日志

				handler(obj, msg.GetFrom())
			}()

			// 等待处理完成或超时
			select {
			case <-done:
				// 正常完成
			case <-time.After(t.handlerTimeout):
				topicString := "unknown"
				if t.topic != nil {
					topicString = t.topic.String()
				}
				t.logger.Warn("消息处理超时",
					"timeout", t.handlerTimeout,
					"from", msg.GetFrom().String(),
					"topic", topicString)
				metrics.IncrCounter([]string{networkMetrics, "timeout_messages"}, float32(1))
			}
		}()
	}
}

// isValidMessage 验证消息的有效性，防止零值消息被处理
func (t *Topic) isValidMessage(obj proto.Message) bool {
	if obj == nil {
		return false
	}

	// 检查topic是否为nil，防止panic
	if t.topic == nil {
		return false
	}

	// 针对DPoS签名请求消息的特殊验证
	topicString := t.topic.String()
	if strings.Contains(topicString, "dpos-signature-request") {
		return t.isValidSignatureRequest(obj)
	}

	// 针对DPoS签名响应消息的特殊验证
	if strings.Contains(topicString, "dpos-signature-response") {
		return t.isValidSignatureResponse(obj)
	}

	// 对于其他类型的消息，默认认为是有效的
	return true
}

// isValidSignatureRequest 验证签名请求消息的有效性
func (t *Topic) isValidSignatureRequest(obj proto.Message) bool {
	// 使用反射获取消息字段值
	val := reflect.ValueOf(obj).Elem()

	// 检查BlockNumber字段
	if blockNumberField := val.FieldByName("BlockNumber"); blockNumberField.IsValid() {
		if blockNumber, ok := blockNumberField.Interface().(uint64); ok {
			if blockNumber == 0 {
				// 如果是查询请求（BlockNumber=0），检查是否有有效的标识符
				if blockHashField := val.FieldByName("BlockHash"); blockHashField.IsValid() {
					if blockHash, ok := blockHashField.Interface().([]byte); ok {
						if string(blockHash) == "QUERY_REQUEST" {
							return true // 有效的查询请求
						}
					}
				}
				if checkpointHashField := val.FieldByName("CheckpointHash"); checkpointHashField.IsValid() {
					if checkpointHash, ok := checkpointHashField.Interface().([]byte); ok {
						if string(checkpointHash) == "QUERY_REQUEST" {
							return true // 有效的查询请求
						}
					}
				}
				// BlockNumber=0 但没有有效标识符，认为是无效消息
				return false
			}
		}
	}

	// 对于非查询请求，检查其他必要字段
	if checkpointHashField := val.FieldByName("CheckpointHash"); checkpointHashField.IsValid() {
		if checkpointHash, ok := checkpointHashField.Interface().([]byte); ok {
			if len(checkpointHash) == 0 {
				return false // CheckpointHash为空
			}
		}
	}

	if proposerField := val.FieldByName("Proposer"); proposerField.IsValid() {
		if proposer, ok := proposerField.Interface().([]byte); ok {
			if len(proposer) == 0 {
				return false // Proposer为空
			}
		}
	}

	return true
}

// isValidSignatureResponse 验证签名响应消息的有效性
func (t *Topic) isValidSignatureResponse(obj proto.Message) bool {
	// 使用反射获取消息字段值
	val := reflect.ValueOf(obj).Elem()

	// 检查ValidatorAddr字段
	if validatorAddrField := val.FieldByName("ValidatorAddr"); validatorAddrField.IsValid() {
		if validatorAddr, ok := validatorAddrField.Interface().([]byte); ok {
			if len(validatorAddr) == 0 {
				return false // ValidatorAddr为空
			}
		}
	}

	// 检查Signature字段
	if signatureField := val.FieldByName("Signature"); signatureField.IsValid() {
		if signature, ok := signatureField.Interface().([]byte); ok {
			if len(signature) == 0 {
				return false // Signature为空
			}
		}
	}

	return true
}

func (s *Server) NewTopic(protoID string, obj proto.Message) (*Topic, error) {
	s.logger.Debug("🔍 开始创建topic", "protoID", protoID)

	topic, err := s.ps.Join(protoID)
	if err != nil {
		s.logger.Error("🔍 Join topic失败", "protoID", protoID, "错误类型", fmt.Sprintf("%T", err), "错误信息", err.Error())
		return nil, err
	}
	s.logger.Debug("🔍 Join topic成功", "protoID", protoID)

	maxHandlers := int64(maxMessageHandlers)
	if s.config != nil && s.config.MaxMessageHandlers > 0 {
		maxHandlers = s.config.MaxMessageHandlers
	}
	tt := &Topic{
		logger:         s.logger.Named(protoID),
		topic:          topic,
		typ:            reflect.TypeOf(obj).Elem(),
		closeCh:        make(chan struct{}),
		maxHandlers:    maxHandlers,
		handlerTimeout: messageHandlerTimeout,
		messageStats:   make(map[string]int64),
		actualProtoID:  protoID, // 存储实际使用的protoID
	}
	tt.closed.Store(false)

	s.logger.Debug("🔍 Topic对象创建成功", "protoID", protoID, "topic对象", fmt.Sprintf("%p", tt))
	return tt, nil
}

// GetActualProtoID 获取实际使用的protoID（可能被重命名）
func (t *Topic) GetActualProtoID() string {
	return t.actualProtoID
}

// GetMessageStats 获取消息处理统计信息
func (t *Topic) GetMessageStats() map[string]interface{} {
	t.statsMutex.RLock()
	defer t.statsMutex.RUnlock()

	stats := make(map[string]interface{})
	stats["activeHandlers"] = atomic.LoadInt64(&t.activeHandlers)
	stats["maxHandlers"] = t.maxHandlers
	stats["handlerTimeout"] = t.handlerTimeout.String()

	// 复制消息统计
	messageStatsCopy := make(map[string]int64)
	for k, v := range t.messageStats {
		messageStatsCopy[k] = v
	}
	stats["messageStats"] = messageStatsCopy

	return stats
}

// LogMessageStats 记录消息处理统计信息
func (t *Topic) LogMessageStats() {
	stats := t.GetMessageStats()
	activeHandlers := stats["activeHandlers"].(int64)
	maxHandlers := stats["maxHandlers"].(int64)

	utilization := float64(activeHandlers) / float64(maxHandlers) * 100

	if utilization > 80 {
		t.logger.Warn("消息处理器使用率较高",
			"activeHandlers", activeHandlers,
			"maxHandlers", maxHandlers,
			"utilization", fmt.Sprintf("%.1f%%", utilization))
	} else {
		t.logger.Debug("消息处理器状态正常",
			"activeHandlers", activeHandlers,
			"maxHandlers", maxHandlers,
			"utilization", fmt.Sprintf("%.1f%%", utilization))
	}
}
