package network

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

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
)

type Topic struct {
	logger hclog.Logger

	topic     *pubsub.Topic
	typ       reflect.Type
	closeCh   chan struct{}
	closed    atomic.Bool
	waitGroup sync.WaitGroup
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
		t.topic.Close()
		t.topic = nil
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
	if t.topic.String() == "syncer/status/0.1" ||
		strings.Contains(t.topic.String(), "dpos-signature") {
		//t.logger.Info("gossip发布消息", "topic", t.topic.String(), "消息大小", len(data))
	} else {
		//t.logger.Debug("gossip发布消息", "topic", t.topic.String(), "消息大小", len(data))
	}

	// 添加网络状态监控
	if err := t.topic.Publish(context.Background(), data); err != nil {
		// 记录详细的网络错误信息
		t.logger.Error("网络发布失败",
			"topic", t.topic.String(),
			"消息大小", len(data),
			"错误", err)

		// 检查是否是缓冲区满的错误
		if strings.Contains(err.Error(), "buffer") ||
			strings.Contains(err.Error(), "queue") ||
			strings.Contains(err.Error(), "full") {
			t.logger.Error("网络缓冲区已满，消息丢失",
				"topic", t.topic.String(),
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
	defer t.waitGroup.Done()

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

		go func() {
			obj := t.createObj()
			if err := proto.Unmarshal(msg.Data, obj); err != nil {
				t.logger.Error("failed to unmarshal topic", "err", err)
				metrics.IncrCounter([]string{networkMetrics, "bad_messages"}, float32(1))

				return
			}

			// 验证消息的有效性，防止零值消息被传递给处理器
			if !t.isValidMessage(obj) {
				t.logger.Debug("收到无效消息，跳过处理",
					"topic", t.topic.String(),
					"from", msg.GetFrom().String())
				metrics.IncrCounter([]string{networkMetrics, "invalid_messages"}, float32(1))
				return
			}

			metrics.SetGauge([]string{networkMetrics, "ingress_bytes"}, float32(len(msg.Data)))

			// 只对状态广播消息使用INFO级别日志，其他消息不记录
			if t.topic != nil && t.topic.String() == "syncer/status/0.1" {
				t.logger.Info("状态广播消息接收", "topic", t.topic.String(), "来源", msg.GetFrom().String(), "消息大小", len(msg.Data))
			}

			handler(obj, msg.GetFrom())
		}()
	}
}

// isValidMessage 验证消息的有效性，防止零值消息被处理
func (t *Topic) isValidMessage(obj proto.Message) bool {
	if obj == nil {
		return false
	}

	// 针对DPoS签名请求消息的特殊验证
	if strings.Contains(t.topic.String(), "dpos-signature-request") {
		return t.isValidSignatureRequest(obj)
	}

	// 针对DPoS签名响应消息的特殊验证
	if strings.Contains(t.topic.String(), "dpos-signature-response") {
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
	topic, err := s.ps.Join(protoID)
	if err != nil {
		return nil, err
	}

	tt := &Topic{
		logger:  s.logger.Named(protoID),
		topic:   topic,
		typ:     reflect.TypeOf(obj).Elem(),
		closeCh: make(chan struct{}),
	}
	tt.closed.Store(false)

	return tt, nil
}
