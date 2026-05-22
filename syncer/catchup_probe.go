package syncer

import (
	"sort"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	catchUpProbeTimeout = 2 * time.Second
)

type catchUpProbeClass int

const (
	catchUpProbeFail catchUpProbeClass = iota
	catchUpProbeOK
	catchUpProbeFork
)

// orderedBootCatchUpCandidates 全部 genesis boot，按 RPC 高度（无则 P2P）降序，逐个尝试拉块。
func (s *syncer) orderedBootCatchUpCandidates() []peer.ID {
	type cand struct {
		id peer.ID
		h  uint64
	}
	var list []cand
	for id := range s.trustedBootnodeIDs {
		h, ok := s.getTrustedBootRPCHeight(id)
		if !ok {
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
		out = append(out, c.id)
	}
	return out
}

// tryCatchUpNextBlockFromBoot：trusted 已超前 → 对 boot 拉 local+1 并验块写入（不等待 P2P 宣称 > local）。
func (s *syncer) tryCatchUpNextBlockFromBoot(local uint64, meta trustedTipResult, callback func(*types.FullBlock) bool) bool {
	if !trustedAheadOfLocal(meta, local) {
		return false
	}
	hdr := s.blockchain.Header()
	if hdr == nil {
		return false
	}
	nextNum := local + 1
	localHash := hdr.Hash

	for _, pid := range s.orderedBootCatchUpCandidates() {
		blk, class := s.fetchCanonicalNextForCatchUp(pid, nextNum, localHash, catchUpProbeTimeout)
		switch class {
		case catchUpProbeOK:
			if blk == nil {
				continue
			}
			if s.ingestCatchUpBlock(pid, blk, callback) {
				s.logger.Info("syncer: catch-up wrote next block from boot (trusted height ahead)",
					"peer", pid.String(),
					"blockNumber", nextNum,
					"trustedTip", meta.Tip,
					"maxBootHeight", meta.MaxBootHeight)
				s.notifyNewStatusEvent()
				return true
			}
		case catchUpProbeFork:
			s.logger.Debug("syncer: catch-up boot parent mismatch, try next boot",
				"peer", pid.String(),
				"nextHeight", nextNum)
		default:
			s.logger.Debug("syncer: catch-up boot did not deliver block",
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
			return false
		}
	}
	if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
		return false
	}
	s.markBlockTransactionsProcessed(block.Transactions)
	s.recordTrustedPeerSuccess(peerID, block.Number())
	updateMetrics(fullBlock)
	callback(fullBlock)
	return true
}
