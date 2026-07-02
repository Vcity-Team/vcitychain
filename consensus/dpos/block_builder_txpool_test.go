package dpos

import (
	"math/big"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/txpool"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// fromAddressSigner returns tx.From for BlockBuilder + txpool tests.
type fromAddressSigner struct{}

func (fromAddressSigner) Sender(tx *types.Transaction) (types.Address, error) {
	return tx.From, nil
}

func newRealTxPool(t *testing.T, executor *state.Executor, parent *types.Header) (*txpool.TxPool, *txpool.ExecutorStore) {
	t.Helper()

	store := txpool.NewExecutorStore(executor, parent)
	pool, err := txpool.NewTestPoolWithExecutorStore(hclog.NewNullLogger(), store, nil)
	require.NoError(t, err)

	pool.SetSigner(fromAddressSigner{})
	pool.SetSealing(true)
	pool.Start()
	t.Cleanup(func() { pool.Close() })

	return pool, store
}

func addTxToPool(t *testing.T, pool *txpool.TxPool, tx *types.Transaction) {
	t.Helper()
	require.NoError(t, pool.AddTx(tx))
}

func newDAGBlockBuilder(t *testing.T, executor *state.Executor, parent *types.Header, pool txPoolInterface) *BlockBuilder {
	t.Helper()

	builder := NewBlockBuilder(&BlockBuilderParams{
		BlockTime:          time.Second,
		Parent:             parent,
		Coinbase:           types.ZeroAddress,
		Executor:           executor,
		GasLimit:           parent.GasLimit,
		TxPool:             pool,
		Logger:             hclog.NewNullLogger(),
		EnableDAGExecution: true,
	})
	bb := builder.(*BlockBuilder)
	require.True(t, bb.enableDAGExecution)
	return bb
}

func TestBlockBuilder_FillWithRealTxPool_multiAccount(t *testing.T) {
	senderA := testWalletAccount(t)
	senderB := testWalletAccount(t)
	senderC := testWalletAccount(t)
	recv1 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv2 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv3 := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{senderA, senderB, senderC})
	pool, _ := newRealTxPool(t, executor, parent)

	txA := signedTransfer(t, signer, senderA, recv1, 1000)
	txB := signedTransfer(t, signer, senderB, recv2, 2000)
	txC := signedTransfer(t, signer, senderC, recv3, 3000)
	for _, tx := range []*types.Transaction{txA, txB, txC} {
		addTxToPool(t, pool, tx)
	}

	bb := newDAGBlockBuilder(t, executor, parent, pool)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 3)
	require.Len(t, bb.Receipts(), 3)
	for _, r := range bb.Receipts() {
		require.NotNil(t, r.Status)
		require.Equal(t, types.ReceiptSuccess, *r.Status)
	}
}

func TestBlockBuilder_FillWithRealTxPool_nonceHoleSkipsGap(t *testing.T) {
	sender := testWalletAccount(t)
	recv := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{sender})
	pool, _ := newRealTxPool(t, executor, parent)

	tx0 := signedTransferWithNonce(t, signer, sender, recv, 0, 1000)
	tx2 := signedTransferWithNonce(t, signer, sender, recv, 2, 2000)
	addTxToPool(t, pool, tx0)
	addTxToPool(t, pool, tx2)

	bb := newDAGBlockBuilder(t, executor, parent, pool)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 1)
	require.Equal(t, uint64(0), bb.txns[0].Nonce)
	require.Equal(t, types.ReceiptSuccess, *bb.Receipts()[0].Status)
}

func TestBlockBuilder_FillWithRealTxPool_txReplacement(t *testing.T) {
	sender := testWalletAccount(t)
	recv := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{sender})
	pool, _ := newRealTxPool(t, executor, parent)

	fromAddr := types.Address(sender.Ecdsa.Address())
	pk, err := sender.GetEcdsaPrivateKey()
	require.NoError(t, err)

	low := &types.Transaction{
		Value:    big.NewInt(1000),
		GasPrice: big.NewInt(1),
		Gas:      state.TxGas,
		Nonce:    0,
		To:       &recv,
		From:     fromAddr,
	}
	low, err = signer.SignTx(low, pk)
	require.NoError(t, err)
	low.From = fromAddr

	high := &types.Transaction{
		Value:    big.NewInt(5000),
		GasPrice: big.NewInt(10),
		Gas:      state.TxGas,
		Nonce:    0,
		To:       &recv,
		From:     fromAddr,
	}
	high, err = signer.SignTx(high, pk)
	require.NoError(t, err)
	high.From = fromAddr

	addTxToPool(t, pool, high)
	require.ErrorIs(t, pool.AddTx(low), txpool.ErrReplacementUnderpriced)

	bb := newDAGBlockBuilder(t, executor, parent, pool)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 1)
	require.Equal(t, big.NewInt(5000), bb.txns[0].Value)
}

