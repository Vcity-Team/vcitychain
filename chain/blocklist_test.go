package chain

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestParams_IsTransactionsBlockListActive(t *testing.T) {
	t.Parallel()

	admin := types.StringToAddress("0xabc")

	t.Run("disabled when config nil", func(t *testing.T) {
		t.Parallel()
		p := &Params{}
		require.False(t, p.IsTransactionsBlockListActive(0))
	})

	t.Run("allow list takes precedence", func(t *testing.T) {
		t.Parallel()
		p := &Params{
			TransactionsBlockList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
			TransactionsAllowList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
		}
		require.False(t, p.IsTransactionsBlockListActive(0))
	})

	t.Run("config without fork is always active", func(t *testing.T) {
		t.Parallel()
		p := &Params{
			TransactionsBlockList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
			Forks:                 &Forks{London: NewFork(0)},
		}
		require.True(t, p.IsTransactionsBlockListActive(0))
		require.True(t, p.IsTransactionsBlockListActive(100))
	})

	t.Run("fork gates activation", func(t *testing.T) {
		t.Parallel()
		p := &Params{
			TransactionsBlockList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
			Forks: &Forks{
				TransactionsBlockList: NewFork(10),
			},
		}
		require.False(t, p.IsTransactionsBlockListActive(9))
		require.True(t, p.IsTransactionsBlockListActive(10))
		require.True(t, p.IsTransactionsBlockListActive(11))
	})
}

func TestParams_ShouldApplyTransactionsBlockListGenesisAllocs(t *testing.T) {
	t.Parallel()

	admin := types.StringToAddress("0xabc")

	t.Run("false when config nil", func(t *testing.T) {
		t.Parallel()
		require.False(t, (&Params{}).ShouldApplyTransactionsBlockListGenesisAllocs())
	})

	t.Run("true when no fork key", func(t *testing.T) {
		t.Parallel()
		p := &Params{
			TransactionsBlockList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
			Forks:                 &Forks{London: NewFork(0)},
		}
		require.True(t, p.ShouldApplyTransactionsBlockListGenesisAllocs())
	})

	t.Run("true when fork at genesis", func(t *testing.T) {
		t.Parallel()
		p := &Params{
			TransactionsBlockList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
			Forks: &Forks{
				TransactionsBlockList: NewFork(0),
			},
		}
		require.True(t, p.ShouldApplyTransactionsBlockListGenesisAllocs())
	})

	t.Run("false when fork height greater than zero", func(t *testing.T) {
		t.Parallel()
		p := &Params{
			TransactionsBlockList: &AddressListConfig{AdminAddresses: []types.Address{admin}},
			Forks: &Forks{
				TransactionsBlockList: NewFork(1000),
			},
		}
		require.False(t, p.ShouldApplyTransactionsBlockListGenesisAllocs())
	})
}
