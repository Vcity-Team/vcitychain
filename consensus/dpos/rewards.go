package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	hcf "github.com/hashicorp/go-hclog"
)

// RewardRecord 奖励记录
type RewardRecord struct {
	BlockNumber uint64        `json:"block_number"`
	EpochNumber uint64        `json:"epoch_number"`
	Amount      *big.Int      `json:"amount"`
	Type        string        `json:"type"` // "block", "epoch", "stake"
	Timestamp   time.Time     `json:"timestamp"`
	Staker      types.Address `json:"staker"`
}

// EpochRewards 周期奖励信息
type EpochRewards struct {
	EpochNumber   uint64                     `json:"epoch_number"`
	TotalRewards  *big.Int                   `json:"total_rewards"`
	BlockRewards  *big.Int                   `json:"block_rewards"`
	StakeRewards  *big.Int                   `json:"stake_rewards"`
	Distributions map[types.Address]*big.Int `json:"distributions"`
	Timestamp     time.Time                  `json:"timestamp"`
}

// RewardCalculator 奖励计算器
type RewardCalculator struct {
	config *DPoSConfig
	logger hcf.Logger
}

// NewRewardCalculator 创建奖励计算器
func NewRewardCalculator(config *DPoSConfig, logger hcf.Logger) *RewardCalculator {
	return &RewardCalculator{
		config: config,
		logger: logger,
	}
}

// CalculateBlockReward 计算区块奖励
func (rc *RewardCalculator) CalculateBlockReward(blockNumber uint64, proposer types.Address, totalVotingPower *big.Int) *big.Int {
	// 基础区块奖励 - 使用默认值
	baseReward := big.NewInt(1000000000000000000) // 1 token

	// 根据投票权重调整奖励
	if totalVotingPower.Cmp(big.NewInt(0)) > 0 {
		// 奖励与总投票权重成反比，鼓励更多参与
		adjustedReward := new(big.Int).Mul(baseReward, big.NewInt(1000000))
		adjustedReward.Div(adjustedReward, totalVotingPower)

		// 设置最小和最大奖励限制
		minReward := new(big.Int).Div(baseReward, big.NewInt(10))
		maxReward := new(big.Int).Mul(baseReward, big.NewInt(2))

		if adjustedReward.Cmp(minReward) < 0 {
			adjustedReward = minReward
		}
		if adjustedReward.Cmp(maxReward) > 0 {
			adjustedReward = maxReward
		}

		baseReward = adjustedReward
	}

	// 应用奖励衰减
	decayFactor := rc.calculateDecayFactor(blockNumber)
	finalReward := new(big.Int).Mul(baseReward, big.NewInt(int64(decayFactor*1000000)))
	finalReward.Div(finalReward, big.NewInt(1000000))

	rc.logger.Debug("calculated block reward",
		"block", blockNumber,
		"proposer", proposer.String(),
		"base_reward", baseReward.String(),
		"final_reward", finalReward.String(),
		"decay_factor", decayFactor)

	return finalReward
}

// CalculateEpochReward 计算周期奖励
func (rc *RewardCalculator) CalculateEpochReward(epochNumber uint64, validators validator.AccountSet) *big.Int {
	// 基础周期奖励 - 使用默认值
	baseReward := new(big.Int).Mul(big.NewInt(1000000000000000000), big.NewInt(100)) // 100 tokens

	// 根据活跃验证者数量调整奖励
	activeValidators := 0
	for _, val := range validators {
		if val.IsActive {
			activeValidators++
		}
	}

	// 奖励与活跃验证者数量成正比
	adjustedReward := new(big.Int).Mul(baseReward, big.NewInt(int64(activeValidators)))
	adjustedReward.Div(adjustedReward, big.NewInt(int64(len(validators))))

	// 应用奖励衰减
	decayFactor := rc.calculateDecayFactor(epochNumber * 100) // 假设每100个区块为一个周期
	finalReward := new(big.Int).Mul(adjustedReward, big.NewInt(int64(decayFactor*1000000)))
	finalReward.Div(finalReward, big.NewInt(1000000))

	rc.logger.Debug("calculated epoch reward",
		"epoch", epochNumber,
		"active_validators", activeValidators,
		"total_validators", len(validators),
		"base_reward", baseReward.String(),
		"final_reward", finalReward.String(),
		"decay_factor", decayFactor)

	return finalReward
}

