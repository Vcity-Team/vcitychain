package dpos

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// getConfigUint64 从 rawConfig 读取 uint64 配置值
func (d *DPoS) getConfigUint64(keys ...string) uint64 {
	if d.rawConfig == nil {
		return 0
	}
	for _, key := range keys {
		if val, ok := d.rawConfig[key]; ok {
			switch v := val.(type) {
			case uint64:
				return v
			case int:
				if v >= 0 {
					return uint64(v)
				}
			case int64:
				if v >= 0 {
					return uint64(v)
				}
			case float64:
				if v >= 0 {
					return uint64(v)
				}
			}
		}
	}
	return 0
}

// getMissedBlocksPercentage 获取漏块率阈值（基点）
func (d *DPoS) getMissedBlocksPercentage() uint64 {
	if val := d.getConfigUint64("dpos_missed_blocks_percentage", "missed_blocks_percentage"); val > 0 {
		return val
	}
	return 1000 // 默认值 10%
}

// getMinorOffenseSlashRate 获取轻度违规削减率（基点）
func (d *DPoS) getMinorOffenseSlashRate() uint64 {
	if val := d.getConfigUint64("dpos_minor_offense_slash_rate", "minor_offense_slash_rate"); val > 0 {
		return val
	}
	return 50 // 默认值 0.5%
}

