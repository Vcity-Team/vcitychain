package syncer

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	trustedBootnodeQuorumK     = 2
	trustedBootnodeMedianSlack = 1
	// 链尖簇：至少 K 台 bootnode 高度落在 [maxH-spread, maxH]（gossip 有先后差，median±1 过严）。
	trustedBootnodeMaxSpreadSlack = 128
	maxBootnodeHeightSpread       = 2048 // bootnode 间高度差超过此值视为分裂视图，不采信 max 簇
	localAheadWarnBlocks          = 64
	localAheadForceKickBlocks     = 256
	trustedTipLogInterval              = 15 * time.Second
	trustedBootHeightCacheTTL          = 2 * time.Second // 常态下 boot eth_blockNumber 缓存
	trustedBootHeightCacheTTLCatchUp   = 8 * time.Second // 追块中降低 RPC 频率
	bootJSONRPCFailLogInterval         = 10 * time.Second // boot eth_blockNumber 失败 WARN 合并节流
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
	PeerID       string
	InPeerMap    bool
	Number       uint64
	HeightSource string
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
	// hash 多数表决（local 高度）
	MajorityHash  types.Hash
	MajorityVotes int
	HashOutliers  []peer.ID
}

// GetTrustedCanonicalTip returns the canonical chain tip from connected genesis bootnodes.
func (s *syncer) GetTrustedCanonicalTip() uint64 {
	local := uint64(0)
	if h := s.blockchain.Header(); h != nil {
		local = h.Number
	}
	return s.computeTrustedBootnodeTip(local).Tip
}

type bootHeightFetchResult struct {
	rep      trustedBootPeerReport
	height   uint64
	hasHeight bool
	jsonOK   bool
	jsonFail bool
	noURL    bool
	failPeer peer.ID
	failRPC  string
	failErr  error
}

// fetchTrustedBootHeightsDirect 对每个创世 boot 并行 HTTP eth_blockNumber（boot IP + jsonrpc 端口）。
func (s *syncer) fetchTrustedBootHeightsDirect(local uint64) ([]trustedBootPeerReport, []uint64) {
	ids := make([]peer.ID, 0, len(s.trustedBootnodeIDs))
	for id := range s.trustedBootnodeIDs {
		ids = append(ids, id)
	}
	results := make([]bootHeightFetchResult, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		i, id := i, id
		rep := trustedBootPeerReport{
			PeerID:       id.String(),
			HeightSource: heightSourceNone,
		}
		rpcURL := s.trustedBootJSONRPC[id]
		if rpcURL == "" {
			results[i] = bootHeightFetchResult{rep: rep, noURL: true}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), bootJSONRPCTimeout)
			n, err := fetchEthBlockNumber(ctx, rpcURL)
			cancel()
			if err != nil {
				results[i] = bootHeightFetchResult{
					rep:      rep,
					jsonFail: true,
					failPeer: id,
					failRPC:  rpcURL,
					failErr:  err,
				}
				return
			}
			rep.InPeerMap = true
			rep.Number = n
			rep.HeightSource = heightSourceJSONRPC
			s.setTrustedBootRPCHeight(id, n)
			results[i] = bootHeightFetchResult{
				rep:       rep,
				height:    n,
				hasHeight: true,
				jsonOK:    true,
			}
		}()
	}
	wg.Wait()

	reports := make([]trustedBootPeerReport, 0, len(ids))
	heights := make([]uint64, 0, len(ids))
	jsonRpcOK := 0
	jsonRpcFail := 0
	noURL := 0
	var jsonRpcFails []bootJSONRPCFailReport
	for _, res := range results {
		if res.noURL {
			noURL++
			reports = append(reports, res.rep)
			continue
		}
		if res.jsonFail {
			jsonRpcFail++
			jsonRpcFails = append(jsonRpcFails, bootJSONRPCFailReport{
				peer: res.failPeer,
				rpc:  res.failRPC,
				err:  res.failErr,
			})
			reports = append(reports, res.rep)
			continue
		}
		if res.jsonOK {
			jsonRpcOK++
		}
		reports = append(reports, res.rep)
		if res.hasHeight {
			heights = append(heights, res.height)
		}
	}

	sort.Slice(reports, func(i, j int) bool { return reports[i].PeerID < reports[j].PeerID })
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })

	if len(jsonRpcFails) > 0 {
		s.logBootJSONRPCFailuresThrottled(jsonRpcFails)
	}

	s.logTrustedTipThrottled(fmt.Sprintf("direct:%d:%d:%d", jsonRpcOK, jsonRpcFail, len(heights)), func() {
		s.logger.Info("syncer: trusted boot heights collected (boot IP + jsonrpc_addr port, eth_blockNumber only)",
			"configuredBootnodes", len(s.trustedBootnodeIDs),
			"jsonRpcOK", jsonRpcOK,
			"jsonRpcFail", jsonRpcFail,
			"noJsonRpcURL", noURL,
			"heightsForQuorum", heights,
			"localLatest", local)
	})
	return reports, heights
}

