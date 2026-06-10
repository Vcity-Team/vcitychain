package dpos

import (
	"math/big"
	"strconv"

	"github.com/Vcity-Team/vcitychain/types"
)

// getEffectiveVoteLockActivationEpoch 返回投票余额锁定激活 epoch；0 表示未激活。
func (d *DPoS) getEffectiveVoteLockActivationEpoch() uint64 {
	if v, err := d.getCurrentParameterValue("dpos_vote_lock_activation_epoch"); err == nil {
		switch t := v.(type) {
		case uint64:
			return t
		case int:
			if t >= 0 {
				return uint64(t)
			}
		case int64:
			if t >= 0 {
				return uint64(t)
			}
		case float64:
			if t >= 0 {
				return uint64(t)
			}
		case string:
			if u, err := strconv.ParseUint(t, 10, 64); err == nil {
				return u
			}
		}
	}
	if d.config != nil {
		return d.config.VoteLockActivationEpoch
	}
	return 0
}

// IsVoteLockActive 判断当前 epoch 是否启用投票余额锁定（账内锁定，非 escrow）。
func (d *DPoS) IsVoteLockActive() bool {
	return d.isVoteLockActive()
}

// isVoteLockActive 判断当前 epoch 是否启用投票余额锁定（账内锁定，非 escrow）。
func (d *DPoS) isVoteLockActive() bool {
	activation := d.getEffectiveVoteLockActivationEpoch()
	if activation == 0 {
		return false
	}
	epoch := d.GetCurrentEpochNumber()
	return epoch >= activation
}

// isVoteLockActiveAtEpoch 判断指定 epoch 是否启用投票余额锁定。
func (d *DPoS) isVoteLockActiveAtEpoch(epochNumber uint64) bool {
	activation := d.getEffectiveVoteLockActivationEpoch()
	if activation == 0 {
		return false
	}
	return epochNumber >= activation
}

// StakeCountsTowardLocked 判断质押记录是否计入锁定余额。
func StakeCountsTowardLocked(stake *StakeInfo, voteLockActive bool) bool {
	if stake == nil || !stake.IsActive {
		return false
	}
	if stake.Amount == nil || stake.Amount.Sign() <= 0 {
		return false
	}
	if stake.PendingUnvote {
		return true
	}
	if voteLockActive {
		return true
	}
	return stake.Applied
}

// sumLockedVoteWeiFromStakes 从 StakeInfo 列表汇总某投票者的锁定金额。
func sumLockedVoteWeiFromStakes(infos []*StakeInfo, voter types.Address, voteLockActive bool) *big.Int {
	locked := big.NewInt(0)
	for _, s := range infos {
		if s == nil || s.Staker != voter {
			continue
		}
		if !StakeCountsTowardLocked(s, voteLockActive) {
			continue
		}
		locked.Add(locked, s.Amount)
	}
	return locked
}

// ComputeLockedVoteWei 返回投票者在当前规则下不可转出的原生余额（wei）。
func (d *DPoS) ComputeLockedVoteWei(voter types.Address) *big.Int {
	if d.state == nil || d.state.StakeStore == nil {
		return big.NewInt(0)
	}
	infos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return big.NewInt(0)
	}
	return sumLockedVoteWeiFromStakes(infos, voter, d.isVoteLockActive())
}

// GetLockedVoteWeiForAddress 供 server/txpool 查询锁定余额。
func (d *DPoS) GetLockedVoteWeiForAddress(voter types.Address) *big.Int {
	return d.ComputeLockedVoteWei(voter)
}
