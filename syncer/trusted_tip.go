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
	trustedBootHeightCacheTTL     = 2 * time.Second // GetStatus 直查 boot 高度的缓存时间
	// syncer 若干 INFO（quorum / best-not-ahead / force-bulk 等）仅在本机链尖连续未变达到该时长后打印。
	syncerLocalStallLogInterval = 20 * time.Second
)

const (
	trustedTipBranchNoConfig       = "no_configured_bootnodes"
	trustedTipBranchNoHeight         = "no_boot_height"
	trustedTipBranchMaxCluster       = "max_cluster_quorum"
	trustedTipBranchMedianCluster    = "median_cluster_quorum"
	trustedTipBranchSingleBoot       = "single_boot_fallback"
	trustedTipBranchLocalMaxAgree    = "local_max_boot_agree"
	trustedTipBranchNoQuorum         = "no_quorum"
	localMaxBootAgreeHeightSlack     = 3 // 本机链尖与 peerMap 最高 boot 高度差上限
)

const (
	heightSourceNone      = "none"
	heightSourceGetStatus = "get_status" // P2P SyncPeer.GetStatus，等同主动查对端链尖（非 gossip）
	heightSourceGossip    = "gossip"
)

type trustedBootPeerReport struct {
	PeerID         string
	InPeerMap      bool
	Number         uint64
	HeightSource   string
	SkippedOutlier bool
}

type trustedTipResult struct {
	Tip                uint64
	MaxBootHeight      uint64 // 任一已连接 bootnode 宣称的最高高度（可高于 quorum Tip）
	Quorum             bool
	ConnectedBoots     int
	ReportingBoots     int
	Median             uint64
	SingleBootFallback bool
	Branch             string
	Heights            []uint64
	// max 簇试算（便于对照为何未命中）
	MaxClusterNearMax  int
	MaxClusterFloor    uint64
	MaxClusterSpreadOK bool
}

// GetTrustedCanonicalTip returns the canonical chain tip from connected genesis bootnodes.
func (s *syncer) GetTrustedCanonicalTip() uint64 {
	local := uint64(0)
	if h := s.blockchain.Header(); h != nil {
		local = h.Number
	}
	return s.computeTrustedBootnodeTip(local).Tip
}

// fetchTrustedBootHeightsDirect 对每个创世 boot 优先 SyncPeer.GetStatus（读对端当前链尖），失败再回退 peerMap gossip。
func (s *syncer) fetchTrustedBootHeightsDirect(local uint64) ([]trustedBootPeerReport, []uint64) {
	reports := make([]trustedBootPeerReport, 0, len(s.trustedBootnodeIDs))
	heights := make([]uint64, 0, len(s.trustedBootnodeIDs))
	rpcOK := 0
	gossipFallback := 0

	for id := range s.trustedBootnodeIDs {
		rep := trustedBootPeerReport{
			PeerID:       id.String(),
			HeightSource: heightSourceNone,
		}
		usedRPC := false
		if s.syncPeerClient != nil {
			if status, err := s.syncPeerClient.GetPeerStatus(id); err == nil && status != nil {
				rep.InPeerMap = true
				rep.Number = status.Number
				rep.HeightSource = heightSourceGetStatus
				s.putToPeerMap(status)
				usedRPC = true
				rpcOK++
			}
		}
		if !usedRPC {
			if v, ok := s.peerMap.Load(id.String()); ok {
				if p, _ := v.(*NoForkPeer); p != nil {
					rep.InPeerMap = true
					rep.Number = p.Number
					rep.HeightSource = heightSourceGossip
					gossipFallback++
				}
			}
		}
		if rep.InPeerMap {
			if local > 0 && rep.Number > local+maxTrustedLeadOverLocal {
				rep.SkippedOutlier = true
				key := fmt.Sprintf("outlier:%s:%d", rep.PeerID, rep.Number)
				s.logTrustedTipThrottled(key, func() {
					s.logger.Info("syncer: ignore bootnode height far above local (outlier)",
						"peer", rep.PeerID,
						"peerNumber", rep.Number,
						"heightSource", rep.HeightSource,
						"localLatest", local,
						"maxLead", maxTrustedLeadOverLocal)
				})
			} else {
				heights = append(heights, rep.Number)
			}
		}
		reports = append(reports, rep)
	}

	sort.Slice(reports, func(i, j int) bool { return reports[i].PeerID < reports[j].PeerID })
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })

	s.logTrustedTipThrottled(fmt.Sprintf("direct:%d:%d:%d", rpcOK, gossipFallback, len(heights)), func() {
		s.logger.Info("syncer: trusted boot heights collected (GetStatus first, gossip fallback)",
			"configuredBootnodes", len(s.trustedBootnodeIDs),
			"getStatusOK", rpcOK,
			"gossipFallback", gossipFallback,
			"heightsForQuorum", heights,
			"localLatest", local)
	})
	return reports, heights
}

