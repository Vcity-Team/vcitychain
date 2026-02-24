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

	// 🔍 调试日志：记录传入的 amount 参数
	d.logger.Info("🔍 [ValidateVoteOnly] 开始验证",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount字符串", amount.String(),
		"amount.Sign()", amount.Sign(),
		"amount.Cmp(-1)", amount.Cmp(big.NewInt(-1)),
		"amount.Cmp(0)", amount.Cmp(big.NewInt(0)))

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

	// 只进行验证，不更新状态（完整验证：检查余额和注册状态，且把已记账未生效的pending也算进已投）
	if err := d.validateVote(vote, false, false, true); err != nil {
		d.logger.Error("❌ Vote validation failed", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}

	d.logger.Debug("✅ Vote validation passed (no state update)")
	return nil
}

// validateVote 验证投票的有效性
// skipBalanceCheck: 是否跳过余额检查（用于交易处理时，余额可能已变化）
// skipRegistrationCheck: 是否跳过注册/候选人状态检查（用于边界应用时，状态可能已变化）
// includePendingInBalance: 余额检查时，是否把已记账但未生效的pending投票金额一起算进“已占用额度”（用于投票阶段防止同一epoch内重复超额）
func (d *DPoS) validateVote(vote *VoteMessage, skipBalanceCheck bool, skipRegistrationCheck bool, includePendingInBalance bool) error {
	// 🔍 调试日志：记录函数入口参数
	d.logger.Info("🔍 [validateVote] 开始验证",
		"amount字符串", vote.Amount.String(),
		"amount.Sign()", vote.Amount.Sign(),
		"amount.Cmp(-1)", vote.Amount.Cmp(big.NewInt(-1)),
		"amount.Cmp(0)", vote.Amount.Cmp(big.NewInt(0)),
		"skipBalanceCheck", skipBalanceCheck,
		"skipRegistrationCheck", skipRegistrationCheck)

	// 1. 检查投票金额边界：合法的 amount 只有 -1（撤销）或正数（投票）
	cmpMinusOne := vote.Amount.Cmp(big.NewInt(-1))
	if cmpMinusOne < 0 {
		return errors.New("vote amount cannot be less than -1")
	}

	cmpZero := vote.Amount.Cmp(big.NewInt(0))
	if cmpZero == 0 {
		return errors.New("vote amount cannot be zero")
	}

	// amount = -1 表示撤销，直接通过
	if cmpMinusOne == 0 {
		return nil
	}

	// amount > 0 时，检查最小/最大金额
	maxAmount, _ := new(big.Int).SetString(MaxVoteAmount, 10)
	cmpMax := vote.Amount.Cmp(maxAmount)
	if cmpMax > 0 {
		return errors.New("vote amount exceeds maximum")
	}

	minAmount, _ := new(big.Int).SetString(MinVoteAmount, 10)
	cmpMin := vote.Amount.Cmp(minAmount)
	if cmpMin < 0 {
		d.logger.Error("❌ [validateVote] 触发最小金额检查失败",
			"amount", vote.Amount.String(),
			"MinVoteAmount", MinVoteAmount,
			"注意：如果 amount = -1，不应该执行到这里！")
		return errors.New("vote amount below minimum")
	}

	// 2. 检查投票者VCITY代币余额和剩余可投票数（可选）
	if !skipBalanceCheck && d.balanceQuerier != nil {
		balance, err := d.balanceQuerier.GetNativeTokenBalance(vote.Voter)
		if err != nil {
			d.logger.Error("Failed to query voter balance",
				"voter", vote.Voter.String(),
				"error", err)
			return fmt.Errorf("failed to query voter balance: %w", err)
		}

		// 计算已生效的总投票金额（使用 VoterInfo.VotingPower）
		totalVotedAmount := d.calculateTotalVotedAmount(vote.Voter)

		// 可选：再把已记账但未生效的pending金额也算进“已占用额度”
		effectiveVoted := new(big.Int).Set(totalVotedAmount)
		var pendingAmount *big.Int
		if includePendingInBalance {
			pendingAmount = d.calculatePendingScheduledAmount(vote.Voter)
			effectiveVoted.Add(effectiveVoted, pendingAmount)
		} else {
			pendingAmount = big.NewInt(0)
		}

		// 计算剩余可投票数：余额 - (已生效 + pending)
		remainingVotableAmount := new(big.Int).Sub(balance, effectiveVoted)

		// 检查剩余可投票数是否足够
		if remainingVotableAmount.Cmp(vote.Amount) < 0 {
			d.logger.Warn("Insufficient remaining votable amount for vote",
				"voter", vote.Voter.String(),
				"required", vote.Amount.String(),
				"remaining", remainingVotableAmount.String(),
				"balance", balance.String(),
				"totalVoted", totalVotedAmount.String(),
				"pending", pendingAmount.String())
			return fmt.Errorf("insufficient remaining votable amount: required %s, remaining %s, balance %s, totalVoted %s, pending %s",
				vote.Amount.String(), remainingVotableAmount.String(), balance.String(), totalVotedAmount.String(), pendingAmount.String())
		}

		d.logger.Info("Vote balance and remaining amount check passed",
			"voter", vote.Voter.String(),
			"voteAmount", vote.Amount.String(),
			"balance", balance.String(),
			"totalVoted", totalVotedAmount.String(),
			"pending", pendingAmount.String(),
			"remaining", remainingVotableAmount.String())
	} else {
		d.logger.Warn("Balance querier not available, skipping balance check")
	}

	// 检查受托人是否已注册（创世验证者例外）
	if !skipRegistrationCheck {
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
	}

	d.logger.Info("✅ 受托人注册状态验证通过",
		"delegate", vote.Delegate.String(),
		"voter", vote.Voter.String())

	// 检查受托人是否为候选人状态（可以接受投票，创世验证者例外）
	if !skipRegistrationCheck {
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
	}

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
	// 验证者记录应该在边界应用时创建，而不是在投票时
	if !delegateExists {
		d.logger.Debug("⚠️ 受托人不在验证者集合中，将在边界应用时创建",
			"delegate", vote.Delegate.String(),
			"note", "投票已记录，将在epoch边界应用时创建验证者记录")
		// 不再调用 addDelegateSafely，避免创建零权重验证者记录
	}

	return nil
}

