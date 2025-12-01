package dpos

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// getConfigUint64, getMissedBlocksPercentage, getMinorOffenseSlashRate, getSevereOffenseSlashRate, logOnceWithInterval
// 已移动到 fault_utils.go

// IsValidatorFaulty 检查验证者是否有故障（公共接口）
func (d *DPoS) IsValidatorFaulty(addr types.Address) (bool, error) {
	// 目前复用内部的故障信息读取逻辑。
	// 要求该方法仅依据各节点可一致获取的数据源（落盘或确定性内存镜像）。
	info := d.getValidatorFaultInfo(addr)
	if info == nil {
		return false, nil
	}
	isFaulty, _ := info["isFaulty"].(bool)
	return isFaulty, nil
}

// GetValidatorFaultInfo 获取验证者故障信息（用于RPC调用）
func (d *DPoS) GetValidatorFaultInfo(validatorAddr types.Address) map[string]interface{} {
	return d.getValidatorFaultInfo(validatorAddr)
}

// getValidatorFaultInfo 获取验证者故障信息（内部方法）
func (d *DPoS) getValidatorFaultInfo(validatorAddr types.Address) map[string]interface{} {
	faultInfo := map[string]interface{}{
		"isFaulty":        false,
		"missedBlocks":    uint64(0),
		"lastUpdateTime":  uint64(0),
		"lastFaultyEpoch": uint64(0),
		"reason":          "",
	}

	// 从数据库读取故障状态
	if store, err := d.getStateStore(); err == nil {
		dbFaultInfo, err := store.GetValidatorFaultStatus(validatorAddr)
		if err != nil {
			d.logger.Warn("⚠️ 读取验证者故障状态失败",
				"address", validatorAddr.String(),
				"error", err)
		} else if dbFaultInfo != nil {
			if isFaulty, ok := dbFaultInfo["isFaulty"].(bool); ok {
				faultInfo["isFaulty"] = isFaulty
			}

			if missedBlocks, ok := dbFaultInfo["missedBlocks"].(float64); ok {
				faultInfo["missedBlocks"] = uint64(missedBlocks)
			} else if missedBlocks, ok := dbFaultInfo["missedBlocks"].(uint64); ok {
				faultInfo["missedBlocks"] = missedBlocks
			}

			if lastUpdateTime, ok := dbFaultInfo["lastUpdateTime"].(float64); ok {
				faultInfo["lastUpdateTime"] = uint64(lastUpdateTime)
			} else if lastUpdateTime, ok := dbFaultInfo["lastUpdateTime"].(uint64); ok {
				faultInfo["lastUpdateTime"] = lastUpdateTime
			}

			if lfe, ok := dbFaultInfo["lastFaultyEpoch"].(float64); ok {
				faultInfo["lastFaultyEpoch"] = uint64(lfe)
			} else if lfe, ok := dbFaultInfo["lastFaultyEpoch"].(uint64); ok {
				faultInfo["lastFaultyEpoch"] = lfe
			}

			if reason, ok := dbFaultInfo["reason"].(string); ok {
				faultInfo["reason"] = reason
			}
		}
	}

	return faultInfo
}

// detectValidatorFaults 检测验证者故障（重构后）
func (d *DPoS) detectValidatorFaults(blockNumber uint64) ([]FaultFlagInfo, error) {
	// 获取或创建FaultDetector
	detector := d.getFaultDetector()
	if detector == nil {
		return nil, fmt.Errorf("fault detector not available")
	}

	// 使用FaultDetector检测故障
	return detector.DetectFaults(blockNumber)
}

// getFaultDetector 获取或创建FaultDetector实例
func (d *DPoS) getFaultDetector() *FaultDetector {
	return NewFaultDetector(d, d.logger)
}

// saveFaultStatusToDatabase 保存故障状态到数据库的辅助方法
func (d *DPoS) saveFaultStatusToDatabase(faultFlag FaultFlagInfo) error {
	store, err := d.getStateStore()
	if err != nil {
		return err
	}

	d.logger.Info("💾 保存验证者故障状态",
		"address", faultFlag.NodeAddress.String(),
		"isFaulty", faultFlag.IsFaulty,
		"missedBlocks", faultFlag.MissedBlocks,
		"lastUpdateTime", faultFlag.LastUpdateTime,
		"epoch", faultFlag.EpochNumber,
		"reason", faultFlag.Reason)

	return store.UpdateValidatorFaultStatus(
		faultFlag.NodeAddress,
		faultFlag.IsFaulty,
		faultFlag.MissedBlocks,
		faultFlag.LastUpdateTime,
		faultFlag.LastFaultyEpoch,
		faultFlag.Reason,
	)
}

