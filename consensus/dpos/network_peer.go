package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/golang/protobuf/proto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// onNewPeerJoined 添加新节点加入时的处理逻辑
func (r *dposRuntime) onNewPeerJoined(peerID peer.ID) {
	r.logger.Info("新节点加入网络", "peer", peerID.String())

	// 延迟一段时间后查询该节点的待处理签名请求
	// 给节点一些时间完成初始化
	go func() {
		time.Sleep(5 * time.Second)
		r.queryPeerForPendingRequests(peerID)
	}()
}

// queryPeerForPendingRequests 向指定节点查询待处理的签名请求
func (r *dposRuntime) queryPeerForPendingRequests(peerID peer.ID) {
	// 实现真正的RPC查询逻辑
	// 通过现有的签名请求主题发送查询消息

	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic for peer query", "error", err, "peer", peerID.String())
		return
	}

	// 创建查询请求 - 使用明确的标识符避免被误认为是无效请求
	queryRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,                       // 使用0表示这是一个查询请求
		BlockHash:      []byte("QUERY_REQUEST"), // 直接使用字符串标识符
		CheckpointHash: []byte("QUERY_REQUEST"), // 直接使用字符串标识符
		Round:          0,
		Proposer:       types.Address(r.config.Key.Address()).Bytes(),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化查询请求
	queryData, err := proto.Marshal(queryRequest)
	if err != nil {
		r.logger.Warn("failed to marshal query request", "error", err, "peer", peerID.String())
		return
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: queryData,
	}

	// 发布查询请求到网络
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature query request", "error", err, "peer", peerID.String())
		return
	}
}

// queryPendingSignatureRequests 查询待处理的签名请求
func (r *dposRuntime) queryPendingSignatureRequests() {
	// 调试：检查当前节点的签名请求存储状态
	r.debugPendingSignatureRequests()

	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Warn("network service not available, cannot query pending signature requests")
		return
	}

	// 获取当前连接的节点
	peers := r.network.Peers()
	if len(peers) == 0 {
		r.logger.Info("no connected peers, skipping pending signature request query")
		return
	}

	// 向所有连接的节点查询待处理的签名请求
	for _, peer := range peers {
		go r.queryPeerForPendingRequests(peer.Info.ID)
	}

	// 实现简单的广播查询机制
	// 通过现有的签名请求主题发送查询消息
	r.broadcastSignatureQuery()
}

// getConnectedPeersCount 获取实际连接的节点数量
func (r *dposRuntime) getConnectedPeersCount() int {
	if r.network == nil {
		return 0
	}

	peers := r.network.Peers()
	return len(peers)
}

