package dpos

import (
	"errors"
	"fmt"

	ibftProto "github.com/0xPolygon/go-ibft/messages/proto"

	"github.com/libp2p/go-libp2p/core/peer"
)

// BridgeTransport is an abstraction of network layer for a bridge
type BridgeTransport interface {
	Multicast(msg interface{})
}

// handleIbftMessage 处理IBFT消息
func (p *DPoS) handleIbftMessage(msg *ibftProto.Message, from peer.ID) error {
	// 记录接收到的IBFT消息
	p.logger.Debug("received IBFT message",
		"type", msg.Type.String(),
		"from", from.String(),
		"height", msg.View.Height,
		"round", msg.View.Round)

	// 根据消息类型进行不同处理
	switch msg.Type {
	case ibftProto.MessageType_PREPREPARE:
		return p.handlePrePrepareMessage(msg, from)
	case ibftProto.MessageType_PREPARE:
		return p.handlePrepareMessage(msg, from)
	case ibftProto.MessageType_COMMIT:
		return p.handleCommitMessage(msg, from)
	case ibftProto.MessageType_ROUND_CHANGE:
		return p.handleRoundChangeMessage(msg, from)
	default:
		p.logger.Warn("unknown IBFT message type", "type", msg.Type.String())
		return nil
	}
}

// handlePrePrepareMessage 处理预准备消息
func (p *DPoS) handlePrePrepareMessage(msg *ibftProto.Message, from peer.ID) error {
	// 验证发送者是否为当前提议者
	if !p.isCurrentProposerWithPeer(from) {
		p.logger.Warn("received pre-prepare from non-proposer", "from", from.String())
		return errors.New("pre-prepare from non-proposer")
	}

	// 验证提案
	if err := p.validateProposal(msg); err != nil {
		p.logger.Warn("invalid proposal in pre-prepare", "error", err)
		return err
	}

	// 转发给其他节点
	return p.broadcastMessage(msg)
}

// handlePrepareMessage 处理准备消息
func (p *DPoS) handlePrepareMessage(msg *ibftProto.Message, from peer.ID) error {
	// 验证准备消息
	if err := p.validatePrepareMessage(msg); err != nil {
		p.logger.Warn("invalid prepare message", "error", err)
		return err
	}

	// 检查是否达到准备阶段法定人数
	if p.hasPrepareQuorum(msg) {
		p.logger.Info("prepare quorum reached", "height", msg.View.Height, "round", msg.View.Round)
		// 进入提交阶段
		return p.enterCommitPhase(msg)
	}

	return nil
}

// handleCommitMessage 处理提交消息
func (p *DPoS) handleCommitMessage(msg *ibftProto.Message, from peer.ID) error {
	// 验证提交消息
	if err := p.validateCommitMessage(msg); err != nil {
		p.logger.Warn("invalid commit message", "error", err)
		return err
	}

	// 检查是否达到提交阶段法定人数
	if p.hasCommitQuorum(msg) {
		p.logger.Info("commit quorum reached", "height", msg.View.Height, "round", msg.View.Round)
		// 最终化区块
		return p.finalizeBlock(msg)
	}

	return nil
}

// handleRoundChangeMessage 处理轮次变更消息
func (p *DPoS) handleRoundChangeMessage(msg *ibftProto.Message, from peer.ID) error {
	// 验证轮次变更消息
	if err := p.validateRoundChangeMessage(msg); err != nil {
		p.logger.Warn("invalid round change message", "error", err)
		return err
	}

	// 检查是否需要轮次变更
	if p.shouldChangeRound(msg) {
		p.logger.Info("round change triggered", "new_round", msg.View.Round)
		return p.changeRound(msg.View.Round)
	}

	return nil
}

// isCurrentProposerWithPeer 检查指定peer是否为当前提议者
func (p *DPoS) isCurrentProposerWithPeer(peerID peer.ID) bool {
	// TODO: 实现提议者检查逻辑
	// 这里需要根据当前的轮次和受托人集合来确定提议者
	return true
}

// validateProposal 验证提案
func (p *DPoS) validateProposal(msg *ibftProto.Message) error {
	// TODO: 实现提案验证逻辑
	return nil
}

// validatePrepareMessage 验证准备消息
func (p *DPoS) validatePrepareMessage(msg *ibftProto.Message) error {
	// TODO: 实现准备消息验证逻辑
	return nil
}