// updateMemoryFaultStatus 更新内存中的故障状态
func (d *DPoS) updateMemoryFaultStatus(faultFlag FaultFlagInfo) {
	// 更新故障验证者映射
	if faultFlag.IsFaulty {
		d.faultyValidators[faultFlag.NodeAddress] = true
		d.logger.Info("🚨 更新内存故障状态",
			"address", faultFlag.NodeAddress.String(),
			"isFaulty", faultFlag.IsFaulty,
			"missedBlocks", faultFlag.MissedBlocks,
			"reason", faultFlag.Reason)
	} else {
		delete(d.faultyValidators, faultFlag.NodeAddress)
	}
}

// updateBlockProducersFromFaultFlags 根据故障标志重新计算出块者列表
func (d *DPoS) updateBlockProducersFromFaultFlags(faultFlags []FaultFlagInfo) error {
	d.logger.Info("🔄 开始根据故障标志重新计算出块者列表", "faultFlagsCount", len(faultFlags))

	// 1. 获取所有验证者
	var allValidators validator.AccountSet
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		allValidators = d.runtime.delegates.Copy()
		d.logger.Info("✅ 使用runtime.delegates", "count", len(allValidators))
	} else if len(d.delegates) > 0 {
		allValidators = d.delegates.Copy()
		d.logger.Info("✅ 使用d.delegates", "count", len(allValidators))
	} else {
		if store, err := d.getStateStore(); err == nil {
			if dbValidators, err := store.GetEpochValidators(); err == nil && len(dbValidators) > 0 {
				allValidators = dbValidators
				d.logger.Info("✅ 使用数据库中的验证者", "count", len(allValidators))
			} else {
				return fmt.Errorf("no validators set in memory or database")
			}
		} else {
			return fmt.Errorf("no validators set in memory and state store not available")
		}
	}

	// 2. 创建故障映射
	faultMap := make(map[types.Address]bool)
	for _, faultFlag := range faultFlags {
		faultMap[faultFlag.NodeAddress] = faultFlag.IsFaulty
	}

	// 3. 过滤掉故障验证者，按权重倒序排序
	activeValidators := make(validator.AccountSet, 0, len(allValidators))
	faultyCount := 0

	for _, validator := range allValidators {
		if isFaulty, exists := faultMap[validator.Address]; exists && isFaulty {
			faultyCount++
			d.logger.Info("🚫 过滤掉故障验证者",
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String())
		} else {
			activeValidators = append(activeValidators, validator)
		}
	}

	d.logger.Info("✅ 故障验证者过滤完成",
		"totalValidators", len(allValidators),
		"faultyValidators", faultyCount,
		"activeValidators", len(activeValidators))

	// 4. 按权重倒序排序
	sortValidatorsByVotingPower(activeValidators)

	d.logger.Info("📊 排序后的验证者列表:")
	for i, validator := range activeValidators {
		d.logger.Info("🏆 排序后验证者",
			"rank", i+1,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String())
	}

	// 5. 应用配置限制
	maxValidators := d.config.DPoSValidatorsCount

	d.logger.Info("🎯 验证者截取逻辑",
		"maxValidators", maxValidators,
		"activeValidators", len(activeValidators))

	var finalValidators validator.AccountSet
	if len(activeValidators) <= int(maxValidators) {
		finalValidators = activeValidators
		d.logger.Info("✅ 验证者数量 <= 配置数量，取全部验证者",
			"取用数量", len(activeValidators),
			"配置数量", maxValidators)
	} else {
		finalValidators = activeValidators[:maxValidators]
		d.logger.Info("✂️ 验证者数量 > 配置数量，截取前N个",
			"取用数量", len(finalValidators),
			"配置数量", maxValidators)
	}

	// 6. 更新内存中的出块者列表
	d.delegates = finalValidators

	// 6.1 同步到runtime，以便出块节点后续流程（如NextEpochValidators写入）使用最新集合
	if d.runtime != nil {
		d.runtime.lock.Lock()
		if err := d.syncDelegatesToRuntime(finalValidators); err != nil {
			d.logger.Warn("同步delegates到runtime失败", "error", err)
		}
		d.runtime.lock.Unlock()

		d.logger.Info("🔁 runtime.delegates已同步最新出块者列表",
			"delegatesCount", len(d.runtime.delegates))
	} else {
		d.logger.Debug("ℹ️ runtime为空，无法同步delegates")
	}

	d.logger.Info("🎉 出块者列表更新完成",
		"finalCount", len(finalValidators),
		"maxValidators", maxValidators)

	// 7. 记录最终出块者列表
	d.logger.Info("📋 最终出块者列表:")
	for i, validator := range finalValidators {
		d.logger.Info("🎖️ 最终出块者",
			"index", i+1,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String())
	}

	return nil
}

