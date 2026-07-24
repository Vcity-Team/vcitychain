package dag

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/require"
)

func addr(b byte) types.Address {
	return types.Address{b}
}

func addrPtr(b byte) *types.Address {
	a := addr(b)
	return &a
}

func tx(from types.Address, nonce uint64, to *types.Address) *types.Transaction {
	return &types.Transaction{
		From:  from,
		Nonce: nonce,
		To:    to,
	}
}

func TestDetectDependencies_sameAccountNonceOrder(t *testing.T) {
	t.Parallel()

	a := addr(0x01)
	tx0 := tx(a, 0, nil)
	tx1 := tx(a, 1, nil)
	other := tx(addr(0x02), 0, nil)

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{tx0, tx1, other})

	require.Len(t, deps[tx1], 1)
	require.Equal(t, tx0, deps[tx1][0])
	require.Empty(t, deps[tx0])
	require.Empty(t, deps[other])
}

func TestDetectDependencies_transferChain(t *testing.T) {
	t.Parallel()

	a, b, c := addr(0x01), addr(0x02), addr(0x03)
	txAB := tx(a, 0, &b)
	txBC := tx(b, 0, &c)

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{txAB, txBC})

	require.Contains(t, deps[txBC], txAB)
}

func TestDetectDependencies_sameContractConservative(t *testing.T) {
	t.Parallel()

	contract := addr(0xcc)
	senderA, senderB := addr(0x0a), addr(0x0b)
	txA := tx(senderA, 0, &contract)
	txB := tx(senderB, 0, &contract)
	txA.Input = []byte{0x01}
	txB.Input = []byte{0x02}

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{txA, txB})

	require.Contains(t, deps[txB], txA)
	require.Empty(t, deps[txA])
}

func TestDetectDependencies_sameToPureTransfer_relaxedSkips(t *testing.T) {
	t.Parallel()

	recipient := addr(0xcc)
	txA := tx(addr(0x0a), 0, &recipient)
	txB := tx(addr(0x0b), 0, &recipient)

	strict := NewDependencyAnalyzerWithMode(SameToModeStrict, nil)
	relaxedDeps, relaxedStats := NewDependencyAnalyzerWithMode(SameToModeRelaxed, nil).
		DetectDependenciesWithStats([]*types.Transaction{txA, txB})

	strictDeps := strict.DetectDependencies([]*types.Transaction{txA, txB})
	require.Contains(t, strictDeps[txB], txA)
	require.Empty(t, relaxedDeps[txB])
	require.Equal(t, 0, relaxedStats.SameToEdges)
	require.Equal(t, string(SameToModeRelaxed), relaxedStats.Mode)
}

func TestDetectDependencies_sameContractCall_relaxedKeeps(t *testing.T) {
	t.Parallel()

	contract := addr(0xcc)
	txA := tx(addr(0x0a), 0, &contract)
	txB := tx(addr(0x0b), 0, &contract)
	txA.Input = []byte{0xaa}
	txB.Input = []byte{0xbb}

	deps, stats := NewDependencyAnalyzerWithMode(SameToModeRelaxed, nil).
		DetectDependenciesWithStats([]*types.Transaction{txA, txB})

	require.Contains(t, deps[txB], txA)
	require.Equal(t, 1, stats.SameToEdges)
}

func TestParseSameToMode(t *testing.T) {
	t.Parallel()
	require.Equal(t, SameToModeStrict, ParseSameToMode(""))
	require.Equal(t, SameToModeStrict, ParseSameToMode("legacy"))
	require.Equal(t, SameToModeStrict, ParseSameToMode("conservative"))
	require.Equal(t, SameToModeStrict, ParseSameToMode("strict"))
	require.Equal(t, SameToModeRelaxed, ParseSameToMode("relaxed"))
	require.Equal(t, SameToModeRelaxed, ParseSameToMode("balanced"))
	require.Equal(t, SameToModeAggressive, ParseSameToMode("AGGRESSIVE"))
	require.Equal(t, SameToModeStrict, ParseSameToMode("unknown"))
}

func TestHasDependencies_independentAccounts(t *testing.T) {
	t.Parallel()

	txA := tx(addr(0x01), 0, addrPtr(0x99))
	txB := tx(addr(0x02), 0, addrPtr(0x98))

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{txA, txB})

	require.False(t, analyzer.HasDependencies(deps))
}

func TestHasDependencies_withDependency(t *testing.T) {
	t.Parallel()

	a := addr(0x01)
	tx0 := tx(a, 0, nil)
	tx1 := tx(a, 1, nil)

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{tx0, tx1})

	require.True(t, analyzer.HasDependencies(deps))
}
