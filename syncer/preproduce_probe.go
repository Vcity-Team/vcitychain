package syncer

import (
	"sort"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	preProduceProbeInflightTries = 8
	// DefaultPreProduceProbeTimeout 出块前 P2P 探测默认超时。
	DefaultPreProduceProbeTimeout = 500 * time.Millisecond
)

type preProduceProbeClass int

const (
	preProduceProbeNone preProduceProbeClass = iota
	preProduceProbeOK
	preProduceProbeFork
	preProduceProbeFail
)

// syncNoopBlockCallback Sync 主循环外的落块路径（出块前探测 ingest）不终止 Sync。
func syncNoopBlockCallback(*types.FullBlock) bool { return false }

// TryProbeCanonicalNextBeforeProduce 出块前从 P2P 拉取下一高度；校验通过后写入本地并返回 true（放弃本轮本地构建）。
func (s *syncer) TryProbeCanonicalNextBeforeProduce(probeTimeout time.Duration) bool {
	if probeTimeout <= 0 {
		probeTimeout = DefaultPreProduceProbeTimeout
	}
	hdr := s.blockchain.Header()
	if hdr == nil {
		return false
	}
	localNum := hdr.Number
	localHash := hdr.Hash
	nextNum := localNum + 1

	skip := s.mergeSkipsForBestPeer(make(map[peer.ID]bool))
	candidates := s.peersAdvertisingAboveLocal(localNum, skip)
	if len(candidates) == 0 {
		s.logger.Debug("pre-produce P2P probe: no peer advertises above local tip",
			"localTip", localNum, "nextHeight", nextNum)
		return false
	}

	s.logger.Info("🔭 【出块前 P2P 探测】对宣称高于本地的 peer 按高度降序逐个探测",
		"nextHeight", nextNum,
		"localTip", localNum,
		"probeTimeout", probeTimeout.String(),
		"candidateCount", len(candidates))

	for _, pid := range candidates {
		class, blk := s.probeOnePeerPreProduce(pid, nextNum, localHash, probeTimeout)
		switch class {
		case preProduceProbeOK:
			if blk == nil {
				continue
			}
			if reason := s.preProduceProbeRejectReason(blk, localHash, localNum); reason != "" {
				s.logger.Warn("pre-produce P2P probe: peer block failed header checks, try next peer",
					"peer", pid.String(),
					"nextHeight", nextNum,
					"blockHash", blk.Hash().String(),
					"reason", reason)
				continue
			}
			if s.ingestCatchUpBlock(pid, blk, syncNoopBlockCallback) {
				s.notifyNewStatusEvent()
				s.logger.Info("✅ 【出块前 P2P 探测】已从 peer 写入下一高度，放弃本轮本地出块",
					"peer", pid.String(),
					"nextHeight", nextNum,
					"blockHash", blk.Hash().String())
				return true
			}
			s.logger.Warn("pre-produce P2P probe: block passed checks but ingest failed, try next peer",
				"peer", pid.String(),
				"nextHeight", nextNum)
		case preProduceProbeFork:
			s.logger.Warn("pre-produce P2P probe: parent mismatch (fork view), try next peer",
				"peer", pid.String(),
				"nextHeight", nextNum)
		case preProduceProbeFail:
			s.logger.Debug("pre-produce P2P probe: peer did not deliver canonical next block",
				"peer", pid.String(),
				"nextHeight", nextNum)
		}
	}
	return false
}

// preProduceProbeRejectReason 非空表示该块不可作为 local+1 落链（父哈希/时间戳/共识 VerifyHeader）。
func (s *syncer) preProduceProbeRejectReason(blk *types.Block, localHash types.Hash, localNum uint64) string {
	if blk == nil {
		return "nil block"
	}
	if blk.Number() != localNum+1 {
		return "unexpected block number"
	}
	if blk.ParentHash() != localHash {
		return "parent hash mismatch"
	}
	parent := s.blockchain.Header()
	if parent == nil || parent.Number != localNum {
		return "local tip changed"
	}
	if blk.Header.Timestamp <= parent.Timestamp {
		return "timestamp older than parent"
	}
	if v := s.blockchain.GetConsensus(); v != nil {
		if err := v.VerifyHeader(blk.Header); err != nil {
			return err.Error()
		}
	}
	return ""
}

// peersAdvertisingAboveLocal 返回所有宣称高度 **高于** 本地链尖的 peer，按 Number 降序、再按 ID 升序（稳定、可复现）。
func (s *syncer) peersAdvertisingAboveLocal(localNum uint64, skip map[peer.ID]bool) []peer.ID {
	var list []*NoForkPeer
	s.peerMap.Range(func(_, value interface{}) bool {
		p, ok := value.(*NoForkPeer)
		if !ok || p == nil {
			return true
		}
		if skip != nil && skip[p.ID] {
			return true
		}
		if p.Number <= localNum {
			return true
		}
		list = append(list, p)
		return true
	})
	sort.Slice(list, func(i, j int) bool {
		if list[i].Number != list[j].Number {
			return list[i].Number > list[j].Number
		}
		return list[i].ID.String() < list[j].ID.String()
	})
	out := make([]peer.ID, 0, len(list))
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out
}

func (s *syncer) probeOnePeerPreProduce(peerID peer.ID, nextNum uint64, localHash types.Hash, probeTimeout time.Duration) (preProduceProbeClass, *types.Block) {
	for attempt := 0; attempt < preProduceProbeInflightTries; attempt++ {
		blockCh, cancel, err := s.syncPeerClient.GetBlocks(peerID, nextNum, probeTimeout)
		if err != nil {
			if strings.Contains(err.Error(), "already in progress") {
				s.logger.Debug("pre-produce P2P probe: GetBlocks inflight, backoff",
					"peer", peerID.String(), "attempt", attempt+1)
				time.Sleep(getBlocksInflightBackoff)
				continue
			}
			s.logger.Debug("pre-produce P2P probe: GetBlocks open failed",
				"peer", peerID.String(), "err", err)
			return preProduceProbeFail, nil
		}

		timer := time.NewTimer(probeTimeout)
		select {
		case blk := <-blockCh:
			cancel()
			if !timer.Stop() {
				<-timer.C
			}
			if blk == nil {
				return preProduceProbeFail, nil
			}
			if blk.Number() != nextNum {
				s.logger.Debug("pre-produce P2P probe: unexpected block number",
					"peer", peerID.String(), "want", nextNum, "got", blk.Number())
				return preProduceProbeFail, nil
			}
			if blk.ParentHash() != localHash {
				return preProduceProbeFork, nil
			}
			return preProduceProbeOK, blk
		case <-timer.C:
			cancel()
			s.logger.Debug("pre-produce P2P probe: timeout waiting first block",
				"peer", peerID.String(), "nextHeight", nextNum)
			return preProduceProbeFail, nil
		}
	}
	s.logger.Debug("pre-produce P2P probe: exhausted inflight retries", "peer", peerID.String())
	return preProduceProbeFail, nil
}
