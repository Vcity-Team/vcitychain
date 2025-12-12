package dpos

import (
	"time"
)

// checkNetworkHealth 检查网络健康状态
func (r *dposRuntime) checkNetworkHealth() bool {
	if r.network == nil {
		r.logger.Debug("网络健康检查失败：网络服务不可用")
		return false
	}

	// 检查主题是否可用
	topic, err := r.getSignatureRequestTopic()
	if err != nil || topic == nil {
		r.logger.Debug("网络健康检查失败：签名请求主题不可用", "error", err)
		return false
	}

	// 检查是否有足够的网络连接
	connectedPeers := r.getConnectedPeersCount()
	if connectedPeers < 2 {
		r.logger.Debug("网络健康检查失败：网络连接不足", "connectedPeers", connectedPeers)
		return false
	}

	r.logger.Debug("网络健康检查通过", "connectedPeers", connectedPeers)
	return true
}

// recoverNetworkConnection 自动恢复网络连接
func (r *dposRuntime) recoverNetworkConnection() {
	r.logger.Warn("检测到网络连接问题，尝试自动恢复")

	// 重新创建网络主题
	r.topicMutex.Lock()
	r.signatureRequestTopic = nil
	r.signatureResponseTopic = nil
	r.signatureQueryTopic = nil
	r.topicMutex.Unlock()

	r.logger.Debug("网络主题已重置，等待重新创建")

	// 重新初始化网络集成层
	if r.networkIntegration != nil {
		r.logger.Debug("重新初始化网络集成层")
		if err := r.networkIntegration.Stop(); err != nil {
			r.logger.Warn("停止网络集成层失败", "error", err)
		}

		// 重新设置网络集成层
		if err := r.setupNetworkIntegration(); err != nil {
			r.logger.Error("重新设置网络集成层失败", "error", err)
		} else {
			r.logger.Info("网络集成层重新初始化成功")
		}
	}
}

// startNetworkHealthMonitoring 启动网络健康监控
func (r *dposRuntime) startNetworkHealthMonitoring() {
	if r.networkHealthTimer == nil {
		// 每30秒检查一次网络健康状态
		r.networkHealthTimer = time.NewTicker(30 * time.Second)
		r.lastNetworkCheck = time.Now()

		go func() {
			for {
				select {
				case <-r.networkHealthTimer.C:
					r.performNetworkHealthCheck()
				case <-r.closeCh:
					return
				}
			}
		}()
	}

	r.logger.Debug("网络健康监控已启动")
}

// performNetworkHealthCheck 执行网络健康检查
func (r *dposRuntime) performNetworkHealthCheck() {
	r.lastNetworkCheck = time.Now()

	// 检查网络健康状态
	if !r.checkNetworkHealth() {
		r.logger.Warn("定期网络健康检查失败，尝试自动恢复")
		go r.recoverNetworkConnection()
	} else {
		r.logger.Debug("定期网络健康检查通过")
	}
}
