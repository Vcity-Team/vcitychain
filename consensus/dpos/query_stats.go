package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// GetFreezeInfo 获取冻结信息（智能接口，支持单个和批量）
func (d *DPoS) GetFreezeInfo(address types.Address) (*FreezeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 获取冻结信息
	var freezeInfo *FreezeInfo
	var err error
	if d.state != nil && d.state.FreezeStore != nil {
		freezeInfo, err = d.state.FreezeStore.GetFreezeInfo(address)
		if err != nil {
			return nil, fmt.Errorf("failed to get freeze info: %w", err)
		}
	}

	// 如果没有冻结信息，返回空信息
	if freezeInfo == nil {
		return &FreezeInfo{
			Address:             address,
			FrozenAmount:        big.NewInt(0),
			FrozenAt:            0,
			UnfreezeAt:          0,
			UnfreezeAvailableAt: 0,
			Status:              "none",
		}, nil
	}

	// 更新状态（根据当前时间）
	currentTime := uint64(time.Now().Unix())
	if freezeInfo.UnfreezeAvailableAt > 0 && currentTime >= freezeInfo.UnfreezeAvailableAt {
		freezeInfo.Status = "withdrawn"
	} else if freezeInfo.UnfreezeAt > 0 {
		freezeInfo.Status = "unfreezing"
	} else {
		freezeInfo.Status = "frozen"
	}

	return freezeInfo, nil
}

// GetAccountBalance 获取账户余额（包含冻结）
func (d *DPoS) GetAccountBalance(address types.Address) (map[string]interface{}, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 获取可用余额（通过余额查询器）
	availableBalance := big.NewInt(0)
	if d.balanceQuerier != nil {
		balance, err := d.balanceQuerier.GetNativeTokenBalance(address)
		if err == nil {
			availableBalance = balance
		} else {
			d.logger.Warn("⚠️ [GetAccountBalance] 余额查询失败",
				"address", address.String(),
				"error", err.Error())
		}
	} else {
		d.logger.Warn("⚠️ [GetAccountBalance] balanceQuerier 为 nil，无法查询余额",
			"address", address.String(),
			"note", "availableBalance 将返回 0")
	}

	// 获取冻结余额
	frozenBalance := big.NewInt(0)
	if d.state != nil && d.state.FreezeStore != nil {
		freezeInfo, err := d.state.FreezeStore.GetFreezeInfo(address)
		if err == nil && freezeInfo != nil {
			frozenBalance = freezeInfo.FrozenAmount
		} else {
			d.logger.Debug("ℹ️ [GetAccountBalance] 冻结余额查询失败或为空",
				"address", address.String(),
				"error", err)
		}
	} else {
		d.logger.Debug("ℹ️ [GetAccountBalance] FreezeStore 不可用",
			"address", address.String(),
			"stateIsNil", d.state == nil,
			"freezeStoreIsNil", d.state != nil && d.state.FreezeStore == nil)
	}

	// 计算总余额
	totalBalance := new(big.Int).Add(availableBalance, frozenBalance)

	return map[string]interface{}{
		"address":          address.String(),
		"availableBalance": availableBalance.String(),
		"frozenBalance":    frozenBalance.String(),
		"totalBalance":     totalBalance.String(),
	}, nil
}

