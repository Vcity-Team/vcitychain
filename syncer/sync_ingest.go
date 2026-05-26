package syncer

import (
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

// syncIngestBlock verifies a block from sync, stages receipts, writes, and invokes callback.
// Caller must hold syncCatchUp scope (beginSyncCatchUp) when used from catch-up burst stream.
func (s *syncer) syncIngestBlock(peerID peer.ID, block *types.Block, callback func(*types.FullBlock) bool) (ok bool, terminate bool) {
	if block == nil {
		return false, false
	}
	if s.isConsensusSwitchHeight(block) {
		if err := s.blockchain.WriteBlockWithoutConsensus(block, syncerName); err != nil {
			return false, false
		}
		fullBlock := &types.FullBlock{Block: block, Receipts: []*types.Receipt{}}
		updateMetrics(fullBlock)
		terminate = callback(fullBlock)
		s.recordTrustedPeerSuccess(peerID, block.Number())
		return true, terminate
	}

	fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
	if err != nil {
		if healed, healErr := s.tryHealStateRootMismatch(block, err); healErr == nil {
			fullBlock = healed
			err = nil
		}
		if err != nil {
			return false, false
		}
	}
	s.blockchain.StageSyncReceipts(block.Hash(), fullBlock.Receipts)
	if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
		return false, false
	}
	s.markBlockTransactionsProcessed(block.Transactions)
	s.recordTrustedPeerSuccess(peerID, block.Number())
	updateMetrics(fullBlock)
	terminate = callback(fullBlock)
	return true, terminate
}
