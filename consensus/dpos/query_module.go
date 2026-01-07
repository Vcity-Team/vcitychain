package dpos

import (
	"fmt"

	querymodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/query"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// initQueryModule initializes the query manager using the shared module implementation.
func (d *DPoS) initQueryModule() {
	if d.query != nil {
		return
	}

	deps := d.buildQueryDependencies()
	d.query = querymodule.NewManager(deps)

	// 🆕 初始化奖励迁移（只执行一次）
	d.initializeRewardMigration()
}

// buildQueryDependencies prepares the dependency set for the query module.
func (d *DPoS) buildQueryDependencies() querymodule.Dependencies {
	deps := querymodule.Dependencies{
		ConsensusSwitchHeight: d.config.ConsensusSwitchHeight,
		EpochSize:             d.getEpochSize(),
		BlockTime:             d.config.BlockTime.Duration,
		Logger:                d.logger.Named("modules.query"),
	}

	deps.GetCurrentBlockNumber = func() uint64 {
		if d.config != nil && d.config.Blockchain != nil {
			if header := d.config.Blockchain.Header(); header != nil {
				return header.Number
			}
		}
		return 0
	}

	// 注入GetHeaderByNumber依赖
	deps.GetHeaderByNumber = func(blockNumber uint64) (*types.Header, bool) {
		if d.config != nil && d.config.Blockchain != nil {
			return d.config.Blockchain.GetHeaderByNumber(blockNumber)
		}
		return nil, false
	}

	if d.epoch != nil {
		deps.EpochManager = d.epoch
	}

	deps.GetValidatorFaultInfo = func(address types.Address) map[string]interface{} {
		return d.getValidatorFaultInfo(address)
	}

	deps.GetSortedValidatorsWithLimit = func() ([]querymodule.ValidatorInfo, error) {
		validators, err := d.GetSortedValidatorsWithLimit()
		if err != nil || len(validators) == 0 {
			return convertValidatorSet(validators), err
		}
		return convertValidatorSet(validators), nil
	}

	// 注入GetValidatorsFromEpochStartBlock依赖
	deps.GetValidatorsFromEpochStartBlock = func(epochNumber uint64) ([]querymodule.ValidatorInfo, error) {
		// 计算epoch开始区块号
		consensusSwitchHeight := d.config.ConsensusSwitchHeight
		epochSize := d.getEpochSize()

		var epochStartBlock uint64
		if epochNumber == 0 {
			epochStartBlock = 0
		} else {
			epochStartBlock = consensusSwitchHeight + (epochNumber-1)*epochSize
		}

		// 获取epoch开始区块的区块头
		if d.config == nil || d.config.Blockchain == nil {
			return nil, fmt.Errorf("blockchain not available to get validators for epoch %d", epochNumber)
		}

		header, exists := d.config.Blockchain.GetHeaderByNumber(epochStartBlock)
		if !exists || header == nil {
			return nil, fmt.Errorf("epoch %d start block %d not found", epochNumber, epochStartBlock)
		}

		// 从ExtraData读取验证者集合
		extra, err := GetDposExtra(header.ExtraData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ExtraData for epoch %d start block %d: %w", epochNumber, epochStartBlock, err)
		}

		// 检查ExtraData中是否有验证者集合
		if extra.Validators == nil || len(extra.Validators.Added) == 0 {
			return nil, fmt.Errorf("validators set is missing in ExtraData for epoch %d start block %d", epochNumber, epochStartBlock)
		}

		// 转换为ValidatorInfo格式
		validators := extra.Validators.Added
		return convertValidatorSet(validators), nil
	}

	if d.blockTracker != nil {
		deps.GetEpochBlockCounts = func(epochNumber uint64) map[types.Address]uint64 {
			return d.blockTracker.GetEpochBlockCounts(epochNumber)
		}
		deps.GetTotalEpochBlocks = func(epochNumber uint64) uint64 {
			return d.blockTracker.GetTotalEpochBlocks(epochNumber)
		}
	}

	return deps
}

// convertValidatorSet transforms validator.AccountSet into the lightweight module representation.
func convertValidatorSet(set validator.AccountSet) []querymodule.ValidatorInfo {
	if len(set) == 0 {
		return []querymodule.ValidatorInfo{}
	}

	result := make([]querymodule.ValidatorInfo, 0, len(set))
	for _, v := range set {
		info := querymodule.ValidatorInfo{
			Address:     v.Address,
			VotingPower: v.VotingPower.String(),
			IsActive:    v.IsActive,
		}
		result = append(result, info)
	}

	return result
}

// initializeRewardMigration 初始化奖励迁移（只执行一次）
// 通过metadata bucket的标志位确保重启后不会重复执行
func (d *DPoS) initializeRewardMigration() {
	if d.state == nil || d.state.StakeStore == nil || d.state.RewardStore == nil {
		return // 如果state未初始化，跳过迁移
	}

	// 检查是否已经迁移过（通过metadata bucket的标志位判断）
	migrationFlag := []byte("reward_migration_completed")
	migrationCompleted := false

	// 检查迁移标志位
	err := d.state.db.View(func(tx *bolt.Tx) error {
		metaBucket := tx.Bucket([]byte("metadata"))
		if metaBucket == nil {
			// metadata bucket不存在，说明还没有迁移过
			return fmt.Errorf("metadata bucket not found")
		}

		flag := metaBucket.Get(migrationFlag)
		if flag != nil && string(flag) == "true" {
			migrationCompleted = true
			return nil
		}

		return fmt.Errorf("migration not completed")
	})

	// 如果已经迁移过，直接返回
	if err == nil && migrationCompleted {
		d.logger.Info("ℹ️ 奖励迁移已完成，跳过（重启后检查）")
		return
	}

	if err != nil {
		d.logger.Info("ℹ️ 奖励迁移标志未找到，执行首次迁移", "reason", err.Error())
	}

	// 执行迁移（即使没有奖励也不会出错）
	d.logger.Info("🔄 开始执行奖励数据迁移...")
	migrationErr := d.state.StakeStore.MigrateCumulativeRewards(d.state.RewardStore, d.logger)
	
	// 无论迁移成功还是失败（包括没有奖励记录的正常情况），都设置标志位
	// 原因：
	// 1. MigrateCumulativeRewards 在没有奖励记录时会返回 nil（正常情况）
	// 2. 即使返回错误，迁移函数内部也有保护机制（不会重复计算已迁移的地址）
	// 3. 设置标志位可以避免重启后重复检查，提高效率
	setFlagErr := d.state.db.Update(func(tx *bolt.Tx) error {
		metaBucket, err := tx.CreateBucketIfNotExists([]byte("metadata"))
		if err != nil {
			return err
		}
		return metaBucket.Put(migrationFlag, []byte("true"))
	})
	
	if setFlagErr != nil {
		d.logger.Warn("⚠️ 设置迁移标志位失败", "error", setFlagErr)
		// 标志位设置失败不影响启动，但下次重启还会执行迁移检查
	} else {
		d.logger.Info("ℹ️ 奖励迁移标志位已写入metadata bucket")
	}
	
	if migrationErr != nil {
		d.logger.Warn("⚠️ 奖励迁移过程中出现错误（但已设置标志位，避免重复检查）", "error", migrationErr)
	} else {
		d.logger.Info("✅ 奖励数据迁移完成")
	}
}
