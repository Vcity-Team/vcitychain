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

// RewardDistributor 奖励分发器
type RewardDistributor struct {
	calculator *RewardCalculator
	state      *State
	logger     hcf.Logger
}

// NewRewardDistributor 创建奖励分发器
func NewRewardDistributor(calculator *RewardCalculator, state *State, logger hcf.Logger) *RewardDistributor {
	return &RewardDistributor{
		calculator: calculator,
		state:      state,
		logger:     logger,
	}
}

// DistributeBlockReward 分发区块奖励
func (rd *RewardDistributor) DistributeBlockReward(block *types.FullBlock, proposer types.Address, totalVotingPower *big.Int) error {
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
func (rd *RewardDistributor) DistributeEpochReward(epochNumber uint64, validators validator.AccountSet, voters map[types.Address]*VoterInfo) error {
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
func (rd *RewardDistributor) saveRewardRecord(record *RewardRecord) error {
	// 这里应该调用数据库存储方法
	// 暂时使用日志记录
	rd.logger.Debug("saving reward record",
		"staker", record.Staker.String(),
		"amount", record.Amount.String(),
		"type", record.Type)

	return nil
}

// saveEpochRewards 保存周期奖励信息
func (rd *RewardDistributor) saveEpochRewards(rewards *EpochRewards) error {
	// 这里应该调用数据库存储方法
	// 暂时使用日志记录
	rd.logger.Debug("saving epoch rewards",
		"epoch", rewards.EpochNumber,
		"total_rewards", rewards.TotalRewards.String(),
		"distributions", len(rewards.Distributions))

	return nil
}

// updateVoterRewards 更新投票者奖励
func (rd *RewardDistributor) updateVoterRewards(staker types.Address, reward *big.Int) error {
	// 这里应该更新投票者的奖励余额
	// 暂时使用日志记录
	rd.logger.Debug("updating voter rewards",
		"staker", staker.String(),
		"reward", reward.String())

	return nil
}

// GetRewardHistory 获取奖励历史
func (rd *RewardDistributor) GetRewardHistory(staker types.Address, limit int) ([]*RewardRecord, error) {
	// 这里应该从数据库获取奖励历史
	// 暂时返回空列表
	return []*RewardRecord{}, nil
}

// GetEpochRewards 获取周期奖励信息
func (rd *RewardDistributor) GetEpochRewards(epochNumber uint64) (*EpochRewards, error) {
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
func (rd *RewardDistributor) GetTotalRewards(staker types.Address) (*big.Int, error) {
	// 这里应该从数据库计算总奖励
	// 暂时返回0
	return big.NewInt(0), nil
}
