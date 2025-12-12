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
	state                 *state.Txn
	rewardAccount         types.Address
	rewardAmount          *big.Int
	blockTracker          *BlockProductionTracker
	stakeStore            *StakeStore
	commissionDefault     uint64
	commissionEffective   time.Duration
	commissionDenominator *big.Int
	logger                hclog.Logger
}

// NewRewardDistributor 创建奖励分发器
func NewRewardDistributor(
	state *state.Txn,
	rewardAccount types.Address,
	rewardAmount *big.Int,
	blockTracker *BlockProductionTracker,
	stakeStore *StakeStore,
	commissionDefault uint64,
	commissionEffective time.Duration,
	logger hclog.Logger,
) *RewardDistributor {
	return &RewardDistributor{
		state:                 state,
		rewardAccount:         rewardAccount,
		rewardAmount:          rewardAmount,
		blockTracker:          blockTracker,
		stakeStore:            stakeStore,
		commissionDefault:     commissionDefault,
		commissionEffective:   commissionEffective,
		commissionDenominator: big.NewInt(10000),
		logger:                logger,
	}
}

// UpdateRewardAmount 更新奖励金额（用于提案执行后更新参数值）
func (rd *RewardDistributor) UpdateRewardAmount(rewardAmount *big.Int) {
	rd.rewardAmount = rewardAmount
}

func (rd *RewardDistributor) getCommissionRate(address types.Address) uint64 {
	if rd.stakeStore == nil {
		return rd.commissionDefault
	}

	info, err := rd.stakeStore.GetDelegateInfo(address)
	if err != nil {
		rd.logger.Debug("⚠️ 获取受托人佣金信息失败，使用默认值",
			"delegate", address.String(),
			"error", err)
		return rd.commissionDefault
	}

	if info == nil {
		return rd.commissionDefault
	}

	effectivePeriod := rd.commissionEffective
	if effectivePeriod <= 0 {
		effectivePeriod = 21 * 24 * time.Hour
	}

	commissionRate := info.CommissionRate
	if commissionRate == 0 {
		commissionRate = rd.commissionDefault
	}

	if info.PendingCommissionRate != 0 {
		now := uint64(time.Now().Unix())
		effectiveSeconds := uint64(effectivePeriod.Seconds())
		canApply := info.CommissionUpdateTime == 0 || effectiveSeconds == 0 || now >= info.CommissionUpdateTime+effectiveSeconds

		if canApply {
			commissionRate = info.PendingCommissionRate
			info.CommissionRate = commissionRate
			info.PendingCommissionRate = 0
			info.CommissionUpdateTime = now

			if err := rd.stakeStore.setDelegateInfo(address, info, nil); err != nil {
				rd.logger.Error("❌ 转正佣金率失败",
					"delegate", address.String(),
					"error", err)
			} else {
				rd.logger.Info("⏳ 佣金率已自动转正",
					"delegate", address.String(),
					"commissionRate", commissionRate)
			}
		}
	}

	if commissionRate > 10000 {
		return 10000
	}

	return commissionRate
}

func (rd *RewardDistributor) addReward(rewards map[types.Address]*big.Int, address types.Address, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}

	if existing, ok := rewards[address]; ok {
		rewards[address] = new(big.Int).Add(existing, amount)
	} else {
		rewards[address] = new(big.Int).Set(amount)
	}
}

func containsDelegate(delegates []types.Address, target types.Address) bool {
	for _, addr := range delegates {
		if addr == target {
			return true
		}
	}
	return false
}

func (rd *RewardDistributor) computeVoterWeights(
	validator types.Address,
	voters map[types.Address]*VoterInfo,
) (map[types.Address]*big.Int, *big.Int) {
	weights := make(map[types.Address]*big.Int)
	total := big.NewInt(0)

	for voterAddress, voter := range voters {
		if voter == nil || voter.VotingPower == nil || voter.VotingPower.Sign() == 0 {
			continue
		}

		if !containsDelegate(voter.VotedDelegates, validator) {
			continue
		}

		delegateCount := len(voter.VotedDelegates)
		if delegateCount == 0 {
			continue
		}

		weight := new(big.Int).Set(voter.VotingPower)
		if delegateCount > 1 {
			weight.Div(weight, big.NewInt(int64(delegateCount)))
		}

		if weight.Sign() == 0 {
			continue
		}

		weights[voterAddress] = weight
		total.Add(total, weight)
	}

	return weights, total
}