func (s *syncer) collectTrustedBootHeights(local uint64) ([]trustedBootPeerReport, []uint64) {
	s.trustedBootHeightMu.Lock()
	if time.Since(s.trustedBootHeightCachedAt) < trustedBootHeightCacheTTL &&
		len(s.trustedBootHeightHeights) > 0 {
		reports := append([]trustedBootPeerReport(nil), s.trustedBootHeightReports...)
		heights := append([]uint64(nil), s.trustedBootHeightHeights...)
		s.trustedBootHeightMu.Unlock()
		return reports, heights
	}
	s.trustedBootHeightMu.Unlock()

	reports, heights := s.fetchTrustedBootHeightsDirect(local)

	if len(heights) > 0 {
		s.trustedBootHeightMu.Lock()
		s.trustedBootHeightCachedAt = time.Now()
		s.trustedBootHeightReports = reports
		s.trustedBootHeightHeights = heights
		s.trustedBootHeightMu.Unlock()
	}
	return reports, heights
}

// tryLocalMaxBootAgree 本机链尖与 peerMap 最高 boot 接近，且至少一台 boot 落在 max 簇窗口内时采信 maxH。
// 用于多台 boot gossip 大面积滞后、仅一台已跟上链尖的场景（避免误 no_quorum）。
func tryLocalMaxBootAgree(local, maxH uint64, nearMax, reportingBoots int) (uint64, bool) {
	if maxH == 0 || nearMax < 1 || reportingBoots < trustedBootnodeQuorumK {
		return 0, false
	}
	if local > maxH {
		if local-maxH > localMaxBootAgreeHeightSlack {
			return 0, false
		}
	} else if maxH-local > localMaxBootAgreeHeightSlack {
		return 0, false
	}
	return maxH, true
}

// shouldLogOnLocalChainStall 本地链尖已连续 stall 时长未变（追块中高度在变则返回 false）。
func (s *syncer) shouldLogOnLocalChainStall(local uint64) bool {
	s.trustedQuorumLogMu.Lock()
	defer s.trustedQuorumLogMu.Unlock()
	now := time.Now()
	if local != s.trustedQuorumLogLocalHeight {
		s.trustedQuorumLogLocalHeight = local
		s.trustedQuorumLogLocalSince = now
		return false
	}
	return now.Sub(s.trustedQuorumLogLocalSince) >= syncerLocalStallLogInterval
}

func (s *syncer) shouldLogTrustedQuorumSuccess(local uint64) bool {
	return s.shouldLogOnLocalChainStall(local)
}

