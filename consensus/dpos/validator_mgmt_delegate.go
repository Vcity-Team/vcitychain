package dpos

import (
	"bytes"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"go.etcd.io/bbolt"
)

// initializeDelegates 初始化受托人集合
func (d *DPoS) initializeDelegates() error {
	d.delegates = make(validator.AccountSet, 0, d.config.DelegateCount)

	// 🆕 初始化故障检测相关字段
	d.faultyValidators = make(map[types.Address]bool)
	d.missedBlocksCount = make(map[types.Address]uint64)

	// 🆕 第一层保护：从数据库加载 currentEpoch（在重置为0之前）
	if d.state != nil && d.state.StakeStore != nil {
		if savedEpoch, err := d.state.StakeStore.LoadCurrentEpoch(); err == nil {
			d.currentEpoch = savedEpoch
			d.logger.Info("✅ 从数据库恢复currentEpoch（第一层保护：防止重复检测）",
				"epoch", savedEpoch,
				"note", "重启后恢复已检测的epoch，避免重复检测")
		} else {
			d.logger.Warn("⚠️ 从数据库加载currentEpoch失败，使用默认值0",
				"error", err,
				"note", "首次启动或数据库中没有记录")
			d.currentEpoch = 0
		}
	} else {
		d.currentEpoch = 0
		d.logger.Warn("⚠️ StakeStore不可用，currentEpoch使用默认值0")
	}

	// 🆕 显著日志：显示当前配置
	d.logger.Debug("🚀 ===== DPoS验证者初始化开始 =====")
	d.logger.Debug("📋 当前配置信息",
		"configDelegateCount", d.config.DelegateCount,
		"initialDelegatesCount", len(d.config.InitialDelegates),
		"dposValidatorsCount", d.config.DPoSValidatorsCount,
		"currentEpoch", d.currentEpoch)

	// 🆕 首先尝试从数据库读取受托人（真正用于出块）
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Info("🔍 开始从数据库读取验证者信息...")
		// 🆕 使用公共函数获取排序和限制后的验证者（包含故障过滤）
		dbValidators, err := d.GetSortedValidatorsWithLimit()
		if err != nil {
			d.logger.Warn("⚠️ 从数据库读取受托人失败，将使用创世文件", "error", err)
		} else if len(dbValidators) > 0 {
			d.logger.Info("✅ 从数据库成功读取验证者", "count", len(dbValidators))

			d.logger.Info("📊 数据库验证者详细信息:")
			for i, validator := range dbValidators {
				// 🆕 获取验证者的故障标志信息
				faultInfo := d.getValidatorFaultInfo(validator.Address)
				d.logger.Info("👤 验证者信息",
					"index", i+1,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"faultFlag", faultInfo) // 🆕 添加故障标志信息
			}

			// 🆕 按权重倒序排序
			d.logger.Info("🔄 开始按权重倒序排序验证者...")
			sortValidatorsByVotingPower(dbValidators)

			// 🆕 显著日志：显示排序后的验证者
			d.logger.Info("📈 排序后的验证者列表:")
			for i, validator := range dbValidators {
				d.logger.Info("🏆 排序后验证者",
					"rank", i+1,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String())
			}

			// 🆕 显著日志：显示截取逻辑
			maxDelegates := int(d.config.DPoSValidatorsCount)
			originalCount := len(dbValidators)

			d.logger.Info("🎯 ===== 验证者截取逻辑 =====")
			d.logger.Info("📊 截取前统计",
				"配置的最大验证者数量", maxDelegates,
				"数据库中的验证者数量", originalCount)

			if originalCount <= maxDelegates {
				d.logger.Info("✅ 数据库验证者数量 <= 配置数量，取全部验证者",
					"取用数量", originalCount,
					"配置数量", maxDelegates)
			} else {
				d.logger.Info("✂️ 数据库验证者数量 > 配置数量，截取前N个",
					"截取前", maxDelegates,
					"原始数量", originalCount,
					"截取后", maxDelegates)
			}

			// 应用限制
			if maxDelegates > 0 && originalCount > maxDelegates {
				dbValidators = dbValidators[:maxDelegates]
			}

			// 设置到内存
			d.delegates = dbValidators
			d.logger.Info("✅ 验证者集合已从数据库加载并设置到内存",
				"最终数量", len(d.delegates))

			// 🆕 初始化创世验证者映射（从创世块或配置）
			d.initializeGenesisValidatorsMap()

			return nil
		}
	}

	// 如果数据库没有，尝试从创世块解析
	d.logger.Info("🔍 开始从创世块解析验证者...")
	if err := d.parseValidatorsFromGenesis(); err != nil {
		d.logger.Warn("⚠️ 从创世块解析验证者失败", "error", err)
		// 如果创世块也失败，使用配置中的初始验证者
		d.logger.Info("🔄 回退到配置中的初始验证者")
		for _, genesisValidator := range d.config.InitialDelegates {
			votingPower := DefaultVotingPower() // 1000 VCITY
			delegate := &validator.ValidatorMetadata{
				Address:     types.Address(genesisValidator.Address),
				VotingPower: votingPower,
				IsActive:    true,
			}
			d.delegates = append(d.delegates, delegate)
		}
		// 🆕 初始化创世验证者映射（从配置）
		d.initializeGenesisValidatorsMap()
	}

	d.logger.Info("✅ 验证者初始化完成", "count", len(d.delegates))
	return nil
}