// reloadValidatorsAfterRecovery 恢复提案执行后重新加载验证者集合
// 从数据库读取最新的验证者集合，过滤掉故障验证者，并更新内存缓存
func (d *DPoS) reloadValidatorsAfterRecovery() error {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.logger.Info("🔄 恢复提案执行后：开始重新加载验证者集合")

	// 1. 从数据库读取所有验证者
	allValidators, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		return fmt.Errorf("failed to get validators from database: %w", err)
	}

	if len(allValidators) == 0 {
		d.logger.Warn("⚠️ 数据库中没有验证者，跳过重新加载")
		return nil
	}

	// 2. 过滤掉故障验证者
	activeValidators := make(validator.AccountSet, 0, len(allValidators))
	faultyCount := 0

	for _, validator := range allValidators {
		// 检查验证者的故障状态
		faultInfo := d.getValidatorFaultInfo(validator.Address)
		isFaulty := false
		if faultInfo != nil && faultInfo["isFaulty"] != nil {
			if faultValue, ok := faultInfo["isFaulty"].(bool); ok {
				isFaulty = faultValue
			}
		}

		if isFaulty {
			faultyCount++
			d.logger.Info("🚫 过滤掉故障验证者",
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String())
		} else {
			activeValidators = append(activeValidators, validator)
		}
	}

	d.logger.Info("✅ 故障验证者过滤完成",
		"totalValidators", len(allValidators),
		"faultyValidators", faultyCount,
		"activeValidators", len(activeValidators))

	// 3. 按权重倒序排序（GetSortedValidatorsWithLimit已经排序，但为了确保一致性，再次排序）
	sortValidatorsByVotingPower(activeValidators)

	// 4. 应用配置限制
	maxValidators := int(d.config.DPoSValidatorsCount)

	var finalValidators validator.AccountSet
	if len(activeValidators) <= maxValidators {
		finalValidators = activeValidators
	} else {
		finalValidators = activeValidators[:maxValidators]
	}

	// 5. 更新内存中的验证者集合
	d.delegates = finalValidators.Copy()
	d.logger.Info("✅ 已更新 d.delegates",
		"delegatesCount", len(d.delegates))

	// 6. 同步到runtime
	if d.runtime != nil {
		d.runtime.lock.Lock()
		if err := d.syncDelegatesToRuntime(finalValidators); err != nil {
			d.logger.Warn("同步delegates到runtime失败", "error", err)
		}
		d.runtime.lock.Unlock()
		d.logger.Info("✅ 已同步 runtime.delegates",
			"runtimeDelegatesCount", len(d.runtime.delegates))
	} else {
		d.logger.Debug("ℹ️ runtime为空，无法同步delegates")
	}

	d.logger.Info("🎉 验证者集合重新加载完成",
		"finalCount", len(finalValidators),
		"maxValidators", maxValidators)

	return nil
}

// getValidatorsForEpoch, getCachedEpochValidators, setCachedEpochValidators, saveEpochValidatorsToDatabase
// 已移动到 epoch_validator_manager.go

