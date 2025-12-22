package evm

import (
	"errors"

	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/ethereum/go-ethereum/core/vm"
)

// convertError 将 go-ethereum 的错误转换为 vcitychain 的错误
func convertError(err error) error {
	if err == nil {
		return nil
	}

	// 错误映射表
	switch {
	case errors.Is(err, vm.ErrOutOfGas):
		return runtime.ErrOutOfGas
	case errors.Is(err, vm.ErrCodeStoreOutOfGas):
		return runtime.ErrCodeStoreOutOfGas
	case errors.Is(err, vm.ErrDepth):
		return runtime.ErrDepth
	case errors.Is(err, vm.ErrInsufficientBalance):
		return runtime.ErrInsufficientBalance
	case errors.Is(err, vm.ErrContractAddressCollision):
		return runtime.ErrContractAddressCollision
	case errors.Is(err, vm.ErrExecutionReverted):
		return runtime.ErrExecutionReverted
	case errors.Is(err, vm.ErrMaxCodeSizeExceeded):
		return runtime.ErrMaxCodeSizeExceeded
	case errors.Is(err, vm.ErrWriteProtection):
		return runtime.ErrUnauthorizedCaller
	case errors.Is(err, vm.ErrInvalidJump):
		return errors.New("invalid jump destination")
	case errors.Is(err, vm.ErrReturnDataOutOfBounds):
		return errors.New("return data out of bounds")
	}

	// 如果无法映射，返回原始错误
	return err
}
