package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/syncer"
)

// catchUpWaitForLag 按落后块数估算 sync burst 收尾等待上限（与 syncer catchUpBurstTimeBudget 对齐）。
func catchUpWaitForLag(lag uint64) time.Duration {
	const perBlock = 800 * time.Millisecond
	const maxWait = 45 * time.Second
	if lag == 0 {
		return 0
	}
	w := time.Duration(lag) * perBlock
	if w < perBlock {
		return perBlock
	}
	if w > maxWait {
		return maxWait
	}
	return w
}

func (r *dposRuntime) syncerFromBackend() syncer.Syncer {
	if r.config == nil || r.config.dposBackend == nil {
		return nil
	}
	dpos, ok := r.config.dposBackend.(*DPoS)
	if !ok || dpos == nil {
		return nil
	}
	return dpos.syncer
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

// canonicalGateTip 出块门禁高度：优先 boot quorum tip；boot 不可用时回退 gossip 合成水位（非追平锁，仅即时比较）。
func (r *dposRuntime) canonicalGateTip() uint64 {
	if tip := r.trustedCanonicalTipFromSyncer(); tip > 0 {
		return tip
	}
	return r.getNetworkLatestBlockNumber()
}

// behindCanonicalGate 本地链尖是否落后于门禁高度。
func (r *dposRuntime) behindCanonicalGate(localTip uint64) bool {
	tip := r.canonicalGateTip()
	return tip > 0 && localTip < tip
}

func (r *dposRuntime) lagBehindCanonicalGate(localTip uint64) uint64 {
	tip := r.canonicalGateTip()
	if tip <= localTip {
		return 0
	}
	return tip - localTip
}

// behindTrustedCanonicalSync 保留命名；语义同 behindCanonicalGate。
func (r *dposRuntime) behindTrustedCanonicalSync(localTip uint64) bool {
	return r.behindCanonicalGate(localTip)
}

// nudgeSyncIfBehind 落后时轻量唤醒 sync（不阻塞等待）；供 shouldProduce 热路径在 leader 判定前提前追块。
func (r *dposRuntime) nudgeSyncIfBehind(local uint64) {
	if !r.behindCanonicalGate(local) {
		return
	}
	syncSvc := r.syncerFromBackend()
	if syncSvc == nil || syncSvc.SyncCatchUpActive() {
		return
	}
	lag := r.lagBehindCanonicalGate(local)
	if lag == 0 {
		return
	}
	syncSvc.KickSync(fmt.Sprintf("lag=%d: nudge sync while behind", lag))
}

// waitForCanonicalCatchUp 轮询直到追平门禁高度或超时（覆盖 KickSync 后 sync 尚未 beginCatchUp 的窗口）。
func (r *dposRuntime) waitForCanonicalCatchUp(local uint64, maxWait time.Duration) uint64 {
	if maxWait <= 0 {
		return local
	}
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if r.config != nil && r.config.blockchain != nil {
			if hdr := r.config.blockchain.CurrentHeader(); hdr != nil {
				local = hdr.Number
			}
		}
		if !r.behindCanonicalGate(local) {
			return local
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r.config != nil && r.config.blockchain != nil {
		if hdr := r.config.blockchain.CurrentHeader(); hdr != nil {
			local = hdr.Number
		}
	}
	return local
}

// blockProductionIfBehindTrustedCanonical 落后时 KickSync 并等待；仍落后则返回 true（应跳过出块）。
func (r *dposRuntime) blockProductionIfBehindTrustedCanonical(local uint64) bool {
	if !r.behindCanonicalGate(local) {
		return false
	}
	lag := r.lagBehindCanonicalGate(local)
	if syncSvc := r.syncerFromBackend(); syncSvc != nil && lag > 0 && !syncSvc.SyncCatchUpActive() {
		syncSvc.KickSync(fmt.Sprintf("lag=%d: sync before produce", lag))
	}
	local = r.waitForCanonicalCatchUp(local, catchUpWaitForLag(lag))
	if !r.behindCanonicalGate(local) {
		return false
	}
	r.logBehindTrustedCanonicalSync(local)
	return true
}

// logBehindTrustedCanonicalSync 仅在本轮应当出块但因未追上 bootnode 链尖而跳过时记录。
func (r *dposRuntime) logBehindTrustedCanonicalSync(localTip uint64) {
	tip := r.canonicalGateTip()
	if tip == 0 || localTip >= tip {
		return
	}
	r.logOnceWithInterval("behind_trusted_bootnode_canonical", 5*time.Second, "info",
		"⏸️ 本地链尖落后于创世 bootnode 共识链尖，暂不出块（先同步）",
		"localBlockNumber", localTip,
		"trustedCanonicalTip", tip,
		"lagBlocks", tip-localTip)
}

// preProduceTrustedCanonicalCheck 出块前：若门禁高度已不低于拟出块高度，应中止本地构建、依赖同步。
func (r *dposRuntime) preProduceTrustedCanonicalCheck(localTip uint64) bool {
	tip := r.canonicalGateTip()
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
