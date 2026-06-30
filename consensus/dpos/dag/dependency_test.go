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

	analyzer := NewDependencyAnalyzer(nil)
	deps := analyzer.DetectDependencies([]*types.Transaction{txA, txB})

	require.Contains(t, deps[txB], txA)
	require.Empty(t, deps[txA])
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
