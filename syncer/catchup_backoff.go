package syncer

import "time"

const (
	// trustedAheadCatchUpRetryBackoff：quorum/RPC 已确认网络超前时，短退避重试（避免固定 3s 空等）。
	trustedAheadCatchUpRetryBackoff = 1 * time.Second
)

// trustedAheadOfLocal boot RPC/quorum 已表明网络高于本地 → 可直接对 boot 拉 local+1。
func trustedAheadOfLocal(meta trustedTipResult, local uint64) bool {
	switch meta.Branch {
	case trustedTipBranchNoQuorum, trustedTipBranchNoHeight, trustedTipBranchNoConfig:
		return false
	}
	if meta.MaxBootHeight > local {
		return true
	}
	return meta.Tip > local
}

// catchUpRetryBackoff：网络已超前用短退避；真追平仍用 3s。
func (s *syncer) catchUpRetryBackoff(local uint64, meta trustedTipResult) time.Duration {
	if trustedAheadOfLocal(meta, local) {
		return trustedAheadCatchUpRetryBackoff
	}
	return syncBestNotAheadWakeInterval
}
