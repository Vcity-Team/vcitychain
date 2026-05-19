package syncer

import (
	"sort"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// 至少 K 台 bootnode 高度落在 [median, median+slack] 内才采信。
	trustedBootnodeQuorumK      = 2
	trustedBootnodeMedianSlack  = 1
	maxTrustedLeadOverLocal     = 2048
	localAheadWarnBlocks        = 64
	localAheadForceKickBlocks   = 256
)

type trustedTipResult struct {
	Tip               uint64
	Quorum            bool
	ConnectedBoots    int
	ReportingBoots    int
	Median            uint64
	SingleBootFallback bool
}

// GetTrustedCanonicalTip returns the canonical chain tip from connected genesis bootnodes (median+K=2).
// Returns 0 when no bootnodes are configured or none are connected with a usable quorum.
func (s *syncer) GetTrustedCanonicalTip() uint64 {
	local := uint64(0)
	if h := s.blockchain.Header(); h != nil {
		local = h.Number
	}
	return s.computeTrustedBootnodeTip(local).Tip
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
			s.logger.Info("syncer: ignore bootnode height far above local (outlier)",
				"peer", p.ID.String(),
				"peerNumber", p.Number,
				"localLatest", local,
				"maxLead", maxTrustedLeadOverLocal)
			return true
		}
		heights = append(heights, p.Number)
		return true
	})

	out.ReportingBoots = len(heights)
	if len(heights) == 0 {
		s.logger.Info("syncer: no trusted bootnode height available (not connected or no status)",
			"configuredBootnodes", len(s.trustedBootnodeIDs),
			"connectedBootnodes", out.ConnectedBoots)
		return out
	}

	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	median := heights[len(heights)/2]
	out.Median = median

	cluster := make([]uint64, 0, len(heights))
	for _, h := range heights {
		if h >= median && h <= median+trustedBootnodeMedianSlack {
			cluster = append(cluster, h)
		}
	}

	if len(cluster) >= trustedBootnodeQuorumK {
		out.Tip = cluster[len(cluster)-1]
		out.Quorum = true
		if out.Tip != s.lastLoggedTrustedTip {
			s.lastLoggedTrustedTip = out.Tip
			s.logger.Info("syncer: trusted bootnode canonical tip (median+K quorum)",
				"trustedTip", out.Tip,
				"median", median,
				"clusterSize", len(cluster),
				"reportingBoots", out.ReportingBoots,
				"connectedBoots", out.ConnectedBoots,
				"localLatest", local)
		}
		return out
	}

	if len(heights) == 1 {
		out.Tip = heights[0]
		out.SingleBootFallback = true
		s.logger.Info("syncer: trusted bootnode tip from single connected boot (no K=2 quorum)",
			"trustedTip", out.Tip,
			"peer", "only-one",
			"localLatest", local)
		return out
	}

	s.logger.Info("syncer: bootnode heights lack median+K=2 quorum; trusted tip unavailable",
		"median", median,
		"clusterSize", len(cluster),
		"requiredK", trustedBootnodeQuorumK,
		"heights", heights,
		"localLatest", local)
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

	// Any bootnode with highest declared height (may equal local; caller checks force path).
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
