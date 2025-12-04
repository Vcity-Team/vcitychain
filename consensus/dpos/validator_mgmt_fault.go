package dpos

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
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
	// 🆕 优先从参数系统读取 dpos_missed_blocks_percentage（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue("dpos_missed_blocks_percentage"); err == nil {
		switch v := paramValue.(type) {
		case uint64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 missed blocks percentage", "percentage", v)
				return v
			}
		case int64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 missed blocks percentage", "percentage", uint64(v))
				return uint64(v)
			}
		case float64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 missed blocks percentage", "percentage", uint64(v))
				return uint64(v)
			}
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed > 0 {
				d.logger.Debug("从参数系统读取 missed blocks percentage", "percentage", parsed)
				return parsed
			}
		}
	}

	// 如果参数系统没有值，从配置文件读取
	if val := d.getConfigUint64("dpos_missed_blocks_percentage", "missed_blocks_percentage"); val > 0 {
		return val
	}
	return 1000 // 默认值 10%
}

// getMinorOffenseSlashRate 获取轻度违规削减率（基点）
func (d *DPoS) getMinorOffenseSlashRate() uint64 {
	// 🆕 优先从参数系统读取 dpos_minor_offense_slash_rate（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue("dpos_minor_offense_slash_rate"); err == nil {
		switch v := paramValue.(type) {
		case uint64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 minor offense slash rate", "rate", v)
				return v
			}
		case int64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 minor offense slash rate", "rate", uint64(v))
				return uint64(v)
			}
		case float64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 minor offense slash rate", "rate", uint64(v))
				return uint64(v)
			}
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed > 0 {
				d.logger.Debug("从参数系统读取 minor offense slash rate", "rate", parsed)
				return parsed
			}
		}
	}

	// 如果参数系统没有值，从配置文件读取
	if val := d.getConfigUint64("dpos_minor_offense_slash_rate", "minor_offense_slash_rate"); val > 0 {
		return val
	}
	return 50 // 默认值 0.5%
}

// getSevereOffenseSlashRate 获取严重违规削减率（基点）
func (d *DPoS) getSevereOffenseSlashRate() uint64 {
	// 🆕 优先从参数系统读取 dpos_severe_offense_slash_rate（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue("dpos_severe_offense_slash_rate"); err == nil {
		switch v := paramValue.(type) {
		case uint64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 severe offense slash rate", "rate", v)
				return v
			}
		case int64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 severe offense slash rate", "rate", uint64(v))
				return uint64(v)
			}
		case float64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 severe offense slash rate", "rate", uint64(v))
				return uint64(v)
			}
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed > 0 {
				d.logger.Debug("从参数系统读取 severe offense slash rate", "rate", parsed)
				return parsed
			}
		}
	}

	// 如果参数系统没有值，从配置文件读取
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
		dbFaultInfo, err := d.state.StakeStore.GetValidatorFaultStatus(validatorAddr)
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
	sort.Slice(activeValidators, func(i, j int) bool {
		votingPowerCmp := activeValidators[i].VotingPower.Cmp(activeValidators[j].VotingPower)
		if votingPowerCmp != 0 {
			return votingPowerCmp > 0
		}
		return bytes.Compare(activeValidators[i].Address[:], activeValidators[j].Address[:]) < 0
	})

	// 4. 应用配置限制
	maxValidators := int(d.config.DPoSValidatorsCount)
	if maxValidators == 0 {
		maxValidators = int(d.config.DelegateCount)
	}

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
		d.runtime.delegates = finalValidators.Copy()
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
				d.logger.Debug("✅ 从ExtraData获取epoch验证者集合",
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

		// ✅ 修复：从该epoch开始区块的ExtraData或数据库获取该epoch的验证者集合
		validatorsCount := uint64(0)
		if epochValidators, err := d.getValidatorsForEpoch(epochNumberForCheck); err == nil && len(epochValidators) > 0 {
			validatorsCount = uint64(len(epochValidators))
		} else {
			// 备用方案：使用当前内存中的验证者集合
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

	if d.stateMgr != nil {
		if err := d.stateMgr.SaveValidators(blockNumber, validators); err != nil {
			d.logger.Warn("state manager 保存验证者集合失败，尝试回退",
				"error", err,
				"count", len(validators))
		} else {
			d.logger.Info("✅ 下一个epoch验证者集合保存成功（state模块）",
				"count", len(validators),
				"blockNumber", blockNumber)
			return nil
		}
	}

	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("stake store not available")
	}

	if err := d.state.StakeStore.SaveEpochValidators(validators); err != nil {
		d.logger.Error("❌ 保存下一个epoch验证者集合失败", "error", err)
		return err
	}

	d.logger.Info("✅ 下一个epoch验证者集合保存成功（legacy）", "count", len(validators))
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

	// 🆕 幂等性检查：检查是否已经执行过该区块的消减
	if hasHistory, err := d.state.StakeStore.HasSlashingHistory(validatorAddr, blockNumber); err != nil {
		d.logger.Warn("⚠️ 检查消减历史失败，继续执行（可能重复）",
			"validator", validatorAddr.String(),
			"blockNumber", blockNumber,
			"error", err)
	} else if hasHistory {
		d.logger.Info("ℹ️ 消减已执行过，跳过重复执行（幂等性保护）",
			"validator", validatorAddr.String(),
			"blockNumber", blockNumber,
			"epochNumber", epochNumber,
			"note", "防止重复处理区块导致的重复消减")
		return nil // 已执行过，直接返回成功
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
