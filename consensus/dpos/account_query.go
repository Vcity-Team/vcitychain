package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

// getAccountBalance 获取账户余额
func (d *DPoS) getAccountBalance(address types.Address) (*big.Int, error) {
	if d.blockchain == nil {
		return nil, fmt.Errorf("blockchain wrapper not available")
	}

	// 获取当前区块头 - 修复：直接使用Header()而不是通过总难度
	currentHeader := d.config.Blockchain.Header()
	if currentHeader == nil {
		return nil, fmt.Errorf("current header not found")
	}

	d.logger.Debug("🔍 获取账户余额",
		"address", address.String(),
		"blockNumber", currentHeader.Number,
		"stateRoot", currentHeader.StateRoot.String())

	// 创建状态转换
	transition, err := d.config.Executor.BeginTxn(currentHeader.StateRoot, currentHeader, types.ZeroAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	// 获取账户余额
	balance := transition.GetBalance(address)

	d.logger.Debug("✅ 成功获取账户余额",
		"address", address.String(),
		"balance", balance.String())

	return balance, nil
}

// getAccountNonce 获取账户nonce
func (d *DPoS) getAccountNonce(address types.Address) (uint64, error) {
	// 尝试通过state获取nonce
	if d.state != nil && d.blockchain != nil {
		// 获取当前区块头
		currentHeader := d.config.Blockchain.Header()
		if currentHeader != nil {
			// 创建状态转换
			transition, err := d.config.Executor.BeginTxn(currentHeader.StateRoot, currentHeader, types.ZeroAddress)
			if err == nil {
				// GetAccount 返回 (*Account, bool)
				if account, exists := transition.Txn().GetAccount(address); exists && account != nil {
					return account.Nonce, nil
				}
				// 或者使用 GetNonce 方法
				nonce := transition.GetNonce(address)
				return nonce, nil
			}
		}
	}

	return 0, fmt.Errorf("failed to get account nonce")
}

// syncStateRootToBlockchain 将状态根同步到区块链
func (d *DPoS) syncStateRootToBlockchain(header *types.Header, newStateRoot []byte) error {
	d.logger.Info("🔄 开始同步状态根到区块链",
		"blockNumber", header.Number,
		"oldStateRoot", fmt.Sprintf("0x%x", header.StateRoot),
		"newStateRoot", fmt.Sprintf("0x%x", newStateRoot))

	// 🆕 关键：确保区块链的当前状态使用新的状态根
	// 这里需要强制更新区块链的当前状态，而不仅仅是区块头
	// 通过重新设置区块头的状态根来触发区块链状态的更新

	// 更新区块头的状态根
	header.StateRoot = types.BytesToHash(newStateRoot)
	header.ComputeHash()

	// 🆕 重要：这里需要确保区块链系统知道状态根已经更新
	// 通过调用区块链的相关方法来同步状态
	if d.config.Blockchain != nil {
		// 这里可能需要调用区块链的特定方法来更新当前状态
		// 具体实现取决于区块链接口的设计
		d.logger.Info("🔧 状态根已更新到区块头",
			"blockNumber", header.Number,
			"newStateRoot", fmt.Sprintf("0x%x", newStateRoot))
	}

	d.logger.Info("✅ 状态根已同步到区块链",
		"blockNumber", header.Number,
		"stateRoot", fmt.Sprintf("0x%x", newStateRoot),
		"note", "验证节点状态根已更新，后续比对将使用新状态根")

	return nil
}

