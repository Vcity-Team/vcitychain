package txpool

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hashByte(b byte) types.Hash {
	return types.Hash{b}
}

func blockWithTxs(number uint64, hash types.Hash, txs ...*types.Transaction) *types.Block {
	for _, tx := range txs {
		tx.ComputeHash(number)
	}

	return &types.Block{
		Header: &types.Header{
			Number: number,
			Hash:   hash,
		},
		Transactions: txs,
	}
}

func newReorgTestStore(blocks map[types.Hash]*types.Block) defaultMockStore {
	store := NewDefaultMockStore(mockHeader)
	store.getBlockByHashFn = func(h types.Hash, _ bool) (*types.Block, bool) {
		block, ok := blocks[h]

		return block, ok
	}

	return store
}

func newReorgTestPool(t *testing.T, store defaultMockStore) *TxPool {
	t.Helper()

	pool, err := newTestPool(store)
	require.NoError(t, err)
	pool.SetSigner(&mockSigner{})
	pool.Start()

	return pool
}

func mineBlock(t *testing.T, pool *TxPool, block *types.Block) {
	t.Helper()

	pool.ResetWithEvent(&blockchain.Event{
		NewChain: []*types.Header{block.Header},
	})
}

func TestReinject_BasicReorg(t *testing.T) {
	t.Parallel()

	tx := newTx(addr1, 0, 1)

	forkBlock := blockWithTxs(100, hashByte(0xAA), tx)
	canonicalBlock := blockWithTxs(100, hashByte(0xBB))

	store := newReorgTestStore(map[types.Hash]*types.Block{
		forkBlock.Header.Hash:     forkBlock,
		canonicalBlock.Header.Hash: canonicalBlock,
	})

	pool := newReorgTestPool(t, store)

	require.NoError(t, pool.addTx(local, tx))

	mineBlock(t, pool, forkBlock)

	_, ok := pool.GetPendingTx(tx.Hash)
	assert.False(t, ok, "tx should be removed after fork block mined")

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventReorg,
		OldChain: []*types.Header{forkBlock.Header},
		NewChain: []*types.Header{canonicalBlock.Header},
	})

	_, ok = pool.GetPendingTx(tx.Hash)
	assert.True(t, ok, "discarded fork tx should be re-injected after reorg")
}

func TestReinject_SkipWhenCanonicalNonceTaken(t *testing.T) {
	t.Parallel()

	const nonce = uint64(5)

	txFork := newTx(addr1, nonce, 1)
	txFork.Input = []byte("fork-tx")

	txCanon := newTx(addr1, nonce, 1)
	txCanon.Input = []byte("canonical-tx")

	forkBlock := blockWithTxs(100, hashByte(0xAA), txFork)
	canonicalBlock := blockWithTxs(100, hashByte(0xBB), txCanon)

	store := newReorgTestStore(map[types.Hash]*types.Block{
		forkBlock.Header.Hash:      forkBlock,
		canonicalBlock.Header.Hash: canonicalBlock,
	})
	store.nonces = map[types.Address]uint64{
		addr1: nonce + 1,
	}

	pool := newReorgTestPool(t, store)

	mineBlock(t, pool, forkBlock)
	mineBlock(t, pool, canonicalBlock)

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventReorg,
		OldChain: []*types.Header{forkBlock.Header},
		NewChain: []*types.Header{canonicalBlock.Header},
	})

	_, ok := pool.GetPendingTx(txFork.Hash)
	assert.False(t, ok, "fork tx must not reinject when canonical chain mined same nonce")
}

func TestReinject_SkipKnownCanonicalHash(t *testing.T) {
	t.Parallel()

	tx := newTx(addr1, 0, 1)
	block := blockWithTxs(100, hashByte(0xCC), tx)

	store := newReorgTestStore(map[types.Hash]*types.Block{
		block.Header.Hash: block,
	})

	pool := newReorgTestPool(t, store)

	mineBlock(t, pool, block)

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventReorg,
		OldChain: []*types.Header{block.Header},
		NewChain: []*types.Header{block.Header},
	})

	_, ok := pool.GetPendingTx(tx.Hash)
	assert.False(t, ok, "tx already on canonical chain should not be duplicated in pool")
}

func TestReinject_SkipStateTx(t *testing.T) {
	t.Parallel()

	tx := newTx(types.ZeroAddress, 0, 1)
	tx.Type = types.StateTx

	forkBlock := blockWithTxs(100, hashByte(0xDD), tx)
	canonicalBlock := blockWithTxs(100, hashByte(0xEE))

	store := newReorgTestStore(map[types.Hash]*types.Block{
		forkBlock.Header.Hash:      forkBlock,
		canonicalBlock.Header.Hash: canonicalBlock,
	})

	pool := newReorgTestPool(t, store)

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventReorg,
		OldChain: []*types.Header{forkBlock.Header},
		NewChain: []*types.Header{canonicalBlock.Header},
	})

	assert.Equal(t, uint64(0), pool.Length(), "state tx must not be re-injected")
}

func TestReinject_MultiBlockOldChain(t *testing.T) {
	t.Parallel()

	tx1 := newTx(addr1, 0, 1)
	tx2 := newTx(addr2, 0, 1)

	oldBlock1 := blockWithTxs(100, hashByte(0x01), tx1)
	oldBlock2 := blockWithTxs(101, hashByte(0x02), tx2)
	newBlock := blockWithTxs(101, hashByte(0x03))

	store := newReorgTestStore(map[types.Hash]*types.Block{
		oldBlock1.Header.Hash: oldBlock1,
		oldBlock2.Header.Hash: oldBlock2,
		newBlock.Header.Hash:  newBlock,
	})

	pool := newReorgTestPool(t, store)

	mineBlock(t, pool, oldBlock1)
	mineBlock(t, pool, oldBlock2)
	mineBlock(t, pool, newBlock)

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventReorg,
		OldChain: []*types.Header{oldBlock1.Header, oldBlock2.Header},
		NewChain: []*types.Header{newBlock.Header},
	})

	_, ok1 := pool.GetPendingTx(tx1.Hash)
	_, ok2 := pool.GetPendingTx(tx2.Hash)
	assert.True(t, ok1, "tx from first discarded block should be re-injected")
	assert.True(t, ok2, "tx from second discarded block should be re-injected")
}

func TestReinject_NoOldChainUnchanged(t *testing.T) {
	t.Parallel()

	tx := newTx(addr1, 0, 1)
	headBlock := blockWithTxs(99, hashByte(0xFF))

	store := newReorgTestStore(map[types.Hash]*types.Block{
		headBlock.Header.Hash: headBlock,
	})

	pool := newReorgTestPool(t, store)

	require.NoError(t, pool.addTx(local, tx))

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventHead,
		NewChain: []*types.Header{headBlock.Header},
	})

	_, ok := pool.GetPendingTx(tx.Hash)
	assert.True(t, ok, "head event without OldChain should not drop unrelated pending tx")
}