func (s *syncer) trustedBootHeightCacheTTLFor(local uint64) time.Duration {
	if s.syncCatchUpActive.Load() > 0 {
		return trustedBootHeightCacheTTLCatchUp
	}
	s.trustedBootHeightMu.Lock()
	defer s.trustedBootHeightMu.Unlock()
	for _, h := range s.trustedBootHeightHeights {
		if h > local {
			return trustedBootHeightCacheTTLCatchUp
		}
	}
	return trustedBootHeightCacheTTL
}

func (s *syncer) collectTrustedBootHeights(local uint64) ([]trustedBootPeerReport, []uint64) {
	ttl := s.trustedBootHeightCacheTTLFor(local)
	s.trustedBootHeightMu.Lock()
	if time.Since(s.trustedBootHeightCachedAt) < ttl &&
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

type bootJSONRPCFailReport struct {
	peer peer.ID
	rpc  string
	err  error
}

// logBootJSONRPCFailuresThrottled 合并打印本轮所有 boot RPC 失败，全局最多每 10 秒一条 WARN。
func (s *syncer) logBootJSONRPCFailuresThrottled(fails []bootJSONRPCFailReport) {
	if len(fails) == 0 {
		return
	}
	s.bootJSONRPCFailLogMu.Lock()
	defer s.bootJSONRPCFailLogMu.Unlock()
	now := time.Now()
	if !s.lastBootJSONRPCFailLogAt.IsZero() && now.Sub(s.lastBootJSONRPCFailLogAt) < bootJSONRPCFailLogInterval {
		return
	}
	s.lastBootJSONRPCFailLogAt = now

	details := make([]string, 0, len(fails))
	for _, f := range fails {
		details = append(details, fmt.Sprintf("peer=%s jsonRpc=%s err=%v",
			f.peer.String(), normalizeJSONRPCEndpoint(f.rpc), f.err))
	}
	s.logger.Warn("syncer: bootnode eth_blockNumber failed (no fallback)",
		"failCount", len(fails),
		"failures", details)
}

// refreshTrustedMetaForCatchUp 强制刷新 boot RPC 高度后重算 quorum tip（用于 catch-up burst，避免缓存 tip 误判已追平）。
func (s *syncer) refreshTrustedMetaForCatchUp(local uint64) trustedTipResult {
	s.invalidateTrustedBootHeightCache()
	return s.computeTrustedBootnodeTip(local)
}

func (s *syncer) computeTrustedBootnodeTip(local uint64) trustedTipResult {
	reports, heights := s.collectTrustedBootHeights(local)

	var hashReports []bootHashReport
	var hashMaj bootHashMajorityResult
	if local > 0 {
		hashReports = s.collectTrustedBootHashReports(local)
		hashMaj = computeBootHashMajority(local, hashReports)
		if hashMaj.ok {
			return s.trustedTipFromHashMajority(local, reports, heights, hashReports, hashMaj)
		}
		if hashMaj.totalVotes > 0 {
			s.logTrustedTipThrottled(fmt.Sprintf("hash-split:%d", local), func() {
				s.logger.Warn("syncer: boot hash split at local height, no majority; trusted tip withheld",
					"localLatest", local,
					"hashVotes", hashMaj.totalVotes,
					"requiredVotes", hashQuorumRequiredVotes(hashMaj.totalVotes),
					"heightsForQuorum", heights)
			})
		}
	}

	out := s.computeTrustedBootnodeQuorum(local, reports, heights)
	if hashMaj.totalVotes > 0 && !hashMaj.ok && len(heights) >= 2 && out.Branch == trustedTipBranchMaxCluster {
		sorted := append([]uint64(nil), heights...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		if sorted[len(sorted)-1]-sorted[0] > forkSplitHeightThreshold {
			s.logTrustedTipThrottled(fmt.Sprintf("height-split:%d", local), func() {
				s.logger.Warn("syncer: boot height spread too large without hash majority; ignore max-cluster tip",
					"localLatest", local,
					"minHeight", sorted[0],
					"maxHeight", sorted[len(sorted)-1],
					"wouldBeTip", out.Tip,
					"heights", heights)
			})
			out.Quorum = false
			out.Tip = 0
			out.Branch = trustedTipBranchHashSplit
		}
	}
	if out.Branch != trustedTipBranchNoQuorum && out.Branch != trustedTipBranchHashSplit {
		return out
	}
	// hash 已分裂时勿用 lone max 高度旁路（否则 6×487+1×52556 仍可能采信 52556）
	if hashMaj.totalVotes > 0 && !hashMaj.ok {
		s.logTrustedTipThrottled(fmt.Sprintf("no-local-max-agree-hash-split:%d", local), func() {
			s.logger.Warn("syncer: skip local-max-boot-agree because boot hash has no majority",
				"localLatest", local,
				"hashVotes", hashMaj.totalVotes,
				"heightsForQuorum", heights)
		})
	} else if tip, ok := tryLocalMaxBootAgree(local, out.MaxBootHeight, out.MaxClusterNearMax, out.ReportingBoots); ok {
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

// isBetterBulkBootThan 在均可供块时优先更高 P2P 高度，同高则优先 RPC 更高的 boot。
func (p *NoForkPeer) isBetterBulkBootThan(other *NoForkPeer, s *syncer) bool {
	if other == nil {
		return true
	}
	if p.Number != other.Number {
		return p.Number > other.Number
	}
	rpcA, okA := s.getTrustedBootRPCHeight(p.ID)
	rpcB, okB := s.getTrustedBootRPCHeight(other.ID)
	if okA && okB && rpcA != rpcB {
		return rpcA > rpcB
	}
	return p.Distance != nil && other.Distance != nil && p.Distance.Cmp(other.Distance) < 0
}

func (s *syncer) isTrustedBootnode(id peer.ID) bool {
	_, ok := s.trustedBootnodeIDs[id]
	return ok
}

// peerExcludedFromBulkPull 为 true 时该 peer 不参与 force-bulk 选源（skipList + pull-distrust 冷却）。
func (s *syncer) peerExcludedFromBulkPull(id peer.ID, skip map[peer.ID]bool) bool {
	if skip != nil && skip[id] {
		return true
	}
	if s.isBootHashOutlier(id) {
		return true
	}
	s.pullDistrustMu.Lock()
	until, inCooldown := s.pullDistrustUntil[id]
	s.pullDistrustMu.Unlock()
	return inCooldown && until.After(time.Now())
}

// pickSyncPeerForTarget chooses a peer to bulk-sync from.
// When forceBulk (behind trusted boot RPC tip): only genesis bootnodes with P2P Number > local; no anyBoot/BestPeer fallback.
func (s *syncer) pickSyncPeerForTarget(local, syncTarget uint64, skip map[peer.ID]bool, forceBulk bool) *NoForkPeer {
	bulkSkip := s.mergeSkipsForBestPeer(skip)
	var bestBoot *NoForkPeer

	s.peerMap.Range(func(_ interface{}, value interface{}) bool {
		p, _ := value.(*NoForkPeer)
		if p == nil || s.peerExcludedFromBulkPull(p.ID, bulkSkip) || !s.isTrustedBootnode(p.ID) {
			return true
		}
		if p.Number <= local {
			return true
		}
		if bestBoot == nil || p.isBetterBulkBootThan(bestBoot, s) {
			bestBoot = p
		}
		return true
	})
	if bestBoot != nil {
		return bestBoot
	}

	if forceBulk && syncTarget > local {
		if p := s.pickBootPeerForTrustedBulk(local, syncTarget, bulkSkip); p != nil && p.Number > local {
			return p
		}
	}

	if forceBulk {
		return nil
	}

	return s.peerMap.BestPeer(s.mergeSkipsForBestPeer(skip))
}

// pickBootPeerForTrustedBulk 在 P2P 宣称不超前时，用 boot RPC/syncTarget 作为 bulk 逻辑高度选源。
// 按 bulkBootRotateIdx 轮换同高度 boot；跳过 bulkSkip 与 pull-distrust 中的 peer。
func (s *syncer) pickBootPeerForTrustedBulk(local, syncTarget uint64, bulkSkip map[peer.ID]bool) *NoForkPeer {
	if syncTarget <= local {
		return nil
	}
	type cand struct {
		id    peer.ID
		rpcH  uint64
		bulkH uint64
	}
	var list []cand
	for id := range s.trustedBootnodeIDs {
		if s.peerExcludedFromBulkPull(id, bulkSkip) || s.isBootHashOutlier(id) {
			continue
		}
		rpcH := uint64(0)
		if rpc, ok := s.getTrustedBootRPCHeight(id); ok {
			rpcH = rpc
		}
		bulkH := syncTarget
		if rpcH > bulkH {
			bulkH = rpcH
		}
		if bulkH <= local {
			continue
		}
		list = append(list, cand{id: id, rpcH: rpcH, bulkH: bulkH})
	}
	if len(list) == 0 {
		return nil
	}
	// 在多数派 boot 中选较高 RPC（排除 hash outlier 后），避免 lone max 孤链源。
	sort.Slice(list, func(i, j int) bool {
		if list[i].rpcH != list[j].rpcH {
			return list[i].rpcH > list[j].rpcH
		}
		if list[i].bulkH != list[j].bulkH {
			return list[i].bulkH > list[j].bulkH
		}
		return list[i].id.String() < list[j].id.String()
	})
	n := len(list)
	start := int(s.bulkBootRotateIdx.Load()) % n
	for i := 0; i < n; i++ {
		best := list[(start+i)%n]
		var base *NoForkPeer
		if v, ok := s.peerMap.Load(best.id.String()); ok {
			if p, _ := v.(*NoForkPeer); p != nil {
				cp := *p
				base = &cp
			}
		}
		if base == nil {
			base = &NoForkPeer{ID: best.id, Distance: big.NewInt(0)}
		}
		if best.bulkH > base.Number {
			base.Number = best.bulkH
		}
		return base
	}
	return nil
}
