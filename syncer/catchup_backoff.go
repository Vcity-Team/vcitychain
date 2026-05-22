package syncer

import "time"

const (
	// trustedAheadCatchUpRetryBackoff：quorum/RPC 已确认网络超前时，短退避重试（避免固定 3s 空等）。
	trustedAheadCatchUpRetryBackoff = 1 * time.Second
)

// trustedQuorumIndicatesNextBlock 是否可安全认为 canonical 链上存在高于 local 的块（须已有 quorum 或单 boot fallback）。
func (s *syncer) trustedQuorumIndicatesNextBlock(local uint64, meta trustedTipResult) bool {
	switch meta.Branch {
	case trustedTipBranchNoQuorum, trustedTipBranchNoHeight, trustedTipBranchNoConfig:
		return false
	}
	if meta.SingleBootFallback {
		return meta.Tip > local
	}
	if !meta.Quorum {
		return false
	}
	return meta.Tip > local || meta.MaxBootHeight > local
}

func countHeightsAtLeast(heights []uint64, min uint64) int {
	n := 0
	for _, h := range heights {
		if h >= min {
			n++
		}
	}
	return n
}

func (s *syncer) bootsRPCAtLeast(min uint64) int {
	s.trustedBootRPCHeightMu.RLock()
	defer s.trustedBootRPCHeightMu.RUnlock()
	if len(s.trustedBootnodeIDs) == 0 {
		return 0
	}
	n := 0
	for id := range s.trustedBootnodeIDs {
		h, ok := s.trustedBootRPCHeight[id]
		if ok && h >= min {
			n++
		}
	}
	return n
}

// catchUpRetryBackoff：网络已超前用短退避；真追平（RPC/quorum 未超前）仍用 3s。
func (s *syncer) catchUpRetryBackoff(local uint64, meta trustedTipResult) time.Duration {
	if s.trustedQuorumIndicatesNextBlock(local, meta) {
		return trustedAheadCatchUpRetryBackoff
	}
	return syncBestNotAheadWakeInterval
}
