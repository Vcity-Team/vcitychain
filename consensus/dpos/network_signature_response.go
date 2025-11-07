package dpos

import (
	"context"
	"fmt"

	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

// listenForSignatureResponses 监听签名响应消息
func (r *dposRuntime) listenForSignatureResponses(ctx context.Context, listener *SignatureListener) {
	// 移除重复的签名请求监听，因为全局监听器已经处理了
	r.logger.Debug("启动签名响应监听器", "checkpointHash", listener.checkpointHash.String())

	// 监听上下文取消
	<-ctx.Done()
	r.logger.Debug("签名响应监听器停止")
}

// handleSignatureResponseMessage 处理签名响应消息
func (r *dposRuntime) handleSignatureResponseMessage(obj interface{}, from peer.ID, listener *SignatureListener) {

	protoResponse, ok := obj.(*dposProto.SignatureResponse)
	if !ok {
		r.logger.Warn("received invalid signature response message", "from", from.String())
		return
	}

	response := &SignatureResponse{
		ValidatorAddr:  types.BytesToAddress(protoResponse.ValidatorAddr),
		Signature:      protoResponse.Signature,
		CheckpointHash: types.BytesToHash(protoResponse.CheckpointHash),
		Timestamp:      protoResponse.Timestamp,
	}

	// 验证消息是否针对当前的checkpoint
	if response.CheckpointHash != listener.checkpointHash {
		r.logger.Debug("ignoring signature response for different checkpoint",
			"received", response.CheckpointHash.String(),
			"expected", listener.checkpointHash.String())
		return
	}

	// 检查是否已经收到过该验证者的签名
	listener.receivedMutex.RLock()
	alreadyReceived := listener.receivedSigs[response.ValidatorAddr]
	listener.receivedMutex.RUnlock()

	if alreadyReceived {
		r.logger.Debug("ignoring duplicate signature from validator",
			"validator", response.ValidatorAddr.String())
		return
	}

	// 验证签名响应
	if err := r.validateSignatureResponse(response); err != nil {
		r.logger.Warn("invalid signature response", "error", err, "validator", response.ValidatorAddr.String())
		return
	}

	// 验证签名
	if err := r.verifyValidatorSignatureByAddress(response.ValidatorAddr, response.Signature, response.CheckpointHash); err != nil {
		r.logger.Warn("signature verification failed", "error", err, "validator", response.ValidatorAddr.String())
		return
	}

	// 标记已收到该验证者的签名
	listener.receivedMutex.Lock()
	listener.receivedSigs[response.ValidatorAddr] = true
	listener.receivedMutex.Unlock()

	// 发送到签名通道
	select {
	case listener.signatureCh <- response:
		r.logger.Info("成功接收签名响应",
			"validator", response.ValidatorAddr.String(),
			"from", from.String(),
			"signatureLength", len(response.Signature),
			"checkpointHash", response.CheckpointHash.String())
	default:
		r.logger.Warn("signature channel is full, dropping response",
			"validator", response.ValidatorAddr.String())
	}
}

// subscribeToSignatureTopic 订阅签名响应主题
func (r *dposRuntime) subscribeToSignatureTopic(listener *SignatureListener) error {
	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("network service not available, cannot subscribe to signature topic")
		return fmt.Errorf("network service not available, cannot subscribe to signature topic")
	}

	// 获取签名响应主题
	topic, err := r.getSignatureResponseTopic()
	if err != nil {
		r.logger.Warn("failed to get signature response topic, using fallback", "error", err)
		r.logger.Debug("订阅签名响应主题（回退模式）")
		return nil
	}

	// 检查topic是否为nil
	if topic == nil {
		r.logger.Warn("signature response topic is nil, using fallback")
		r.logger.Debug("订阅签名响应主题（回退模式）")
		return nil
	}

	// 订阅主题 - 使用闭包传递listener参数
	handler := func(obj interface{}, from peer.ID) {
		r.handleSignatureResponseMessage(obj, from, listener)
	}
	if err := topic.Subscribe(handler); err != nil {
		r.logger.Warn("failed to subscribe to signature response topic, using fallback", "error", err)
		r.logger.Debug("订阅签名响应主题（回退模式）")
		return nil
	}

	// 保存主题引用
	listener.topic = topic

	r.logger.Info("成功订阅签名响应主题")

	// 注意：network.Topic 没有 Unsubscribe 方法，使用 Close() 会自动关闭所有订阅者
	// 在 SignatureListener.Close() 中会清理 topic 引用，避免内存泄露
	return nil
}

// HandleSignatureResponse 处理来自网络集成的签名响应
func (r *dposRuntime) HandleSignatureResponse(response *SignatureResponse) error {
	r.logger.Info("收到来自网络集成的签名响应",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String())

	// 这里可以添加签名响应的处理逻辑
	// 例如：验证签名、更新状态等
	return nil
}