func (d *DPoS) calculateMissedBlocksWithActual(validatorAddr types.Address, startEpoch, endEpoch uint64) (uint64, uint64, uint64) {
	missedBlocks := uint64(0)
	var epochToCheck uint64
	var expectedBlocks uint64
	var actualBlocks uint64

	// 计算每个epoch中该验证者应该出块的次数
	blocksPerEpoch := d.getEpochSize()

	// 🔧 修复：当 startEpoch == endEpoch 时也应该计算（单个epoch的统计）
	if endEpoch >= startEpoch {
		epochToCheck = endEpoch
		epochNumberForCheck := epochToCheck + 1

		// 计算当前epoch，判断是否为历史epoch
		currentBlockNumber := d.getCurrentBlockNumber()
		consensusSwitchHeight := d.config.ConsensusSwitchHeight
		var currentEpoch uint64
		if currentBlockNumber < consensusSwitchHeight {
			currentEpoch = 0
		} else {
			dposBlockNumber := currentBlockNumber - consensusSwitchHeight
			currentEpoch = (dposBlockNumber / blocksPerEpoch) + 1
		}
		isHistoricalEpoch := epochNumberForCheck < currentEpoch

		// ✅ 修复：从该epoch开始区块的ExtraData或数据库获取该epoch的验证者集合
		validatorsCount := uint64(0)
		if epochValidators, err := d.getValidatorsForEpoch(epochNumberForCheck); err == nil && len(epochValidators) > 0 {
			validatorsCount = uint64(len(epochValidators))
		} else {
			// 对于历史epoch，如果获取失败，直接返回错误（不使用当前内存验证者，因为不准确）
			if isHistoricalEpoch {
				d.logger.Error("❌ 无法获取历史epoch的验证者集合",
					"epochNumber", epochNumberForCheck,
					"error", err)
				return 0, 0, 0 // 返回零值表示失败
			}
			// 对于当前epoch或未来epoch，可以使用当前内存中的验证者集合作为备用
			if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
				validatorsCount = uint64(len(d.runtime.delegates))
			} else if len(d.delegates) > 0 {
				validatorsCount = uint64(len(d.delegates))
			}
		}
		if validatorsCount == 0 {
			validatorsCount = 1 // 避免除零
		}

		// 计算该验证者在这个epoch中应该出块的次数
		expectedBlocks = blocksPerEpoch / validatorsCount
		if expectedBlocks == 0 {
			expectedBlocks = 1 // 至少应该出1个块
		}

		// 查询区块历史，计算实际出块数
		if d.blockchain != nil {
			actualBlocks = 0
			consensusSwitchHeight := d.config.ConsensusSwitchHeight

			// 🆕 修复：正确计算epoch对应的区块范围
			epochStartBlock := consensusSwitchHeight + epochToCheck*blocksPerEpoch
			epochEndBlock := consensusSwitchHeight + (epochToCheck+1)*blocksPerEpoch

			// 查询这个epoch中该验证者实际出块的次数
			for blockNum := epochStartBlock; blockNum < epochEndBlock; blockNum++ {
				header, exists := d.blockchain.GetHeaderByNumber(blockNum)
				if (!exists || header == nil) && blockNum == epochEndBlock-1 {
					if pendingHeader := d.getPendingEpochEndHeader(blockNum); pendingHeader != nil {
						header = pendingHeader
						exists = true
					}
				}

				if exists && header != nil {
					// 🆕 修复：通过Miner字段检查出块者
					if len(header.Miner) == 20 {
						minerAddr := types.Address(header.Miner)
						if minerAddr == validatorAddr {
							actualBlocks++
						}
					}
				}
			}

			// 计算漏块数
			if expectedBlocks > actualBlocks {
				missedBlocks = expectedBlocks - actualBlocks
			}
		}
	}

	return missedBlocks, actualBlocks, expectedBlocks
}

// getCurrentEpochByBlock 获取当前epoch（基于区块号）
func (d *DPoS) getCurrentEpochByBlock(blockNumber uint64) uint64 {
	consensusSwitchHeight := d.config.ConsensusSwitchHeight
	blocksPerEpoch := d.getEpochSize()

	if blockNumber < consensusSwitchHeight {
		return 0
	}

	dposBlockNumber := blockNumber - consensusSwitchHeight
	return dposBlockNumber / blocksPerEpoch
}

