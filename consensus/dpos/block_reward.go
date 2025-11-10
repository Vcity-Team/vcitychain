package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// executeRewardDistributionForEpochEnd 在epoch最后一个区块时执行奖励分发
func (r *dposRuntime) executeRewardDistributionForEpochEnd(blockNumber uint64, currentRound uint64, blockProducer types.Address) error {
	r.logger.Debug("🎉 ========== 生成节点中开始预先计算epoch结束时的奖励分发 ==========",
		"blockNumber", blockNumber,
		"blockProducer", blockProducer.String(),
		"timestamp", time.Now().Format("2006-01-02 15:04:05"))

	// 获取DPoS实例
	if r.config == nil || r.config.dposBackend == nil {
		return fmt.Errorf("DPoS配置不可用")
	}

	dposInstance, ok := r.config.dposBackend.(*DPoS)
	if !ok {
		return fmt.Errorf("无法获取DPoS实例")
	}
	currentEpochNumber := dposInstance.epochManager.GetCurrentEpoch(blockNumber)
	rewardEpoch := currentEpochNumber

	if rewardEpoch == 0 {
		r.logger.Info("ℹ️ 第一个epoch，无需分发奖励",
			"currentEpoch", currentEpochNumber,
			"blockNumber", blockNumber)
		return nil
	}

	// 🆕 关键修复：在计算奖励之前，先记录epoch结束区块本身的出块
	// 因为该区块还没有被处理，所以出块记录还没有被记录到blockTracker中
	if dposInstance.blockTracker != nil {
		blockTime := time.Now()
		dposInstance.blockTracker.RecordBlockProduction(
			blockNumber,
			blockTime,
			blockProducer,
			rewardEpoch,
		)
		r.logger.Info("📊 已记录epoch结束区块的出块",
			"blockNumber", blockNumber,
			"blockProducer", blockProducer.String(),
			"epoch", rewardEpoch)
	}

	// 直接计算和分发奖励
	return dposInstance.distributeEpochRewards(rewardEpoch, currentRound)
}

// processRewardDistributionInBlockForBuilder 在区块构建器中处理奖励分发
func (r *dposRuntime) processRewardDistributionInBlockForBuilder(builder blockBuilder, blockNumber uint64) error {
	r.logger.Info("🎯 生产节点开始执行奖励分配",
		"blockNumber", blockNumber,
		"说明", "生产节点在buildBlock时执行奖励分配")

	// 获取DPoS实例
	if r.config == nil || r.config.dposBackend == nil {
		return fmt.Errorf("DPoS backend not available")
	}

	dposInstance, ok := r.config.dposBackend.(*DPoS)
	if !ok {
		return fmt.Errorf("failed to cast dposBackend to DPoS")
	}

	// 检查是否有待处理的奖励分配信息
	if dposInstance.pendingRewardDistribution == nil {
		r.logger.Info("ℹ️ 没有待处理的奖励分配信息", "blockNumber", blockNumber)
		return nil
	}

	rewardInfo := dposInstance.pendingRewardDistribution
	r.logger.Info("💰 验证节点中开始执行奖励分配",
		"blockNumber", blockNumber,
		"epochNumber", rewardInfo.EpochNumber,
		"rewardCount", len(rewardInfo.Rewards))

	// 获取状态
	state := builder.GetState()
	if state == nil {
		return fmt.Errorf("failed to get state from builder")
	}

	// 使用TotalReward字段
	totalReward := rewardInfo.TotalReward
	if totalReward == nil {
		totalReward = big.NewInt(0)
	}

	// 检查奖励账户余额
	rewardAccount := dposInstance.config.RewardAccount
	currentBalance := state.GetBalance(rewardAccount)

	r.logger.Info("💰 奖励账户余额检查",
		"address", rewardAccount.String(),
		"currentBalance", currentBalance.String(),
		"totalReward", totalReward.String())

	if currentBalance.Cmp(totalReward) < 0 {
		return fmt.Errorf("insufficient balance for reward distribution: have %s, need %s",
			currentBalance.String(), totalReward.String())
	}

	// 从奖励账户扣除总奖励
	state.Txn().SubBalance(rewardAccount, totalReward)
	r.logger.Info("✅ 从奖励账户扣除总奖励",
		"address", rewardAccount.String(),
		"amount", totalReward.String())

	// 分配奖励给验证者
	for addrStr, amount := range rewardInfo.Rewards {
		addr := types.StringToAddress(addrStr)
		state.Txn().AddBalance(addr, amount)
		r.logger.Info("✅ 验证者余额增加",
			"to", addr.String(),
			"amount", amount.String())
	}

	r.logger.Info("🎉 生产节点奖励分配完成",
		"blockNumber", blockNumber,
		"rewardCount", len(rewardInfo.Rewards))

	return nil
}




