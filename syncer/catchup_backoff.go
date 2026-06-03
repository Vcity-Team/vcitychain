package syncer

import "time"

const (
	// trustedAheadCatchUpRetryBackoff：quorum/RPC 已确认网络超前时，短退避重试（避免固定 3s 空等）。
	trustedAheadCatchUpRetryBackoff = 500 * time.Millisecond
	// syncCaughtUpMinWakeInterval / syncCaughtUpMaxWakeInterval：P2P+boot 均已追平时的轮询下限/上限。
	// 原固定 3s 与 DPoS blockWindow≈3s 叠加，SR 节点易长期 lag=1。
	syncCaughtUpMinWakeInterval = 500 * time.Millisecond
	syncCaughtUpMaxWakeInterval = 1500 * time.Millisecond
)

// trustedAheadOfLocal boot RPC/quorum 已表明网络高于本地 → 可直接对 boot 拉 local+1。
func trustedAheadOfLocal(meta trustedTipResult, local uint64) bool {
	switch meta.Branch {
	case trustedTipBranchNoQuorum, trustedTipBranchNoHeight, trustedTipBranchNoConfig, trustedTipBranchHashSplit:
		return false
	}
	if meta.MaxBootHeight > local {
		return true
	}
	return meta.Tip > local
}

// caughtUpWakeBackoff P2P+boot 均已追平时的 self-wake 间隔（按 blockTimeout≈3×出块间隔估算）。
func (s *syncer) caughtUpWakeBackoff() time.Duration {
	if s == nil || s.blockTimeout <= 0 {
		return syncBestNotAheadWakeInterval
	}
	// blockTimeout 在 DPoS 侧约为 3×blockWindow（默认 9s @ 3s 块）。
	w := s.blockTimeout / 9
	if w < syncCaughtUpMinWakeInterval {
		w = syncCaughtUpMinWakeInterval
	}
	if w > syncCaughtUpMaxWakeInterval {
		w = syncCaughtUpMaxWakeInterval
	}
	return w
}

// catchUpRetryBackoff：网络已超前用短退避；真追平用 caughtUpWakeBackoff（不再固定 3s）。
func (s *syncer) catchUpRetryBackoff(local uint64, meta trustedTipResult) time.Duration {
	if trustedAheadOfLocal(meta, local) {
		return trustedAheadCatchUpRetryBackoff
	}
	return s.caughtUpWakeBackoff()
}
