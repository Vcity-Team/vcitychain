package dpos

import (
	"context"
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

	gm.logger.Debug("启动协程", "name", name, "active", atomic.LoadInt64(&gm.activeGoroutines))
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
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-gm.ctx.Done():
			return
		case <-ticker.C:
			stats := gm.GetStats()

			// 记录统计信息
			gm.logger.Debug("协程管理器统计",
				"active", stats["activeGoroutines"],
				"peak", stats["peakGoroutines"],
				"utilization", stats["goroutineUtilization"])

			// 检查协程数量是否过高
			if stats["activeGoroutines"].(int64) > gm.maxGoroutines*80/100 {
				gm.logger.Warn("协程数量接近上限",
					"active", stats["activeGoroutines"],
					"max", gm.maxGoroutines,
					"utilization", stats["goroutineUtilization"])
			}
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

