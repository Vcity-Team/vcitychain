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

// TryProbeCanonicalNextBeforeProduce 出块前从 P2P 拉取下一高度；若成功且可接到本地链尖，则返回 true（调用方应放弃本地构建）。
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
		class, blockHash := s.probeOnePeerPreProduce(pid, nextNum, localHash, probeTimeout)
		switch class {
		case preProduceProbeOK:
			s.logger.Info("⏸️ 【出块前 P2P 探测】放弃本轮本地构建（某 peer 已持有可衔接的下一高度）",
				"peer", pid.String(),
				"nextHeight", nextNum,
				"blockHash", blockHash.String())
			return true
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

func (s *syncer) probeOnePeerPreProduce(peerID peer.ID, nextNum uint64, localHash types.Hash, probeTimeout time.Duration) (preProduceProbeClass, types.Hash) {
	var zero types.Hash
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
			return preProduceProbeFail, zero
		}

		timer := time.NewTimer(probeTimeout)
		select {
		case blk := <-blockCh:
			cancel()
			if !timer.Stop() {
				<-timer.C
			}
			if blk == nil {
				return preProduceProbeFail, zero
			}
			if blk.Number() != nextNum {
				s.logger.Debug("pre-produce P2P probe: unexpected block number",
					"peer", peerID.String(), "want", nextNum, "got", blk.Number())
				return preProduceProbeFail, zero
			}
			if blk.ParentHash() != localHash {
				return preProduceProbeFork, zero
			}
			return preProduceProbeOK, blk.Hash()
		case <-timer.C:
			cancel()
			s.logger.Debug("pre-produce P2P probe: timeout waiting first block",
				"peer", peerID.String(), "nextHeight", nextNum)
			return preProduceProbeFail, zero
		}
	}
	s.logger.Debug("pre-produce P2P probe: exhausted inflight retries", "peer", peerID.String())
	return preProduceProbeFail, zero
}