// CalculateStakeReward 计算质押奖励
func (rc *RewardCalculator) CalculateStakeReward(staker types.Address, votingPower *big.Int, totalVotingPower *big.Int, epochReward *big.Int) *big.Int {
	if totalVotingPower.Cmp(big.NewInt(0)) == 0 {
		return big.NewInt(0)
	}

	// 按投票权重比例分配奖励
	stakeReward := new(big.Int).Mul(epochReward, votingPower)
	stakeReward.Div(stakeReward, totalVotingPower)

	rc.logger.Debug("calculated stake reward",
		"staker", staker.String(),
		"voting_power", votingPower.String(),
		"total_voting_power", totalVotingPower.String(),
		"epoch_reward", epochReward.String(),
		"stake_reward", stakeReward.String())

	return stakeReward
}

// calculateDecayFactor 计算衰减因子
func (rc *RewardCalculator) calculateDecayFactor(blockNumber uint64) float64 {
	// 每10000个区块衰减一次
	decayPeriod := uint64(10000)
	decayCount := blockNumber / decayPeriod

	// 计算衰减因子 - 使用默认衰减率0.95
	decayRate := 0.95
	decayFactor := 1.0
	for i := uint64(0); i < decayCount; i++ {
		decayFactor *= decayRate
	}

	// 设置最小衰减因子
	if decayFactor < 0.1 {
		decayFactor = 0.1
	}

	return decayFactor
}

// LegacyRewardDistributor 旧版奖励分发器（已废弃）
type LegacyRewardDistributor struct {
	calculator *RewardCalculator
	state      *State
	logger     hcf.Logger
}

// NewLegacyRewardDistributor 创建旧版奖励分发器（已废弃）
func NewLegacyRewardDistributor(calculator *RewardCalculator, state *State, logger hcf.Logger) *LegacyRewardDistributor {
	return &LegacyRewardDistributor{
		calculator: calculator,
		state:      state,
		logger:     logger,
	}
}

// DistributeBlockReward 分发区块奖励
func (rd *LegacyRewardDistributor) DistributeBlockReward(block *types.FullBlock, proposer types.Address, totalVotingPower *big.Int) error {
	// 计算区块奖励
	reward := rd.calculator.CalculateBlockReward(block.Block.Number(), proposer, totalVotingPower)

	// 创建奖励记录
	record := &RewardRecord{
		BlockNumber: block.Block.Number(),
		EpochNumber: block.Block.Number() / 100, // 假设每100个区块为一个周期
		Amount:      reward,
		Type:        "block",
		Timestamp:   time.Now(),
		Staker:      proposer,
	}

	// 保存奖励记录
	if err := rd.saveRewardRecord(record); err != nil {
		return fmt.Errorf("failed to save reward record: %w", err)
	}

	// 更新投票者信息
	if err := rd.updateVoterRewards(proposer, reward); err != nil {
		return fmt.Errorf("failed to update voter rewards: %w", err)
	}

	rd.logger.Info("distributed block reward",
		"block", block.Block.Number(),
		"proposer", proposer.String(),
		"reward", reward.String())

	return nil
}

