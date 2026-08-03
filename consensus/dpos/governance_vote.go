package dpos

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// VoteOnParameterProposal 对参数提案进行投票
func (d *DPoS) VoteOnParameterProposal(voter types.Address, proposalID string, support bool, privateKeyHex string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 获取提案
	proposal, exists := d.parameterProposals[proposalID]
	if !exists {
		return fmt.Errorf("proposal not found")
	}

	// 检查投票时间
	currentBlock := d.getCurrentBlockNumber()
	if currentBlock < proposal.StartBlock || currentBlock > proposal.EndBlock {
		return fmt.Errorf("voting period has ended or not started")
	}

	// 仅超级代表可对提案投票：投票者必须在配置的 SR 集合（前 DPoSValidatorsCount 个验证者）内
	srSet, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		d.logger.Warn("Failed to get super representatives for proposal vote", "voter", voter.String(), "error", err)
		return fmt.Errorf("cannot determine super representatives: %w", err)
	}
	srMap := make(map[types.Address]struct{}, len(srSet))
	for _, v := range srSet {
		srMap[v.Address] = struct{}{}
	}
	if _, ok := srMap[voter]; !ok {
		return fmt.Errorf("only super representatives can vote on proposals (voter %s is not an SR)", voter.String())
	}

	// 检查是否已经投票
	if _, exists := proposal.Votes[voter]; exists {
		return fmt.Errorf("already voted on this proposal")
	}

	// 提案投票采用“一 SR 一票”：权重固定为 1，通过条件为获得固定 SR 数量的 51%
	weight := big.NewInt(1)

	// 创建投票
	vote := &ParameterVote{
		Voter:      voter,
		ProposalID: proposalID,
		Support:    support,
		Weight:     weight,
		Timestamp:  uint64(time.Now().Unix()),
	}

	// 调试日志：记录投票参数
	d.logger.Info("🔍 创建投票记录",
		"proposalID", proposalID,
		"voter", voter.String(),
		"support", support,
		"weight", weight.String(),
		"timestamp", vote.Timestamp)

	// 签名投票（传递私钥）
	if err := d.signParameterVote(vote, privateKeyHex); err != nil {
		return fmt.Errorf("failed to sign vote: %w", err)
	}

	// 验证签名
	if err := d.verifyParameterVote(vote); err != nil {
		return fmt.Errorf("failed to verify vote signature: %w", err)
	}

	// 记录投票
	proposal.Votes[voter] = *vote

	// 保存更新后的提案到数据库
	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ ProposalStore不可用，跳过保存投票记录",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("Failed to save updated proposal to database", "error", err)
		}
	} else {
		d.logger.Debug("✅ Updated proposal successfully saved to database",
			"proposalID", proposalID,
			"voter", voter.String(),
			"support", support)
	}

	d.logger.Info("Parameter vote cast",
		"proposalID", proposalID,
		"voter", voter.String(),
		"support", support,
		"weight", weight.String())

	return nil
}

