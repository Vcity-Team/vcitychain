package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

// getEpochSize 根据配置计算epoch大小（区块数）
func (r *dposRuntime) getEpochSize() uint64 {
	if r.config == nil || r.config.dposBackend == nil {
		r.logger.Warn("⚠️ dposRuntime配置为空，使用默认epoch大小")
		return 5 // 默认值：10秒 / 2秒 = 5个区块
	}

	dposInstance, ok := r.config.dposBackend.(*DPoS)
	if !ok {
		r.logger.Warn("⚠️ 无法转换为DPoS实例，使用默认epoch大小")
		return 5
	}

	return dposInstance.getEpochSize()
}

// runtimeBalanceQuerier 实现 NativeTokenBalanceQuerier 接口
type runtimeBalanceQuerier struct {
	runtime *dposRuntime
}

// GetNativeTokenBalance 通过 runtime 查询账户余额
func (r *runtimeBalanceQuerier) GetNativeTokenBalance(address types.Address) (*big.Int, error) {
	return r.runtime.getAccountBalance(address)
}

// getAccountBalance 查询账户余额
func (r *dposRuntime) getAccountBalance(address types.Address) (*big.Int, error) {
	if r.config == nil || r.config.blockchain == nil {
		return big.NewInt(0), fmt.Errorf("blockchain not available")
	}

	// 获取当前区块头
	currentHeader := r.config.blockchain.CurrentHeader()
	if currentHeader == nil {
		return big.NewInt(0), fmt.Errorf("current header not available")
	}

	r.logger.Debug("🔍 开始查询验证者余额", "address", address.String())

	// 通过backend获取DPoS实例，然后查询真实余额
	if r.backend != nil {
		if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance.config != nil && dposInstance.config.Executor != nil {
			// 通过executor查询余额
			snapshot, err := dposInstance.config.Executor.StateAt(currentHeader.StateRoot)
			if err != nil {
				r.logger.Error("❌ 无法创建状态快照", "error", err)
				return big.NewInt(0), WrapError("create state snapshot", err)
			}

			account, err := snapshot.GetAccount(address)
			if err != nil {
				r.logger.Warn("⚠️ 无法获取账户信息，返回0余额", "address", address.String(), "error", err)
				return big.NewInt(0), nil
			}

			// 🆕 检查账户余额是否为空，避免空指针解引用
			if account == nil || account.Balance == nil {
				r.logger.Warn("⚠️ 账户或余额为空，返回0余额", "address", address.String())
				return big.NewInt(0), nil
			}

			r.logger.Debug("✅ 成功查询到验证者余额", "address", address.String(), "balance", account.Balance.String())
			return account.Balance, nil
		}
	}

	// 如果无法通过backend查询，返回0余额
	r.logger.Warn("⚠️ 无法通过backend查询余额，返回0余额", "address", address.String())
	return big.NewInt(0), nil
}

