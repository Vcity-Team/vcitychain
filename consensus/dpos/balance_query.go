package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// NativeTokenBalanceQuerier 定义原生代币余额查询接口
type NativeTokenBalanceQuerier interface {
	// GetNativeTokenBalance 获取指定地址的原生代币余额
	GetNativeTokenBalance(address types.Address) (*big.Int, error)
}

// RealBalanceQuerier 真实余额查询实现
type RealBalanceQuerier struct {
	logger     hclog.Logger
	blockchain interface {
		Header() *types.Header
		GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
		GetAccount(root types.Hash, addr types.Address) (*state.Account, error)
	}
}

// NewRealBalanceQuerier 创建新的真实余额查询器
func NewRealBalanceQuerier(logger hclog.Logger, blockchain interface {
	Header() *types.Header
	GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
	GetAccount(root types.Hash, addr types.Address) (*state.Account, error)
}) *RealBalanceQuerier {
	return &RealBalanceQuerier{
		logger:     logger,
		blockchain: blockchain,
	}
}

// GetNativeTokenBalance 获取指定地址的原生代币余额
func (q *RealBalanceQuerier) GetNativeTokenBalance(address types.Address) (*big.Int, error) {
	// 获取当前区块头
	currentHeader := q.blockchain.Header()
	if currentHeader == nil {
		return nil, fmt.Errorf("failed to get current header")
	}

	// 方法1：尝试使用GetBalance方法
	if balance, err := q.blockchain.GetBalance(currentHeader.StateRoot, address); err == nil {
		q.logger.Info("Native token balance queried (real)",
			"address", address.String(),
			"balance", balance.String(),
			"balanceHex", fmt.Sprintf("0x%x", balance),
			"balanceWei", balance.String(),
			"note", "using real balance from GetBalance method")
		return balance, nil
	} else if err.Error() == "state not found" {
		q.logger.Info("Native token balance queried (real)",
			"address", address.String(),
			"balance", "0",
			"note", "account not found, returning 0 balance")
		return big.NewInt(0), nil
	}

	// 方法2：尝试使用GetAccount方法
	account, err := q.blockchain.GetAccount(currentHeader.StateRoot, address)
	if err != nil {
		// 如果账户不存在，返回0余额
		if err.Error() == "state not found" {
			q.logger.Info("Native token balance queried (real)",
				"address", address.String(),
				"balance", "0",
				"note", "account not found, returning 0 balance")
			return big.NewInt(0), nil
		}
		return nil, fmt.Errorf("failed to get account for address %s: %w", address.String(), err)
	}

	// 记录真实的余额查询日志
	q.logger.Info("Native token balance queried (real)",
		"address", address.String(),
		"balance", account.Balance.String(),
		"balanceHex", fmt.Sprintf("0x%x", account.Balance),
		"balanceWei", account.Balance.String(),
		"note", "using real balance from GetAccount method")

	return account.Balance, nil
}
