package syncer

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	trustedTipBranchHashMajority = "hash_majority_quorum"
	trustedTipBranchHashSplit     = "hash_split_no_quorum"

	bootHashQuorumMinVotes       = 3              // 绝对下限（7 台 boot 时仍须至少 3 票同 hash）
	bootHashOutlierDistrust      = 10 * time.Minute
	forkSplitHeightThreshold     = 32             // 高度差超过此值且 hash 分裂时不采信 lone max RPC
	hashMajorityRollbackMaxDepth = 128
)

type bootHashReport struct {
	peer      peer.ID
	rpcHeight uint64
	hash      types.Hash
	hashOK    bool
}

// bootHashMajorityResult 在高度 local 上对 boot 块 hash 投票的结果。
type bootHashMajorityResult struct {
	ok            bool
	majorityHash  types.Hash
	majorityVotes int
	tip           uint64
	outliers      []peer.ID
	totalVotes    int
}

func hashQuorumRequiredVotes(reporting int) int {
	if reporting <= 0 {
		return bootHashQuorumMinVotes
	}
	need := reporting/2 + 1
	if need < bootHashQuorumMinVotes {
		need = bootHashQuorumMinVotes
	}
	if need > reporting {
		need = reporting
	}
	return need
}

func computeBootHashMajority(local uint64, reports []bootHashReport) bootHashMajorityResult {
	out := bootHashMajorityResult{}
	if local == 0 || len(reports) == 0 {
		return out
	}
	type hashCount struct {
		hash  types.Hash
		votes int
		peers []peer.ID
		maxRPC uint64
	}
	counts := make(map[string]*hashCount)
	var voted int
	for _, r := range reports {
		if !r.hashOK {
			continue
		}
		voted++
		key := r.hash.String()
		c, ok := counts[key]
		if !ok {
			c = &hashCount{hash: r.hash, peers: make([]peer.ID, 0, 4)}
			counts[key] = c
		}
		c.votes++
		c.peers = append(c.peers, r.peer)
		if r.rpcHeight > c.maxRPC {
			c.maxRPC = r.rpcHeight
		}
	}
	out.totalVotes = voted
	if voted == 0 {
		return out
	}
	need := hashQuorumRequiredVotes(voted)
	var best *hashCount
	for _, c := range counts {
		if c.votes < need {
			continue
		}
		if best == nil || c.votes > best.votes || (c.votes == best.votes && c.maxRPC > best.maxRPC) {
			best = c
		}
	}
	if best == nil {
		return out
	}
	out.ok = true
	out.majorityHash = best.hash
	out.majorityVotes = best.votes
	majorityKey := best.hash.String()
	// tip = 多数派 boot 的最高 RPC（用于追块门禁），不含 outlier 虚高
	out.tip = local
	for _, r := range reports {
		if !r.hashOK || r.hash.String() != majorityKey {
			continue
		}
		if r.rpcHeight > out.tip {
			out.tip = r.rpcHeight
		}
	}
	for _, r := range reports {
		if !r.hashOK {
			continue
		}
		if r.hash.String() != majorityKey {
			out.outliers = append(out.outliers, r.peer)
		}
	}
	sort.Slice(out.outliers, func(i, j int) bool {
		return out.outliers[i].String() < out.outliers[j].String()
	})
	return out
}

func (s *syncer) invalidateTrustedBootHashCache() {
	s.trustedBootHashMu.Lock()
	defer s.trustedBootHashMu.Unlock()
	s.trustedBootHashCachedAt = time.Time{}
	s.trustedBootHashLocal = 0
	s.trustedBootHashReports = nil
}

func (s *syncer) collectTrustedBootHashReports(local uint64) []bootHashReport {
	if local == 0 {
		return nil
	}
	ttl := s.trustedBootHeightCacheTTLFor(local)
	s.trustedBootHashMu.Lock()
	if time.Since(s.trustedBootHashCachedAt) < ttl &&
		s.trustedBootHashLocal == local &&
		len(s.trustedBootHashReports) > 0 {
		out := append([]bootHashReport(nil), s.trustedBootHashReports...)
		s.trustedBootHashMu.Unlock()
		return out
	}
	s.trustedBootHashMu.Unlock()
	reports := s.fetchTrustedBootHashReportsDirect(local)
	if len(reports) > 0 {
		s.trustedBootHashMu.Lock()
		s.trustedBootHashCachedAt = time.Now()
		s.trustedBootHashLocal = local
		s.trustedBootHashReports = append([]bootHashReport(nil), reports...)
		s.trustedBootHashMu.Unlock()
	}
	return reports
}

