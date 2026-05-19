package syncer

import (
	"fmt"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	trustedBootnodeQuorumK     = 2
	trustedBootnodeMedianSlack = 1
	// 链尖簇：至少 K 台 bootnode 高度落在 [maxH-spread, maxH]（gossip 有先后差，median±1 过严）。
	trustedBootnodeMaxSpreadSlack = 128
	maxTrustedLeadOverLocal       = 2048
	maxBootnodeHeightSpread       = 2048 // bootnode 间高度差超过此值视为分裂视图，不采信 max 簇
	localAheadWarnBlocks          = 64
	localAheadForceKickBlocks     = 256
	trustedTipLogInterval         = 15 * time.Second
	// quorum 成功 INFO 仅在本机链尖连续未变达到该时长后打印（追块中不刷）。
	trustedQuorumLogLocalStallInterval = 10 * time.Second
)

type trustedTipResult struct {
	Tip                uint64
	MaxBootHeight      uint64 // 任一已连接 bootnode 宣称的最高高度（可高于 quorum Tip）
	Quorum             bool
	ConnectedBoots     int
	ReportingBoots     int
	Median             uint64
	SingleBootFallback bool
}

// GetTrustedCanonicalTip returns the canonical chain tip from connected genesis bootnodes.
func (s *syncer) GetTrustedCanonicalTip() uint64 {
	local := uint64(0)
	if h := s.blockchain.Header(); h != nil {
		local = h.Number
	}
	return s.computeTrustedBootnodeTip(local).Tip
}

// shouldLogTrustedQuorumSuccess 仅在本地链尖已连续 stall 时长未变时允许打 quorum INFO（追块中高度在变则不打印）。
func (s *syncer) shouldLogTrustedQuorumSuccess(local uint64) bool {
	s.trustedQuorumLogMu.Lock()
	defer s.trustedQuorumLogMu.Unlock()
	now := time.Now()
	if local != s.trustedQuorumLogLocalHeight {
		s.trustedQuorumLogLocalHeight = local
		s.trustedQuorumLogLocalSince = now
		return false
	}
	return now.Sub(s.trustedQuorumLogLocalSince) >= trustedQuorumLogLocalStallInterval
}

func (s *syncer) logTrustedQuorumSuccess(local, tip uint64, logFn func()) {
	if !s.shouldLogTrustedQuorumSuccess(local) {
		return
	}
	if tip == s.lastLoggedTrustedTip {
		return
	}
	s.lastLoggedTrustedTip = tip
	logFn()
}

func (s *syncer) logTrustedTipThrottled(key string, fn func()) {
	s.trustedTipLogMu.Lock()
	defer s.trustedTipLogMu.Unlock()
	now := time.Now()
	if key == s.lastTrustedTipLogKey && now.Sub(s.lastTrustedTipLogAt) < trustedTipLogInterval {
		return
	}
	s.lastTrustedTipLogKey = key
	s.lastTrustedTipLogAt = now
	fn()
}

