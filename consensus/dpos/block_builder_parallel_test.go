package dpos

import (
	"math/big"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/state"
	itrie "github.com/Vcity-Team/vcitychain/state/immutable-trie"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/umbracle/ethgo"
)

// stubTxPool feeds a fixed queue for BlockBuilder.Fill / fillWithDAG tests.
type stubTxPool struct {
	txs    []*types.Transaction
	pos    int
	pops   []*types.Transaction
	popped map[*types.Transaction]bool
}

func newStubTxPool(txs ...*types.Transaction) *stubTxPool {
	return &stubTxPool{
		txs:    append([]*types.Transaction(nil), txs...),
		popped: make(map[*types.Transaction]bool),
	}
}

func (s *stubTxPool) Prepare() {
	s.pos = 0
}

func (s *stubTxPool) Length() uint64 {
	var n uint64
	for _, tx := range s.txs {
		if !s.popped[tx] {
			n++
		}
	}
	return n
}

func (s *stubTxPool) Peek() *types.Transaction {
	for s.pos < len(s.txs) {
		tx := s.txs[s.pos]
		s.pos++
		if !s.popped[tx] {
			return tx
		}
	}
	return nil
}

func (s *stubTxPool) Pop(tx *types.Transaction) {
	s.pops = append(s.pops, tx)
	s.popped[tx] = true
}

func (s *stubTxPool) Drop(*types.Transaction) {}

func (s *stubTxPool) Demote(*types.Transaction) {}

func (s *stubTxPool) SetSealing(bool) {}

func (s *stubTxPool) ResetWithHeaders(...*types.Header) {}

func (s *stubTxPool) ResetWithEvent(*blockchain.Event) {}

func testWalletAccount(t *testing.T) *wallet.Account {
	t.Helper()
	acc, err := wallet.GenerateAccount()
	require.NoError(t, err)
	return acc
}

func setupParallelBlockBuilderTest(t *testing.T, fundAccounts []*wallet.Account) (*state.Executor, *types.Header, crypto.TxSigner) {
	t.Helper()

	const chainID = 100
	forks := &chain.Forks{}
	logger := hclog.NewNullLogger()
	signer := crypto.NewSigner(forks.At(0), chainID)

	mchain := &chain.Chain{
		Params: &chain.Params{
			ChainID: chainID,
			Forks:   forks,
		},
	}
	mstate := itrie.NewState(itrie.NewMemoryStorage())
	executor := state.NewExecutor(mchain.Params, mstate, logger)
	executor.GetHash = func(header *types.Header) func(i uint64) types.Hash {
		return func(i uint64) types.Hash {
			return types.BytesToHash(common.EncodeUint64ToBytes(i))
		}
	}

	alloc := make(map[types.Address]*chain.GenesisAccount, len(fundAccounts))
	for _, acc := range fundAccounts {
		addr := types.Address(acc.Ecdsa.Address())
		alloc[addr] = &chain.GenesisAccount{Balance: ethgo.Ether(1)}
	}

	root, err := executor.WriteGenesis(alloc, types.ZeroHash)
	require.NoError(t, err)

	parent := &types.Header{
		Number:    0,
		StateRoot: root,
		GasLimit:  30_000_000,
		Timestamp: uint64(time.Now().Unix()),
	}
	parent.ComputeHash()

	return executor, parent, signer
}

func signedTransfer(t *testing.T, signer crypto.TxSigner, from *wallet.Account, to types.Address, amount int64) *types.Transaction {
	return signedTransferWithNonce(t, signer, from, to, 0, amount)
}

func signedTransferWithNonce(t *testing.T, signer crypto.TxSigner, from *wallet.Account, to types.Address, nonce uint64, amount int64) *types.Transaction {
	t.Helper()

	pk, err := from.GetEcdsaPrivateKey()
	require.NoError(t, err)

	fromAddr := types.Address(from.Ecdsa.Address())
	tx := &types.Transaction{
		Value:    big.NewInt(amount),
		GasPrice: big.NewInt(1),
		Gas:      state.TxGas,
		Nonce:    nonce,
		To:       &to,
		From:     fromAddr,
	}
	tx, err = signer.SignTx(tx, pk)
	require.NoError(t, err)
	tx.From = fromAddr
	return tx
}

func TestBlockBuilder_FillWithDAG_multiAccountParallel(t *testing.T) {

	senderA := testWalletAccount(t)
	senderB := testWalletAccount(t)
	recv1 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv2 := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{senderA, senderB})

	txA := signedTransfer(t, signer, senderA, recv1, 1000)
	txB := signedTransfer(t, signer, senderB, recv2, 2000)

	pool := newStubTxPool(txA, txB)
	logger := hclog.NewNullLogger()

	builder := NewBlockBuilder(&BlockBuilderParams{
		BlockTime: time.Second,
		Parent:    parent,
		Coinbase:  types.ZeroAddress,
		Executor:  executor,
		GasLimit:  parent.GasLimit,
		TxPool:    pool,
		Logger:    logger,
	})

	bb, ok := builder.(*BlockBuilder)
	require.True(t, ok)
	require.True(t, bb.enableDAGExecution)

	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 2)
	require.Len(t, pool.pops, 2)
	require.Len(t, bb.Receipts(), 2)
	for _, r := range bb.Receipts() {
		require.NotNil(t, r.Status)
		require.Equal(t, types.ReceiptSuccess, *r.Status)
	}
}

