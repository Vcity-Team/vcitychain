package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// FaultDetector 负责检测验证者故障
type FaultDetector struct {
	dposInstance *DPoS
	logger       hclog.Logger

	// 依赖组件
	validatorProvider *ValidatorProvider
	blockCounter      *BlockCounter
	faultCalculator   *FaultCalculator
	slashingCollector *SlashingCollector
}

// NewFaultDetector 创建故障检测器
func NewFaultDetector(dposInstance *DPoS, logger hclog.Logger) *FaultDetector {
	return &FaultDetector{
		dposInstance:      dposInstance,
		logger:            logger.Named("fault-detector"),
		validatorProvider: NewValidatorProvider(dposInstance, logger),
		blockCounter:      NewBlockCounter(dposInstance, logger),
		faultCalculator:   NewFaultCalculator(dposInstance, logger),
		slashingCollector: NewSlashingCollector(dposInstance, logger),
	}
}

// DetectFaults 检测验证者故障（主入口）
func (fd *FaultDetector) DetectFaults(blockNumber uint64) ([]FaultFlagInfo, error) {
	// 1. 检查epoch变化
	epochInfo, shouldSkip := fd.checkEpochChange(blockNumber)
	if shouldSkip {
		return nil, nil
	}

	// 2. 获取要检测的epoch的验证者集合（应该是上一个epoch的验证者）
	// 🔧 修复：应该检测上一个epoch的验证者故障，所以获取上一个epoch的验证者集合
	validators, err := fd.validatorProvider.GetValidatorsForDetection(epochInfo)
	if err != nil {
		return nil, err
	}

	// 更新dposInstance的epochValidators字段（向后兼容）
	fd.dposInstance.epochValidators = validators

	// 3. 获取上一个epoch的验证者集合，用于判断新加入的验证者
	// 注意：这里获取的是"上一个epoch"的验证者集合，用于判断哪些验证者是新加入的
	previousEpochValidators := fd.validatorProvider.GetPreviousEpochValidators(epochInfo.PreviousEpochNumber)
	previousEpochValidatorMap := fd.validatorProvider.BuildPreviousEpochValidatorMap(previousEpochValidators)

	fd.logger.Info("ℹ️ 上一个epoch验证者映射构建完成",
		"previousEpoch", epochInfo.PreviousEpochNumber,
		"validatorCount", len(previousEpochValidatorMap))

	// 4. 检测每个验证者的故障
	faultFlags := fd.detectValidatorFaults(epochInfo, validators, previousEpochValidatorMap)

	// 5. 收集消减信息
	fd.slashingCollector.CollectSlashingInfo(faultFlags, epochInfo)

	// 6. 更新epoch
	fd.dposInstance.currentEpoch = epochInfo.CurrentEpoch

	return faultFlags, nil
}