func (s *syncer) computeTrustedBootnodeTip(local uint64) trustedTipResult {
	out := trustedTipResult{}
	if len(s.trustedBootnodeIDs) == 0 {
		return out
	}

	var heights []uint64
	s.peerMap.Range(func(_ interface{}, value interface{}) bool {
		p, _ := value.(*NoForkPeer)
		if p == nil {
			return true
		}
		if !s.isTrustedBootnode(p.ID) {
			return true
		}
		out.ConnectedBoots++
		if local > 0 && p.Number > local+maxTrustedLeadOverLocal {
			key := fmt.Sprintf("outlier:%s:%d", p.ID.String(), p.Number)
			s.logTrustedTipThrottled(key, func() {
				s.logger.Info("syncer: ignore bootnode height far above local (outlier)",
					"peer", p.ID.String(),
					"peerNumber", p.Number,
					"localLatest", local,
					"maxLead", maxTrustedLeadOverLocal)
			})
			return true
		}
		heights = append(heights, p.Number)
		return true
	})

	out.ReportingBoots = len(heights)
	if len(heights) == 0 {
		s.logTrustedTipThrottled("no-height", func() {
			s.logger.Info("syncer: no trusted bootnode height available (not connected or no status)",
				"configuredBootnodes", len(s.trustedBootnodeIDs),
				"connectedBootnodes", out.ConnectedBoots)
		})
		return out
	}

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	median := heights[len(heights)/2]
	out.Median = median
	minH, maxH := heights[0], heights[len(heights)-1]
	out.MaxBootHeight = maxH

	// 1) max 簇 quorum：至少 K 台落在 [maxH-spread, maxH]
	if maxH-minH <= maxBootnodeHeightSpread {
		floor := uint64(0)
		if maxH > trustedBootnodeMaxSpreadSlack {
			floor = maxH - trustedBootnodeMaxSpreadSlack
		}
		nearMax := 0
		for _, h := range heights {
			if h >= floor {
				nearMax++
			}
		}
		if nearMax >= trustedBootnodeQuorumK {
			out.Tip = maxH
			out.Quorum = true
			s.logTrustedQuorumSuccess(local, out.Tip, func() {
				s.logger.Info("syncer: trusted bootnode canonical tip (max-cluster quorum)",
					"trustedTip", out.Tip,
					"nearMaxCount", nearMax,
					"requiredK", trustedBootnodeQuorumK,
					"maxSpreadSlack", trustedBootnodeMaxSpreadSlack,
					"reportingBoots", out.ReportingBoots,
					"connectedBoots", out.ConnectedBoots,
					"localLatest", local,
					"heights", heights)
			})
			return out
		}
	}

	// 2) median±slack 紧簇（高度非常接近时）
	cluster := make([]uint64, 0, len(heights))
	for _, h := range heights {
		if h >= median && h <= median+trustedBootnodeMedianSlack {
			cluster = append(cluster, h)
		}
	}
	if len(cluster) >= trustedBootnodeQuorumK {
		out.Tip = cluster[len(cluster)-1]
		out.Quorum = true
		s.logTrustedQuorumSuccess(local, out.Tip, func() {
			s.logger.Info("syncer: trusted bootnode canonical tip (median-cluster quorum)",
				"trustedTip", out.Tip,
				"median", median,
				"clusterSize", len(cluster),
				"reportingBoots", out.ReportingBoots,
				"localLatest", local)
		})
		return out
	}

	if len(heights) == 1 {
		out.Tip = heights[0]
		out.SingleBootFallback = true
		s.logTrustedTipThrottled(fmt.Sprintf("single:%d", out.Tip), func() {
			s.logger.Info("syncer: trusted bootnode tip from single connected boot (no K=2 quorum)",
				"trustedTip", out.Tip,
				"localLatest", local)
		})
		return out
	}

	s.logTrustedTipThrottled(fmt.Sprintf("no-quorum:%d:%v", maxH, heights), func() {
		s.logger.Info("syncer: bootnode heights lack quorum; trusted tip unavailable (will fall back to gossip)",
			"median", median,
			"minHeight", minH,
			"maxHeight", maxH,
			"nearMaxRequiredK", trustedBootnodeQuorumK,
			"maxSpreadSlack", trustedBootnodeMaxSpreadSlack,
			"reportingBoots", out.ReportingBoots,
			"localLatest", local)
	})
	return out
}

func (s *syncer) isTrustedBootnode(id peer.ID) bool {
	_, ok := s.trustedBootnodeIDs[id]
	return ok
}

// pickSyncPeerForTarget chooses a peer to bulk-sync from, preferring connected bootnodes that can serve blocks.
func (s *syncer) pickSyncPeerForTarget(local, trusted uint64, skip map[peer.ID]bool) *NoForkPeer {
	var bestBoot *NoForkPeer

	s.peerMap.Range(func(_ interface{}, value interface{}) bool {
		p, _ := value.(*NoForkPeer)
		if p == nil || skip[p.ID] || !s.isTrustedBootnode(p.ID) {
			return true
		}
		if p.Number <= local {
			return true
		}
		if bestBoot == nil || p.Number > bestBoot.Number {
			bestBoot = p
		}
		return true
	})
	if bestBoot != nil {
		return bestBoot
	}

	var anyBoot *NoForkPeer
	s.peerMap.Range(func(_ interface{}, value interface{}) bool {
		p, _ := value.(*NoForkPeer)
		if p == nil || skip[p.ID] || !s.isTrustedBootnode(p.ID) {
			return true
		}
		if anyBoot == nil || p.Number > anyBoot.Number {
			anyBoot = p
		}
		return true
	})
	if trusted > local && anyBoot != nil {
		return anyBoot
	}

	return s.peerMap.BestPeer(s.mergeSkipsForBestPeer(skip))
}
