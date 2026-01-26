package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
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

	// 添加详细的出块统计日志
	d.logger.Info("🔍 检查出块统计",
		"epoch", epochNumber,
		"blockCounts", blockCounts,
		"totalBlocks", totalBlocks,
		"validatorsCount", len(validators))

	// 详细打印每个验证者的出块记录
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
		d.logger.Info("🔍 [Epoch奖励诊断] totalBlocks=0，跳过奖励计算",
			"epoch", epochNumber,
			"blockTrackerIsNil", d.blockTracker == nil,
			"rewardDistributorIsNil", d.rewardDistributor == nil)
		return nil
	}

	// 优先从参数系统读取 dpos_reward_amount（经过治理流程修改的值是权威数据源）
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

	// 更新 RewardDistributor 的奖励金额（确保使用最新值）
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

	// 使用 RewardDistributor 统一计算奖励（验证者 + 投票者）
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

	// 3. 使用 Reward 管理器计算奖励
	var rewards map[types.Address]*big.Int
	if d.reward != nil {
		moduleRewards, err := d.reward.CalculateRewards(epochNumber)
		if err != nil {
			d.logger.Warn("⚠️ 模块化奖励计算失败",
				"epoch", epochNumber,
				"error", err)
		} else {
			rewards = moduleRewards
		}
	}

	if rewards == nil && d.rewardDistributor != nil {
		rewards = d.rewardDistributor.CalculateRewards(
			validators,
			voters,
			blockCounts,
			totalBlocks,
		)
	}

	if rewards == nil {
		rewards = map[types.Address]*big.Int{}
	}

	// 4. 准备状态更新（用于奖励分发）
	stateUpdates := make(map[types.Address]*big.Int)
	for address, totalReward := range rewards {
		if totalReward.Sign() > 0 {
			stateUpdates[address] = totalReward
		}
	}

	// 不再在生产节点记录奖励，改为在同步节点记录（避免重复）
	// 奖励信息已写入 pendingRewardDistribution，将在 buildBlock 时写入 ExtraData
	// 同步节点在 processRewardDistributionInBlock 中会记录奖励

	// 在epoch结束区块准备奖励分发信息（不直接执行状态更新）
	if len(stateUpdates) > 0 {

		// 计算总奖励金额
		totalReward := big.NewInt(0)
		for _, reward := range stateUpdates {
			totalReward.Add(totalReward, reward)
		}

		// 计算验证者-投票者映射关系（用于记录到数据库）
		// 为每个验证者-投票者组合创建单独记录（支持投票者投票给多个验证者）
		voterRewards := []*VoterRewardDetail{}
		if d.rewardDistributor != nil && d.blockTracker != nil && totalBlocks > 0 {
			// 为每个验证者计算投票者奖励详情
			for _, validator := range validators {
				_, voterRewardsForValidator := d.rewardDistributor.computeRewardsForValidator(validator, voters, blockCounts, totalBlocks)
				// 获取投票权重
				voterWeights, _ := d.rewardDistributor.computeVoterWeights(validator.Address, voters)
				for voterAddr, share := range voterRewardsForValidator {
					if share.Sign() > 0 {
						// 获取该投票者的权重
						var voteWeight *big.Int
						if weight, exists := voterWeights[voterAddr]; exists && weight != nil {
							voteWeight = new(big.Int).Set(weight)
						} else {
							voteWeight = big.NewInt(0)
						}
						voterRewards = append(voterRewards, &VoterRewardDetail{
							VoterAddress:     voterAddr.String(),
							ValidatorAddress: validator.Address.String(),
							Amount:           new(big.Int).Set(share),
							VoteWeight:       voteWeight,
						})
					}
				}
			}
		} else {
			d.logger.Warn("⚠️ 无法计算VoterRewards",
				"epoch", epochNumber,
				"rewardDistributorIsNil", d.rewardDistributor == nil,
				"blockTrackerIsNil", d.blockTracker == nil,
				"totalBlocks", totalBlocks)
			d.logger.Info("🔍 [Epoch奖励诊断] 无法计算VoterRewards，VoterRewards将为空",
				"epoch", epochNumber,
				"rewardDistributorIsNil", d.rewardDistributor == nil,
				"blockTrackerIsNil", d.blockTracker == nil,
				"totalBlocks", totalBlocks,
				"voterCount", len(voters))
		}
		
		d.logger.Info("🔍 [Epoch奖励诊断] 准备奖励分发信息",
			"epoch", epochNumber,
			"voterRewardCount", len(voterRewards),
			"rewardCount", len(stateUpdates),
			"totalReward", totalReward.String())

		// 不直接执行状态更新，而是将奖励分发信息存储到pendingRewardDistribution
		// 这样buildBlock可以将其添加到ExtraData中，然后在区块执行时处理
		d.pendingRewardDistribution = &RewardDistributionInfo{
			EpochNumber:  epochNumber,
			Rewards:      make(map[string]*big.Int),
			VoterRewards: voterRewards,
			TotalReward:  totalReward,
			Timestamp:    uint64(time.Now().Unix()),
		}

		// 转换地址格式
		for address, reward := range stateUpdates {
			d.pendingRewardDistribution.Rewards[address.String()] = reward
		}

		d.logger.Info("✅ 准备奖励分发信息（包含验证者-投票者映射）",
			"epoch", epochNumber,
			"rewardCount", len(stateUpdates),
			"voterRewardCount", len(voterRewards),
			"totalReward", totalReward.String())
	} else {
		d.logger.Warn("⚠️ 没有奖励分发信息", "epoch", epochNumber)
	}

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

	var rewards map[types.Address]*big.Int
	if d.reward != nil {
		moduleRewards, err := d.reward.CalculateRewards(epochNumber)
		if err != nil {
			d.logger.Warn("⚠️ 模块化奖励计算失败",
				"epoch", epochNumber,
				"error", err)
		} else {
			rewards = moduleRewards
		}
	}

	if rewards == nil && d.rewardDistributor != nil {
		rewards = d.rewardDistributor.CalculateRewards(validators, voters, blockCounts, totalBlocks)
	}

	if rewards == nil {
		rewards = map[types.Address]*big.Int{}
	}
	if len(rewards) == 0 {
		d.logger.Warn("⚠️ 计算结果为空，跳过奖励记录", "epoch", epochNumber)
		return nil
	}

	// 不再在生产节点记录奖励，改为在同步节点记录（避免重复）
	// 奖励信息将在同步节点处理区块时记录

	// 延迟状态更新机制已移除，奖励分发在epoch结束区块直接执行

	return nil
}

// getBlocksProducedInEpoch 获取验证者在指定epoch中生产的区块数
func (d *DPoS) getBlocksProducedInEpoch(address types.Address, epochNumber uint64) uint64 {
	// 简化实现：返回固定值，实际应该从区块跟踪器中获取
	return 8 // 假设每个验证者生产8个区块
}