// CanWithdrawDelegate 检查是否可以退出注册
func (d *DPoS) CanWithdrawDelegate(address types.Address) (map[string]interface{}, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	result := map[string]interface{}{
		"address":     address.String(),
		"canWithdraw": false,
		"reason":      "",
		"details":     make(map[string]interface{}),
	}

	// 获取注册信息
	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil || reg == nil {
		result["canWithdraw"] = false
		result["reason"] = "delegate_not_found"
		return result, nil
	}

	details := make(map[string]interface{})
	details["hasFrozenAmount"] = reg.Deposit.Cmp(big.NewInt(0)) > 0
	details["frozenAmount"] = reg.Deposit.String()

	// 检查投票
	hasVotes := reg.TotalVotes.Cmp(big.NewInt(0)) > 0
	details["hasVotes"] = hasVotes
	details["totalVotes"] = reg.TotalVotes.String()

	if hasVotes {
		result["canWithdraw"] = false
		result["reason"] = "has_active_votes"
		result["details"] = details
		return result, nil
	}

	// 检查最小冻结期
	currentTime := uint64(time.Now().Unix())
	minFreezePeriod := d.config.MinFreezePeriod
	if minFreezePeriod == 0 {
		minFreezePeriod = 604800 // 默认7天
	}

	if reg.FrozenAt > 0 {
		elapsedTime := currentTime - reg.FrozenAt
		details["frozenAt"] = reg.FrozenAt
		details["elapsedTime"] = elapsedTime
		details["minFreezePeriod"] = minFreezePeriod
		details["remainingTime"] = uint64(0)
		if elapsedTime < minFreezePeriod {
			remainingTime := minFreezePeriod - elapsedTime
			details["remainingTime"] = remainingTime
			result["canWithdraw"] = false
			result["reason"] = "min_freeze_period_not_met"
			result["details"] = details
			return result, nil
		}
	}

	// 可以退出
	result["canWithdraw"] = true
	result["reason"] = ""
	result["details"] = details
	return result, nil
}

// ==================== DPoS经济系统JSON-RPC查询方法 ====================

// GetEpochInfoForBlock 获取指定区块的 epoch 信息（供 blockchain 模块调用）
func (d *DPoS) GetEpochInfoForBlock(blockNumber uint64) map[string]interface{} {
	consensusSwitchHeight := d.config.ConsensusSwitchHeight
	epochSize := d.getEpochSize()

	var epochNumber uint64
	var firstBlockInEpoch uint64

	if blockNumber < consensusSwitchHeight {
		// 在共识切换之前，epoch为0
		epochNumber = 0
		firstBlockInEpoch = 0
	} else {
		// 计算DPoS epoch：从共识切换高度开始
		dposBlockNumber := blockNumber - consensusSwitchHeight
		epochNumber = (dposBlockNumber / epochSize) + 1
		firstBlockInEpoch = consensusSwitchHeight + (epochNumber-1)*epochSize
	}

	lastBlockInEpoch := firstBlockInEpoch + epochSize - 1

	// 计算剩余区块数
	remainingBlocks := int64(0)
	if blockNumber < lastBlockInEpoch {
		remainingBlocks = int64(lastBlockInEpoch) - int64(blockNumber)
	}

	// 计算剩余时间
	blockTime := d.config.BlockTime.Duration
	if blockTime == 0 {
		return nil // blockTime 配置错误
	}
	timeRemaining := time.Duration(remainingBlocks) * blockTime

	return map[string]interface{}{
		"epochNumber":     epochNumber,
		"remainingBlocks": remainingBlocks,
		"timeRemaining":   timeRemaining.String(),
	}
}

// GetCurrentEpochInfo 获取当前Epoch信息（公共方法，供外部调用）
func (d *DPoS) GetCurrentEpochInfo() map[string]interface{} {
	if d.query == nil {
		return map[string]interface{}{
			"error": "query manager not initialized",
		}
	}
	return d.query.GetCurrentEpochInfo()
}

// GetEpochInfoByNumber 获取指定Epoch信息（公共方法，供外部调用）
func (d *DPoS) GetEpochInfoByNumber(epochNumber uint64) map[string]interface{} {
	if d.query == nil {
		return map[string]interface{}{
			"error": "query manager not initialized",
		}
	}
	return d.query.GetEpochInfoByNumber(epochNumber)
}

// GetValidatorBlockStats 获取验证者出块统计
func (d *DPoS) GetValidatorBlockStats(validatorAddress types.Address, epochNumber uint64) map[string]interface{} {
	if d.blockTracker == nil {
		return map[string]interface{}{
			"error": "block tracker not initialized",
		}
	}

	// 获取出块统计
	blockCounts := d.blockTracker.GetEpochBlockCounts(epochNumber)
	totalBlocks := d.blockTracker.GetTotalEpochBlocks(epochNumber)

	blocksProduced := blockCounts[validatorAddress]
	blockPercentage := 0.0
	if totalBlocks > 0 {
		blockPercentage = float64(blocksProduced) / float64(totalBlocks) * 100
	}

	// 获取验证者信息
	validators := d.getAllValidators()
	var votingPower string = "0"
	var isActive bool = false

	for _, validator := range validators {
		if validator.Address == validatorAddress {
			votingPower = validator.VotingPower.String()
			isActive = validator.IsActive
			break
		}
	}

	return map[string]interface{}{
		"validatorAddress": validatorAddress.String(),
		"epochNumber":      epochNumber,
		"blocksProduced":   blocksProduced,
		"totalEpochBlocks": totalBlocks,
		"blockPercentage":  blockPercentage,
		"isActive":         isActive,
		"votingPower":      votingPower,
	}
}