func (s *syncer) fetchTrustedBootHashReportsDirect(local uint64) []bootHashReport {
	if local == 0 {
		return nil
	}
	ids := make([]peer.ID, 0, len(s.trustedBootnodeIDs))
	for id := range s.trustedBootnodeIDs {
		ids = append(ids, id)
	}
	reports := make([]bootHashReport, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		i, id := i, id
		rpcURL := s.trustedBootJSONRPC[id]
		if rpcURL == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), bootJSONRPCTimeout)
			defer cancel()
			n, err := fetchEthBlockNumber(ctx, rpcURL)
			if err != nil {
				return
			}
			s.setTrustedBootRPCHeight(id, n)
			if n < local {
				reports[i] = bootHashReport{peer: id, rpcHeight: n, hashOK: false}
				return
			}
			h, err := fetchEthBlockHashByNumber(ctx, rpcURL, local)
			if err != nil {
				reports[i] = bootHashReport{peer: id, rpcHeight: n, hashOK: false}
				return
			}
			reports[i] = bootHashReport{
				peer:      id,
				rpcHeight: n,
				hash:      h,
				hashOK:    true,
			}
		}()
	}
	wg.Wait()
	out := make([]bootHashReport, 0, len(ids))
	for _, r := range reports {
		if r.peer != "" {
			out = append(out, r)
		}
	}
	return out
}

func (s *syncer) isBootHashOutlier(id peer.ID) bool {
	s.bootHashOutlierMu.Lock()
	defer s.bootHashOutlierMu.Unlock()
	until, ok := s.bootHashOutlierUntil[id]
	return ok && until.After(time.Now())
}

func (s *syncer) markBootHashOutliers(outliers []peer.ID) {
	if len(outliers) == 0 {
		return
	}
	until := time.Now().Add(bootHashOutlierDistrust)
	s.bootHashOutlierMu.Lock()
	defer s.bootHashOutlierMu.Unlock()
	if s.bootHashOutlierUntil == nil {
		s.bootHashOutlierUntil = make(map[peer.ID]time.Time)
	}
	for _, id := range outliers {
		s.bootHashOutlierUntil[id] = until
	}
}

func (s *syncer) clearBootHashOutlier(id peer.ID) {
	s.bootHashOutlierMu.Lock()
	defer s.bootHashOutlierMu.Unlock()
	delete(s.bootHashOutlierUntil, id)
}

func (s *syncer) clearBootHashOutliersMatching(majority types.Hash, reports []bootHashReport) {
	for _, r := range reports {
		if r.hashOK && r.hash == majority {
			s.clearBootHashOutlier(r.peer)
		}
	}
}

func (s *syncer) trustedTipFromHashMajority(local uint64, heightReports []trustedBootPeerReport, heights []uint64, hashReports []bootHashReport, maj bootHashMajorityResult) trustedTipResult {
	s.markBootHashOutliers(maj.outliers)
	s.clearBootHashOutliersMatching(maj.majorityHash, hashReports)

	out := trustedTipResult{
		Tip:            maj.tip,
		MaxBootHeight:  maj.tip, // 勿用全量 heights 的 max（含 outlier 虚高），否则 syncTarget 仍会被抬到孤链高度
		Quorum:         true,
		ReportingBoots: len(heights),
		Heights:        append([]uint64(nil), heights...),
		Branch:         trustedTipBranchHashMajority,
		MajorityHash:   maj.majorityHash,
		MajorityVotes:  maj.majorityVotes,
		HashOutliers:   append([]peer.ID(nil), maj.outliers...),
	}
	for _, r := range heightReports {
		if r.InPeerMap {
			out.ConnectedBoots++
		}
	}
	if len(heights) > 0 {
		sort.Slice(out.Heights, func(i, j int) bool { return out.Heights[i] < out.Heights[j] })
		out.Median = out.Heights[len(out.Heights)/2]
	}

	s.logTrustedQuorumSuccess(local, out.Tip, func() {
		s.logger.Info("syncer: trusted bootnode canonical tip (hash majority at local height)",
			"trustedTip", out.Tip,
			"localLatest", local,
			"majorityHash", maj.majorityHash.String(),
			"majorityVotes", maj.majorityVotes,
			"hashVoteTotal", maj.totalVotes,
			"outlierCount", len(maj.outliers),
			"heightsForQuorum", heights)
	})
	return out
}

