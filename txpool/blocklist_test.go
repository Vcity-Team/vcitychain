package txpool

import (
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/state/runtime/addresslist"
	"github.com/Vcity-Team/vcitychain/types"
)

type blockListMockStore struct {
	defaultMockStore
	roles map[types.Address]addresslist.Role
}

func (m blockListMockStore) GetStorage(_ types.Hash, addr types.Address, slot types.Hash) (types.Hash, error) {
	if addr != contracts.BlockListTransactionsAddr {
		return types.ZeroHash, nil
	}

	account := types.BytesToAddress(slot.Bytes())
	if role, ok := m.roles[account]; ok {
		return types.Hash(role), nil
	}

	return types.ZeroHash, nil
}

func newBlockListTestPool(
	t *testing.T,
	store store,
	forkBlock uint64,
) *TxPool {
	t.Helper()

	admin := types.StringToAddress("0xabcd")

	pool, err := NewTxPool(
		hclog.NewNullLogger(),
		&chain.Forks{chain.TransactionsBlockList: chain.NewFork(forkBlock)},
		store,
		nil,
		nil,
		&Config{
			PriceLimit:            defaultPriceLimit,
			MaxSlots:              defaultMaxSlots,
			MaxAccountEnqueued:    defaultMaxAccountEnqueued,
			TransactionsBlockList: &chain.AddressListConfig{AdminAddresses: []types.Address{admin}},
		},
	)
	require.NoError(t, err)

	return pool
}

func TestTxPool_checkTransactionsBlockList(t *testing.T) {
	t.Parallel()

	from := types.StringToAddress("0x1111")
	to := types.StringToAddress("0x2222")
	admin := types.StringToAddress("0xabcd")

	t.Run("rejects blacklisted from", func(t *testing.T) {
		t.Parallel()

		store := blockListMockStore{
			defaultMockStore: NewDefaultMockStore(mockHeader),
			roles:            map[types.Address]addresslist.Role{from: addresslist.EnabledRole},
		}
		pool := newBlockListTestPool(t, store, 0)

		err := pool.checkTransactionsBlockList(types.ZeroHash, 1, from, &to)
		require.ErrorIs(t, err, ErrAccountBlacklisted)
	})

	t.Run("rejects blacklisted to", func(t *testing.T) {
		t.Parallel()

		blockedTo := types.StringToAddress("0x3333")
		store := blockListMockStore{
			defaultMockStore: NewDefaultMockStore(mockHeader),
			roles:            map[types.Address]addresslist.Role{blockedTo: addresslist.EnabledRole},
		}
		pool := newBlockListTestPool(t, store, 0)

		err := pool.checkTransactionsBlockList(types.ZeroHash, 1, types.StringToAddress("0x9999"), &blockedTo)
		require.ErrorIs(t, err, ErrAccountBlacklisted)
	})

	t.Run("allows admin", func(t *testing.T) {
		t.Parallel()

		store := blockListMockStore{
			defaultMockStore: NewDefaultMockStore(mockHeader),
			roles:            map[types.Address]addresslist.Role{admin: addresslist.AdminRole},
		}
		pool := newBlockListTestPool(t, store, 0)

		require.NoError(t, pool.checkTransactionsBlockList(types.ZeroHash, 1, admin, &to))
	})

	t.Run("inactive before fork", func(t *testing.T) {
		t.Parallel()

		store := blockListMockStore{
			defaultMockStore: NewDefaultMockStore(mockHeader),
			roles:            map[types.Address]addresslist.Role{from: addresslist.EnabledRole},
		}
		pool := newBlockListTestPool(t, store, 100)

		require.NoError(t, pool.checkTransactionsBlockList(types.ZeroHash, 99, from, &to))
	})
}
