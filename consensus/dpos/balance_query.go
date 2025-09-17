package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// NativeTokenBalanceQuerier 定义原生代币余额查询接口
type NativeTokenBalanceQuerier interface {
	// GetNativeTokenBalance 获取指定地址的原生代币余额
	GetNativeTokenBalance(address types.Address) (*big.Int, error)
}

// MockBalanceQuerier 模拟余额查询实现（用于测试）
type MockBalanceQuerier struct {
	logger hclog.Logger
}

// NewMockBalanceQuerier 创建新的模拟余额查询器
func NewMockBalanceQuerier(logger hclog.Logger) *MockBalanceQuerier {
	return &MockBalanceQuerier{
		logger: logger,
	}
}

// GetNativeTokenBalance 获取指定地址的原生代币余额（模拟实现）
func (q *MockBalanceQuerier) GetNativeTokenBalance(address types.Address) (*big.Int, error) {
	// 模拟返回固定余额（1 VCITY = 1e18 wei）
	balance := big.NewInt(1000000000000000000) // 1 VCITY
	
	// 记录详细的余额查询日志
	q.logger.Info("Native token balance queried (mock)", 
		"address", address.String(),
		"balance", balance.String(),
		"balanceHex", fmt.Sprintf("0x%x", balance),
		"balanceWei", balance.String(),
		"note", "using mock balance for testing")
	
	return balance, nil
}

// getNativeTokenBalance 便捷函数，用于获取原生代币余额
func getNativeTokenBalance(querier NativeTokenBalanceQuerier, address types.Address) (*big.Int, error) {
	if querier == nil {
		return nil, fmt.Errorf("balance querier is nil")
	}
	
	return querier.GetNativeTokenBalance(address)
}
