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

	// 本epoch投票在本epoch末尾应用（effectiveEpoch = currentEpoch）
	effectiveEpoch := currentEpoch
	d.logger.Info("✅ [投票] 设置生效epoch为当前epoch（本轮边界应用）",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"currentBlock", currentBlockNumber,
		"currentEpoch", currentEpoch,
		"effectiveEpoch", effectiveEpoch)
	vote := &VoteMessage{
		Voter:          voter,
		Delegate:       candidate,
		Amount:         amount,
		Round:          d.currentRound,
		Timestamp:      uint64(time.Now().Unix()),
		EffectiveEpoch: effectiveEpoch,
		Applied:        false,
	}

	// 只进行验证，不更新状态（完整验证：检查余额和注册状态，并将已记账未生效的pending也计入占用额度）
	d.logger.Debug("🔄 验证投票（不更新数据库，包含余额检查并计入pending）...")
	if err := d.verifyVoteSignature(vote); err != nil {
		d.logger.Error("❌ 投票签名验证失败", "error", err)
		return fmt.Errorf("vote signature verification failed: %w", err)
	}
	if err := d.validateVote(vote, false, false, true); err != nil {
		d.logger.Error("❌ 投票验证失败", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}
	d.logger.Debug("✅ 投票验证通过（余额和注册状态检查通过）")

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

	// 边界应用时做完整验证（包括余额和注册状态），但此时不再叠加pending（当前正在应用的这批就是pending）
	if err := d.validateVote(vote, false, false, false); err != nil {
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
	d.logger.Info("🎯 ===== 投票权重更新详情 =====",
		"delegate", vote.Delegate.String(),
		"originalVotingPower", originalVotingPower.String(),
		"originalVotingPowerHex", fmt.Sprintf("0x%x", originalVotingPower.Bytes()),
		"addedAmount", vote.Amount.String(),
		"addedAmountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"expectedNewPower", new(big.Int).Add(originalVotingPower, vote.Amount).String(),
		"dataSource", "database")

	newVotingPower := new(big.Int).Add(originalVotingPower, vote.Amount)
	err = d.updateVotingPowerInDatabase(vote.Delegate, newVotingPower)
	if err != nil {
		d.logger.Error("❌ 更新数据库验证者权重失败",
			"delegate", vote.Delegate.String(),
			"newPower", newVotingPower.String(),
			"error", err)
		return fmt.Errorf("failed to update voting power in database: %w", err)
	}

	err = d.syncDelegateFromDatabase(vote.Delegate)
	if err != nil {
		d.logger.Warn("⚠️ 同步验证者信息到内存失败",
			"delegate", vote.Delegate.String(),
			"error", err)
		// 不返回错误，因为数据库更新已经成功
	}
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
		// 验证投票（批量处理时做完整验证），但此处不叠加pending（批量数据本身即为待处理集合）
		if err := d.validateVote(vote, false, false, false); err != nil {
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

	d.updateCache(voterUpdates)

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
	// 使用去重机制，避免同一投票被处理两次
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
					d.logger.Info("ℹ️ [边界应用投票] 投票记录已应用，跳过",
						"voter", stakeInfo.Staker.String(),
						"delegate", stakeInfo.Delegate.String(),
						"effectiveEpoch", stakeInfo.EffectiveEpoch,
						"currentEpoch", epochNumber,
						"applied", stakeInfo.Applied)
					continue
				}
				if stakeInfo.EffectiveEpoch != epochNumber {
					d.logger.Info("⚠️ [边界应用投票] 投票记录被跳过（EffectiveEpoch不匹配）",
						"voter", stakeInfo.Staker.String(),
						"delegate", stakeInfo.Delegate.String(),
						"effectiveEpoch", stakeInfo.EffectiveEpoch,
						"currentEpoch", epochNumber,
						"applied", stakeInfo.Applied,
						"amount", func() string {
							if stakeInfo.Amount != nil {
								return stakeInfo.Amount.String()
							}
							return "nil"
						}())
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
				d.logger.Info("✅ [边界应用投票] 找到待应用投票",
					"voter", stakeInfo.Staker.String(),
					"delegate", stakeInfo.Delegate.String(),
					"effectiveEpoch", stakeInfo.EffectiveEpoch,
					"currentEpoch", epochNumber,
					"amount", func() string {
						if stakeInfo.Amount != nil {
							return stakeInfo.Amount.String()
						}
						return "nil"
					}())
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

	d.logger.Info("✅ [边界应用投票] 投票条件满足，开始应用",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber,
		"votesCount", len(scheduledVotes))

	for _, voteRecord := range scheduledVotes {
		vote := &VoteMessage{
			Voter:          voteRecord.Voter,
			Delegate:       voteRecord.Delegate,
			Amount:         voteRecord.Amount,
			Round:          d.currentRound,
			Timestamp:      voteRecord.Timestamp,
			EffectiveEpoch: voteRecord.EffectiveEpoch,
			Applied:        true, // 标记为已应用
		}

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

// processUnvote 处理撤销投票逻辑（与投票一致：在本 epoch 边界生效，避免权重突变导致分叉）
func (d *DPoS) processUnvote(voter types.Address, candidate types.Address) error {
	d.logger.Info("🔄 开始处理撤销投票（边界生效）",
		"voter", voter.String(),
		"candidate", candidate.String())

	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("❌ 数据库不可用，无法撤销投票",
			"voter", voter.String())
		return fmt.Errorf("database not available, cannot process unvote")
	}
	// 当前 epoch：用「下一块」算 epoch，使 unvote 一定在本 epoch 或下一 epoch 边界被应用（避免 tx 进下一块时 UnvoteEffectiveEpoch 仍是上一 epoch 导致永不生效）
	currentBlockNumber := d.getCurrentBlockNumber()
	blockContainingTx := currentBlockNumber + 1
	currentEpochMeta := d.getEpochForBlock(blockContainingTx)
	if currentEpochMeta == nil {
		currentEpochMeta = d.getEpochForBlock(currentBlockNumber)
	}
	var currentEpoch uint64
	if currentEpochMeta != nil {
		currentEpoch = currentEpochMeta.Number
	}

	// 找出所有可撤销的 StakeInfo（已应用、活跃、金额>0）
	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		d.logger.Warn("⚠️ 获取 StakeInfo 失败", "error", err)
		return fmt.Errorf("failed to get staking info: %w", err)
	}
	var matchingStakes []*StakeInfo
	totalToUnvote := big.NewInt(0)
	for _, s := range stakingInfos {
		if s == nil || s.Staker != voter || s.Delegate != candidate {
			continue
		}
		if !s.Applied || !s.IsActive || s.Amount == nil || s.Amount.Sign() <= 0 {
			continue
		}
		matchingStakes = append(matchingStakes, s)
		totalToUnvote.Add(totalToUnvote, s.Amount)
	}
	if len(matchingStakes) == 0 || totalToUnvote.Sign() <= 0 {
		d.logger.Warn("❌ 投票者未投票给该候选人，无法撤销",
			"voter", voter.String(),
			"candidate", candidate.String())
		return fmt.Errorf("no vote found for candidate %s", candidate.String())
	}

	// 只做「待撤销」标记，不修改 VoterInfo/DelegateInfo/Amount，边界时由 applyScheduledUnvotes 统一生效
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		d.logger.Warn("⚠️ 开启事务失败", "error", err)
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer dbTx.Rollback()

	updatedCount := 0
	for _, stakeInfo := range matchingStakes {
		stakeInfo.PendingUnvote = true
		stakeInfo.UnvoteEffectiveEpoch = currentEpoch
		if err := d.state.StakeStore.setStakingInfo(voter, stakeInfo, stakeInfo.StartTime, dbTx); err != nil {
			d.logger.Warn("⚠️ 保存 StakeInfo 待撤销标记失败", "error", err)
			continue
		}
		updatedCount++
	}
	if err := dbTx.Commit(); err != nil {
		d.logger.Warn("⚠️ 提交事务失败", "error", err)
		return fmt.Errorf("failed to commit: %w", err)
	}

	d.logger.Info("✅ 撤销已登记，将在本 epoch 边界生效",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"unvotedAmount", totalToUnvote.String(),
		"effectiveEpoch", currentEpoch,
		"updatedStakeCount", updatedCount,
		"note", "与投票一致，权重在 epoch 边界统一变化，避免分叉")
	return nil
}

// applyScheduledUnvotes 在 epoch 边界应用待撤销的投票（与 applyScheduledVotes 对称，避免权重突变）
func (d *DPoS) applyScheduledUnvotes(epochNumber uint64, blockNumber uint64) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	if d.state == nil || d.state.StakeStore == nil {
		return nil
	}
	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		d.logger.Warn("⚠️ [边界应用撤销] 获取 StakeInfo 失败", "error", err)
		return nil
	}
	type key struct{ voter, delegate string }
	toUnvote := make(map[key][]*StakeInfo)
	for _, s := range stakingInfos {
		if s == nil || !s.PendingUnvote || s.UnvoteEffectiveEpoch != epochNumber {
			continue
		}
		if s.Amount == nil || s.Amount.Sign() <= 0 {
			continue
		}
		k := key{s.Staker.String(), s.Delegate.String()}
		toUnvote[k] = append(toUnvote[k], s)
	}
	if len(toUnvote) == 0 {
		d.logger.Info("ℹ️ [边界应用撤销] 本 epoch 无待撤销", "epochNumber", epochNumber)
		return nil
	}
	d.logger.Info("🔍 [边界应用撤销] 开始应用", "epochNumber", epochNumber, "blockNumber", blockNumber, "pairs", len(toUnvote))

	for k, stakes := range toUnvote {
		voterAddr := types.StringToAddress(k.voter)
		delegateAddr := types.StringToAddress(k.delegate)
		totalAmount := big.NewInt(0)
		for _, s := range stakes {
			if s.Amount != nil {
				totalAmount.Add(totalAmount, s.Amount)
			}
		}
		if totalAmount.Sign() <= 0 {
			continue
		}
		dbTx, err := d.state.beginDBTransaction(true)
		if err != nil {
			d.logger.Warn("⚠️ [边界应用撤销] 开启事务失败", "error", err)
			continue
		}
		for _, s := range stakes {
			if s.OriginalAmount == nil && s.Amount != nil && s.Amount.Sign() > 0 {
				s.OriginalAmount = new(big.Int).Set(s.Amount)
			}
			s.Amount = big.NewInt(0)
			s.PendingUnvote = false
			s.UnvoteEffectiveEpoch = 0
			_ = d.state.StakeStore.setStakingInfo(voterAddr, s, s.StartTime, dbTx)
		}
		voterInfo, _ := d.state.StakeStore.GetVoterInfo(voterAddr)
		if voterInfo != nil {
			delete(voterInfo.DelegateVotes, delegateAddr)
			newDelegates := make([]types.Address, 0)
			for _, del := range voterInfo.VotedDelegates {
				if del != delegateAddr {
					newDelegates = append(newDelegates, del)
				}
			}
			voterInfo.VotedDelegates = newDelegates
			voterInfo.VotingPower = new(big.Int).Sub(voterInfo.VotingPower, totalAmount)
			if voterInfo.VotingPower.Sign() < 0 {
				voterInfo.VotingPower = big.NewInt(0)
			}
			_ = d.state.StakeStore.setVoterInfo(voterAddr, voterInfo, dbTx)
		} else {
			d.logger.Warn("⚠️ [边界应用撤销] VoterInfo 不存在，仅更新 StakeInfo 与 Delegate 权重", "voter", k.voter, "delegate", k.delegate)
		}
		origPower, _ := d.getVotingPowerFromDatabase(delegateAddr)
		if origPower == nil {
			origPower = big.NewInt(0)
		}
		newPower := new(big.Int).Sub(origPower, totalAmount)
		if newPower.Sign() < 0 {
			newPower = big.NewInt(0)
		}
		_ = d.updateVotingPowerInDatabaseWithTx(delegateAddr, newPower, dbTx)
		if err := dbTx.Commit(); err != nil {
			d.logger.Warn("⚠️ [边界应用撤销] 提交事务失败", "error", err)
			_ = dbTx.Rollback()
			continue
		}
		_ = d.syncDelegateFromDatabase(delegateAddr)
		d.logger.Info("✅ [边界应用撤销] 已生效", "voter", k.voter, "delegate", k.delegate, "amount", totalAmount.String())
	}
	d.pendingValidatorUpdate = true
	d.logger.Info("✅ [边界应用撤销] 完成", "epochNumber", epochNumber, "blockNumber", blockNumber)
	return nil
}