// getSevereOffenseSlashRate 获取严重违规削减率（基点）
func (d *DPoS) getSevereOffenseSlashRate() uint64 {
	if val := d.getConfigUint64("dpos_severe_offense_slash_rate", "severe_offense_slash_rate"); val > 0 {
		return val
	}
	return 1000 // 默认值 10%
}

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

	missedBlocksPercentageThreshold := d.getMissedBlocksPercentage()
	d.logger.Info("🔍 ===== 开始检测验证者故障 =====",
		"blockNumber", blockNumber,
		"currentEpoch", currentEpoch,
		"previousEpoch", d.currentEpoch,
		"maxMissedBlocks", d.config.MaxMissedBlocks,
		"missedBlocksPercentage", missedBlocksPercentageThreshold)

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

	var epochToCheck uint64
	if currentEpoch > 0 {
		epochToCheck = currentEpoch
	} else {
		epochToCheck = 0
	}

	for _, validator := range d.epochValidators {
		d.logger.Info("🔥 [FaultDetection] 调用 calculateMissedBlocksWithActual",
			"validator", validator.Address.String(),
			"startEpoch", d.currentEpoch,
			"targetEpoch", currentEpoch,
			"blockNumber", blockNumber)

		missedBlocks, actualBlocks := d.calculateMissedBlocksWithActual(validator.Address, d.currentEpoch, currentEpoch)

		d.logger.Info("🔥 [FaultDetection] 计算完成",
			"validator", validator.Address.String(),
			"missedBlocks", missedBlocks,
			"actualBlocks", actualBlocks,
			"currentEpoch", currentEpoch)

		d.missedBlocksCount[validator.Address] = missedBlocks

		// 🆕 计算预期出块数和漏块率
		// 获取epoch大小和验证者数量
		blocksPerEpoch := d.getEpochSize()
		validatorsCount := uint64(len(d.epochValidators))
		if validatorsCount == 0 {
			validatorsCount = 1 // 避免除零
		}

		// 计算该验证者在这个epoch中应该出块的次数
		expectedBlocks := blocksPerEpoch / validatorsCount
		if expectedBlocks == 0 {
			expectedBlocks = 1 // 至少应该出1个块
		}

		// 计算漏块率（基点，10000 = 100%）
		missedBlocksPercentage := uint64(0)
		if expectedBlocks > 0 {
			missedBlocksPercentage = (missedBlocks * 10000) / expectedBlocks
		}

		missedBlocksPercentageThreshold := d.getMissedBlocksPercentage()
		d.logger.Info("📊 验证者漏块率计算",
			"address", validator.Address.String(),
			"epochToCheck", epochToCheck,
			"blocksPerEpoch", blocksPerEpoch,
			"validatorsCount", validatorsCount,
			"expectedBlocks", expectedBlocks,
			"actualBlocks", actualBlocks,
			"missedBlocks", missedBlocks,
			"missedBlocksPercentage", missedBlocksPercentage,
			"thresholdPercentage", missedBlocksPercentageThreshold)

		// 🆕 使用漏块率判断故障（而不是绝对漏块数）
		isFaulty := expectedBlocks > 0 && missedBlocksPercentage >= missedBlocksPercentageThreshold

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
			"expectedBlocks", expectedBlocks,
			"actualBlocks", actualBlocks,
			"missedBlocks", missedBlocks,
			"missedBlocksPercentage", missedBlocksPercentage,
			"thresholdPercentage", missedBlocksPercentageThreshold,
			"isFaulty", isFaulty,
			"lastFaultyEpoch", lastFaultyEpoch)

		// 🆕 构建故障原因
		var reason string
		if isFaulty {
			reason = fmt.Sprintf("Epoch %d: missed blocks percentage reached threshold: %d bp >= %d bp (missed %d/%d blocks)",
				epochToCheck, missedBlocksPercentage, missedBlocksPercentageThreshold, missedBlocks, expectedBlocks)
		} else {
			reason = fmt.Sprintf("Epoch %d: missed blocks percentage normal: %d bp < %d bp (missed %d/%d blocks)",
				epochToCheck, missedBlocksPercentage, missedBlocksPercentageThreshold, missedBlocks, expectedBlocks)
		}

		// 🆕 不管漏块数为多少，都保存到数据库
		faultFlag := FaultFlagInfo{
			NodeAddress:            validator.Address,
			IsFaulty:               isFaulty,
			MissedBlocks:           missedBlocks,
			ActualBlocks:           actualBlocks,
			ExpectedBlocks:         expectedBlocks,         // 🆕 预期出块数
			MissedBlocksPercentage: missedBlocksPercentage, // 🆕 漏块率（基点）
			LastUpdateTime:         uint64(time.Now().Unix()),
			EpochNumber:            epochToCheck,
			LastFaultyEpoch:        lastFaultyEpoch,
			Reason:                 reason,
		}
		faultFlags = append(faultFlags, faultFlag)

		if isFaulty {
			d.logger.Info("🚨 ===== 检测到故障验证者 =====",
				"address", validator.Address.String(),
				"expectedBlocks", expectedBlocks,
				"actualBlocks", actualBlocks,
				"missedBlocks", missedBlocks,
				"missedBlocksPercentage", missedBlocksPercentage,
				"thresholdPercentage", missedBlocksPercentageThreshold,
				"reason", faultFlag.Reason)

			// 🆕 执行削减惩罚
			slashRate := d.getMinorOffenseSlashRate()
			if err := d.executeSlashing(
				validator.Address,
				slashRate,
				blockNumber,
				epochToCheck,
				faultFlag.Reason,
				missedBlocks,
				missedBlocksPercentage,
				0, // doubleSigningHeight (轻度违规为0)
			); err != nil {
				d.logger.Error("❌ 执行削减失败",
					"validator", validator.Address.String(),
					"error", err)
			} else {
				d.logger.Info("✅ 削减执行成功",
					"validator", validator.Address.String(),
					"slashRate", slashRate,
					"基点")
			}
		} else {
			d.logger.Info("✅ 验证者状态正常",
				"address", validator.Address.String(),
				"expectedBlocks", expectedBlocks,
				"actualBlocks", actualBlocks,
				"missedBlocks", missedBlocks,
				"missedBlocksPercentage", missedBlocksPercentage,
				"thresholdPercentage", missedBlocksPercentageThreshold)
		}
	}

	// 更新当前epoch
	d.currentEpoch = currentEpoch

	d.logger.Info("🏁 ===== 故障检测完成 =====",
		"faultyValidatorsCount", len(faultFlags),
		"currentEpoch", currentEpoch)

	if len(faultFlags) > 0 {
		d.logger.Info("📋 验证者故障检测结果汇总:")
		faultyCount := 0
		for i, faultFlag := range faultFlags {
			if faultFlag.IsFaulty {
				faultyCount++
			}
			d.logger.Info("👤 验证者故障详情",
				"index", i+1,
				"address", faultFlag.NodeAddress.String(),
				"expectedBlocks", faultFlag.ExpectedBlocks,
				"actualBlocks", faultFlag.ActualBlocks,
				"missedBlocks", faultFlag.MissedBlocks,
				"missedBlocksPercentage", faultFlag.MissedBlocksPercentage,
				"isFaulty", faultFlag.IsFaulty,
				"reason", faultFlag.Reason)
		}
		d.logger.Info("📊 故障统计",
			"totalValidators", len(faultFlags),
			"faultyValidators", faultyCount,
			"normalValidators", len(faultFlags)-faultyCount)
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

	// 6.1 同步到runtime，以便出块节点后续流程（如NextEpochValidators写入）使用最新集合
	if d.runtime != nil {
		d.runtime.lock.Lock()
		d.runtime.delegates = finalValidators.Copy()
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

// getValidatorsForEpoch 获取指定epoch的验证者集合（从该epoch开始区块的ExtraData或数据库获取）
func (d *DPoS) getValidatorsForEpoch(epochNumber uint64) (validator.AccountSet, error) {
	if d.blockchain == nil {
		return nil, fmt.Errorf("blockchain not available")
	}

	blocksPerEpoch := d.getEpochSize()
	consensusSwitchHeight := d.config.ConsensusSwitchHeight

	// 计算该epoch的开始区块号
	// epoch 1 从 consensusSwitchHeight 开始
	// epoch 2 从 consensusSwitchHeight + blocksPerEpoch 开始
	// epoch N 从 consensusSwitchHeight + (N-1) * blocksPerEpoch 开始
	epochStartBlock := consensusSwitchHeight
	if epochNumber > 1 {
		epochStartBlock = consensusSwitchHeight + (epochNumber-1)*blocksPerEpoch
	}

	// 方式1：从该epoch开始区块的ExtraData获取验证者集合
	if header, exists := d.blockchain.GetHeaderByNumber(epochStartBlock); exists {
		extra := &Extra{}
		if err := extra.UnmarshalRLP(header.ExtraData); err == nil {
			// 从ExtraData获取验证者集合
			if extra.Validators != nil && !extra.Validators.IsEmpty() && len(extra.Validators.Added) > 0 {
				validators := make(validator.AccountSet, 0, len(extra.Validators.Added))
				for _, v := range extra.Validators.Added {
					validators = append(validators, &validator.ValidatorMetadata{
						Address:     v.Address,
						VotingPower: v.VotingPower,
						IsActive:    v.IsActive,
					})
				}
				d.logger.Info("✅ 从ExtraData获取epoch验证者集合",
					"epochNumber", epochNumber,
					"epochStartBlock", epochStartBlock,
					"validatorsCount", len(validators))
				return validators, nil
			}
		}
	}

	// 方式2：从数据库获取（如果ExtraData中没有）
	if d.state != nil && d.state.StakeStore != nil {
		if validators, err := d.state.StakeStore.GetEpochValidators(); err == nil && len(validators) > 0 {
			d.logger.Info("✅ 从数据库获取epoch验证者集合",
				"epochNumber", epochNumber,
				"validatorsCount", len(validators))
			return validators, nil
		}
	}

	// 方式3：备用方案 - 使用当前内存中的验证者集合
	d.logger.Warn("⚠️ 无法从ExtraData或数据库获取epoch验证者集合，使用当前内存中的验证者集合",
		"epochNumber", epochNumber,
		"epochStartBlock", epochStartBlock)
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		return d.runtime.delegates.Copy(), nil
	} else if len(d.delegates) > 0 {
		return d.delegates.Copy(), nil
	}

	return nil, fmt.Errorf("cannot get validators for epoch %d", epochNumber)
}

func (d *DPoS) calculateMissedBlocksWithActual(validatorAddr types.Address, startEpoch, endEpoch uint64) (uint64, uint64) {
	missedBlocks := uint64(0)
	var epochToCheck uint64
	var expectedBlocks uint64
	var actualBlocks uint64

	d.logger.Info("📊 [calculateMissedBlocksWithActual] 开始统计",
		"validator", validatorAddr.String(),
		"startEpoch", startEpoch,
		"endEpoch", endEpoch)

	// 计算每个epoch中该验证者应该出块的次数
	blocksPerEpoch := d.getEpochSize()
	d.logger.Info("📊 [calculateMissedBlocksWithActual] epoch基础信息",
		"validator", validatorAddr.String(),
		"blocksPerEpoch", blocksPerEpoch)

	if endEpoch > startEpoch {
		epochToCheck = endEpoch

		// ✅ 修复：从该epoch开始区块的ExtraData或数据库获取该epoch的验证者集合
		validatorsCount := uint64(0)
		if epochValidators, err := d.getValidatorsForEpoch(epochToCheck); err == nil && len(epochValidators) > 0 {
			validatorsCount = uint64(len(epochValidators))
			d.logger.Info("📊 [calculateMissedBlocksWithActual] 获取epoch验证者集合",
				"epochToCheck", epochToCheck,
				"validatorsCount", validatorsCount)
		} else {
			// 备用方案：使用当前内存中的验证者集合
			d.logger.Warn("⚠️ [calculateMissedBlocksWithActual] 无法获取epoch验证者集合，使用当前内存中的验证者集合",
				"epochToCheck", epochToCheck,
				"error", err)
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
		d.logger.Info("📊 [calculateMissedBlocksWithActual] 预期出块统计",
			"validator", validatorAddr.String(),
			"epochToCheck", epochToCheck,
			"expectedBlocks", expectedBlocks)

		// 查询区块历史，计算实际出块数
		if d.blockchain != nil {
			actualBlocks = 0
			consensusSwitchHeight := d.config.ConsensusSwitchHeight

			// 🆕 修复：正确计算epoch对应的区块范围
			epochStartBlock := consensusSwitchHeight + epochToCheck*blocksPerEpoch
			epochEndBlock := consensusSwitchHeight + (epochToCheck+1)*blocksPerEpoch
			d.logger.Info("📊 [calculateMissedBlocksWithActual] 区块范围",
				"validator", validatorAddr.String(),
				"epochToCheck", epochToCheck,
				"startBlock", epochStartBlock,
				"endBlock", epochEndBlock)

			// 查询这个epoch中该验证者实际出块的次数
			for blockNum := epochStartBlock; blockNum < epochEndBlock; blockNum++ {
				header, exists := d.blockchain.GetHeaderByNumber(blockNum)
				if (!exists || header == nil) && blockNum == epochEndBlock-1 {
					if pendingHeader := d.getPendingEpochEndHeader(blockNum); pendingHeader != nil {
						header = pendingHeader
						exists = true
						d.logger.Info("✅ [calculateMissedBlocksWithActual] 使用待写入的epoch结束区块头",
							"validator", validatorAddr.String(),
							"blockNumber", blockNum)
					}
				}

				if exists && header != nil {
					// 🆕 修复：通过Miner字段检查出块者
					if len(header.Miner) == 20 {
						minerAddr := types.Address(header.Miner)
						if minerAddr == validatorAddr {
							actualBlocks++
							d.logger.Info("✅ [calculateMissedBlocksWithActual] 匹配到验证者出块",
								"validator", validatorAddr.String(),
								"blockNumber", blockNum,
								"blockHash", header.Hash.String())
						} else {
							d.logger.Debug("ℹ️ [calculateMissedBlocksWithActual] 非目标验证者出块",
								"validator", validatorAddr.String(),
								"blockNumber", blockNum,
								"miner", minerAddr.String())
						}
					} else {
						d.logger.Debug("ℹ️ [calculateMissedBlocksWithActual] Miner长度异常",
							"blockNumber", blockNum,
							"minerLength", len(header.Miner))
					}
				} else {
					d.logger.Debug("ℹ️ [calculateMissedBlocksWithActual] 未找到区块",
						"validator", validatorAddr.String(),
						"blockNumber", blockNum)
				}
			}

			// 计算漏块数
			if expectedBlocks > actualBlocks {
				missedBlocks = expectedBlocks - actualBlocks
			}
			d.logger.Info("📊 [calculateMissedBlocksWithActual] 实际出块统计完成",
				"validator", validatorAddr.String(),
				"epochToCheck", epochToCheck,
				"expectedBlocks", expectedBlocks,
				"actualBlocks", actualBlocks,
				"missedBlocks", missedBlocks)
		} else {
			d.logger.Warn("⚠️ [calculateMissedBlocksWithActual] 区块链实例为空，无法统计实际出块",
				"validator", validatorAddr.String())
		}
	} else {
		d.logger.Info("ℹ️ [calculateMissedBlocksWithActual] endEpoch <= startEpoch，跳过统计",
			"validator", validatorAddr.String(),
			"startEpoch", startEpoch,
			"endEpoch", endEpoch)
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

// applyNextEpochValidatorsFromExtra 使用区块ExtraData中的验证者集合更新本地状态
func (d *DPoS) applyNextEpochValidatorsFromExtra(validators validator.AccountSet, blockNumber uint64) error {
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

// executeSlashing 执行削减惩罚（按比例削减每个投票者的质押金额）
func (d *DPoS) executeSlashing(
	validatorAddr types.Address,
	slashRate uint64,
	blockNumber uint64,
	epochNumber uint64,
	reason string,
	missedBlocks uint64,
	missedBlocksPercentage uint64,
	doubleSigningHeight uint64,
) error {
	d.logger.Info("🔨 ===== 开始执行削减惩罚 =====",
		"validator", validatorAddr.String(),
		"slashRate", slashRate,
		"基点",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber,
		"reason", reason)

	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 1. 获取验证者的当前 VotingPower
	validator, err := d.state.StakeStore.GetDelegateInfo(validatorAddr)
	if err != nil || validator == nil {
		return fmt.Errorf("failed to get validator info: %w", err)
	}

	oldVotingPower := new(big.Int).Set(validator.VotingPower)
	if oldVotingPower.Sign() == 0 {
		d.logger.Warn("⚠️ 验证者投票权重为0，跳过削减", "validator", validatorAddr.String())
		return nil
	}

	d.logger.Info("📊 验证者当前投票权重",
		"validator", validatorAddr.String(),
		"oldVotingPower", oldVotingPower.String())

	// 2. 获取所有投票给该验证者的质押记录
	allStakes, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	// 筛选出投票给该验证者的记录
	var validatorStakes []*StakeInfo
	for _, stake := range allStakes {
		if stake != nil && stake.Delegate == validatorAddr && stake.Amount != nil && stake.Amount.Sign() > 0 {
			validatorStakes = append(validatorStakes, stake)
		}
	}

	if len(validatorStakes) == 0 {
		d.logger.Warn("⚠️ 没有找到该验证者的质押记录，跳过削减", "validator", validatorAddr.String())
		return nil
	}

	d.logger.Info("📋 找到质押记录",
		"validator", validatorAddr.String(),
		"stakesCount", len(validatorStakes))

	// 3. 按投票者聚合（一个投票者可能有多条记录）
	voterStakesMap := make(map[types.Address]*big.Int) // voter -> 总投票金额
	for _, stake := range validatorStakes {
		if stake.Amount != nil && stake.Amount.Sign() > 0 {
			if existing, exists := voterStakesMap[stake.Staker]; exists {
				existing.Add(existing, stake.Amount)
			} else {
				voterStakesMap[stake.Staker] = new(big.Int).Set(stake.Amount)
			}
		}
	}

	// 4. 按比例削减每个投票者的质押金额
	totalSlashAmount := big.NewInt(0)
	newVotingPower := big.NewInt(0)

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

	for voterAddr, oldVoteAmount := range voterStakesMap {
		// 4.1 计算该投票者的削减金额（按比例）
		voterSlashAmount := new(big.Int).Mul(oldVoteAmount, big.NewInt(int64(slashRate)))
		voterSlashAmount.Div(voterSlashAmount, big.NewInt(10000)) // 基点转换

		// 4.2 计算削减后的金额
		newVoteAmount := new(big.Int).Sub(oldVoteAmount, voterSlashAmount)
		if newVoteAmount.Sign() < 0 {
			newVoteAmount = big.NewInt(0)
		}

		totalSlashAmount.Add(totalSlashAmount, voterSlashAmount)
		newVotingPower.Add(newVotingPower, newVoteAmount)

		d.logger.Info("🔨 削减投票者",
			"voter", voterAddr.String(),
			"oldAmount", oldVoteAmount.String(),
			"slashAmount", voterSlashAmount.String(),
			"newAmount", newVoteAmount.String())

		// 4.3 更新该投票者的所有 StakeInfo 记录
		for _, stake := range validatorStakes {
			if stake.Staker == voterAddr {
				// 按该记录在总金额中的比例计算削减金额
				recordSlashAmount := new(big.Int).Mul(stake.Amount, big.NewInt(int64(slashRate)))
				recordSlashAmount.Div(recordSlashAmount, big.NewInt(10000))

				recordNewAmount := new(big.Int).Sub(stake.Amount, recordSlashAmount)
				if recordNewAmount.Sign() < 0 {
					recordNewAmount = big.NewInt(0)
				}

				// 保存原始金额（如果还没有保存）
				if stake.OriginalAmount == nil {
					stake.OriginalAmount = new(big.Int).Set(stake.Amount)
				}

				// 创建削减记录
				slashingRecord := &SlashingRecord{
					ValidatorAddr:          validatorAddr,
					BlockNumber:            blockNumber,
					EpochNumber:            epochNumber,
					Timestamp:              uint64(time.Now().Unix()),
					SlashAmount:            recordSlashAmount,
					OldVoteAmount:          new(big.Int).Set(stake.Amount), // 削减前的金额
					NewVoteAmount:          recordNewAmount,                // 削减后的金额
					SlashRate:              slashRate,
					Reason:                 reason,
					MissedBlocks:           missedBlocks,
					MissedBlocksPercentage: missedBlocksPercentage,
					DoubleSigningHeight:    doubleSigningHeight,
				}

				// 更新 StakeInfo（使用外部事务）
				if err := d.updateStakingInfoAfterSlashing(
					voterAddr,
					validatorAddr,
					recordNewAmount,
					stake.Amount,
					slashingRecord,
					dbTx, // 🆕 传入外部事务，避免嵌套事务
				); err != nil {
					d.logger.Warn("⚠️ 更新质押记录失败",
						"voter", voterAddr.String(),
						"error", err)
				}
			}
		}

		// 4.4 更新 VoterInfo.DelegateVotes（使用外部事务）
		if err := d.updateVoterVoteAmountForValidator(
			voterAddr,
			validatorAddr,
			newVoteAmount,
			oldVoteAmount,
			voterSlashAmount,
			slashRate,
			blockNumber,
			epochNumber,
			reason,
			missedBlocks,
			missedBlocksPercentage,
			doubleSigningHeight,
			dbTx, // 🆕 传入外部事务，避免嵌套事务
		); err != nil {
			d.logger.Warn("⚠️ 更新投票者信息失败",
				"voter", voterAddr.String(),
				"error", err)
		}
	}

	// 5. 更新验证者的 VotingPower（使用外部事务）
	if err := d.updateVotingPowerInDatabaseWithTx(validatorAddr, newVotingPower, dbTx); err != nil {
		return fmt.Errorf("failed to update validator voting power: %w", err)
	}

	// 提交事务
	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 6. 保存削减历史（验证者级别）
	slashingHistory := &SlashingHistory{
		ValidatorAddr:          validatorAddr,
		BlockNumber:            blockNumber,
		EpochNumber:            epochNumber,
		Timestamp:              uint64(time.Now().Unix()),
		SlashAmount:            totalSlashAmount,
		OldVotingPower:         oldVotingPower,
		NewVotingPower:         newVotingPower,
		SlashRate:              slashRate,
		Reason:                 reason,
		MissedBlocks:           missedBlocks,
		MissedBlocksPercentage: missedBlocksPercentage,
		DoubleSigningHeight:    doubleSigningHeight,
	}
	if err := d.state.StakeStore.SaveSlashingHistory(slashingHistory); err != nil {
		d.logger.Warn("⚠️ 保存削减历史失败", "error", err)
	}

	d.logger.Info("✅ ===== 削减惩罚执行完成 =====",
		"validator", validatorAddr.String(),
		"oldVotingPower", oldVotingPower.String(),
		"totalSlashAmount", totalSlashAmount.String(),
		"newVotingPower", newVotingPower.String(),
		"slashRate", slashRate,
		"基点")

	return nil
}

// updateStakingInfoInDatabase 更新数据库中的质押记录（使用相同的 key）
func (d *DPoS) updateStakingInfoInDatabase(stake *StakeInfo, dbTx *bolt.Tx) error {
	bucket := dbTx.Bucket([]byte("StakingInfo"))
	if bucket == nil {
		return fmt.Errorf("staking info bucket not found")
	}

	// 使用复合 key: staker (20 bytes) + delegate (20 bytes) + timestamp (8 bytes) = 48 bytes
	key := make([]byte, 48)
	copy(key[0:20], stake.Staker[:])
	copy(key[20:40], stake.Delegate[:])
	binary.BigEndian.PutUint64(key[40:48], stake.StartTime)

	// 序列化更新后的记录
	data, err := json.Marshal(stake)
	if err != nil {
		return fmt.Errorf("failed to marshal staking info: %w", err)
	}

	// 更新数据库中的记录（如果记录存在）
	if existingData := bucket.Get(key); existingData != nil {
		// 记录存在，更新它
		if err := bucket.Put(key, data); err != nil {
			return fmt.Errorf("failed to update staking info: %w", err)
		}
		d.logger.Debug("✅ 更新现有质押记录",
			"staker", stake.Staker.String(),
			"delegate", stake.Delegate.String(),
			"newAmount", stake.Amount.String())
	} else {
		// 记录不存在，遍历所有记录，找到 staker + delegate 匹配的记录
		cursor := bucket.Cursor()
		found := false
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			_ = v // 避免未使用变量警告
			if len(k) >= 40 {
				// 检查 staker 和 delegate 是否匹配
				stakerMatch := len(k) >= 20 && types.Address(k[0:20]) == stake.Staker
				delegateMatch := len(k) >= 40 && types.Address(k[20:40]) == stake.Delegate
				if stakerMatch && delegateMatch {
					// 更新这条记录
					if err := bucket.Put(k, data); err != nil {
						d.logger.Error("❌ 更新质押记录失败", "error", err)
						continue
					}
					found = true
					d.logger.Debug("✅ 找到并更新匹配的质押记录",
						"staker", stake.Staker.String(),
						"delegate", stake.Delegate.String(),
						"newAmount", stake.Amount.String())
				}
			}
		}
		if !found {
			d.logger.Warn("⚠️ 未找到匹配的质押记录",
				"staker", stake.Staker.String(),
				"delegate", stake.Delegate.String())
		}
	}

	return nil
}

// updateVoterVoteAmountForValidator 更新委托人对特定验证者的投票金额并记录削减
func (d *DPoS) updateVoterVoteAmountForValidator(
	voterAddr types.Address,
	validatorAddr types.Address,
	newVoteAmount *big.Int,
	oldVoteAmount *big.Int,
	slashAmount *big.Int,
	slashRate uint64,
	blockNumber uint64,
	epochNumber uint64,
	reason string,
	missedBlocks uint64,
	missedBlocksPercentage uint64,
	doubleSigningHeight uint64,
	dbTx *bolt.Tx, // 🆕 使用外部事务，避免嵌套事务
) error {
	// 1. 获取 VoterInfo（使用外部事务）
	voterInfo, err := d.state.StakeStore.getVoterInfo(voterAddr, dbTx)
	if err != nil {
		return fmt.Errorf("failed to get voter info: %w", err)
	}

	if voterInfo == nil {
		return fmt.Errorf("voter info not found: %s", voterAddr.String())
	}

	// 2. 初始化 DelegateVotes 和 SlashingRecords（如果不存在）
	if voterInfo.DelegateVotes == nil {
		voterInfo.DelegateVotes = make(map[types.Address]*big.Int)
	}
	if voterInfo.SlashingRecords == nil {
		voterInfo.SlashingRecords = make(map[types.Address][]*SlashingRecord)
	}

	// 3. 更新 DelegateVotes
	voterInfo.DelegateVotes[validatorAddr] = newVoteAmount

	// 4. 创建削减记录
	slashingRecord := &SlashingRecord{
		ValidatorAddr:          validatorAddr,
		BlockNumber:            blockNumber,
		EpochNumber:            epochNumber,
		Timestamp:              uint64(time.Now().Unix()),
		SlashAmount:            slashAmount,
		OldVoteAmount:          oldVoteAmount,
		NewVoteAmount:          newVoteAmount,
		SlashRate:              slashRate,
		Reason:                 reason,
		MissedBlocks:           missedBlocks,
		MissedBlocksPercentage: missedBlocksPercentage,
		DoubleSigningHeight:    doubleSigningHeight,
	}

	// 5. 添加到 SlashingRecords
	voterInfo.SlashingRecords[validatorAddr] = append(
		voterInfo.SlashingRecords[validatorAddr],
		slashingRecord,
	)

	// 6. 更新内存中的 VoterInfo
	d.voters[voterAddr] = voterInfo

	// 7. 保存到数据库（使用外部事务）
	if err := d.state.StakeStore.setVoterInfo(voterAddr, voterInfo, dbTx); err != nil {
		return fmt.Errorf("failed to save voter info: %w", err)
	}

	return nil
}

// updateStakingInfoAfterSlashing 更新StakingInfo记录（削减后）
func (d *DPoS) updateStakingInfoAfterSlashing(
	voterAddr types.Address,
	validatorAddr types.Address,
	newAmount *big.Int,
	oldAmount *big.Int,
	slashingRecord *SlashingRecord,
	dbTx *bolt.Tx, // 🆕 使用外部事务，避免嵌套事务
) error {
	// 如果 dbTx 为 nil，开启新事务（兼容性）
	if dbTx == nil {
		var err error
		dbTx, err = d.state.beginDBTransaction(true)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer dbTx.Rollback()
	}

	// 1. 获取所有 StakeInfo 记录（在事务中读取）
	allStakes, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	bucket := dbTx.Bucket([]byte("StakingInfo"))
	if bucket == nil {
		return fmt.Errorf("staking info bucket not found")
	}

	for _, stake := range allStakes {
		if stake != nil && stake.Staker == voterAddr && stake.Delegate == validatorAddr {
			// 3. 更新金额
			stake.Amount = newAmount

			// 4. 保存原始金额（如果还没有保存）
			if stake.OriginalAmount == nil {
				stake.OriginalAmount = new(big.Int).Set(oldAmount)
			}

			// 5. 添加削减记录
			if stake.SlashingRecords == nil {
				stake.SlashingRecords = make([]*SlashingRecord, 0)
			}
			stake.SlashingRecords = append(stake.SlashingRecords, slashingRecord)

			// 6. 保存到数据库（使用复合 key）
			key := make([]byte, 48)
			copy(key[0:20], stake.Staker[:])
			copy(key[20:40], stake.Delegate[:])
			binary.BigEndian.PutUint64(key[40:48], stake.StartTime)

			data, err := json.Marshal(stake)
			if err != nil {
				d.logger.Warn("⚠️ 序列化质押记录失败",
					"staker", stake.Staker.String(),
					"error", err)
				continue
			}

			if err := bucket.Put(key, data); err != nil {
				d.logger.Warn("⚠️ 更新质押记录失败",
					"staker", stake.Staker.String(),
					"error", err)
				continue
			}
		}
	}

	// 🆕 如果使用的是外部事务，不在这里提交（由调用者提交）
	// 如果开启的是新事务，需要提交（但这种情况不应该发生，因为现在总是传入事务）
	// 注意：这里不提交外部事务，由 executeSlashing 统一提交

	return nil
}