// GetValidatorRewardsInfo 获取验证者奖励信息
func (d *DPoS) GetValidatorRewardsInfo(validatorAddress types.Address, epochNumber uint64) map[string]interface{} {
	// 添加方法开始的调试日志
	d.logger.Debug("🔍 GetValidatorRewardsInfo开始",
		"validatorAddress", validatorAddress.String(),
		"epochNumber", epochNumber)

	if d.rewardDistributor == nil {
		d.logger.Debug("❌ rewardDistributor为nil")
		return map[string]interface{}{
			"error": "reward distributor not initialized",
		}
	}

	// 添加配置值的调试日志
	d.logger.Debug("🔍 DPoS配置值调试",
		"RewardAmount", func() string {
			if d.config.RewardAmount != nil {
				return d.config.RewardAmount.String()
			}
			return "nil"
		}(),
		"EpochDuration", d.config.EpochDuration.String(),
		"BlockTime", d.config.BlockTime.Duration.String(),
		"CommissionRateDefault", d.config.CommissionRateDefault,
		"CommissionEffectivePeriod", d.config.CommissionEffectivePeriod.String())

	// 从真实状态获取奖励账户余额
	rewardAccountBalance, err := d.getAccountBalance(d.config.RewardAccount)
	if err != nil {
		return map[string]interface{}{
			"error": fmt.Sprintf("failed to get reward account balance: %v", err),
		}
	}

	// 从真实状态获取验证者余额
	validatorBalance, err := d.getAccountBalance(validatorAddress)
	if err != nil {
		return map[string]interface{}{
			"error": fmt.Sprintf("failed to get validator balance: %v", err),
		}
	}

	// 获取出块统计
	blockCounts := d.blockTracker.GetEpochBlockCounts(epochNumber)
	blocksProduced := blockCounts[validatorAddress]
	totalBlocks := d.blockTracker.GetTotalEpochBlocks(epochNumber)

	// 添加出块统计的调试日志
	d.logger.Debug("🔍 出块统计调试信息",
		"epochNumber", epochNumber,
		"blockCounts", blockCounts,
		"blocksProduced", blocksProduced,
		"totalBlocks", totalBlocks)

	// 使用统一的奖励分配逻辑计算本验证者及其委托人的奖励
	validatorReward := "0"
	voterReward := "0"
	totalReward := "0"
	rewardPerBlock := "0"
	actualReward := "0"
	voterRewards := make(map[string]string)

	validatorRewardBig := big.NewInt(0)
	voterRewardBig := big.NewInt(0)

	if d.rewardDistributor != nil && blocksProduced > 0 {
		validators := d.GetValidators()
		var targetValidator *validator.ValidatorMetadata
		for _, val := range validators {
			if val.Address == validatorAddress {
				targetValidator = val
				break
			}
		}

		if targetValidator == nil {
			targetValidator = &validator.ValidatorMetadata{
				Address:     validatorAddress,
				VotingPower: big.NewInt(0),
				IsActive:    true,
			}
		}

		voters := d.GetVoters()
		if voters == nil {
			voters = make(map[types.Address]*VoterInfo)
		}

		validatorAmount, voterAmounts := d.rewardDistributor.computeRewardsForValidator(targetValidator, voters, blockCounts, totalBlocks)

		validatorRewardBig = new(big.Int).Set(validatorAmount)
		voterRewardBig = big.NewInt(0)
		for voterAddr, amount := range voterAmounts {
			if amount == nil {
				continue
			}
			voterRewardBig.Add(voterRewardBig, amount)
			voterRewards[voterAddr.String()] = amount.String()
		}

		totalRewardBig := new(big.Int).Add(validatorRewardBig, voterRewardBig)

		validatorReward = validatorRewardBig.String()
		voterReward = voterRewardBig.String()
		totalReward = totalRewardBig.String()

		if blocksProduced > 0 && validatorRewardBig.Sign() > 0 {
			rewardPerBlockBig := new(big.Int).Div(new(big.Int).Set(validatorRewardBig), big.NewInt(int64(blocksProduced)))
			rewardPerBlock = rewardPerBlockBig.String()
		}
	}

	if validatorRewardBig.Sign() > 0 {
		actualRewardBig := new(big.Int).Set(validatorRewardBig)
		if rewardAccountBalance != nil && actualRewardBig.Cmp(rewardAccountBalance) > 0 {
			actualRewardBig.Set(rewardAccountBalance)
		}
		actualReward = actualRewardBig.String()
	}

	// 获取验证者详细信息
	validators := d.getAllValidators()
	var votingPower string = "0"
	var isActive bool = false
	var validatorIndex int = -1

	for i, validator := range validators {
		if validator.Address == validatorAddress {
			votingPower = validator.VotingPower.String()
			isActive = validator.IsActive
			validatorIndex = i
			break
		}
	}

	// 计算奖励占比
	rewardPercentage := 0.0
	if totalBlocks > 0 {
		rewardPercentage = float64(blocksProduced) / float64(totalBlocks) * 100
	}

	// 检查奖励是否足够
	insufficientFunds := false
	if d.rewardDistributor != nil {
		requiredAmount := d.rewardDistributor.GetRewardAmount()
		if requiredAmount != nil {
			insufficientFunds = rewardAccountBalance.Cmp(requiredAmount) < 0
		}
	}

	// 添加最终结果的调试日志
	d.logger.Debug("🔍 GetValidatorRewardsInfo最终结果",
		"validatorAddress", validatorAddress.String(),
		"epochNumber", epochNumber,
		"validatorReward", validatorReward,
		"voterReward", voterReward,
		"totalReward", totalReward,
		"actualReward", actualReward,
		"blocksProduced", blocksProduced,
		"totalEpochBlocks", totalBlocks,
		"rewardPerBlock", rewardPerBlock)

	return map[string]interface{}{
		"validatorAddress":     validatorAddress.String(),
		"validatorIndex":       validatorIndex,
		"epochNumber":          epochNumber,
		"validatorReward":      validatorReward,
		"voterReward":          voterReward,
		"totalReward":          totalReward,
		"actualReward":         actualReward, // 实际可获得的奖励
		"blocksProduced":       blocksProduced,
		"totalEpochBlocks":     totalBlocks,
		"rewardPercentage":     rewardPercentage,
		"rewardPerBlock":       rewardPerBlock,
		"rewardAccount":        d.config.RewardAccount.String(),
		"rewardAccountBalance": rewardAccountBalance.String(),
		"validatorBalance":     validatorBalance.String(),
		"votingPower":          votingPower,
		"isActive":             isActive,
		"insufficientFunds":    insufficientFunds,              // 奖励账户资金是否充足
		"canDistribute":        !insufficientFunds && isActive, // 是否可以分发奖励
		"voterRewards":         voterRewards,                   // 投票者详细奖励分配
	}
}

