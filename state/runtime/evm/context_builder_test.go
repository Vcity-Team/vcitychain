package evm

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
)

func TestBuildChainConfig(t *testing.T) {
	config := &chain.ForksInTime{
		Homestead:      true,
		EIP150:         true,
		EIP155:         true,
		EIP158:         true,
		Byzantium:      true,
		Constantinople: true,
		Petersburg:     true,
		Istanbul:       true,
		London:         true,
	}

	chainConfig := buildChainConfig(config, 1)

	assert.NotNil(t, chainConfig)
	assert.Equal(t, big.NewInt(1), chainConfig.ChainID)
	assert.NotNil(t, chainConfig.HomesteadBlock)
	assert.NotNil(t, chainConfig.EIP150Block)
	assert.NotNil(t, chainConfig.EIP155Block)
	assert.NotNil(t, chainConfig.EIP158Block)
	assert.NotNil(t, chainConfig.ByzantiumBlock)
	assert.NotNil(t, chainConfig.ConstantinopleBlock)
	assert.NotNil(t, chainConfig.PetersburgBlock)
	assert.NotNil(t, chainConfig.IstanbulBlock)
	assert.NotNil(t, chainConfig.LondonBlock)
}

func TestBuildTxContext(t *testing.T) {
	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
	}

	txCtx := buildTxContext(host)

	assert.NotNil(t, txCtx)
	assert.Equal(t, VcAddressToCommon(types.ZeroAddress), txCtx.Origin)
}

func TestBuildBlockContext(t *testing.T) {
	host := &mockHostForGeth{
		balances: make(map[types.Address]*big.Int),
	}

	blockCtx := buildBlockContext(host)

	assert.NotNil(t, blockCtx)
	assert.NotNil(t, blockCtx.CanTransfer)
	assert.NotNil(t, blockCtx.Transfer)
	assert.NotNil(t, blockCtx.GetHash)
	assert.Equal(t, uint64(1), blockCtx.BlockNumber.Uint64())
	assert.Equal(t, uint64(1000), blockCtx.Time)
	assert.Equal(t, uint64(1000000), blockCtx.GasLimit)
}
