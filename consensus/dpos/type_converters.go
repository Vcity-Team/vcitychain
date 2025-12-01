package dpos

import (
	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
)

// ProposalConverters 提案相关类型转换器
type ProposalConverters struct{}

// ToCore 将本地提案列表转换为 core 类型
func (c *ProposalConverters) ToCore(proposals []*ParameterProposal) []core.RecoveryProposalInfo {
	result := make([]core.RecoveryProposalInfo, 0, len(proposals))
	for _, prop := range proposals {
		if prop == nil {
			continue
		}

		info := core.RecoveryProposalInfo{
			ID:               prop.ID,
			ProposalType:     prop.ProposalType,
			Parameter:        prop.Parameter,
			ValidatorAddress: prop.ValidatorAddress,
			Schedule: core.ProposalScheduleInfo{
				Scheduled:      prop.Schedule.Scheduled,
				EffectiveEpoch: prop.Schedule.EffectiveEpoch,
				Applied:        prop.Schedule.Applied,
				AppliedAtBlock: prop.Schedule.AppliedAtBlock,
			},
		}

		if info.ValidatorAddress == (types.Address{}) && prop.Parameter != "" {
			info.ValidatorAddress = types.StringToAddress(prop.Parameter)
		}

		result = append(result, info)
	}

	return result
}

// FaultFlagConverters 故障标志相关类型转换器
type FaultFlagConverters struct{}

// ToCore 将本地故障标志列表转换为 core 类型
func (c *FaultFlagConverters) ToCore(flags []FaultFlagInfo) []core.FaultFlagInfo {
	result := make([]core.FaultFlagInfo, 0, len(flags))
	for _, flag := range flags {
		result = append(result, c.ToCoreSingle(flag))
	}
	return result
}

// ToCoreSingle 将单个本地故障标志转换为 core 类型
func (c *FaultFlagConverters) ToCoreSingle(flag FaultFlagInfo) core.FaultFlagInfo {
	return core.FaultFlagInfo{
		ValidatorAddress:       flag.NodeAddress,
		IsFaulty:               flag.IsFaulty,
		MissedBlocks:           flag.MissedBlocks,
		ActualBlocks:           flag.ActualBlocks,
		ExpectedBlocks:         flag.ExpectedBlocks,
		MissedBlocksPercentage: flag.MissedBlocksPercentage,
		LastUpdateTime:         flag.LastUpdateTime,
		EpochNumber:            flag.EpochNumber,
		LastFaultyEpoch:        flag.LastFaultyEpoch,
		FaultType:              flag.Reason,
		Reason:                 flag.Reason,
		DoubleSigningHeight:    flag.DoubleSigningHeight,
	}
}

// FromCore 将 core 故障标志列表转换为本地类型
func (c *FaultFlagConverters) FromCore(flags []core.FaultFlagInfo) []FaultFlagInfo {
	result := make([]FaultFlagInfo, 0, len(flags))
	for _, flag := range flags {
		result = append(result, c.FromCoreSingle(flag))
	}
	return result
}

// FromCoreSingle 将单个 core 故障标志转换为本地类型
func (c *FaultFlagConverters) FromCoreSingle(flag core.FaultFlagInfo) FaultFlagInfo {
	return FaultFlagInfo{
		NodeAddress:            flag.ValidatorAddress,
		IsFaulty:               flag.IsFaulty,
		MissedBlocks:           flag.MissedBlocks,
		ActualBlocks:           flag.ActualBlocks,
		ExpectedBlocks:         flag.ExpectedBlocks,
		MissedBlocksPercentage: flag.MissedBlocksPercentage,
		LastUpdateTime:         flag.LastUpdateTime,
		EpochNumber:            flag.EpochNumber,
		LastFaultyEpoch:        flag.LastFaultyEpoch,
		Reason:                 flag.Reason,
		DoubleSigningHeight:    flag.DoubleSigningHeight,
	}
}

// 全局转换器实例（避免重复创建）
var (
	proposalConverters  = &ProposalConverters{}
	faultFlagConverters = &FaultFlagConverters{}
)

// convertParameterProposalsToCore 将参数提案转换为 core 类型（兼容旧代码）
func convertParameterProposalsToCore(list []*ParameterProposal) []core.RecoveryProposalInfo {
	return proposalConverters.ToCore(list)
}

// convertLocalFaultFlagsToCore 将本地故障标志转换为 core 类型（兼容旧代码）
func convertLocalFaultFlagsToCore(flags []FaultFlagInfo) []core.FaultFlagInfo {
	return faultFlagConverters.ToCore(flags)
}

// convertCoreFaultFlagsToLocal 将 core 故障标志转换为本地类型（兼容旧代码）
func convertCoreFaultFlagsToLocal(flags []core.FaultFlagInfo) []FaultFlagInfo {
	return faultFlagConverters.FromCore(flags)
}

// convertLocalFaultFlagToCore 将单个本地故障标志转换为 core 类型（兼容旧代码）
func convertLocalFaultFlagToCore(flag FaultFlagInfo) core.FaultFlagInfo {
	return faultFlagConverters.ToCoreSingle(flag)
}

// convertCoreFaultFlagToLocal 将单个 core 故障标志转换为本地类型（兼容旧代码）
func convertCoreFaultFlagToLocal(flag core.FaultFlagInfo) FaultFlagInfo {
	return faultFlagConverters.FromCoreSingle(flag)
}

