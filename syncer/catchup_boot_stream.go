package syncer

import (
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

// runBootCatchUpStream opens one GetBlocks stream on a boot peer and ingests up to maxBlocks
// before deadline. Returns blocks written and whether a fork was detected on the stream.
func (s *syncer) runBootCatchUpStream(
	localNum uint64,
	localHash types.Hash,
	maxBlocks int,
	deadline time.Time,
	callback func(*types.FullBlock) bool,
) (written int, fork bool) {
	if maxBlocks <= 0 || !time.Now().Before(deadline) {
		return 0, false
	}
	candidates := s.orderedBootCatchUpCandidatesRotated()
	for i, peerID := range candidates {
		if !time.Now().Before(deadline) {
			break
		}
		n, sawFork := s.bootCatchUpStreamFromPeer(peerID, localNum, localHash, maxBlocks, deadline, callback)
		if sawFork {
			return written + n, true
		}
		if n > 0 {
			return written + n, false
		}
		if i+1 < len(candidates) {
			s.bulkBootRotateIdx.Add(1)
		}
	}
	return written, false
}

func (s *syncer) bootCatchUpStreamFromPeer(
	peerID peer.ID,
	localNum uint64,
	localHash types.Hash,
	maxBlocks int,
	deadline time.Time,
	callback func(*types.FullBlock) bool,
) (written int, fork bool) {
	nextNum := localNum + 1
	blockCh, cancel, err := s.syncPeerClient.GetBlocks(peerID, nextNum, s.blockTimeout)
	if err != nil {
		s.logger.Debug("syncer: boot catch-up stream open failed",
			"peer", peerID.String(),
			"from", nextNum,
			"error", err)
		return 0, false
	}
	defer cancel()
	defer func() { _ = s.syncPeerClient.CloseStream(peerID) }()

	expectedNum := nextNum
	expectedParent := localHash

	for written < maxBlocks {
		if !time.Now().Before(deadline) {
			break
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			break
		}
		timer := time.NewTimer(wait)
		select {
		case block, ok := <-blockCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if !ok {
				return written, false
			}
			if block == nil || block.Number() == 0 {
				continue
			}
			if block.Number() != expectedNum {
				s.logger.Warn("syncer: boot catch-up stream unexpected block number",
					"peer", peerID.String(),
					"expected", expectedNum,
					"got", block.Number())
				return written, false
			}
			if block.ParentHash() != expectedParent {
				s.logger.Warn("syncer: boot catch-up stream parent mismatch (fork)",
					"peer", peerID.String(),
					"height", expectedNum,
					"expectedParent", expectedParent.String(),
					"gotParent", block.ParentHash().String())
				return written, true
			}
			okIngest, terminate := s.syncIngestBlock(peerID, block, callback)
			if !okIngest {
				return written, false
			}
			written++
			expectedNum++
			expectedParent = block.Hash()
			if terminate {
				return written, false
			}
		case <-timer.C:
			return written, false
		}
	}
	return written, false
}
