package dpos

import (
	"context"
	"fmt"
	"time"
)

// start 启动DPoS runtime
func (r *dposRuntime) start() error {
	r.logger.Info("🚀 开始启动DPoS runtime")

	// 初始化运行时状态
	r.logger.Debug("🔧 开始初始化DPoS runtime状态...")
	if err := r.initializeRuntime(); err != nil {
		r.logger.Error("❌ 初始化DPoS runtime失败", "error", err)
		return fmt.Errorf("failed to initialize runtime: %w", err)
	}
	r.logger.Debug("✅ DPoS runtime状态初始化成功")

	// 启动区块生产定时器
	if err := r.startBlockProduction(); err != nil {
		r.logger.Error("❌ 启动区块生产定时器失败", "error", err)
		return fmt.Errorf("failed to start block production: %w", err)
	}

	// 启动投票统计定时器
	if err := r.startVoteCollection(); err != nil {
		r.logger.Error("❌ 启动投票统计定时器失败", "error", err)
		return fmt.Errorf("failed to start vote collection: %w", err)
	}

	// 启动持久的签名请求监听器 - 确保所有节点都能接收到广播的签名请求
	go r.listenForSignatureRequests(context.Background())

	// 🆕 启动网络健康监控
	r.startNetworkHealthMonitoring()

	r.logger.Debug("🎉 DPoS runtime启动成功")
	return nil
}

// close 关闭DPoS runtime
func (r *dposRuntime) close() {
	r.logger.Debug("closing DPoS runtime")

	// 停止所有定时器
	if r.voteTimer != nil {
		r.voteTimer.Stop()
	}
	// 🆕 停止网络健康监控定时器
	if r.networkHealthTimer != nil {
		r.networkHealthTimer.Stop()
	}

	// 停止网络集成管理器
	if r.networkIntegration != nil {
		if err := r.networkIntegration.Stop(); err != nil {
			r.logger.Warn("failed to stop network integration", "error", err)
		} else {
			r.logger.Debug("网络集成管理器已停止")
		}
	}

	// 停止资源监控器
	if r.resourceMonitor != nil {
		// 这里可以添加停止资源监控器的逻辑
		r.logger.Debug("资源监控器已停止")
	}

	// 清理运行时状态
	r.cleanupRuntime()

	// 清理网络主题引用
	r.topicMutex.Lock()
	r.signatureRequestTopic = nil
	r.signatureResponseTopic = nil
	r.signatureQueryTopic = nil
	r.topicMutex.Unlock()

	r.logger.Debug("DPoS runtime closed")
}

// cleanupRuntime 清理运行时状态
func (r *dposRuntime) cleanupRuntime() {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 🆕 先停止所有定时器，确保没有goroutine在运行
	if r.voteTimer != nil {
		r.voteTimer.Stop()
		r.voteTimer = nil
	}
	if r.networkHealthTimer != nil {
		r.networkHealthTimer.Stop()
		r.networkHealthTimer = nil
	}

	// 🆕 等待一小段时间，确保正在运行的goroutine能够完成
	time.Sleep(100 * time.Millisecond)

	// 清理投票者映射
	r.voters = nil
	r.delegates = nil
}

// startSignatureCleanup 启动签名去重清理协程
func (r *dposRuntime) startSignatureCleanup() {
	go func() {
		defer func() {
			if panicErr := recover(); panicErr != nil {
				r.logger.Error("签名清理协程panic", "error", panicErr)
			}
		}()

		ticker := time.NewTicker(30 * time.Second) // 每30秒清理一次
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.cleanupSignatureMaps()
			case <-r.closeCh:
				return
			}
		}
	}()
}

// cleanupSignatureMaps 异步清理所有签名map
func (r *dposRuntime) cleanupSignatureMaps() {
	now := time.Now()

	// 清理签名请求
	r.signatureRequestDedupMutex.Lock()
	for k, v := range r.processedSignatureRequests {
		if now.Sub(v) > 2*time.Minute { // 改为2分钟
			delete(r.processedSignatureRequests, k)
		}
	}
	r.signatureRequestDedupMutex.Unlock()

	// 清理签名响应
	r.signatureResponseDedupMutex.Lock()
	for k, v := range r.processedSignatureResponses {
		if now.Sub(v) > 2*time.Minute {
			delete(r.processedSignatureResponses, k)
		}
	}
	r.signatureResponseDedupMutex.Unlock()

	// 清理签名生成
	r.signatureGenerationDedupMutex.Lock()
	for k, v := range r.processedSignatureGenerations {
		if now.Sub(v) > 2*time.Minute {
			delete(r.processedSignatureGenerations, k)
		}
	}
	r.signatureGenerationDedupMutex.Unlock()
}

