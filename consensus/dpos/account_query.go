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

	currentHeader := d.config.Blockchain.Header()
	if currentHeader == nil {
		return nil, fmt.Errorf("current header not found")
	}

	d.logger.Debug("🔍 获取账户余额",
		"address", address.String(),
		"blockNumber", currentHeader.Number,
		"stateRoot", currentHeader.StateRoot.String())

	transition, err := d.config.Executor.BeginTxn(currentHeader.StateRoot, currentHeader, types.ZeroAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	balance := transition.GetBalance(address)

	d.logger.Debug("✅ 成功获取账户余额",
		"address", address.String(),
		"balance", balance.String())

	return balance, nil
}

// getAccountNonce 获取账户nonce
func (d *DPoS) getAccountNonce(address types.Address) (uint64, error) {
	if d.state != nil && d.blockchain != nil {
		currentHeader := d.config.Blockchain.Header()
		if currentHeader != nil {
			transition, err := d.config.Executor.BeginTxn(currentHeader.StateRoot, currentHeader, types.ZeroAddress)
			if err == nil {
				if account, exists := transition.Txn().GetAccount(address); exists && account != nil {
					return account.Nonce, nil
				}
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

	// 更新区块头的状态根
	header.StateRoot = types.BytesToHash(newStateRoot)
	header.ComputeHash()

	d.logger.Info("✅ 状态根已同步到区块链",
		"blockNumber", header.Number,
		"newstateRoot", fmt.Sprintf("0x%x", newStateRoot),
		"note", "验证节点状态根已更新，后续比对将使用新状态根")

	return nil
}
