package syncer

import (
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	advertVerifiedTTL    = 45 * time.Second
	advertIgnoreDuration = 2 * time.Minute
	advertProbeTimeout   = 5 * time.Second
	maxAdvertBestTries   = 8
)

type advertVerifiedEntry struct {
	claimed    uint64
	verifiedAt time.Time
}

type advertProbeResult int

const (
	advertProbeOK advertProbeResult = iota
	advertProbeBad
	advertProbeInflight
	advertProbeForkMismatch
)

// GetVerifiedBestPeerNumber 对宣称高于本地的 peer（BestPeer）尝试通过 GetBlocks 拉取 local+1，
// 且校验父哈希与本地链尖一致。拉取失败则短期内将该 peer 从 Best 候选中排除（冷却）；父哈希不一致视为分叉视图，不惩罚 peer，换下一个 Best。
// GetBlocks 已被主同步占用时返回 0，调用方应依赖 GetTrustedPeerNumber / 本地高度。
func (s *syncer) GetVerifiedBestPeerNumber() uint64 {
	hdr := s.blockchain.Header()
	if hdr == nil {
		return 0
	}
	local := hdr.Number
	localHash := hdr.Hash

	skip := s.advertSkipMap()

	for attempt := 0; attempt < maxAdvertBestTries; attempt++ {
		best := s.peerMap.BestPeer(skip)
		if best == nil || best.Number <= local {
			return 0
		}
		if s.advertVerifiedRecently(best.ID, best.Number) {
			return best.Number
		}
		switch s.probeAdvertisedNextBlock(best.ID, local, localHash) {
		case advertProbeOK:
			s.markAdvertVerified(best.ID, best.Number)
			return best.Number
		case advertProbeInflight:
			return 0
		case advertProbeForkMismatch:
			skip[best.ID] = true
			continue
		case advertProbeBad:
			s.markAdvertIgnore(best.ID)
			skip[best.ID] = true
			continue
		}
	}
	return 0
}

func (s *syncer) advertSkipMap() map[peer.ID]bool {
	s.advertMu.Lock()
	defer s.advertMu.Unlock()
	now := time.Now()
	m := make(map[peer.ID]bool)
	for id, until := range s.advertIgnoreUntil {
		if until.After(now) {
			m[id] = true
		} else {
			delete(s.advertIgnoreUntil, id)
		}
	}
	return m
}

func (s *syncer) advertVerifiedRecently(id peer.ID, claimed uint64) bool {
	s.advertMu.Lock()
	defer s.advertMu.Unlock()
	v, ok := s.advertVerified[id]
	if !ok || v.claimed != claimed {
		return false
	}
	if time.Since(v.verifiedAt) > advertVerifiedTTL {
		delete(s.advertVerified, id)
		return false
	}
	return true
}

func (s *syncer) markAdvertVerified(id peer.ID, claimed uint64) {
	s.advertMu.Lock()
	defer s.advertMu.Unlock()
	s.advertVerified[id] = advertVerifiedEntry{claimed: claimed, verifiedAt: time.Now()}
}

func (s *syncer) markAdvertIgnore(id peer.ID) {
	s.advertMu.Lock()
	defer s.advertMu.Unlock()
	if s.advertIgnoreUntil == nil {
		s.advertIgnoreUntil = make(map[peer.ID]time.Time)
	}
	s.advertIgnoreUntil[id] = time.Now().Add(advertIgnoreDuration)
	s.logger.Debug("advert verify: peer claimed height ignored (cooldown)",
		"peer", id.String(),
		"cooldown", advertIgnoreDuration.String())
}

func (s *syncer) probeAdvertisedNextBlock(peerID peer.ID, localNum uint64, localHash types.Hash) advertProbeResult {
	nextNum := localNum + 1
	blockCh, cancel, err := s.syncPeerClient.GetBlocks(peerID, nextNum, advertProbeTimeout)
	if err != nil {
		if strings.Contains(err.Error(), "already in progress") {
			return advertProbeInflight
		}
		return advertProbeBad
	}
	defer cancel()

	timer := time.NewTimer(advertProbeTimeout + 2*time.Second)
	defer timer.Stop()

	select {
	case blk := <-blockCh:
		if blk == nil {
			return advertProbeBad
		}
		if blk.Number() != nextNum {
			return advertProbeBad
		}
		if blk.ParentHash() != localHash {
			return advertProbeForkMismatch
		}
		return advertProbeOK
	case <-timer.C:
		return advertProbeBad
	}
}
