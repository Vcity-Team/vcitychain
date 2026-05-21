package syncer

import (
	"fmt"
	"sort"
	"strings"
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
	trustedTipCalcLogInterval     = 5 * time.Second
	// syncer 若干 INFO（quorum / best-not-ahead / force-bulk 等）仅在本机链尖连续未变达到该时长后打印。
	syncerLocalStallLogInterval = 20 * time.Second
)

const (
	trustedTipBranchNoConfig     = "no_configured_bootnodes"
	trustedTipBranchNoHeight     = "no_boot_height_in_peer_map"
	trustedTipBranchMaxCluster   = "max_cluster_quorum"
	trustedTipBranchMedianCluster = "median_cluster_quorum"
	trustedTipBranchSingleBoot   = "single_boot_fallback"
	trustedTipBranchNoQuorum     = "no_quorum"
)

type trustedBootPeerReport struct {
	PeerID         string
	InPeerMap      bool
	Number         uint64
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

// logTrustedTipCalcThrottled 打印 trustedTip 完整计算过程（INFO）；本机明显超前时缩短节流便于排查。
func (s *syncer) logTrustedTipCalcThrottled(local uint64, out trustedTipResult, reports []trustedBootPeerReport, force bool, fn func()) {
	interval := trustedTipCalcLogInterval
	if force {
		interval = 0
	}
	key := fmt.Sprintf("calc:%d:%d:%s:%v", local, out.Tip, out.Branch, out.Heights)
	s.trustedTipLogMu.Lock()
	defer s.trustedTipLogMu.Unlock()
	now := time.Now()
	if !force && key == s.lastTrustedTipLogKey && now.Sub(s.lastTrustedTipLogAt) < interval {
		return
	}
	s.lastTrustedTipLogKey = key
	s.lastTrustedTipLogAt = now
	fn()
}

func (s *syncer) collectTrustedBootPeerReports(local uint64) (reports []trustedBootPeerReport, heights []uint64) {
	reports = make([]trustedBootPeerReport, 0, len(s.trustedBootnodeIDs))
	for id := range s.trustedBootnodeIDs {
		rep := trustedBootPeerReport{
			PeerID: id.String(),
		}
		if v, ok := s.peerMap.Load(id.String()); ok {
			p, _ := v.(*NoForkPeer)
			if p != nil {
				rep.InPeerMap = true
				rep.Number = p.Number
				if local > 0 && p.Number > local+maxTrustedLeadOverLocal {
					rep.SkippedOutlier = true
				} else {
					heights = append(heights, p.Number)
				}
			}
		}
		reports = append(reports, rep)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].PeerID < reports[j].PeerID })
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	return reports, heights
}

func formatTrustedBootPeerReports(reports []trustedBootPeerReport) string {
	parts := make([]string, 0, len(reports))
	for _, r := range reports {
		shortID := r.PeerID
		if len(shortID) > 16 {
			shortID = shortID[:16] + "…"
		}
		switch {
		case !r.InPeerMap:
			parts = append(parts, fmt.Sprintf("%s:not_in_peer_map", shortID))
		case r.SkippedOutlier:
			parts = append(parts, fmt.Sprintf("%s:%d(outlier_skip)", shortID, r.Number))
		default:
			parts = append(parts, fmt.Sprintf("%s:%d", shortID, r.Number))
		}
	}
	return strings.Join(parts, " ")
}

