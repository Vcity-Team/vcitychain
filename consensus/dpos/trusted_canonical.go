package dpos

import "time"

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

// mustWaitForTrustedCanonicalSync：创世 bootnode 共识链尖高于本地时，禁止出块直至同步追上。
func (r *dposRuntime) mustWaitForTrustedCanonicalSync(localTip uint64) bool {
	tip := r.trustedCanonicalTipFromSyncer()
	if tip == 0 || localTip >= tip {
		return false
	}
	r.logOnceWithInterval("behind_trusted_bootnode_canonical", 5*time.Second, "info",
		"⏸️ 本地链尖落后于创世 bootnode 共识链尖，暂不出块（先同步）",
		"localBlockNumber", localTip,
		"trustedCanonicalTip", tip,
		"lagBlocks", tip-localTip)
	return true
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
