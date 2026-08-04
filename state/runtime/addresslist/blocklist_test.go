package addresslist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/types"
)

func TestCheckBlockedTx(t *testing.T) {
	t.Parallel()

	from := types.StringToAddress("0x1")
	to := types.StringToAddress("0x2")
	admin := types.StringToAddress("0x3")
	blockListAddr := contracts.BlockListTransactionsAddr

	roles := map[types.Address]Role{
		from:  NoRole,
		to:    NoRole,
		admin: AdminRole,
	}

	getRole := func(addr types.Address) Role {
		if role, ok := roles[addr]; ok {
			return role
		}

		return NoRole
	}

	t.Run("allows normal transfer", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, CheckBlockedTx(getRole, from, &to))
	})

	t.Run("allows contract creation when from is clean", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, CheckBlockedTx(getRole, from, nil))
	})

	t.Run("blocks blacklisted from", func(t *testing.T) {
		t.Parallel()
		local := map[types.Address]Role{from: EnabledRole}
		require.ErrorIs(t, CheckBlockedTx(func(a types.Address) Role { return local[a] }, from, &to), ErrAccountBlacklisted)
	})

	t.Run("blocks blacklisted to", func(t *testing.T) {
		t.Parallel()
		local := map[types.Address]Role{to: EnabledRole}
		require.ErrorIs(t, CheckBlockedTx(func(a types.Address) Role { return local[a] }, from, &to), ErrAccountBlacklisted)
	})

	t.Run("admin is not blocked", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, CheckBlockedTx(getRole, admin, &to))
	})

	t.Run("system caller is exempt", func(t *testing.T) {
		t.Parallel()
		local := map[types.Address]Role{contracts.SystemCaller: EnabledRole}
		require.NoError(t, CheckBlockedTx(func(a types.Address) Role { return local[a] }, contracts.SystemCaller, &to))
	})

	t.Run("calls to blocklist contract are exempt", func(t *testing.T) {
		t.Parallel()
		local := map[types.Address]Role{from: EnabledRole}
		require.NoError(t, CheckBlockedTx(func(a types.Address) Role { return local[a] }, from, &blockListAddr))
	})
}
