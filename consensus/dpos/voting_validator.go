package dpos

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// 投票相关常量
const (
	MaxVotingPower = "1000000000000000000000000" // 1M tokens
	MaxDelegates   = 100
	VoteLockTime   = 2                          // 2秒（仅用于测试）
	MaxVoteAmount  = "100000000000000000000000" // 100K tokens
	MinVoteAmount  = "1000000000000000000"      // 1 token
)

// ValidateVoteOnly 仅验证投票，不更新状态（供 JSON-RPC 调用）
func (d *DPoS) ValidateVoteOnly(voter types.Address, candidate types.Address, amount *big.Int) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.logger.Debug("Validating vote only (no state update)",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String())

	// 创建投票消息
	vote := &VoteMessage{
		Voter:     voter,
		Delegate:  candidate,
		Amount:    amount,
		Round:     d.currentRound,
		Timestamp: uint64(time.Now().Unix()),
	}

	// 只进行验证，不更新状态
	if err := d.validateVote(vote); err != nil {
		d.logger.Error("❌ Vote validation failed", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}

	d.logger.Debug("✅ Vote validation passed (no state update)")
	return nil
}

// validateVote 验证投票的有效性
func (d *DPoS) validateVote(vote *VoteMessage) error {
	// 1. 检查投票金额边界
	if vote.Amount.Cmp(big.NewInt(0)) <= 0 {
		return errors.New("vote amount must be positive")
	}

	maxAmount, _ := new(big.Int).SetString(MaxVoteAmount, 10)
	if vote.Amount.Cmp(maxAmount) > 0 {
		return errors.New("vote amount exceeds maximum")
	}

	minAmount, _ := new(big.Int).SetString(MinVoteAmount, 10)
	if vote.Amount.Cmp(minAmount) < 0 {
		return errors.New("vote amount below minimum")
	}

	// 2. 检查投票者VCITY代币余额和剩余可投票数
	if d.balanceQuerier != nil {
		balance, err := d.balanceQuerier.GetNativeTokenBalance(vote.Voter)
		if err != nil {
			d.logger.Error("Failed to query voter balance",
				"voter", vote.Voter.String(),
				"error", err)
			return fmt.Errorf("failed to query voter balance: %w", err)
		}

		// 计算已投票金额（使用 VoterInfo.VotingPower）
		totalVotedAmount := d.calculateTotalVotedAmount(vote.Voter)

		// 计算剩余可投票数
		remainingVotableAmount := new(big.Int).Sub(balance, totalVotedAmount)

		// 检查剩余可投票数是否足够
		if remainingVotableAmount.Cmp(vote.Amount) < 0 {
			d.logger.Warn("Insufficient remaining votable amount for vote",
				"voter", vote.Voter.String(),
				"required", vote.Amount.String(),
				"remaining", remainingVotableAmount.String(),
				"balance", balance.String(),
				"totalVoted", totalVotedAmount.String())
			return fmt.Errorf("insufficient remaining votable amount: required %s, remaining %s, balance %s, totalVoted %s",
				vote.Amount.String(), remainingVotableAmount.String(), balance.String(), totalVotedAmount.String())
		}

		d.logger.Info("Vote balance and remaining amount check passed",
			"voter", vote.Voter.String(),
			"voteAmount", vote.Amount.String(),
			"balance", balance.String(),
			"totalVoted", totalVotedAmount.String(),
			"remaining", remainingVotableAmount.String())
	} else {
		d.logger.Warn("Balance querier not available, skipping balance check")
	}

	// 新增：检查受托人是否已注册（创世验证者例外）
	d.logger.Info("🔍 开始验证受托人注册状态",
		"delegate", vote.Delegate.String(),
		"voter", vote.Voter.String(),
		"amount", vote.Amount.String())

	// 确保创世验证者映射已初始化
	if d.genesisValidators == nil || len(d.genesisValidators) == 0 {
		d.initializeGenesisValidatorsMap()
	}

	// 创世验证者可以直接被投票，无需注册
	if d.isGenesisValidator(vote.Delegate) {
		d.logger.Info("✅ 受托人是创世验证者，跳过注册检查",
			"delegate", vote.Delegate.String(),
			"voter", vote.Voter.String())
	} else if !d.IsDelegateRegistered(vote.Delegate) {
		d.logger.Warn("❌ 受托人未注册，投票被拒绝",
			"delegate", vote.Delegate.String(),
			"voter", vote.Voter.String(),
			"amount", vote.Amount.String(),
			"reason", "delegate not registered")
		return fmt.Errorf("delegate %s is not registered", vote.Delegate.String())
	}

	d.logger.Info("✅ 受托人注册状态验证通过",
		"delegate", vote.Delegate.String(),
		"voter", vote.Voter.String())

	// 新增：检查受托人是否为候选人状态（可以接受投票，创世验证者例外）
	d.logger.Info("🔍 开始验证受托人候选人状态",
		"delegate", vote.Delegate.String(),
		"voter", vote.Voter.String())

	// 确保创世验证者映射已初始化（如果之前没有初始化）
	if d.genesisValidators == nil || len(d.genesisValidators) == 0 {
		d.initializeGenesisValidatorsMap()
	}

	// 创世验证者可以直接被投票，无需检查候选人状态
	if d.isGenesisValidator(vote.Delegate) {
		d.logger.Info("✅ 受托人是创世验证者，跳过候选人状态检查",
			"delegate", vote.Delegate.String(),
			"voter", vote.Voter.String())
	} else if !d.IsDelegateCandidate(vote.Delegate) {
		d.logger.Warn("❌ 受托人不是候选人状态，投票被拒绝",
			"delegate", vote.Delegate.String(),
			"voter", vote.Voter.String(),
			"amount", vote.Amount.String(),
			"reason", "delegate not a candidate")
		return fmt.Errorf("delegate %s is not a candidate", vote.Delegate.String())
	}

	d.logger.Info("✅ 受托人候选人状态验证通过",
		"delegate", vote.Delegate.String(),
		"voter", vote.Voter.String())

	// 2. 检查受托人是否存在且活跃（从数据库查询，不依赖内存）
	delegateExists := false
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Error("❌ 数据库不可用，无法验证受托人",
			"stateIsNil", d.state == nil,
			"stakeStoreIsNil", d.state != nil && d.state.StakeStore == nil)
		return fmt.Errorf("database not available, cannot verify delegate")
	}

	// 从数据库查询受托人信息
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		d.logger.Error("❌ 从数据库查询受托人失败", "error", err)
		return fmt.Errorf("failed to query delegates from database: %w", err)
	}

	for _, validator := range validators {
		if validator.Address == vote.Delegate && validator.IsActive {
			delegateExists = true
			d.logger.Debug("🔍 从数据库找到活跃受托人",
				"delegate", vote.Delegate.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive)
			break
		}
	}

	// ✅ 修改：不在投票时创建验证者记录到数据库
	// 验证者记录应该在边界应用时创建，而不是在投票时
	// 这里只检查验证者是否存在，不创建新记录
	if !delegateExists {
		d.logger.Debug("⚠️ 受托人不在验证者集合中，将在边界应用时创建",
			"delegate", vote.Delegate.String(),
			"note", "投票已记录，将在epoch边界应用时创建验证者记录")
		// 不再调用 addDelegateSafely，避免创建零权重验证者记录
	}

	// 3. 投票锁定时间检查 - 已删除（投票者不受限制）
	// 4. 投票权重上限检查 - 已删除（投票者不受限制）

	return nil
}