// 注意：调用此函数时，调用者必须已经持有 d.lock 锁，否则会导致死锁
func (d *DPoS) CheckProposalResult(proposalID string) error {
	checkStartTime := time.Now()
	d.logger.Info("🔍 [CheckProposalResult] 开始检查投票结果", "proposalID", proposalID, "startTime", checkStartTime.Format("15:04:05.000000"))

	// 不再获取锁，因为调用者已经持有锁（ProcessProposalExecuteTransaction已经持有d.lock）
	// 如果这里再次获取锁，会导致死锁（Go的sync.Mutex不是可重入的）

	proposal, exists := d.parameterProposals[proposalID]
	if !exists {
		d.logger.Error("❌ [CheckProposalResult] 提案不存在", "proposalID", proposalID)
		return fmt.Errorf("proposal not found")
	}

	// 检查是否在投票期内
	currentBlock := d.getCurrentBlockNumber()
	if currentBlock <= proposal.EndBlock {
		d.logger.Info("ℹ️ [CheckProposalResult] 投票期未结束，跳过检查", "proposalID", proposalID, "currentBlock", currentBlock, "endBlock", proposal.EndBlock)
		return nil // 投票期未结束
	}

	// 统计投票结果：提案通过需获得「实际有效 SR 人数」的 51% 赞成（一 SR 一票）
	// 实际有效 SR：当前 SR 集合中 VotingPower > 0、处于活跃且非故障的验证者数量。
	// 使用带故障过滤的集合（GetSortedValidatorsWithLimitFilterFaulty），这样门槛只按“当前真正有资格出块的验证者”计算。
	calcStartTime := time.Now()
	srSet, err := d.GetSortedValidatorsWithLimitFilterFaulty()
	actualSRCount := uint64(0)
	if err != nil || len(srSet) == 0 {
		// 无法获取 SR 集合时回退到配置人数
		actualSRCount = d.config.DPoSValidatorsCount
		if actualSRCount == 0 {
			actualSRCount = 21
		}
		d.logger.Warn("⚠️ [CheckProposalResult] 使用配置的 SR 数量作为分母（无法获取或无有效SR）", "actualSRCount", actualSRCount, "error", err)
	} else {
		// 仅统计“真实参与”的 SR（有投票权、且活跃；GetSortedValidatorsWithLimitFilterFaulty 已保证非故障）
		for _, v := range srSet {
			if v.VotingPower != nil && v.VotingPower.Sign() > 0 && v.IsActive {
				actualSRCount++
			}
		}
		// 极端情况下如果统计结果为 0，仍然回退到配置人数，避免分母为 0
		if actualSRCount == 0 {
			actualSRCount = d.config.DPoSValidatorsCount
			if actualSRCount == 0 {
				actualSRCount = 21
			}
			d.logger.Warn("⚠️ [CheckProposalResult] 有 SR 但无有效投票权/非活跃，使用配置的 SR 数量作为分母", "fallbackSRCount", actualSRCount)
		}
	}
	minRequiredYes := (actualSRCount*51 + 99) / 100 // ceil(actualSRCount * 0.51)
	if minRequiredYes < 1 {
		minRequiredYes = 1
	}

	yesCount := 0
	for _, vote := range proposal.Votes {
		if vote.Support {
			yesCount++
		}
	}
	voteCount := len(proposal.Votes)
	calcDuration := time.Since(calcStartTime)
	d.logger.Info("📊 [CheckProposalResult] 投票统计完成", "proposalID", proposalID, "voteCount", voteCount, "yesCount", yesCount, "actualSRCount", actualSRCount, "minRequiredYes", minRequiredYes, "calcDuration", calcDuration.String())

	if voteCount == 0 {
		proposal.Status = ProposalRejected
		d.logger.Info("❌ [CheckProposalResult] 提案被拒绝：无投票", "proposalID", proposalID)
		finalizeStartTime := time.Now()
		d.finalizeProposalLifecycle(proposalID, proposal)
		finalizeDuration := time.Since(finalizeStartTime)
		if finalizeDuration > 100*time.Millisecond {
			d.logger.Warn("⚠️ [CheckProposalResult] finalizeProposalLifecycle耗时较长", "proposalID", proposalID, "duration", finalizeDuration.String())
		}
		totalDuration := time.Since(checkStartTime)
		d.logger.Info("✅ [CheckProposalResult] 检查完成", "proposalID", proposalID, "totalDuration", totalDuration.String())
		return nil
	}

	// 通过条件：赞成票数 >= 实际 SR 数量的 51%
	passed := yesCount >= int(minRequiredYes)
	if passed {
		proposal.Status = ProposalPassed
		d.logger.Info("✅ [CheckProposalResult] Proposal passed",
			"proposalID", proposalID,
			"yesCount", yesCount,
			"minRequiredYes", minRequiredYes,
			"actualSRCount", actualSRCount)
	} else {
		proposal.Status = ProposalRejected
		d.logger.Info("❌ [CheckProposalResult] Proposal rejected",
			"proposalID", proposalID,
			"yesCount", yesCount,
			"minRequiredYes", minRequiredYes,
			"actualSRCount", actualSRCount)
	}

	finalizeStartTime := time.Now()
	d.finalizeProposalLifecycle(proposalID, proposal)
	finalizeDuration := time.Since(finalizeStartTime)
	if finalizeDuration > 100*time.Millisecond {
		d.logger.Warn("⚠️ [CheckProposalResult] finalizeProposalLifecycle耗时较长", "proposalID", proposalID, "duration", finalizeDuration.String())
	}

	totalDuration := time.Since(checkStartTime)
	d.logger.Info("✅ [CheckProposalResult] 检查完成", "proposalID", proposalID, "totalDuration", totalDuration.String(), "status", proposal.Status.String())

	return nil
}

