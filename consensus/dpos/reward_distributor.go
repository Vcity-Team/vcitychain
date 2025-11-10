package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
)

// RewardDistributor 奖励分发器
type RewardDistributor struct {
	state          *state.Txn
	rewardAccount  types.Address
	rewardAmount   *big.Int
	validatorRatio uint64
	voterRatio     uint64
	blockTracker   *BlockProductionTracker
	logger         hclog.Logger
}

// NewRewardDistributor 创建奖励分发器
func NewRewardDistributor(
	state *state.Txn,
	rewardAccount types.Address,
	rewardAmount *big.Int,
	validatorRatio uint64,
	voterRatio uint64,
	blockTracker *BlockProductionTracker,
	logger hclog.Logger,
) *RewardDistributor {
	return &RewardDistributor{
		state:          state,
		rewardAccount:  rewardAccount,
		rewardAmount:   rewardAmount,
		validatorRatio: validatorRatio,
		voterRatio:     voterRatio,
		blockTracker:   blockTracker,
		logger:         logger,
	}
}

// DistributeEpochRewards 分发Epoch奖励
func (rd *RewardDistributor) DistributeEpochRewards(
	epochNumber uint64,
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
) error {
	rd.logger.Info("🚀 ========== 开始分发Epoch奖励 ==========",
		"epoch", epochNumber,
		"rewardAccount", rd.rewardAccount.String(),
		"totalReward", rd.rewardAmount.String(),
		"timestamp", time.Now().Format("2006-01-02 15:04:05"))

	// 1. 检查奖励账户余额
	rewardBalance := rd.state.GetBalance(rd.rewardAccount)
	if rewardBalance.Cmp(rd.rewardAmount) < 0 {
		return fmt.Errorf("insufficient reward balance: have %s, need %s",
			rewardBalance.String(), rd.rewardAmount.String())
	}

	// 2. 获取该epoch的出块统计
	blockCounts := rd.blockTracker.GetEpochBlockCounts(epochNumber)
	totalBlocks := rd.blockTracker.GetTotalEpochBlocks(epochNumber)

	if totalBlocks == 0 {
		rd.logger.Warn("⚠️ Epoch没有出块记录，跳过奖励分发", "epoch", epochNumber)
		return nil
	}

	// 3. 计算总投票权重
	totalVotingPower := rd.calculateTotalVotingPower(voters)

	// 4. 计算奖励分配
	rewards := rd.calculateRewards(validators, voters, blockCounts, totalBlocks, totalVotingPower)

	// 5. 批量状态更新
	return rd.batchUpdateBalances(rewards, epochNumber)
}

// calculateTotalVotingPower 计算总投票权重
func (rd *RewardDistributor) calculateTotalVotingPower(voters map[types.Address]*VoterInfo) *big.Int {
	totalPower := big.NewInt(0)
	for _, voter := range voters {
		totalPower.Add(totalPower, voter.VotingPower)
	}
	return totalPower
}

// calculateRewards 计算奖励
func (rd *RewardDistributor) calculateRewards(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
	totalVotingPower *big.Int,
) map[types.Address]*big.Int {
	rewards := make(map[types.Address]*big.Int)

	// 1. 验证者奖励：按出块次数分配
	validatorReward := new(big.Int).Mul(rd.rewardAmount, big.NewInt(int64(rd.validatorRatio)))
	validatorReward.Div(validatorReward, big.NewInt(100))

	rd.logger.Info("🏭 计算验证者奖励",
		"validatorRatio", rd.validatorRatio,
		"validatorReward", validatorReward.String())

	for _, validator := range validators {
		if validator.IsActive {
			blocksProduced := blockCounts[validator.Address]
			if blocksProduced > 0 {
				reward := new(big.Int).Mul(validatorReward, big.NewInt(int64(blocksProduced)))
				reward.Div(reward, big.NewInt(int64(totalBlocks)))
				rewards[validator.Address] = reward

				rd.logger.Debug("🏭 验证者奖励",
					"validator", validator.Address.String(),
					"blocksProduced", blocksProduced,
					"totalBlocks", totalBlocks,
					"reward", reward.String())
			}
		}
	}

	// 2. 投票者奖励：按投票权重分配
	voterReward := new(big.Int).Mul(rd.rewardAmount, big.NewInt(int64(rd.voterRatio)))
	voterReward.Div(voterReward, big.NewInt(100))

	rd.logger.Info("🗳️ 计算投票者奖励",
		"voterRatio", rd.voterRatio,
		"voterReward", voterReward.String())

	if totalVotingPower.Cmp(big.NewInt(0)) > 0 {
		for staker, voter := range voters {
			if voter.VotingPower.Cmp(big.NewInt(0)) > 0 {
				reward := new(big.Int).Mul(voterReward, voter.VotingPower)
				reward.Div(reward, totalVotingPower)

				// 🆕 如果地址已存在（是验证者），累加奖励而不是覆盖
				if existingReward, exists := rewards[staker]; exists {
					// 该地址既是验证者又是投票者，累加奖励
					rewards[staker] = new(big.Int).Add(existingReward, reward)
					rd.logger.Debug("🗳️ 投票者奖励（累加验证者奖励）",
						"staker", staker.String(),
						"validatorReward", existingReward.String(),
						"voterReward", reward.String(),
						"totalReward", rewards[staker].String())
				} else {
					// 该地址只是投票者，直接设置
					rewards[staker] = reward
					rd.logger.Debug("🗳️ 投票者奖励",
						"staker", staker.String(),
						"votingPower", voter.VotingPower.String(),
						"totalVotingPower", totalVotingPower.String(),
						"reward", reward.String())
				}
			}
		}
	}

	return rewards
}

// CalculateRewards 计算奖励（公开方法，用于外部调用）
// 与 calculateRewards 功能相同，但不需要 state，只返回计算结果
func (rd *RewardDistributor) CalculateRewards(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) map[types.Address]*big.Int {
	// 计算总投票权重
	totalVotingPower := rd.calculateTotalVotingPower(voters)

	// 调用内部方法计算奖励（验证者 + 投票者，已支持累加）
	return rd.calculateRewards(validators, voters, blockCounts, totalBlocks, totalVotingPower)
}

// batchUpdateBalances 批量更新余额
func (rd *RewardDistributor) batchUpdateBalances(
	rewards map[types.Address]*big.Int,
	epochNumber uint64,
) error {
	totalDistributed := big.NewInt(0)
	distributionCount := 0

	// 批量处理所有奖励
	for recipient, amount := range rewards {
		if amount.Cmp(big.NewInt(0)) > 0 {
			// 增加接收者余额
			rd.state.AddBalance(recipient, amount)

			// 累计已分发金额
			totalDistributed.Add(totalDistributed, amount)
			distributionCount++

			rd.logger.Debug("💸 分发奖励",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	// 从奖励账户扣除总金额
	if err := rd.state.SubBalance(rd.rewardAccount, totalDistributed); err != nil {
		return fmt.Errorf("failed to deduct from reward account: %w", err)
	}

	rd.logger.Info("✅ Epoch奖励分发完成",
		"epoch", epochNumber,
		"recipients", distributionCount,
		"totalDistributed", totalDistributed.String(),
		"remainingBalance", rd.state.GetBalance(rd.rewardAccount).String())

	return nil
}
