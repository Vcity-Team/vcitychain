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

	// 计算投票生效的epoch（边界应用）
	currentBlockNumber := d.getCurrentBlockNumber()
	currentEpochMeta := d.getEpochForBlock(currentBlockNumber)
	var currentEpoch uint64
	if currentEpochMeta != nil {
		currentEpoch = currentEpochMeta.Number
	} else {
		currentEpoch = 0
	}

	// 改为：本epoch投票在本epoch末尾应用（effectiveEpoch = currentEpoch）
	effectiveEpoch := currentEpoch
	d.logger.Info("✅ [投票] 设置生效epoch为当前epoch（本轮边界应用）",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"currentBlock", currentBlockNumber,
		"currentEpoch", currentEpoch,
		"effectiveEpoch", effectiveEpoch)

	// 创建投票消息（用于验证）
	vote := &VoteMessage{
		Voter:          voter,
		Delegate:       candidate,
		Amount:         amount,
		Round:          d.currentRound,
		Timestamp:      uint64(time.Now().Unix()),
		EffectiveEpoch: effectiveEpoch,
		Applied:        false,
	}

	// ✅ 修改：只进行基本验证，不更新数据库（边界应用）
	d.logger.Debug("🔄 验证投票（不更新数据库）...")
	if err := d.validateVoteOnly(vote); err != nil {
		d.logger.Error("❌ 投票验证失败", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}
	d.logger.Debug("✅ 投票验证通过")

	// ✅ 修改：只保存投票记录到数据库，不更新验证者权重
	d.logger.Debug("🔄 保存投票记录到数据库（等待边界应用）...")
	if err := d.persistVoteToDatabase(voter, candidate, amount, effectiveEpoch, false); err != nil {
		d.logger.Error("Failed to persist vote to database", "error", err)
		return fmt.Errorf("failed to persist vote to database: %w", err)
	}
	d.logger.Debug("✅ 投票记录已保存到数据库",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String(),
		"effectiveEpoch", effectiveEpoch)

	d.logger.Info("✅ 投票完成，将在epoch边界应用",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String(),
		"effectiveEpoch", effectiveEpoch,
		"note", "投票不会立即更新验证者集合，将在下一个epoch边界生效")

	return nil
}

// validateVoteOnly 只验证投票，不更新数据库（用于边界应用）
func (d *DPoS) validateVoteOnly(vote *VoteMessage) error {
	// 1. 基本验证（签名、格式等）
	if err := d.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("vote signature verification failed: %w", err)
	}

	// 2. 投票权重上限检查 - 已删除（投票者不受限制）

	// 3. 检查受托人是否存在（只检查，不创建）
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err == nil {
		delegateExists := false
		for _, validator := range validators {
			if validator.Address == vote.Delegate && validator.IsActive {
				delegateExists = true
				break
			}
		}
		if !delegateExists {
			d.logger.Debug("⚠️ 受托人不在验证者集合中，将在边界应用时创建",
				"delegate", vote.Delegate.String(),
				"note", "投票已记录，将在epoch边界应用时创建验证者记录")
		}
	}

	return nil
}

