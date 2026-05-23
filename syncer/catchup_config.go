package syncer

import "time"

const (
	// catchUpBurstMaxBlocks：trusted 超前时每轮 Sync 连续 catch-up 的上限（套餐 A）。
	catchUpBurstMaxBlocks = 32
	// catchUpBulkMinLag：落后至少该块数时优先 bulk，跳过单块 catch-up（套餐 B）。
	catchUpBulkMinLag = 2
	// catchUpLargeLagBurst：落后达到该块数时仍允许 boot burst（避免大落后只单块 + 反复 KickSync）。
	catchUpLargeLagBurst = 32
	// catchUpBootParallel：单高度并行向 Top-N boot 拉块（套餐 E1）。
	catchUpBootParallel = 3
	// catchUpProbeTimeout：boot catch-up / 并行探测超时。
	catchUpProbeTimeout = 3 * time.Second
)

// trustedCatchUpLag 返回 trusted 相对本地的落后块数（0 表示未超前）。
func trustedCatchUpLag(meta trustedTipResult, local uint64) uint64 {
	if !trustedAheadOfLocal(meta, local) {
		return 0
	}
	tip := meta.MaxBootHeight
	if meta.Tip > tip {
		tip = meta.Tip
	}
	if tip <= local {
		return 0
	}
	return tip - local
}
