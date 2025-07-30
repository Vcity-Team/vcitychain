package dpos

import (
	"encoding/json"
	"fmt"
	ibftProto "github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/libp2p/go-libp2p/core/peer"
	"math/big"
	"github.com/Vcity-Team/vcitychain/types"
	polybftProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
)

// BridgeTransport is an abstraction of network layer for a bridge
type BridgeTransport interface {
	Multicast(msg interface{})
}

// VoteMessage 投票消息
type VoteMessage struct {
	Voter      types.Address `json:"voter"`
	Delegate   types.Address `json:"delegate"`
	Amount     *big.Int      `json:"amount"`
	Round      uint64        `json:"round"`
	Signature  []byte        `json:"signature"`
	Timestamp  uint64        `json:"timestamp"`
}

// DelegateMessage 委托消息
type DelegateMessage struct {
	Delegate   types.Address `json:"delegate"`
	Action     string        `json:"action"` // "register", "unregister", "update"
	Stake      *big.Int      `json:"stake"`
	Signature  []byte        `json:"signature"`
	Timestamp  uint64        `json:"timestamp"`
}

// handleIbftMessage 处理IBFT消息
func (p *DPoS) handleIbftMessage(msg *ibftProto.Message, from peer.ID) error {
	// TODO: 实现IBFT消息处理逻辑
	p.logger.Debug("handling IBFT message", "type", msg.Type, "from", from)
	return nil
}

// handleVoteMessage 处理投票消息
func (p *DPoS) handleVoteMessage(msg *VoteMessage, from peer.ID) error {
	// TODO: 实现投票消息处理逻辑
	p.logger.Debug("handling vote message", "voter", msg.Voter, "delegate", msg.Delegate, "from", from)
	return nil
}

// handleDelegateMessage 处理委托消息
func (p *DPoS) handleDelegateMessage(msg *DelegateMessage, from peer.ID) error {
	// TODO: 实现委托消息处理逻辑
	p.logger.Debug("handling delegate message", "delegate", msg.Delegate, "action", msg.Action, "from", from)
	return nil
}

// subscribeToIbftTopic subscribes to ibft topic
func (p *DPoS) subscribeToIbftTopic() error {
	// 实现 DPoS 的 IBFT 主题订阅
	topicName := "dpos-ibft"
	
	// 创建或获取主题
	topic, err := p.config.Network.NewTopic(topicName, &ibftProto.Message{})
	if err != nil {
		return fmt.Errorf("failed to create IBFT topic: %w", err)
	}

	// 订阅主题
	if err := topic.Subscribe(func(obj interface{}, from peer.ID) {
		msg, ok := obj.(*ibftProto.Message)
		if !ok {
			p.logger.Warn("received invalid message type on IBFT topic")
			return
		}

		// 处理IBFT消息
		if err := p.handleIbftMessage(msg, from); err != nil {
			p.logger.Error("failed to handle IBFT message", "error", err, "from", from)
		}
	}); err != nil {
		return fmt.Errorf("failed to subscribe to IBFT topic: %w", err)
	}

	p.consensusTopic = topic
	p.logger.Info("subscribed to IBFT topic", "topic", topicName)
	return nil
}

// createTopics create all topics for a DPoS instance
func (p *DPoS) createTopics() (err error) {
	// 实现 DPoS 的主题创建
	
	// 创建投票主题 - 使用 JSON 序列化
	voteTopicName := "dpos-vote"
	voteTopic, err := p.config.Network.NewTopic(voteTopicName, &polybftProto.TransportMessage{})
	if err != nil {
		return fmt.Errorf("failed to create vote topic: %w", err)
	}

	// 订阅投票主题
	if err := voteTopic.Subscribe(func(obj interface{}, from peer.ID) {
		transportMsg, ok := obj.(*polybftProto.TransportMessage)
		if !ok {
			p.logger.Warn("received invalid message type on vote topic")
			return
		}

		// 反序列化投票消息
		var voteMsg VoteMessage
		if err := json.Unmarshal(transportMsg.Data, &voteMsg); err != nil {
			p.logger.Warn("failed to unmarshal vote message", "error", err)
			return
		}

		// 处理投票消息
		if err := p.handleVoteMessage(&voteMsg, from); err != nil {
			p.logger.Error("failed to handle vote message", "error", err, "from", from)
		}
	}); err != nil {
		return fmt.Errorf("failed to subscribe to vote topic: %w", err)
	}

	// 创建委托主题 - 使用 JSON 序列化
	delegateTopicName := "dpos-delegate"
	delegateTopic, err := p.config.Network.NewTopic(delegateTopicName, &polybftProto.TransportMessage{})
	if err != nil {
		return fmt.Errorf("failed to create delegate topic: %w", err)
	}

	// 订阅委托主题
	if err := delegateTopic.Subscribe(func(obj interface{}, from peer.ID) {
		transportMsg, ok := obj.(*polybftProto.TransportMessage)
		if !ok {
			p.logger.Warn("received invalid message type on delegate topic")
			return
		}

		// 反序列化委托消息
		var delegateMsg DelegateMessage
		if err := json.Unmarshal(transportMsg.Data, &delegateMsg); err != nil {
			p.logger.Warn("failed to unmarshal delegate message", "error", err)
			return
		}

		// 处理委托消息
		if err := p.handleDelegateMessage(&delegateMsg, from); err != nil {
			p.logger.Error("failed to handle delegate message", "error", err, "from", from)
		}
	}); err != nil {
		return fmt.Errorf("failed to subscribe to delegate topic: %w", err)
	}

	p.logger.Info("created DPoS topics", "voteTopic", voteTopicName, "delegateTopic", delegateTopicName)
	return nil
}

// Multicast is implementation of core.Transport interface
func (p *DPoS) Multicast(msg *ibftProto.Message) {
	if err := p.consensusTopic.Publish(msg); err != nil {
		p.logger.Warn("failed to multicast consensus message", "error", err)
	}
}
