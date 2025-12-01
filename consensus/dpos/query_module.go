package dpos

import (
	querymodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/query"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// initQueryModule initializes the query manager using the shared module implementation.
func (d *DPoS) initQueryModule() {
	if d.query != nil {
		return
	}

	deps := d.buildQueryDependencies()
	d.query = querymodule.NewManager(deps)
}

// buildQueryDependencies prepares the dependency set for the query module.
func (d *DPoS) buildQueryDependencies() querymodule.Dependencies {
	deps := querymodule.Dependencies{
		ConsensusSwitchHeight: d.config.ConsensusSwitchHeight,
		EpochSize:             d.getEpochSize(),
		BlockTime:             d.config.BlockTime.Duration,
		Logger:                d.logger.Named("modules.query"),
	}

	deps.GetCurrentBlockNumber = func() uint64 {
		if d.config != nil && d.config.Blockchain != nil {
			if header := d.config.Blockchain.Header(); header != nil {
				return header.Number
			}
		}
		return 0
	}

	if d.epoch != nil {
		deps.EpochManager = d.epoch
	}

	deps.GetValidatorFaultInfo = func(address types.Address) map[string]interface{} {
		return d.getValidatorFaultInfo(address)
	}

	deps.GetSortedValidatorsWithLimit = func() ([]querymodule.ValidatorInfo, error) {
		validators, err := d.GetSortedValidatorsWithLimit()
		if err != nil || len(validators) == 0 {
			return convertValidatorSet(validators), err
		}
		return convertValidatorSet(validators), nil
	}

	deps.GetValidatorsForEpoch = func(epochNumber uint64) ([]querymodule.ValidatorInfo, error) {
		validators, err := d.getValidatorsForEpoch(epochNumber)
		if err != nil || len(validators) == 0 {
			return convertValidatorSet(validators), err
		}
		return convertValidatorSet(validators), nil
	}

	if d.blockTracker != nil {
		deps.GetEpochBlockCounts = func(epochNumber uint64) map[types.Address]uint64 {
			return d.blockTracker.GetEpochBlockCounts(epochNumber)
		}
		deps.GetTotalEpochBlocks = func(epochNumber uint64) uint64 {
			return d.blockTracker.GetTotalEpochBlocks(epochNumber)
		}
	}

	return deps
}

// convertValidatorSet transforms validator.AccountSet into the lightweight module representation.
func convertValidatorSet(set validator.AccountSet) []querymodule.ValidatorInfo {
	if len(set) == 0 {
		return []querymodule.ValidatorInfo{}
	}

	result := make([]querymodule.ValidatorInfo, 0, len(set))
	for _, v := range set {
		info := querymodule.ValidatorInfo{
			Address:     v.Address,
			VotingPower: v.VotingPower.String(),
			IsActive:    v.IsActive,
		}
		result = append(result, info)
	}

	return result
}