// addDelegateSafely 安全地添加受托人（检查重复）
func (d *DPoS) addDelegateSafely(newDelegate *validator.ValidatorMetadata) {
	// 检查是否已存在相同地址的验证者
	for i, existingDelegate := range d.delegates {
		if existingDelegate.Address == newDelegate.Address {
			// 累计权重 - 修复：使用正确的加法方式
			oldPower := new(big.Int).Set(existingDelegate.VotingPower)
			existingDelegate.VotingPower = new(big.Int).Add(existingDelegate.VotingPower, newDelegate.VotingPower)
			d.logger.Warn("🔄 发现重复验证者地址，累计权重",
				"address", newDelegate.Address.String(),
				"originalVotingPower", newDelegate.VotingPower.String(),
				"oldPower", oldPower.String(),
				"newPower", existingDelegate.VotingPower.String(),
				"existingIndex", i)
			return
		}
	}

	// 如果没有重复，直接添加
	d.delegates = append(d.delegates, newDelegate)
	d.logger.Debug("✅ 添加新验证者",
		"address", newDelegate.Address.String(),
		"votingPower", newDelegate.VotingPower.String(),
		"isActive", newDelegate.IsActive,
		"totalDelegates", len(d.delegates))
}

// getGenesisValidators 获取创世验证者集合
func (d *DPoS) getGenesisValidators() validator.AccountSet {
	d.logger.Debug("🔍 获取创世验证者集合", "genesisValidatorsCount", len(d.genesisValidators))

	// 从内存中的创世验证者映射创建AccountSet
	var validators validator.AccountSet
	for address := range d.genesisValidators {
		votingPower := DefaultVotingPower() // 1000 VCITY

		validatorMetadata := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: votingPower,
			IsActive:    true,
		}
		validators = append(validators, validatorMetadata)
	}

	d.logger.Debug("✅ 创世验证者集合创建完成", "count", len(validators))
	return validators
}

