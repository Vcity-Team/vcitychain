package evm

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
)

func TestContractRefAdapter_Address(t *testing.T) {
	addr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	contract := runtime.NewContract(
		1,
		types.ZeroAddress,
		types.ZeroAddress,
		addr,
		big.NewInt(0),
		100000,
		[]byte{},
	)

	adapter := NewContractRefAdapter(contract)
	commonAddr := adapter.Address()

	assert.Equal(t, VcAddressToCommon(addr), commonAddr)
}


