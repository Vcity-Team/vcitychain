package dpos

import (
	faultmodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/fault"
	"github.com/Vcity-Team/vcitychain/types"
)

// initFaultModule 初始化故障管理模块
func (d *DPoS) initFaultModule() {
	if d.fault != nil {
		return
	}

	deps := d.buildFaultModuleDependencies()
	d.fault = faultmodule.NewManager(deps)
}

func (d *DPoS) buildFaultModuleDependencies() faultmodule.Dependencies {
	deps := faultmodule.Dependencies{
		Logger: d.logger.Named("modules.fault"),
	}

	deps.DetectFaults = func(blockNumber uint64) ([]faultmodule.LocalFaultFlagInfo, error) {
		flags, err := d.detectValidatorFaults(blockNumber)
		if err != nil {
			return nil, err
		}

		result := make([]faultmodule.LocalFaultFlagInfo, 0, len(flags))
		for _, flag := range flags {
			result = append(result, faultmodule.LocalFaultFlagInfo{
				NodeAddress:            flag.NodeAddress,
				EpochNumber:            flag.EpochNumber,
				Reason:                 flag.Reason,
				IsFaulty:               flag.IsFaulty,
				MissedBlocks:           flag.MissedBlocks,
				ActualBlocks:           flag.ActualBlocks,
				ExpectedBlocks:         flag.ExpectedBlocks,
				MissedBlocksPercentage: flag.MissedBlocksPercentage,
				LastUpdateTime:         flag.LastUpdateTime,
				LastFaultyEpoch:        flag.LastFaultyEpoch,
				DoubleSigningHeight:    flag.DoubleSigningHeight,
			})
		}
		return result, nil
	}

	deps.IsValidatorFaulty = func(address types.Address) (bool, error) {
		return d.IsValidatorFaulty(address)
	}

	deps.GetFaultInfo = func(address types.Address) map[string]interface{} {
		return d.getValidatorFaultInfo(address)
	}

	return deps
}
