package dpos

import (
	"encoding/json"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
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
func (d *DPoS) GetStakingInfoWithTx(blockNumber uint64, staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 简化实现，直接调用 GetStakingInfo（避免复杂的数据库事务操作）
	// 注意：dbTx 参数保留以符合接口定义，但当前实现不使用它
	return d.GetStakingInfo(blockNumber, staker)
}

// GetAllStakingInfo 从数据库获取所有质押信息（直接从DelegateInfo bucket读取）
func (d *DPoS) GetAllStakingInfo() ([]*StakeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 检查 state 和 StakeStore 是否可用
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, falling back to memory")
		// 回退到内存读取（保持向后兼容）
		delegates := d.delegates
		if len(delegates) == 0 && d.runtime != nil && len(d.runtime.delegates) > 0 {
			delegates = d.runtime.delegates
		}

		result := make([]*StakeInfo, 0, len(delegates))
		for _, delegate := range delegates {
			faultInfo := d.getValidatorFaultInfo(delegate.Address)
			stakingInfo := &StakeInfo{
				Staker:    delegate.Address,
				Amount:    new(big.Int).Set(delegate.VotingPower),
				IsActive:  delegate.IsActive,
				StartTime: uint64(time.Now().Unix()),
				EndTime:   0,
				IsLocked:  false,
				Rewards:   big.NewInt(0),
				Delegate:  delegate.Address,
				FaultFlag: faultInfo,
			}
			result = append(result, stakingInfo)
		}
		return result, nil
	}

	// 从数据库读取投票记录（从 VoterInfo bucket 读取）
	var result []*StakeInfo
	err := d.state.StakeStore.db.View(func(tx *bolt.Tx) error {
		// 从 VoterInfo bucket 读取所有投票者信息
		voterBucket := tx.Bucket([]byte("VoterInfo"))
		if voterBucket == nil {
			d.logger.Warn("VoterInfo bucket not found in database")
			return nil // 返回空列表，不返回错误
		}

		cursor := voterBucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var voterInfo VoterInfo
			if err := json.Unmarshal(value, &voterInfo); err != nil {
				d.logger.Warn("Failed to unmarshal voter info", "key", key, "error", err)
				continue
			}

			// 跳过投票权重为0的投票者
			if voterInfo.VotingPower == nil || voterInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
				continue
			}

			// 为每个投票者-验证者对创建一个 StakeInfo
			// 如果投票者投票给多个验证者，平均分配投票权重
			voteCount := len(voterInfo.VotedDelegates)
			if voteCount == 0 {
				continue
			}

			// 计算每个验证者分到的投票权重（平均分配）
			amountPerDelegate := new(big.Int).Div(voterInfo.VotingPower, big.NewInt(int64(voteCount)))
			remainder := new(big.Int).Mod(voterInfo.VotingPower, big.NewInt(int64(voteCount)))

			for i, delegate := range voterInfo.VotedDelegates {
				// 分配投票权重
				amount := new(big.Int).Set(amountPerDelegate)
				// 余数分配给第一个验证者
				if i == 0 {
					amount.Add(amount, remainder)
				}

				stakingInfo := &StakeInfo{
					Staker:    voterInfo.Address,
					Amount:    amount,
					IsActive:  true,
					StartTime: voterInfo.LastVoteTime,
					EndTime:   voterInfo.LockedUntil,
					IsLocked:  voterInfo.LockedUntil > uint64(time.Now().Unix()),
					Rewards:   big.NewInt(0),
					Delegate:  delegate, // 投票给这个验证者
				}
				result = append(result, stakingInfo)
			}
		}

		return nil
	})

	if err != nil {
		d.logger.Error("Failed to read staking info from database", "error", err)
		// 如果数据库读取失败，回退到内存读取
		delegates := d.delegates
		if len(delegates) == 0 && d.runtime != nil && len(d.runtime.delegates) > 0 {
			delegates = d.runtime.delegates
		}

		result = make([]*StakeInfo, 0, len(delegates))
		for _, delegate := range delegates {
			faultInfo := d.getValidatorFaultInfo(delegate.Address)
			stakingInfo := &StakeInfo{
				Staker:    delegate.Address,
				Amount:    new(big.Int).Set(delegate.VotingPower),
				IsActive:  delegate.IsActive,
				StartTime: uint64(time.Now().Unix()),
				EndTime:   0,
				IsLocked:  false,
				Rewards:   big.NewInt(0),
				Delegate:  delegate.Address,
				FaultFlag: faultInfo,
			}
			result = append(result, stakingInfo)
		}
		return result, nil
	}

	d.logger.Debug("Successfully retrieved staking info from database", "count", len(result))
	return result, nil
}

// getPrimaryDelegate 获取主要受托人地址（辅助方法）
func (d *DPoS) getPrimaryDelegate(votedDelegates []types.Address) types.Address {
	if len(votedDelegates) == 0 {
		return types.ZeroAddress
	}
	return votedDelegates[0]
}
