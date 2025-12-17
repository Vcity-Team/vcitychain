package dpos

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

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

// ============================================================
// 注意：IBFT 共识消息处理代码已删除
// DPoS 使用时间槽轮询出块机制，不依赖 IBFT BFT 投票
// 出块逻辑位于 block_production.go 的 produceBlock() 和 buildBlock()
// ============================================================

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
	p.logger.Debug("收到签名请求",
		"from", from.String(),
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String())

	// 验证请求
	if err := p.validateSignatureRequest(request); err != nil {
		p.logger.Warn("签名请求验证失败", "error", err, "from", from.String())
		return err
	}

	// 检查是否为当前验证者
	if !p.isActiveValidator() {
		p.logger.Debug("非活跃验证者，跳过签名请求", "from", from.String())
		return nil
	}

	// 生成签名响应
	if err := p.generateSignatureResponse(request); err != nil {
		p.logger.Error("生成签名响应失败", "error", err, "from", from.String())
		return err
	}

	return nil
}

// handleSignatureResponse 处理签名响应
func (p *DPoS) handleSignatureResponse(response *SignatureResponse, from peer.ID) error {
	p.logger.Debug("收到签名响应",
		"from", from.String(),
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String())

	// 验证响应
	if err := p.validateSignatureResponse(response); err != nil {
		p.logger.Warn("签名响应验证失败", "error", err, "from", from.String())
		return err
	}

	// 验证签名
	if err := p.verifySignatureResponse(response); err != nil {
		p.logger.Warn("签名验证失败", "error", err, "from", from.String())
		return err
	}

	// 转发签名响应
	if err := p.forwardSignatureResponse(response); err != nil {
		p.logger.Error("转发签名响应失败", "error", err, "from", from.String())
		return err
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
	if request == nil {
		return errors.New("签名请求为空")
	}

	if request.BlockNumber == 0 {
		return errors.New("区块高度无效")
	}

	if request.CheckpointHash == (types.Hash{}) {
		return errors.New("检查点哈希无效")
	}

	if request.Proposer == (types.Address{}) {
		return errors.New("提议者地址无效")
	}

	// 检查时间戳，防止重放攻击
	now := uint64(time.Now().Unix())
	if request.Timestamp > now+60 || request.Timestamp < now-300 {
		return fmt.Errorf("时间戳无效: %d, 当前时间: %d", request.Timestamp, now)
	}

	return nil
}

// validateSignatureResponse 验证签名响应
func (p *DPoS) validateSignatureResponse(response *SignatureResponse) error {
	if response == nil {
		return errors.New("签名响应为空")
	}

	if response.ValidatorAddr == (types.Address{}) {
		return errors.New("验证者地址无效")
	}

	if len(response.Signature) == 0 {
		return errors.New("签名为空")
	}

	if response.CheckpointHash == (types.Hash{}) {
		return errors.New("检查点哈希无效")
	}

	// 检查时间戳，防止重放攻击
	now := uint64(time.Now().Unix())
	if response.Timestamp > now+60 || response.Timestamp < now-300 {
		return fmt.Errorf("时间戳无效: %d, 当前时间: %d", response.Timestamp, now)
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

// generateSignatureResponse 生成签名响应
func (p *DPoS) generateSignatureResponse(request *SignatureRequest) error {
	// 检查是否已经处理过该请求
	if p.runtime.isSignatureRequestProcessed(request.Proposer, request.CheckpointHash) {
		p.logger.Debug("签名请求已处理，跳过",
			"proposer", request.Proposer.String(),
			"checkpointHash", request.CheckpointHash.String())
		return nil
	}

	// 生成BLS私钥
	blsPrivateKey, err := p.getBLSPrivateKey()
	if err != nil {
		return fmt.Errorf("获取BLS私钥失败: %w", err)
	}

	// 生成签名
	signature, err := blsPrivateKey.Sign(request.CheckpointHash.Bytes(), nil)
	if err != nil {
		return fmt.Errorf("生成BLS签名失败: %w", err)
	}

	// 序列化签名
	signatureBytes, err := signature.Marshal()
	if err != nil {
		return fmt.Errorf("序列化BLS签名失败: %w", err)
	}

	// 创建签名响应
	response := &SignatureResponse{
		ValidatorAddr:  types.Address(p.key.Address()),
		Signature:      signatureBytes,
		CheckpointHash: request.CheckpointHash,
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 标记请求已处理
	p.runtime.markSignatureRequestProcessed(request.Proposer, request.CheckpointHash)

	// 广播签名响应
	if err := p.broadcastSignatureResponse(response); err != nil {
		return fmt.Errorf("广播签名响应失败: %w", err)
	}

	p.logger.Info("生成并广播签名响应",
		"proposer", request.Proposer.String(),
		"checkpointHash", request.CheckpointHash.String(),
		"signatureLength", len(signatureBytes))

	return nil
}

// broadcastSignatureResponse 广播签名响应
func (p *DPoS) broadcastSignatureResponse(response *SignatureResponse) error {
	// 检查网络集成状态
	if err := p.checkNetworkIntegration(); err != nil {
		return fmt.Errorf("网络集成检查失败: %w", err)
	}

	// 检查是否已经广播过该响应
	responseKey := fmt.Sprintf("%s_%s_%d",
		response.ValidatorAddr.String(),
		response.CheckpointHash.String(),
		response.Timestamp)

	if p.runtime.isSignatureResponseBroadcasted(responseKey) {
		p.logger.Debug("签名响应已广播，跳过", "responseKey", responseKey)
		return nil
	}

	// 标记响应已广播
	p.runtime.markSignatureResponseBroadcasted(responseKey)

	// 使用网络集成层发送 protobuf 格式的消息
	if err := p.runtime.networkIntegration.BroadcastSignatureResponse(response); err != nil {
		return fmt.Errorf("通过网络集成层广播签名响应失败: %w", err)
	}

	p.logger.Debug("通过网络集成层广播签名响应",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String())
	return nil
}

// forwardSignatureResponse 转发签名响应
func (p *DPoS) forwardSignatureResponse(response *SignatureResponse) error {
	// 检查网络集成状态
	if err := p.checkNetworkIntegration(); err != nil {
		return fmt.Errorf("网络集成检查失败: %w", err)
	}

	// 检查是否已经转发过该响应
	responseKey := fmt.Sprintf("%s_%s_%d",
		response.ValidatorAddr.String(),
		response.CheckpointHash.String(),
		response.Timestamp)

	if p.runtime.isSignatureResponseBroadcasted(responseKey) {
		p.logger.Debug("签名响应已转发，跳过", "responseKey", responseKey)
		return nil
	}

	// 标记响应已转发
	p.runtime.markSignatureResponseBroadcasted(responseKey)

	// 使用网络集成层发送 protobuf 格式的消息
	if err := p.runtime.networkIntegration.BroadcastSignatureResponse(response); err != nil {
		return fmt.Errorf("通过网络集成层转发签名响应失败: %w", err)
	}

	p.logger.Debug("通过网络集成层转发签名响应",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String())
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

// checkNetworkIntegration 检查网络集成是否可用
func (p *DPoS) checkNetworkIntegration() error {
	if p.runtime == nil {
		return fmt.Errorf("runtime 未初始化")
	}
	if p.runtime.networkIntegration == nil {
		return fmt.Errorf("网络集成层未初始化")
	}
	return nil
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

// broadcastMessage 广播消息到其他节点
// 注意：DPoS 投票/委托消息广播（非 IBFT 共识消息）
func (p *DPoS) broadcastMessage(msg interface{}) error {
	// 当前 DPoS 使用时间槽轮询出块，投票消息通过交易处理
	// 此方法保留用于未来 P2P gossip 协议扩展
	p.logger.Debug("broadcastMessage called (no-op in current DPoS implementation)")
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
