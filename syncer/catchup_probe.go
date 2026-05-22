package syncer

import (
	"sort"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// catchUpProbeTimeout：RPC 已超前、P2P 仍滞后时，对 boot 单块 GetBlocks 的超时。
	catchUpProbeTimeout = 2 * time.Second
)

type catchUpProbeClass int

const (
	catchUpProbeFail catchUpProbeClass = iota
	catchUpProbeOK
	catchUpProbeFork
)

// catchUpProbeAllowed P0-3：至少 quorum 链尖或 K 台 boot RPC 报告 nextNum，避免单台 RPC 虚高。
func (s *syncer) catchUpProbeAllowed(nextNum uint64, meta trustedTipResult) bool {
	if meta.Tip >= nextNum {
		return true
	}
	if countHeightsAtLeast(meta.Heights, nextNum) >= trustedBootnodeQuorumK {
		return true
	}
	return s.bootsRPCAtLeast(nextNum) >= trustedBootnodeQuorumK
}

func (s *syncer) orderedBootCatchUpCandidates(nextNum uint64) []peer.ID {
	type cand struct {
		id peer.ID
		h  uint64
	}
	var list []cand
	for id := range s.trustedBootnodeIDs {
		h, ok := s.getTrustedBootRPCHeight(id)
		if !ok {
			h = 0
			if v, exists := s.peerMap.Load(id.String()); exists {
				if p, _ := v.(*NoForkPeer); p != nil {
					h = p.Number
				}
			}
		}
		list = append(list, cand{id: id, h: h})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].h != list[j].h {
			return list[i].h > list[j].h
		}
		return list[i].id.String() < list[j].id.String()
	})
	out := make([]peer.ID, 0, len(list))
	for _, c := range list {
		if c.h >= nextNum {
			out = append(out, c.id)
		}
	}
	if len(out) == 0 {
		for _, c := range list {
			out = append(out, c.id)
		}
	}
	return out
}

// tryCatchUpNextBlockFromBoot 从 genesis boot 拉取并验证写入 local+1（仅 trusted boot，完整 VerifyFinalizedBlock）。
func (s *syncer) tryCatchUpNextBlockFromBoot(local uint64, meta trustedTipResult, callback func(*types.FullBlock) bool) bool {
	if !s.trustedQuorumIndicatesNextBlock(local, meta) {
		return false
	}
	hdr := s.blockchain.Header()
	if hdr == nil {
		return false
	}
	nextNum := local + 1
	if !s.catchUpProbeAllowed(nextNum, meta) {
		return false
	}
	localHash := hdr.Hash

	for _, pid := range s.orderedBootCatchUpCandidates(nextNum) {
		blk, class := s.fetchCanonicalNextForCatchUp(pid, nextNum, localHash, catchUpProbeTimeout)
		switch class {
		case catchUpProbeOK:
			if blk == nil {
				continue
			}
			if s.ingestCatchUpBlock(pid, blk, callback) {
				s.logger.Info("syncer: catch-up wrote next block from boot probe",
					"peer", pid.String(),
					"blockNumber", nextNum)
				s.notifyNewStatusEvent()
				return true
			}
		case catchUpProbeFork:
			s.logger.Debug("syncer: catch-up boot probe parent mismatch, try next boot",
				"peer", pid.String(),
				"nextHeight", nextNum)
		default:
			s.logger.Debug("syncer: catch-up boot probe did not deliver block",
				"peer", pid.String(),
				"nextHeight", nextNum)
		}
	}
	return false
}

func (s *syncer) fetchCanonicalNextForCatchUp(peerID peer.ID, nextNum uint64, localHash types.Hash, probeTimeout time.Duration) (*types.Block, catchUpProbeClass) {
	for attempt := 0; attempt < preProduceProbeInflightTries; attempt++ {
		blockCh, cancel, err := s.syncPeerClient.GetBlocks(peerID, nextNum, probeTimeout)
		if err != nil {
			if strings.Contains(err.Error(), "already in progress") {
				time.Sleep(getBlocksInflightBackoff)
				continue
			}
			return nil, catchUpProbeFail
		}

		timer := time.NewTimer(probeTimeout)
		select {
		case blk := <-blockCh:
			cancel()
			if !timer.Stop() {
				<-timer.C
			}
			if blk == nil {
				return nil, catchUpProbeFail
			}
			if blk.Number() != nextNum {
				return nil, catchUpProbeFail
			}
			if blk.ParentHash() != localHash {
				return nil, catchUpProbeFork
			}
			return blk, catchUpProbeOK
		case <-timer.C:
			cancel()
			return nil, catchUpProbeFail
		}
	}
	return nil, catchUpProbeFail
}

func (s *syncer) ingestCatchUpBlock(peerID peer.ID, block *types.Block, callback func(*types.FullBlock) bool) bool {
	if block == nil {
		return false
	}
	if s.isConsensusSwitchHeight(block) {
		if err := s.blockchain.WriteBlockWithoutConsensus(block, syncerName); err != nil {
			s.logger.Debug("syncer: catch-up consensus switch write failed",
				"peer", peerID.String(),
				"blockNumber", block.Number(),
				"err", err)
			return false
		}
		fullBlock := &types.FullBlock{Block: block, Receipts: []*types.Receipt{}}
		updateMetrics(fullBlock)
		callback(fullBlock)
		s.recordTrustedPeerSuccess(peerID, block.Number())
		return true
	}

	fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
	if err != nil {
		if healed, healErr := s.tryHealStateRootMismatch(block, err); healErr == nil {
			fullBlock = healed
			err = nil
		}
		if err != nil {
			s.logger.Debug("syncer: catch-up block verify failed",
				"peer", peerID.String(),
				"blockNumber", block.Number(),
				"err", err)
			return false
		}
	}
	if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
		s.logger.Debug("syncer: catch-up block write failed",
			"peer", peerID.String(),
			"blockNumber", block.Number(),
			"err", err)
		return false
	}
	s.markBlockTransactionsProcessed(block.Transactions)
	s.recordTrustedPeerSuccess(peerID, block.Number())
	updateMetrics(fullBlock)
	callback(fullBlock)
	return true
}
