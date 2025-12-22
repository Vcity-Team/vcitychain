package evm

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGethEVMAdapter_CanRun(t *testing.T) {
	adapter := NewGethEVMAdapter(1)
	contract := newMockContract(big.NewInt(0), 100000, []byte{})
	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
		storage:  make(map[types.Address]map[types.Hash]types.Hash),
		codes:    make(map[types.Address][]byte),
		nonces:   make(map[types.Address]uint64),
	}
	config := &chain.ForksInTime{
		Homestead: true,
		EIP150:    true,
		EIP155:    true,
		EIP158:    true,
		Byzantium: true,
		London:    true,
	}

	result := adapter.CanRun(contract, host, config)
	assert.True(t, result)
}

func TestGethEVMAdapter_Name(t *testing.T) {
	adapter := NewGethEVMAdapter(1)
	assert.Equal(t, "geth_evm", adapter.Name())
}

func TestGethEVMAdapter_Run_SimpleContract(t *testing.T) {
	adapter := NewGethEVMAdapter(1)

	// 简单的 STOP 合约
	code := []byte{0x00} // STOP opcode

	contract := runtime.NewContractCall(
		1,
		types.ZeroAddress,
		types.ZeroAddress,
		types.ZeroAddress,
		big.NewInt(0),
		100000,
		code,
		[]byte{},
	)

	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
		storage:  make(map[types.Address]map[types.Hash]types.Hash),
		codes:    make(map[types.Address][]byte),
		nonces:   make(map[types.Address]uint64),
	}

	// 设置初始余额
	host.balances[types.ZeroAddress] = big.NewInt(1000000)

	config := &chain.ForksInTime{
		Homestead: true,
		EIP150:    true,
		EIP155:    true,
		EIP158:    true,
		Byzantium: true,
		London:    true,
	}

	result := adapter.Run(contract, host, config)

	require.NotNil(t, result)
	assert.NoError(t, result.Err)
	assert.True(t, result.GasLeft > 0)
}

// mockHostForGeth 是用于测试的 mock Host
type mockHostForGeth struct {
	balances map[types.Address]*big.Int
	storage  map[types.Address]map[types.Hash]types.Hash
	codes    map[types.Address][]byte
	nonces   map[types.Address]uint64
	refund   uint64
}

func (m *mockHostForGeth) AccountExists(addr types.Address) bool {
	balance, exists := m.balances[addr]
	return exists && balance != nil && balance.Sign() > 0
}

func (m *mockHostForGeth) GetStorage(addr types.Address, key types.Hash) types.Hash {
	if addrStorage, ok := m.storage[addr]; ok {
		if value, ok := addrStorage[key]; ok {
			return value
		}
	}
	return types.ZeroHash
}

func (m *mockHostForGeth) SetStorage(addr types.Address, key types.Hash, value types.Hash, config *chain.ForksInTime) runtime.StorageStatus {
	if m.storage[addr] == nil {
		m.storage[addr] = make(map[types.Hash]types.Hash)
	}
	m.storage[addr][key] = value
	return runtime.StorageModified
}

func (m *mockHostForGeth) SetState(addr types.Address, key types.Hash, value types.Hash) {
	if m.storage[addr] == nil {
		m.storage[addr] = make(map[types.Hash]types.Hash)
	}
	m.storage[addr][key] = value
}

func (m *mockHostForGeth) SetNonPayable(nonPayable bool) {}

func (m *mockHostForGeth) GetBalance(addr types.Address) *big.Int {
	if balance, ok := m.balances[addr]; ok {
		return new(big.Int).Set(balance)
	}
	return big.NewInt(0)
}

func (m *mockHostForGeth) GetCodeSize(addr types.Address) int {
	if code, ok := m.codes[addr]; ok {
		return len(code)
	}
	return 0
}

func (m *mockHostForGeth) GetCodeHash(addr types.Address) types.Hash {
	if code, ok := m.codes[addr]; ok && len(code) > 0 {
		return types.BytesToHash(code)
	}
	return types.EmptyCodeHash
}

func (m *mockHostForGeth) GetCode(addr types.Address) []byte {
	if code, ok := m.codes[addr]; ok {
		return code
	}
	return []byte{}
}

func (m *mockHostForGeth) Selfdestruct(addr types.Address, beneficiary types.Address) {
	if balance, ok := m.balances[addr]; ok {
		if m.balances[beneficiary] == nil {
			m.balances[beneficiary] = big.NewInt(0)
		}
		m.balances[beneficiary].Add(m.balances[beneficiary], balance)
		delete(m.balances, addr)
	}
}

func (m *mockHostForGeth) GetTxContext() runtime.TxContext {
	return runtime.TxContext{
		Origin:     types.ZeroAddress,
		Coinbase:   types.ZeroAddress,
		Number:     1,
		Timestamp:  1000,
		GasLimit:   1000000,
		ChainID:    1,
		Difficulty: types.ZeroHash,
		BaseFee:    big.NewInt(1000000000),
	}
}

func (m *mockHostForGeth) GetBlockHash(number int64) types.Hash {
	return types.ZeroHash
}

func (m *mockHostForGeth) EmitLog(addr types.Address, topics []types.Hash, data []byte) {}

func (m *mockHostForGeth) Callx(*runtime.Contract, runtime.Host) *runtime.ExecutionResult {
	return &runtime.ExecutionResult{}
}

func (m *mockHostForGeth) Empty(addr types.Address) bool {
	balance := m.GetBalance(addr)
	return balance.Sign() == 0 && len(m.GetCode(addr)) == 0 && m.GetNonce(addr) == 0
}

func (m *mockHostForGeth) GetNonce(addr types.Address) uint64 {
	if nonce, ok := m.nonces[addr]; ok {
		return nonce
	}
	return 0
}

func (m *mockHostForGeth) Transfer(from types.Address, to types.Address, amount *big.Int) error {
	if m.balances[from] == nil {
		m.balances[from] = big.NewInt(0)
	}
	if m.balances[to] == nil {
		m.balances[to] = big.NewInt(0)
	}

	if m.balances[from].Cmp(amount) < 0 {
		return runtime.ErrInsufficientBalance
	}

	m.balances[from].Sub(m.balances[from], amount)
	m.balances[to].Add(m.balances[to], amount)
	return nil
}

func (m *mockHostForGeth) GetTracer() runtime.VMTracer {
	return nil
}

func (m *mockHostForGeth) GetRefund() uint64 {
	return m.refund
}