// checkEpochChange 检查epoch变化
func (fd *FaultDetector) checkEpochChange(blockNumber uint64) (EpochInfo, bool) {
	blocksPerEpoch := fd.dposInstance.getEpochSize()
	consensusSwitchHeight := fd.dposInstance.config.ConsensusSwitchHeight

	// 计算当前epoch编号（从1开始）
	var currentEpochNumber uint64
	if blockNumber < consensusSwitchHeight {
		currentEpochNumber = 0
	} else {
		dposBlockNumber := blockNumber - consensusSwitchHeight
		currentEpochNumber = (dposBlockNumber / blocksPerEpoch) + 1 // 编号（从1开始）
	}
	// 因为我们要检测的是 epochToCheckNumber，所以应该与 epochToCheckNumber - 1 比较
	previousEpochNumber := uint64(0)
	if fd.dposInstance.currentEpoch > 0 {
		previousEpochNumber = fd.dposInstance.currentEpoch + 1 // 索引转编号
	} else if fd.dposInstance.currentEpoch == 0 {
		previousEpochNumber = 1 // epoch索引0对应编号1
	}

	// 如果epoch没有变化，则跳过。逻辑的有效场景 1.区块重试：防止同一区块重试时重复检测 同一区块多次处理：防止重复检测（虽然当前代码不适用）
	currentEpochIndexForCompare := uint64(0)
	if currentEpochNumber > 0 {
		currentEpochIndexForCompare = currentEpochNumber - 1
	}
	if currentEpochIndexForCompare == fd.dposInstance.currentEpoch {
		fd.logger.Info("ℹ️ Epoch未变化，跳过故障检测",
			"currentEpoch", currentEpochNumber,
			"previousEpoch", previousEpochNumber)
		return EpochInfo{}, true
	}

	// 🔧 修复：应该检测当前epoch的故障（因为是在epoch结束区块检测）
	// 区块7417是epoch 6的最后一个区块，应该检测epoch 6的故障
	// epoch编号从1开始：共识切换高度7370是epoch 1的开始
	epochToCheckNumber := currentEpochNumber

	// 🔧 修复：重新计算 previousEpochNumber，应该使用 epochToCheckNumber - 1 来判断新加入的验证者
	if epochToCheckNumber > 1 {
		previousEpochNumber = epochToCheckNumber - 1
	} else {
		previousEpochNumber = 0
	}

	// 计算检测epoch的区块区间范围
	// epoch N 从 consensusSwitchHeight + (N-1) * blocksPerEpoch 开始
	// epoch N 到 consensusSwitchHeight + N * blocksPerEpoch - 1 结束
	var epochStartBlock, epochEndBlock uint64

	if epochToCheckNumber == 0 {
		epochStartBlock = 0
		epochEndBlock = 0
	} else {
		epochStartBlock = consensusSwitchHeight + (epochToCheckNumber-1)*blocksPerEpoch
		epochEndBlock = consensusSwitchHeight + epochToCheckNumber*blocksPerEpoch - 1
	}

	missedBlocksPercentageThreshold := fd.dposInstance.getMissedBlocksPercentage()
	fd.logger.Info("🔍 ===== 开始检测验证者故障 =====",
		"blockNumber", blockNumber,
		"currentEpoch", currentEpochNumber,
		"previousEpoch", previousEpochNumber,
		"epochToCheck", epochToCheckNumber,
		"epochToCheckBlockRange", fmt.Sprintf("[%d, %d]", epochStartBlock, epochEndBlock),
		"epochToCheckBlockCount", epochEndBlock-epochStartBlock+1,
		"consensusSwitchHeight", consensusSwitchHeight,
		"blocksPerEpoch", blocksPerEpoch,
		"missedBlocksPercentage", missedBlocksPercentageThreshold)

	// 计算索引（用于存储和后续计算）
	currentEpochIndexForStorage := uint64(0)
	if currentEpochNumber > 0 {
		currentEpochIndexForStorage = currentEpochNumber - 1
	}

	return EpochInfo{
		CurrentEpoch:        currentEpochIndexForStorage, // 存储索引（用于fd.dposInstance.currentEpoch）
		CurrentEpochNumber:  currentEpochNumber,
		PreviousEpochNumber: previousEpochNumber,
		EpochToCheck:        currentEpochIndexForStorage, // 存储索引（用于CalculateBlockStats）
		EpochToCheckNumber:  epochToCheckNumber,
	}, false
}

