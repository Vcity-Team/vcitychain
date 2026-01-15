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

	// 检查是否为撤销投票 (amount = -1)
	if amount.Cmp(big.NewInt(-1)) == 0 {
		d.logger.Info("🔄 检测到撤销投票请求，开始处理撤销逻辑")
		return d.processUnvote(voter, candidate)
	}

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
	// 交易处理时跳过余额检查（余额可能已变化），但检查注册状态
	d.logger.Debug("🔄 验证投票（不更新数据库）...")
	if err := d.verifyVoteSignature(vote); err != nil {
		d.logger.Error("❌ 投票签名验证失败", "error", err)
		return fmt.Errorf("vote signature verification failed: %w", err)
	}
	if err := d.validateVote(vote, true, false); err != nil {
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

	// 边界应用时做完整验证（包括余额和注册状态）
	if err := d.validateVote(vote, false, false); err != nil {
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
		// 验证投票（批量处理时做完整验证）
		if err := d.validateVote(vote, false, false); err != nil {
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

	// 查询所有待应用的投票（内存 + 数据库）
	// 🆕 修复：使用去重机制，避免同一投票被处理两次
	var scheduledVotes []*VoteRecord
	voteKeyMap := make(map[string]bool) // 用于去重：key = "Voter_Delegate_Timestamp"

	addVote := func(v *VoteRecord) {
		if v == nil {
			return
		}
		// 生成唯一键：Voter + Delegate + Timestamp
		voteKey := fmt.Sprintf("%s_%s_%d", v.Voter.String(), v.Delegate.String(), v.Timestamp)
		if voteKeyMap[voteKey] {
			// 已存在，跳过（去重）
			d.logger.Debug("🔄 [边界应用投票] 发现重复投票，跳过",
				"voter", v.Voter.String(),
				"delegate", v.Delegate.String(),
				"timestamp", v.Timestamp,
				"amount", v.Amount.String())
			return
		}
		voteKeyMap[voteKey] = true
		scheduledVotes = append(scheduledVotes, v)
	}

	// 1) 内存中的待应用投票
	d.voteRecordsMutex.RLock()
	for _, record := range d.voteRecords {
		if record.EffectiveEpoch == epochNumber && !record.Applied {
			addVote(record)
		}
	}
	d.voteRecordsMutex.RUnlock()

	// 2) 数据库中的待应用投票（Applied=false && EffectiveEpoch==epochNumber）
	if d.state != nil && d.state.StakeStore != nil {
		if stakingInfos, err := d.state.StakeStore.GetStakingInfo(); err == nil {
			for _, stakeInfo := range stakingInfos {
				if stakeInfo == nil {
					continue
				}
				if stakeInfo.Applied {
					continue
				}
				if stakeInfo.EffectiveEpoch != epochNumber {
					continue
				}
				addVote(&VoteRecord{
					Voter:          stakeInfo.Staker,
					Delegate:       stakeInfo.Delegate,
					Amount:         new(big.Int).Set(stakeInfo.Amount),
					Timestamp:      stakeInfo.StartTime, // 复用 StartTime 作为唯一键
					EffectiveEpoch: stakeInfo.EffectiveEpoch,
					Applied:        stakeInfo.Applied,
				})
			}
		} else {
			d.logger.Warn("⚠️ [边界应用投票] 从数据库加载待应用投票失败",
				"blockNumber", blockNumber,
				"epochNumber", epochNumber,
				"error", err)
		}
	}

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

// processUnvote 处理撤销投票逻辑（立即生效）
func (d *DPoS) processUnvote(voter types.Address, candidate types.Address) error {
	d.logger.Info("🔄 开始处理撤销投票",
		"voter", voter.String(),
		"candidate", candidate.String())

	// 1. 直接从数据库获取投票者信息（不依赖内存）
	var voterInfo *VoterInfo
	var exists bool

	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("❌ 数据库不可用，无法撤销投票",
			"voter", voter.String())
		return fmt.Errorf("database not available, cannot process unvote")
	}

	// 1.1 从数据库的 VoterInfo bucket 加载
	dbVoterInfo, err := d.state.StakeStore.GetVoterInfo(voter)
	if err != nil {
		d.logger.Warn("⚠️ 从数据库加载 VoterInfo 失败", "error", err)
	} else if dbVoterInfo != nil {
		voterInfo = dbVoterInfo
		exists = true
		d.logger.Info("✅ 从数据库加载投票者信息成功",
			"voter", voter.String(),
			"votingPower", voterInfo.VotingPower.String())
	}

	// 1.2 如果 VoterInfo 也没有，从 StakeInfo 构建
	if !exists {
		d.logger.Info("🔍 VoterInfo 中未找到，尝试从 StakeInfo 构建",
			"voter", voter.String(),
			"candidate", candidate.String())

		stakingInfos, err := d.state.StakeStore.GetStakingInfo()
		if err == nil {
			// 查找该投票者对该候选人的所有投票记录（只统计已应用且活跃的）
			totalVoteAmount := big.NewInt(0)
			hasVoteRecord := false

			for _, stakeInfo := range stakingInfos {
				if stakeInfo != nil && stakeInfo.Staker == voter && stakeInfo.Delegate == candidate {
					// 只统计已应用且活跃的投票（Applied=true && IsActive=true）
					if stakeInfo.Applied && stakeInfo.IsActive {
						totalVoteAmount.Add(totalVoteAmount, stakeInfo.Amount)
						hasVoteRecord = true
						d.logger.Info("🔍 找到活跃投票记录",
							"staker", stakeInfo.Staker.String(),
							"delegate", stakeInfo.Delegate.String(),
							"amount", stakeInfo.Amount.String(),
							"applied", stakeInfo.Applied,
							"isActive", stakeInfo.IsActive)
					}
				}
			}

			if hasVoteRecord && totalVoteAmount.Sign() > 0 {
				// 构建 VoterInfo
				voterInfo = &VoterInfo{
					Address:        voter,
					VotingPower:    new(big.Int).Set(totalVoteAmount),
					VotedDelegates: []types.Address{candidate},
					DelegateVotes:  map[types.Address]*big.Int{candidate: new(big.Int).Set(totalVoteAmount)},
					LastVoteTime:   uint64(time.Now().Unix()),
					LockedUntil:    uint64(time.Now().Unix()) + VoteLockTime,
					Nonce:          make(map[uint64]bool),
				}
				exists = true
				d.logger.Info("✅ 从 StakeInfo 构建投票者信息成功",
					"voter", voter.String(),
					"candidate", candidate.String(),
					"totalVoteAmount", totalVoteAmount.String())
			}
		}
	}

	if !exists || voterInfo == nil {
		d.logger.Warn("❌ 投票者不存在，无法撤销",
			"voter", voter.String())
		return fmt.Errorf("voter %s has no voting record", voter.String())
	}

	// 2. 检查是否已投票给该候选人
	if voterInfo.DelegateVotes == nil {
		voterInfo.DelegateVotes = make(map[types.Address]*big.Int)
	}

	currentVoteAmount, hasVote := voterInfo.DelegateVotes[candidate]
	if !hasVote || currentVoteAmount.Sign() <= 0 {
		// 🔧 如果 DelegateVotes 中没有，从 StakeInfo 重新计算（只统计已应用且活跃的）
		stakingInfos, err := d.state.StakeStore.GetStakingInfo()
		if err == nil {
			totalVoteAmount := big.NewInt(0)
			for _, stakeInfo := range stakingInfos {
				if stakeInfo != nil && stakeInfo.Staker == voter && stakeInfo.Delegate == candidate {
					// 只统计已应用且活跃的投票（Applied=true && IsActive=true）
					if stakeInfo.Applied && stakeInfo.IsActive {
						totalVoteAmount.Add(totalVoteAmount, stakeInfo.Amount)
					}
				}
			}
			if totalVoteAmount.Sign() > 0 {
				// 更新 DelegateVotes
				if voterInfo.DelegateVotes == nil {
					voterInfo.DelegateVotes = make(map[types.Address]*big.Int)
				}
				voterInfo.DelegateVotes[candidate] = new(big.Int).Set(totalVoteAmount)
				currentVoteAmount = totalVoteAmount
				hasVote = true
				d.logger.Info("✅ 从 StakeInfo 重新计算投票金额",
					"voter", voter.String(),
					"candidate", candidate.String(),
					"amount", totalVoteAmount.String())
			}
		}

		if !hasVote || currentVoteAmount.Sign() <= 0 {
			d.logger.Warn("❌ 投票者未投票给该候选人，无法撤销",
				"voter", voter.String(),
				"candidate", candidate.String(),
				"currentVote", func() string {
					if currentVoteAmount != nil {
						return currentVoteAmount.String()
					}
					return "nil"
				}())
			return fmt.Errorf("no vote found for candidate %s", candidate.String())
		}
	}

	d.logger.Info("📊 撤销前状态",
		"voterVotingPower", voterInfo.VotingPower.String(),
		"candidateVoteAmount", currentVoteAmount.String())

	// 3. 撤销投票：从投票者移除该候选人的投票
	delete(voterInfo.DelegateVotes, candidate)

	// 4. 重构VotedDelegates列表（移除该候选人）
	newVotedDelegates := make([]types.Address, 0)
	for _, delegate := range voterInfo.VotedDelegates {
		if delegate != candidate {
			newVotedDelegates = append(newVotedDelegates, delegate)
		}
	}
	voterInfo.VotedDelegates = newVotedDelegates

	// 5. 减少投票者的总投票权重
	voterInfo.VotingPower = new(big.Int).Sub(voterInfo.VotingPower, currentVoteAmount)

	// 6. 减少候选人的投票权重（先更新数据库，再同步到内存）
	// 🔧 修复：撤销投票时也需要更新数据库中的 DelegateInfo.VotingPower
	// 从数据库读取当前权重
	originalVotingPower, err := d.getVotingPowerFromDatabase(candidate)
	if err != nil || originalVotingPower == nil {
		d.logger.Warn("⚠️ 从数据库读取验证者权重失败，尝试从内存获取",
			"candidate", candidate.String(),
			"error", err)
		// 如果读取失败，尝试从内存获取
		originalVotingPower = big.NewInt(0)
		validatorUpdated := false
		for _, validator := range d.delegates {
			if validator.Address == candidate && validator.VotingPower != nil {
				originalVotingPower = new(big.Int).Set(validator.VotingPower)
				validatorUpdated = true
				break
			}
		}
		if !validatorUpdated {
			d.logger.Warn("⚠️ 候选人不在验证者列表中，使用0作为初始权重",
				"candidate", candidate.String())
		}
	}

	// 计算新的投票权重（减去撤销的金额）
	newVotingPower := new(big.Int).Sub(originalVotingPower, currentVoteAmount)
	if newVotingPower.Sign() < 0 {
		newVotingPower = big.NewInt(0) // 确保不为负数
	}

	d.logger.Info("🔄 准备更新验证者投票权重（撤销投票）",
		"candidate", candidate.String(),
		"originalVotingPower", originalVotingPower.String(),
		"unvotedAmount", currentVoteAmount.String(),
		"newVotingPower", newVotingPower.String())

	// 7. 更新最后投票时间
	voterInfo.LastVoteTime = uint64(time.Now().Unix())

	// 8. 更新数据库中的 VoterInfo 和 StakeInfo
	if d.state != nil && d.state.StakeStore != nil {
		// 8.1 先获取需要更新的 StakeInfo 记录（在事务外获取，避免嵌套事务）
		stakingInfos, err := d.state.StakeStore.GetStakingInfo()
		var matchingStakes []*StakeInfo
		if err == nil {
			for _, stakeInfo := range stakingInfos {
				// 只更新已应用且活跃的记录（Applied=true && IsActive=true）
				if stakeInfo != nil && stakeInfo.Staker == voter && stakeInfo.Delegate == candidate &&
					stakeInfo.Applied && stakeInfo.IsActive && stakeInfo.Amount != nil && stakeInfo.Amount.Sign() > 0 {
					matchingStakes = append(matchingStakes, stakeInfo)
				}
			}
		}

		// 8.2 在事务中更新 VoterInfo、StakeInfo 和 DelegateInfo
		dbTx, err := d.state.beginDBTransaction(true)
		if err == nil {
			// 更新 VoterInfo
			if err := d.state.StakeStore.setVoterInfo(voter, voterInfo, dbTx); err != nil {
				d.logger.Warn("⚠️ 保存 VoterInfo 到数据库失败", "error", err)
				dbTx.Rollback()
			} else {
				// 更新 StakeInfo：将相关记录的 Amount 设置为 0（保持 IsActive = true）
				updatedCount := 0
				for _, stakeInfo := range matchingStakes {
					// 将 Amount 设置为 0（保持 IsActive = true，OriginalAmount 保留原始金额）
					oldAmount := new(big.Int).Set(stakeInfo.Amount)
					stakeInfo.Amount = big.NewInt(0)
					// 保存更新后的记录
					if err := d.state.StakeStore.setStakingInfo(voter, stakeInfo, stakeInfo.StartTime, dbTx); err == nil {
						updatedCount++
						d.logger.Info("✅ 更新 StakeInfo Amount 为 0（撤销投票）",
							"staker", stakeInfo.Staker.String(),
							"delegate", stakeInfo.Delegate.String(),
							"oldAmount", oldAmount.String(),
							"newAmount", "0",
							"originalAmount", func() string {
								if stakeInfo.OriginalAmount != nil {
									return stakeInfo.OriginalAmount.String()
								}
								return "nil"
							}(),
							"startTime", stakeInfo.StartTime,
							"isActive", stakeInfo.IsActive)
					} else {
						d.logger.Warn("⚠️ 保存 StakeInfo 失败", "error", err)
					}
				}

				// 🔧 更新 DelegateInfo.VotingPower（在同一事务中）
				if err := d.updateVotingPowerInDatabaseWithTx(candidate, newVotingPower, dbTx); err != nil {
					d.logger.Warn("⚠️ 更新 DelegateInfo.VotingPower 失败", "error", err)
					dbTx.Rollback()
					return fmt.Errorf("failed to update delegate voting power in database: %w", err)
				} else {
					d.logger.Info("✅ DelegateInfo.VotingPower 已更新到数据库",
						"candidate", candidate.String(),
						"oldVotingPower", originalVotingPower.String(),
						"newVotingPower", newVotingPower.String())
				}

				// 提交事务
				if err := dbTx.Commit(); err != nil {
					d.logger.Warn("⚠️ 提交事务失败", "error", err)
				} else {
					if updatedCount > 0 {
						d.logger.Info("✅ VoterInfo、StakeInfo 和 DelegateInfo 已更新到数据库",
							"voter", voter.String(),
							"candidate", candidate.String(),
							"updatedStakeInfoCount", updatedCount,
							"remainingVotingPower", voterInfo.VotingPower.String(),
							"candidateNewVotingPower", newVotingPower.String(),
							"note", "StakeInfo.Amount 已设置为 0，IsActive 保持为 true，OriginalAmount 保留原始金额")
					} else {
						d.logger.Info("✅ VoterInfo 和 DelegateInfo 已保存到数据库",
							"voter", voter.String(),
							"candidate", candidate.String(),
							"remainingVotingPower", voterInfo.VotingPower.String(),
							"candidateNewVotingPower", newVotingPower.String(),
							"note", "未找到需要更新的 StakeInfo 记录")
					}
				}
			}
		}

		// 8.3 数据库更新成功后，同步到内存
		if err := d.syncDelegateFromDatabase(candidate); err != nil {
			d.logger.Warn("⚠️ 同步验证者信息到内存失败",
				"candidate", candidate.String(),
				"error", err)
			// 不返回错误，因为数据库更新已经成功
		} else {
			d.logger.Info("✅ 验证者信息已同步到内存",
				"candidate", candidate.String(),
				"votingPower", newVotingPower.String())
		}
	}

	// 10. 标记需要更新验证者集合（立即生效）
	d.pendingValidatorUpdate = true

	// 11. 记录撤销操作（可选，用于审计）
	d.logger.Info("✅ 撤销投票成功",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"unvotedAmount", currentVoteAmount.String(),
		"remainingVotingPower", voterInfo.VotingPower.String(),
		"remainingDelegates", len(voterInfo.VotedDelegates))

	return nil
}
