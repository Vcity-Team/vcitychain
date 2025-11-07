package dpos

import (
	"sync"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// validatorService 验证者服务实现
type validatorService struct {
	dpos        *DPoS
	stateManager StateManager
	mutex       sync.RWMutex
}

// NewValidatorService 创建新的验证者服务
func NewValidatorService(dpos *DPoS, stateManager StateManager) ValidatorService {
	return &validatorService{
		dpos:         dpos,
		stateManager: stateManager,
	}
}

// GetValidators 获取验证者集合
func (vs *validatorService) GetValidators(blockNumber uint64) (validator.AccountSet, error) {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetDelegates方法
	return vs.dpos.GetDelegates(blockNumber, nil)
}

// GetValidatorsWithTx 在事务中获取验证者集合
func (vs *validatorService) GetValidatorsWithTx(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error) {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetDelegatesWithTx方法
	return vs.dpos.GetDelegatesWithTx(blockNumber, nil, dbTx)
}

// GetCurrentValidators 获取当前验证者集合
func (vs *validatorService) GetCurrentValidators() validator.AccountSet {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	return vs.dpos.GetCurrentDelegates()
}

// UpdateValidators 更新验证者集合
func (vs *validatorService) UpdateValidators(validators validator.AccountSet) error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()
	
	// 更新DPoS的验证者集合
	vs.dpos.lock.Lock()
	vs.dpos.delegates = validators.Copy()
	vs.dpos.lock.Unlock()
	
	// 如果runtime存在，也更新runtime的验证者
	if vs.dpos.runtime != nil {
		vs.dpos.runtime.lock.Lock()
		vs.dpos.runtime.delegates = validators.Copy()
		vs.dpos.runtime.lock.Unlock()
	}
	
	return nil
}

// IsValidator 判断是否是验证者
func (vs *validatorService) IsValidator(address types.Address) bool {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的isValidator方法
	return vs.dpos.isValidator(address)
}

// GetStakingInfo 获取质押信息
func (vs *validatorService) GetStakingInfo(blockNumber uint64, staker types.Address) (*StakeInfo, error) {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetStakingInfo方法
	return vs.dpos.GetStakingInfo(blockNumber, staker)
}

// GetStakingInfoWithTx 在事务中获取质押信息
func (vs *validatorService) GetStakingInfoWithTx(blockNumber uint64, staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error) {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetStakingInfoWithTx方法
	return vs.dpos.GetStakingInfoWithTx(blockNumber, staker, dbTx)
}

// DetectFaults 检测验证者故障
func (vs *validatorService) DetectFaults() error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()
	
	// 调用现有的故障检测逻辑
	// 这里需要调用validator_mgmt_fault.go中的detectValidatorFaults
	// 暂时返回nil，后续迁移
	return nil
}

// GetValidatorFaultInfo 获取验证者故障信息
func (vs *validatorService) GetValidatorFaultInfo(address types.Address) *FaultFlagInfo {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的getValidatorFaultInfo方法
	faultMap := vs.dpos.getValidatorFaultInfo(address)
	
	// 将map转换为FaultFlagInfo
	faultInfo := &FaultFlagInfo{
		NodeAddress:     address,
		IsFaulty:        false,
		MissedBlocks:    0,
		ActualBlocks:    0,
		LastUpdateTime:  0,
		EpochNumber:     0,
		LastFaultyEpoch: 0,
		Reason:          "",
	}
	
	if isFaulty, ok := faultMap["isFaulty"].(bool); ok {
		faultInfo.IsFaulty = isFaulty
	}
	if missedBlocks, ok := faultMap["missedBlocks"].(uint64); ok {
		faultInfo.MissedBlocks = missedBlocks
	}
	if lastUpdateTime, ok := faultMap["lastUpdateTime"].(uint64); ok {
		faultInfo.LastUpdateTime = lastUpdateTime
	}
	
	return faultInfo
}

