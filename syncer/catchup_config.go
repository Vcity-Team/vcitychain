package syncer

import "time"

const (
	// catchUpBurstMaxBlocks：trusted 大幅超前时每轮 Sync 连续 catch-up 的上限。
	catchUpBurstMaxBlocks = 32
	// catchUpBurstSmallLagMax：落后块数在 (0, largeLagBurst) 时每轮 burst 上限（避免 lag=2～4 时一块一轮）。
	catchUpBurstSmallLagMax = 8
	// catchUpLargeLagBurst：落后达到该块数时 burst 上限提升至 catchUpBurstMaxBlocks。
	catchUpLargeLagBurst = 32
	// catchUpBootParallel：单高度并行向 Top-N boot 拉块（套餐 E1）。
	catchUpBootParallel = 3
	// catchUpProbeTimeout：boot catch-up / 并行探测超时。
	catchUpProbeTimeout = 3 * time.Second
)

// catchUpBurstLimit 返回本轮 trusted-ahead burst 最多连续写入块数。
func catchUpBurstLimit(lag uint64) int {
	if lag == 0 {
		return 0
	}
	if lag >= catchUpLargeLagBurst {
		if lag > catchUpBurstMaxBlocks {
			return catchUpBurstMaxBlocks
		}
		return int(lag)
	}
	if lag > catchUpBurstSmallLagMax {
		return catchUpBurstSmallLagMax
	}
	return int(lag)
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