func TestBlockBuilder_FillWithDAG_sameRecipientUsesDAGLevels(t *testing.T) {

	senderA := testWalletAccount(t)
	senderB := testWalletAccount(t)
	sharedRecv := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{senderA, senderB})

	txA := signedTransfer(t, signer, senderA, sharedRecv, 1000)
	txB := signedTransfer(t, signer, senderB, sharedRecv, 2000)

	pool := newStubTxPool(txA, txB)
	logger := hclog.NewNullLogger()

	builder := NewBlockBuilder(&BlockBuilderParams{
		BlockTime: time.Second,
		Parent:    parent,
		Coinbase:  types.ZeroAddress,
		Executor:  executor,
		GasLimit:  parent.GasLimit,
		TxPool:    pool,
		Logger:    logger,
	})

	bb := builder.(*BlockBuilder)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 2)
	require.Len(t, bb.Receipts(), 2)
}

func TestBlockBuilder_FillWithDAG_fallsBackToSerialOnSingleTx(t *testing.T) {

	sender := testWalletAccount(t)
	recv := types.Address(testWalletAccount(t).Ecdsa.Address())

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{sender})
	tx := signedTransfer(t, signer, sender, recv, 1000)

	pool := newStubTxPool(tx)
	logger := hclog.NewNullLogger()

	builder := NewBlockBuilder(&BlockBuilderParams{
		BlockTime: time.Second,
		Parent:    parent,
		Coinbase:  types.ZeroAddress,
		Executor:  executor,
		GasLimit:  parent.GasLimit,
		TxPool:    pool,
		Logger:    logger,
	})

	bb := builder.(*BlockBuilder)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 1)
	require.Len(t, bb.Receipts(), 1)
}

func TestBlockBuilder_parallelFillMatchesSerialFillStateRoot(t *testing.T) {
	senderA := testWalletAccount(t)
	senderB := testWalletAccount(t)
	recv1 := types.Address(testWalletAccount(t).Ecdsa.Address())
	recv2 := types.Address(testWalletAccount(t).Ecdsa.Address())

	executorDAG, parentDAG, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{senderA, senderB})
	executorSerial, parentSerial, _ := setupParallelBlockBuilderTest(t, []*wallet.Account{senderA, senderB})

	txA := signedTransfer(t, signer, senderA, recv1, 1000)
	txB := signedTransfer(t, signer, senderB, recv2, 2000)
	logger := hclog.NewNullLogger()

	buildRoot := func(executor *state.Executor, parent *types.Header, enableDAG bool) types.Hash {
		pool := newStubTxPool(txA, txB)
		builder := NewBlockBuilder(&BlockBuilderParams{
			BlockTime: time.Second,
			Parent:    parent,
			Coinbase:  types.ZeroAddress,
			Executor:  executor,
			GasLimit:  parent.GasLimit,
			TxPool:    pool,
			Logger:    logger,
		})
		bb := builder.(*BlockBuilder)
		bb.enableDAGExecution = enableDAG
		require.NoError(t, bb.Reset())
		require.NoError(t, bb.Fill())
		fb, err := bb.Build(nil)
		require.NoError(t, err)
		return fb.Block.Header.StateRoot
	}

	require.Equal(t,
		buildRoot(executorSerial, parentSerial, false),
		buildRoot(executorDAG, parentDAG, true),
	)
}

func TestBlockBuilder_FillWithDAG_transferChainABC(t *testing.T) {
	// A -> B -> C in one block: B is unfunded until A's transfer lands.
	walletA := testWalletAccount(t)
	walletB := testWalletAccount(t)
	finalRecv := types.Address(testWalletAccount(t).Ecdsa.Address())

	const (
		amountAB = int64(800_000_000_000_000)
		amountBC = int64(300_000_000_000_000)
	)

	executor, parent, signer := setupParallelBlockBuilderTest(t, []*wallet.Account{walletA})
	addrB := types.Address(walletB.Ecdsa.Address())

	txAB := signedTransfer(t, signer, walletA, addrB, amountAB)
	txBC := signedTransferWithNonce(t, signer, walletB, finalRecv, 0, amountBC)

	pool := newStubTxPool(txAB, txBC)
	logger := hclog.NewNullLogger()

	builder := NewBlockBuilder(&BlockBuilderParams{
		BlockTime: time.Second,
		Parent:    parent,
		Coinbase:  types.ZeroAddress,
		Executor:  executor,
		GasLimit:  parent.GasLimit,
		TxPool:    pool,
		Logger:    logger,
	})

	bb := builder.(*BlockBuilder)
	require.NoError(t, bb.Reset())
	require.NoError(t, bb.Fill())

	require.Len(t, bb.txns, 2)
	require.Len(t, bb.Receipts(), 2)
	for _, r := range bb.Receipts() {
		require.NotNil(t, r.Status)
		require.Equal(t, types.ReceiptSuccess, *r.Status)
	}

	fb, err := bb.Build(nil)
	require.NoError(t, err)
	require.NotEqual(t, types.ZeroHash, fb.Block.Header.StateRoot)
}
