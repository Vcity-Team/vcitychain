package dpos

import (
	"fmt"
	"math/big"

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

	// 🆕 初始化 DelegateInfo 迁移（只执行一次）
	// 为所有已注册但没有 DelegateInfo 的受托人创建初始 DelegateInfo
	d.initializeDelegateInfoMigration()

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

// initializeDelegateInfoMigration 初始化 DelegateInfo 迁移（只执行一次）
// 为所有已注册但没有 DelegateInfo 的受托人创建初始 DelegateInfo
func (d *DPoS) initializeDelegateInfoMigration() {
	d.logger.Info("🔍 [DelegateInfo迁移] 开始检查迁移状态...")
	
	if d.state == nil || d.state.StakeStore == nil || d.state.RegistrationStore == nil {
		d.logger.Warn("⚠️ [DelegateInfo迁移] State未初始化，跳过迁移",
			"state", d.state != nil,
			"stakeStore", d.state != nil && d.state.StakeStore != nil,
			"registrationStore", d.state != nil && d.state.RegistrationStore != nil)
		return // 如果state未初始化，跳过迁移
	}

	d.logger.Info("✅ [DelegateInfo迁移] State已初始化，继续检查迁移标志位")

	// 检查是否已经迁移过（通过metadata bucket的标志位判断）
	migrationFlag := []byte("delegate_info_migration_completed")
	migrationCompleted := false

	// 检查迁移标志位
	err := d.state.db.View(func(tx *bolt.Tx) error {
		metaBucket := tx.Bucket([]byte("metadata"))
		if metaBucket == nil {
			// metadata bucket不存在，说明还没有迁移过
			d.logger.Info("ℹ️ [DelegateInfo迁移] metadata bucket不存在，需要执行首次迁移")
			return fmt.Errorf("metadata bucket not found")
		}

		d.logger.Info("✅ [DelegateInfo迁移] metadata bucket存在，检查标志位")
		flag := metaBucket.Get(migrationFlag)
		if flag != nil && string(flag) == "true" {
			migrationCompleted = true
			d.logger.Info("✅ [DelegateInfo迁移] 标志位已存在且为true，迁移已完成")
			return nil
		}

		d.logger.Info("ℹ️ [DelegateInfo迁移] 标志位不存在或不为true，需要执行迁移",
			"flagValue", func() string {
				if flag != nil {
					return string(flag)
				}
				return "nil"
			}())
		return fmt.Errorf("migration not completed")
	})

	// 如果已经迁移过，直接返回
	if err == nil && migrationCompleted {
		d.logger.Info("ℹ️ [DelegateInfo迁移] 迁移已完成，跳过（重启后检查）")
		return
	}

	if err != nil {
		d.logger.Info("ℹ️ [DelegateInfo迁移] 迁移标志未找到，执行首次迁移", "reason", err.Error())
	}

	// 执行迁移
	d.logger.Info("🔄 [DelegateInfo迁移] 开始执行 DelegateInfo 迁移...")
	migrationErr := d.migrateDelegateInfos()

	// 无论迁移成功还是失败，都设置标志位
	d.logger.Info("💾 [DelegateInfo迁移] 开始设置迁移标志位...")
	setFlagErr := d.state.db.Update(func(tx *bolt.Tx) error {
		metaBucket, err := tx.CreateBucketIfNotExists([]byte("metadata"))
		if err != nil {
			d.logger.Error("❌ [DelegateInfo迁移] 创建metadata bucket失败", "error", err)
			return err
		}
		d.logger.Info("✅ [DelegateInfo迁移] metadata bucket已创建或已存在")
		
		if err := metaBucket.Put(migrationFlag, []byte("true")); err != nil {
			d.logger.Error("❌ [DelegateInfo迁移] 写入标志位失败", "error", err)
			return err
		}
		
		d.logger.Info("✅ [DelegateInfo迁移] 标志位已写入", "flag", string(migrationFlag))
		return nil
	})

	if setFlagErr != nil {
		d.logger.Warn("⚠️ [DelegateInfo迁移] 设置迁移标志位失败", "error", setFlagErr)
	} else {
		d.logger.Info("✅ [DelegateInfo迁移] 迁移标志位已成功写入metadata bucket")
	}

	if migrationErr != nil {
		d.logger.Warn("⚠️ [DelegateInfo迁移] 迁移过程中出现错误（但已设置标志位）", "error", migrationErr)
	} else {
		d.logger.Info("✅ [DelegateInfo迁移] 迁移完成，无错误")
	}
}

// migrateDelegateInfos 为所有已注册但没有 DelegateInfo 的受托人创建初始 DelegateInfo
func (d *DPoS) migrateDelegateInfos() error {
	d.logger.Info("🔍 [DelegateInfo迁移] 开始执行迁移逻辑...")
	
	if d.state == nil || d.state.StakeStore == nil || d.state.RegistrationStore == nil {
		d.logger.Error("❌ [DelegateInfo迁移] State store不可用",
			"state", d.state != nil,
			"stakeStore", d.state != nil && d.state.StakeStore != nil,
			"registrationStore", d.state != nil && d.state.RegistrationStore != nil)
		return fmt.Errorf("state store not available")
	}

	d.logger.Info("✅ [DelegateInfo迁移] State store可用，开始获取所有已注册的受托人")

	// 获取所有已注册的受托人
	allRegistrations, err := d.state.RegistrationStore.GetAllRegistrations()
	if err != nil {
		d.logger.Warn("⚠️ [DelegateInfo迁移] 获取注册信息失败", "error", err)
		return nil // 不阻断，只记录警告
	}

	d.logger.Info("📊 [DelegateInfo迁移] 获取注册信息完成", "count", len(allRegistrations))

	if len(allRegistrations) == 0 {
		d.logger.Info("ℹ️ [DelegateInfo迁移] 没有找到已注册的受托人，跳过迁移")
		return nil
	}

	d.logger.Info("📋 [DelegateInfo迁移] 开始遍历已注册的受托人", "totalCount", len(allRegistrations))

	// 统计信息
	createdCount := 0
	skippedCount := 0
	errorCount := 0

	// 为每个已注册的受托人检查并创建 DelegateInfo
	for i, reg := range allRegistrations {
		if reg == nil {
			d.logger.Warn("⚠️ [DelegateInfo迁移] 跳过nil注册记录", "index", i)
			continue
		}

		d.logger.Info("🔍 [DelegateInfo迁移] 处理受托人",
			"index", i+1,
			"total", len(allRegistrations),
			"address", reg.Address.String(),
			"name", reg.Name)

		// 检查是否已有 DelegateInfo
		d.logger.Info("🔍 [DelegateInfo迁移] 检查受托人是否已有DelegateInfo", "address", reg.Address.String())
		existingInfo, err := d.state.StakeStore.GetDelegateInfo(reg.Address)
		if err != nil {
			d.logger.Info("ℹ️ [DelegateInfo迁移] 查询DelegateInfo时出错（可能不存在）",
				"address", reg.Address.String(),
				"error", err)
		}
		
		if err == nil && existingInfo != nil {
			// 已有 DelegateInfo，跳过
			skippedCount++
			d.logger.Info("⏭️ [DelegateInfo迁移] 受托人已有DelegateInfo，跳过",
				"address", reg.Address.String(),
				"votingPower", func() string {
					if existingInfo.VotingPower != nil {
						return existingInfo.VotingPower.String()
					}
					return "nil"
				}(),
				"isRegistered", existingInfo.IsRegistered,
				"isActive", existingInfo.IsActive)
			continue
		}

		d.logger.Info("🆕 [DelegateInfo迁移] 受托人没有DelegateInfo，开始创建",
			"address", reg.Address.String(),
			"name", reg.Name)

		// 创建初始 DelegateInfo（投票权重为0）
		delegateInfo := &DelegateInfo{
			Address:        reg.Address,
			VotingPower:    big.NewInt(0), // 初始投票权重为0
			TotalVotes:     big.NewInt(0), // 初始总投票数为0
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       false, // 初始为非活跃，需要投票激活
			IsRegistered:   true,  // 已注册
			RegistrationInfo: reg, // 保存注册信息
			BlsPublicKey:    nil,  // BLS密钥由其他逻辑处理
		}

		d.logger.Info("📝 [DelegateInfo迁移] DelegateInfo对象已创建",
			"address", reg.Address.String(),
			"votingPower", "0",
			"isRegistered", true,
			"isActive", false)

		// 填充佣金字段
		d.logger.Info("💰 [DelegateInfo迁移] 开始填充佣金字段", "address", reg.Address.String())
		d.populateCommissionFields(reg.Address, delegateInfo)
		d.logger.Info("✅ [DelegateInfo迁移] 佣金字段填充完成",
			"address", reg.Address.String(),
			"commissionRate", delegateInfo.CommissionRate)

		// 保存到数据库
		d.logger.Info("💾 [DelegateInfo迁移] 开始保存DelegateInfo到数据库", "address", reg.Address.String())
		if err := d.state.StakeStore.setDelegateInfo(reg.Address, delegateInfo, nil); err != nil {
			errorCount++
			d.logger.Error("❌ [DelegateInfo迁移] 保存DelegateInfo失败",
				"address", reg.Address.String(),
				"error", err)
			continue
		}

		createdCount++
		d.logger.Info("✅ [DelegateInfo迁移] 为已注册受托人创建初始DelegateInfo成功",
			"address", reg.Address.String(),
			"name", reg.Name,
			"votingPower", "0",
			"createdCount", createdCount)
	}

	d.logger.Info("📊 [DelegateInfo迁移] 迁移统计",
		"totalRegistrations", len(allRegistrations),
		"created", createdCount,
		"skipped", skippedCount,
		"errors", errorCount)

	if createdCount > 0 {
		d.logger.Info("✅ [DelegateInfo迁移] 成功创建了DelegateInfo", "count", createdCount)
	}
	if skippedCount > 0 {
		d.logger.Info("⏭️ [DelegateInfo迁移] 跳过了已有DelegateInfo的受托人", "count", skippedCount)
	}
	if errorCount > 0 {
		d.logger.Warn("⚠️ [DelegateInfo迁移] 迁移过程中出现错误", "count", errorCount)
	}

	return nil
}
