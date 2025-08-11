package dpos

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	ibftMessages "github.com/0xPolygon/go-ibft/messages/proto"
	// polybftProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

// BridgeTransport is an abstraction of network layer for a bridge
type BridgeTransport interface {
	Multicast(msg interface{})
}

// handleIbftMessage 处理IBFT消息
func (p *DPoS) handleIbftMessage(msg *ibftMessages.Message, from peer.ID) error {
	// 记录接收到的IBFT消息
	p.logger.Debug("received IBFT message",
		"type", msg.Type.String(),
		"from", from.String(),
		"height", msg.View.Height,
		"round", msg.View.Round)

	// 根据消息类型进行不同处理
	switch msg.Type {
	case ibftMessages.MessageType_PREPREPARE:
		return p.handlePrePrepareMessage(msg, from)
	case ibftMessages.MessageType_PREPARE:
		return p.handlePrepareMessage(msg, from)
	case ibftMessages.MessageType_COMMIT:
		return p.handleCommitMessage(msg, from)
	case ibftMessages.MessageType_ROUND_CHANGE:
		return p.handleRoundChangeMessage(msg, from)
	default:
		p.logger.Warn("unknown IBFT message type", "type", msg.Type.String())
		return nil
	}
}

// handlePrePrepareMessage 处理预准备消息
func (p *DPoS) handlePrePrepareMessage(msg *ibftMessages.Message, from peer.ID) error {
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
func (p *DPoS) handlePrepareMessage(msg *ibftMessages.Message, from peer.ID) error {
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
func (p *DPoS) handleCommitMessage(msg *ibftMessages.Message, from peer.ID) error {
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
func (p *DPoS) handleRoundChangeMessage(msg *ibftMessages.Message, from peer.ID) error {
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

// handleSignatureRequest 处理签名请求
func (p *DPoS) handleSignatureRequest(request *SignatureRequest, from peer.ID) error {
	p.logger.Debug("received signature request",
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String(),
		"round", request.Round,
		"proposer", request.Proposer.String(),
		"from", from.String())

	// 1. 验证请求
	if err := p.validateSignatureRequest(request); err != nil {
		p.logger.Warn("invalid signature request", "error", err)
		return err
	}

	// 2. 检查自己是否为验证者
	if !p.isActiveValidator() {
		p.logger.Debug("not an active validator, ignoring signature request")
		return nil
	}

	// 3. 生成签名
	signature, err := p.generateSignatureForCheckpoint(request.CheckpointHash)
	if err != nil {
		p.logger.Error("failed to generate signature", "error", err)
		return err
	}

	// 4. 发送签名响应
	response := &SignatureResponse{
		ValidatorAddr:  types.Address(p.key.Address()),
		Signature:      signature,
		CheckpointHash: request.CheckpointHash,
		Timestamp:      uint64(time.Now().Unix()),
	}

	return p.broadcastSignatureResponse(response)
}

// handleSignatureResponse 处理签名响应
func (p *DPoS) handleSignatureResponse(response *SignatureResponse, from peer.ID) error {
	p.logger.Debug("received signature response",
		"validator", response.ValidatorAddr.String(),
		"signatureLength", len(response.Signature),
		"checkpointHash", response.CheckpointHash.String(),
		"from", from.String())

	// 1. 验证响应
	if err := p.validateSignatureResponse(response); err != nil {
		p.logger.Warn("invalid signature response", "error", err)
		return err
	}

	// 2. 验证签名
	if err := p.verifySignatureResponse(response); err != nil {
		p.logger.Warn("signature verification failed", "error", err)
		return err
	}

	// 3. 转发给当前提议者（如果自己不是提议者）
	if !p.isCurrentProposer() {
		return p.forwardSignatureResponse(response)
	}

	return nil
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

// validateSignatureRequest 验证签名请求
func (p *DPoS) validateSignatureRequest(request *SignatureRequest) error {
	// 1. 检查基本字段
	if request.BlockNumber == 0 {
		return errors.New("invalid block number")
	}
	if request.CheckpointHash == types.ZeroHash {
		return errors.New("invalid checkpoint hash")
	}
	if request.Proposer == types.ZeroAddress {
		return errors.New("invalid proposer address")
	}

	// 2. 检查时间戳
	now := uint64(time.Now().Unix())
	if request.Timestamp < now-300 || request.Timestamp > now+60 {
		return errors.New("request timestamp out of range")
	}

	// 3. 检查提议者是否为有效验证者
	isValidValidator := false
	for _, delegate := range p.delegates {
		if delegate.Address == request.Proposer {
			isValidValidator = true
			break
		}
	}
	if !isValidValidator {
		return errors.New("proposer is not a valid validator")
	}

	return nil
}

// validateSignatureResponse 验证签名响应
func (p *DPoS) validateSignatureResponse(response *SignatureResponse) error {
	// 1. 检查基本字段
	if response.ValidatorAddr == types.ZeroAddress {
		return errors.New("invalid validator address")
	}
	if len(response.Signature) == 0 {
		return errors.New("empty signature")
	}
	if response.CheckpointHash == types.ZeroHash {
		return errors.New("invalid checkpoint hash")
	}

	// 2. 检查时间戳
	now := uint64(time.Now().Unix())
	if response.Timestamp < now-300 || response.Timestamp > now+60 {
		return errors.New("response timestamp out of range")
	}

	// 3. 检查验证者是否为有效验证者
	isValidValidator := false
	for _, delegate := range p.delegates {
		if delegate.Address == response.ValidatorAddr {
			isValidValidator = true
			break
		}
	}
	if !isValidValidator {
		return errors.New("validator is not a valid delegate")
	}

	return nil
}

// generateSignatureForCheckpoint 为checkpoint生成签名
func (p *DPoS) generateSignatureForCheckpoint(checkpointHash types.Hash) ([]byte, error) {
	// 获取自己的BLS私钥
	blsKey, err := p.getBLSPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to get BLS private key: %w", err)
	}

	// 对checkpointHash进行签名
	signature, err := blsKey.Sign(checkpointHash[:], signer.DomainValidatorSet)
	if err != nil {
		return nil, fmt.Errorf("failed to sign checkpoint: %w", err)
	}

	// 序列化签名
	signatureBytes, err := signature.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal signature: %w", err)
	}

	p.logger.Info("generated signature for checkpoint",
		"checkpointHash", checkpointHash.String(),
		"signatureLength", len(signatureBytes))

	return signatureBytes, nil
}

// verifySignatureResponse 验证签名响应
func (p *DPoS) verifySignatureResponse(response *SignatureResponse) error {
	// 查找验证者的BLS公钥
	var validatorPubKey *bls.PublicKey
	for _, delegate := range p.delegates {
		if delegate.Address == response.ValidatorAddr {
			validatorPubKey = delegate.BlsKey
			break
		}
	}

	if validatorPubKey == nil {
		return fmt.Errorf("validator %s not found or has no BLS key", response.ValidatorAddr)
	}

	// 检查签名长度
	if len(response.Signature) != 64 {
		return fmt.Errorf("signature length must be 64 bytes, got %d", len(response.Signature))
	}

	// 解析签名
	blsSignature, err := bls.UnmarshalSignature(response.Signature)
	if err != nil {
		return fmt.Errorf("failed to unmarshal signature: %w", err)
	}

	// 验证签名
	if !blsSignature.Verify(validatorPubKey, response.CheckpointHash[:], signer.DomainValidatorSet) {
		return fmt.Errorf("signature verification failed for validator %s", response.ValidatorAddr)
	}

	return nil
}

// broadcastSignatureResponse 广播签名响应
func (p *DPoS) broadcastSignatureResponse(response *SignatureResponse) error {
	// 广播到网络
	if p.consensusTopic != nil {
		// 这里应该使用实际的网络广播机制
		p.logger.Info("broadcasting signature response",
			"validator", response.ValidatorAddr.String(),
			"signatureLength", len(response.Signature))
	}

	return nil
}

// forwardSignatureResponse 转发签名响应给提议者
func (p *DPoS) forwardSignatureResponse(response *SignatureResponse) error {
	// 在实际实现中，这里应该将签名响应转发给当前提议者
	p.logger.Debug("forwarding signature response to proposer",
		"validator", response.ValidatorAddr.String())
	return nil
}

// isActiveValidator 检查自己是否为活跃验证者
func (p *DPoS) isActiveValidator() bool {
	myAddr := types.Address(p.key.Address())
	for _, delegate := range p.delegates {
		if delegate.Address == myAddr && delegate.IsActive {
			return true
		}
	}
	return false
}

// isCurrentProposer 检查自己是否为当前提议者
func (p *DPoS) isCurrentProposer() bool {
	currentDelegate := p.GetCurrentDelegate()
	myAddr := types.Address(p.key.Address())
	return currentDelegate == myAddr
}

// getBLSPrivateKey 获取BLS私钥
func (p *DPoS) getBLSPrivateKey() (*bls.PrivateKey, error) {
	// 从密钥管理器获取BLS私钥
	if p.config.SecretsManager == nil {
		return nil, errors.New("secrets manager is not initialized")
	}

	// 尝试从密钥管理器获取BLS私钥
	blsKey, err := p.config.SecretsManager.GetSecret("bls")
	if err != nil {
		// 如果BLS私钥不存在，生成一个新的
		p.logger.Info("BLS private key not found, generating new one")
		return p.generateBLSPrivateKey()
	}

	// 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey(blsKey)
	if err != nil {
		p.logger.Error("failed to unmarshal BLS private key", "error", err)
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}

	return privateKey, nil
}

// generateBLSPrivateKey 生成新的BLS私钥
func (p *DPoS) generateBLSPrivateKey() (*bls.PrivateKey, error) {
	// 使用当前节点的地址作为种子生成私钥
	addressBytes := p.key.Address().Bytes()
	seedHash := crypto.Keccak256(addressBytes)

	// 将哈希转换为大整数作为私钥
	privateKeyInt := new(big.Int).SetBytes(seedHash)

	// 使用bn256的阶数作为模数
	modulus, _ := new(big.Int).SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)
	privateKeyInt.Mod(privateKeyInt, modulus)

	// 确保私钥不为零
	if privateKeyInt.Cmp(big.NewInt(0)) == 0 {
		privateKeyInt.Set(big.NewInt(1))
	}

	// 将私钥转换为字节并解析
	privateKeyBytes := privateKeyInt.Bytes()
	privateKey, err := bls.UnmarshalPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create BLS private key: %w", err)
	}

	// 保存私钥到密钥管理器
	blsKeyBytes, err := privateKey.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal BLS private key: %w", err)
	}

	if err := p.config.SecretsManager.SetSecret("bls", blsKeyBytes); err != nil {
		p.logger.Warn("failed to save BLS private key", "error", err)
		// 不返回错误，因为私钥已经生成成功
	}

	p.logger.Info("generated new BLS private key")
	return privateKey, nil
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

// isCurrentProposerWithPeer 检查指定peer是否为当前提议者
func (p *DPoS) isCurrentProposerWithPeer(peerID peer.ID) bool {
	// TODO: 实现提议者检查逻辑
	// 这里需要根据当前的轮次和受托人集合来确定提议者
	return true
}

// validateProposal 验证提案
func (p *DPoS) validateProposal(msg *ibftMessages.Message) error {
	// TODO: 实现提案验证逻辑
	return nil
}

// validatePrepareMessage 验证准备消息
func (p *DPoS) validatePrepareMessage(msg *ibftMessages.Message) error {
	// TODO: 实现准备消息验证逻辑
	return nil
}

// validateCommitMessage 验证提交消息
func (p *DPoS) validateCommitMessage(msg *ibftMessages.Message) error {
	// TODO: 实现提交消息验证逻辑
	return nil
}

// validateRoundChangeMessage 验证轮次变更消息
func (p *DPoS) validateRoundChangeMessage(msg *ibftMessages.Message) error {
	// TODO: 实现轮次变更消息验证逻辑
	return nil
}

// hasPrepareQuorum 检查是否达到准备阶段法定人数
func (p *DPoS) hasPrepareQuorum(msg *ibftMessages.Message) bool {
	// TODO: 实现准备阶段法定人数检查
	return false
}

// hasCommitQuorum 检查是否达到提交阶段法定人数
func (p *DPoS) hasCommitQuorum(msg *ibftMessages.Message) bool {
	// TODO: 实现提交阶段法定人数检查
	return false
}

// shouldChangeRound 检查是否需要轮次变更
func (p *DPoS) shouldChangeRound(msg *ibftMessages.Message) bool {
	// TODO: 实现轮次变更检查逻辑
	return false
}

// enterCommitPhase 进入提交阶段
func (p *DPoS) enterCommitPhase(msg *ibftMessages.Message) error {
	// TODO: 实现进入提交阶段逻辑
	return nil
}

// finalizeBlock 最终化区块
func (p *DPoS) finalizeBlock(msg *ibftMessages.Message) error {
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
	topic, err := p.config.Network.NewTopic(topicName, &ibftMessages.Message{})
	if err != nil {
		return fmt.Errorf("failed to create IBFT topic: %w", err)
	}

	// 订阅主题
	if err := topic.Subscribe(func(obj interface{}, from peer.ID) {
		msg, ok := obj.(*ibftMessages.Message)
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
func (p *DPoS) Multicast(msg *ibftMessages.Message) {
	if err := p.consensusTopic.Publish(msg); err != nil {
		p.logger.Warn("failed to multicast consensus message", "error", err)
	}
}