func (rd *RewardDistributor) computeRewardsForValidator(
	validator *validator.ValidatorMetadata,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) (*big.Int, map[types.Address]*big.Int) {
	validatorAmount := big.NewInt(0)
	voterRewards := make(map[types.Address]*big.Int)

	if validator == nil || !validator.IsActive || totalBlocks == 0 {
		return validatorAmount, voterRewards
	}

	blocksProduced := blockCounts[validator.Address]
	if blocksProduced == 0 {
		return validatorAmount, voterRewards
	}

	validatorBlocks := new(big.Int).SetUint64(blocksProduced)
	totalBlocksBig := new(big.Int).SetUint64(totalBlocks)

	validatorReward := new(big.Int).Mul(validatorBlocks, rd.rewardAmount)
	validatorReward.Div(validatorReward, totalBlocksBig)

	if validatorReward.Sign() == 0 {
		return validatorAmount, voterRewards
	}

	commissionRate := rd.getCommissionRate(validator.Address)
	if commissionRate > 10000 {
		commissionRate = 10000
	}

	commissionAmount := new(big.Int).Mul(validatorReward, big.NewInt(int64(commissionRate)))
	commissionAmount.Div(commissionAmount, rd.commissionDenominator)
	if commissionAmount.Sign() > 0 {
		validatorAmount.Add(validatorAmount, commissionAmount)
	}

	distributable := new(big.Int).Sub(validatorReward, commissionAmount)
	if distributable.Sign() <= 0 {
		return validatorAmount, voterRewards
	}

	voterWeights, totalWeight := rd.computeVoterWeights(validator.Address, voters)
	if totalWeight.Sign() == 0 {
		validatorAmount.Add(validatorAmount, distributable)
		return validatorAmount, voterRewards
	}

	allocated := big.NewInt(0)
	for voterAddr, weight := range voterWeights {
		if weight == nil || weight.Sign() == 0 {
			continue
		}

		share := new(big.Int).Mul(distributable, weight)
		share.Div(share, totalWeight)

		if share.Sign() == 0 {
			continue
		}

		voterRewards[voterAddr] = share
		allocated.Add(allocated, share)
	}

	remainder := new(big.Int).Sub(distributable, allocated)
	if remainder.Sign() > 0 {
		validatorAmount.Add(validatorAmount, remainder)
	}

	rd.logger.Info("🏭 验证者奖励计算详情",
		"validator", validator.Address.String(),
		"blocksProduced", blocksProduced,
		"totalReward", validatorReward.String(),
		"commissionRate", commissionRate,
		"commissionAmount", commissionAmount.String(),
		"distributedToVoters", distributable.String(),
		"voterCount", len(voterRewards))

	return validatorAmount, voterRewards
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

	// 4. 计算奖励分配
	rewards := rd.calculateRewards(validators, voters, blockCounts, totalBlocks)

	// 5. 批量状态更新
	return rd.batchUpdateBalances(rewards, epochNumber)
}

// calculateRewards 计算奖励
func (rd *RewardDistributor) calculateRewards(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) map[types.Address]*big.Int {
	rewards := make(map[types.Address]*big.Int)

	if totalBlocks == 0 {
		return rewards
	}

	for _, validator := range validators {
		validatorAmount, voterRewards := rd.computeRewardsForValidator(validator, voters, blockCounts, totalBlocks)

		rd.addReward(rewards, validator.Address, validatorAmount)

		for voterAddr, share := range voterRewards {
			rd.addReward(rewards, voterAddr, share)
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
	return rd.calculateRewards(validators, voters, blockCounts, totalBlocks)
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