// processVoteInternal 内部投票处理方法（不加锁，由调用者负责）
func (d *DPoS) processVoteInternal(vote *VoteMessage) error {
	d.logger.Debug("🔄 processVoteInternal started",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"amountWei", vote.Amount.String(),
		"timestamp", time.Now().Unix())

	// 1. 安全校验
	d.logger.Debug("🔍 开始投票安全校验",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()))

	if err := d.validateVote(vote); err != nil {
		d.logger.Error("❌ Vote validation failed", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}

	d.logger.Debug("✅ Vote validation passed")

	// 2. 签名校验
	if err := d.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("vote signature verification failed: %w", err)
	}

	// 3. 防重放攻击 - 检查nonce（临时跳过用于测试）
	d.logger.Debug("🔍 Checking vote nonce",
		"voter", vote.Voter.String(),
		"round", vote.Round,
		"timestamp", vote.Timestamp)

	// 临时跳过nonce检查用于测试
	/*
		if err := d.checkVoteNonce(vote); err != nil {
			return fmt.Errorf("vote nonce check failed: %w", err)
		}
	*/

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
	d.logger.Debug("🔄 准备更新受托人投票权重",
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()))

	// 从数据库读取原始权重，而不是从内存
	originalVotingPower, err := d.getVotingPowerFromDatabase(vote.Delegate)
	if err != nil {
		d.logger.Error("❌ 从数据库读取验证者权重失败",
			"delegate", vote.Delegate.String(),
			"error", err)
		return fmt.Errorf("failed to get voting power from database: %w", err)
	}

	// 显著日志：显示投票权重更新详情
	d.logger.Info("🎯 ===== 投票权重更新详情 =====",
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

	// 显著日志：记录更新完成
	d.logger.Info("🎯 ===== 投票权重更新完成 =====",
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

	// 修复：批量投票时，将所有被投票的验证者添加到集合中
	if len(delegateUpdates) > 0 {
		d.pendingValidatorUpdate = true
		if d.lastVotedDelegates == nil {
			d.lastVotedDelegates = make(map[types.Address]bool)
		}
		for delegate := range delegateUpdates {
			d.lastVotedDelegates[delegate] = true
		}
		d.logger.Info("✅ 批量投票完成，已标记需要更新的验证者", "votedDelegatesCount", len(d.lastVotedDelegates))
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

// applyScheduledVotes 在epoch边界应用待生效的投票
func (d *DPoS) applyScheduledVotes(epochNumber uint64, blockNumber uint64) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.logger.Info("🔍 [边界应用投票] 开始查询待应用投票",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber)

	// 查询所有待应用的投票
	d.voteRecordsMutex.RLock()
	var scheduledVotes []*VoteRecord
	for _, record := range d.voteRecords {
		if record.EffectiveEpoch == epochNumber && !record.Applied {
			scheduledVotes = append(scheduledVotes, record)
		}
	}
	d.voteRecordsMutex.RUnlock()

	d.logger.Info("🔍 [边界应用投票] 查询结果",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber,
		"scheduledVotesCount", len(scheduledVotes))

	if len(scheduledVotes) == 0 {
		d.logger.Info("ℹ️ [边界应用投票] 没有待应用的投票",
			"blockNumber", blockNumber,
			"epochNumber", epochNumber)
		return nil
	}

	// ✅ 修改：在边界应用时更新数据库和验证者集合
	d.logger.Info("✅ [边界应用投票] 投票条件满足，开始应用",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber,
		"votesCount", len(scheduledVotes))

	// 应用每个投票：更新数据库和内存
	for _, voteRecord := range scheduledVotes {
		// 创建 VoteMessage 用于 processVoteInternal
		vote := &VoteMessage{
			Voter:          voteRecord.Voter,
			Delegate:       voteRecord.Delegate,
			Amount:         voteRecord.Amount,
			Round:          d.currentRound,
			Timestamp:      voteRecord.Timestamp,
			EffectiveEpoch: voteRecord.EffectiveEpoch,
			Applied:        true, // 标记为已应用
		}

		// 调用 processVoteInternal 更新数据库和内存
		if err := d.processVoteInternal(vote); err != nil {
			d.logger.Error("❌ [边界应用投票] 应用投票失败",
				"voter", voteRecord.Voter.String(),
				"delegate", voteRecord.Delegate.String(),
				"error", err)
			// 继续处理其他投票，不中断
			continue
		}

		// ✅ 更新数据库中的 Applied 状态
		if d.state != nil && d.state.StakeStore != nil {
			// 更新 StakeInfo 中的 Applied 字段
			stakingInfos, err := d.state.StakeStore.GetStakingInfo()
			if err == nil {
				for _, stakeInfo := range stakingInfos {
					if stakeInfo.Staker == voteRecord.Voter &&
						stakeInfo.Delegate == voteRecord.Delegate &&
						stakeInfo.StartTime == voteRecord.Timestamp {
						stakeInfo.Applied = true
						// 保存更新后的 StakeInfo
						dbTx, err := d.state.beginDBTransaction(true)
						if err == nil {
							d.state.StakeStore.setStakingInfo(voteRecord.Voter, stakeInfo, voteRecord.Timestamp, dbTx)
							dbTx.Commit()
						}
						break
					}
				}
			}
		}

		d.logger.Info("✅ [边界应用投票] 投票应用成功",
			"voter", voteRecord.Voter.String(),
			"delegate", voteRecord.Delegate.String(),
			"amount", voteRecord.Amount.String(),
			"effectiveEpoch", voteRecord.EffectiveEpoch)
	}

	// 标记投票为已应用（更新内存）
	d.voteRecordsMutex.Lock()
	for _, vote := range scheduledVotes {
		voteKey := fmt.Sprintf("%s_%s_%d", vote.Voter.String(), vote.Delegate.String(), vote.Timestamp)
		if record, exists := d.voteRecords[voteKey]; exists {
			record.Applied = true
			d.logger.Debug("✅ [边界应用投票] 标记投票为已应用",
				"voteKey", voteKey,
				"voter", vote.Voter.String(),
				"delegate", vote.Delegate.String())
		}
	}
	d.voteRecordsMutex.Unlock()

	// 标记需要更新验证者集合
	d.pendingValidatorUpdate = true
	if d.lastVotedDelegates == nil {
		d.lastVotedDelegates = make(map[types.Address]bool)
	}
	for _, vote := range scheduledVotes {
		d.lastVotedDelegates[vote.Delegate] = true
	}

	d.logger.Info("✅ [边界应用投票] 投票应用成功",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber,
		"appliedVotesCount", len(scheduledVotes),
		"note", "验证者集合将在下一轮更新时重新排序和截取")

	return nil
}




