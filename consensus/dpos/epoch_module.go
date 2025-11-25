package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	epochmodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/epoch"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

func (d *DPoS) initEpochModule() {
	if d.epoch != nil {
		return
	}
	if d.epochManager == nil {
		d.logger.Warn("epoch manager backend not initialized")
		return
	}

	d.epoch = epochmodule.NewManager(d.epochManager, d.logger.Named("epoch_module"))
}

func (d *DPoS) initEpochLifecycleModule() {
	if d.epochLifecycle != nil {
		return
	}

	deps := d.buildEpochLifecycleDependencies()
	d.epochLifecycle = epochmodule.NewLifecycleManager(deps)
}

func (d *DPoS) buildEpochLifecycleDependencies() epochmodule.LifecycleDependencies {
	return epochmodule.LifecycleDependencies{
		Logger: d.logger.Named("epoch_lifecycle"),

		ResolveEpochNumber: func(blockNumber uint64) uint64 {
			if meta := d.getEpochForBlock(blockNumber); meta != nil {
				return meta.Number
			}
			return 0
		},
		LoadScheduledRecoveries: func(epochNumber uint64) []core.RecoveryProposalInfo {
			return convertParameterProposalsToCore(d.governanceLoadScheduled(epochNumber))
		},
		ClearValidatorFaultStatus: func(address types.Address, proposalID string) error {
			if d.state == nil || d.state.StakeStore == nil {
				return fmt.Errorf("stake store not available")
			}
			return d.state.StakeStore.ClearValidatorFaultStatus(address, proposalID)
		},
		ClearMemoryFaultStatus: func(address types.Address) {
			if d.faultyValidators != nil {
				delete(d.faultyValidators, address)
			}
		},
		ReloadValidatorsAfterRecovery: d.reloadValidatorsAfterRecovery,
		MarkProposalApplied:           d.governanceMarkProposalApplied,
		TriggerEpochSwitch: func(nextBlockNumber uint64) {
			if d.epochManager != nil {
				d.epochManager.TriggerEpochSwitch(nextBlockNumber)
			}
		},
		BuildPendingHeader:       d.buildPendingEpochHeader,
		SetPendingEpochEndHeader: d.SetPendingEpochEndHeader,
		ClearPendingEpochEndHeader: func(blockNumber uint64) {
			d.ClearPendingEpochEndHeader(blockNumber)
		},
		DetectFaults: func(blockNumber uint64) ([]core.FaultFlagInfo, error) {
			flags, err := d.detectValidatorFaults(blockNumber)
			if err != nil {
				return nil, err
			}
			return convertLocalFaultFlagsToCore(flags), nil
		},
		SaveFaultStatus: func(flag core.FaultFlagInfo) error {
			return d.saveFaultStatusToDatabase(convertCoreFaultFlagToLocal(flag))
		},
		UpdateMemoryFaultStatus: func(flag core.FaultFlagInfo) {
			d.updateMemoryFaultStatus(convertCoreFaultFlagToLocal(flag))
		},
		SaveCurrentEpoch: func(epochNumber uint64, blockNumber uint64) error {
			if d.state == nil || d.state.StakeStore == nil {
				return fmt.Errorf("stake store not available")
			}
			return d.state.StakeStore.SaveCurrentEpoch(d.currentEpoch)
		},
		UpdateBlockProducers: func(flags []core.FaultFlagInfo) error {
			return d.updateBlockProducersFromFaultFlags(convertCoreFaultFlagsToLocal(flags))
		},
		CalculateNextEpochValidators: d.calculateNextEpochValidators,
		SaveNextEpochValidators:      d.saveNextEpochValidators,
		UpdateValidatorCaches:        d.updateValidatorCachesFromModule,
	}
}

func (d *DPoS) buildPendingEpochHeader(parentHash types.Hash, nextBlockNumber uint64) *types.Header {
	if d.runtime == nil || d.runtime.config == nil || d.runtime.config.Key == nil {
		return nil
	}

	keyAddr := types.Address(d.runtime.config.Key.Address())
	return &types.Header{
		ParentHash: parentHash,
		Number:     nextBlockNumber,
		Miner:      keyAddr[:],
		Timestamp:  uint64(time.Now().Unix()),
	}
}

func convertParameterProposalsToCore(list []*ParameterProposal) []core.RecoveryProposalInfo {
	result := make([]core.RecoveryProposalInfo, 0, len(list))
	for _, prop := range list {
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

func convertLocalFaultFlagsToCore(flags []FaultFlagInfo) []core.FaultFlagInfo {
	result := make([]core.FaultFlagInfo, 0, len(flags))
	for _, flag := range flags {
		result = append(result, convertLocalFaultFlagToCore(flag))
	}
	return result
}

func convertCoreFaultFlagsToLocal(flags []core.FaultFlagInfo) []FaultFlagInfo {
	result := make([]FaultFlagInfo, 0, len(flags))
	for _, flag := range flags {
		result = append(result, convertCoreFaultFlagToLocal(flag))
	}
	return result
}

func convertLocalFaultFlagToCore(flag FaultFlagInfo) core.FaultFlagInfo {
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

func convertCoreFaultFlagToLocal(flag core.FaultFlagInfo) FaultFlagInfo {
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

func (d *DPoS) updateValidatorCachesFromModule(validators validator.AccountSet) {
	if validators == nil {
		return
	}

	d.delegates = validators.Copy()

	if d.runtime != nil {
		d.runtime.lock.Lock()
		d.runtime.delegates = validators.Copy()
		d.runtime.lock.Unlock()
	}
}
