package state

import (
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/state/runtime/addresslist"
	"github.com/Vcity-Team/vcitychain/types"
)

type memoryStateRef struct {
	storage map[types.Address]map[types.Hash]types.Hash
}

func newMemoryStateRef() *memoryStateRef {
	return &memoryStateRef{storage: map[types.Address]map[types.Hash]types.Hash{}}
}

func (m *memoryStateRef) SetState(addr types.Address, key, value types.Hash) {
	slots, ok := m.storage[addr]
	if !ok {
		slots = map[types.Hash]types.Hash{}
		m.storage[addr] = slots
	}

	slots[key] = value
}

func (m *memoryStateRef) GetStorage(addr types.Address, key types.Hash) types.Hash {
	if slots, ok := m.storage[addr]; ok {
		return slots[key]
	}

	return types.ZeroHash
}

func TestTransition_isTxBlocked(t *testing.T) {
	t.Parallel()

	from := types.StringToAddress("0x1111")
	to := types.StringToAddress("0x2222")
	admin := types.StringToAddress("0x3333")
	blockListAddr := contracts.BlockListTransactionsAddr

	st := newMemoryStateRef()
	list := addresslist.NewAddressList(st, blockListAddr)
	list.SetRole(admin, addresslist.AdminRole)

	tr := &Transition{txnBlockList: list}

	t.Run("allows clean addresses", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, tr.isTxBlocked(from, &to))
	})

	t.Run("blocks enabled from", func(t *testing.T) {
		t.Parallel()
		localSt := newMemoryStateRef()
		localList := addresslist.NewAddressList(localSt, blockListAddr)
		localList.SetRole(from, addresslist.EnabledRole)
		localTr := &Transition{txnBlockList: localList}
		require.ErrorIs(t, localTr.isTxBlocked(from, &to), addresslist.ErrAccountBlacklisted)
	})

	t.Run("blocks enabled to", func(t *testing.T) {
		t.Parallel()
		localSt := newMemoryStateRef()
		localList := addresslist.NewAddressList(localSt, blockListAddr)
		localList.SetRole(to, addresslist.EnabledRole)
		localTr := &Transition{txnBlockList: localList}
		require.ErrorIs(t, localTr.isTxBlocked(from, &to), addresslist.ErrAccountBlacklisted)
	})

	t.Run("admin not blocked", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, tr.isTxBlocked(admin, &to))
	})

	t.Run("system caller exempt", func(t *testing.T) {
		t.Parallel()
		localSt := newMemoryStateRef()
		localList := addresslist.NewAddressList(localSt, blockListAddr)
		localList.SetRole(contracts.SystemCaller, addresslist.EnabledRole)
		localTr := &Transition{txnBlockList: localList}
		require.NoError(t, localTr.isTxBlocked(contracts.SystemCaller, &to))
	})

	t.Run("blocklist contract calls exempt", func(t *testing.T) {
		t.Parallel()
		localSt := newMemoryStateRef()
		localList := addresslist.NewAddressList(localSt, blockListAddr)
		localList.SetRole(from, addresslist.EnabledRole)
		localTr := &Transition{txnBlockList: localList}
		require.NoError(t, localTr.isTxBlocked(from, &blockListAddr))
	})

	t.Run("create only checks from", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, tr.isTxBlocked(from, nil))

		localSt := newMemoryStateRef()
		localList := addresslist.NewAddressList(localSt, blockListAddr)
		localList.SetRole(from, addresslist.EnabledRole)
		localTr := &Transition{txnBlockList: localList}
		require.ErrorIs(t, localTr.isTxBlocked(from, nil), addresslist.ErrAccountBlacklisted)
	})
}

func TestExecutor_maybeApplyTransactionsBlockListFork(t *testing.T) {
	t.Parallel()

	admin := types.StringToAddress("0xaaaa")
	victim := types.StringToAddress("0xbbbb")
	forkBlock := uint64(100)

	newExec := func() (*Executor, *Transition, *memoryStateRef) {
		st := newMemoryStateRef()
		list := addresslist.NewAddressList(st, contracts.BlockListTransactionsAddr)
		params := &chain.Params{
			TransactionsBlockList: &chain.AddressListConfig{
				AdminAddresses:   []types.Address{admin},
				EnabledAddresses: []types.Address{victim},
			},
			Forks: &chain.Forks{
				chain.TransactionsBlockList: chain.NewFork(forkBlock),
			},
		}
		exec := &Executor{
			config: params,
			logger: hclog.NewNullLogger(),
		}
		txn := &Transition{txnBlockList: list}

		return exec, txn, st
	}

	t.Run("skips before fork height", func(t *testing.T) {
		t.Parallel()
		exec, txn, _ := newExec()
		exec.maybeApplyTransactionsBlockListFork(txn, forkBlock-1)
		require.Equal(t, addresslist.NoRole, txn.txnBlockList.GetRole(admin))
	})

	t.Run("applies at exact fork height", func(t *testing.T) {
		t.Parallel()
		exec, txn, _ := newExec()
		exec.maybeApplyTransactionsBlockListFork(txn, forkBlock)
		require.Equal(t, addresslist.AdminRole, txn.txnBlockList.GetRole(admin))
		require.Equal(t, addresslist.EnabledRole, txn.txnBlockList.GetRole(victim))
	})

	t.Run("catch-up after missed fork height", func(t *testing.T) {
		t.Parallel()
		exec, txn, _ := newExec()
		exec.maybeApplyTransactionsBlockListFork(txn, forkBlock+50)
		require.Equal(t, addresslist.AdminRole, txn.txnBlockList.GetRole(admin))
		require.Equal(t, addresslist.EnabledRole, txn.txnBlockList.GetRole(victim))
	})

	t.Run("noop when admin already present at exact fork height", func(t *testing.T) {
		t.Parallel()
		exec, txn, _ := newExec()
		txn.txnBlockList.SetRole(admin, addresslist.AdminRole)
		exec.maybeApplyTransactionsBlockListFork(txn, forkBlock)
		require.Equal(t, addresslist.AdminRole, txn.txnBlockList.GetRole(admin))
		require.Equal(t, addresslist.NoRole, txn.txnBlockList.GetRole(victim))
	})

	t.Run("noop when admin already present after fork", func(t *testing.T) {
		t.Parallel()
		exec, txn, _ := newExec()
		txn.txnBlockList.SetRole(admin, addresslist.AdminRole)
		// victim not set — catch-up must not re-apply enabled list once any admin exists
		exec.maybeApplyTransactionsBlockListFork(txn, forkBlock+1)
		require.Equal(t, addresslist.AdminRole, txn.txnBlockList.GetRole(admin))
		require.Equal(t, addresslist.NoRole, txn.txnBlockList.GetRole(victim))
	})
}
