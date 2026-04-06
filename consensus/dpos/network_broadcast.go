package dpos

import (
	"fmt"
	"time"

	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/golang/protobuf/proto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// broadcastSignatureRequest 广播签名请求
func (r *dposRuntime) broadcastSignatureRequest(protoRequest *dposProto.SignatureRequest) error {
	// 验证签名请求的有效性，防止发送无效消息
	if protoRequest == nil {
		return fmt.Errorf("signature request is nil")
	}

	// 检查是否是查询请求
	if protoRequest.BlockNumber == 0 {
		// 查询请求必须包含有效的标识符
		if string(protoRequest.BlockHash) != "QUERY_REQUEST" && string(protoRequest.CheckpointHash) != "QUERY_REQUEST" {
			r.logger.Warn("阻止发送无效的查询请求",
				"blockHash", string(protoRequest.BlockHash),
				"checkpointHash", string(protoRequest.CheckpointHash))
			return fmt.Errorf("invalid query request: missing QUERY_REQUEST identifier")
		}
	} else {
		// 正常签名请求必须包含有效字段
		if protoRequest.BlockNumber == 0 {
			return fmt.Errorf("invalid block number: cannot be 0 for non-query requests")
		}
		if len(protoRequest.CheckpointHash) == 0 {
			return fmt.Errorf("invalid checkpoint hash: cannot be empty")
		}
		if len(protoRequest.Proposer) == 0 {
			return fmt.Errorf("invalid proposer: cannot be empty")
		}
	}

	checkpointHash := types.BytesToHash(protoRequest.CheckpointHash)

	// 创建内部请求对象
	internalRequest := &SignatureRequest{
		BlockNumber:    protoRequest.BlockNumber,
		BlockHash:      types.BytesToHash(protoRequest.BlockHash),
		CheckpointHash: checkpointHash,
		Round:          protoRequest.Round,
		Proposer:       types.BytesToAddress(protoRequest.Proposer),
		Timestamp:      protoRequest.Timestamp,
	}

	// 存储待处理的签名请求
	r.signatureRequestMutex.Lock()
	r.pendingSignatureRequests[checkpointHash] = internalRequest
	r.signatureRequestMutex.Unlock()

	// 获取签名请求主题
	_, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		return nil
	}

	// 发布签名请求
	r.logger.Debug("attempting to publish signature request",
		"blockNumber", protoRequest.BlockNumber,
		"round", protoRequest.Round)

	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		return nil
	}

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化 SignatureRequest
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		r.logger.Warn("failed to marshal signature request", "error", err)
		return fmt.Errorf("failed to marshal signature request: %w", err)
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: requestData,
	}

	// 发布签名请求
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature request, using fallback", "error", err)
		return nil
	}

	// 启动基于时间的简单备用传播监控
	go r.simpleFallbackMonitoring(protoRequest, checkpointHash)

	return nil
}

// broadcastSignatureRequestToPeer 向特定节点广播签名请求
func (r *dposRuntime) broadcastSignatureRequestToPeer(request *SignatureRequest, peerID peer.ID) {
	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic for peer broadcast", "error", err)
		return
	}

	// 转换为protobuf格式
	protoRequest := &dposProto.SignatureRequest{
		BlockNumber:    request.BlockNumber,
		BlockHash:      request.BlockHash.Bytes(),
		CheckpointHash: request.CheckpointHash.Bytes(),
		Round:          request.Round,
		Proposer:       request.Proposer.Bytes(),
		Timestamp:      request.Timestamp,
	}

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化签名请求
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		r.logger.Warn("failed to marshal signature request", "error", err, "peer", peerID.String())
		return
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: requestData,
	}

	// 发布签名请求
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature request to peer", "error", err, "peer", peerID.String())
		return
	}
}

// broadcastSignatureQuery 广播签名查询请求
func (r *dposRuntime) broadcastSignatureQuery() {
	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic for query", "error", err)
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
		r.logger.Warn("failed to marshal query request", "error", err)
		return
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: queryData,
	}

	// 发布查询请求
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature query request", "error", err)
		return
	}
}

// broadcastTransaction 广播交易到网络
func (d *DPoS) broadcastTransaction(tx *types.Transaction) error {
	d.logger.Info("=== 开始尝试广播交易 ===")
	d.logger.Info("Attempting to broadcast transaction", "txHash", tx.Hash.String())

	// 暂时跳过广播，因为交易池接口不支持AddTx
	d.logger.Warn("⚠️ 暂时跳过交易广播，因为交易池接口不支持AddTx")
	d.logger.Info("Transaction created but not broadcasted",
		"txHash", tx.Hash.String(),
		"reason", "txPool interface does not support AddTx")

	return nil
}

// sendDirectSignatureRequest 直接发送签名请求给指定peer
func (r *dposRuntime) sendDirectSignatureRequest(peerID peer.ID, protoRequest *dposProto.SignatureRequest) error {
	// 临时方案：重新尝试gossip发布
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		return fmt.Errorf("failed to get signature request topic: %w", err)
	}

	r.logger.Debug("备用传播：重新尝试签名请求gossip发布", "peer", peerID.String()[:8], "区块高度", protoRequest.BlockNumber)

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化签名请求
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal signature request: %w", err)
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: requestData,
	}

	return topic.Publish(dposMsg)
}

// sendDirectSignatureRequestWithRetry 带重试的直接签名请求
func (r *dposRuntime) sendDirectSignatureRequestWithRetry(peerID peer.ID, protoRequest *dposProto.SignatureRequest) error {
	const maxRetries = 3
	const baseDelay = 100 * time.Millisecond

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避延迟
			delay := time.Duration(float64(baseDelay) * float64(attempt) * 1.5)
			time.Sleep(delay)
		}

		if err := r.sendDirectSignatureRequest(peerID, protoRequest); err != nil {
			lastErr = err
			r.logger.Debug("直接签名请求重试", "peer", peerID.String()[:8], "attempt", attempt+1, "error", err)
			continue
		}

		// 成功发送
		return nil
	}

	return fmt.Errorf("所有重试尝试都失败了，最后的错误: %w", lastErr)
}
