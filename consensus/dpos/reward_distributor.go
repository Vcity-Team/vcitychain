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

	distributionEpoch                 uint64
	isCommissionRemovedAtEpoch        func(epochNumber uint64) bool
	isStakeWeightPoolSplitAtEpoch     func(epochNumber uint64) bool
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

// GetRewardAmount 返回当前 Epoch 奖池（最近一次 UpdateRewardAmount 的值）
func (rd *RewardDistributor) GetRewardAmount() *big.Int {
	if rd.rewardAmount == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(rd.rewardAmount)
}

// SetCommissionRemovedAtEpochChecker 设置按 epoch 判断是否已关闭佣金。
func (rd *RewardDistributor) SetCommissionRemovedAtEpochChecker(fn func(epochNumber uint64) bool) {
	rd.isCommissionRemovedAtEpoch = fn
}

// SetStakeWeightPoolSplitAtEpochChecker 设置按 epoch 判断是否启用 SR 质押权重分配 voter 池。
func (rd *RewardDistributor) SetStakeWeightPoolSplitAtEpochChecker(fn func(epochNumber uint64) bool) {
	rd.isStakeWeightPoolSplitAtEpoch = fn
}

// SetRewardAccount 设置本 epoch 奖励扣款账户（治理切换后按 epoch 解析）。
func (rd *RewardDistributor) SetRewardAccount(addr types.Address) {
	rd.rewardAccount = addr
}

// SetDistributionEpoch 设置当前奖励计算/分发对应的 epoch。
func (rd *RewardDistributor) SetDistributionEpoch(epochNumber uint64) {
	rd.distributionEpoch = epochNumber
}

func (rd *RewardDistributor) commissionDisabledForDistribution() bool {
	if rd.isCommissionRemovedAtEpoch == nil {
		return false
	}
	return rd.isCommissionRemovedAtEpoch(rd.distributionEpoch)
}

func (rd *RewardDistributor) useStakeWeightPoolSplit() bool {
	if rd.isStakeWeightPoolSplitAtEpoch == nil {
		return false
	}
	return rd.isStakeWeightPoolSplitAtEpoch(rd.distributionEpoch)
}

// WeightedAverageCommissionBps 按出块数加权平均佣金率（基点）。
func (rd *RewardDistributor) WeightedAverageCommissionBps(
	validators validator.AccountSet,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) uint64 {
	if rd.commissionDisabledForDistribution() || totalBlocks == 0 {
		return 0
	}
	weighted := big.NewInt(0)
	for _, v := range validators {
		blocks := blockCounts[v.Address]
		if blocks == 0 {
			continue
		}
		c := rd.getCommissionRate(v.Address)
		part := new(big.Int).Mul(big.NewInt(int64(blocks)), big.NewInt(int64(c)))
		weighted.Add(weighted, part)
	}
	avg := new(big.Int).Div(weighted, new(big.Int).SetUint64(totalBlocks))
	if !avg.IsUint64() {
		return rd.commissionDefault
	}
	return avg.Uint64()
}

func (rd *RewardDistributor) getCommissionRate(address types.Address) uint64 {
	if rd.commissionDisabledForDistribution() {
		return 0
	}
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

// voterStakeOnDelegate returns the voter's effective stake on a specific SR (delegate).
func voterStakeOnDelegate(voter *VoterInfo, delegate types.Address) *big.Int {
	if voter == nil || voter.DelegateVotes == nil {
		return nil
	}
	amount, ok := voter.DelegateVotes[delegate]
	if !ok || amount == nil || amount.Sign() <= 0 {
		return nil
	}
	return new(big.Int).Set(amount)
}

func (rd *RewardDistributor) computeVoterWeights(
	validator types.Address,
	voters map[types.Address]*VoterInfo,
) (map[types.Address]*big.Int, *big.Int) {
	weights := make(map[types.Address]*big.Int)
	total := big.NewInt(0)

	for voterAddress, voter := range voters {
		weight := voterStakeOnDelegate(voter, validator)
		if weight == nil || weight.Sign() == 0 {
			continue
		}

		weights[voterAddress] = weight
		total.Add(total, weight)
	}

	return weights, total
}

// srDelegatedStake returns effective delegated stake on an SR (voter weights first, else VotingPower).
func (rd *RewardDistributor) srDelegatedStake(
	v *validator.ValidatorMetadata,
	voters map[types.Address]*VoterInfo,
) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	_, total := rd.computeVoterWeights(v.Address, voters)
	if total.Sign() > 0 {
		return total
	}
	if v.VotingPower != nil && v.VotingPower.Sign() > 0 {
		return new(big.Int).Set(v.VotingPower)
	}
	return big.NewInt(0)
}