// verifyVoteSignature 验证投票签名
func (d *DPoS) verifyVoteSignature(vote *VoteMessage) error {
	// 构建投票消息哈希
	message := fmt.Sprintf("%s:%s:%s:%d",
		vote.Voter.String(),
		vote.Delegate.String(),
		vote.Amount.String(),
		vote.Round)

	messageBytes := []byte(message)
	hash := crypto.Keccak256(messageBytes)

	// 注意：DPoS 投票通过交易处理，签名验证在区块链层完成
	// 此函数仅用于 P2P gossip 消息（当前未使用）
	// 如启用 P2P 投票，需实现 ECDSA 签名恢复验证
	_ = hash // 保留用于未来 P2P 签名验证

	// 检查时间戳防重放
	now := uint64(time.Now().Unix())
	if vote.Timestamp < now-300 || vote.Timestamp > now+60 { // 5分钟时间窗口
		return errors.New("vote timestamp out of range")
	}

	return nil
}

// checkVoteNonce 检查投票nonce，防止重放攻击
func (d *DPoS) checkVoteNonce(vote *VoteMessage) error {
	if voter, exists := d.voters[vote.Voter]; exists {
		if voter.Nonce[vote.Round] {
			return errors.New("vote nonce already used")
		}
	}
	return nil
}