func (s *syncer) logTrustedTipCalculation(local uint64, out trustedTipResult, reports []trustedBootPeerReport, force bool) {
	s.logTrustedTipCalcThrottled(local, out, reports, force, func() {
		ahead := uint64(0)
		if local > 0 && out.Tip > 0 && local > out.Tip {
			ahead = local - out.Tip
		}
		s.logger.Info("syncer: trustedTip calculation detail",
			"localLatest", local,
			"trustedTip", out.Tip,
			"maxBootHeight", out.MaxBootHeight,
			"branch", out.Branch,
			"quorum", out.Quorum,
			"singleBootFallback", out.SingleBootFallback,
			"configuredBootnodes", len(s.trustedBootnodeIDs),
			"connectedBootsInPeerMap", out.ConnectedBoots,
			"reportingBootsUsedInQuorum", out.ReportingBoots,
			"heightsUsedInQuorum", out.Heights,
			"median", out.Median,
			"maxClusterSpreadOK", out.MaxClusterSpreadOK,
			"maxClusterFloor", out.MaxClusterFloor,
			"maxClusterNearMax", out.MaxClusterNearMax,
			"requiredK", trustedBootnodeQuorumK,
			"maxSpreadSlack", trustedBootnodeMaxSpreadSlack,
			"maxBootHeightSpread", maxBootnodeHeightSpread,
			"maxTrustedLeadOverLocal", maxTrustedLeadOverLocal,
			"aheadOfTrustedBlocks", ahead,
			"bootPeerReports", formatTrustedBootPeerReports(reports),
			"note", "trustedTip 仅来自创世 bootnode 且在 peerMap 中的 Number；未连接 boot 显示 not_in_peer_map")
	})
}

func (s *syncer) computeTrustedBootnodeTip(local uint64) trustedTipResult {
	out := trustedTipResult{}
	if len(s.trustedBootnodeIDs) == 0 {
		out.Branch = trustedTipBranchNoConfig
		s.logTrustedTipCalculation(local, out, nil, false)
		return out
	}

	reports, heights := s.collectTrustedBootPeerReports(local)
	for _, r := range reports {
		if r.InPeerMap {
			out.ConnectedBoots++
		}
		if r.InPeerMap && r.SkippedOutlier {
			key := fmt.Sprintf("outlier:%s:%d", r.PeerID, r.Number)
			s.logTrustedTipThrottled(key, func() {
				s.logger.Info("syncer: ignore bootnode height far above local (outlier)",
					"peer", r.PeerID,
					"peerNumber", r.Number,
					"localLatest", local,
					"maxLead", maxTrustedLeadOverLocal)
			})
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
		s.logTrustedTipCalculation(local, out, reports, true)
		return out
	}

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	median := heights[len(heights)/2]
	out.Median = median
	minH, maxH := heights[0], heights[len(heights)-1]
	out.MaxBootHeight = maxH
	forceLog := local > 0 && local > maxH+localAheadWarnBlocks

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
			if local > 0 && local > out.Tip+localAheadWarnBlocks {
				forceLog = true
			}
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
			s.logTrustedTipCalculation(local, out, reports, forceLog)
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
		if local > 0 && local > out.Tip+localAheadWarnBlocks {
			forceLog = true
		}
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
		s.logTrustedTipCalculation(local, out, reports, forceLog)
		return out
	}

	if len(heights) == 1 {
		out.Tip = heights[0]
		out.SingleBootFallback = true
		out.Branch = trustedTipBranchSingleBoot
		if local > 0 && local > out.Tip+localAheadWarnBlocks {
			forceLog = true
		}
		s.logTrustedTipThrottled(fmt.Sprintf("single:%d", out.Tip), func() {
			s.logger.Info("syncer: trusted bootnode tip from single connected boot (no K=2 quorum)",
				"trustedTip", out.Tip,
				"localLatest", local)
		})
		s.logTrustedTipCalculation(local, out, reports, forceLog)
		return out
	}

	out.Branch = trustedTipBranchNoQuorum
	s.logTrustedTipThrottled(fmt.Sprintf("no-quorum:%d:%v", maxH, heights), func() {
		s.logger.Info("syncer: bootnode heights lack quorum; trusted tip unavailable (will fall back to gossip)",
			"median", median,
			"minHeight", minH,
			"maxHeight", maxH,
			"nearMaxCount", out.MaxClusterNearMax,
			"nearMaxRequiredK", trustedBootnodeQuorumK,
			"maxClusterFloor", out.MaxClusterFloor,
			"maxSpreadSlack", trustedBootnodeMaxSpreadSlack,
			"maxClusterSpreadOK", out.MaxClusterSpreadOK,
			"reportingBoots", out.ReportingBoots,
			"localLatest", local,
			"heights", heights)
	})
	s.logTrustedTipCalculation(local, out, reports, true)
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
