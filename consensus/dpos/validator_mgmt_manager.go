package dpos

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"go.etcd.io/bbolt"
)

// GetDelegates 获取指定区块的受托人集合（dposBackend接口方法）
// 内部实现通过ValidatorManager接口调用
func (d *DPoS) GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	currentBlockNumber := d.blockchain.CurrentHeader().Number

	// 如果是当前区块，优先使用runtime.delegates
	if blockNumber == currentBlockNumber {
		// 注意：不能通过接口调用，因为GetDelegates本身就是接口的实现
		// 直接使用原有逻辑，避免无限递归
		if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
			result := d.runtime.delegates.Copy()
			return result, nil
		}

		// 如果runtime.delegates为空，尝试从数据库读取
		if d.state != nil && d.state.StakeStore != nil {
			d.logger.Info("🔍 runtime.delegates为空，尝试从数据库读取验证者")
			if dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false); err == nil && len(dbValidators) > 0 {
				d.logger.Info("🔍 从数据库成功读取验证者", "count", len(dbValidators))

				// 添加详细日志：打印从数据库读取的验证者信息
				d.logger.Info("🔍 数据库验证者详细信息:")
				for i, validator := range dbValidators {
					// 获取验证者的故障标志信息
					faultInfo := d.getValidatorFaultInfo(validator.Address)

					d.logger.Info("🔍 数据库验证者",
						"index", i,
						"address", validator.Address.String(),
						"votingPower", validator.VotingPower.String(),
						"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
						"isActive", validator.IsActive,
						"hasBlsKey", validator.BlsKey != nil,
						"faultFlag", faultInfo) // 添加故障标志信息
				}

				return dbValidators, nil
			}
		}

		return validator.AccountSet{}, nil
	}

	// 已移除数据库读取机制，改为从区块 ExtraData 直接解析
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
	configSource := "DPoSValidatorsCount"
	if maxValidators == 0 {
		// 如果 DPoSValidatorsCount 为0，回退到 DelegateCount
		maxValidators = int(d.config.DelegateCount)
		configSource = "DelegateCount"
	}

	// 添加调试日志，确保配置正确读取（使用限频日志避免刷屏）
	if maxValidators > 0 && len(validators) > maxValidators {
		d.logOnceWithInterval("get_sorted_validators_truncate", 10*time.Second, "info",
			"🔍 [GetSortedValidatorsWithLimit] 截取验证者",
			"originalCount", len(validators),
			"maxValidators", maxValidators,
			"configSource", configSource,
			"DPoSValidatorsCount", d.config.DPoSValidatorsCount,
			"DelegateCount", d.config.DelegateCount)
		validators = validators[:maxValidators]
	} else if maxValidators == 0 {
		d.logOnceWithInterval("get_sorted_validators_zero_config", 10*time.Second, "warn",
			"⚠️ [GetSortedValidatorsWithLimit] 配置为0，不截取验证者",
			"originalCount", len(validators),
			"DPoSValidatorsCount", d.config.DPoSValidatorsCount,
			"DelegateCount", d.config.DelegateCount)
	} else if len(validators) <= maxValidators {
		d.logOnceWithInterval("get_sorted_validators_no_truncate", 30*time.Second, "debug",
			"✅ [GetSortedValidatorsWithLimit] 验证者数量未超过限制，无需截取",
			"originalCount", len(validators),
			"maxValidators", maxValidators,
			"configSource", configSource)
	}

	return validators, nil
}

// GetSortedValidatorsWithLimitFilterFaulty 获取排序后的验证者集合（带限制，并过滤故障验证者）
// 用于出块相关逻辑，确保故障节点不会参与出块
func (d *DPoS) GetSortedValidatorsWithLimitFilterFaulty() (validator.AccountSet, error) {
	// 先获取排序和截取后的验证者集合
	allValidators, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		return nil, err
	}

	if len(allValidators) == 0 {
		return validator.AccountSet{}, nil
	}

	// 过滤掉故障验证者
	activeValidators := make(validator.AccountSet, 0, len(allValidators))
	faultyCount := 0

	for _, v := range allValidators {
		// 检查验证者的故障状态
		faultInfo := d.getValidatorFaultInfo(v.Address)
		isFaulty := false
		if faultInfo != nil && faultInfo["isFaulty"] != nil {
			if faultValue, ok := faultInfo["isFaulty"].(bool); ok {
				isFaulty = faultValue
			}
		}

		if isFaulty {
			faultyCount++
			d.logger.Debug("🚫 [GetSortedValidatorsWithLimitFilterFaulty] 过滤掉故障验证者",
				"address", v.Address.String(),
				"votingPower", v.VotingPower.String())
		} else {
			activeValidators = append(activeValidators, v)
		}
	}

	if faultyCount > 0 {
		d.logger.Debug("✅ [GetSortedValidatorsWithLimitFilterFaulty] 故障验证者过滤完成",
			"totalValidators", len(allValidators),
			"faultyValidators", faultyCount,
			"activeValidators", len(activeValidators))
	}

	// 注意：过滤后保持原有排序（权重倒序，地址升序），因为已经排序过了
	return activeValidators, nil
}

// getValidatorsFromCurrentBlockExtraData 从当前区块的 ExtraData 读取验证者集合
func (d *DPoS) getValidatorsFromCurrentBlockExtraData(header *types.Header) (validator.AccountSet, error) {
	if header == nil {
		return nil, fmt.Errorf("header is nil")
	}

	extra := &Extra{}
	if err := extra.UnmarshalRLP(header.ExtraData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal extra data: %w", err)
	}

	// 从 Validators 读取
	if extra.Validators != nil && !extra.Validators.IsEmpty() && len(extra.Validators.Added) > 0 {
		validators := make(validator.AccountSet, 0, len(extra.Validators.Added))
		for _, v := range extra.Validators.Added {
			validators = append(validators, &validator.ValidatorMetadata{
				Address:     v.Address,
				VotingPower: v.VotingPower,
				IsActive:    v.IsActive,
			})
		}
		return validators, nil
	}

	return nil, fmt.Errorf("no validators found in ExtraData")
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
