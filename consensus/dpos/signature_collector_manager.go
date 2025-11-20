package dpos

import (
	"context"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// SignatureCollectorManager 管理签名收集器
type SignatureCollectorManager struct {
	collectors      map[types.Hash]*SignatureCollector
	mutex           sync.RWMutex
	logger          hclog.Logger
	goroutineManager *GoroutineManager
}

// NewSignatureCollectorManager 创建签名收集器管理器
func NewSignatureCollectorManager(logger hclog.Logger, goroutineManager *GoroutineManager) *SignatureCollectorManager {
	return &SignatureCollectorManager{
		collectors:        make(map[types.Hash]*SignatureCollector),
		logger:            logger,
		goroutineManager:  goroutineManager,
	}
}

// RegisterSignatureCollector 注册签名收集器
func (scm *SignatureCollectorManager) RegisterSignatureCollector(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration, requiredCount int) {
	scm.mutex.Lock()
	defer scm.mutex.Unlock()

	// 检查是否已存在相同checkpointHash的收集器
	if existingCollector, exists := scm.collectors[checkpointHash]; exists {
		existingCollector.Close()
	}

	// 创建新的签名收集器
	collector := NewSignatureCollector(checkpointHash, signatureCh, timeout, requiredCount, scm.goroutineManager)
	collector.logger = scm.logger.Named("signature-collector")

	scm.collectors[checkpointHash] = collector

	// 启动清理工作器
	if scm.goroutineManager != nil {
		scm.goroutineManager.StartGoroutine("collector-cleanup", func() {
			scm.startCollectorCleanupWorker(context.Background(), checkpointHash)
		})

		// 启动定期状态检查
		scm.goroutineManager.StartGoroutine("collector-status-check", func() {
			scm.monitorCollectorStatus(checkpointHash)
		})
	}
}

// GetSignatureCollector 获取签名收集器
func (scm *SignatureCollectorManager) GetSignatureCollector(checkpointHash types.Hash) *SignatureCollector {
	scm.mutex.RLock()
	defer scm.mutex.RUnlock()
	return scm.collectors[checkpointHash]
}

// UnregisterSignatureCollector 注销签名收集器
func (scm *SignatureCollectorManager) UnregisterSignatureCollector(checkpointHash types.Hash) {
	scm.mutex.Lock()
	defer scm.mutex.Unlock()
	delete(scm.collectors, checkpointHash)
}

// ForwardSignatureResponse 转发签名响应到相应的收集器
func (scm *SignatureCollectorManager) ForwardSignatureResponse(response *SignatureResponse) {
	// 使用读锁快速查找收集器
	scm.mutex.RLock()
	collector, exists := scm.collectors[response.CheckpointHash]
	scm.mutex.RUnlock()

	if !exists {
		return
	}

	// 使用AddSignature方法处理签名，它会自动更新内部状态
	if !collector.AddSignature(response) {
		scm.logger.Debug("签名响应处理失败",
			"validator", response.ValidatorAddr.String(),
			"checkpointHash", response.CheckpointHash.String())
	}
}

// cleanupExpiredCollectors 清理所有过期的签名收集器
func (scm *SignatureCollectorManager) cleanupExpiredCollectors() {
	scm.mutex.RLock()
	collectors := make(map[types.Hash]*SignatureCollector)
	for hash, collector := range scm.collectors {
		collectors[hash] = collector
	}
	scm.mutex.RUnlock()

	for hash, collector := range collectors {
		if collector.IsExpired() || collector.IsComplete() {
			scm.UnregisterSignatureCollector(hash)
			collector.Close()
		}
	}
}

// startCollectorCleanupWorker 启动收集器清理工作器
func (scm *SignatureCollectorManager) startCollectorCleanupWorker(ctx context.Context, checkpointHash types.Hash) {
	ticker := time.NewTicker(10 * time.Second) // 降低频率到10秒，减少goroutine压力
	defer ticker.Stop()

	// 添加超时控制，避免无限运行
	timeout := time.After(15 * time.Minute) // 缩短到15分钟，更快清理

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			scm.logger.Debug("收集器清理工作器超时，自动退出",
				"checkpointHash", checkpointHash.String())
			return
		case <-ticker.C:
			// 快速检查收集器是否存在
			scm.mutex.RLock()
			collector, exists := scm.collectors[checkpointHash]
			scm.mutex.RUnlock()

			if !exists {
				return
			}

			// 检查是否过期或完成（这些方法使用收集器内部的锁，不会与外部锁冲突）
			expired := collector.IsExpired()
			complete := collector.IsComplete()

			if expired || complete {
				// 清理签名收集器，静默处理
				scm.UnregisterSignatureCollector(checkpointHash)
				return
			}

			// 定期记录收集器状态（每30秒）
			if time.Now().Unix()%30 == 0 {
				scm.logger.Debug("签名收集器状态检查",
					"checkpointHash", checkpointHash.String(),
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount(),
					"isActive", collector.isActive)
			}
		}
	}
}

// monitorCollectorStatus 监控收集器状态
func (scm *SignatureCollectorManager) monitorCollectorStatus(checkpointHash types.Hash) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.After(15 * time.Minute)

	for {
		select {
		case <-timeout:
			return
		case <-ticker.C:
			scm.mutex.RLock()
			collector, exists := scm.collectors[checkpointHash]
			scm.mutex.RUnlock()

			if !exists {
				return
			}

			// 检查收集器状态
			if collector.IsExpired() || collector.IsComplete() {
				scm.UnregisterSignatureCollector(checkpointHash)
				return
			}
		}
	}
}

// Clear 清空所有收集器
func (scm *SignatureCollectorManager) Clear() {
	scm.mutex.Lock()
	defer scm.mutex.Unlock()

	for _, collector := range scm.collectors {
		collector.Close()
	}
	scm.collectors = make(map[types.Hash]*SignatureCollector)
}

