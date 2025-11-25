package dpos

import (
	"math/big"

	validatormodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/validator"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// initValidatorModule 初始化验证者管理模块
func (d *DPoS) initValidatorModule() {
	if d.validator != nil {
		return
	}

	deps := d.buildValidatorModuleDependencies()
	d.validator = validatormodule.NewManager(deps)
}

func (d *DPoS) buildValidatorModuleDependencies() validatormodule.Dependencies {
	deps := validatormodule.Dependencies{
		Logger: d.logger.Named("modules.validator"),
	}

	deps.GetDelegates = func(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
		return d.GetDelegates(blockNumber, parents)
	}

	deps.GetCurrentValidators = func() validator.AccountSet {
		return d.GetValidators()
	}

	deps.UpdateValidatorsInMemory = func(set validator.AccountSet) {
		d.updateValidatorCachesFromModule(set)
	}

	deps.GetVotingPower = func(blockNumber uint64, validatorAddr types.Address) (*big.Int, error) {
		return d.GetVotingPower(blockNumber, validatorAddr)
	}

	return deps
}
