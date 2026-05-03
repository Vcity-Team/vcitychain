package dpos

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
)

// ResourceMonitor 资源监控器
type ResourceMonitor struct {
	logger hclog.Logger

	// 协程管理器
	goroutineManager *GoroutineManager

	// 协程数量监控
	goroutineCount int64
	goroutineMutex sync.RWMutex

	// 网络主题监控
	topicCount int64
	topicMutex sync.RWMutex

	// 网络服务状态监控
	networkAvailable bool
	networkMutex     sync.RWMutex

	// 清理间隔
	cleanupInterval time.Duration

	// DPoS运行时引用，用于缓存清理
	dposRuntime *dposRuntime

	// 日志间隔管理
	lastLogTime map[string]time.Time
	logMutex    sync.RWMutex

	syncLagKickMu        sync.Mutex
	syncLagKickInit      bool
	syncLagKickLastLocal uint64
	syncLagKickLastProbe time.Time
}

// NewResourceMonitor 创建资源监控器
func NewResourceMonitor(logger hclog.Logger, dposRuntime *dposRuntime) *ResourceMonitor {
	return &ResourceMonitor{
		logger:           logger.Named("resource-monitor"),
		goroutineManager: NewGoroutineManager(logger, 2000, 200), // 最大2000个协程，200个重试工作器
		lastLogTime:      make(map[string]time.Time),
		cleanupInterval:  30 * time.Second,
		dposRuntime:      dposRuntime,
	}
}

// Start 启动资源监控
func (rm *ResourceMonitor) Start(ctx context.Context) {
	rm.goroutineManager.StartGoroutine("resource-monitor", func() {
		rm.monitorLoop(ctx)
	})
}

// monitorLoop 监控循环
func (rm *ResourceMonitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(rm.cleanupInterval)
	defer ticker.Stop()

	planBTicker := time.NewTicker(planBStallCheckInterval)
	defer planBTicker.Stop()

	// 添加内存监控ticker
	memoryTicker := time.NewTicker(60 * time.Second)
	defer memoryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rm.cleanupResources()
		case <-planBTicker.C:
			if rm.dposRuntime != nil {
				rm.maybeKickSyncIfStalledBehind()
			}
		case <-memoryTicker.C:
			// 定期内存监控
			rm.monitorMemoryUsage()
		}
	}
}

// cleanupResources 清理资源
func (rm *ResourceMonitor) cleanupResources() {
	// 获取当前协程数量
	currentGoroutines := runtime.NumGoroutine()

	rm.goroutineMutex.Lock()
	rm.goroutineCount = int64(currentGoroutines)
	rm.goroutineMutex.Unlock()

	// 如果协程数量过多，记录警告
	if currentGoroutines > 1000 {
		rm.logger.Warn("协程数量过多，可能存在泄漏", "count", currentGoroutines)

		// 如果协程管理器可用，获取其统计信息（已删除日志）
		if rm.goroutineManager != nil {
			_ = rm.goroutineManager.GetStats()
		}
	}

	// 清理DPoS运行时缓存（方案 B 由 planBTicker 单独检查）
	if rm.dposRuntime != nil {
		rm.dposRuntime.cleanupExpiredCaches()
	}
}

// maybeKickSyncIfStalledBehind 当 lag>=配置块差且本机 tip 连续超过停滞时长不涨时触发方案 B（DPoS.restartSyncerForRecovery）。
func (rm *ResourceMonitor) maybeKickSyncIfStalledBehind() {
	if rm.dposRuntime == nil || rm.dposRuntime.config == nil {
		return
	}
	cfg := rm.dposRuntime.config
	if cfg.SyncLagRestartBlocks == 0 || cfg.SyncLagRestartStagnant <= 0 {
		return
	}
	hdr := cfg.blockchain.CurrentHeader()
	if hdr == nil {
		return
	}
	local := hdr.Number
	network := rm.dposRuntime.getNetworkLatestBlockNumber()

	rm.syncLagKickMu.Lock()
	defer rm.syncLagKickMu.Unlock()

	if !rm.syncLagKickInit {
		rm.syncLagKickInit = true
		rm.syncLagKickLastLocal = local
		rm.syncLagKickLastProbe = time.Now()
		return
	}

	if local != rm.syncLagKickLastLocal {
		rm.syncLagKickLastLocal = local
		rm.syncLagKickLastProbe = time.Now()
		return
	}

	if network <= local {
		return
	}
	lag := network - local
	if lag < cfg.SyncLagRestartBlocks {
		return
	}
	if time.Since(rm.syncLagKickLastProbe) < cfg.SyncLagRestartStagnant {
		return
	}

	dp, ok := cfg.dposBackend.(*DPoS)
	if !ok || dp == nil {
		rm.logger.Info("sync lag recovery: hard restart skipped (DPoS unavailable)",
			"localBlockNumber", local,
			"networkLatestBlockNumber", network,
			"lagBlocks", lag)
		rm.syncLagKickLastProbe = time.Now()
		return
	}

	rm.logger.Info("sync lag recovery: invoking DPoS restartSyncerForRecovery (plan B)",
		"localBlockNumber", local,
		"networkLatestBlockNumber", network,
		"lagBlocks", lag,
		"minLagBlocks", cfg.SyncLagRestartBlocks,
		"stagnantDuration", cfg.SyncLagRestartStagnant.String())

	if err := dp.restartSyncerForRecovery("resource-monitor: local tip stalled behind network gateway height"); err != nil {
		rm.logger.Info("sync lag recovery: restartSyncerForRecovery failed", "error", err)
	}
	rm.syncLagKickLastProbe = time.Now()
}

// monitorMemoryUsage 监控内存使用情况
func (rm *ResourceMonitor) monitorMemoryUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := m.Alloc / 1024 / 1024
	sysMB := m.Sys / 1024 / 1024

	// 如果内存使用超过100MB，记录警告
	if allocMB > 100 {
		rm.logger.Debug("内存使用较高",
			"allocMB", allocMB,
			"sysMB", sysMB,
			"goroutines", runtime.NumGoroutine())
	} else {
		rm.logger.Debug("内存使用正常",
			"allocMB", allocMB,
			"sysMB", sysMB,
			"goroutines", runtime.NumGoroutine())
	}
}

// logOnceWithInterval 防重复日志函数（自定义间隔）
func (rm *ResourceMonitor) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	rm.logMutex.Lock()
	defer rm.logMutex.Unlock()

	now := time.Now()
	if lastTime, exists := rm.lastLogTime[key]; exists {
		// 如果指定间隔内已经记录过相同key的日志，则跳过
		if now.Sub(lastTime) < interval {
			return
		}
	}

	// 更新最后记录时间
	rm.lastLogTime[key] = now

	// 根据级别记录日志
	switch level {
	case "debug":
		rm.logger.Debug(message, args...)
	case "info":
		rm.logger.Info(message, args...)
	case "warn":
		rm.logger.Warn(message, args...)
	case "error":
		rm.logger.Error(message, args...)
	default:
		rm.logger.Info(message, args...)
	}
}
