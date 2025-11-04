package dpos

import (
	"context"
	"fmt"

	"github.com/Vcity-Team/vcitychain/types"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// listenForSignatureRequests 监听签名请求并生成响应
func (r *dposRuntime) listenForSignatureRequests(ctx context.Context) {
	r.logger.Debug("开始监听签名请求", "节点地址", types.Address(r.config.Key.Address()).String())

	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("network service not available, cannot listen for signature requests")
		return
	}

	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		r.logger.Info("监听签名请求（回退模式）")
		<-ctx.Done()
		r.logger.Debug("签名请求监听器停止（回退模式）")
		return
	}

	// 订阅主题
	if err := topic.Subscribe(r.handleSignatureRequestMessage); err != nil {
		r.logger.Warn("failed to subscribe to signature request topic, using fallback", "error", err)
		r.logger.Info("监听签名请求（回退模式）")
		<-ctx.Done()
		r.logger.Debug("签名请求监听器停止（回退模式）")
		return
	}

	r.logger.Debug("成功订阅签名请求主题", "节点地址", types.Address(r.config.Key.Address()).String())

	// 监听上下文取消
	<-ctx.Done()

	// 注意：network.Topic 没有 Unsubscribe 方法，使用 Close() 会自动关闭所有订阅者
	// 这里不需要手动取消订阅，因为主题会在程序退出时自动清理
	r.logger.Debug("签名请求监听器停止")
}

// handleSignatureRequestMessage 处理签名请求消息
func (r *dposRuntime) handleSignatureRequestMessage(obj interface{}, from peer.ID) {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot handle signature request message")
		return
	}

	protoRequest, ok := obj.(*dposProto.SignatureRequest)
	if !ok {
		return
	}

	// 检查是否是模板消息（忽略）
	if protoRequest.BlockNumber == 0 && (string(protoRequest.BlockHash) == "TEMPLATE_MSG" || string(protoRequest.CheckpointHash) == "TEMPLATE_MSG") {
		r.logger.Debug("收到模板消息，忽略", "from", from.String())
		return
	}

	// 检查是否是查询请求
	if protoRequest.BlockNumber == 0 && (string(protoRequest.BlockHash) == "QUERY_REQUEST" || string(protoRequest.CheckpointHash) == "QUERY_REQUEST") {
		r.logger.Debug("收到签名查询请求", "from", from.String())
		r.handleSignatureQueryRequest(from)
		return
	}

	// 转换为内部格式
	request := &SignatureRequest{
		BlockNumber:    protoRequest.BlockNumber,
		BlockHash:      types.BytesToHash(protoRequest.BlockHash),
		CheckpointHash: types.BytesToHash(protoRequest.CheckpointHash),
		Round:          protoRequest.Round,
		Proposer:       types.BytesToAddress(protoRequest.Proposer),
		Timestamp:      protoRequest.Timestamp,
	}

	// 验证签名请求的有效性
	// 首先检查是否是模板消息（BlockNumber=0 且包含模板标识符）
	if request.BlockNumber == 0 {
		// 检查是否是模板消息
		if string(protoRequest.BlockHash) == "TEMPLATE_MSG" || string(protoRequest.CheckpointHash) == "TEMPLATE_MSG" {
			// 这是模板消息，忽略
			r.logger.Debug("收到模板消息，忽略",
				"blockNumber", request.BlockNumber,
				"blockHash", request.BlockHash.String(),
				"checkpointHash", request.CheckpointHash.String(),
				"from", from.String())
			return
		} else if string(protoRequest.BlockHash) == "QUERY_REQUEST" || string(protoRequest.CheckpointHash) == "QUERY_REQUEST" {
			// 这是查询请求，正常处理
			r.logger.Debug("收到查询请求，正常处理",
				"blockNumber", request.BlockNumber,
				"blockHash", request.BlockHash.String(),
				"checkpointHash", request.CheckpointHash.String(),
				"from", from.String())
		} else {
			// 这是无效的请求
			return
		}
	} else {
		// 对于正常的签名请求，检查其他字段
		if request.CheckpointHash == (types.Hash{}) {
			r.logger.Warn("收到无效的签名请求：CheckpointHash 为全零，忽略此请求",
				"blockNumber", request.BlockNumber,
				"proposer", request.Proposer.String(),
				"from", from.String())
			return
		}

		if request.Proposer == (types.Address{}) {
			r.logger.Warn("收到无效的签名请求：Proposer 地址为空，忽略此请求",
				"checkpointHash", request.CheckpointHash.String(),
				"proposer", request.Proposer.String(),
				"from", from.String())
			return
		}
	}

	// 简化处理：直接处理签名请求，不做去重检查

	// 获取当前区块高度
	delegateCount := uint64(len(r.delegates))
	if delegateCount == 0 {
		// 如果委托者列表为空，使用默认值
		delegateCount = 4 // 默认4个委托者
	}
	// 🆕 已删除 currentDelegateIndex，使用区块号计算
	currentBlock := r.config.blockchain.CurrentHeader()
	currentBlockNumber := uint64(0)
	if currentBlock != nil {
		currentBlockNumber = currentBlock.Number
	}

	// 检查是否是过期请求（区块号差距过大）
	if currentBlockNumber > request.BlockNumber+10 {
		r.logger.Debug("忽略过期签名请求",
			"from", from.String(),
			"requestBlockNumber", request.BlockNumber,
			"currentBlockNumber", currentBlockNumber,
			"本地节点", types.Address(r.config.Key.Address()).String())
		return
	}

	// 检查是否是自己的请求
	if request.Proposer == types.Address(r.config.Key.Address()) {
		return
	}

	// 检查自己是否是验证者
	if !r.isValidator() {
		r.logger.Info("自己不是验证者，忽略签名请求", "本地节点", types.Address(r.config.Key.Address()).String())
		return
	}

	// 使用并发控制，限制同时处理的签名请求数量
	select {
	case r.signatureRequestSemaphore <- struct{}{}:
		// 获取到信号量，可以处理签名请求
		go func() {
			defer func() { <-r.signatureRequestSemaphore }() // 释放信号量

			if err := r.generateSignatureResponse(request); err != nil {
				r.logger.Error("failed to generate signature response", "error", err)
			}
		}()
	default:
		// 信号量已满，记录警告并跳过处理
	}
}

