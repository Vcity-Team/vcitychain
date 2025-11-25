package state

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies 定义了状态管理模块运行所需的外部能力
type Dependencies struct {
	Logger hclog.Logger

	// FetchAccount 从底层状态存储中查询账户信息
	FetchAccount func(address types.Address) (*core.AccountInfo, error)

	// PersistValidators 将指定区块的验证者集合持久化
	PersistValidators func(blockNumber uint64, validators validator.AccountSet) error

	// LoadValidators 获取验证者集合
	LoadValidators func(filterZeroVotingPower bool) (validator.AccountSet, error)
}

// Manager 是 core.StateManager 的模块化实现
type Manager struct {
	deps Dependencies
}

// NewManager 构建状态管理器
func NewManager(deps Dependencies) core.StateManager {
	if deps.Logger == nil {
		deps.Logger = hclog.NewNullLogger()
	}
	return &Manager{
		deps: deps,
	}
}

// GetAccount 返回账户信息
func (m *Manager) GetAccount(address types.Address) (*core.AccountInfo, error) {
	if m.deps.FetchAccount == nil {
		return nil, fmt.Errorf("fetch account dependency not configured")
	}
	info, err := m.deps.FetchAccount(address)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return &core.AccountInfo{
			Address:  address,
			Balance:  big.NewInt(0),
			Nonce:    0,
			CodeHash: types.ZeroHash,
		}, nil
	}
	return info, nil
}

// GetBalance 返回账户余额
func (m *Manager) GetBalance(address types.Address) (*big.Int, error) {
	account, err := m.GetAccount(address)
	if err != nil {
		return nil, err
	}
	if account.Balance == nil {
		return big.NewInt(0), nil
	}
	return account.Balance, nil
}

// SaveValidators 持久化验证者集合
func (m *Manager) SaveValidators(blockNumber uint64, validators validator.AccountSet) error {
	if m.deps.PersistValidators == nil {
		return fmt.Errorf("persist validators dependency not configured")
	}
	return m.deps.PersistValidators(blockNumber, validators)
}

// GetValidators 返回验证者集合
func (m *Manager) GetValidators(filterZeroVotingPower bool) (validator.AccountSet, error) {
	if m.deps.LoadValidators == nil {
		return nil, fmt.Errorf("load validators dependency not configured")
	}
	return m.deps.LoadValidators(filterZeroVotingPower)
}