// computeExpectedBlocksForValidator mirrors fault/block stats: fair share of effective epoch blocks.
func computeExpectedBlocksForValidator(
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
	validators validator.AccountSet,
) uint64 {
	if totalBlocks == 0 || len(validators) == 0 {
		return 0
	}

	active := make(map[types.Address]struct{}, len(validators))
	activeCount := uint64(0)
	for _, v := range validators {
		if v == nil || !v.IsActive {
			continue
		}
		if v.VotingPower == nil || v.VotingPower.Sign() <= 0 {
			continue
		}
		active[v.Address] = struct{}{}
		activeCount++
	}
	if activeCount == 0 {
		activeCount = 1
	}

	removedBlocks := uint64(0)
	for addr, count := range blockCounts {
		if _, ok := active[addr]; !ok {
			removedBlocks += count
		}
	}
	effectiveBlocks := totalBlocks - removedBlocks
	if effectiveBlocks == 0 {
		return 0
	}

	expected := effectiveBlocks / activeCount
	if expected == 0 {
		expected = 1
	}
	return expected
}

// blockCompletionBps returns completion in basis points (10000 = 100%).
func blockCompletionBps(actualBlocks, expectedBlocks uint64) uint64 {
	if expectedBlocks == 0 || actualBlocks == 0 {
		return 0
	}
	if actualBlocks >= expectedBlocks {
		return 10000
	}
	return actualBlocks * 10000 / expectedBlocks
}

// computeValidatorPoolAllocations splits epoch voter pool R between SRs.
// Before activation epoch: proportional to block count. After activation: stake weight × completion.
func (rd *RewardDistributor) computeValidatorPoolAllocations(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) map[types.Address]*big.Int {
	if !rd.useStakeWeightPoolSplit() {
		return rd.computeValidatorPoolAllocationsByBlocks(validators, blockCounts, totalBlocks)
	}

	rd.logger.Info("📊 voter 池 SR 间分配：质押权重×完成度",
		"epoch", rd.distributionEpoch,
		"totalPool", rd.rewardAmount.String(),
		"totalBlocks", totalBlocks)

	return rd.computeValidatorPoolAllocationsByStakeWeight(validators, voters, blockCounts, totalBlocks)
}

func (rd *RewardDistributor) computeValidatorPoolAllocationsByBlocks(
	validators validator.AccountSet,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) map[types.Address]*big.Int {
	allocations := make(map[types.Address]*big.Int)
	if rd.rewardAmount == nil || rd.rewardAmount.Sign() == 0 || totalBlocks == 0 {
		return allocations
	}

	totalBlocksBig := new(big.Int).SetUint64(totalBlocks)
	for _, v := range validators {
		if v == nil || !v.IsActive {
			continue
		}
		blocksProduced := blockCounts[v.Address]
		if blocksProduced == 0 {
			continue
		}
		pool := new(big.Int).Mul(new(big.Int).SetUint64(blocksProduced), rd.rewardAmount)
		pool.Div(pool, totalBlocksBig)
		if pool.Sign() > 0 {
			allocations[v.Address] = pool
		}
	}
	return allocations
}

func (rd *RewardDistributor) computeValidatorPoolAllocationsByStakeWeight(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) map[types.Address]*big.Int {
	allocations := make(map[types.Address]*big.Int)
	if rd.rewardAmount == nil || rd.rewardAmount.Sign() == 0 || totalBlocks == 0 {
		return allocations
	}

	expectedBlocks := computeExpectedBlocksForValidator(blockCounts, totalBlocks, validators)
	totalWeighted := big.NewInt(0)
	weightedStake := make(map[types.Address]*big.Int)

	for _, v := range validators {
		if v == nil || !v.IsActive {
			continue
		}
		stake := rd.srDelegatedStake(v, voters)
		if stake.Sign() == 0 {
			continue
		}
		actual := blockCounts[v.Address]
		completionBps := blockCompletionBps(actual, expectedBlocks)
		if completionBps == 0 {
			continue
		}

		weighted := new(big.Int).Mul(stake, big.NewInt(int64(completionBps)))
		weighted.Div(weighted, big.NewInt(10000))
		if weighted.Sign() == 0 {
			continue
		}
		weightedStake[v.Address] = weighted
		totalWeighted.Add(totalWeighted, weighted)
	}

	if totalWeighted.Sign() == 0 {
		return allocations
	}

	for addr, weighted := range weightedStake {
		pool := new(big.Int).Mul(rd.rewardAmount, weighted)
		pool.Div(pool, totalWeighted)
		if pool.Sign() > 0 {
			allocations[addr] = pool
		}
	}
	return allocations
}

