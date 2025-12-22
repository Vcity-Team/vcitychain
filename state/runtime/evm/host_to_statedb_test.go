package evm

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
)

func TestHostToStateDBAdapter_GetBalance(t *testing.T) {
	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
	}
	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	host.balances[addr] = big.NewInt(1000)

	adapter := NewHostToStateDBAdapter(host, &chain.ForksInTime{})
	commonAddr := VcAddressToCommon(addr)

	balance := adapter.GetBalance(commonAddr)
	assert.Equal(t, big.NewInt(1000), balance)
}

func TestHostToStateDBAdapter_GetState(t *testing.T) {
	host := &mockHostForGeth{
		storage: make(map[types.Address]map[types.Hash]types.Hash),
	}
	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	key := types.StringToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := types.StringToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	if host.storage[addr] == nil {
		host.storage[addr] = make(map[types.Hash]types.Hash)
	}
	host.storage[addr][key] = value

	adapter := NewHostToStateDBAdapter(host, &chain.ForksInTime{})
	commonAddr := VcAddressToCommon(addr)
	commonKey := VcHashToCommon(key)

	result := adapter.GetState(commonAddr, commonKey)
	assert.Equal(t, VcHashToCommon(value), result)
}

func TestHostToStateDBAdapter_SetState(t *testing.T) {
	host := &mockHostForGeth{
		storage: make(map[types.Address]map[types.Hash]types.Hash),
	}
	adapter := NewHostToStateDBAdapter(host, &chain.ForksInTime{})

	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	key := types.StringToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := types.StringToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	commonAddr := VcAddressToCommon(addr)
	commonKey := VcHashToCommon(key)
	commonValue := VcHashToCommon(value)

	adapter.SetState(commonAddr, commonKey, commonValue)

	result := adapter.GetState(commonAddr, commonKey)
	assert.Equal(t, commonValue, result)
}

func TestHostToStateDBAdapter_GetCode(t *testing.T) {
	host := &mockHostForGeth{
		codes: make(map[types.Address][]byte),
	}
	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	code := []byte{0x60, 0x00, 0x60, 0x00, 0x52} // PUSH1 0 PUSH1 0 MSTORE
	host.codes[addr] = code

	adapter := NewHostToStateDBAdapter(host, &chain.ForksInTime{})
	commonAddr := VcAddressToCommon(addr)

	result := adapter.GetCode(commonAddr)
	assert.Equal(t, code, result)
}

func TestHostToStateDBAdapter_Exist(t *testing.T) {
	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
	}
	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	host.balances[addr] = big.NewInt(1000)

	adapter := NewHostToStateDBAdapter(host, &chain.ForksInTime{})
	commonAddr := VcAddressToCommon(addr)

	assert.True(t, adapter.Exist(commonAddr))

	emptyAddr := types.StringToAddress("0x0000000000000000000000000000000000000000")
	commonEmptyAddr := VcAddressToCommon(emptyAddr)
	assert.False(t, adapter.Exist(commonEmptyAddr))
}

func TestHostToStateDBAdapter_Empty(t *testing.T) {
	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
		codes:    make(map[types.Address][]byte),
		nonces:   make(map[types.Address]uint64),
	}
	adapter := NewHostToStateDBAdapter(host, &chain.ForksInTime{})

	emptyAddr := types.StringToAddress("0x0000000000000000000000000000000000000000")
	commonEmptyAddr := VcAddressToCommon(emptyAddr)
	assert.True(t, adapter.Empty(commonEmptyAddr))

	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	host.balances[addr] = big.NewInt(1000)
	commonAddr := VcAddressToCommon(addr)
	assert.False(t, adapter.Empty(commonAddr))
}
