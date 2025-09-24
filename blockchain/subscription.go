package blockchain

import (
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

type void struct{}

// Subscription is the blockchain subscription interface
type Subscription interface {
	GetEventCh() chan *Event
	GetEvent() *Event
}

// FOR TESTING PURPOSES //

type MockSubscription struct {
	*subscription
}

func NewMockSubscription() *MockSubscription {
	return &MockSubscription{
		subscription: &subscription{
			updateCh:  make(chan *Event),
			closeCh:   make(chan void),
			timeout:   10 * time.Minute, // 10分钟超时
			createdAt: time.Now(),
		},
	}
}
func (m *MockSubscription) Push(e *Event) {
	m.updateCh <- e
}

// subscription is the Blockchain event subscription object
type subscription struct {
	updateCh  chan *Event   // Channel for update information
	closeCh   chan void     // Channel for close signals
	timeout   time.Duration // 订阅超时时间
	createdAt time.Time     // 创建时间
}

// GetEventCh creates a new event channel, and returns it
func (s *subscription) GetEventCh() chan *Event {
	return s.updateCh
}

// GetEvent returns the event from the subscription (BLOCKING)
func (s *subscription) GetEvent() *Event {
	// Wait for an update
	select {
	case ev := <-s.updateCh:
		return ev
	case <-s.closeCh:
		return nil
	}
}

type EventType int

const (
	EventHead  EventType = iota // New head event
	EventReorg                  // Chain reorganization event
	EventFork                   // Chain fork event
)

// Event is the blockchain event that gets passed to the listeners
type Event struct {
	// Old chain (removed headers) if there was a reorg
	OldChain []*types.Header

	// New part of the chain (or a fork)
	NewChain []*types.Header

	// Difficulty is the new difficulty created with this event
	Difficulty *big.Int

	// Type is the type of event
	Type EventType

	// Source is the source that generated the blocks for the event
	// right now it can be either the Sealer or the Syncer
	Source string
}

// Header returns the latest block header for the event
func (e *Event) Header() *types.Header {
	return e.NewChain[len(e.NewChain)-1]
}

// SetDifficulty sets the event difficulty
func (e *Event) SetDifficulty(b *big.Int) {
	e.Difficulty = new(big.Int).Set(b)
}

// AddNewHeader appends a header to the event's NewChain array
func (e *Event) AddNewHeader(newHeader *types.Header) {
	header := newHeader.Copy()

	if e.NewChain == nil {
		// Array doesn't exist yet, create it
		e.NewChain = []*types.Header{}
	}

	e.NewChain = append(e.NewChain, header)
}

// AddOldHeader appends a header to the event's OldChain array
func (e *Event) AddOldHeader(oldHeader *types.Header) {
	header := oldHeader.Copy()

	if e.OldChain == nil {
		// Array doesn't exist yet, create it
		e.OldChain = []*types.Header{}
	}

	e.OldChain = append(e.OldChain, header)
}

// SubscribeEvents returns a blockchain event subscription
func (b *Blockchain) SubscribeEvents() Subscription {
	return b.stream.subscribe()
}

// UnsubscribeEvents removes subscription from blockchain event stream
func (b *Blockchain) UnsubscribeEvents(sub Subscription) {
	if subPtr, ok := sub.(*subscription); ok {
		b.stream.unsubscribe(subPtr)
	} else {
		b.logger.Warn("Failed to unsubscribe from event stream. Invalid subscription.")
	}
}

// eventStream is the structure that contains the subscribers
// which it uses to notify of updates
type eventStream struct {
	sync.RWMutex
	subscriptions    map[*subscription]struct{}
	maxSubscriptions int // 最大订阅数量
}

// newEventStream creates event stream and initializes subscriptions map
func newEventStream() *eventStream {
	return &eventStream{
		subscriptions:    make(map[*subscription]struct{}),
		maxSubscriptions: 100, // 最大100个订阅
	}
}

// subscribe creates a new blockchain event subscription
func (e *eventStream) subscribe() *subscription {
	e.Lock()
	defer e.Unlock()

	// 检查订阅数量限制
	if len(e.subscriptions) >= e.maxSubscriptions {
		// 清理过期的订阅
		e.cleanupExpiredSubscriptions()

		// 如果清理后仍然超过限制，拒绝新订阅
		if len(e.subscriptions) >= e.maxSubscriptions {
			return nil
		}
	}

	sub := &subscription{
		updateCh:  make(chan *Event, 5),
		closeCh:   make(chan void),
		timeout:   10 * time.Minute, // 10分钟超时
		createdAt: time.Now(),
	}

	e.subscriptions[sub] = struct{}{}

	return sub
}

// cleanupExpiredSubscriptions 清理过期的订阅
func (e *eventStream) cleanupExpiredSubscriptions() {
	now := time.Now()
	expiredSubscriptions := make([]*subscription, 0)

	for sub := range e.subscriptions {
		// 清理超过超时时间的订阅
		if now.Sub(sub.createdAt) > sub.timeout {
			expiredSubscriptions = append(expiredSubscriptions, sub)
		}
	}

	// 删除过期的订阅
	for _, sub := range expiredSubscriptions {
		delete(e.subscriptions, sub)
		// 关闭订阅
		close(sub.closeCh)
	}
}

func (e *eventStream) unsubscribe(sub *subscription) {
	e.Lock()
	defer e.Unlock()

	delete(e.subscriptions, sub)
	close(sub.closeCh)
}

// push adds a new Event, and notifies listeners
func (e *eventStream) push(event *Event) {
	e.RLock()
	defer e.RUnlock()

	// 添加事件推送日志跟踪
	if event != nil && len(event.NewChain) > 0 {
		// 注意：这里没有logger，所以暂时注释掉
		// latest := event.NewChain[len(event.NewChain)-1]
		// fmt.Printf("🔔 事件流推送: 区块高度=%d 区块哈希=%s 来源=%s 订阅者数量=%d\n", 
		// 	latest.Number, latest.Hash.String(), event.Source, len(e.subscriptions))
	}

	// Notify the listeners
	for sub := range e.subscriptions {
		select {
		case sub.updateCh <- event:
			// 成功发送
		default:
			// 如果channel满了，跳过这个订阅者，避免阻塞
			// 可以选择记录日志或采取其他处理方式
		}
	}
}
