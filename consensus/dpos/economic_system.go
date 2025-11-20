package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// ==================== DPoS经济系统相关函数 ====================

// initializeEconomicSystem 初始化经济系统组件
func (d *DPoS) initializeEconomicSystem() error {
	d.logger.Info("💰 开始初始化DPoS经济系统组件")

	// 1. 初始化时间基础Epoch管理器
	d.epochManager = NewTimeBasedEpochManager(
		d.config.EpochDuration,
		d.config.BlockTime.Duration, // 🆕 传递blockTime配置
		d.config.RewardAccount,
		d.config.RewardAmount,
		d.config.ConsensusSwitchHeight,
		d.logger.Named("epoch_manager"),
	)

	// 🆕 设置区块链引用，用于基于区块高度计算Epoch
	d.epochManager.SetBlockchain(d.config.Blockchain)

	// 🆕 设置回调函数
	d.epochManager.SetCallback(d.handleEpochSwitch)

	// 2. 初始化出块统计管理器
	d.blockTracker = NewBlockProductionTracker(
		d.logger.Named("block_tracker"),
		d.state.BlockTrackerStore,   // 🆕 传递数据库存储
		d.config.BlockTime.Duration, // 🆕 传递blockTime配置
	)

	// 3. 初始化奖励分发器
	var stakeStore *StakeStore
	if d.state != nil {
		stakeStore = d.state.StakeStore
	}

	if d.config.CommissionRateDefault == 0 {
		d.config.CommissionRateDefault = 1000
	}
	if d.config.CommissionEffectivePeriod == 0 {
		d.config.CommissionEffectivePeriod = 21 * 24 * time.Hour
	}

	d.rewardDistributor = NewRewardDistributor(
		nil, // 状态将在运行时设置
		d.config.RewardAccount,
		d.config.RewardAmount,
		d.blockTracker,
		stakeStore,
		d.config.CommissionRateDefault,
		d.config.CommissionEffectivePeriod,
		d.logger.Named("reward_distributor"),
	)

	// 4. 🆕 初始化固定时间窗口调度器
	d.blockScheduler = NewBlockScheduler(
		d.config.BlockTime.Duration,
		int(d.config.DelegateCount),
		d.config.Blockchain,            // 传入区块链接口（*blockchain.Blockchain实现了BlockchainInterface）
		d.config.ConsensusSwitchHeight, // 传入共识切换高度
		d.logger.Named("block_scheduler"),
	)

	// 添加调试日志
	d.logger.Info("🔧 BlockScheduler初始化",
		"blockWindow", d.config.BlockTime.Duration.String(),
		"delegateCount", d.config.DelegateCount,
		"blockchain", d.config.Blockchain != nil)

	d.logger.Info("✅ DPoS经济系统组件初始化完成",
		"epochDuration", d.config.EpochDuration.String(),
		"rewardAccount", d.config.RewardAccount.String(),
		"rewardAmount", d.config.RewardAmount.String(),
		"commissionDefault", d.config.CommissionRateDefault,
		"commissionEffectivePeriod", d.config.CommissionEffectivePeriod.String())

	return nil
}

// handleEpochSwitch 处理epoch切换回调
func (d *DPoS) handleEpochSwitch(epochNumber uint64) error {
	currentTime := time.Now()
	d.logger.Info("🔄 处理epoch切换",
		"epoch", epochNumber,
		"currentTime", currentTime.Format("2006-01-02 15:04:05"))

	// 1. 启动新epoch的时间调度
	if d.blockScheduler != nil {
		d.blockScheduler.StartNewEpoch(epochNumber, currentTime)
	}

	// 2. 计算和记录上一个epoch的奖励（延迟状态更新）
	if epochNumber > 1 {
		previousEpoch := epochNumber - 1

		if err := d.calculateAndRecordEpochRewards(previousEpoch); err != nil {
			d.logger.Error("❌ 计算Epoch奖励失败", "epoch", previousEpoch, "error", err)
			return err
		}
	}

	return nil
}

// processEconomicSystem 处理经济系统逻辑
func (d *DPoS) processEconomicSystem(block *types.FullBlock) error {
	// 检查组件是否初始化
	if d.epochManager == nil || d.blockTracker == nil || d.rewardDistributor == nil {
		d.logger.Warn("⚠️ 经济系统组件未初始化，跳过处理",
			"epochManagerIsNil", d.epochManager == nil,
			"blockTrackerIsNil", d.blockTracker == nil,
			"rewardDistributorIsNil", d.rewardDistributor == nil)
		return nil
	}

	blockNumber := block.Block.Number()
	blockTime := time.Unix(int64(block.Block.Header.Timestamp), 0)
	blockProducer := types.BytesToAddress(block.Block.Header.Miner)

	// 🆕 修改：使用统一的Epoch管理器（现在基于区块高度计算）
	currentEpoch := d.epochManager.GetCurrentEpoch(blockNumber)

	// 1. 记录出块统计（使用统一的Epoch）
	d.blockTracker.RecordBlockProduction(
		blockNumber,
		blockTime,
		blockProducer,
		currentEpoch, // 🆕 使用统一的Epoch管理器
	)

	// 2. 添加详细日志
	d.logger.Debug("📊 记录出块到统一Epoch",
		"blockNumber", blockNumber,
		"blockTime", blockTime.Format("2006-01-02 15:04:05"),
		"currentEpoch", currentEpoch,
		"blockProducer", blockProducer.String()[:16])

	return nil
}

// calculateAndRecordEpochRewards 已迁移到 rewards.go
