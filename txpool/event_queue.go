package txpool

import (
	"sync"

	"github.com/Vcity-Team/vcitychain/txpool/proto"
)

type eventQueue struct {
	events []*proto.TxPoolEvent
	sync.Mutex
	maxSize int // 最大队列大小
}

func (es *eventQueue) push(event *proto.TxPoolEvent) {
	es.Lock()
	defer es.Unlock()

	// 如果队列已满，移除最旧的事件
	if es.maxSize > 0 && len(es.events) >= es.maxSize {
		es.events = es.events[1:]
	}

	es.events = append(es.events, event)
}

func (es *eventQueue) pop() *proto.TxPoolEvent {
	es.Lock()
	defer es.Unlock()

	if len(es.events) == 0 {
		return nil
	}

	event := es.events[0]
	es.events = es.events[1:]

	return event
}