// logSyncInfoOnLocalStall 本地链尖 stall 后再按时间节流打 syncer INFO。
func (s *syncer) logSyncInfoOnLocalStall(local uint64, fn func()) {
	if !s.shouldLogOnLocalChainStall(local) {
		return
	}
	s.logBestPeerNotAheadStatusThrottled(fn)
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
	reports, heights := s.collectTrustedBootHeights(local)
	out := s.computeTrustedBootnodeQuorum(local, reports, heights)
	if out.Branch != trustedTipBranchNoQuorum {
		return out
	}
	if tip, ok := tryLocalMaxBootAgree(local, out.MaxBootHeight, out.MaxClusterNearMax, out.ReportingBoots); ok {
		out.Tip = tip
		out.Quorum = true
		out.Branch = trustedTipBranchLocalMaxAgree
		s.logTrustedTipThrottled(fmt.Sprintf("local-agree:%d", tip), func() {
			s.logger.Info("syncer: trusted bootnode tip (local agrees with max boot gossip height)",
				"trustedTip", tip,
				"localLatest", local,
				"maxBootHeight", out.MaxBootHeight,
				"nearMaxCount", out.MaxClusterNearMax,
				"note", "gossip 大面积滞后时 max-cluster K=2 未满足，本机与最高 boot 一致则采信 maxH")
		})
		return out
	}
	s.logTrustedTipThrottled(fmt.Sprintf("no-quorum:%d:%v", out.MaxBootHeight, out.Heights), func() {
		s.logger.Info("syncer: bootnode heights lack quorum; trusted tip unavailable (will fall back to gossip)",
			"median", out.Median,
			"minHeight", func() uint64 {
				if len(out.Heights) > 0 {
					return out.Heights[0]
				}
				return 0
			}(),
			"maxHeight", out.MaxBootHeight,
			"nearMaxCount", out.MaxClusterNearMax,
			"nearMaxRequiredK", trustedBootnodeQuorumK,
			"maxClusterFloor", out.MaxClusterFloor,
			"maxSpreadSlack", trustedBootnodeMaxSpreadSlack,
			"maxClusterSpreadOK", out.MaxClusterSpreadOK,
			"reportingBoots", out.ReportingBoots,
			"localLatest", local,
			"heights", out.Heights)
	})
	return out
}

func (s *syncer) computeTrustedBootnodeQuorum(local uint64, reports []trustedBootPeerReport, heights []uint64) trustedTipResult {
	out := trustedTipResult{}
	if len(s.trustedBootnodeIDs) == 0 {
		out.Branch = trustedTipBranchNoConfig
		return out
	}

	for _, r := range reports {
		if r.InPeerMap {
			out.ConnectedBoots++
		}
	}

	out.ReportingBoots = len(heights)
	out.Heights = append([]uint64(nil), heights...)

	if len(heights) == 0 {
		out.Branch = trustedTipBranchNoHeight
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
	out.MaxClusterSpreadOK = maxH-minH <= maxBootnodeHeightSpread
	if out.MaxClusterSpreadOK {
		floor := uint64(0)
		if maxH > trustedBootnodeMaxSpreadSlack {
			floor = maxH - trustedBootnodeMaxSpreadSlack
		}
		out.MaxClusterFloor = floor
		nearMax := 0
		for _, h := range heights {
			if h >= floor {
				nearMax++
			}
		}
		out.MaxClusterNearMax = nearMax
		if nearMax >= trustedBootnodeQuorumK {
			out.Tip = maxH
			out.Quorum = true
			out.Branch = trustedTipBranchMaxCluster
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
		if tip, ok := tryLocalMaxBootAgree(local, maxH, nearMax, out.ReportingBoots); ok {
			out.Tip = tip
			out.Quorum = true
			out.Branch = trustedTipBranchLocalMaxAgree
			s.logTrustedTipThrottled(fmt.Sprintf("local-agree:%d", tip), func() {
				s.logger.Info("syncer: trusted bootnode tip (local agrees with max boot gossip height)",
					"trustedTip", tip,
					"localLatest", local,
					"nearMaxCount", nearMax,
					"maxBootHeight", maxH)
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
		out.Branch = trustedTipBranchMedianCluster
		s.logTrustedQuorumSuccess(local, out.Tip, func() {
			s.logger.Info("syncer: trusted bootnode canonical tip (median-cluster quorum)",
				"trustedTip", out.Tip,
				"median", median,
				"clusterSize", len(cluster),
				"clusterHeights", cluster,
				"reportingBoots", out.ReportingBoots,
				"localLatest", local,
				"heights", heights)
		})
		return out
	}

	if len(heights) == 1 {
		out.Tip = heights[0]
		out.SingleBootFallback = true
		out.Branch = trustedTipBranchSingleBoot
		s.logTrustedTipThrottled(fmt.Sprintf("single:%d", out.Tip), func() {
			s.logger.Info("syncer: trusted bootnode tip from single connected boot (no K=2 quorum)",
				"trustedTip", out.Tip,
				"localLatest", local)
		})
		return out
	}

	out.Branch = trustedTipBranchNoQuorum
	out.Median = median
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