// getDelegatesFromState 从状态获取历史验证者集合
func (d *DPoS) getDelegatesFromState(blockNumber uint64) (validator.AccountSet, error) {
	d.logger.Debug("🔍 ===== 开始获取历史验证者集合 =====",
		"blockNumber", blockNumber,
		"stateIsNil", d.state == nil,
		"stakeStoreIsNil", d.state != nil && d.state.StakeStore == nil)

	// 🆕 新增：检查是否在共识切换高度之前，如果是则跳过历史验证者集合获取
	if d.config.ConsensusSwitchHeight > 0 && blockNumber < d.config.ConsensusSwitchHeight {
		d.logger.Debug("🔄 区块在共识切换高度之前，跳过历史验证者集合获取",
			"blockNumber", blockNumber,
			"consensusSwitchHeight", d.config.ConsensusSwitchHeight,
			"reason", "切换高度之前不需要BLS验证，因此不需要历史验证者集合")

		// 返回创世验证者集合作为默认值
		return d.getGenesisValidators(), nil
	}

	// 🆕 关键修复：首先尝试从历史数据获取验证者集合
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Debug("🔍 开始数据库事务", "blockNumber", blockNumber)

		// 开始数据库事务
		dbTx, err := d.state.beginDBTransaction(false) // 只读事务，因为只是读取数据
		if err != nil {
			d.logger.Error("❌ 无法开始数据库事务",
				"blockNumber", blockNumber,
				"error", err,
				"errorType", fmt.Sprintf("%T", err))
		} else {
			d.logger.Debug("✅ 数据库事务开始成功", "blockNumber", blockNumber)

			d.logger.Info("🔍 ========== 开始调用getDelegatesAtBlock ==========",
				"blockNumber", blockNumber,
				"stakeStoreType", fmt.Sprintf("%T", d.state.StakeStore),
				"timestamp", time.Now().Format("2006-01-02 15:04:05.000"))

			startTime := time.Now()
			d.logger.Info("🚀 开始执行getDelegatesAtBlock",
				"blockNumber", blockNumber,
				"startTime", startTime.Format("2006-01-02 15:04:05.000"))

			if historicalDelegates, err := d.state.StakeStore.getDelegatesAtBlock(blockNumber, dbTx); err == nil {
				endTime := time.Now()
				duration := endTime.Sub(startTime)
				d.logger.Info("✅ getDelegatesAtBlock执行成功",
					"blockNumber", blockNumber,
					"duration", duration.String(),
					"endTime", endTime.Format("2006-01-02 15:04:05.000"),
					"delegatesCount", len(historicalDelegates))

				if err := dbTx.Rollback(); err != nil {
					d.logger.Warn("⚠️ 数据库事务回滚失败", "error", err)
				}

				if len(historicalDelegates) > 0 {
					d.logger.Info("✅ 从数据库获取历史验证者集合成功",
						"blockNumber", blockNumber,
						"count", len(historicalDelegates))
					return historicalDelegates, nil
				}
			} else {
				d.logger.Error("❌ getDelegatesAtBlock执行失败",
					"blockNumber", blockNumber,
					"error", err,
					"errorType", fmt.Sprintf("%T", err))

				if err := dbTx.Rollback(); err != nil {
					d.logger.Warn("⚠️ 数据库事务回滚失败", "error", err)
				}
			}
		}
	}

	// 如果数据库没有，返回创世验证者集合
	d.logger.Debug("🔄 数据库中没有历史验证者集合，返回创世验证者",
		"blockNumber", blockNumber)
	return d.getGenesisValidators(), nil
}

// getDelegatesFromStateWithTx 从状态获取历史验证者集合（带事务）
func (d *DPoS) getDelegatesFromStateWithTx(blockNumber uint64, dbTx *bbolt.Tx) (validator.AccountSet, error) {
	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接调用内存版本，避免复杂的数据库操作
	return d.getDelegatesFromState(blockNumber)
}

// updateValidatorStatus 更新验证者状态
func (d *DPoS) updateValidatorStatus(address types.Address, newStake *big.Int) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	for _, delegate := range d.delegates {
		if delegate.Address == address {
			oldActive := delegate.IsActive
			oldStake := delegate.VotingPower
			delegate.VotingPower = new(big.Int).Set(newStake)

			// 关键：根据新stake更新活跃状态
			// 🆕 使用统一的零值检查函数
			if isPositive(newStake) {
				delegate.IsActive = true // 有stake了，变为活跃
			} else {
				delegate.IsActive = false // stake为0，变为不活跃
			}

			// 记录状态变化
			if oldActive != delegate.IsActive {
				d.logger.Info("validator status changed",
					"address", address,
					"oldStake", oldStake.String(),
					"newStake", newStake.String(),
					"oldActive", oldActive,
					"newActive", delegate.IsActive)
			}

			return nil
		}
	}

	return fmt.Errorf("validator not found: %s", address)
}

// canParticipateInConsensus 检查验证者是否可以参与共识
func (d *DPoS) canParticipateInConsensus(delegate *validator.ValidatorMetadata) bool {
	// 🆕 使用统一的零值检查函数
	return delegate.IsActive && isPositive(delegate.VotingPower)
}

