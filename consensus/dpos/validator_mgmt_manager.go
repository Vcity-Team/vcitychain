package dpos

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"go.etcd.io/bbolt"
)

// GetDelegates 获取指定区块的受托人集合
// 统一从区块的 ExtraData 中解析验证者集合
func (d *DPoS) GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	return d.getDelegatesInternal(blockNumber, parents)
}

// getDelegatesInternal 内部实现，不加锁（避免递归调用时死锁）
func (d *DPoS) getDelegatesInternal(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	// 获取指定区块的 header
	var header *types.Header
	var exists bool

	// 先从 parents 中查找
	if len(parents) > 0 {
		for _, p := range parents {
			if p.Number == blockNumber {
				header = p
				exists = true
				break
			}
		}
	}

	// 如果 parents 中没有，从区块链获取
	if !exists {
		header, exists = d.blockchain.GetHeaderByNumber(blockNumber)
		if !exists {
			return nil, fmt.Errorf("block %d not found", blockNumber)
		}
	}

	// 解析 ExtraData
	extra, err := GetIbftExtra(header.ExtraData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ExtraData for block %d: %w", blockNumber, err)
	}

	// 如果 ExtraData 中有验证者信息，直接使用
	if extra.Validators != nil && !extra.Validators.IsEmpty() && len(extra.Validators.Added) > 0 {
		validatorAddresses := extra.Validators.Added
		productionValidators := make(validator.AccountSet, 0, len(validatorAddresses))

		for _, validatorAddr := range validatorAddresses {
			// 从创世文件获取BLS公钥
			blsKey, err := extra.getBLSKeyFromGenesis(validatorAddr.Address, d.logger)
			if err != nil {
				// BLS公钥获取失败，继续处理
			}

			// 构建完整的验证者信息
			productionValidators = append(productionValidators, &validator.ValidatorMetadata{
				Address:     validatorAddr.Address,
				BlsKey:      blsKey,
				VotingPower: validatorAddr.VotingPower,
				IsActive:    validatorAddr.IsActive,
			})
		}

		return productionValidators, nil
	}

	// 如果没有验证者信息，需要获取父区块的验证者集合
	var parentValidators validator.AccountSet
	if blockNumber > 0 {
		// 递归获取父区块的验证者集合（通过 parents 参数避免重复获取 header）
		var err error
		parentValidators, err = d.getDelegatesInternal(blockNumber-1, parents)
		if err != nil {
			// 如果获取父区块验证者失败，尝试从创世获取
			genesisValidators, err2 := extra.getGenesisValidators(d, d.logger)
			if err2 != nil {
				return nil, fmt.Errorf("failed to get parent validators and genesis validators: %w, %w", err, err2)
			}
			parentValidators = genesisValidators
		}
	} else {
		// 创世区块，从创世文件获取
		genesisValidators, err := extra.getGenesisValidators(d, d.logger)
		if err != nil {
			return nil, fmt.Errorf("failed to get genesis validators: %w", err)
		}
		return genesisValidators, nil
	}

	// 如果没有验证者集合变化，直接返回父区块的验证者集合
	if extra.Validators == nil || extra.Validators.IsEmpty() {
		return parentValidators, nil
	}

	// 应用验证者集合变化
	currentValidators := extra.applyValidatorSetDelta(parentValidators, extra.Validators, d.logger)
	return currentValidators, nil
}

// GetDelegatesWithTx 在数据库事务中获取受托人集合
func (d *DPoS) GetDelegatesWithTx(blockNumber uint64, parents []*types.Header, dbTx *bbolt.Tx) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 在事务中获取受托人集合
	return d.getDelegatesFromStateWithTx(blockNumber, dbTx)
}

// GetCurrentDelegates 获取当前内存中的受托人集合
func (d *DPoS) GetCurrentDelegates() validator.AccountSet {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 返回当前内存中的受托人集合的副本
	return d.delegates.Copy()
}

// GetValidatorsWithFilter 返回验证者集合（可选过滤）
func (d *DPoS) GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// Access the stake store directly through d.state.StakeStore
	if d.state == nil || d.state.StakeStore == nil {
		return nil, fmt.Errorf("DPoS state or stake store is nil")
	}

	// Get validators from stake store with filtering control
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(filterZeroVotingPower)
	if err != nil {
		return nil, fmt.Errorf("failed to get validators from stake store: %w", err)
	}

	return validators, nil
}

// GetSortedValidatorsWithLimit 获取排序后的验证者集合（带限制）
func (d *DPoS) GetSortedValidatorsWithLimit() (validator.AccountSet, error) {
	if d.state == nil || d.state.StakeStore == nil {
		return nil, fmt.Errorf("stake store not available")
	}

	// 从数据库读取所有验证者
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return nil, fmt.Errorf("failed to get validators from database: %w", err)
	}

	if len(validators) == 0 {
		return validator.AccountSet{}, nil
	}

	// 按权重倒序排序
	sort.Slice(validators, func(i, j int) bool {
		votingPowerCmp := validators[i].VotingPower.Cmp(validators[j].VotingPower)
		if votingPowerCmp != 0 {
			return votingPowerCmp > 0
		}
		// 权重相同时，按地址升序排序（确保排序稳定）
		return bytes.Compare(validators[i].Address[:], validators[j].Address[:]) < 0
	})

	// 应用限制（如果配置了）
	maxValidators := int(d.config.DPoSValidatorsCount)
	if maxValidators > 0 && len(validators) > maxValidators {
		validators = validators[:maxValidators]
	}

	return validators, nil
}

// isValidator 检查地址是否是验证者
func (d *DPoS) isValidator(address types.Address) bool {
	if d.state != nil && d.state.StakeStore != nil {
		validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		if err == nil {
			for _, validator := range validators {
				if validator.Address == address {
					d.logger.Debug("✅ 验证者身份确认（数据库）", "address", address.String())
					return true
				}
			}
		}
	}

	// 检查内存中的验证者
	for _, delegate := range d.delegates {
		if delegate.Address == address {
			d.logger.Debug("✅ 验证者身份确认（内存）", "address", address.String())
			return true
		}
	}

	d.logger.Debug("❌ 不是验证者", "address", address.String())
	return false
}