// handleSignatureQueryRequest 处理签名查询请求
func (r *dposRuntime) handleSignatureQueryRequest(from peer.ID) {
	// 获取所有待处理的签名请求
	r.signatureRequestMutex.RLock()
	pendingRequests := make([]*SignatureRequest, 0, len(r.pendingSignatureRequests))
	for _, request := range r.pendingSignatureRequests {
		pendingRequests = append(pendingRequests, request)
	}
	r.signatureRequestMutex.RUnlock()

	if len(pendingRequests) == 0 {
		return
	}

	// 广播所有待处理的签名请求
	for _, request := range pendingRequests {
		r.broadcastSignatureRequestToPeer(request, from)
	}
}

// HandleSignatureRequest 处理来自网络集成的签名请求
func (r *dposRuntime) HandleSignatureRequest(request *SignatureRequest) error {
	r.logger.Debug("收到来自网络集成的签名请求",
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String(),
		"proposer", request.Proposer.String())

	// 检查是否是自己的请求
	if request.Proposer == types.Address(r.config.Key.Address()) {
		return nil
	}

	// 检查自己是否是验证者
	if !r.isValidator() {
		r.logger.Info("自己不是验证者，忽略签名请求")
		return nil
	}

	// 使用并发控制，限制同时处理的签名请求数量
	select {
	case r.signatureRequestSemaphore <- struct{}{}:
		// 获取到信号量，可以处理签名请求
		go func() {
			defer func() { <-r.signatureRequestSemaphore }() // 释放信号量

			if err := r.generateSignatureResponse(request); err != nil {
				r.logger.Error("failed to generate signature response", "error", err)
			}
		}()
		return nil
	default:
		// 信号量已满，记录警告并跳过处理
		r.logger.Warn("并发签名请求过多，跳过处理",
			"checkpointHash", request.CheckpointHash.String(),
			"proposer", request.Proposer.String(),
			"当前并发数", r.maxConcurrentSignatures)
		return fmt.Errorf("too many concurrent signature requests")
	}
}

