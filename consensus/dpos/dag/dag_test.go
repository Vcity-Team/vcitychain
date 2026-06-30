package dag

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/require"
)

func TestBuildDAG_levelsFromNonceChain(t *testing.T) {
	t.Parallel()

	a := addr(0x01)
	tx0 := tx(a, 0, nil)
	tx1 := tx(a, 1, nil)

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{tx0, tx1})

	d, err := BuildDAG([]*types.Transaction{tx0, tx1}, deps)
	require.NoError(t, err)
	require.Equal(t, 1, d.GetMaxLevel())
	require.Len(t, d.GetNodesAtLevel(0), 1)
	require.Len(t, d.GetNodesAtLevel(1), 1)
	require.Equal(t, tx0, d.GetNodesAtLevel(0)[0].Tx)
	require.Equal(t, tx1, d.GetNodesAtLevel(1)[0].Tx)
}

func TestBuildDAG_independentAccountsSingleLevel(t *testing.T) {
	t.Parallel()

	txA := tx(addr(0x01), 0, addrPtr(0x99))
	txB := tx(addr(0x02), 0, addrPtr(0x98))

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{txA, txB})

	d, err := BuildDAG([]*types.Transaction{txA, txB}, deps)
	require.NoError(t, err)
	require.Equal(t, 0, d.GetMaxLevel())
	require.Len(t, d.GetNodesAtLevel(0), 2)
}

func TestBuildDAG_missingDependencyTx(t *testing.T) {
	t.Parallel()

	tx0 := tx(addr(0x01), 0, nil)
	tx1 := tx(addr(0x01), 1, nil)
	deps := DependencyMap{
		tx1: {tx0},
	}

	_, err := BuildDAG([]*types.Transaction{tx1}, deps)
	require.Error(t, err)
}

func TestBuildDAG_cycleRejected(t *testing.T) {
	t.Parallel()

	tx0 := tx(addr(0x01), 0, nil)
	tx1 := tx(addr(0x02), 0, nil)
	deps := DependencyMap{
		tx0: {tx1},
		tx1: {tx0},
	}

	_, err := BuildDAG([]*types.Transaction{tx0, tx1}, deps)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle")
}

func TestTransactionNode_executedFlag(t *testing.T) {
	t.Parallel()

	node := &TransactionNode{Tx: tx(addr(0x01), 0, nil)}
	require.False(t, node.IsExecuted())
	node.MarkExecuted()
	require.True(t, node.IsExecuted())
}