func TestBlockBuilder_FillWithRealTxPool_afterReorgReinject(t *testing.T) {
	sender := testWalletAccount(t)
	recv := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{sender})
	tx := signedTransfer(t, signer, sender, recv, 4000)

	store := txpool.NewExecutorStore(executor, parent)
	forkHash := types.Hash{0xAA}
	canonicalHash := types.Hash{0xBB}
	forkBlock := &types.Block{
		Header: &types.Header{
			Number:    parent.Number + 1,
			Hash:      forkHash,
			StateRoot: parent.StateRoot,
			GasLimit:  parent.GasLimit,
		},
		Transactions: []*types.Transaction{tx},
	}
	canonicalBlock := &types.Block{
		Header: &types.Header{
			Number:    parent.Number + 1,
			Hash:      canonicalHash,
			StateRoot: parent.StateRoot,
			GasLimit:  parent.GasLimit,
		},
	}
	tx.ComputeHash(forkBlock.Header.Number)
	store.SetBlocks(map[types.Hash]*types.Block{
		forkHash:     forkBlock,
		canonicalHash: canonicalBlock,
	})

	pool, err := txpool.NewTestPoolWithExecutorStore(hclog.NewNullLogger(), store, nil)
	require.NoError(t, err)
	pool.SetSigner(fromAddressSigner{})
	pool.SetSealing(true)
	pool.Start()
	t.Cleanup(func() { pool.Close() })

	addTxToPool(t, pool, tx)

	pool.ResetWithEvent(&blockchain.Event{
		NewChain: []*types.Header{forkBlock.Header},
	})
	_, ok := pool.GetPendingTx(tx.Hash)
	require.False(t, ok, "tx should leave pool when fork block is canonical")

	pool.ResetWithEvent(&blockchain.Event{
		Type:     blockchain.EventReorg,
		OldChain: []*types.Header{forkBlock.Header},
		NewChain: []*types.Header{canonicalBlock.Header},
	})
	_, ok = pool.GetPendingTx(tx.Hash)
	require.True(t, ok, "discarded fork tx should be re-injected after reorg")

	bb := newDAGBlockBuilder(t, executor, parent, pool)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 1)
	require.Equal(t, tx.Hash, bb.txns[0].Hash)
	require.Equal(t, types.ReceiptSuccess, *bb.Receipts()[0].Status)
}

func TestBlockBuilder_FillWithRealTxPool_singleAccountNonceChain(t *testing.T) {
	sender := testWalletAccount(t)
	recv1 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv2 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv3 := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{sender})
	pool, _ := newRealTxPool(t, executor, parent)

	for i, recv := range []types.Address{recv1, recv2, recv3} {
		addTxToPool(t, pool, signedTransferWithNonce(t, signer, sender, recv, uint64(i), int64(1000*(i+1))))
	}

	bb := newDAGBlockBuilder(t, executor, parent, pool)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 3)
	for i, r := range bb.Receipts() {
		require.Equal(t, types.ReceiptSuccess, *r.Status, "tx index %d", i)
	}
}

func TestBlockBuilder_realTxPoolMatchesSerialFillStateRoot(t *testing.T) {
	senderA := testWalletAccount(t)
	senderB := testWalletAccount(t)
	recv1 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv2 := types.Address(testWalletAccount(t).Ecdsa.Address())

	buildRoot := func(enableDAG bool) types.Hash {
		executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{senderA, senderB})
		pool, _ := newRealTxPool(t, executor, parent)

		txA := signedTransfer(t, signer, senderA, recv1, 1000)
		txB := signedTransfer(t, signer, senderB, recv2, 2000)
		addTxToPool(t, pool, txA)
		addTxToPool(t, pool, txB)

		bb := newDAGBlockBuilder(t, executor, parent, pool)
		bb.enableDAGExecution = enableDAG
		require.NoError(t, bb.Reset())
		require.NoError(t, bb.Fill())
		fb, err := bb.Build(nil)
		require.NoError(t, err)
		return fb.Block.Header.StateRoot
	}

	require.Equal(t, buildRoot(false), buildRoot(true))
}