// ==================== 委托者注册相关函数 ====================

// initializeGenesisValidatorsMap 初始化创世验证者映射
// 从创世块或配置中获取创世验证者列表，并填充到映射中
func (d *DPoS) initializeGenesisValidatorsMap() {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 如果已经初始化过，直接返回
	if d.genesisValidators != nil && len(d.genesisValidators) > 0 {
		d.logger.Debug("🔍 创世验证者映射已存在，跳过初始化", "count", len(d.genesisValidators))
		return
	}

	// 初始化映射
	if d.genesisValidators == nil {
		d.genesisValidators = make(map[types.Address]bool)
	}

	// 方法1: 尝试从创世块解析
	if d.config != nil && d.config.Blockchain != nil {
		genesisHeader, exists := d.config.Blockchain.GetHeaderByNumber(0)
		if exists && len(genesisHeader.ExtraData) >= 32 {
			ibftValidators, err := d.parseValidatorsFromExtraData(genesisHeader.ExtraData)
			if err == nil && len(ibftValidators) > 0 {
				for _, validator := range ibftValidators {
					d.genesisValidators[validator.Address] = true
				}
				d.logger.Info("✅ 从创世块初始化创世验证者映射", "count", len(d.genesisValidators))
				return
			}
		}
	}

	// 方法2: 从配置中的初始验证者
	if d.config != nil && len(d.config.InitialDelegates) > 0 {
		for _, genesisValidator := range d.config.InitialDelegates {
			d.genesisValidators[genesisValidator.Address] = true
		}
		d.logger.Info("✅ 从配置初始化创世验证者映射", "count", len(d.genesisValidators))
		return
	}

	d.logger.Warn("⚠️ 无法初始化创世验证者映射：创世块和配置都不可用")
}

// isGenesisValidator 检查地址是否为创世验证者（内部方法）
// 注意：调用此方法前必须已经持有写锁（Lock）或读锁（RLock）
func (d *DPoS) isGenesisValidator(address types.Address) bool {
	// 如果映射为空，需要初始化（但此时可能持有读锁，需要特殊处理）
	if d.genesisValidators == nil || len(d.genesisValidators) == 0 {
		// 如果映射为空，返回 false，让调用者知道需要初始化
		// 初始化应该在外部完成（在持有写锁的情况下）
		return false
	}
	// 检查地址是否在创世验证者映射中
	return d.genesisValidators[address]
}

// IsGenesisValidator 检查地址是否为创世验证者（公共方法，供RPC调用）
func (d *DPoS) IsGenesisValidator(address types.Address) bool {
	// 先尝试读锁检查
	d.lock.RLock()
	isGenesis := false
	needsInit := false
	if d.genesisValidators == nil || len(d.genesisValidators) == 0 {
		needsInit = true
	} else {
		isGenesis = d.genesisValidators[address]
	}
	d.lock.RUnlock()

	// 如果需要初始化，获取写锁并初始化
	if needsInit {
		d.initializeGenesisValidatorsMap()
		// 重新检查
		d.lock.RLock()
		if d.genesisValidators != nil {
			isGenesis = d.genesisValidators[address]
		}
		d.lock.RUnlock()
	}

	// 🆕 添加调试日志
	d.logger.Info("🔍 IsGenesisValidator检查",
		"address", address.String(),
		"isGenesis", isGenesis,
		"genesisValidatorsCount", func() int {
			d.lock.RLock()
			defer d.lock.RUnlock()
			if d.genesisValidators == nil {
				return 0
			}
			return len(d.genesisValidators)
		}(),
		"genesisValidators", func() []string {
			d.lock.RLock()
			defer d.lock.RUnlock()
			if d.genesisValidators == nil {
				return []string{}
			}
			addresses := make([]string, 0, len(d.genesisValidators))
			for addr := range d.genesisValidators {
				addresses = append(addresses, addr.String())
			}
			return addresses
		}())

	return isGenesis
}

