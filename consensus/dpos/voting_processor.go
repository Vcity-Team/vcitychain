package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// AddVote 添加投票到 DPoS 状态（供 JSON-RPC 调用）
func (d *DPoS) AddVote(voter types.Address, candidate types.Address, amount *big.Int) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.logger.Debug("Adding vote to DPoS state",
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

	// 处理投票（内部调用，不重复加锁）
	d.logger.Debug(" Calling processVoteInternal...")
	if err := d.processVoteInternal(vote); err != nil {
		d.logger.Error("❌ Failed to process vote", "error", err)
		return fmt.Errorf("failed to process vote: %w", err)
	}
	d.logger.Debug(" processVoteInternal completed successfully")

	d.logger.Debug(" Starting vote persistence to database...")
	if err := d.persistVoteToDatabase(voter, candidate, amount); err != nil {
		d.logger.Error("Failed to persist vote to database", "error", err)
	} else {
		d.logger.Debug("Vote successfully persisted to database",
			"voter", voter.String(),
			"candidate", candidate.String(),
			"amount", amount.String())
	}

	d.logger.Debug("Vote added successfully to DPoS state",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String())

	d.logger.Debug("🔄 投票完成，标记需要延迟更新验证者集合...")
	d.pendingValidatorUpdate = true
	d.lastVotedDelegate = candidate // 记录最后投票的验证者
	d.logger.Info(" 投票完成，验证者集合将在下一轮更新",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String(),
		"lastVotedDelegate", d.lastVotedDelegate.String())

	return nil
}

// processVoteInternal 内部投票处理方法（不加锁，由调用者负责）
func (d *DPoS) processVoteInternal(vote *VoteMessage) error {
	d.logger.Debug("processVoteInternal started",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"amountWei", vote.Amount.String(),
		"timestamp", time.Now().Unix())

	// 1. 安全校验
	d.logger.Debug("开始投票安全校验",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()))

	if err := d.validateVote(vote); err != nil {
		d.logger.Error("❌ Vote validation failed", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}

	d.logger.Debug("Vote validation passed")

	// 2. 签名校验
	if err := d.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("vote signature verification failed: %w", err)
	}

	// 3. 防重放攻击 - 检查nonce
	d.logger.Debug("🔍 Checking vote nonce",
		"voter", vote.Voter.String(),
		"round", vote.Round,
		"timestamp", vote.Timestamp)

	if err := d.checkVoteNonce(vote); err != nil {
		return fmt.Errorf("vote nonce check failed: %w", err)
	}

	// 4. 更新投票者信息
	voter, exists := d.voters[vote.Voter]
	if !exists {
		voter = &VoterInfo{
			Address:        vote.Voter,
			VotingPower:    big.NewInt(0),
			VotedDelegates: make([]types.Address, 0),
			LastVoteTime:   uint64(time.Now().Unix()),
			LockedUntil:    uint64(time.Now().Unix()) + VoteLockTime,
			Nonce:          make(map[uint64]bool), // 防重放
		}
		d.voters[vote.Voter] = voter
	}

	// 5. 更新投票权重
	voter.VotingPower = new(big.Int).Add(voter.VotingPower, vote.Amount)
	voter.LastVoteTime = uint64(time.Now().Unix())
	voter.LockedUntil = uint64(time.Now().Unix()) + VoteLockTime

	// 6. 添加受托人到投票列表（去重）
	found := false
	for _, del := range voter.VotedDelegates {
		if del == vote.Delegate {
			found = true
			break
		}
	}
	if !found {
		voter.VotedDelegates = append(voter.VotedDelegates, vote.Delegate)
	}

	// 7. 更新受托人的投票权重
	d.logger.Debug(" 准备更新受托人投票权重",
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()))

	originalVotingPower, err := d.getVotingPowerFromDatabase(vote.Delegate)
	if err != nil {
		d.logger.Error("❌ 从数据库读取验证者权重失败",
			"delegate", vote.Delegate.String(),
			"error", err)
		return fmt.Errorf("failed to get voting power from database: %w", err)
	}

	d.logger.Info("===== 投票权重更新详情 =====",
		"delegate", vote.Delegate.String(),
		"originalVotingPower", originalVotingPower.String(),
		"originalVotingPowerHex", fmt.Sprintf("0x%x", originalVotingPower.Bytes()),
		"addedAmount", vote.Amount.String(),
		"addedAmountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"expectedNewPower", new(big.Int).Add(originalVotingPower, vote.Amount).String(),
		"dataSource", "database")

	// 直接更新数据库，然后同步到内存
	newVotingPower := new(big.Int).Add(originalVotingPower, vote.Amount)
	err = d.updateVotingPowerInDatabase(vote.Delegate, newVotingPower)
	if err != nil {
		d.logger.Error("❌ 更新数据库验证者权重失败",
			"delegate", vote.Delegate.String(),
			"newPower", newVotingPower.String(),
			"error", err)
		return fmt.Errorf("failed to update voting power in database: %w", err)
	}

	// 数据库更新成功后，同步到内存
	err = d.syncDelegateFromDatabase(vote.Delegate)
	if err != nil {
		d.logger.Warn("⚠️ 同步验证者信息到内存失败",
			"delegate", vote.Delegate.String(),
			"error", err)
		// 不返回错误，因为数据库更新已经成功
	}

	d.logger.Info("===== 投票权重更新完成 =====",
		"delegate", vote.Delegate.String(),
		"originalVotingPower", originalVotingPower.String(),
		"finalVotingPower", newVotingPower.String(),
		"finalVotingPowerHex", fmt.Sprintf("0x%x", newVotingPower.Bytes()),
		"addedAmount", vote.Amount.String(),
		"calculation", fmt.Sprintf("%s + %s = %s", originalVotingPower.String(), vote.Amount.String(), newVotingPower.String()),
		"updateSuccess", true,
		"dataSource", "database")

	// 8. 记录nonce防止重放
	voter.Nonce[vote.Round] = true

	d.logger.Debug("✅ vote processed successfully",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"round", vote.Round)

	return nil
}

