package dpos

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
)

// GoroutineManager 协程管理器
type GoroutineManager struct {
	logger hclog.Logger

	// 协程计数
	activeGoroutines int64
	maxGoroutines    int64

	// 重试协程池
	retryWorkerPool chan struct{}
	maxRetryWorkers int

	// 上下文管理
	ctx    context.Context
	cancel context.CancelFunc

	// 锁
	mutex sync.RWMutex

	// 统计信息
	stats struct {
		totalGoroutinesCreated int64
		totalGoroutinesExited  int64
		peakGoroutines         int64
	}

	// 日志间隔管理
	lastLogTime map[string]time.Time
	logMutex    sync.RWMutex
}

// NewGoroutineManager 创建协程管理器
func NewGoroutineManager(logger hclog.Logger, maxGoroutines int64, maxRetryWorkers int) *GoroutineManager {
	ctx, cancel := context.WithCancel(context.Background())

	gm := &GoroutineManager{
		logger:          logger.Named("goroutine-manager"),
		maxGoroutines:   maxGoroutines,
		maxRetryWorkers: maxRetryWorkers,
		ctx:             ctx,
		cancel:          cancel,
		retryWorkerPool: make(chan struct{}, maxRetryWorkers),
		lastLogTime:     make(map[string]time.Time),
	}

	// 启动监控协程
	go gm.monitorLoop()

	return gm
}

// StartGoroutine 启动受管理的协程
func (gm *GoroutineManager) StartGoroutine(name string, fn func()) bool {
	// 检查协程数量限制
	if atomic.LoadInt64(&gm.activeGoroutines) >= gm.maxGoroutines {
		gm.logger.Warn("协程数量已达上限，拒绝启动新协程",
			"name", name,
			"active", atomic.LoadInt64(&gm.activeGoroutines),
			"max", gm.maxGoroutines)
		return false
	}

	// 增加计数
	atomic.AddInt64(&gm.activeGoroutines, 1)
	atomic.AddInt64(&gm.stats.totalGoroutinesCreated, 1)

	// 启动协程
	go func() {
		defer func() {
			// 减少计数
			atomic.AddInt64(&gm.activeGoroutines, -1)
			atomic.AddInt64(&gm.stats.totalGoroutinesExited, 1)

			// 更新峰值
			current := atomic.LoadInt64(&gm.activeGoroutines)
			for {
				peak := atomic.LoadInt64(&gm.stats.peakGoroutines)
				if current <= peak {
					break
				}
				if atomic.CompareAndSwapInt64(&gm.stats.peakGoroutines, peak, current) {
					break
				}
			}
		}()

		// 执行函数
		fn()
	}()

	return true
}

// StartRetryGoroutine 启动重试协程（使用协程池）
func (gm *GoroutineManager) StartRetryGoroutine(name string, fn func()) bool {
	select {
	case gm.retryWorkerPool <- struct{}{}:
		// 获取到工作槽位
		return gm.StartGoroutine(name, func() {
			defer func() {
				<-gm.retryWorkerPool // 释放工作槽位
			}()
			fn()
		})
	default:
		// 工作池已满，拒绝启动
		gm.logger.Warn("重试协程池已满，拒绝启动新重试协程",
			"name", name,
			"active", atomic.LoadInt64(&gm.activeGoroutines))
		return false
	}
}

// StartSmartGoroutine 启动智能协程，自动检测和避免泄漏
func (gm *GoroutineManager) StartSmartGoroutine(name string, fn func()) bool {
	// 检查当前goroutine数量
	active := atomic.LoadInt64(&gm.activeGoroutines)
	if active >= gm.maxGoroutines {
		gm.logger.Error("无法启动新协程，已达到最大限制",
			"name", name,
			"active", active,
			"max", gm.maxGoroutines)
		return false
	}

	// 检查goroutine使用率
	utilization := float64(active) / float64(gm.maxGoroutines) * 100
	if utilization > 80 {
		gm.logger.Warn("协程使用率较高，谨慎启动新协程",
			"name", name,
			"utilization", utilization)
	}

	// 启动协程
	return gm.StartGoroutine(name, func() {
		// 添加超时保护
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn()
		}()

		// 设置合理的超时时间
		select {
		case <-done:
			// 正常完成
		case <-time.After(5 * time.Minute): // 5分钟超时
			gm.logger.Error("协程超时，可能存在泄漏",
				"name", name,
				"timeout", "5 minutes")
		}
	})
}

// StartTimedGoroutine 启动带超时的协程
func (gm *GoroutineManager) StartTimedGoroutine(name string, timeout time.Duration, fn func()) bool {
	return gm.StartGoroutine(name, func() {
		// 创建带超时的上下文
		ctx, cancel := context.WithTimeout(gm.ctx, timeout)
		defer cancel()

		// 使用通道来协调超时
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn()
		}()

		select {
		case <-done:
			// 正常完成
		case <-ctx.Done():
			gm.logger.Warn("协程超时", "name", name, "timeout", timeout)
		}
	})
}

// GetStats 获取统计信息
func (gm *GoroutineManager) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"activeGoroutines":       atomic.LoadInt64(&gm.activeGoroutines),
		"maxGoroutines":          gm.maxGoroutines,
		"totalGoroutinesCreated": atomic.LoadInt64(&gm.stats.totalGoroutinesCreated),
		"totalGoroutinesExited":  atomic.LoadInt64(&gm.stats.totalGoroutinesExited),
		"peakGoroutines":         atomic.LoadInt64(&gm.stats.peakGoroutines),
		"retryWorkerPoolUsage":   len(gm.retryWorkerPool),
		"maxRetryWorkers":        gm.maxRetryWorkers,
		"goroutineUtilization":   float64(atomic.LoadInt64(&gm.activeGoroutines)) / float64(gm.maxGoroutines) * 100,
	}
}