// isDelegateRegistrationTransaction 检查交易是否是受托人注册交易
func (d *DPoS) isDelegateRegistrationTransaction(tx *types.Transaction) bool {
	// 检查交易是否有输入数据（受托人注册交易应该有输入数据）
	if len(tx.Input) == 0 {
		return false
	}

	// 检查是否是DPoS受托人注册交易
	if len(tx.Input) < 8 || string(tx.Input[:4]) != "DPOS" || string(tx.Input[4:7]) != "REG" {
		return false
	}

	return true
}

// isVoteTransaction 检查交易是否是投票交易
func (d *DPoS) isVoteTransaction(tx *types.Transaction) bool {
	// 检查交易是否有输入数据（投票交易应该有输入数据）
	if len(tx.Input) == 0 {
		return false
	}

	// 检查交易输入数据长度（DPoS投票交易格式：4字节"DPOS" + 20字节投票者 + 20字节受托人 + 32字节金额）
	const (
		dposPrefixLen  = 4
		addrLen        = 20
		amountLen      = 32
		expectedLength = dposPrefixLen + addrLen + addrLen + amountLen
	)

	if len(tx.Input) < expectedLength {
		return false
	}

	// 检查是否是DPoS投票交易（前4字节应该是"DPOS"）
	if !bytes.Equal(tx.Input[:4], []byte("DPOS")) {
		return false
	}

	d.logger.Debug("🔍 发现DPoS投票交易",
		"inputLength", len(tx.Input),
		"prefix", string(tx.Input[:4]),
		"expectedLength", expectedLength)

	return true
}

// isCommissionUpdateTransaction 检查交易是否是佣金率修改交易
func (d *DPoS) isCommissionUpdateTransaction(tx *types.Transaction) bool {
	if len(tx.Input) < 9 {
		return false
	}

	if string(tx.Input[:4]) != "DPOS" {
		return false
	}

	if string(tx.Input[4:7]) != "COM" {
		return false
	}

	return true
}

// processDelegateRegistrationTransaction, processCommissionUpdateTransaction, parseDelegateRegistrationTransactionData
// 已移动到 delegate_transaction.go

// calculateTotalVotedAmount, GetDelegateRegistration, IsDelegateRegistered, IsDelegateCandidate
// 已移动到 delegate_query.go

// createDelegateRegistrationTransactionData 已移动到 delegate_transaction.go

// RegisterDelegate, RegisterDelegateWithKey, RegisterDelegateWithKeyAndChainID,
// createDelegateRegistrationTransaction, createDelegateRegistrationTransactionWithChainID,
// GetDelegateRegistrations, getGenesisValidatorsAsRegistrations
// 已移动到 delegate_registration.go

// getDelegateDepositAmount 获取受托人保证金金额
// 注意：保证金金额统一使用 dpos_delegate_threshold（MinVotingPower），Tron 只有一个门槛值
func (d *DPoS) getDelegateDepositAmount() *big.Int {
	// 🆕 优先从参数系统读取 dpos_delegate_threshold（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue("dpos_delegate_threshold"); err == nil {
		switch v := paramValue.(type) {
		case string:
			if bigAmount, ok := new(big.Int).SetString(v, 10); ok && isPositive(bigAmount) {
				d.logger.Debug("从参数系统读取 delegate threshold", "value", v)
				return bigAmount
			}
		case *big.Int:
			if isPositive(v) {
				d.logger.Debug("从参数系统读取 delegate threshold", "value", v.String())
				return new(big.Int).Set(v)
			}
		}
	}

	// 如果参数系统没有值，使用配置值
	if d.config != nil && d.config.MinVotingPower != nil && isPositive(d.config.MinVotingPower) {
		return new(big.Int).Set(d.config.MinVotingPower)
	}

	// 默认保证金：1000 VCITY（与 Tron 的 1000 TRX 一致）
	depositAmount := new(big.Int)
	depositAmount.Set(DefaultVotingPower()) // 1000 VCITY
	return depositAmount
}

// getMaxActiveDelegates 获取最大活跃受托人数量
func (d *DPoS) getMaxActiveDelegates() int {
	// 默认21个活跃受托人
	maxActive := 21

	// 可以从配置参数中读取
	if d.parameterCurrentValues != nil {
		d.parameterValuesMutex.RLock()
		if value, exists := d.parameterCurrentValues["max_active_delegates"]; exists {
			if count, ok := value.(uint64); ok {
				maxActive = int(count)
			}
		}
		d.parameterValuesMutex.RUnlock()
	}

	return maxActive
}