// validateCommitMessage 验证提交消息
func (p *DPoS) validateCommitMessage(msg *ibftProto.Message) error {
	// TODO: 实现提交消息验证逻辑
	return nil
}

// validateRoundChangeMessage 验证轮次变更消息
func (p *DPoS) validateRoundChangeMessage(msg *ibftProto.Message) error {
	// TODO: 实现轮次变更消息验证逻辑
	return nil
}

// hasPrepareQuorum 检查是否达到准备阶段法定人数
func (p *DPoS) hasPrepareQuorum(msg *ibftProto.Message) bool {
	// TODO: 实现准备阶段法定人数检查
	return false
}

// hasCommitQuorum 检查是否达到提交阶段法定人数
func (p *DPoS) hasCommitQuorum(msg *ibftProto.Message) bool {
	// TODO: 实现提交阶段法定人数检查
	return false
}

// shouldChangeRound 检查是否需要轮次变更
func (p *DPoS) shouldChangeRound(msg *ibftProto.Message) bool {
	// TODO: 实现轮次变更检查逻辑
	return false
}

// enterCommitPhase 进入提交阶段
func (p *DPoS) enterCommitPhase(msg *ibftProto.Message) error {
	// TODO: 实现进入提交阶段逻辑
	return nil
}

// finalizeBlock 最终化区块
func (p *DPoS) finalizeBlock(msg *ibftProto.Message) error {
	// TODO: 实现区块最终化逻辑
	return nil
}

// changeRound 变更轮次
func (p *DPoS) changeRound(newRound uint64) error {
	// TODO: 实现轮次变更逻辑
	return nil
}

// broadcastMessage 广播消息
func (p *DPoS) broadcastMessage(msg interface{}) error {
	// TODO: 实现消息广播逻辑
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
	// TODO: 暂时注释掉 proto 相关代码，等 proto 问题解决后再启用
	/*
		// 创建投票主题
		voteTopicName := "dpos-vote"
		p.voteTopic, err = p.config.Network.NewTopic(voteTopicName, &TransportMessage{})
		if err != nil {
			return fmt.Errorf("failed to create vote topic: %w", err)
		}

		// 订阅投票主题
		err = p.voteTopic.Subscribe(func(obj interface{}, from peer.ID) {
			transportMsg, ok := obj.(*TransportMessage)
			if !ok {
				p.logger.Warn("failed to deliver vote, invalid msg", "obj", obj)
				return
			}

			var voteMsg VoteMessage
			if err := json.Unmarshal(transportMsg.Data, &voteMsg); err != nil {
				p.logger.Warn("failed to unmarshal vote message", "error", err)
				return
			}

			if err := p.handleVoteMessage(&voteMsg, from); err != nil {
				p.logger.Warn("failed to handle vote message", "error", err)
			}
		})
		if err != nil {
			return fmt.Errorf("failed to subscribe to vote topic: %w", err)
		}

		// 创建受托人主题
		delegateTopicName := "dpos-delegate"
		p.delegateTopic, err = p.config.Network.NewTopic(delegateTopicName, &TransportMessage{})
		if err != nil {
			return fmt.Errorf("failed to create delegate topic: %w", err)
		}

		// 订阅受托人主题
		err = p.delegateTopic.Subscribe(func(obj interface{}, from peer.ID) {
			transportMsg, ok := obj.(*TransportMessage)
			if !ok {
				p.logger.Warn("failed to deliver delegate, invalid msg", "obj", obj)
				return
			}

			var delegateMsg DelegateMessage
			if err := json.Unmarshal(transportMsg.Data, &delegateMsg); err != nil {
				p.logger.Warn("failed to unmarshal delegate message", "error", err)
				return
			}

			if err := p.handleDelegateMessage(&delegateMsg, from); err != nil {
				p.logger.Warn("failed to handle delegate message", "error", err)
			}
		})
		if err != nil {
			return fmt.Errorf("failed to subscribe to delegate topic: %w", err)
		}
	*/

	p.logger.Info("DPoS topics creation temporarily disabled")
	return nil
}

// Multicast is implementation of core.Transport interface
func (p *DPoS) Multicast(msg *ibftProto.Message) {
	if err := p.consensusTopic.Publish(msg); err != nil {
		p.logger.Warn("failed to multicast consensus message", "error", err)
	}
}