// finalizeProposalLifecycle 移除活跃状态并持久化提案
func (d *DPoS) finalizeProposalLifecycle(proposalID string, proposal *ParameterProposal) {
	delete(d.activeProposals, proposalID)

	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ ProposalStore不可用，无法持久化提案状态",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("Failed to persist proposal status", "proposalID", proposalID, "error", err)
		}
	}
}

// buildVoteMessage 构造投票签名消息
func (d *DPoS) buildVoteMessage(vote *ParameterVote) []byte {
	var chainID uint64
	if d.config != nil && d.config.Blockchain != nil && d.config.Blockchain.Config() != nil {
		chainID = uint64(d.config.Blockchain.Config().ChainID)
	}
	message := BuildVoteDomainHash(chainID, vote)
	d.logger.Debug("🔍 构造投票签名消息",
		"voter", vote.Voter.String(),
		"proposalID", vote.ProposalID,
		"support", vote.Support,
		"chainID", chainID,
		"messageHash", hex.EncodeToString(message))
	return message
}

// signParameterVote 签名参数投票
func (d *DPoS) signParameterVote(vote *ParameterVote, privateKeyHex string) error {
	d.logger.Info("🔍 开始签名投票", "voter", vote.Voter.String(), "proposalID", vote.ProposalID)

	// 1. 验证私钥格式
	if len(privateKeyHex) != 64 {
		return fmt.Errorf("invalid private key length: expected 64, got %d", len(privateKeyHex))
	}

	// 2. 验证私钥hex字符
	for i, char := range privateKeyHex {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return fmt.Errorf("invalid hex character at position %d: %c", i, char)
		}
	}

	// 3. 解码私钥
	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}

	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	// 4. 创建ECDSA私钥对象
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	// 5. 验证私钥地址匹配
	calculatedAddr := crypto.PubKeyToAddress(&privateKey.PublicKey)
	if calculatedAddr != vote.Voter {
		return fmt.Errorf("private key does not match voter address: calculated=%s, expected=%s",
			calculatedAddr.String(), vote.Voter.String())
	}

	d.logger.Info("✅ 私钥验证通过", "address", calculatedAddr.String())

	// 6. 构造签名消息
	message := d.buildVoteMessage(vote)

	// 7. 签名
	signature, err := crypto.Sign(privateKey, message)
	if err != nil {
		return fmt.Errorf("failed to sign vote: %w", err)
	}

	// 8. 保存签名
	vote.Signature = signature

	d.logger.Info("✅ 投票签名成功",
		"voter", vote.Voter.String(),
		"proposalID", vote.ProposalID,
		"signatureLength", len(signature))

	return nil
}

// verifyParameterVote 验证参数投票签名
func (d *DPoS) verifyParameterVote(vote *ParameterVote) error {
	if len(vote.Signature) == 0 {
		return fmt.Errorf("vote signature is empty")
	}

	// 1. 构造消息（与签名时相同）
	message := d.buildVoteMessage(vote)

	// 2. 恢复公钥
	pubKey, err := crypto.RecoverPubkey(vote.Signature, message)
	if err != nil {
		return fmt.Errorf("failed to recover public key: %w", err)
	}

	// 3. 计算地址
	recoveredAddr := crypto.PubKeyToAddress(pubKey)

	// 4. 验证地址匹配
	if recoveredAddr != vote.Voter {
		return fmt.Errorf("signature does not match voter address: recovered=%s, expected=%s",
			recoveredAddr.String(), vote.Voter.String())
	}

	d.logger.Debug("✅ 投票签名验证成功", "voter", vote.Voter.String())

	return nil
}

// SignVoteForTx 为交易签名投票（用于RPC层）
func (d *DPoS) SignVoteForTx(vote *ParameterVote, privateKeyHex string) ([]byte, error) {
	// 直接调用signParameterVote
	if err := d.signParameterVote(vote, privateKeyHex); err != nil {
		return nil, err
	}
	return vote.Signature, nil
}