func maxUint64Slice(v []uint64) uint64 {
	var m uint64
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// bootHashDisagreesWithLocal 在 local 高度上，是否有足够多 boot 与本地 head hash 不一致（无需完整 majority 也可判定 minority）。
func bootHashDisagreesWithLocal(localHash types.Hash, reports []bootHashReport) (disagree int, agree int) {
	for _, r := range reports {
		if !r.hashOK {
			continue
		}
		if r.hash == localHash {
			agree++
		} else {
			disagree++
		}
	}
	return disagree, agree
}

// LocalHeadMatchesBootMajority 本机链尖 hash 是否与 boot 多数派在 local 高度一致。
func (s *syncer) LocalHeadMatchesBootMajority() (match bool, majority types.Hash, votes int, ok bool) {
	hdr := s.blockchain.Header()
	if hdr == nil {
		return false, types.Hash{}, 0, false
	}
	local := hdr.Number
	hashReports := s.collectTrustedBootHashReports(local)
	maj := computeBootHashMajority(local, hashReports)
	if !maj.ok {
		return false, types.Hash{}, 0, false
	}
	return hdr.Hash == maj.majorityHash, maj.majorityHash, maj.majorityVotes, true
}

// maybeRollbackIfMinorityFork 本机 head 与 boot hash 多数不一致时回滚到 local-1。
func (s *syncer) maybeRollbackIfMinorityFork(reason string) bool {
	hdr := s.blockchain.Header()
	if hdr == nil || hdr.Number == 0 {
		return false
	}
	match, majority, votes, ok := s.LocalHeadMatchesBootMajority()
	if !ok || match {
		return false
	}
	rb, canRollback := s.blockchain.(interface{ RollbackToHeight(uint64) error })
	if !canRollback {
		s.logger.Warn("syncer: local minority fork but blockchain cannot rollback",
			"reason", reason,
			"localLatest", hdr.Number,
			"localHash", hdr.Hash.String(),
			"majorityHash", majority.String(),
			"majorityVotes", votes)
		return false
	}

	rolled := false
	for step := 0; step < hashMajorityRollbackMaxDepth; step++ {
		hdr = s.blockchain.Header()
		if hdr == nil || hdr.Number == 0 {
			break
		}
		match, majority, votes, ok = s.LocalHeadMatchesBootMajority()
		if !ok || match {
			break
		}
		forkHeight := hdr.Number
		target := forkHeight - 1
		if err := rb.RollbackToHeight(target); err != nil {
			s.logger.Warn("syncer: minority fork rollback failed",
				"reason", reason,
				"rollbackTo", target,
				"step", step,
				"error", err)
			break
		}
		rolled = true
		s.invalidateTrustedBootHeightCache()
		s.invalidateTrustedBootHashCache()
		s.logger.Warn("syncer: rolled back one block toward boot hash majority",
			"reason", reason,
			"forkHeight", forkHeight,
			"rollbackTo", target,
			"majorityHash", majority.String(),
			"majorityVotes", votes,
			"step", step+1)
	}
	if rolled {
		s.notifyNewStatusEvent()
	}
	return rolled
}

// PreProduceAllowLocalBuild 出块前：hash 分裂或本机 minority 时不应继续本地构建。
func (s *syncer) PreProduceAllowLocalBuild(sawPeerFork bool) bool {
	hdr := s.blockchain.Header()
	if hdr == nil {
		return true
	}
	local := hdr.Number
	hashReports := s.collectTrustedBootHashReports(local)
	disagree, agree := bootHashDisagreesWithLocal(hdr.Hash, hashReports)

	// 完整多数派（6:1 等）
	maj := computeBootHashMajority(local, hashReports)
	if maj.ok {
		if hdr.Hash != maj.majorityHash {
			s.logger.Warn("syncer: deny local block production — local head hash != boot majority",
				"localHash", hdr.Hash.String(),
				"majorityHash", maj.majorityHash.String(),
				"majorityVotes", maj.majorityVotes)
			s.maybeRollbackIfMinorityFork("pre_produce_minority")
			return false
		}
		if sawPeerFork {
			s.logger.Info("syncer: local head matches boot majority; peer fork view ignored for produce",
				"majorityHash", maj.majorityHash.String(),
				"majorityVotes", maj.majorityVotes)
		}
		return true
	}

	// 无完整 majority，但多台 boot 已报 hash 且与本地不一致 → 典型朱朱1（0xd49ae vs 6×0x83241a）
	if disagree >= 2 && disagree > agree {
		s.logger.Warn("syncer: deny local block production — boot hashes disagree with local head (plurality)",
			"localHash", hdr.Hash.String(),
			"bootAgreeLocal", agree,
			"bootDisagreeLocal", disagree)
		s.maybeRollbackIfMinorityFork("pre_produce_plurality")
		return false
	}
	need := hashQuorumRequiredVotes(disagree + agree)
	if disagree >= need {
		s.logger.Warn("syncer: deny local block production — boot quorum rejects local head hash",
			"localHash", hdr.Hash.String(),
			"bootDisagreeLocal", disagree,
			"requiredVotes", need)
		s.maybeRollbackIfMinorityFork("pre_produce_quorum_reject")
		return false
	}

	if disagree+agree == 0 {
		s.logger.Warn("syncer: boot hash quorum unavailable for produce gate; allowing local build (degraded)",
			"sawPeerFork", sawPeerFork,
			"localLatest", local)
	} else if sawPeerFork {
		s.logger.Info("syncer: boot hash split but local head not clearly minority; allowing produce",
			"localHash", hdr.Hash.String(),
			"bootAgreeLocal", agree,
			"bootDisagreeLocal", disagree)
	}
	return true
}
