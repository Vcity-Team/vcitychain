package dpos

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/hashicorp/go-hclog"
)

// FaultCalculator 负责计算故障状态
type FaultCalculator struct {
	dposInstance *DPoS
	logger       hclog.Logger
}

// NewFaultCalculator 创建故障计算器
func NewFaultCalculator(dposInstance *DPoS, logger hclog.Logger) *FaultCalculator {
	return &FaultCalculator{
		dposInstance: dposInstance,
		logger:       logger.Named("fault-calculator"),
	}
}

// CalculateFaultFlag 计算验证者的故障标志
func (fc *FaultCalculator) CalculateFaultFlag(
	validator *validator.ValidatorMetadata,
	stats BlockStats,
	epochInfo EpochInfo,
	isNewlyAdded bool,
) FaultFlagInfo {
	// 如果是新加入的验证者，跳过故障检测和消减，但仍记录为正常状态
	if isNewlyAdded {
		return FaultFlagInfo{
			NodeAddress:            validator.Address,
			IsFaulty:               false,
			MissedBlocks:           0,
			ActualBlocks:           0,
			ExpectedBlocks:         0,
			MissedBlocksPercentage: 0,
			LastUpdateTime:         0, // 将在调用处设置
			EpochNumber:            epochInfo.EpochToCheckNumber,
			LastFaultyEpoch:        0,
			Reason:                 fmt.Sprintf("Epoch %d: 新加入的验证者，跳过故障检测", epochInfo.EpochToCheckNumber),
		}
	}

	// 计算漏块率（基点，10000 = 100%）
	missedBlocksPercentage := uint64(0)
	if stats.ExpectedBlocks > 0 {
		missedBlocksPercentage = (stats.MissedBlocks * 10000) / stats.ExpectedBlocks
	}

	missedBlocksPercentageThreshold := fc.dposInstance.getMissedBlocksPercentage()
	isFaulty := stats.ExpectedBlocks > 0 && missedBlocksPercentage >= missedBlocksPercentageThreshold

	fc.logger.Info("🔍 [故障判断]",
		"validator", validator.Address.String(),
		"epoch", epochInfo.EpochToCheckNumber,
		"missed", fmt.Sprintf("%d/%d", stats.MissedBlocks, stats.ExpectedBlocks),
		"pct", fmt.Sprintf("%dbp/%dbp", missedBlocksPercentage, missedBlocksPercentageThreshold),
		"faulty", isFaulty)

	// 获取上次故障的epoch
	lastFaultyEpoch := uint64(0)
	if fc.dposInstance.state != nil && fc.dposInstance.state.StakeStore != nil {
		if dbFaultInfo, err := fc.dposInstance.state.StakeStore.GetValidatorFaultStatus(validator.Address); err == nil && dbFaultInfo != nil {
			// 尝试从数据库获取上次故障的epoch
			if lfe, ok := dbFaultInfo["lastFaultyEpoch"].(float64); ok {
				lastFaultyEpoch = uint64(lfe)
			}
		}
	}

	// 如果当前有故障，更新lastFaultyEpoch为当前epoch；否则保留上次的值
	if isFaulty {
		lastFaultyEpoch = epochInfo.EpochToCheckNumber
	}

	var reason string
	if isFaulty {
		reason = fmt.Sprintf("Epoch %d: 漏块率已达到阈值: %d 基点 >= %d 基点 (漏块数: %d/%d)",
			epochInfo.EpochToCheckNumber, missedBlocksPercentage, missedBlocksPercentageThreshold, stats.MissedBlocks, stats.ExpectedBlocks)
	} else {
		reason = fmt.Sprintf("Epoch %d: 漏块率正常: %d 基点 < %d 基点 (漏块数: %d/%d)",
			epochInfo.EpochToCheckNumber, missedBlocksPercentage, missedBlocksPercentageThreshold, stats.MissedBlocks, stats.ExpectedBlocks)
	}

	return FaultFlagInfo{
		NodeAddress:            validator.Address,
		IsFaulty:               isFaulty,
		MissedBlocks:           stats.MissedBlocks,
		ActualBlocks:           stats.ActualBlocks,
		ExpectedBlocks:         stats.ExpectedBlocks,
		MissedBlocksPercentage: missedBlocksPercentage,
		LastUpdateTime:         0, // 将在调用处设置
		EpochNumber:            epochInfo.EpochToCheckNumber,
		LastFaultyEpoch:        lastFaultyEpoch,
		Reason:                 reason,
	}
}
