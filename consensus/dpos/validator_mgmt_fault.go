package dpos

import (
	"fmt"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

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
	if d.state != nil && d.state.StakeStore != nil {
		if dbFaultInfo, err := d.state.StakeStore.GetValidatorFaultStatus(validatorAddr); err == nil && dbFaultInfo != nil {
			faultInfo["isFaulty"] = dbFaultInfo["isFaulty"]
			faultInfo["missedBlocks"] = dbFaultInfo["missedBlocks"]
			faultInfo["lastUpdateTime"] = dbFaultInfo["lastUpdateTime"]
			if lfe, ok := dbFaultInfo["lastFaultyEpoch"].(float64); ok {
				faultInfo["lastFaultyEpoch"] = uint64(lfe)
			}
			faultInfo["reason"] = dbFaultInfo["reason"]
		}
	}

	return faultInfo
}

// detectValidatorFaults 检测验证者故障
func (d *DPoS) detectValidatorFaults(blockNumber uint64) ([]FaultFlagInfo, error) {
	var faultFlags []FaultFlagInfo

	// 计算当前epoch
	currentEpoch := d.getCurrentEpochByBlock(blockNumber)

	// 如果epoch没有变化，不需要检测
	if currentEpoch == d.currentEpoch {
		d.logger.Info("ℹ️ Epoch未变化，跳过故障检测", "currentEpoch", currentEpoch, "previousEpoch", d.currentEpoch)
		return faultFlags, nil
	}

	d.logger.Info("🔍 ===== 开始检测验证者故障 =====",
		"blockNumber", blockNumber,
		"currentEpoch", currentEpoch,
		"previousEpoch", d.currentEpoch,
		"maxMissedBlocks", d.config.MaxMissedBlocks)

	// 计算每个验证者的漏块数
	d.logger.Info("🔍 开始计算每个验证者的漏块数...")

	// 🆕 直接使用内存中的验证者集合（当前epoch的出块者）
	d.logger.Info("🔧 使用内存中的验证者集合...")

	// 优先使用 runtime.delegates（最实时）
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		d.epochValidators = d.runtime.delegates.Copy()
		d.logger.Info("✅ 使用runtime.delegates", "count", len(d.epochValidators))
	} else if len(d.delegates) > 0 {
		// 备用方案：使用 d.delegates
		d.epochValidators = d.delegates.Copy()
		d.logger.Info("✅ 使用d.delegates", "count", len(d.epochValidators))
	} else {
		d.logger.Error("❌ 内存中验证者集合为空")
		return nil, fmt.Errorf("no validators in memory")
	}

	// 统计归属的epoch = 刚结束的那个epoch
	var epochToCheck uint64
	if currentEpoch > 0 {
		epochToCheck = currentEpoch - 1
	} else {
		epochToCheck = 0
	}

	currentBlock := d.getCurrentBlockNumber()
	if d.config != nil && currentBlock >= d.config.ConsensusSwitchHeight && currentBlock < d.config.ConsensusSwitchHeight+20 {
		d.logger.Info("⏭️ 故障统计保护期，跳过所有故障检测", "blockNumber", currentBlock, "protectionWindow", 20, "consensusSwitchHeight", d.config.ConsensusSwitchHeight)
		return nil, nil
	}

	for _, validator := range d.epochValidators {
		missedBlocks, actualBlocks := d.calculateMissedBlocksWithActual(validator.Address, d.currentEpoch, currentEpoch)
		d.missedBlocksCount[validator.Address] = missedBlocks

		isFaulty := missedBlocks >= d.config.MaxMissedBlocks

		// 🆕 获取上次故障的epoch（从数据库或FaultFlags中）
		lastFaultyEpoch := uint64(0)
		if d.state != nil && d.state.StakeStore != nil {
			if dbFaultInfo, err := d.state.StakeStore.GetValidatorFaultStatus(validator.Address); err == nil && dbFaultInfo != nil {
				// 尝试从数据库获取上次故障的epoch
				if lfe, ok := dbFaultInfo["lastFaultyEpoch"].(float64); ok {
					lastFaultyEpoch = uint64(lfe)
				}
			}
		}

		// 🆕 如果当前有故障，更新lastFaultyEpoch为当前epoch；否则保留上次的值
		if isFaulty {
			lastFaultyEpoch = epochToCheck
		}

		d.logger.Info("📊 验证者漏块统计",
			"address", validator.Address.String(),
			"missedBlocks", missedBlocks,
			"threshold", d.config.MaxMissedBlocks,
			"isFaulty", isFaulty,
			"lastFaultyEpoch", lastFaultyEpoch)

		// 🆕 不管漏块数为多少，都保存到数据库
		faultFlag := FaultFlagInfo{
			NodeAddress:     validator.Address,
			IsFaulty:        isFaulty,
			MissedBlocks:    missedBlocks,
			ActualBlocks:    actualBlocks, // 🆕 实际出块数
			LastUpdateTime:  uint64(time.Now().Unix()),
			EpochNumber:     epochToCheck,
			LastFaultyEpoch: lastFaultyEpoch,
			Reason: func() string {
				if isFaulty {
					return fmt.Sprintf("Epoch %d: missed blocks reached threshold: %d >= %d", epochToCheck, missedBlocks, d.config.MaxMissedBlocks)
				}
				return fmt.Sprintf("Epoch %d: missed blocks normal: %d less than %d", epochToCheck, missedBlocks, d.config.MaxMissedBlocks)
			}(),
		}
		faultFlags = append(faultFlags, faultFlag)

		if isFaulty {
			d.logger.Info("🚨 ===== 检测到故障验证者 =====",
				"address", validator.Address.String(),
				"missedBlocks", missedBlocks,
				"threshold", d.config.MaxMissedBlocks,
				"reason", faultFlag.Reason)
		} else {
			d.logger.Info("✅ 验证者状态正常",
				"address", validator.Address.String(),
				"missedBlocks", missedBlocks,
				"threshold", d.config.MaxMissedBlocks)
		}
	}

	// 更新当前epoch
	d.currentEpoch = currentEpoch

	d.logger.Info("🏁 ===== 故障检测完成 =====",
		"faultyValidatorsCount", len(faultFlags),
		"currentEpoch", currentEpoch)

	if len(faultFlags) > 0 {
		d.logger.Info("📋 验证者列表:")
		for i, faultFlag := range faultFlags {
			d.logger.Info("👤 验证者", "index", i+1, "address", faultFlag.NodeAddress.String(), "actualBlocks", faultFlag.ActualBlocks, "missedBlocks", faultFlag.MissedBlocks, "isFaulty", faultFlag.IsFaulty, "reason", faultFlag.Reason)
		}
	} else {
		d.logger.Info("✅ 没有检测到验证者")
	}

	return faultFlags, nil
}

