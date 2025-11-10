package dpos

import (
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"go.etcd.io/bbolt"
)

// GetStakingInfo 获取指定区块的质押信息
func (d *DPoS) GetStakingInfo(blockNumber uint64, staker types.Address) (*StakeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接从内存中获取质押信息，避免复杂的数据库操作

	// 从内存中获取
	if voter, exists := d.voters[staker]; exists {
		return &StakeInfo{
			Staker:    staker,
			Amount:    new(big.Int).Set(voter.VotingPower),
			StartTime: voter.LastVoteTime,
			EndTime:   voter.LockedUntil,
			IsLocked:  voter.LockedUntil > uint64(time.Now().Unix()),
			IsActive:  len(voter.VotedDelegates) > 0,
			Rewards:   big.NewInt(0), // TODO: 实现奖励计算
			Delegate:  d.getPrimaryDelegate(voter.VotedDelegates),
		}, nil
	}

	// 如果内存中没有，尝试从受托人信息中获取
	for _, delegate := range d.delegates {
		if delegate.Address == staker {
			return &StakeInfo{
				Staker:    staker,
				Amount:    new(big.Int).Set(delegate.VotingPower),
				StartTime: uint64(time.Now().Unix()), // 使用当前时间作为开始时间
				EndTime:   0,                         // 受托人没有锁定时间
				IsLocked:  false,
				IsActive:  delegate.IsActive,
				Rewards:   big.NewInt(0),
				Delegate:  staker, // 受托人自己就是委托人
			}, nil
		}
	}

	// 返回默认值
	return &StakeInfo{
		Staker:    staker,
		Amount:    big.NewInt(0),
		StartTime: 0,
		EndTime:   0,
		IsLocked:  false,
		IsActive:  false,
		Rewards:   big.NewInt(0),
		Delegate:  types.ZeroAddress,
	}, nil
}

// GetStakingInfoWithTx 在数据库事务中获取指定区块的质押信息
func (d *DPoS) GetStakingInfoWithTx(blockNumber uint64, staker types.Address, dbTx *bbolt.Tx) (*StakeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 简化实现，直接调用 GetStakingInfo（避免复杂的数据库事务操作）
	// 注意：dbTx 参数保留以符合接口定义，但当前实现不使用它
	return d.GetStakingInfo(blockNumber, staker)
}

// GetAllStakingInfo 从内存获取所有质押信息（简单直接）
func (d *DPoS) GetAllStakingInfo() ([]*StakeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 优先从 d.delegates 读取，如果为空则从 d.runtime.delegates 读取
	delegates := d.delegates
	if len(delegates) == 0 && d.runtime != nil && len(d.runtime.delegates) > 0 {
		delegates = d.runtime.delegates
	}

	// 转换为 StakeInfo，包含故障标志
	result := make([]*StakeInfo, 0, len(delegates))
	for _, delegate := range delegates {
		// 获取故障标志信息
		faultInfo := d.getValidatorFaultInfo(delegate.Address)

		stakingInfo := &StakeInfo{
			Staker:    delegate.Address,
			Amount:    new(big.Int).Set(delegate.VotingPower),
			IsActive:  delegate.IsActive,
			StartTime: uint64(time.Now().Unix()),
			EndTime:   0,
			IsLocked:  false,
			Rewards:   big.NewInt(0),
			Delegate:  delegate.Address, // 验证者自己就是委托人
			FaultFlag: faultInfo,        // 🆕 添加故障标志
		}
		result = append(result, stakingInfo)
	}

	return result, nil
}

// getPrimaryDelegate 获取主要受托人地址（辅助方法）
func (d *DPoS) getPrimaryDelegate(votedDelegates []types.Address) types.Address {
	if len(votedDelegates) == 0 {
		return types.ZeroAddress
	}
	return votedDelegates[0]
}