// recordRewardsFromExtraData 直接从 ExtraData 中的奖励信息记录到数据库（无需重新计算）
func (d *DPoS) recordRewardsFromExtraData(rewardInfo *RewardDistributionInfo) error {
	if d.state == nil || d.state.RewardStore == nil {
		return fmt.Errorf("RewardStore not available")
	}

	// 如果 VoterRewards 为空，说明该 epoch 没有投票者奖励（可能投票者还没有投票，或者投票者权重为 0）
	// 但仍然应该记录验证者奖励（从 Rewards 中获取）
	if len(rewardInfo.VoterRewards) == 0 {
		d.logger.Warn("⚠️ ExtraData中VoterRewards为空，只记录验证者奖励（该epoch可能没有投票者奖励）",
			"epoch", rewardInfo.EpochNumber,
			"rewardCount", len(rewardInfo.Rewards))
		d.logger.Info("🔍 [Epoch奖励诊断] 同步节点收到ExtraData，但VoterRewards为空",
			"epoch", rewardInfo.EpochNumber,
			"rewardCount", len(rewardInfo.Rewards),
			"totalReward", rewardInfo.TotalReward.String())
		
		// 仍然记录验证者奖励（从 Rewards 中获取）
		validators := d.GetValidators()
		// 获取出块统计
		var blockCounts map[types.Address]uint64
		if d.blockTracker != nil {
			blockCounts = d.blockTracker.GetEpochBlockCounts(rewardInfo.EpochNumber)
		}
		for _, validator := range validators {
			validatorAddrStr := validator.Address.String()
			if amount, exists := rewardInfo.Rewards[validatorAddrStr]; exists && amount.Sign() > 0 {
				// 计算出块数和每块奖励
				blocksProduced := uint64(0)
				rewardPerBlock := "0"
				if blockCounts != nil {
					blocksProduced = blockCounts[validator.Address]
					if blocksProduced > 0 {
						rewardPerBlockBig := new(big.Int).Div(amount, big.NewInt(int64(blocksProduced)))
						rewardPerBlock = rewardPerBlockBig.String()
					}
				}
				validatorRecord := &RewardRecordExtended{
					EpochNumber:      rewardInfo.EpochNumber,
					Recipient:        validatorAddrStr,
					RewardType:       "validator",
					Amount:           amount.String(),
					VoteWeight:       "0",
					ValidatorAddress: "",
					BlocksProduced:   blocksProduced,
					RewardPerBlock:   rewardPerBlock,
					Timestamp:        time.Now(),
					Status:           "completed",
				}

				if err := d.state.RewardStore.RecordReward(validatorRecord); err != nil {
					d.logger.Error("❌ 记录验证者奖励失败",
						"epoch", rewardInfo.EpochNumber,
						"validator", validatorAddrStr,
						"error", err)
				}
			}
		}
		
		// 更新StakeInfo中的累计奖励（使用 Rewards）
		for addrStr, reward := range rewardInfo.Rewards {
			if d.state.StakeStore != nil {
				addr := types.StringToAddress(addrStr)
				if err := d.updateStakeInfoCumulativeReward(addr, reward); err != nil {
					d.logger.Warn("⚠️ 更新StakeInfo累计奖励失败",
						"address", addrStr,
						"reward", reward.String(),
						"error", err)
				}
			}
		}
		
		return nil
	}

	// 记录验证者奖励（从 Rewards 中获取）
	validators := d.GetValidators()
	// 获取出块统计
	var blockCounts map[types.Address]uint64
	if d.blockTracker != nil {
		blockCounts = d.blockTracker.GetEpochBlockCounts(rewardInfo.EpochNumber)
	}
	for _, validator := range validators {
		validatorAddrStr := validator.Address.String()
		if amount, exists := rewardInfo.Rewards[validatorAddrStr]; exists && amount.Sign() > 0 {
			// 计算出块数和每块奖励
			blocksProduced := uint64(0)
			rewardPerBlock := "0"
			if blockCounts != nil {
				blocksProduced = blockCounts[validator.Address]
				if blocksProduced > 0 {
					rewardPerBlockBig := new(big.Int).Div(amount, big.NewInt(int64(blocksProduced)))
					rewardPerBlock = rewardPerBlockBig.String()
				}
			}
			validatorRecord := &RewardRecordExtended{
				EpochNumber:      rewardInfo.EpochNumber,
				Recipient:        validatorAddrStr,
				RewardType:       "validator",
				Amount:           amount.String(),
				VoteWeight:       "0",
				ValidatorAddress: "", // 验证者自己的奖励，不需要验证者地址
				BlocksProduced:   blocksProduced,
				RewardPerBlock:   rewardPerBlock,
				Timestamp:        time.Now(),
				Status:           "completed",
			}

			if err := d.state.RewardStore.RecordReward(validatorRecord); err != nil {
				d.logger.Error("❌ 记录验证者奖励失败",
					"epoch", rewardInfo.EpochNumber,
					"validator", validatorAddrStr,
					"error", err)
			}
		}
	}

	// 记录投票者奖励（从 VoterRewards 中获取，包含 validator_address）
	// 获取出块统计（如果还没有获取）
	if blockCounts == nil && d.blockTracker != nil {
		blockCounts = d.blockTracker.GetEpochBlockCounts(rewardInfo.EpochNumber)
	}
	// 如果 VoteWeight 为 nil 或 0，尝试重新计算（用于处理旧数据）
	var voters map[types.Address]*VoterInfo
	if d.rewardDistributor != nil {
		// 检查是否有需要重新计算权重的记录
		needRecalculate := false
		for _, voterReward := range rewardInfo.VoterRewards {
			if voterReward != nil && (voterReward.VoteWeight == nil || voterReward.VoteWeight.Sign() == 0) {
				needRecalculate = true
				break
			}
		}
		if needRecalculate {
			voters = d.GetVoters()
		}
	}
	for _, voterReward := range rewardInfo.VoterRewards {
		if voterReward != nil && voterReward.Amount != nil && voterReward.Amount.Sign() > 0 {
			// 获取投票权重
			voteWeight := "0"
			if voterReward.VoteWeight != nil && voterReward.VoteWeight.Sign() > 0 {
				// 如果 VoteWeight 有值且大于 0，直接使用
				voteWeight = voterReward.VoteWeight.String()
			} else if d.rewardDistributor != nil && voters != nil {
				// 如果 VoteWeight 为 nil 或 0，尝试重新计算（处理旧数据或错误数据）
				validatorAddr := types.StringToAddress(voterReward.ValidatorAddress)
				voterAddr := types.StringToAddress(voterReward.VoterAddress)
				voterWeights, _ := d.rewardDistributor.computeVoterWeights(validatorAddr, voters)
				if weight, exists := voterWeights[voterAddr]; exists && weight != nil && weight.Sign() > 0 {
					voteWeight = weight.String()
					d.logger.Info("✅ 重新计算投票权重（ExtraData中VoteWeight为nil或0）",
						"epoch", rewardInfo.EpochNumber,
						"voter", voterReward.VoterAddress,
						"validator", voterReward.ValidatorAddress,
						"voteWeight", voteWeight)
				} else {
					d.logger.Debug("⚠️ 无法重新计算投票权重（投票者可能已撤销投票）",
						"epoch", rewardInfo.EpochNumber,
						"voter", voterReward.VoterAddress,
						"validator", voterReward.ValidatorAddress)
				}
			}
			// 计算验证者的出块数和每块奖励
			validatorAddr := types.StringToAddress(voterReward.ValidatorAddress)
			blocksProduced := uint64(0)
			rewardPerBlock := "0"
			if blockCounts != nil {
				blocksProduced = blockCounts[validatorAddr]
				if blocksProduced > 0 {
					rewardPerBlockBig := new(big.Int).Div(voterReward.Amount, big.NewInt(int64(blocksProduced)))
					rewardPerBlock = rewardPerBlockBig.String()
				}
			}
			voterRecord := &RewardRecordExtended{
				EpochNumber:      rewardInfo.EpochNumber,
				Recipient:        voterReward.VoterAddress,
				RewardType:       "voter",
				Amount:           voterReward.Amount.String(),
				VoteWeight:       voteWeight,
				ValidatorAddress: voterReward.ValidatorAddress, // ✅ 直接使用 ExtraData 中的验证者地址
				BlocksProduced:   blocksProduced,
				RewardPerBlock:   rewardPerBlock,
				Timestamp:        time.Now(),
				Status:           "completed",
			}

			if err := d.state.RewardStore.RecordReward(voterRecord); err != nil {
				d.logger.Error("❌ 记录投票者奖励失败",
					"epoch", rewardInfo.EpochNumber,
					"voter", voterReward.VoterAddress,
					"validator", voterReward.ValidatorAddress,
					"error", err)
			}
		}
	}

	d.logger.Info("✅ 从ExtraData记录奖励成功",
		"epoch", rewardInfo.EpochNumber,
		"voterRewardCount", len(rewardInfo.VoterRewards))

	// 更新StakeInfo中的累计奖励（使用 Rewards）
	for addrStr, reward := range rewardInfo.Rewards {
		if d.state.StakeStore != nil {
			addr := types.StringToAddress(addrStr)
			if err := d.updateStakeInfoCumulativeReward(addr, reward); err != nil {
				d.logger.Warn("⚠️ 更新StakeInfo累计奖励失败",
					"address", addrStr,
					"reward", reward.String(),
					"error", err)
			}
		}
	}

	return nil
}

