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
		GetAccount(root types.Hash, addr types.Address) (*state.Account, error)
	}
}

// NewRealBalanceQuerier 创建新的真实余额查询器
func NewRealBalanceQuerier(logger hclog.Logger, blockchain interface {
	Header() *types.Header
	GetAccount(root types.Hash, addr types.Address) (*state.Account, error)
}) *RealBalanceQuerier {
	return &RealBalanceQuerier{
		logger:     logger,
		blockchain: blockchain,
	}
}

// GetNativeTokenBalance 获取指定地址的原生代币余额（真实实现）
func (q *RealBalanceQuerier) GetNativeTokenBalance(address types.Address) (*big.Int, error) {
	// 获取当前区块头
	currentHeader := q.blockchain.Header()
	if currentHeader == nil {
		return nil, fmt.Errorf("failed to get current header")
	}
	
	// 查询账户余额
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
		"note", "using real balance from blockchain state")
	
	return account.Balance, nil
}

// getNativeTokenBalance 便捷函数，用于获取原生代币余额
func getNativeTokenBalance(querier NativeTokenBalanceQuerier, address types.Address) (*big.Int, error) {
	if querier == nil {
		return nil, fmt.Errorf("balance querier is nil")
	}
	
	return querier.GetNativeTokenBalance(address)
}
