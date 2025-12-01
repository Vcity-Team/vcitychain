package dpos

import (
	"math/big"
	"runtime"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// cacheEntry 缓存条目，用于排序
type cacheEntry struct {
	addr      types.Address
	cacheTime time.Time
	cacheType string
}

// 初始化性能优化组件
func (d *DPoS) initPerformanceOptimizations() {
	// 初始化缓存
	d.cache = &DPoSCache{
		voterCache:           make(map[types.Address]*VoterInfo),
		delegateCache:        make(map[types.Address]*validator.ValidatorMetadata),
		rewardCache:          make(map[types.Address]*big.Int),
		voterCacheTime:       make(map[types.Address]time.Time),
		delegateCacheTime:    make(map[types.Address]time.Time),
		rewardCacheTime:      make(map[types.Address]time.Time),
		cacheTTL:             5 * time.Minute,
		maxCacheSize:         1000,                                  // 最大缓存1000个条目
		epochValidatorsCache: make(map[uint64]validator.AccountSet), // 🆕 初始化epoch验证者缓存
		epochCacheTime:       make(map[uint64]time.Time),            // 🆕 初始化epoch缓存时间戳
		epochCacheTTL:        5 * time.Minute,                       // 🆕 默认5分钟TTL
	}

	// 初始化批量处理器
	d.batchProcessor = &BatchProcessor{
		voteQueue:     make(chan *VoteMessage, 1000),
		delegateQueue: make(chan *DelegateMessage, 100),
		batchSize:     100,
		batchTimeout:  100 * time.Millisecond,
		workerCount:   4,
		stopCh:        make(chan struct{}),
	}

	// 初始化指标
	d.metrics = &DPoSMetrics{
		BlockRewards: big.NewInt(0),
	}

	// 启动批量处理工作协程
	d.startBatchWorkers()

	// 启动缓存清理协程
	go d.cacheCleanupWorker()

	// 🆕 启动签名去重清理协程
	if d.runtime != nil {
		d.runtime.startSignatureCleanup()
	}
}

// 更新缓存
func (d *DPoS) updateCache(voterUpdates map[types.Address]*VoterInfo) {
	d.cache.lock.Lock()
	defer d.cache.lock.Unlock()

	for addr, voter := range voterUpdates {
		d.cache.voterCache[addr] = voter
	}
	d.cache.lastUpdate = time.Now()
}

// 缓存清理工作协程
func (d *DPoS) cacheCleanupWorker() {
	// 改为1倍TTL，增加清理频率
	ticker := time.NewTicker(d.cache.cacheTTL) // 从 * 2 改为直接使用
	defer ticker.Stop()

	// 添加内存压力检测
	memoryTicker := time.NewTicker(30 * time.Second)
	defer memoryTicker.Stop()

	for {
		select {
		case <-ticker.C:
			d.cleanupExpiredCache()
		case <-memoryTicker.C:
			// 内存压力检测
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.Alloc > 50*1024*1024 { // 50MB阈值
				d.cleanupExpiredCache()
			}
		case <-d.closeCh:
			return
		}
	}
}

// cleanupExpiredCache 清理过期的缓存条目
func (d *DPoS) cleanupExpiredCache() {
	d.cache.lock.Lock()
	defer d.cache.lock.Unlock()

	now := time.Now()
	cleanedCount := 0

	// 清理过期的投票者缓存
	expiredVoters := make([]types.Address, 0)
	for addr, cacheTime := range d.cache.voterCacheTime {
		if now.Sub(cacheTime) > d.cache.cacheTTL {
			expiredVoters = append(expiredVoters, addr)
		}
	}
	for _, addr := range expiredVoters {
		delete(d.cache.voterCache, addr)
		delete(d.cache.voterCacheTime, addr)
		cleanedCount++
	}

	// 清理过期的委托者缓存
	expiredDelegates := make([]types.Address, 0)
	for addr, cacheTime := range d.cache.delegateCacheTime {
		if now.Sub(cacheTime) > d.cache.cacheTTL {
			expiredDelegates = append(expiredDelegates, addr)
		}
	}
	for _, addr := range expiredDelegates {
		delete(d.cache.delegateCache, addr)
		delete(d.cache.delegateCacheTime, addr)
		cleanedCount++
	}

	// 清理过期的奖励缓存
	expiredRewards := make([]types.Address, 0)
	for addr, cacheTime := range d.cache.rewardCacheTime {
		if now.Sub(cacheTime) > d.cache.cacheTTL {
			expiredRewards = append(expiredRewards, addr)
		}
	}
	for _, addr := range expiredRewards {
		delete(d.cache.rewardCache, addr)
		delete(d.cache.rewardCacheTime, addr)
		cleanedCount++
	}

	// 如果缓存过大，清理最旧的条目
	d.cleanupOversizedCache()

	if cleanedCount > 0 {
		d.logger.Debug("DPoS缓存清理完成",
			"清理数量", cleanedCount,
			"投票者缓存", len(d.cache.voterCache),
			"委托者缓存", len(d.cache.delegateCache),
			"奖励缓存", len(d.cache.rewardCache))
	}
}

// cleanupOversizedCache 清理过大的缓存
func (d *DPoS) cleanupOversizedCache() {
	totalCacheSize := len(d.cache.voterCache) + len(d.cache.delegateCache) + len(d.cache.rewardCache)

	if totalCacheSize <= d.cache.maxCacheSize {
		return
	}

	// 计算需要清理的数量（保留80%的缓存）
	targetSize := int(float64(d.cache.maxCacheSize) * 0.8)
	needToClean := totalCacheSize - targetSize

	// 按时间排序，清理最旧的条目
	allEntries := make([]cacheEntry, 0)

	// 收集投票者缓存条目
	for addr, cacheTime := range d.cache.voterCacheTime {
		allEntries = append(allEntries, cacheEntry{
			addr:      addr,
			cacheTime: cacheTime,
			cacheType: "voter",
		})
	}

	// 收集委托者缓存条目
	for addr, cacheTime := range d.cache.delegateCacheTime {
		allEntries = append(allEntries, cacheEntry{
			addr:      addr,
			cacheTime: cacheTime,
			cacheType: "delegate",
		})
	}

	// 收集奖励缓存条目
	for addr, cacheTime := range d.cache.rewardCacheTime {
		allEntries = append(allEntries, cacheEntry{
			addr:      addr,
			cacheTime: cacheTime,
			cacheType: "reward",
		})
	}

	// 按时间排序（最旧的在前）
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].cacheTime.Before(allEntries[j].cacheTime)
	})

	// 清理最旧的条目
	cleaned := 0
	for _, entry := range allEntries {
		if cleaned >= needToClean {
			break
		}

		switch entry.cacheType {
		case "voter":
			delete(d.cache.voterCache, entry.addr)
			delete(d.cache.voterCacheTime, entry.addr)
		case "delegate":
			delete(d.cache.delegateCache, entry.addr)
			delete(d.cache.delegateCacheTime, entry.addr)
		case "reward":
			delete(d.cache.rewardCache, entry.addr)
			delete(d.cache.rewardCacheTime, entry.addr)
		}
		cleaned++
	}

	if cleaned > 0 {
		d.logger.Debug("DPoS缓存大小清理完成",
			"清理数量", cleaned,
			"目标大小", targetSize,
			"当前大小", totalCacheSize-cleaned)
	}
}
