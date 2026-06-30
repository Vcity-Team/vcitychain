package txpool

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// ExecutorStore implements the txpool store interface using state.Executor.
// Intended for BlockBuilder / executor integration tests.
type ExecutorStore struct {
	Executor *state.Executor
	head     *types.Header

	blocks map[types.Hash]*types.Block
}

// NewExecutorStore returns a store backed by executor state at header.StateRoot.
func NewExecutorStore(executor *state.Executor, header *types.Header) *ExecutorStore {
	return &ExecutorStore{
		Executor: executor,
		head:     header,
	}
}

// UpdateHeader points the store at a new chain head (e.g. after Commit).
func (s *ExecutorStore) UpdateHeader(header *types.Header) {
	s.head = header
}

// SetBlocks enables GetBlockByHash for reorg tests.
func (s *ExecutorStore) SetBlocks(blocks map[types.Hash]*types.Block) {
	s.blocks = blocks
}

func (s *ExecutorStore) Header() *types.Header {
	return s.head
}

func (s *ExecutorStore) GetNonce(_ types.Hash, addr types.Address) uint64 {
	snap, err := s.Executor.StateAt(s.head.StateRoot)
	if err != nil {
		return 0
	}

	acct, err := snap.GetAccount(addr)
	if err != nil || acct == nil {
		return 0
	}

	return acct.Nonce
}

func (s *ExecutorStore) GetBalance(_ types.Hash, addr types.Address) (*big.Int, error) {
	snap, err := s.Executor.StateAt(s.head.StateRoot)
	if err != nil {
		return nil, err
	}

	acct, err := snap.GetAccount(addr)
	if err != nil {
		return nil, err
	}

	if acct == nil {
		return big.NewInt(0), nil
	}

	return acct.Balance, nil
}

func (s *ExecutorStore) GetBlockByHash(hash types.Hash, _ bool) (*types.Block, bool) {
	if s.blocks == nil {
		return nil, false
	}

	block, ok := s.blocks[hash]

	return block, ok
}

func (s *ExecutorStore) CalculateBaseFee(parent *types.Header) uint64 {
	if parent != nil {
		return parent.BaseFee
	}

	return 0
}

// NewTestPoolWithExecutorStore creates a TxPool wired to executor-backed state.
func NewTestPoolWithExecutorStore(logger hclog.Logger, store *ExecutorStore, cfg *Config) (*TxPool, error) {
	if cfg == nil {
		cfg = &Config{
			PriceLimit:         1,
			MaxSlots:           4096,
			MaxAccountEnqueued: 128,
		}
	}

	return NewTxPool(
		logger,
		defaultEnabledForks(),
		store,
		nil,
		nil,
		cfg,
	)
}

func defaultEnabledForks() *chain.Forks {
	return &chain.Forks{
		chain.Homestead: chain.NewFork(0),
		chain.Istanbul:  chain.NewFork(0),
		chain.London:    chain.NewFork(0),
	}
}