// processVote 增强的投票处理（外部调用，加锁版本）
func (d *DPoS) processVote(vote *VoteMessage) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	return d.processVoteInternal(vote)
}

// processVoteBatch 批量处理投票
func (d *DPoS) processVoteBatch(votes []*VoteMessage) {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 批量更新投票者信息
	voterUpdates := make(map[types.Address]*VoterInfo)
	delegateUpdates := make(map[types.Address]*big.Int)

	for _, vote := range votes {
		// 验证投票
		if err := d.validateVote(vote); err != nil {
			d.logger.Warn("invalid vote in batch", "error", err, "voter", vote.Voter)
			continue
		}

		// 更新投票者
		voter, exists := d.voters[vote.Voter]
		if !exists {
			voter = &VoterInfo{
				Address:        vote.Voter,
				VotingPower:    big.NewInt(0),
				VotedDelegates: make([]types.Address, 0),
				LastVoteTime:   uint64(time.Now().Unix()),
				LockedUntil:    uint64(time.Now().Unix()) + VoteLockTime,
				Nonce:          make(map[uint64]bool),
			}
			d.voters[vote.Voter] = voter
		}

		voter.VotingPower = new(big.Int).Add(voter.VotingPower, vote.Amount)
		voter.LastVoteTime = uint64(time.Now().Unix())
		voter.LockedUntil = uint64(time.Now().Unix()) + VoteLockTime
		voter.Nonce[vote.Round] = true

		// 添加受托人到投票列表（去重）
		found := false
		for _, del := range voter.VotedDelegates {
			if del == vote.Delegate {
				found = true
				break
			}
		}
		if !found {
			voter.VotedDelegates = append(voter.VotedDelegates, vote.Delegate)
		}

		voterUpdates[vote.Voter] = voter

		// 累计受托人更新
		if delegateUpdates[vote.Delegate] == nil {
			delegateUpdates[vote.Delegate] = big.NewInt(0)
		}
		delegateUpdates[vote.Delegate].Add(delegateUpdates[vote.Delegate], vote.Amount)
	}

	// 批量更新受托人投票权重
	for delegate, amount := range delegateUpdates {
		for _, del := range d.delegates {
			if del.Address == delegate {
				del.VotingPower = new(big.Int).Add(del.VotingPower, amount)
				break
			}
		}
	}

	// 更新缓存
	d.updateCache(voterUpdates)

	// 更新指标
	d.metrics.lock.Lock()
	d.metrics.TotalVotes += uint64(len(votes))
	d.metrics.ActiveVoters = uint64(len(d.voters))
	d.metrics.lock.Unlock()

	d.logger.Debug("processed vote batch", "count", len(votes))
}
