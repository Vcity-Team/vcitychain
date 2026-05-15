package blockchain

import (
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
)

// ExecutorWithMode extends Executor with execution mode control (A-lite).
type ExecutorWithMode interface {
	Executor
	ProcessBlockWithMode(
		parentRoot types.Hash,
		block *types.Block,
		blockCreator types.Address,
		mode ExecutionMode,
	) (*state.Transition, error)
}
