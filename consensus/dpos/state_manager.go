package dpos

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// stateManager 状态管理器实现
type stateManager struct {
	state      *State
	stakeStore *StakeStore
	mutex      sync.RWMutex
}

// NewStateManager 创建新的状态管理器
func NewStateManager(state *State) StateManager {
	if state == nil {
		return nil
	}
	return &stateManager{
		state:      state,
		stakeStore: state.StakeStore,
	}
}

// GetValidators 获取验证者集合
func (sm *stateManager) GetValidators() (validator.AccountSet, error) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	
	if sm.stakeStore == nil {
		return nil, fmt.Errorf("stake store is nil")
	}
	
	return sm.stakeStore.GetValidatorsWithFilter(false)
}

// GetStakingInfo 获取质押信息
func (sm *stateManager) GetStakingInfo(address types.Address) (*StakeInfo, error) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	
	if sm.stakeStore == nil {
		return nil, fmt.Errorf("stake store is nil")
	}
	
	stakingInfo, err := sm.stakeStore.GetStakingInfo()
	if err != nil {
		return nil, err
	}
	
	// 查找指定地址的质押信息
	for _, stake := range stakingInfo {
		if stake.Delegate == address {
			return &StakeInfo{
				Staker:    stake.Staker,
				Amount:    stake.Amount,
				Delegate:  stake.Delegate,
				IsActive:  true,
			}, nil
		}
	}
	
	return nil, fmt.Errorf("staking info not found for address %s", address.String())
}

// GetVotingPower 获取投票权重
func (sm *stateManager) GetVotingPower(delegate types.Address) (*big.Int, error) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	
	if sm.stakeStore == nil {
		return big.NewInt(0), fmt.Errorf("stake store is nil")
	}
	
	validators, err := sm.stakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return big.NewInt(0), err
	}
	
	// 查找指定验证者的投票权重
	for _, validator := range validators {
		if validator.Address == delegate {
			return validator.VotingPower, nil
		}
	}
	
	return big.NewInt(0), nil
}

// SaveValidators 保存验证者集合
func (sm *stateManager) SaveValidators(validators validator.AccountSet) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	
	if sm.stakeStore == nil {
		return fmt.Errorf("stake store is nil")
	}
	
	// 通过stakeStore保存验证者
	// 这里需要调用stakeStore的保存方法
	return nil
}

// SaveStakingInfo 保存质押信息
func (sm *stateManager) SaveStakingInfo(info *StakeInfo) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	
	if sm.stakeStore == nil {
		return fmt.Errorf("stake store is nil")
	}
	
	// 通过stakeStore保存质押信息
	return nil
}

// UpdateVotingPower 更新投票权重
func (sm *stateManager) UpdateVotingPower(delegate types.Address, power *big.Int) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	
	if sm.stakeStore == nil {
		return fmt.Errorf("stake store is nil")
	}
	
	// 更新验证者的投票权重
	validators, err := sm.stakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return err
	}
	
	// 查找并更新
	for _, validator := range validators {
		if validator.Address == delegate {
			validator.VotingPower = power
			// 保存更新后的验证者集合
			return nil
		}
	}
	
	return fmt.Errorf("validator not found: %s", delegate.String())
}


