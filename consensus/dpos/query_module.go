package dpos

import (
	"fmt"

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

	// 注入GetHeaderByNumber依赖
	deps.GetHeaderByNumber = func(blockNumber uint64) (*types.Header, bool) {
		if d.config != nil && d.config.Blockchain != nil {
			return d.config.Blockchain.GetHeaderByNumber(blockNumber)
		}
		return nil, false
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

	// 注入GetValidatorsFromEpochStartBlock依赖
	deps.GetValidatorsFromEpochStartBlock = func(epochNumber uint64) ([]querymodule.ValidatorInfo, error) {
		// 计算epoch开始区块号
		consensusSwitchHeight := d.config.ConsensusSwitchHeight
		epochSize := d.getEpochSize()

		var epochStartBlock uint64
		if epochNumber == 0 {
			epochStartBlock = 0
		} else {
			epochStartBlock = consensusSwitchHeight + (epochNumber-1)*epochSize
		}

		// 获取epoch开始区块的区块头
		if d.config == nil || d.config.Blockchain == nil {
			return nil, fmt.Errorf("blockchain not available to get validators for epoch %d", epochNumber)
		}

		header, exists := d.config.Blockchain.GetHeaderByNumber(epochStartBlock)
		if !exists || header == nil {
			return nil, fmt.Errorf("epoch %d start block %d not found", epochNumber, epochStartBlock)
		}

		// 从ExtraData读取验证者集合
		extra, err := GetDposExtra(header.ExtraData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ExtraData for epoch %d start block %d: %w", epochNumber, epochStartBlock, err)
		}

		// 检查ExtraData中是否有验证者集合
		if extra.Validators == nil || len(extra.Validators.Added) == 0 {
			return nil, fmt.Errorf("validators set is missing in ExtraData for epoch %d start block %d", epochNumber, epochStartBlock)
		}

		// 转换为ValidatorInfo格式
		validators := extra.Validators.Added
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
