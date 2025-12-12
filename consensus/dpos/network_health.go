package dpos

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