// calculateNextEpochValidators 计算下一个epoch的验证者集合
func (d *DPoS) calculateNextEpochValidators(blockNumber uint64) (validator.AccountSet, error) {
	d.logger.Info("计算下一个epoch的验证者集合", "blockNumber", blockNumber)

	// 1. 获取所有验证者（包括故障的）
	allValidators, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		return nil, fmt.Errorf("failed to get sorted validators: %v", err)
	}

	// 2. 过滤掉故障验证者
	activeValidators := make(validator.AccountSet, 0, len(allValidators))
	faultyCount := 0

	for _, validator := range allValidators {
		// 检查验证者的故障状态
		faultInfo := d.getValidatorFaultInfo(validator.Address)
		isFaulty := false
		if faultInfo != nil && faultInfo["isFaulty"] != nil {
			if faultValue, ok := faultInfo["isFaulty"].(bool); ok {
				isFaulty = faultValue
			}
		}

		if isFaulty {
			faultyCount++
			d.logger.Info("🚫 过滤掉故障验证者",
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String())
		} else {
			activeValidators = append(activeValidators, validator)
		}
	}

	d.logger.Info("✅ 下一个epoch验证者集合计算完成",
		"totalValidators", len(allValidators),
		"faultyValidators", faultyCount,
		"activeValidators", len(activeValidators))

	return activeValidators, nil
}

// saveNextEpochValidators 保存下一个epoch的验证者集合
func (d *DPoS) saveNextEpochValidators(validators validator.AccountSet) error {
	blockNumber := d.getCurrentBlockNumber()

	// 🆕 计算下一个epoch号
	blocksPerEpoch := d.getEpochSize()
	consensusSwitchHeight := d.config.ConsensusSwitchHeight
	var nextEpochNumber uint64
	if blockNumber < consensusSwitchHeight {
		nextEpochNumber = 1
	} else {
		dposBlockNumber := blockNumber - consensusSwitchHeight
		currentEpoch := (dposBlockNumber / blocksPerEpoch) + 1
		nextEpochNumber = currentEpoch + 1
	}

	// 使用 state 模块保存验证者集合
	if d.stateMgr == nil {
		d.logger.Error("❌ state模块未初始化", "blockNumber", blockNumber)
		return fmt.Errorf("state module not initialized")
	}

	if err := d.stateMgr.SaveValidators(blockNumber, validators); err != nil {
		d.logger.Error("❌ state模块保存验证者集合失败",
			"error", err,
			"count", len(validators),
			"blockNumber", blockNumber,
			"nextEpochNumber", nextEpochNumber)
		return fmt.Errorf("failed to save validators via state module: %w", err)
	}

	d.logger.Info("✅ 下一个epoch验证者集合保存成功（state模块）",
		"count", len(validators),
		"blockNumber", blockNumber,
		"nextEpochNumber", nextEpochNumber)
	return nil
}

// applyNextEpochValidatorsFromExtra 使用区块ExtraData中的验证者集合更新本地状态
func (d *DPoS) applyNextEpochValidatorsFromExtra(validators validator.AccountSet, blockNumber uint64) error {
	if d.epochLifecycle != nil {
		if err := d.epochLifecycle.ApplyNextValidatorsFromExtra(validators, blockNumber); err != nil {
			d.logger.Error("❌ 模块化应用下一个epoch验证者集合失败", "blockNumber", blockNumber, "error", err)
			return err
		}
		return nil
	}

	if len(validators) == 0 {
		return nil
	}

	d.logger.Info("🆕 从ExtraData应用下一个epoch验证者集合",
		"blockNumber", blockNumber,
		"nextEpochValidatorsCount", len(validators))

	// 保存到数据库
	if err := d.saveNextEpochValidators(validators); err != nil {
		return fmt.Errorf("failed to save next epoch validators from extra: %w", err)
	}

	// 更新内存缓存
	d.delegates = validators.Copy()
	d.logger.Info("🆕 已用ExtraData验证者集合覆盖本地delegates",
		"blockNumber", blockNumber,
		"delegatesCount", len(d.delegates))

	if d.runtime != nil {
		d.runtime.lock.Lock()
		d.runtime.delegates = validators.Copy()
		d.runtime.lock.Unlock()
		d.logger.Info("🆕 已同步runtime.delegates",
			"blockNumber", blockNumber,
			"delegatesCount", len(d.runtime.delegates))
	}

	return nil
}

// getEpochValidatorsFromDatabase 已移动到 epoch_validator_manager.go

// logOnceWithInterval 防重复日志函数（自定义间隔）

// executeSlashing, updateStakingInfoInDatabase, updateVoterVoteAmountForValidator, updateStakingInfoAfterSlashing
// 已移动到 slashing_executor.go
