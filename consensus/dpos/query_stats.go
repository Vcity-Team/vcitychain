package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
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
		}
	}

	// 获取冻结余额
	frozenBalance := big.NewInt(0)
	if d.state != nil && d.state.FreezeStore != nil {
		freezeInfo, err := d.state.FreezeStore.GetFreezeInfo(address)
		if err == nil && freezeInfo != nil {
			frozenBalance = freezeInfo.FrozenAmount
		}
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
		blockTime = 3 * time.Second // 默认值
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
	// 🆕 添加方法开始的调试日志
	d.logger.Debug("🔍 GetValidatorRewardsInfo开始",
		"validatorAddress", validatorAddress.String(),
		"epochNumber", epochNumber)

	if d.rewardDistributor == nil {
		d.logger.Debug("❌ rewardDistributor为nil")
		return map[string]interface{}{
			"error": "reward distributor not initialized",
		}
	}

	// 🆕 添加配置值的调试日志
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

	// 🆕 添加出块统计的调试日志
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

	if d.config.RewardAmount != nil && blocksProduced > 0 {
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
	if d.config.RewardAmount != nil {
		requiredAmount := new(big.Int).Set(d.config.RewardAmount)
		insufficientFunds = rewardAccountBalance.Cmp(requiredAmount) < 0
	}

	// 🆕 添加最终结果的调试日志
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
		"voterRewards":         voterRewards,                   // 🆕 投票者详细奖励分配
	}
}

// recordRewardsToDatabase 记录奖励到数据库（不更新状态）
func (d *DPoS) recordRewardsToDatabase(epochNumber uint64, rewards map[types.Address]*big.Int) error {
	// 记录验证者奖励到数据库
	for address, reward := range rewards {
		rewardRecord := &RewardRecordExtended{
			EpochNumber:     epochNumber,
			Recipient:       address.String(),
			RewardType:      "validator",
			Amount:          reward.String(),
			BlockCount:      0,
			VoteWeight:      "0",
			Timestamp:       time.Now(),
			TransactionHash: "",
			Status:          "completed",
		}

		if d.state.RewardStore != nil {
			if err := d.state.RewardStore.RecordReward(rewardRecord); err != nil {
				d.logger.Error("❌ 记录奖励失败",
					"epoch", epochNumber,
					"address", address.String(),
					"error", err)
			}
		} else {
			d.logger.Warn("⚠️ RewardStore为nil，跳过记录奖励",
				"epoch", epochNumber,
				"address", address.String())
		}
	}

	return nil
}