// DistributeEpochReward 分发周期奖励
func (rd *LegacyRewardDistributor) DistributeEpochReward(epochNumber uint64, validators validator.AccountSet, voters map[types.Address]*VoterInfo) error {
	// 计算周期奖励
	epochReward := rd.calculator.CalculateEpochReward(epochNumber, validators)

	// 计算总投票权重
	totalVotingPower := big.NewInt(0)
	for _, voter := range voters {
		totalVotingPower.Add(totalVotingPower, voter.VotingPower)
	}

	// 创建周期奖励信息
	epochRewards := &EpochRewards{
		EpochNumber:   epochNumber,
		TotalRewards:  epochReward,
		BlockRewards:  big.NewInt(0), // 区块奖励在区块处理时已分发
		StakeRewards:  epochReward,
		Distributions: make(map[types.Address]*big.Int),
		Timestamp:     time.Now(),
	}

	// 按投票权重分配奖励
	for staker, voter := range voters {
		if voter.VotingPower.Cmp(big.NewInt(0)) > 0 {
			stakeReward := rd.calculator.CalculateStakeReward(staker, voter.VotingPower, totalVotingPower, epochReward)

			// 创建奖励记录
			record := &RewardRecord{
				BlockNumber: epochNumber * 100, // 周期结束区块
				EpochNumber: epochNumber,
				Amount:      stakeReward,
				Type:        "stake",
				Timestamp:   time.Now(),
				Staker:      staker,
			}

			// 保存奖励记录
			if err := rd.saveRewardRecord(record); err != nil {
				rd.logger.Warn("failed to save reward record", "error", err, "staker", staker)
				continue
			}

			// 更新投票者信息
			if err := rd.updateVoterRewards(staker, stakeReward); err != nil {
				rd.logger.Warn("failed to update voter rewards", "error", err, "staker", staker)
				continue
			}

			epochRewards.Distributions[staker] = stakeReward
		}
	}

	// 保存周期奖励信息
	if err := rd.saveEpochRewards(epochRewards); err != nil {
		return fmt.Errorf("failed to save epoch rewards: %w", err)
	}

	rd.logger.Info("distributed epoch reward",
		"epoch", epochNumber,
		"total_reward", epochReward.String(),
		"recipients", len(epochRewards.Distributions))

	return nil
}

// saveRewardRecord 保存奖励记录
func (rd *LegacyRewardDistributor) saveRewardRecord(record *RewardRecord) error {
	// 这里应该调用数据库存储方法
	// 暂时使用日志记录
	rd.logger.Debug("saving reward record",
		"staker", record.Staker.String(),
		"amount", record.Amount.String(),
		"type", record.Type)

	return nil
}

// saveEpochRewards 保存周期奖励信息
func (rd *LegacyRewardDistributor) saveEpochRewards(rewards *EpochRewards) error {
	// 这里应该调用数据库存储方法
	// 暂时使用日志记录
	rd.logger.Debug("saving epoch rewards",
		"epoch", rewards.EpochNumber,
		"total_rewards", rewards.TotalRewards.String(),
		"distributions", len(rewards.Distributions))

	return nil
}

// updateVoterRewards 更新投票者奖励
func (rd *LegacyRewardDistributor) updateVoterRewards(staker types.Address, reward *big.Int) error {
	// 这里应该更新投票者的奖励余额
	// 暂时使用日志记录
	rd.logger.Debug("updating voter rewards",
		"staker", staker.String(),
		"reward", reward.String())

	return nil
}

// GetRewardHistory 获取奖励历史
func (rd *LegacyRewardDistributor) GetRewardHistory(staker types.Address, limit int) ([]*RewardRecord, error) {
	// 这里应该从数据库获取奖励历史
	// 暂时返回空列表
	return []*RewardRecord{}, nil
}

// GetEpochRewards 获取周期奖励信息
func (rd *LegacyRewardDistributor) GetEpochRewards(epochNumber uint64) (*EpochRewards, error) {
	// 这里应该从数据库获取周期奖励信息
	// 暂时返回空信息
	return &EpochRewards{
		EpochNumber:   epochNumber,
		TotalRewards:  big.NewInt(0),
		BlockRewards:  big.NewInt(0),
		StakeRewards:  big.NewInt(0),
		Distributions: make(map[types.Address]*big.Int),
		Timestamp:     time.Now(),
	}, nil
}

// GetTotalRewards 获取总奖励统计
func (rd *LegacyRewardDistributor) GetTotalRewards(staker types.Address) (*big.Int, error) {
	// 这里应该从数据库计算总奖励
	// 暂时返回0
	return big.NewInt(0), nil
}

