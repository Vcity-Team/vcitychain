package syncer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
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
		if s.isBootHashOutlier(id) {
			continue
		}
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

// rotatePeerIDs 将 ids 左旋 offset 位（offset mod len），用于 bulk 失败后轮换 boot 尝试顺序。
func rotatePeerIDs(ids []peer.ID, offset int) []peer.ID {
	n := len(ids)
	if n == 0 {
		return nil
	}
	off := offset % n
	if off == 0 {
		out := make([]peer.ID, n)
		copy(out, ids)
		return out
	}
	out := make([]peer.ID, n)
	copy(out, ids[off:])
	copy(out[n-off:], ids[:off])
	return out
}

func (s *syncer) orderedBootCatchUpCandidatesRotated() []peer.ID {
	return rotatePeerIDs(s.orderedBootCatchUpCandidates(), int(s.bulkBootRotateIdx.Load()))
}

// logCatchUpBurstBreak 记录 burst 提前结束原因（限频 INFO，便于与「误判追平」区分）。
func (s *syncer) logCatchUpBurstBreak(reason string, local uint64, meta trustedTipResult, extra ...interface{}) {
	key := fmt.Sprintf("burst-break:%s:%d", reason, local)
	s.logTrustedTipThrottled(key, func() {
		args := append([]interface{}{
			"reason", reason,
			"localLatest", local,
			"trustedTip", meta.Tip,
			"maxBootHeight", meta.MaxBootHeight,
			"trustedBranch", meta.Branch,
		}, extra...)
		s.logger.Info("syncer: catch-up burst stopped", args...)
	})
}

// tryCatchUpBurstFromBoot：trusted 超前时按 lag 对 boot 开长流连续 catch-up。
// 入口强制刷新 boot 高度；若 local 已达旧 tip 但网络仍超前，再条件刷新一次 meta，避免「trustedTip==local」误停。
func (s *syncer) tryCatchUpBurstFromBoot(local uint64, _ trustedTipResult, callback func(*types.FullBlock) bool) bool {
	meta := s.refreshTrustedMetaForCatchUp(local)
	if !trustedAheadOfLocal(meta, local) {
		return false
	}
	startTip := meta.Tip
	lag := trustedCatchUpLag(meta, local)
	sessionMax := catchUpBurstLimit(lag)
	if sessionMax <= 0 {
		return false
	}

	start := time.Now()
	deadline := start.Add(catchUpBurstTimeBudget)
	s.beginSyncCatchUp()
	defer s.endSyncCatchUp()

	wrote := false
	blocksWritten := 0
	for blocksWritten < sessionMax && time.Now().Before(deadline) {
		if !trustedAheadOfLocal(meta, local) {
			if local >= startTip && local >= meta.Tip {
				fresh := s.refreshTrustedMetaForCatchUp(local)
				if trustedAheadOfLocal(fresh, local) {
					meta = fresh
					lag = trustedCatchUpLag(meta, local)
					if extra := catchUpBurstLimit(lag); extra > 0 {
						need := blocksWritten + extra
						if need > sessionMax {
							sessionMax = need
							if sessionMax > catchUpBurstBlocksHuge {
								sessionMax = catchUpBurstBlocksHuge
							}
						}
					}
					continue
				}
				s.logCatchUpBurstBreak("not_ahead_after_refresh", local, meta)
			} else {
				s.logCatchUpBurstBreak("not_ahead", local, meta)
			}
			break
		}
		hdr := s.blockchain.Header()
		if hdr == nil {
			break
		}
		local = hdr.Number
		remaining := sessionMax - blocksWritten
		lag = trustedCatchUpLag(meta, local)
		s.logger.Debug("syncer: catch-up boot stream started",
			"localLatest", local,
			"sessionMax", sessionMax,
			"remaining", remaining,
			"lag", lag,
			"timeBudget", catchUpBurstTimeBudget.String(),
			"trustedTip", meta.Tip)
		n, sawFork := s.runBootCatchUpStream(local, hdr.Hash, remaining, deadline, callback)
		if sawFork {
			s.maybeRollbackIfMinorityFork("catch_up_boot_stream_fork")
			if n > 0 {
				wrote = true
				blocksWritten += n
			}
			break
		}
		if n == 0 {
			s.logCatchUpBurstBreak("fetch_fail", local, meta, "nextHeight", local+1)
			s.maybeRollbackIfMinorityFork("catch_up_burst")
			break
		}
		wrote = true
		blocksWritten += n
		if hdr := s.blockchain.Header(); hdr != nil {
			local = hdr.Number
		}
	}
	if wrote {
		s.notifyNewStatusEvent()
		meta = s.computeTrustedBootnodeTip(local)
		elapsed := time.Since(start)
		avgMs := int64(0)
		if blocksWritten > 0 {
			avgMs = elapsed.Milliseconds() / int64(blocksWritten)
		}
		s.logger.Info("syncer: catch-up burst completed from boot",
			"localLatest", local,
			"trustedTip", meta.Tip,
			"maxBootHeight", meta.MaxBootHeight,
			"blocksWritten", blocksWritten,
			"duration", elapsed.String(),
			"avgBlockMs", avgMs)
	}
	return wrote
}

