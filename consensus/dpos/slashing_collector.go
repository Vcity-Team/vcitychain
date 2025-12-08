package dpos

import (
	"time"

	"github.com/hashicorp/go-hclog"
)

// SlashingCollector 负责收集消减信息
type SlashingCollector struct {
	dposInstance *DPoS
	logger       hclog.Logger
}

// NewSlashingCollector 创建消减收集器
func NewSlashingCollector(dposInstance *DPoS, logger hclog.Logger) *SlashingCollector {
	return &SlashingCollector{
		dposInstance: dposInstance,
		logger:       logger.Named("slashing-collector"),
	}
}

// CollectSlashingInfo 收集消减信息
func (sc *SlashingCollector) CollectSlashingInfo(
	faultFlags []FaultFlagInfo,
	epochInfo EpochInfo,
) {
	// 遍历故障标志
	for _, faultFlag := range faultFlags {
		if faultFlag.IsFaulty {
			sc.logger.Info("🚨 ===== 检测到故障验证者 =====",
				"address", faultFlag.NodeAddress.String(),
				"expectedBlocks", faultFlag.ExpectedBlocks,
				"actualBlocks", faultFlag.ActualBlocks,
				"missedBlocks", faultFlag.MissedBlocks,
				"missedBlocksPercentage", faultFlag.MissedBlocksPercentage,
				"thresholdPercentage", sc.dposInstance.getMissedBlocksPercentage(),
				"reason", faultFlag.Reason)

			// 去重检查：检查该验证者是否已经在该epoch的pendingSlashingInfo中
			if sc.dposInstance.pendingSlashingInfo != nil {
				// 检查是否是同一个epoch
				if sc.dposInstance.pendingSlashingInfo.EpochNumber == epochInfo.EpochToCheckNumber {
					// 检查该验证者是否已经存在
					alreadyExists := false
					for _, existingOp := range sc.dposInstance.pendingSlashingInfo.Slashings {
						if existingOp.ValidatorAddr == faultFlag.NodeAddress {
							alreadyExists = true
							sc.logger.Info("ℹ️ 消减信息已存在，跳过重复收集",
								"validator", faultFlag.NodeAddress.String(),
								"epochNumber", epochInfo.EpochToCheckNumber,
								"note", "防止重复检测导致的重复收集")
							break
						}
					}
					if alreadyExists {
						continue // 跳过，不重复添加
					}
				}
			}

			// 收集消减信息到 pendingSlashingInfo（不直接执行）
			slashRate := sc.dposInstance.getMinorOffenseSlashRate()

			// 初始化 pendingSlashingInfo（如果还没有）
			if sc.dposInstance.pendingSlashingInfo == nil {
				sc.dposInstance.pendingSlashingInfo = &SlashingInfo{
					EpochNumber: epochInfo.EpochToCheckNumber,
					Slashings:   []*SlashingOperation{},
					Timestamp:   uint64(time.Now().Unix()),
				}
			}

			// 添加消减操作
			slashingOp := &SlashingOperation{
				ValidatorAddr:          faultFlag.NodeAddress,
				SlashRate:              slashRate,
				MissedBlocks:           faultFlag.MissedBlocks,
				MissedBlocksPercentage: faultFlag.MissedBlocksPercentage,
				Reason:                 faultFlag.Reason,
			}
			sc.dposInstance.pendingSlashingInfo.Slashings = append(sc.dposInstance.pendingSlashingInfo.Slashings, slashingOp)

			sc.logger.Info("✅ 消减信息已收集到pendingSlashingInfo",
				"validator", faultFlag.NodeAddress.String(),
				"slashRate", slashRate,
				"基点",
				"note", "将通过ExtraData传播给所有节点执行")
		}
	}
}