// ==================== 从 dpos.go 迁移的奖励分配函数 ====================

// distributeEpochRewards 分发Epoch奖励
func (d *DPoS) distributeEpochRewards(epochNumber uint64, currentRound uint64) error {
	startTime := time.Now()
	d.logger.Info("🎉 ========== 生成节点中开始计算Epoch奖励 ==========",
		"epoch", epochNumber,
		"startTime", startTime.Format("2006-01-02 15:04:05"),
		"note", "为上一个epoch计算奖励")

	// 1. 获取验证者集合
	validators := d.getAllValidators()
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 没有验证者，跳过奖励计算", "epoch", epochNumber)
		return nil
	}

	// 获取出块统计（使用区块基础Epoch）
	blockCounts := d.blockTracker.GetEpochBlockCounts(epochNumber)
	totalBlocks := d.blockTracker.GetTotalEpochBlocks(epochNumber)

	// 🆕 添加详细的出块统计日志
	d.logger.Info("🔍 检查出块统计",
		"epoch", epochNumber,
		"blockCounts", blockCounts,
		"totalBlocks", totalBlocks,
		"validatorsCount", len(validators))

	// 🆕 详细打印每个验证者的出块记录
	d.logger.Info("📋 ========== 详细出块记录 ==========",
		"epoch", epochNumber,
		"totalBlocks", totalBlocks)

	for _, validator := range validators {
		blocksProduced := blockCounts[validator.Address]
		d.logger.Info("📋 验证者出块记录",
			"epoch", epochNumber,
			"validator", validator.Address.String()[:16],
			"blocksProduced", blocksProduced,
			"votingPower", validator.VotingPower.String())
	}

	if totalBlocks == 0 {
		d.logger.Warn("⚠️ 该epoch没有出块记录，跳过奖励计算", "epoch", epochNumber)
		return nil
	}

	// 🆕 优先从参数系统读取 dpos_reward_amount（经过治理流程修改的值是权威数据源）
	var rewardAmount *big.Int
	if paramValue, err := d.getCurrentParameterValue("dpos_reward_amount"); err == nil {
		switch v := paramValue.(type) {
		case string:
			if bigAmount, ok := new(big.Int).SetString(v, 10); ok && bigAmount.Cmp(big.NewInt(0)) > 0 {
				rewardAmount = bigAmount
				d.logger.Debug("从参数系统读取 reward amount", "amount", v)
			}
		case *big.Int:
			if v != nil && v.Cmp(big.NewInt(0)) > 0 {
				rewardAmount = new(big.Int).Set(v)
				d.logger.Debug("从参数系统读取 reward amount", "amount", v.String())
			}
		}
	}

	// 如果参数系统没有值，使用配置值
	if rewardAmount == nil {
		if d.config.RewardAmount != nil {
			rewardAmount = d.config.RewardAmount
		} else {
			d.logger.Error("❌ 奖励金额配置为空，跳过奖励计算", "epoch", epochNumber)
			return fmt.Errorf("reward amount is nil")
		}
	}

	// 🆕 更新 RewardDistributor 的奖励金额（确保使用最新值）
	if d.rewardDistributor != nil {
		d.rewardDistributor.UpdateRewardAmount(rewardAmount)
	}

	// 检查奖励账户余额
	rewardAccountBalance, err := d.getAccountBalance(d.config.RewardAccount)
	if err != nil {
		d.logger.Error("❌ 获取奖励账户余额失败", "account", d.config.RewardAccount.String(), "error", err)
		return fmt.Errorf("failed to get reward account balance: %w", err)
	}

	if rewardAccountBalance.Cmp(rewardAmount) < 0 {
		d.logger.Error("❌ 奖励账户余额不足",
			"account", d.config.RewardAccount.String(),
			"balance", rewardAccountBalance.String(),
			"required", rewardAmount.String())
		return fmt.Errorf("insufficient reward account balance")
	}

	// 🆕 使用 RewardDistributor 统一计算奖励（验证者 + 投票者）
	// 1. 获取投票者信息
	voters := d.GetVoters()
	if voters == nil {
		voters = make(map[types.Address]*VoterInfo)
	}

	// 2. 检查 rewardDistributor 是否已初始化
	if d.rewardDistributor == nil {
		d.logger.Error("❌ rewardDistributor未初始化", "epoch", epochNumber)
		return fmt.Errorf("rewardDistributor is nil")
	}

	// 3. 使用 RewardDistributor 统一计算奖励（已支持累加）
	rewards := d.rewardDistributor.CalculateRewards(
		validators,
		voters,
		blockCounts,
		totalBlocks,
	)

	// 4. 处理奖励结果：区分验证者和投票者，记录到数据库
	stateUpdates := make(map[types.Address]*big.Int)
	validatorRewardCount := 0

	for address, totalReward := range rewards {
		if totalReward.Sign() > 0 {
			// 🆕 简单区分：检查地址是否在 validators 和 voters 中
			isValidator := false
			var blocksProduced uint64 = 0

			// 检查是否是验证者
			for _, validator := range validators {
				if validator.Address == address {
					isValidator = true
					blocksProduced = blockCounts[address]
					validatorRewardCount++
					break
				}
			}

			// 检查是否是投票者
			isVoter := false
			if voter, exists := voters[address]; exists && voter.VotingPower.Cmp(big.NewInt(0)) > 0 {
				isVoter = true
			}

			// 确定奖励类型（用于数据库记录）
			rewardType := "voter"
			if isValidator && isVoter {
				rewardType = "validator+voter" // 既是验证者又是投票者
			} else if isValidator {
				rewardType = "validator"
			}

			// 记录奖励到数据库
			rewardRecord := &RewardRecordExtended{
				EpochNumber:     epochNumber,
				Recipient:       address.String(),
				RewardType:      rewardType,
				Amount:          totalReward.String(), // 总奖励（已累加）
				BlockCount:      blocksProduced,
				VoteWeight:      "0", // 可以后续优化记录实际投票权重
				Timestamp:       time.Now(),
				TransactionHash: "",
				Status:          "completed",
			}

			if d.state.RewardStore != nil {
				if err := d.state.RewardStore.RecordReward(rewardRecord); err != nil {
					d.logger.Error("❌ 记录奖励失败",
						"epoch", epochNumber,
						"recipient", address.String(),
						"type", rewardType,
						"error", err)
				}
			}

			// totalReward 已经是累加后的总奖励（验证者+投票者），直接使用
			stateUpdates[address] = totalReward
		}
	}

	// 🆕 在epoch结束区块准备奖励分发信息（不直接执行状态更新）
	if len(stateUpdates) > 0 {
		d.logger.Info("🎯 在epoch结束区块准备奖励分发信息",
			"epoch", epochNumber,
			"updateCount", len(stateUpdates))

		// 计算总奖励金额
		totalReward := big.NewInt(0)
		for _, reward := range stateUpdates {
			totalReward.Add(totalReward, reward)
		}

		// 🆕 不直接执行状态更新，而是将奖励分发信息存储到pendingRewardDistribution
		// 这样buildBlock可以将其添加到ExtraData中，然后在区块执行时处理
		d.pendingRewardDistribution = &RewardDistributionInfo{
			EpochNumber: epochNumber,
			Rewards:     make(map[string]*big.Int),
			TotalReward: totalReward,
		}

		// 转换地址格式
		for address, reward := range stateUpdates {
			d.pendingRewardDistribution.Rewards[address.String()] = reward
		}

		d.logger.Info("✅ 奖励分发信息已准备完成，等待buildBlock处理",
			"epoch", epochNumber,
			"updateCount", len(stateUpdates))
	} else {
		d.logger.Warn("⚠️ 没有奖励分发信息", "epoch", epochNumber)
	}

	// 3. 记录分发统计
	d.logger.Info("📊 ========== 生产节点中奖励计算信息统计（使用RewardDistributor）==========",
		"epoch", epochNumber,
		"validatorsCount", len(validators),
		"votersCount", len(voters),
		"validatorRewardCount", validatorRewardCount,
		"totalBlocks", totalBlocks,
		"stateUpdateCount", len(stateUpdates),
		"status", "奖励计算完成（统一使用RewardDistributor）")

	return nil
}