// saveFaultStatusToDatabase 保存故障状态到数据库的辅助方法
func (d *DPoS) saveFaultStatusToDatabase(faultFlag FaultFlagInfo) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	return d.state.StakeStore.UpdateValidatorFaultStatus(
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
		d.logger.Info("✅ 清除内存故障状态",
			"address", faultFlag.NodeAddress.String(),
			"missedBlocks", faultFlag.MissedBlocks,
			"reason", faultFlag.Reason)
	}
}

// updateBlockProducersFromFaultFlags 根据故障标志重新计算出块者列表
func (d *DPoS) updateBlockProducersFromFaultFlags(faultFlags []FaultFlagInfo) error {
	d.logger.Info("🔄 开始根据故障标志重新计算出块者列表", "faultFlagsCount", len(faultFlags))

	// 1. 获取所有验证者
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	var allValidators validator.AccountSet
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		allValidators = d.runtime.delegates.Copy()
		d.logger.Info("✅ 使用runtime.delegates", "count", len(allValidators))
	} else if len(d.delegates) > 0 {
		allValidators = d.delegates.Copy()
		d.logger.Info("✅ 使用d.delegates", "count", len(allValidators))
	} else {
		return fmt.Errorf("no validators set in memory")
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
	sort.Slice(activeValidators, func(i, j int) bool {
		return activeValidators[i].VotingPower.Cmp(activeValidators[j].VotingPower) > 0
	})

	d.logger.Info("📊 排序后的验证者列表:")
	for i, validator := range activeValidators {
		d.logger.Info("🏆 排序后验证者",
			"rank", i+1,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String())
	}

	// 5. 应用配置限制
	maxValidators := d.config.DPoSValidatorsCount
	if maxValidators == 0 {
		maxValidators = d.config.DelegateCount
	}

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

// calculateMissedBlocks 计算验证者漏块数
func (d *DPoS) calculateMissedBlocks(validatorAddr types.Address, startEpoch, endEpoch uint64) uint64 {
	missedBlocks := uint64(0)
	var epochToCheck uint64
	var expectedBlocks uint64
	var actualBlocks uint64

	// 计算每个epoch中该验证者应该出块的次数
	blocksPerEpoch := d.config.DPoSValidatorsCount
	if blocksPerEpoch == 0 {
		blocksPerEpoch = d.config.DelegateCount
	}

	// 🆕 修复：只检测刚结束的epoch，不是跨多个epoch
	if endEpoch > startEpoch {
		// 只检测最后一个epoch（刚结束的epoch）
		epochToCheck = endEpoch - 1

		// 计算该验证者在这个epoch中应该出块的次数
		expectedBlocks = blocksPerEpoch / d.config.DPoSValidatorsCount
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
				if header, exists := d.blockchain.GetHeaderByNumber(blockNum); exists {
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

	d.logger.Info("📊 计算验证者漏块数",
		"validator", validatorAddr.String(),
		"epochToCheck", epochToCheck,
		"expectedBlocks", expectedBlocks,
		"actualBlocks", actualBlocks,
		"missedBlocks", missedBlocks)

	return missedBlocks
}

// calculateMissedBlocksWithActual 返回实际出块数的版本
func (d *DPoS) calculateMissedBlocksWithActual(validatorAddr types.Address, startEpoch, endEpoch uint64) (uint64, uint64) {
	missedBlocks := uint64(0)
	var epochToCheck uint64
	var expectedBlocks uint64
	var actualBlocks uint64

	// 计算每个epoch中该验证者应该出块的次数
	blocksPerEpoch := d.config.DPoSValidatorsCount
	if blocksPerEpoch == 0 {
		blocksPerEpoch = d.config.DelegateCount
	}

	// 🆕 修复：只检测刚结束的epoch，不是跨多个epoch
	if endEpoch > startEpoch {
		// 只检测最后一个epoch（刚结束的epoch）
		epochToCheck = endEpoch - 1

		// 计算该验证者在这个epoch中应该出块的次数
		expectedBlocks = blocksPerEpoch / d.config.DPoSValidatorsCount
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
				if header, exists := d.blockchain.GetHeaderByNumber(blockNum); exists {
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

	d.logger.Info("📊 计算验证者漏块数",
		"validator", validatorAddr.String(),
		"epochToCheck", epochToCheck,
		"expectedBlocks", expectedBlocks,
		"actualBlocks", actualBlocks,
		"missedBlocks", missedBlocks)

	return missedBlocks, actualBlocks
}

// getCurrentEpochByBlock 获取当前epoch（基于区块号）
func (d *DPoS) getCurrentEpochByBlock(blockNumber uint64) uint64 {
	consensusSwitchHeight := d.config.ConsensusSwitchHeight
	blocksPerEpoch := d.config.DPoSValidatorsCount
	if blocksPerEpoch == 0 {
		blocksPerEpoch = d.config.DelegateCount
	}

	if blockNumber < consensusSwitchHeight {
		return 0
	}

	dposBlockNumber := blockNumber - consensusSwitchHeight
	return dposBlockNumber / blocksPerEpoch
}

// calculateNextEpochValidators 计算下一个epoch的验证者集合
func (d *DPoS) calculateNextEpochValidators(blockNumber uint64) (validator.AccountSet, error) {
	d.logger.Info("🔄 计算下一个epoch的验证者集合", "blockNumber", blockNumber)

	// 🆕 使用公共函数获取排序和限制后的验证者（包含故障过滤）
	activeValidators, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		return nil, fmt.Errorf("failed to get sorted validators: %v", err)
	}

	d.logger.Info("✅ 下一个epoch验证者集合计算完成",
		"activeValidators", len(activeValidators))

	return activeValidators, nil
}

// saveNextEpochValidators 保存下一个epoch的验证者集合
func (d *DPoS) saveNextEpochValidators(validators validator.AccountSet) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("stake store not available")
	}

	err := d.state.StakeStore.SaveEpochValidators(validators)
	if err != nil {
		d.logger.Error("❌ 保存下一个epoch验证者集合失败", "error", err)
		return err
	}

	d.logger.Info("✅ 下一个epoch验证者集合保存成功", "count", len(validators))
	return nil
}

// getEpochValidatorsFromDatabase 从数据库获取epoch验证者
func (d *DPoS) getEpochValidatorsFromDatabase() (validator.AccountSet, error) {
	if d.state == nil || d.state.StakeStore == nil {
		return nil, fmt.Errorf("stake store not available")
	}

	validators, err := d.state.StakeStore.GetEpochValidators()
	if err != nil {
		// 🆕 使用日志频率限制，10秒一次
		d.logOnceWithInterval("get_epoch_validators_failed", 10*time.Second, "warn",
			"⚠️ 从数据库获取epoch验证者失败", "error", err)
		return nil, err
	}

	// 🆕 使用日志频率限制，10秒一次
	d.logOnceWithInterval("get_epoch_validators_success", 10*time.Second, "debug",
		"✅ 从数据库获取epoch验证者成功", "count", len(validators))
	return validators, nil
}

// logOnceWithInterval 防重复日志函数（自定义间隔）
func (d *DPoS) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	d.logMutex.Lock()
	defer d.logMutex.Unlock()

	now := time.Now()
	if lastTime, exists := d.lastLogTime[key]; exists {
		if now.Sub(lastTime) < interval {
			return
		}
	}
	d.lastLogTime[key] = now
	switch level {
	case "debug":
		d.logger.Debug(message, args...)
	case "info":
		d.logger.Info(message, args...)
	case "warn":
		d.logger.Warn(message, args...)
	case "error":
		d.logger.Error(message, args...)
	default:
		d.logger.Info(message, args...)
	}
}