// WithdrawDelegate 退出受托人（直接解冻，参考 Tron）
func (d *DPoS) WithdrawDelegate(address types.Address) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 获取注册信息
	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		return fmt.Errorf("failed to get registration: %w", err)
	}

	if reg == nil {
		return fmt.Errorf("delegate not found")
	}

	// 1. 检查是否还有投票（参考 Tron：要求先手动撤回投票）
	if isPositive(reg.TotalVotes) {
		return fmt.Errorf("cannot withdraw while having votes. Please use dpos_vote to withdraw votes first")
	}

	// 2. 检查是否满足最小冻结期要求
	currentTime := uint64(time.Now().Unix())
	// 🆕 优先从参数系统读取 min_freeze_period（经过治理流程修改的值是权威数据源）
	minFreezePeriod := d.getParameterUint64("min_freeze_period", d.config.MinFreezePeriod, 604800) // 默认7天

	if reg.FrozenAt > 0 {
		elapsedTime := currentTime - reg.FrozenAt
		if elapsedTime < minFreezePeriod {
			remainingTime := minFreezePeriod - elapsedTime
			return fmt.Errorf("cannot withdraw before minimum freeze period. Registered at: %d, minimum period: %d seconds, remaining: %d seconds",
				reg.FrozenAt, minFreezePeriod, remainingTime)
		}
	}

	// 3. 执行解冻（直接解冻，进入锁定期）
	unfreezeAt := currentTime
	// 🆕 优先从参数系统读取 unfreeze_lock_period（经过治理流程修改的值是权威数据源）
	unfreezeLockPeriod := d.getParameterUint64("unfreeze_lock_period", d.config.UnfreezeLockPeriod, 1209600) // 默认14天
	unfreezeAvailableAt := unfreezeAt + unfreezeLockPeriod

	// 更新注册信息
	reg.Status = RegStatusWithdrawn
	reg.IsActive = false
	reg.UnfreezeAt = unfreezeAt
	reg.UnfreezeAvailableAt = unfreezeAvailableAt

	// 保存更新
	if err := d.state.RegistrationStore.SaveRegistration(reg); err != nil {
		return fmt.Errorf("failed to save registration: %w", err)
	}

	// 更新冻结信息
	if d.state != nil && d.state.FreezeStore != nil {
		freezeInfo, err := d.state.FreezeStore.GetFreezeInfo(address)
		if err == nil && freezeInfo != nil {
			freezeInfo.UnfreezeAt = unfreezeAt
			freezeInfo.UnfreezeAvailableAt = unfreezeAvailableAt
			freezeInfo.Status = "unfreezing" // 解冻中（锁定期内）
			if err := d.state.FreezeStore.SaveFreezeInfo(freezeInfo); err != nil {
				d.logger.Warn("⚠️ 更新冻结信息失败", "error", err)
			}
		}
	}

	// 从受托人列表中移除
	for i, delegate := range d.delegates {
		if delegate.Address == address {
			d.delegates = append(d.delegates[:i], d.delegates[i+1:]...)
			break
		}
	}

	d.logger.Info("Delegate withdrawn and unfrozen successfully",
		"address", address.String(),
		"name", reg.Name,
		"deposit", reg.Deposit.String(),
		"unfreezeAt", unfreezeAt,
		"unfreezeAvailableAt", unfreezeAvailableAt)

	return nil
}

// compareDelegateSets 比较受托人集合变化
func (d *DPoS) compareDelegateSets(oldSet, newSet validator.AccountSet) (added, removed []types.Address) {
	oldMap := make(map[types.Address]bool)
	newMap := make(map[types.Address]bool)

	for _, del := range oldSet {
		oldMap[del.Address] = true
	}
	for _, del := range newSet {
		newMap[del.Address] = true
	}

	for _, del := range newSet {
		if !oldMap[del.Address] {
			added = append(added, del.Address)
		}
	}

	for _, del := range oldSet {
		if !newMap[del.Address] {
			removed = append(removed, del.Address)
		}
	}

	return added, removed
}

