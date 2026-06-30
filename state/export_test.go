package state

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Test-only exports for external test packages (state_test).

func GroupTransactionsByAccountForTest(txs []*types.Transaction) map[types.Address][]*types.Transaction {
	return groupTransactionsByAccount(txs)
}

func NewTestSnapshot(preState map[types.Address]*PreState) Snapshot {
	return newStateWithPreState(preState)
}

func NewTestTxn(preState map[types.Address]*PreState) *Txn {
	return newTestTxn(preState)
}

func NewParallelTestTransition(preState map[types.Address]*PreState) *Transition {
	snap := newStateWithPreState(preState)
	t := NewTransition(chain.ForksInTime{}, snap, newTestTxn(preState))
	t.logger = hclog.NewNullLogger()
	t.ctx = runtime.TxContext{BaseFee: big.NewInt(0), ChainID: 100, GasLimit: 30_000_000}
	t.gasPool = 30_000_000
	return t
}

func SetupExecutorGetHash(e *Executor) {
	e.GetHash = func(header *types.Header) func(i uint64) types.Hash {
		return func(i uint64) types.Hash {
			return types.BytesToHash(common.EncodeUint64ToBytes(i))
		}
	}
}

func (t *Transition) AccountNonceForTest(addr types.Address) uint64 {
	return t.state.GetNonce(addr)
}

func (t *Transition) AccountBalanceForTest(addr types.Address) *big.Int {
	return t.state.GetBalance(addr)
}

func (t *Transition) SetCodeForTest(addr types.Address, code []byte) {
	t.state.SetCode(addr, code)
}
