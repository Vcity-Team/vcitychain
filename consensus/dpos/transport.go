package dpos

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	ibftProto "github.com/0xPolygon/go-ibft/messages/proto"
	polybftProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
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
	if !p.isCurrentProposer(from) {
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

// handleVoteMessage 处理投票消息
func (p *DPoS) handleVoteMessage(msg *VoteMessage, from peer.ID) error {
	p.logger.Debug("received vote message",
		"voter", msg.Voter.String(),
		"delegate", msg.Delegate.String(),
		"amount", msg.Amount.String(),
		"from", from.String())

	// 1. 验证投票消息
	if err := p.validateVoteMessage(msg); err != nil {
		p.logger.Warn("invalid vote message", "error", err, "voter", msg.Voter)
		return err
	}

	// 2. 验证签名
	if err := p.verifyVoteSignature(msg); err != nil {
		p.logger.Warn("vote signature verification failed", "error", err, "voter", msg.Voter)
		return err
	}

	// 3. 检查防重放攻击
	if err := p.checkVoteNonce(msg); err != nil {
		p.logger.Warn("vote nonce check failed", "error", err, "voter", msg.Voter)
		return err
	}

	// 4. 处理投票
	if err := p.processVote(msg); err != nil {
		p.logger.Warn("failed to process vote", "error", err, "voter", msg.Voter)
		return err
	}

	// 5. 广播投票消息给其他节点
	return p.broadcastVoteMessage(msg)
}

// handleDelegateMessage 处理委托消息
func (p *DPoS) handleDelegateMessage(msg *DelegateMessage, from peer.ID) error {
	p.logger.Debug("received delegate message",
		"action", msg.Action,
		"delegate", msg.Delegate.String(),
		"stake", msg.Stake.String(),
		"from", from.String())

	// 1. 验证委托消息
	if err := p.validateDelegateMessage(msg); err != nil {
		p.logger.Warn("invalid delegate message", "error", err)
		return err
	}

	// 2. 根据动作类型处理
	switch msg.Action {
	case "register":
		return p.handleDelegateRegistration(msg)
	case "unregister":
		return p.handleDelegateUnregistration(msg)
	case "update":
		return p.handleDelegateUpdate(msg)
	default:
		return fmt.Errorf("unknown delegate action: %s", msg.Action)
	}
}

// validateVoteMessage 验证投票消息
func (p *DPoS) validateVoteMessage(msg *VoteMessage) error {
	// 1. 检查基本字段
	if msg.Voter == types.ZeroAddress {
		return errors.New("invalid voter address")
	}
	if msg.Delegate == types.ZeroAddress {
		return errors.New("invalid delegate address")
	}
	if msg.Amount == nil || msg.Amount.Cmp(big.NewInt(0)) <= 0 {
		return errors.New("invalid vote amount")
	}

	// 2. 检查投票金额边界
	maxAmount, _ := new(big.Int).SetString(MaxVoteAmount, 10)
	if msg.Amount.Cmp(maxAmount) > 0 {
		return errors.New("vote amount exceeds maximum")
	}

	minAmount, _ := new(big.Int).SetString(MinVoteAmount, 10)
	if msg.Amount.Cmp(minAmount) < 0 {
		return errors.New("vote amount below minimum")
	}

	// 3. 检查时间戳
	now := uint64(time.Now().Unix())
	if msg.Timestamp < now-300 || msg.Timestamp > now+60 {
		return errors.New("vote timestamp out of range")
	}

	return nil
}

// validateDelegateMessage 验证委托消息
func (p *DPoS) validateDelegateMessage(msg *DelegateMessage) error {
	// 1. 检查基本字段
	if msg.Delegate == types.ZeroAddress {
		return errors.New("invalid delegate address")
	}
	if msg.Stake == nil || msg.Stake.Cmp(big.NewInt(0)) <= 0 {
		return errors.New("invalid stake amount")
	}

	// 2. 检查动作类型
	validActions := map[string]bool{
		"register":   true,
		"unregister": true,
		"update":     true,
	}
	if !validActions[msg.Action] {
		return fmt.Errorf("invalid action: %s", msg.Action)
	}

	// 3. 检查质押金额
	minStake, _ := new(big.Int).SetString(MinVoteAmount, 10)
	if msg.Stake.Cmp(minStake) < 0 {
		return errors.New("stake amount below minimum")
	}

	return nil
}

// handleDelegateRegistration 处理受托人注册
func (p *DPoS) handleDelegateRegistration(msg *DelegateMessage) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	// 检查受托人是否已存在
	for _, del := range p.delegates {
		if del.Address == msg.Delegate {
			return errors.New("delegate already exists")
		}
	}

	// 添加新受托人
	newDelegate := &validator.ValidatorMetadata{
		Address:     msg.Delegate,
		VotingPower: msg.Stake,
		IsActive:    true,
	}

	p.delegates = append(p.delegates, newDelegate)

	p.logger.Info("delegate registered",
		"address", msg.Delegate.String(),
		"stake", msg.Stake.String())

	return nil
}

// handleDelegateUnregistration 处理受托人注销
func (p *DPoS) handleDelegateUnregistration(msg *DelegateMessage) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	// 查找并移除受托人
	newSet := validator.AccountSet{}
	found := false

	for _, del := range p.delegates {
		if del.Address == msg.Delegate {
			found = true
			continue
		}
		newSet = append(newSet, del)
	}

	if !found {
		return errors.New("delegate not found")
	}

	p.delegates = newSet

	p.logger.Info("delegate unregistered", "address", msg.Delegate.String())

	return nil
}

// handleDelegateUpdate 处理受托人更新
func (p *DPoS) handleDelegateUpdate(msg *DelegateMessage) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	// 查找并更新受托人
	for _, del := range p.delegates {
		if del.Address == msg.Delegate {
			oldStake := del.VotingPower
			del.VotingPower = msg.Stake

			p.logger.Info("delegate updated",
				"address", msg.Delegate.String(),
				"old_stake", oldStake.String(),
				"new_stake", msg.Stake.String())

			return nil
		}
	}

	return errors.New("delegate not found")
}

// broadcastVoteMessage 广播投票消息
func (p *DPoS) broadcastVoteMessage(msg *VoteMessage) error {
	// 序列化投票消息
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal vote message: %w", err)
	}

	// 广播给其他节点
	return p.broadcastMessage(data)
}

// broadcastDelegateMessage 广播委托消息
func (p *DPoS) broadcastDelegateMessage(msg *DelegateMessage) error {
	// 序列化委托消息
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal delegate message: %w", err)
	}

	// 广播给其他节点
	return p.broadcastMessage(data)
}

// isCurrentProposer 检查是否为当前提议者
func (p *DPoS) isCurrentProposer(peerID peer.ID) bool {
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
