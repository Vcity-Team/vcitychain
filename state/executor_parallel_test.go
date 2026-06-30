package state_test

import (
	"math/big"
	"sync"
	"testing"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/dag"
	"github.com/Vcity-Team/vcitychain/state"
	itrie "github.com/Vcity-Team/vcitychain/state/immutable-trie"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

var (
	parallelSenderA = types.StringToAddress("0x1000000000000000000000000000000000000001")
	parallelSenderB = types.StringToAddress("0x1000000000000000000000000000000000000002")
	parallelRecvC   = types.StringToAddress("0x2000000000000000000000000000000000000003")
	parallelRecvD   = types.StringToAddress("0x2000000000000000000000000000000000000004")
)

func newParallelTestExecutor(t *testing.T) (*state.Executor, types.Hash) {
	t.Helper()

	forks := &chain.Forks{}
	mchain := &chain.Chain{
		Params: &chain.Params{
			ChainID: 100,
			Forks:   forks,
		},
	}
	mstate := itrie.NewState(itrie.NewMemoryStorage())
	logger := hclog.NewNullLogger()
	executor := state.NewExecutor(mchain.Params, mstate, logger)
	state.SetupExecutorGetHash(executor)

	genesisAlloc := map[types.Address]*chain.GenesisAccount{
		parallelSenderA: {Balance: big.NewInt(1_000_000_000_000_000_000)},
		parallelSenderB: {Balance: big.NewInt(1_000_000_000_000_000_000)},
	}
	root, err := executor.WriteGenesis(genesisAlloc, types.ZeroHash)
	require.NoError(t, err)
	require.NotEqual(t, types.ZeroHash, root)

	return executor, root
}

func valueTransfer(from, to types.Address, nonce uint64, amount int64) *types.Transaction {
	return &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(amount),
		Gas:      state.TxGas,
		GasPrice: big.NewInt(1),
		Nonce:    nonce,
	}
}

func TestGroupTransactionsByAccount_sortsByNonce(t *testing.T) {
	t.Parallel()

	tx2 := valueTransfer(parallelSenderA, parallelRecvC, 2, 1)
	tx0 := valueTransfer(parallelSenderA, parallelRecvC, 0, 1)
	tx1 := valueTransfer(parallelSenderA, parallelRecvC, 1, 1)

	groups := state.GroupTransactionsByAccountForTest([]*types.Transaction{tx2, tx0, tx1})
	require.Len(t, groups, 1)
	require.Len(t, groups[parallelSenderA], 3)
	require.Equal(t, uint64(0), groups[parallelSenderA][0].Nonce)
	require.Equal(t, uint64(1), groups[parallelSenderA][1].Nonce)
	require.Equal(t, uint64(2), groups[parallelSenderA][2].Nonce)
}

func TestProcessBlock_parallelMultiAccount(t *testing.T) {
	t.Parallel()

	executor, parentRoot := newParallelTestExecutor(t)

	const transferAmount = int64(1_000_000_000_000_000)
	const gasCost = int64(state.TxGas) // gasPrice = 1
	txA := valueTransfer(parallelSenderA, parallelRecvC, 0, transferAmount)
	txB := valueTransfer(parallelSenderB, parallelRecvD, 0, transferAmount)

	block := &types.Block{
		Header: &types.Header{
			Number:   1,
			GasLimit: 30_000_000,
		},
		Transactions: []*types.Transaction{txA, txB},
	}

	transition, err := executor.ProcessBlock(parentRoot, block, types.ZeroAddress)
	require.NoError(t, err)
	require.Len(t, transition.Receipts(), 2)

	for _, r := range transition.Receipts() {
		require.NotNil(t, r.Status)
		require.Equal(t, types.ReceiptSuccess, *r.Status)
	}

	snap, _, err := transition.Commit()
	require.NoError(t, err)

	acctA, err := snap.GetAccount(parallelSenderA)
	require.NoError(t, err)
	acctB, err := snap.GetAccount(parallelSenderB)
	require.NoError(t, err)
	require.Equal(t, uint64(1), acctA.Nonce)
	require.Equal(t, uint64(1), acctB.Nonce)

	recvC, err := snap.GetAccount(parallelRecvC)
	require.NoError(t, err)
	recvD, err := snap.GetAccount(parallelRecvD)
	require.NoError(t, err)

	require.Equal(t, big.NewInt(1_000_000_000_000_000_000-transferAmount-gasCost), acctA.Balance)
	require.Equal(t, big.NewInt(1_000_000_000_000_000_000-transferAmount-gasCost), acctB.Balance)
	require.Equal(t, big.NewInt(transferAmount), recvC.Balance)
	require.Equal(t, big.NewInt(transferAmount), recvD.Balance)
}

