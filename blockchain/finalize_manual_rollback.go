package blockchain

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/state"
	itrie "github.com/Vcity-Team/vcitychain/state/immutable-trie"
	"github.com/Vcity-Team/vcitychain/types"
)

// noopRollbackVerifier is a minimal consensus stub for offline rollback state heal.
// DPoS metadata rewind is handled separately by the rollback CLI.
type noopRollbackVerifier struct{}

func (noopRollbackVerifier) VerifyHeader(*types.Header) error { return nil }

func (noopRollbackVerifier) ProcessHeaders([]*types.Header) error { return nil }

func (noopRollbackVerifier) GetBlockCreator(*types.Header) (types.Address, error) {
	return types.ZeroAddress, nil
}

func (noopRollbackVerifier) PreCommitState(*types.Block, *state.Transition) error { return nil }

func (noopRollbackVerifier) OnRewindToHeight(uint64) error { return nil }

func (noopRollbackVerifier) PrepareSameHeightForkReplay(*types.Block) error { return nil }

// NewNoopRollbackVerifier returns a Verifier that also implements StateHealer (no-op side effects).
func NewNoopRollbackVerifier() Verifier {
	return noopRollbackVerifier{}
}

// FinalizeManualRollbackResult summarizes offline execution-layer finalize.
type FinalizeManualRollbackResult struct {
	TargetHeight      uint64
	TargetStateRoot   types.Hash
	StateRootVerified bool
	BlocksReplayed    uint64
	FinalHeadHeight   uint64
}

// VerifyStateRootAvailable checks that the trie DB contains nodes for root.
func VerifyStateRootAvailable(trieStorage itrie.Storage, root types.Hash) error {
	if root == types.EmptyRootHash {
		return nil
	}

	checked, err := itrie.HashChecker(root.Bytes(), trieStorage)
	if err != nil {
		return fmt.Errorf("state root %s not available in trie db: %w", root.String(), err)
	}

	if checked != root {
		return fmt.Errorf("state root mismatch in trie db: have %s, checked %s", root.String(), checked.String())
	}

	return nil
}

// FinalizeManualRollback aligns the in-memory chain view and (optionally) replays blocks
// above targetHeight using the same execution path as HealCanonicalBlockState.
//
// Call this after the rollback CLI has updated head/canonical in LevelDB and the node is stopped.
// blocksToReplay should be collected before deleting block data; pass nil to only verify target stateRoot.
func (b *Blockchain) FinalizeManualRollback(
	trieStorage itrie.Storage,
	targetHeight uint64,
	blocksToReplay []*types.Block,
) (*FinalizeManualRollbackResult, error) {
	if b == nil {
		return nil, fmt.Errorf("finalize rollback: nil blockchain")
	}

	targetHash, ok := b.db.ReadCanonicalHash(targetHeight)
	if !ok {
		return nil, fmt.Errorf("finalize rollback: canonical hash not found at height %d", targetHeight)
	}

	targetHeader, ok := b.GetHeaderByHash(targetHash)
	if !ok || targetHeader == nil {
		return nil, fmt.Errorf("finalize rollback: header not found at height %d", targetHeight)
	}

	if err := VerifyStateRootAvailable(trieStorage, targetHeader.StateRoot); err != nil {
		return nil, err
	}

	result := &FinalizeManualRollbackResult{
		TargetHeight:      targetHeight,
		TargetStateRoot:   targetHeader.StateRoot,
		StateRootVerified: true,
	}

	td, ok := b.GetTD(targetHash)
	if !ok || td == nil {
		return nil, fmt.Errorf("finalize rollback: total difficulty not found at height %d", targetHeight)
	}

	b.setCurrentHeader(targetHeader, td)

	if healer, ok := b.consensus.(StateHealer); ok {
		if err := healer.OnRewindToHeight(targetHeight); err != nil {
			return nil, fmt.Errorf("finalize rollback: consensus rewind: %w", err)
		}
	}

	for _, block := range blocksToReplay {
		if block == nil || block.Header == nil {
			continue
		}

		if block.Number() <= targetHeight {
			continue
		}

		fullBlock, err := b.HealCanonicalBlockState(block)
		if err != nil {
			return nil, fmt.Errorf("finalize rollback: replay block %d: %w", block.Number(), err)
		}

		if err := b.WriteFullBlock(fullBlock, "rollback-replay"); err != nil {
			return nil, fmt.Errorf("finalize rollback: write replayed block %d: %w", block.Number(), err)
		}

		result.BlocksReplayed++
	}

	if head := b.Header(); head != nil {
		result.FinalHeadHeight = head.Number
	}

	return result, nil
}