// recordRewardsToDatabase 记录奖励到数据库（不更新状态）并更新StakeInfo中的累计奖励
// blockCounts 和 totalBlocks 为可选参数，如果为 nil/0 则从 blockTracker 获取
// 注意：此函数用于重新计算奖励，新代码应该使用 recordRewardsFromExtraData
func (d *DPoS) recordRewardsToDatabase(epochNumber uint64, rewards map[types.Address]*big.Int, validators validator.AccountSet, voters map[types.Address]*VoterInfo, blockCounts map[types.Address]uint64, totalBlocks uint64) error {
	// 重新计算奖励，为每个验证者-投票者组合单独记录-这样可以保存验证者地址信息
	if d.rewardDistributor != nil {
		// 如果没有提供 blockCounts，从 blockTracker 获取
		if blockCounts == nil {
			if d.blockTracker != nil {
				blockCounts = d.blockTracker.GetEpochBlockCounts(epochNumber)
				totalBlocks = d.blockTracker.GetTotalEpochBlocks(epochNumber)
			} else {
				// blockTracker 不可用，无法记录奖励
				d.logger.Warn("⚠️ blockTracker不可用，无法记录奖励", "epoch", epochNumber)
				return nil
			}
		} else if totalBlocks == 0 {
			// 如果提供了 blockCounts 但 totalBlocks 为 0，计算 totalBlocks
			for _, count := range blockCounts {
				totalBlocks += count
			}
		}

		// 如果 totalBlocks > 0，记录奖励（重新计算，包含 validator_address）
		if totalBlocks > 0 && blockCounts != nil {
			// 为每个验证者单独记录奖励
			for _, validator := range validators {
				validatorAmount, voterRewards := d.rewardDistributor.computeRewardsForValidator(validator, voters, blockCounts, totalBlocks)

				// 记录验证者奖励
				if validatorAmount.Sign() > 0 {
					// 计算出块数和每块奖励
					blocksProduced := blockCounts[validator.Address]
					rewardPerBlock := "0"
					if blocksProduced > 0 {
						rewardPerBlockBig := new(big.Int).Div(validatorAmount, big.NewInt(int64(blocksProduced)))
						rewardPerBlock = rewardPerBlockBig.String()
					}
					validatorRecord := &RewardRecordExtended{
						EpochNumber:      epochNumber,
						Recipient:        validator.Address.String(),
						RewardType:       "validator",
						Amount:           validatorAmount.String(),
						VoteWeight:       "0",
						ValidatorAddress: "", // 验证者自己的奖励，不需要验证者地址
						BlocksProduced:   blocksProduced,
						RewardPerBlock:   rewardPerBlock,
						Timestamp:        time.Now(),
						Status:           "completed",
					}

					if d.state.RewardStore != nil {
						if err := d.state.RewardStore.RecordReward(validatorRecord); err != nil {
							d.logger.Error("❌ 记录验证者奖励失败",
								"epoch", epochNumber,
								"validator", validator.Address.String(),
								"error", err)
						}
					}
				}

				// 记录投票者奖励（每个验证者-投票者组合单独记录）
				// 获取投票权重
				voterWeights, _ := d.rewardDistributor.computeVoterWeights(validator.Address, voters)
				validatorBlocksProduced := blockCounts[validator.Address]
				for voterAddr, share := range voterRewards {
					if share.Sign() > 0 {
						// 获取投票权重
						voteWeight := "0"
						if weight, exists := voterWeights[voterAddr]; exists && weight != nil {
							voteWeight = weight.String()
						}
						// 计算出块数和每块奖励（使用验证者的出块数）
						rewardPerBlock := "0"
						if validatorBlocksProduced > 0 {
							rewardPerBlockBig := new(big.Int).Div(share, big.NewInt(int64(validatorBlocksProduced)))
							rewardPerBlock = rewardPerBlockBig.String()
						}
						voterRecord := &RewardRecordExtended{
							EpochNumber:      epochNumber,
							Recipient:        voterAddr.String(),
							RewardType:       "voter",
							Amount:           share.String(),
							VoteWeight:       voteWeight,
							ValidatorAddress: validator.Address.String(), // ✅ 保存验证者地址
							BlocksProduced:   validatorBlocksProduced,
							RewardPerBlock:   rewardPerBlock,
							Timestamp:        time.Now(),
							Status:           "completed",
						}

						if d.state.RewardStore != nil {
							if err := d.state.RewardStore.RecordReward(voterRecord); err != nil {
								d.logger.Error("❌ 记录投票者奖励失败",
									"epoch", epochNumber,
									"voter", voterAddr.String(),
									"validator", validator.Address.String(),
									"error", err)
							}
						}
					}
				}
			}
		} else if len(rewards) > 0 {
			// 如果 totalBlocks == 0 但 rewards 有数据（区块还未完全同步），直接记录 rewards（不包含 validator_address）
			// 这种情况发生在同步节点处理 epoch 结束区块时，该 epoch 的区块还没有完全同步
			d.logger.Warn("⚠️ 区块还未完全同步，直接记录奖励（不包含 validator_address）",
				"epoch", epochNumber,
				"rewardCount", len(rewards),
				"totalBlocks", totalBlocks)

			for address, reward := range rewards {
				if reward.Sign() > 0 {
					// 判断是验证者还是投票者
					isValidator := false
					for _, validator := range validators {
						if validator.Address == address {
							isValidator = true
							break
						}
					}

					rewardType := "voter"
					if isValidator {
						rewardType = "validator"
					}

					record := &RewardRecordExtended{
						EpochNumber:      epochNumber,
						Recipient:        address.String(),
						RewardType:       rewardType,
						Amount:           reward.String(),
						VoteWeight:       "0",
						ValidatorAddress: "", // 无法确定，因为区块还未完全同步
						BlocksProduced:   0,  // 无法确定，因为区块还未完全同步
						RewardPerBlock:   "0", // 无法确定，因为区块还未完全同步
						Timestamp:        time.Now(),
						Status:           "completed",
					}

					if d.state.RewardStore != nil {
						if err := d.state.RewardStore.RecordReward(record); err != nil {
							d.logger.Error("❌ 记录奖励失败（fallback）",
								"epoch", epochNumber,
								"address", address.String(),
								"error", err)
						}
					}
				}
			}
		}
	}

	// 更新StakeInfo中的累计奖励（使用原始rewards map）
	for address, reward := range rewards {
		if d.state.StakeStore != nil {
			if err := d.updateStakeInfoCumulativeReward(address, reward); err != nil {
				d.logger.Warn("⚠️ 更新StakeInfo累计奖励失败",
					"address", address.String(),
					"reward", reward.String(),
					"error", err)
			}
		}
	}

	return nil
}

