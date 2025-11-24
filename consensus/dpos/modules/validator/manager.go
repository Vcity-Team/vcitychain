package validator

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies 验证者管理器依赖
type Dependencies struct {
	// 获取验证者集合
	GetDelegates func(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error)
	
	// 获取当前验证者集合
	GetCurrentValidators func() validator.AccountSet
	
	// 更新验证者集合
	UpdateValidatorsInMemory func(validators validator.AccountSet)
	
	// 获取投票权重
	GetVotingPower func(blockNumber uint64, validatorAddr types.Address) (*big.Int, error)
	
	Logger hclog.Logger
}

// Manager 验证者管理器实现
type Manager struct {
	deps Dependencies
}

// NewManager 创建验证者管理器
func NewManager(deps Dependencies) core.ValidatorManager {
	return &Manager{
		deps: deps,
	}
}

// GetValidators 获取指定区块的验证者集合
func (m *Manager) GetValidators(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	if m.deps.GetDelegates == nil {
		return nil, nil
	}
	return m.deps.GetDelegates(blockNumber, parents)
}

// GetCurrentValidators 获取当前验证者集合
func (m *Manager) GetCurrentValidators() validator.AccountSet {
	if m.deps.GetCurrentValidators == nil {
		return validator.AccountSet{}
	}
	return m.deps.GetCurrentValidators()
}

// UpdateValidators 更新验证者集合
func (m *Manager) UpdateValidators(blockNumber uint64, validators validator.AccountSet) error {
	if m.deps.UpdateValidatorsInMemory != nil {
		m.deps.UpdateValidatorsInMemory(validators)
	}
	return nil
}

// IsValidator 判断指定地址是否是验证者
func (m *Manager) IsValidator(address types.Address, blockNumber uint64) bool {
	validators, err := m.GetValidators(blockNumber, nil)
	if err != nil {
		return false
	}
	return validators.ContainsAddress(address)
}

// GetVotingPower 获取指定验证者的投票权重
func (m *Manager) GetVotingPower(blockNumber uint64, validatorAddr types.Address) (*big.Int, error) {
	if m.deps.GetVotingPower == nil {
		return big.NewInt(0), nil
	}
	return m.deps.GetVotingPower(blockNumber, validatorAddr)
}

