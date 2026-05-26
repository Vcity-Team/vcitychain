package syncer

import "time"

const (
	// catchUpBurstSmallLagMax：落后块数在 (0, catchUpLagTierMedium) 时每轮 burst 上限（避免 lag=2～4 时一块一轮）。
	catchUpBurstSmallLagMax = 8
	// catchUpLagTierMedium：达到该 lag 后每轮至少可写 catchUpBurstBlocksMedium 块。
	catchUpLagTierMedium = 32
	// catchUpLagTierLarge：达到该 lag 后每轮可写 catchUpBurstBlocksLarge 块。
	catchUpLagTierLarge = 256
	// catchUpLagTierHuge：达到该 lag 后每轮可写 catchUpBurstBlocksHuge 块（硬顶）。
	catchUpLagTierHuge = 1024
	catchUpBurstBlocksMedium = 32
	catchUpBurstBlocksLarge  = 128
	catchUpBurstBlocksHuge   = 256
	// catchUpBurstTimeBudget：单轮 Sync catch-up 最长连续写入时间（与块数上限先到先停）。
	catchUpBurstTimeBudget = 45 * time.Second
	// catchUpBootParallel：单高度并行向 Top-N boot 拉块（单块 fallback / 探测）。
	catchUpBootParallel = 3
	// catchUpProbeTimeout：boot catch-up / 并行探测超时。
	catchUpProbeTimeout = 3 * time.Second
)

// catchUpBurstLimit 返回本轮 trusted-ahead burst 最多连续写入块数。
func catchUpBurstLimit(lag uint64) int {
	if lag == 0 {
		return 0
	}
	if lag <= catchUpBurstSmallLagMax {
		return int(lag)
	}
	if lag < catchUpLagTierMedium {
		return catchUpBurstSmallLagMax
	}
	if lag < catchUpLagTierLarge {
		return catchUpBurstBlocksMedium
	}
	if lag < catchUpLagTierHuge {
		return catchUpBurstBlocksLarge
	}
	return catchUpBurstBlocksHuge
}

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
