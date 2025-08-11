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
	subscribeOutputBufferSize = 4096
)

type Topic struct {
	logger hclog.Logger

	topic     *pubsub.Topic
	typ       reflect.Type
	closeCh   chan struct{}
	closed    atomic.Bool
	waitGroup sync.WaitGroup

	// 统计信息
	statsMutex      sync.RWMutex
	publishedCount  int64
	receivedCount   int64
	lastPublishTime time.Time
	lastReceiveTime time.Time
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

	// 更新统计信息
	t.statsMutex.Lock()
	t.publishedCount++
	t.lastPublishTime = time.Now()
	t.statsMutex.Unlock()

	// 添加pubsub调试信息
	if t.topic.String() == "syncer/status/0.1" {
		t.logger.Info("pubsub发布状态开始", "topic", t.topic.String(), "消息大小", len(data), "时间", time.Now().Format("15:04:05.000"))
	}

	// 调用pubsub的Publish方法
	err = t.topic.Publish(context.Background(), data)

	// 记录发布结果
	if t.topic.String() == "syncer/status/0.1" {
		if err != nil {
			t.logger.Error("pubsub发布状态失败", "topic", t.topic.String(), "错误", err, "时间", time.Now().Format("15:04:05.000"))
		} else {
			t.logger.Info("pubsub发布状态成功", "topic", t.topic.String(), "消息大小", len(data), "时间", time.Now().Format("15:04:05.000"))
		}
	}

	return err
}

func (t *Topic) Subscribe(handler func(obj interface{}, from peer.ID)) error {
	sub, err := t.topic.Subscribe(pubsub.WithBufferSize(subscribeOutputBufferSize))
	if err != nil {
		return err
	}

	// Mark topic active.
	t.closed.Store(false)

	// 记录订阅信息
	if t.topic.String() == "syncer/status/0.1" {
		t.logger.Info("订阅状态广播topic", "topic", t.topic.String(), "缓冲区大小", subscribeOutputBufferSize)
	}

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

			metrics.SetGauge([]string{networkMetrics, "ingress_bytes"}, float32(len(msg.Data)))

			// 更新接收统计信息
			t.statsMutex.Lock()
			t.receivedCount++
			t.lastReceiveTime = time.Now()
			t.statsMutex.Unlock()

			// 只对状态广播消息使用INFO级别日志，其他消息不记录
			if t.topic != nil && t.topic.String() == "syncer/status/0.1" {
				t.logger.Info("状态广播消息接收",
					"topic", t.topic.String(),
					"来源", msg.GetFrom().String(),
					"消息大小", len(msg.Data))
			}

			handler(obj, msg.GetFrom())
		}()
	}
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

// GetStats 获取topic的统计信息
func (t *Topic) GetStats() map[string]interface{} {
	t.statsMutex.RLock()
	defer t.statsMutex.RUnlock()

	stats := map[string]interface{}{
		"publishedCount":  t.publishedCount,
		"receivedCount":   t.receivedCount,
		"lastPublishTime": t.lastPublishTime,
		"lastReceiveTime": t.lastReceiveTime,
		"topic":           t.topic.String(),
	}

	// 添加时间间隔信息
	if !t.lastPublishTime.IsZero() {
		stats["timeSinceLastPublish"] = time.Since(t.lastPublishTime).String()
	}
	if !t.lastReceiveTime.IsZero() {
		stats["timeSinceLastReceive"] = time.Since(t.lastReceiveTime).String()
	}

	return stats
}