// updateDelegates 增强的受托人更新（公共接口，需要获取锁）
func (d *DPoS) updateDelegates(block *types.FullBlock) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	return d.updateDelegatesInternal(block)
}

// updateDelegatesInternal 内部方法，不需要获取锁（由调用者负责锁管理）
func (d *DPoS) updateDelegatesInternal(block *types.FullBlock) error {
	d.logger.Info("🔄 updateDelegatesInternal called", "block", block, "currentDelegatesCount", len(d.delegates))

	// 🆕 方案1+方案2：检查是否需要重新排序验证者集合
	if d.pendingValidatorUpdate {
		d.logger.Info("🔄 轮次边界：重新排序验证者集合")

		// 按标准化规则排序，确保所有节点完全一致
		sortValidatorsByVotingPower(d.delegates)

		d.logger.Info("✅ 轮次边界：验证者集合重新排序完成")
		for i, delegate := range d.delegates {
			d.logger.Info("🔍 重新排序后的验证者",
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}

		// 🆕 已删除 currentDelegateIndex 的重新计算
		// 现在完全通过 getCurrentDelegate() 基于时间slot实时计算
		// 只同步验证者集合
		if d.runtime != nil {
			if d.runtime.lock.TryLock() {
				// 🆕 关键修复：同步重新排序后的验证者集合到runtime
				d.logger.Info("🔄 同步重新排序后的验证者集合到runtime")
				d.runtime.delegates = make(validator.AccountSet, len(d.delegates))
				copy(d.runtime.delegates, d.delegates)
				d.logger.Info("✅ 验证者集合同步完成",
					"runtimeDelegatesCount", len(d.runtime.delegates),
					"sourceDelegatesCount", len(d.delegates))
				d.runtime.lock.Unlock()
			} else {
				d.logger.Warn("⚠️ 无法获取 runtime.lock，跳过同步验证者集合")
			}
		}
	}

	// 🆕 落盘时：直接保存当前受托人集合，不进行排名和截取
	d.logger.Debug("💾 落盘时：直接保存当前受托人集合，不进行排名和截取")

	// 🆕 详细记录要落盘的见证人信息
	d.logger.Info("📋 准备落盘的见证人详情:")
	for i, delegate := range d.delegates {
		d.logger.Info("👤 见证人详情",
			"序号", i+1,
			"地址", delegate.Address.String(),
			"投票权重", delegate.VotingPower.String(),
			"是否活跃", delegate.IsActive,
			"isActiveType", fmt.Sprintf("%T", delegate.IsActive),
			"BLS密钥", delegate.BlsKey != nil,
			"BLS获取方式", func() string {
				if delegate.BlsKey != nil {
					return "已存在"
				}
				return "验证时动态获取"
			}())
	}

	// 直接保存当前的 d.delegates 到数据库，不改变受托人集合
	// 如果是在投票处理过程中，只更新受投票影响的验证者
	d.lock.RLock()
	hasAffectedDelegates := d.pendingValidatorUpdate && d.affectedDelegates != nil && len(d.affectedDelegates) > 0
	affectedDelegatesCopy := make(map[types.Address]bool)
	if hasAffectedDelegates {
		for addr := range d.affectedDelegates {
			affectedDelegatesCopy[addr] = true
		}
	}
	d.lock.RUnlock()

	if hasAffectedDelegates {
		d.logger.Info("🎯 投票处理中，只更新受投票影响的验证者",
			"affectedDelegatesCount", len(affectedDelegatesCopy))

		// 遍历所有受影响的验证者，逐个持久化
		for addr := range affectedDelegatesCopy {
			if err := d.persistDelegateSetToDatabaseWithTarget(d.delegates, addr); err != nil {
				d.logger.Error("❌ Failed to persist affected delegate to database",
					"delegate", addr.String(), "error", err)
			return err
			}
		}
	} else {
		// 正常情况，保存所有验证者
		if err := d.persistDelegateSetToDatabase(d.delegates); err != nil {
			d.logger.Error("❌ Failed to persist delegate set to database", "error", err)
			return err
		}
	}

	d.logger.Debug("✅ 受托人集合落盘完成", "count", len(d.delegates))
	return nil
}
