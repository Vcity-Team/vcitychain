package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
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

func (r *dposRuntime) localHasCanonicalBlock(height uint64) bool {
	if r.config == nil || r.config.blockchain == nil {
		return false
	}
	_, ok := r.config.blockchain.GetHeaderByNumber(height)
	return ok
}

// designatedProduceSyncExempt 轮值 proposer 且本地尚无 next：可绕过 lag=1 的 sync 门禁（blockProductionIfBehind/nudgeSync）。
// trustedTip>=next 时仍须先 P2P 探测；仅探测不到 valid next 时才允许本地 build（保守，防 reorg）。
func (r *dposRuntime) designatedProduceSyncExempt(localTip uint64) bool {
	if r.config == nil || r.config.Key == nil || r.config.blockScheduler == nil || r.config.dposBackend == nil {
		return false
	}
	dpos, ok := r.config.dposBackend.(*DPoS)
	if !ok || dpos == nil {
		return false
	}
	validators, err := dpos.GetSortedValidatorsWithLimitFilterFaulty()
	if err != nil || len(validators) == 0 {
		return false
	}
	addrs := make([]types.Address, len(validators))
	for i, v := range validators {
		addrs[i] = v.Address
	}
	myAddress := types.Address(r.config.Key.Address())
	eligible := dpos.productionEligibilityCheckerBase(validators)
	if !r.config.blockScheduler.DesignatedProposerForNext(localTip, myAddress, addrs, eligible) {
		return false
	}
	if r.localHasCanonicalBlock(localTip + 1) {
		return false
	}
	return true
}

// produceDecisionObsoletedByChainAdvance sync 已写入拟出块高度（或已超过）时放弃本轮 produce。
func (r *dposRuntime) produceDecisionObsoletedByChainAdvance(decisionLocalTip uint64) bool {
	if r.config == nil || r.config.blockchain == nil || decisionLocalTip == 0 {
		return false
	}
	hdr := r.config.blockchain.CurrentHeader()
	if hdr == nil {
		return false
	}
	plannedNext := decisionLocalTip + 1
	if hdr.Number >= plannedNext || r.localHasCanonicalBlock(plannedNext) {
		r.logger.Info("⏭️ 【出块跳过】链尖已推进，拟出块高度已存在或已被同步掠过",
			"decisionLocalTip", decisionLocalTip,
			"plannedNextBlockNumber", plannedNext,
			"currentLocalTip", hdr.Number,
			"nextHeightInDB", r.localHasCanonicalBlock(plannedNext))
		return true
	}
	return false
}

// blockProductionIfBehindTrustedCanonical 落后时 KickSync 并等待；仍落后则返回 true（应跳过出块）。
func (r *dposRuntime) blockProductionIfBehindTrustedCanonical(local uint64) bool {
	if r.designatedProduceSyncExempt(local) {
		return false
	}
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
// 调用方须先完成出块前 P2P 探测且未拉到可衔接 next；designated 在该前提下才可继续（保守路径）。
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
	if tip < nextHeight {
		return false
	}
	if r.designatedProduceSyncExempt(localTip) {
		r.logger.Info("🔭 【出块前 bootnode 共识链尖】designated 保守路径：trusted 已超前但 P2P 未拉到可衔接 next，继续本地出块",
			"localTip", localTip,
			"plannedNextBlockNumber", nextHeight,
			"trustedCanonicalTip", tip)
		return false
	}
	r.logger.Info("⏸️ 【出块前跳过出块】bootnode 共识高度已不低于拟出块高度，请依赖同步拉取",
		"localTip", localTip,
		"plannedNextBlockNumber", nextHeight,
		"trustedCanonicalTip", tip)
	return true
}