// tryCatchUpNextBlockFromBoot：trusted 已超前 → 对 boot 拉 local+1 并验块写入（不等待 P2P 宣称 > local）。
func (s *syncer) tryCatchUpNextBlockFromBoot(local uint64, meta trustedTipResult, callback func(*types.FullBlock) bool, notify bool) bool {
	if !trustedAheadOfLocal(meta, local) {
		return false
	}
	hdr := s.blockchain.Header()
	if hdr == nil {
		return false
	}
	nextNum := local + 1
	localHash := hdr.Hash

	candidates := s.orderedBootCatchUpCandidatesRotated()
	blk, pid, class := s.fetchCanonicalNextFromBoots(candidates, nextNum, localHash, catchUpProbeTimeout)
	switch class {
	case catchUpProbeOK:
		if blk == nil {
			return false
		}
		if s.ingestCatchUpBlock(pid, blk, callback) {
			s.logger.Debug("syncer: catch-up wrote next block from boot (trusted height ahead)",
				"peer", pid.String(),
				"blockNumber", nextNum,
				"trustedTip", meta.Tip,
				"maxBootHeight", meta.MaxBootHeight)
			if notify {
				s.notifyNewStatusEvent()
			}
			return true
		}
	case catchUpProbeFork:
		s.logger.Warn("syncer: catch-up boot parent mismatch (fork)",
			"peer", pid.String(),
			"nextHeight", nextNum,
			"localHeadHash", localHash.String())
		s.maybeRollbackIfMinorityFork("catch_up_boot_fork")
	default:
		s.logger.Debug("syncer: catch-up boot did not deliver block",
			"nextHeight", nextNum,
			"bootCandidates", len(candidates))
	}
	return false
}

// fetchCanonicalNextFromBoots 并行探测 Top-N boot，取首个成功交付的 canonical next。
func (s *syncer) fetchCanonicalNextFromBoots(candidates []peer.ID, nextNum uint64, localHash types.Hash, probeTimeout time.Duration) (*types.Block, peer.ID, catchUpProbeClass) {
	if len(candidates) == 0 {
		return nil, "", catchUpProbeFail
	}
	parallel := catchUpBootParallel
	if parallel < 1 {
		parallel = 1
	}
	if parallel >= len(candidates) {
		for _, pid := range candidates {
			blk, class := s.fetchCanonicalNextForCatchUp(pid, nextNum, localHash, probeTimeout)
			if class == catchUpProbeOK && blk != nil {
				return blk, pid, class
			}
			if class == catchUpProbeFork {
				return nil, pid, class
			}
		}
		return nil, "", catchUpProbeFail
	}

	type probeResult struct {
		blk   *types.Block
		pid   peer.ID
		class catchUpProbeClass
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	results := make(chan probeResult, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel && i < len(candidates); i++ {
		pid := candidates[i]
		wg.Add(1)
		go func(id peer.ID) {
			defer wg.Done()
			blk, class := s.fetchCanonicalNextForCatchUp(id, nextNum, localHash, probeTimeout)
			select {
			case results <- probeResult{blk: blk, pid: id, class: class}:
			case <-ctx.Done():
			}
		}(pid)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var sawFork bool
	var forkPID peer.ID
	for res := range results {
		switch res.class {
		case catchUpProbeOK:
			if res.blk != nil {
				cancel()
				return res.blk, res.pid, catchUpProbeOK
			}
		case catchUpProbeFork:
			sawFork = true
			forkPID = res.pid
		}
	}
	if sawFork {
		return nil, forkPID, catchUpProbeFork
	}
	for _, pid := range candidates[parallel:] {
		blk, class := s.fetchCanonicalNextForCatchUp(pid, nextNum, localHash, probeTimeout)
		if class == catchUpProbeOK && blk != nil {
			return blk, pid, class
		}
		if class == catchUpProbeFork {
			return nil, pid, class
		}
	}
	return nil, "", catchUpProbeFail
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
	s.beginSyncCatchUp()
	defer s.endSyncCatchUp()
	ok, _ := s.syncIngestBlock(peerID, block, callback)
	return ok
}