func TestProcessBlock_parallelDeterministic(t *testing.T) {
	t.Parallel()

	executor, parentRoot := newParallelTestExecutor(t)

	txA := valueTransfer(parallelSenderA, parallelRecvC, 0, 1000)
	txB := valueTransfer(parallelSenderB, parallelRecvD, 0, 2000)
	block := &types.Block{
		Header:       &types.Header{Number: 1, GasLimit: 30_000_000},
		Transactions: []*types.Transaction{txA, txB},
	}

	run := func() types.Hash {
		transition, err := executor.ProcessBlock(parentRoot, block, types.ZeroAddress)
		require.NoError(t, err)
		_, root, err := transition.Commit()
		require.NoError(t, err)
		return root
	}

	require.Equal(t, run(), run())
}

func TestProcessBlock_singleAccountUsesSerialPath(t *testing.T) {
	t.Parallel()

	executor, parentRoot := newParallelTestExecutor(t)

	tx0 := valueTransfer(parallelSenderA, parallelRecvC, 0, 1000)
	tx1 := valueTransfer(parallelSenderA, parallelRecvD, 1, 1000)
	block := &types.Block{
		Header:       &types.Header{Number: 1, GasLimit: 30_000_000},
		Transactions: []*types.Transaction{tx0, tx1},
	}

	transition, err := executor.ProcessBlock(parentRoot, block, types.ZeroAddress)
	require.NoError(t, err)
	require.Len(t, transition.Receipts(), 2)
}

