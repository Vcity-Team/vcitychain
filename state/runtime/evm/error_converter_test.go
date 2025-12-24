package evm

import (
	"errors"
	"testing"

	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/stretchr/testify/assert"
)

func TestConvertError_Nil(t *testing.T) {
	result := convertError(nil)
	assert.NoError(t, result)
}

func TestConvertError_OutOfGas(t *testing.T) {
	err := vm.ErrOutOfGas
	result := convertError(err)
	assert.Equal(t, runtime.ErrOutOfGas, result)
}

func TestConvertError_CodeStoreOutOfGas(t *testing.T) {
	err := vm.ErrCodeStoreOutOfGas
	result := convertError(err)
	assert.Equal(t, runtime.ErrCodeStoreOutOfGas, result)
}

func TestConvertError_Depth(t *testing.T) {
	err := vm.ErrDepth
	result := convertError(err)
	assert.Equal(t, runtime.ErrDepth, result)
}

func TestConvertError_InsufficientBalance(t *testing.T) {
	err := vm.ErrInsufficientBalance
	result := convertError(err)
	assert.Equal(t, runtime.ErrInsufficientBalance, result)
}

func TestConvertError_ExecutionReverted(t *testing.T) {
	err := vm.ErrExecutionReverted
	result := convertError(err)
	assert.Equal(t, runtime.ErrExecutionReverted, result)
}

func TestConvertError_UnknownError(t *testing.T) {
	err := errors.New("unknown error")
	result := convertError(err)
	assert.Equal(t, err, result)
}