// detectValidatorFaults 检测每个验证者的故障
func (fd *FaultDetector) detectValidatorFaults(
	epochInfo EpochInfo,
	validators validator.AccountSet,
	previousEpochValidatorMap map[types.Address]bool,
) []FaultFlagInfo {
	var faultFlags []FaultFlagInfo

	fd.logger.Info("🔍 开始计算每个验证者的漏块数...")

	for _, validator := range validators {
		// 判断是否是新加入的验证者
		isNewlyAdded := false
		if len(previousEpochValidatorMap) > 0 {
			if _, wasInPreviousEpoch := previousEpochValidatorMap[validator.Address]; !wasInPreviousEpoch {
				isNewlyAdded = true
				fd.logger.Info("🆕 检测到新加入的验证者，跳过故障检测和消减",
					"address", validator.Address.String(),
					"currentEpoch", epochInfo.CurrentEpochNumber,
					"previousEpoch", epochInfo.PreviousEpochNumber,
					"note", "新加入的验证者在本epoch还没有机会出块，不应被消减")
			}
		}

		// 如果该验证者已经在上一epoch被标记为故障，跳过重新检测，保留原故障原因
		// 🔧 但是，如果该验证者有恢复提案，需要重新检测，而不是使用旧的故障信息
		if faultInfo := fd.dposInstance.getValidatorFaultInfo(validator.Address); faultInfo != nil {
			if alreadyFaulty, ok := faultInfo["isFaulty"].(bool); ok && alreadyFaulty {
				// 检查该验证者是否有恢复提案（当前epoch或下一个epoch）
				hasRecoveryProposal := false
				currentEpoch := epochInfo.EpochToCheckNumber
				// 检查当前epoch的恢复提案
				currentProposals := fd.dposInstance.governanceLoadScheduled(currentEpoch)
				for _, prop := range currentProposals {
					if prop != nil && prop.ProposalType == "validator_recovery" {
						propValidatorAddr := prop.ValidatorAddress
						if propValidatorAddr == (types.Address{}) && prop.Parameter != "" {
							propValidatorAddr = types.StringToAddress(prop.Parameter)
						}
						if propValidatorAddr == validator.Address {
							hasRecoveryProposal = true
							break
						}
					}
				}
				// 如果当前epoch没有，检查下一个epoch
				if !hasRecoveryProposal {
					nextEpoch := currentEpoch + 1
					nextProposals := fd.dposInstance.governanceLoadScheduled(nextEpoch)
					for _, prop := range nextProposals {
						if prop != nil && prop.ProposalType == "validator_recovery" {
							if prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch == nextEpoch && !prop.Schedule.Applied {
								propValidatorAddr := prop.ValidatorAddress
								if propValidatorAddr == (types.Address{}) && prop.Parameter != "" {
									propValidatorAddr = types.StringToAddress(prop.Parameter)
								}
								if propValidatorAddr == validator.Address {
									hasRecoveryProposal = true
									break
								}
							}
						}
					}
				}

				if hasRecoveryProposal {
					// 🔧 如果有恢复提案，跳过"已处于故障状态"的检查，重新检测该验证者的实际出块情况
					fd.logger.Info("🔄 验证者有恢复提案，重新检测故障状态（不使用旧的故障信息）",
						"address", validator.Address.String(),
						"currentEpoch", currentEpoch)
					// 继续执行下面的重新检测逻辑
				} else {
					// 没有恢复提案，使用旧的故障信息
					existingReason, _ := faultInfo["reason"].(string)
					existingMissedBlocks := toUint64Safe(faultInfo["missedBlocks"])
					existingLastUpdate := toUint64Safe(faultInfo["lastUpdateTime"])
					existingLastFaultyEpoch := toUint64Safe(faultInfo["lastFaultyEpoch"])

					fd.logger.Info("⏭️ 验证者已处于故障状态，跳过本epoch检测",
						"address", validator.Address.String(),
						"existingReason", existingReason,
						"lastFaultyEpoch", existingLastFaultyEpoch)

					faultFlags = append(faultFlags, FaultFlagInfo{
						NodeAddress:            validator.Address,
						IsFaulty:               true,
						MissedBlocks:           existingMissedBlocks,
						ActualBlocks:           0,
						ExpectedBlocks:         0,
						MissedBlocksPercentage: 0,
						LastUpdateTime:         existingLastUpdate,
						EpochNumber:            epochInfo.EpochToCheckNumber,
						LastFaultyEpoch:        existingLastFaultyEpoch,
						Reason:                 existingReason,
					})
					continue
				}
			}
		}

		// 计算出块统计
		// epochToCheck存储的是索引，直接使用
		stats := fd.blockCounter.CalculateBlockStats(validator.Address, epochInfo.EpochToCheck, epochInfo.EpochToCheck)

		// 更新漏块数计数
		fd.dposInstance.missedBlocksCount[validator.Address] = stats.MissedBlocks

		// 计算故障标志
		faultFlag := fd.faultCalculator.CalculateFaultFlag(validator, stats, epochInfo, isNewlyAdded)
		faultFlag.LastUpdateTime = uint64(time.Now().Unix())

		faultFlags = append(faultFlags, faultFlag)

		if stats.MissedBlocks > 0 {
			fd.logger.Info("📊 验证者漏块统计",
				"address", validator.Address.String(),
				"expectedBlocks", stats.ExpectedBlocks,
				"actualBlocks", stats.ActualBlocks,
				"missedBlocks", stats.MissedBlocks,
				"missedBlocksPercentage", faultFlag.MissedBlocksPercentage,
				"thresholdPercentage", fd.dposInstance.getMissedBlocksPercentage(),
				"isFaulty", faultFlag.IsFaulty,
				"lastFaultyEpoch", faultFlag.LastFaultyEpoch)
		}
	}

	fd.logger.Info("🏁 ===== 故障检测完成 =====",
		"faultyValidatorsCount", len(faultFlags),
		"currentEpoch", epochInfo.CurrentEpochNumber)

	if len(faultFlags) > 0 {
		fd.logger.Info("📋 验证者故障检测结果汇总:")
		faultyCount := 0
		for i, faultFlag := range faultFlags {
			if faultFlag.IsFaulty {
				faultyCount++
				fd.logger.Info("👤 验证者故障详情",
					"index", i+1,
					"address", faultFlag.NodeAddress.String(),
					"expectedBlocks", faultFlag.ExpectedBlocks,
					"actualBlocks", faultFlag.ActualBlocks,
					"missedBlocks", faultFlag.MissedBlocks,
					"missedBlocksPercentage", faultFlag.MissedBlocksPercentage,
					"isFaulty", faultFlag.IsFaulty,
					"reason", faultFlag.Reason)
			}
		}
		fd.logger.Info("📊 故障统计",
			"totalValidators", len(faultFlags),
			"faultyValidators", faultyCount,
			"normalValidators", len(faultFlags)-faultyCount)
	} else {
		fd.logger.Info("✅ 没有检测到验证者")
	}

	return faultFlags
}

func toUint64Safe(value interface{}) uint64 {
	switch v := value.(type) {
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
	return 0
}
