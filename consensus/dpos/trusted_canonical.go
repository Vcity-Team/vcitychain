package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/syncer"
)

// waitForSyncCatchUpDrain 等待进行中的 boot catch-up 收尾（lag=1 时 burst 通常 <1s 完成）。
func (r *dposRuntime) waitForSyncCatchUpDrain(syncSvc syncer.Syncer, maxWait time.Duration) {
	if syncSvc == nil || !syncSvc.SyncCatchUpActive() {
		return
	}
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if !syncSvc.SyncCatchUpActive() {
			return
		}
	}
}

// trustedCanonicalTipFromSyncer returns syncer bootnode quorum tip for gate/sync (no dpos_bootstrap_rpc height).
func (r *dposRuntime) trustedCanonicalTipFromSyncer() uint64 {
	if r.config == nil || r.config.dposBackend == nil {
		return 0
	}
	dpos, ok := r.config.dposBackend.(*DPoS)
	if !ok || dpos == nil || dpos.syncer == nil {
		return 0
	}
	return dpos.syncer.GetTrustedCanonicalTip()
}

// behindTrustedCanonicalSync 本地链尖是否落后于创世 bootnode 共识链尖（无日志）。
func (r *dposRuntime) behindTrustedCanonicalSync(localTip uint64) bool {
	tip := r.trustedCanonicalTipFromSyncer()
	return tip > 0 && localTip < tip
}

// logBehindTrustedCanonicalSync 仅在本轮应当出块但因未追上 bootnode 链尖而跳过时记录。
func (r *dposRuntime) logBehindTrustedCanonicalSync(localTip uint64) {
	tip := r.trustedCanonicalTipFromSyncer()
	if tip == 0 || localTip >= tip {
		return
	}
	r.logOnceWithInterval("behind_trusted_bootnode_canonical", 5*time.Second, "info",
		"⏸️ 本地链尖落后于创世 bootnode 共识链尖，暂不出块（先同步）",
		"localBlockNumber", localTip,
		"trustedCanonicalTip", tip,
		"lagBlocks", tip-localTip)
}

// preProduceTrustedCanonicalCheck 出块前：若 bootnode 共识链尖已不低于拟出块高度，应中止本地构建、依赖同步。
func (r *dposRuntime) preProduceTrustedCanonicalCheck(localTip uint64) bool {
	tip := r.trustedCanonicalTipFromSyncer()
	if tip == 0 {
		return false
	}
	nextHeight := localTip + 1
	r.logger.Info("🔭 【出块前 bootnode 共识链尖】",
		"localTip", localTip,
		"plannedNextBlockNumber", nextHeight,
		"trustedCanonicalTip", tip)
	if tip >= nextHeight {
		r.logger.Info("⏸️ 【出块前跳过出块】bootnode 共识高度已不低于拟出块高度，请依赖同步拉取",
			"localTip", localTip,
			"plannedNextBlockNumber", nextHeight,
			"trustedCanonicalTip", tip)
		return true
	}
	return false
}
