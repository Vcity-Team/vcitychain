package blockchain

import "github.com/Vcity-Team/vcitychain/types"

// StateHealer allows consensus engines to participate in B-path state healing.
type StateHealer interface {
	OnRewindToHeight(targetHeight uint64) error
	PrepareSameHeightForkReplay(replacedBlock *types.Block) error
}