// updateStakeInfoCumulativeReward 更新StakeInfo中的累计奖励
func (d *DPoS) updateStakeInfoCumulativeReward(address types.Address, reward *big.Int) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("stakeStore not available")
	}

	return d.state.StakeStore.db.Update(func(tx *bolt.Tx) error {
		stakingBucket := tx.Bucket([]byte("StakingInfo"))
		if stakingBucket == nil {
			return nil // bucket不存在，跳过
		}

		// 查找该地址的所有StakeInfo记录（可能有多条，因为使用复合key）
		cursor := stakingBucket.Cursor()
		updated := false

		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if len(key) < 20 {
				continue
			}

			// 检查地址是否匹配
			var keyAddr types.Address
			copy(keyAddr[:], key[:20])
			if keyAddr != address {
				continue
			}

			// 解析并更新
			var stakeInfo StakeInfo
			if err := json.Unmarshal(value, &stakeInfo); err != nil {
				continue
			}

			// 初始化或累加
			if stakeInfo.Rewards == nil {
				stakeInfo.Rewards = new(big.Int).Set(reward)
			} else {
				stakeInfo.Rewards.Add(stakeInfo.Rewards, reward)
			}

			// 保存
			updatedData, err := json.Marshal(stakeInfo)
			if err != nil {
				continue
			}

			if err := stakingBucket.Put(key, updatedData); err != nil {
				continue
			}

			updated = true
		}

		if !updated {
			// 如果没有找到记录，这是正常的（可能该地址还没有StakeInfo）
			// 不返回错误，因为StakeInfo可能在其他地方创建
		}

		return nil
	})
}
