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
func (d *DPoS) GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	currentBlockNumber := d.blockchain.CurrentHeader().Number
	d.logger.Debug("🔍 验证时获取验证者集合",
		"requestedBlockNumber", blockNumber,
		"currentBlockNumber", currentBlockNumber,
		"isCurrentBlock", blockNumber == currentBlockNumber,
		"note", "验证时获取验证者集合")

	// 如果是当前区块，优先返回从runtime.delegates获取的验证者
	if blockNumber == currentBlockNumber {
		d.logger.Debug("🔍 Returning current delegates from memory")

		// 🆕 优先使用 runtime.delegates，如果为空则从数据库读取
		if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
			d.logger.Debug("🔍 使用从runtime.delegates获取的验证者")
			result := d.runtime.delegates.Copy()
			return result, nil
		}

		// 🆕 如果runtime.delegates为空，尝试从数据库读取
		if d.state != nil && d.state.StakeStore != nil {
			d.logger.Info("🔍 runtime.delegates为空，尝试从数据库读取验证者")
			if dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false); err == nil && len(dbValidators) > 0 {
				d.logger.Info("🔍 从数据库成功读取验证者", "count", len(dbValidators))

				// 🆕 添加详细日志：打印从数据库读取的验证者信息
				d.logger.Info("🔍 数据库验证者详细信息:")
				for i, validator := range dbValidators {
					// 🆕 获取验证者的故障标志信息
					faultInfo := d.getValidatorFaultInfo(validator.Address)

					d.logger.Info("🔍 数据库验证者",
						"index", i,
						"address", validator.Address.String(),
						"votingPower", validator.VotingPower.String(),
						"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
						"isActive", validator.IsActive,
						"hasBlsKey", validator.BlsKey != nil,
						"faultFlag", faultInfo) // 🆕 添加故障标志信息
				}

				return dbValidators, nil
			}
		}

		return validator.AccountSet{}, nil
	}

	// 🆕 已移除数据库读取机制，改为从区块 ExtraData 直接解析
	d.logger.Info("📝 GetDelegates 获取方式已更新",
		"requestedBlockNumber", blockNumber,
		"currentBlockNumber", currentBlockNumber,
		"method", "从区块ExtraData直接解析",
		"note", "不再从数据库读取，验证者集合应从区块数据直接获取")

	// 对于历史区块，应该通过区块的 ExtraData 来获取验证者集合
	// 这里返回错误，提示调用者应该使用 ExtraData 解析方式
	return nil, fmt.Errorf("GetDelegates for historical block %d is deprecated, use ExtraData parsing instead", blockNumber)
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

