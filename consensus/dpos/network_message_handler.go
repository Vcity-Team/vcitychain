package dpos

import (
	"time"
)

// setupNetworkEventListeners 设置网络事件监听器
func (r *dposRuntime) setupNetworkEventListeners() {
	// 监听新节点加入事件
	// 注意：这里需要根据实际的网络事件系统来实现
	// 由于当前代码中没有直接的网络事件监听机制，我们使用定时器来模拟

	// 启动定期检查新节点的协程
	go r.periodicPeerCheck()
}

// periodicPeerCheck 定期检查新节点
func (r *dposRuntime) periodicPeerCheck() {
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	lastPeerCount := 0

	for {
		select {
		case <-ticker.C:
			if r.network == nil {
				continue
			}

			currentPeers := r.network.Peers()
			currentPeerCount := len(currentPeers)

			// 如果节点数量增加，说明有新节点加入
			if currentPeerCount > lastPeerCount {
				// 检测到新节点加入（静默处理）

				// 查询新节点的待处理签名请求
				r.queryPendingSignatureRequests()
			}

			lastPeerCount = currentPeerCount

		case <-r.closeCh:
			return
		}
	}
}