func TestWriteForAccount_concurrentDifferentAccounts(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*state.PreState{
		parallelSenderA: {Balance: 1_000_000, Nonce: 0},
		parallelSenderB: {Balance: 1_000_000, Nonce: 0},
		parallelRecvC:   {Balance: 0, Nonce: 0},
		parallelRecvD:   {Balance: 0, Nonce: 0},
	}
	transition := state.NewParallelTestTransition(preState)

	txA := valueTransfer(parallelSenderA, parallelRecvC, 0, 100)
	txB := valueTransfer(parallelSenderB, parallelRecvD, 0, 200)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, pair := range []struct {
		tx  *types.Transaction
		acc types.Address
	}{
		{txA, parallelSenderA},
		{txB, parallelSenderB},
	} {
		wg.Add(1)
		go func(tx *types.Transaction, acc types.Address) {
			defer wg.Done()
			if err := transition.WriteForAccount(tx, acc); err != nil {
				errCh <- err
			}
		}(pair.tx, pair.acc)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	require.Equal(t, uint64(1), transition.AccountNonceForTest(parallelSenderA))
	require.Equal(t, uint64(1), transition.AccountNonceForTest(parallelSenderB))
	require.Equal(t, big.NewInt(100), transition.AccountBalanceForTest(parallelRecvC))
	require.Equal(t, big.NewInt(200), transition.AccountBalanceForTest(parallelRecvD))
}

func TestDAGExecutor_executeIndependentAccounts(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*state.PreState{
		parallelSenderA: {Balance: 1_000_000, Nonce: 0},
		parallelSenderB: {Balance: 1_000_000, Nonce: 0},
		parallelRecvC:   {Balance: 0, Nonce: 0},
		parallelRecvD:   {Balance: 0, Nonce: 0},
	}
	transition := state.NewParallelTestTransition(preState)
	logger := hclog.NewNullLogger()

	txA := valueTransfer(parallelSenderA, parallelRecvC, 0, 100)
	txB := valueTransfer(parallelSenderB, parallelRecvD, 0, 200)
	txs := []*types.Transaction{txA, txB}

	analyzer := dag.NewDependencyAnalyzer(logger)
	deps := analyzer.DetectDependencies(txs)
	require.False(t, analyzer.HasDependencies(deps))

	d, err := dag.BuildDAG(txs, deps)
	require.NoError(t, err)

	executor := dag.NewDAGExecutor(transition, logger)
	require.NoError(t, executor.ExecuteDAG(d))

	require.Equal(t, uint64(1), transition.AccountNonceForTest(parallelSenderA))
	require.Equal(t, uint64(1), transition.AccountNonceForTest(parallelSenderB))
	require.Equal(t, big.NewInt(100), transition.AccountBalanceForTest(parallelRecvC))
	require.Equal(t, big.NewInt(200), transition.AccountBalanceForTest(parallelRecvD))
}

func TestDAGExecutor_executeNonceChainSerialLevels(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*state.PreState{
		parallelSenderA: {Balance: 1_000_000, Nonce: 0},
		parallelRecvC:   {Balance: 0, Nonce: 0},
		parallelRecvD:   {Balance: 0, Nonce: 0},
	}
	transition := state.NewParallelTestTransition(preState)
	logger := hclog.NewNullLogger()

	tx0 := valueTransfer(parallelSenderA, parallelRecvC, 0, 100)
	tx1 := valueTransfer(parallelSenderA, parallelRecvD, 1, 50)
	txs := []*types.Transaction{tx0, tx1}

	analyzer := dag.NewDependencyAnalyzer(logger)
	deps := analyzer.DetectDependencies(txs)
	require.True(t, analyzer.HasDependencies(deps))

	d, err := dag.BuildDAG(txs, deps)
	require.NoError(t, err)
	require.Equal(t, 1, d.GetMaxLevel())

	executor := dag.NewDAGExecutor(transition, logger)
	require.NoError(t, executor.ExecuteDAG(d))

	require.Equal(t, uint64(2), transition.AccountNonceForTest(parallelSenderA))
	require.Equal(t, big.NewInt(100), transition.AccountBalanceForTest(parallelRecvC))
	require.Equal(t, big.NewInt(50), transition.AccountBalanceForTest(parallelRecvD))
}

func TestDAGExecutor_executeMatchesProcessBlockStateRoot(t *testing.T) {
	t.Parallel()

	executor, parentRoot := newParallelTestExecutor(t)
	txA := valueTransfer(parallelSenderA, parallelRecvC, 0, 1000)
	txB := valueTransfer(parallelSenderB, parallelRecvD, 0, 2000)
	block := &types.Block{
		Header:       &types.Header{Number: 1, GasLimit: 30_000_000},
		Transactions: []*types.Transaction{txA, txB},
	}

	processBlockRoot := func() types.Hash {
		transition, err := executor.ProcessBlock(parentRoot, block, types.ZeroAddress)
		require.NoError(t, err)
		_, root, err := transition.Commit()
		require.NoError(t, err)
		return root
	}

	dagRoot := func() types.Hash {
		transition, err := executor.BeginTxn(parentRoot, block.Header, types.ZeroAddress)
		require.NoError(t, err)

		analyzer := dag.NewDependencyAnalyzer(hclog.NewNullLogger())
		deps := analyzer.DetectDependencies(block.Transactions)
		d, err := dag.BuildDAG(block.Transactions, deps)
		require.NoError(t, err)
		require.NoError(t, dag.NewDAGExecutor(transition, hclog.NewNullLogger()).ExecuteDAG(d))

		_, root, err := transition.Commit()
		require.NoError(t, err)
		return root
	}

	require.Equal(t, processBlockRoot(), dagRoot())
}
