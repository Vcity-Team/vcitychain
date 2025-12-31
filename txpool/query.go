package txpool

import (
	"sync/atomic"

	"github.com/Vcity-Team/vcitychain/types"
)

/* QUERY methods */
// Used to query the pool for specific state info.

// GetNonce returns the next nonce for the account
//
// -> Returns the value from the TxPool if the account is initialized in-memory
//
// -> Returns the value from the world state otherwise
func (p *TxPool) GetNonce(addr types.Address) uint64 {
	account := p.accounts.get(addr)
	if account == nil {
		header := p.store.Header()
		stateRoot := header.StateRoot
		stateNonce := p.store.GetNonce(stateRoot, addr)

		// 🔍 调试：记录从链上状态获取的 nonce（Info 级别以便在 gas 估算时可见）
		// ⚠️ 关键：如果 stateNonce 不是最新的，说明 header.StateRoot 可能不是最新的状态根
		// 这可能是因为 header 还没有更新到包含最新交易的状态
		p.logger.Info("🔍 [TxPool.GetNonce] 从链上状态获取 nonce（账户不在TxPool中）",
			"addr", addr.String(),
			"blockNumber", header.Number,
			"stateRoot", stateRoot.String(),
			"stateNonce", stateNonce,
			"returning", stateNonce,
			"note", "如果 stateNonce 不是最新的，说明 header.StateRoot 可能不是最新的状态根")

		return stateNonce
	}

	poolNonce := account.getNonce()
	header := p.store.Header()
	stateRoot := header.StateRoot
	stateNonce := p.store.GetNonce(stateRoot, addr)

	// 🔍 调试：记录从 TxPool 获取的 nonce 和链上 nonce 的对比（Info 级别以便在 gas 估算时可见）
	// ⚠️ 关键：如果 poolNonce < stateNonce，说明 TxPool 的 nonce 没有及时更新，应该使用 stateNonce
	p.logger.Info("🔍 [TxPool.GetNonce] 从 TxPool 获取 nonce",
		"addr", addr.String(),
		"blockNumber", header.Number,
		"stateRoot", stateRoot.String(),
		"poolNonce", poolNonce,
		"stateNonce", stateNonce,
		"nonceMismatch", poolNonce != stateNonce,
		"returning", poolNonce)

	// ⭐ 关键修复：如果 TxPool 的 nonce 小于链上的 nonce，说明 TxPool 没有及时更新
	// 应该返回链上的 nonce，而不是 TxPool 的 nonce
	// 这确保了即使 TxPool 没有及时更新，也能获取到正确的 nonce
	if poolNonce < stateNonce {
		p.logger.Warn("⚠️ [TxPool.GetNonce] TxPool nonce 小于链上 nonce，使用链上 nonce",
			"addr", addr.String(),
			"poolNonce", poolNonce,
			"stateNonce", stateNonce,
			"returning", stateNonce)
		return stateNonce
	}

	return poolNonce
}

// GetCapacity returns the current number of slots
// occupied in the pool as well as the max limit
func (p *TxPool) GetCapacity() (uint64, uint64) {
	return p.gauge.read(), p.gauge.max
}

// GetPendingTx returns the transaction by hash in the TxPool (pending txn) [Thread-safe]
func (p *TxPool) GetPendingTx(txHash types.Hash) (*types.Transaction, bool) {
	tx, ok := p.index.get(txHash)
	if !ok {
		return nil, false
	}

	return tx, true
}

// GetTxs gets pending and queued transactions
func (p *TxPool) GetTxs(inclQueued bool) (
	allPromoted, allEnqueued map[types.Address][]*types.Transaction,
) {
	return p.accounts.allTxs(inclQueued)
}

// GetBaseFee returns current base fee
func (p *TxPool) GetBaseFee() uint64 {
	return atomic.LoadUint64(&p.baseFee)
}

// GetMaxAccountEnqueued returns the maximum number of enqueued transactions per account
func (p *TxPool) GetMaxAccountEnqueued() uint64 {
	return p.maxAccountEnqueued
}

// SetBaseFee calculates base fee from the (current) header and sets value into baseFee field
func (p *TxPool) SetBaseFee(header *types.Header) {
	atomic.StoreUint64(&p.baseFee, p.store.CalculateBaseFee(header))
}
