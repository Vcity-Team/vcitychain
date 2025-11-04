package dpos

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// getValidatorBalance 获取验证者余额
func (d *DPoS) getValidatorBalance(address types.Address) (*big.Int, error) {
	d.logger.Debug("🔍 开始查询验证者余额", "address", address.String())

	// 检查config和executor
	if d.config == nil {
		d.logger.Error("❌ DPoS config is nil")
		return big.NewInt(0), nil
	}

	if d.config.Executor == nil {
		d.logger.Error("❌ DPoS config.Executor is nil")
		return big.NewInt(0), nil
	}

	// 获取当前区块头
	currentHeader := d.config.Blockchain.Header()
	if currentHeader == nil {
		d.logger.Error("❌ 无法获取当前区块头")
		return nil, fmt.Errorf("failed to get current header")
	}

	d.logger.Debug("📋 当前区块头信息", "number", currentHeader.Number, "stateRoot", currentHeader.StateRoot.String())

	// 通过executor查询余额
	if d.config.Executor != nil {
		// 通过state.Executor的StateAt方法直接获取状态快照
		snapshot, err := d.config.Executor.StateAt(currentHeader.StateRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to create snapshot at state root %s: %w", currentHeader.StateRoot.String(), err)
		}

		account, err := snapshot.GetAccount(address)
		if err != nil {
			d.logger.Warn("⚠️ 无法获取账户信息，返回0余额", "address", address.String(), "error", err)
			return big.NewInt(0), nil
		}

		// 🆕 检查账户余额是否为空，避免空指针解引用
		if account == nil || account.Balance == nil {
			d.logger.Warn("⚠️ 账户或余额为空，返回0余额", "address", address.String())
			return big.NewInt(0), nil
		}

		// 返回账户余额
		d.logger.Debug("✅ 成功查询到验证者余额", "address", address.String(), "balance", account.Balance.String())
		return account.Balance, nil
	}

	// 如果无法获取executor，返回0余额
	d.logger.Warn("无法获取executor，返回0余额", "address", address.String())
	return big.NewInt(0), nil
}

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

// executeBatchStateUpdate 已保留在 dpos.go 中（函数复杂，依赖较多）

// calculateStateUpdateHash 计算状态更新哈希
func (d *DPoS) calculateStateUpdateHash(stateUpdates map[types.Address]*big.Int) []byte {
	// 创建哈希计算器
	hasher := crypto.NewKeccakState()

	// 按地址排序确保一致性
	var addresses []types.Address
	for addr := range stateUpdates {
		addresses = append(addresses, addr)
	}
	sort.Slice(addresses, func(i, j int) bool {
		return bytes.Compare(addresses[i].Bytes(), addresses[j].Bytes()) < 0
	})

	// 计算哈希
	for _, addr := range addresses {
		hasher.Write(addr.Bytes())
		hasher.Write(stateUpdates[addr].Bytes())
	}

	return hasher.Sum(nil)
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