// calculatePendingScheduledAmount 计算已记账但未生效（Applied=false）的投票总金额
// 用于在投票阶段，将同一epoch内已提交但尚未边界应用的投票一并计入额度占用
func (d *DPoS) calculatePendingScheduledAmount(voter types.Address) *big.Int {
	if d.state == nil || d.state.StakeStore == nil {
		return big.NewInt(0)
	}

	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		d.logger.Warn("failed to get staking info for pending amount", "error", err)
		return big.NewInt(0)
	}

	total := big.NewInt(0)
	for _, si := range stakingInfos {
		if si == nil {
			continue
		}
		if si.Staker != voter {
			continue
		}
		if si.Applied {
			// 只统计未生效的记录
			continue
		}
		if si.Amount == nil || si.Amount.Sign() <= 0 {
			continue
		}
		total.Add(total, si.Amount)
	}

	return total
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
	// 此函数仅用于 P2P gossip 消息（当前未使用）
	// 如启用 P2P 投票，需实现 ECDSA 签名恢复验证
	_ = hash // 保留用于未来 P2P 签名验证

	// 检查时间戳防重放
	// 延迟应用（Applied=true）的投票已经通过链上交易验证，
	// 此处跳过时间戳检查，避免旧时间戳导致的误判。
	if !vote.Applied {
		now := uint64(time.Now().Unix())
		if vote.Timestamp < now-300 || vote.Timestamp > now+60 { // 5分钟时间窗口
			return errors.New("vote timestamp out of range")
		}
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
