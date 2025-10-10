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

// TronRewardDistributor TRON模式奖励分发器
type TronRewardDistributor struct {
	state          *state.Txn
	rewardAccount  types.Address
	rewardAmount   *big.Int
	validatorRatio uint64
	voterRatio     uint64
	blockTracker   *BlockProductionTracker
	logger         hclog.Logger
}

// NewTronRewardDistributor 创建TRON模式奖励分发器
func NewTronRewardDistributor(
	state *state.Txn,
	rewardAccount types.Address,
	rewardAmount *big.Int,
	validatorRatio uint64,
	voterRatio uint64,
	blockTracker *BlockProductionTracker,
	logger hclog.Logger,
) *TronRewardDistributor {
	return &TronRewardDistributor{
		state:          state,
		rewardAccount:  rewardAccount,
		rewardAmount:   rewardAmount,
		validatorRatio: validatorRatio,
		voterRatio:     voterRatio,
		blockTracker:   blockTracker,
		logger:         logger,
	}
}

// DistributeEpochRewards 分发Epoch奖励（TRON模式）
func (trd *TronRewardDistributor) DistributeEpochRewards(
	epochNumber uint64,
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
) error {
	trd.logger.Info("🚀 ========== 开始分发Epoch奖励（TRON模式）==========",
		"epoch", epochNumber,
		"rewardAccount", trd.rewardAccount.String(),
		"totalReward", trd.rewardAmount.String(),
		"timestamp", time.Now().Format("2006-01-02 15:04:05"))

	// 1. 检查奖励账户余额
	rewardBalance := trd.state.GetBalance(trd.rewardAccount)
	if rewardBalance.Cmp(trd.rewardAmount) < 0 {
		return fmt.Errorf("insufficient reward balance: have %s, need %s",
			rewardBalance.String(), trd.rewardAmount.String())
	}

	// 2. 获取该epoch的出块统计
	blockCounts := trd.blockTracker.GetEpochBlockCounts(epochNumber)
	totalBlocks := trd.blockTracker.GetTotalEpochBlocks(epochNumber)

	if totalBlocks == 0 {
		trd.logger.Warn("⚠️ Epoch没有出块记录，跳过奖励分发", "epoch", epochNumber)
		return nil
	}

	// 3. 计算总投票权重
	totalVotingPower := trd.calculateTotalVotingPower(voters)

	// 4. 计算奖励分配
	rewards := trd.calculateTronRewards(validators, voters, blockCounts, totalBlocks, totalVotingPower)

	// 5. 批量状态更新
	return trd.batchUpdateBalances(rewards, epochNumber)
}

// calculateTotalVotingPower 计算总投票权重
func (trd *TronRewardDistributor) calculateTotalVotingPower(voters map[types.Address]*VoterInfo) *big.Int {
	totalPower := big.NewInt(0)
	for _, voter := range voters {
		totalPower.Add(totalPower, voter.VotingPower)
	}
	return totalPower
}

// calculateTronRewards 计算TRON模式奖励
func (trd *TronRewardDistributor) calculateTronRewards(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
	totalVotingPower *big.Int,
) map[types.Address]*big.Int {
	rewards := make(map[types.Address]*big.Int)

	// 1. 验证者奖励：按出块次数分配
	validatorReward := new(big.Int).Mul(trd.rewardAmount, big.NewInt(int64(trd.validatorRatio)))
	validatorReward.Div(validatorReward, big.NewInt(100))

	trd.logger.Info("🏭 计算验证者奖励",
		"validatorRatio", trd.validatorRatio,
		"validatorReward", validatorReward.String())

	for _, validator := range validators {
		if validator.IsActive {
			blocksProduced := blockCounts[validator.Address]
			if blocksProduced > 0 {
				reward := new(big.Int).Mul(validatorReward, big.NewInt(int64(blocksProduced)))
				reward.Div(reward, big.NewInt(int64(totalBlocks)))
				rewards[validator.Address] = reward

				trd.logger.Debug("🏭 验证者奖励",
					"validator", validator.Address.String(),
					"blocksProduced", blocksProduced,
					"totalBlocks", totalBlocks,
					"reward", reward.String())
			}
		}
	}

	// 2. 投票者奖励：按投票权重分配
	voterReward := new(big.Int).Mul(trd.rewardAmount, big.NewInt(int64(trd.voterRatio)))
	voterReward.Div(voterReward, big.NewInt(100))

	trd.logger.Info("🗳️ 计算投票者奖励",
		"voterRatio", trd.voterRatio,
		"voterReward", voterReward.String())

	if totalVotingPower.Cmp(big.NewInt(0)) > 0 {
		for staker, voter := range voters {
			if voter.VotingPower.Cmp(big.NewInt(0)) > 0 {
				reward := new(big.Int).Mul(voterReward, voter.VotingPower)
				reward.Div(reward, totalVotingPower)
				rewards[staker] = reward

				trd.logger.Debug("🗳️ 投票者奖励",
					"staker", staker.String(),
					"votingPower", voter.VotingPower.String(),
					"totalVotingPower", totalVotingPower.String(),
					"reward", reward.String())
			}
		}
	}

	return rewards
}

// batchUpdateBalances 批量更新余额
func (trd *TronRewardDistributor) batchUpdateBalances(
	rewards map[types.Address]*big.Int,
	epochNumber uint64,
) error {
	totalDistributed := big.NewInt(0)
	distributionCount := 0

	// 批量处理所有奖励
	for recipient, amount := range rewards {
		if amount.Cmp(big.NewInt(0)) > 0 {
			// 增加接收者余额
			trd.state.AddBalance(recipient, amount)

			// 累计已分发金额
			totalDistributed.Add(totalDistributed, amount)
			distributionCount++

			trd.logger.Debug("💸 分发奖励",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	// 从奖励账户扣除总金额
	if err := trd.state.SubBalance(trd.rewardAccount, totalDistributed); err != nil {
		return fmt.Errorf("failed to deduct from reward account: %w", err)
	}

	trd.logger.Info("✅ Epoch奖励分发完成（TRON模式）",
		"epoch", epochNumber,
		"recipients", distributionCount,
		"totalDistributed", totalDistributed.String(),
		"remainingBalance", trd.state.GetBalance(trd.rewardAccount).String())

	return nil
}
