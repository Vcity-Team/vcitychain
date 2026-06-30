package state_test

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/dag"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

var (
	// simpleStoreRuntime writes 1 to storage slot 0 on each call.
	simpleStoreRuntime = []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
	parallelContract   = types.StringToAddress("0x00000000000000000000000000000000cc01")
)

func newExecutorWithContract(t *testing.T) (*state.Executor, types.Hash) {
	t.Helper()

	return newParallelTestExecutorWithGenesis(t, map[types.Address]*chain.GenesisAccount{
		parallelSenderA: {Balance: big.NewInt(1_000_000_000_000_000_000)},
		parallelSenderB: {Balance: big.NewInt(1_000_000_000_000_000_000)},
		parallelContract: {
			Balance: big.NewInt(0),
			Code:    simpleStoreRuntime,
		},
	})
}

func contractCall(from types.Address, nonce uint64) *types.Transaction {
	return &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &parallelContract,
		Gas:      100_000,
		GasPrice: big.NewInt(1),
		Nonce:    nonce,
		Value:    big.NewInt(0),
		Input:    []byte{0x01},
	}
}

func TestDAGExecutor_sameContractConservativeOrdering(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*state.PreState{
		parallelSenderA: {Balance: 1_000_000_000_000_000_000, Nonce: 0},
		parallelSenderB: {Balance: 1_000_000_000_000_000_000, Nonce: 0},
		parallelContract: {
			Balance: 0,
			State:   map[types.Hash]types.Hash{},
		},
	}
	transition := state.NewParallelTestTransition(preState)
	transition.SetCodeForTest(parallelContract, simpleStoreRuntime)

	txA := contractCall(parallelSenderA, 0)
	txB := contractCall(parallelSenderB, 0)
	txs := []*types.Transaction{txA, txB}

	logger := hclog.NewNullLogger()
	analyzer := dag.NewDependencyAnalyzer(logger)
	deps := analyzer.DetectDependencies(txs)
	require.Contains(t, deps[txB], txA)
	require.True(t, analyzer.HasDependencies(deps))

	d, err := dag.BuildDAG(txs, deps)
	require.NoError(t, err)
	require.Equal(t, 1, d.GetMaxLevel())

	require.NoError(t, dag.NewDAGExecutor(transition, logger).ExecuteDAG(d))
	require.Equal(t, uint64(1), transition.AccountNonceForTest(parallelSenderA))
	require.Equal(t, uint64(1), transition.AccountNonceForTest(parallelSenderB))
}

func TestDAGExecutor_sameContractMatchesSerialWrite(t *testing.T) {
	t.Parallel()

	executor, parentRoot := newExecutorWithContract(t)
	txA := contractCall(parallelSenderA, 0)
	txB := contractCall(parallelSenderB, 0)
	block := &types.Block{
		Header:       &types.Header{Number: 1, GasLimit: 30_000_000},
		Transactions: []*types.Transaction{txA, txB},
	}

	serialRoot := func() types.Hash {
		transition, err := executor.BeginTxn(parentRoot, block.Header, types.ZeroAddress)
		require.NoError(t, err)
		for _, tx := range block.Transactions {
			require.NoError(t, transition.Write(tx))
		}
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

	require.Equal(t, serialRoot(), dagRoot())
}

func TestProcessBlock_twoExecutorsSameStateRoot(t *testing.T) {
	t.Parallel()

	const amount = int64(1_000_000_000_000_000)
	txA := valueTransfer(parallelSenderA, parallelRecvC, 0, amount)
	txB := valueTransfer(parallelSenderB, parallelRecvD, 0, amount)
	block := &types.Block{
		Header:       &types.Header{Number: 1, GasLimit: 30_000_000},
		Transactions: []*types.Transaction{txA, txB},
	}

	run := func() types.Hash {
		executor, parentRoot := newParallelTestExecutor(t)
		transition, err := executor.ProcessBlock(parentRoot, block, types.ZeroAddress)
		require.NoError(t, err)
		_, root, err := transition.Commit()
		require.NoError(t, err)
		return root
	}

	require.Equal(t, run(), run())
}