// monitorLoop 监控循环
func (gm *GoroutineManager) monitorLoop() {
	ticker := time.NewTicker(30 * time.Second) // 降低频率到30秒
	defer ticker.Stop()

	// 添加内存监控
	memoryTicker := time.NewTicker(120 * time.Second) // 降低频率到2分钟
	defer memoryTicker.Stop()

	// 添加goroutine泄漏检测
	leakTicker := time.NewTicker(300 * time.Second) // 降低频率到5分钟
	defer leakTicker.Stop()

	// 记录上次检查时的goroutine数量，用于检测泄漏
	var lastGoroutineCount int64
	var consecutiveHighCount int

	for {
		select {
		case <-gm.ctx.Done():
			return
		case <-ticker.C:
			stats := gm.GetStats()

			// 记录统计信息（已删除日志）

			// 检查协程数量是否过高
			activeCount := stats["activeGoroutines"].(int64)
			if activeCount > gm.maxGoroutines*80/100 {
				gm.logger.Warn("协程数量接近上限",
					"active", stats["activeGoroutines"],
					"max", gm.maxGoroutines,
					"utilization", stats["goroutineUtilization"])
			}

			// 检测goroutine泄漏
			if lastGoroutineCount > 0 && activeCount > lastGoroutineCount {
				consecutiveHighCount++
				if consecutiveHighCount >= 3 {
					gm.logger.Error("检测到goroutine泄漏风险",
						"currentCount", activeCount,
						"lastCount", lastGoroutineCount,
						"consecutiveIncreases", consecutiveHighCount)

					// 只记录警告，不触发清理
					consecutiveHighCount = 0
				}
			} else {
				consecutiveHighCount = 0
			}

			lastGoroutineCount = activeCount

		case <-memoryTicker.C:
			// 内存使用监控
			gm.monitorMemoryUsage()

		case <-leakTicker.C:
			// 定期goroutine泄漏检测
			gm.detectGoroutineLeaks()
		}
	}
}

// Close 关闭协程管理器
func (gm *GoroutineManager) Close() {
	gm.logger.Info("关闭协程管理器")
	gm.cancel()

	// 等待所有协程退出
	for i := 0; i < 50; i++ { // 最多等待5秒
		if atomic.LoadInt64(&gm.activeGoroutines) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if atomic.LoadInt64(&gm.activeGoroutines) > 0 {
		gm.logger.Warn("仍有协程未退出",
			"count", atomic.LoadInt64(&gm.activeGoroutines))
	}

	gm.logger.Info("协程管理器已关闭", "stats", gm.GetStats())
}

// IsHealthy 检查协程管理器是否健康
func (gm *GoroutineManager) IsHealthy() bool {
	active := atomic.LoadInt64(&gm.activeGoroutines)
	return active < gm.maxGoroutines*90/100 // 使用率低于90%认为健康
}

// emergencyCleanup 紧急清理机制
func (gm *GoroutineManager) emergencyCleanup() {
	gm.logger.Warn("启动紧急清理机制")

	// 强制关闭一些长时间运行的goroutine
	// 这里可以根据实际情况实现更复杂的清理逻辑

	// 记录当前状态
	stats := gm.GetStats()
	gm.logger.Info("紧急清理完成",
		"activeGoroutines", stats["activeGoroutines"],
		"peakGoroutines", stats["peakGoroutines"])
}

// monitorMemoryUsage 监控内存使用情况
func (gm *GoroutineManager) monitorMemoryUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 记录内存使用情况
	gm.logger.Debug("内存使用监控",
		"alloc", m.Alloc,
		"totalAlloc", m.TotalAlloc,
		"sys", m.Sys,
		"numGC", m.NumGC)

	// 如果内存使用过高，记录警告
	if m.Alloc > 100*1024*1024 { // 100MB
		gm.logger.Debug("内存使用较高",
			"allocMB", m.Alloc/1024/1024,
			"sysMB", m.Sys/1024/1024)
	}
}

// detectGoroutineLeaks 检测goroutine泄漏
func (gm *GoroutineManager) detectGoroutineLeaks() {
	active := atomic.LoadInt64(&gm.activeGoroutines)

	// 如果活跃goroutine数量持续增长，可能存在泄漏
	if active > gm.maxGoroutines*70/100 {
		gm.logger.Warn("检测到潜在的goroutine泄漏",
			"activeCount", active,
			"maxAllowed", gm.maxGoroutines,
			"utilization", float64(active)/float64(gm.maxGoroutines)*100)

		// 可以在这里添加更多的泄漏检测逻辑
		// 比如检查goroutine的运行时间、堆栈信息等
	}
}

// 防重复日志函数（自定义间隔）
func (gm *GoroutineManager) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	gm.logMutex.Lock()
	defer gm.logMutex.Unlock()

	now := time.Now()
	if lastTime, exists := gm.lastLogTime[key]; exists {
		// 如果指定间隔内已经记录过相同key的日志，则跳过
		if now.Sub(lastTime) < interval {
			return
		}
	}

	// 记录日志
	switch level {
	case "debug":
		gm.logger.Debug(message, args...)
	case "info":
		gm.logger.Info(message, args...)
	case "warn":
		gm.logger.Warn(message, args...)
	case "error":
		gm.logger.Error(message, args...)
	default:
		gm.logger.Info(message, args...)
	}

	// 更新最后记录时间
	gm.lastLogTime[key] = now
}