// calculateAndRecordEpochRewards 计算和记录epoch奖励（延迟状态更新）
func (d *DPoS) calculateAndRecordEpochRewards(epochNumber uint64) error {
	// 获取验证者列表
	validators := d.GetValidators()

	if len(validators) == 0 {
		d.logger.Warn("⚠️ 没有验证者，跳过奖励计算", "epoch", epochNumber)
		return nil
	}

	blockCounts := make(map[types.Address]uint64)
	totalBlocks := uint64(0)
	for _, validator := range validators {
		blocksProduced := d.getBlocksProducedInEpoch(validator.Address, epochNumber)
		if blocksProduced > 0 {
			blockCounts[validator.Address] = blocksProduced
			totalBlocks += blocksProduced
		}
	}

	if totalBlocks == 0 {
		d.logger.Warn("⚠️ 该epoch没有出块记录，跳过奖励记录", "epoch", epochNumber)
		return nil
	}

	voters := d.GetVoters()
	if voters == nil {
		voters = make(map[types.Address]*VoterInfo)
	}

	rewards := d.rewardDistributor.CalculateRewards(validators, voters, blockCounts, totalBlocks)
	if len(rewards) == 0 {
		d.logger.Warn("⚠️ 计算结果为空，跳过奖励记录", "epoch", epochNumber)
		return nil
	}

	// 记录奖励到数据库（不更新状态）
	if err := d.recordRewardsToDatabase(epochNumber, rewards); err != nil {
		return fmt.Errorf("failed to record rewards to database: %w", err)
	}

	// 延迟状态更新机制已移除，奖励分发在epoch结束区块直接执行

	return nil
}