func (rd *RewardDistributor) computeRewardsForValidator(
	validator *validator.ValidatorMetadata,
	voters map[types.Address]*VoterInfo,
	validators validator.AccountSet,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) (*big.Int, map[types.Address]*big.Int) {
	validatorAmount := big.NewInt(0)
	voterRewards := make(map[types.Address]*big.Int)

	if validator == nil || !validator.IsActive || totalBlocks == 0 {
		return validatorAmount, voterRewards
	}

	pools := rd.computeValidatorPoolAllocations(validators, voters, blockCounts, totalBlocks)
	validatorPool := pools[validator.Address]
	if validatorPool == nil || validatorPool.Sign() == 0 {
		return validatorAmount, voterRewards
	}

	return rd.splitValidatorPoolReward(validator, voters, validatorPool)
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

	rd.SetDistributionEpoch(epochNumber)

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

	poolAllocations := rd.computeValidatorPoolAllocations(validators, voters, blockCounts, totalBlocks)
	for _, v := range validators {
		if v == nil {
			continue
		}
		pool := poolAllocations[v.Address]
		if pool == nil || pool.Sign() == 0 {
			continue
		}
		validatorAmount, voterRewards := rd.splitValidatorPoolReward(v, voters, pool)

		rd.addReward(rewards, v.Address, validatorAmount)

		for voterAddr, share := range voterRewards {
			rd.addReward(rewards, voterAddr, share)
		}
	}

	return rewards
}

// splitValidatorPoolReward splits one SR's voter-pool share between commission and voters.
func (rd *RewardDistributor) splitValidatorPoolReward(
	v *validator.ValidatorMetadata,
	voters map[types.Address]*VoterInfo,
	validatorPool *big.Int,
) (*big.Int, map[types.Address]*big.Int) {
	validatorAmount := big.NewInt(0)
	voterRewards := make(map[types.Address]*big.Int)

	if v == nil || validatorPool == nil || validatorPool.Sign() == 0 {
		return validatorAmount, voterRewards
	}

	validatorReward := new(big.Int).Set(validatorPool)

	commissionRate := rd.getCommissionRate(v.Address)
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

	voterWeights, totalWeight := rd.computeVoterWeights(v.Address, voters)
	if totalWeight.Sign() == 0 {
		rd.logger.Info("⚠️ [奖励计算] 验证者没有投票者权重，所有可分配奖励归验证者",
			"validator", v.Address.String(),
			"distributable", distributable.String(),
			"voterCount", len(voters))
		validatorAmount.Add(validatorAmount, distributable)
		return validatorAmount, voterRewards
	}

	allocated := big.NewInt(0)
	for voterAddr, weight := range voterWeights {
		if weight == nil || weight.Sign() == 0 {
			rd.logger.Info("⚠️ [奖励计算] 投票者分红被跳过（权重为nil或0）",
				"voter", voterAddr.String(),
				"validator", v.Address.String(),
				"weight", func() string {
					if weight != nil {
						return weight.String()
					}
					return "nil"
				}())
			continue
		}

		share := new(big.Int).Mul(distributable, weight)
		share.Div(share, totalWeight)

		if share.Sign() == 0 {
			rd.logger.Info("⚠️ [奖励计算] 投票者分红被跳过（计算后分红为0）",
				"voter", voterAddr.String(),
				"validator", v.Address.String(),
				"weight", weight.String(),
				"distributable", distributable.String(),
				"totalWeight", totalWeight.String(),
				"calculatedShare", share.String())
			continue
		}

		voterRewards[voterAddr] = share
		allocated.Add(allocated, share)
	}

	remainder := new(big.Int).Sub(distributable, allocated)
	if remainder.Sign() > 0 {
		validatorAmount.Add(validatorAmount, remainder)
	}

	return validatorAmount, voterRewards
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