// getBlocksProducedInEpoch 获取验证者在指定epoch中生产的区块数
func (d *DPoS) getBlocksProducedInEpoch(address types.Address, epochNumber uint64) uint64 {
	// 简化实现：返回固定值，实际应该从区块跟踪器中获取
	return 8 // 假设每个验证者生产8个区块
}

// applyRewardDistribution 应用奖励分配到状态
func (d *DPoS) applyRewardDistribution(rewardInfo *RewardDistributionInfo, blockHeader *types.Header) error {
	d.logger.Info("💰 开始应用奖励分配",
		"epochNumber", rewardInfo.EpochNumber,
		"rewardCount", len(rewardInfo.Rewards),
		"totalReward", rewardInfo.TotalReward.String())

	// 获取当前状态根
	currentHeader := d.config.Blockchain.Header()
	if currentHeader == nil {
		return fmt.Errorf("failed to get current header")
	}

	d.logger.Info("🔍 验证节点奖励分发 - 获取当前状态根",
		"currentStateRoot", currentHeader.StateRoot.String(),
		"blockNumber", currentHeader.Number,
		"说明", "验证节点开始创建状态快照")

	// 创建状态快照
	snapshot, err := d.config.Executor.StateAt(currentHeader.StateRoot)
	if err != nil {
		return fmt.Errorf("failed to create state snapshot: %w", err)
	}

	d.logger.Info("✅ 验证节点奖励分发 - 状态快照创建成功",
		"stateRoot", currentHeader.StateRoot.String(),
		"说明", "验证节点状态快照创建成功，准备应用奖励分配")

	// 应用奖励分配
	var objects []*state.Object
	d.logger.Info("🔍 验证节点奖励分发 - 开始遍历奖励信息",
		"rewardCount", len(rewardInfo.Rewards),
		"说明", "验证节点开始遍历每个验证者的奖励信息")

	for addrStr, amount := range rewardInfo.Rewards {
		addr := types.StringToAddress(addrStr)

		d.logger.Info("🔍 验证节点奖励分发 - 处理验证者奖励",
			"address", addrStr,
			"reward", amount.String(),
			"说明", "验证节点开始处理单个验证者的奖励分配")

		// 获取当前余额
		accountInfo, err := snapshot.GetAccount(addr)
		if err != nil {
			d.logger.Error("❌ 获取账户信息失败", "address", addrStr, "error", err)
			continue
		}

		currentBalance := big.NewInt(0)
		if accountInfo != nil && accountInfo.Balance != nil {
			currentBalance = accountInfo.Balance
		}

		d.logger.Info("🔍 验证节点奖励分发 - 获取账户余额",
			"address", addrStr,
			"currentBalance", currentBalance.String(),
			"说明", "验证节点获取到验证者的当前余额")

		// 计算新余额
		newBalance := new(big.Int).Add(currentBalance, amount)

		// 创建余额更新对象
		obj := &state.Object{
			Address: addr,
			Balance: newBalance,
		}
		objects = append(objects, obj)

		d.logger.Info("💰 准备更新余额",
			"address", addrStr,
			"reward", amount.String(),
			"beforeBalance", currentBalance.String(),
			"afterBalance", newBalance.String())
	}

	// 提交状态更新
	if len(objects) > 0 {
		d.logger.Info("🔍 验证节点奖励分发 - 开始提交状态更新",
			"objectCount", len(objects),
			"说明", "验证节点开始提交所有余额更新到状态")

		newSnapshot, newStateRoot, err := snapshot.Commit(objects)
		if err != nil {
			return fmt.Errorf("failed to commit state changes: %w", err)
		}

		d.logger.Info("✅ 验证节点奖励分发 - 状态更新提交成功",
			"newStateRoot", fmt.Sprintf("0x%x", newStateRoot),
			"newSnapshot", newSnapshot != nil,
			"说明", "验证节点状态更新提交成功，准备更新区块头状态根")

		// 更新状态根（模拟交易执行后的状态根更新）
		oldStateRoot := currentHeader.StateRoot
		currentHeader.StateRoot = types.BytesToHash(newStateRoot)
		currentHeader.ComputeHash()

		// 🆕 关键修复：将新状态根同步到区块链的当前状态
		// 这样后续的状态根比对就会使用更新后的状态根
		if err := d.syncStateRootToBlockchain(currentHeader, newStateRoot); err != nil {
			d.logger.Error("❌ 同步状态根到区块链失败", "error", err)
			return fmt.Errorf("failed to sync state root to blockchain: %w", err)
		}

		d.logger.Info("🔍 验证节点奖励分发 - 状态根更新详情",
			"oldStateRoot", oldStateRoot.String(),
			"newStateRoot", currentHeader.StateRoot.String(),
			"newStateRootHex", fmt.Sprintf("0x%x", newStateRoot),
			"说明", "验证节点区块头状态根已更新并同步到区块链")

		// 🆕 同步节点状态根更新显著日志标志
		d.logger.Info("🔄🔄🔄 ========== 同步节点状态根更新完成 ========== 🔄🔄🔄",
			"blockNumber", currentHeader.Number,
			"oldStateRoot", oldStateRoot.String(),
			"newStateRoot", currentHeader.StateRoot.String(),
			"newStateRootHex", fmt.Sprintf("0x%x", newStateRoot),
			"updateCount", len(objects),
			"newSnapshot", newSnapshot != nil,
			"note", "同步节点已模拟交易执行更新状态根，奖励分发完成")

		// 🆕 显著日志：验证节点状态根对比
		d.logger.Info("🔍🔍🔍 ========== 验证节点状态根对比 ========== 🔍🔍🔍",
			"blockNumber", currentHeader.Number,
			"区块头状态根", blockHeader.StateRoot.String(),
			"本地计算状态根", currentHeader.StateRoot.String(),
			"状态根是否一致", blockHeader.StateRoot == currentHeader.StateRoot,
			"说明", "对比区块头中的状态根与验证节点本地计算的状态根")
	} else {
		d.logger.Info("⚠️ 验证节点奖励分发 - 没有需要更新的对象",
			"说明", "验证节点没有找到需要更新的余额对象")
	}

	return nil
}
