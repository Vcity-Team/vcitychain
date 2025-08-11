package dpos

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	ibftMessages "github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
)

// DPOSMessage is a wrapper for DPoS messages using ibftMessages.Message
type DPOSMessage struct {
	*ibftMessages.Message
	Data []byte
}

// Reset implements proto.Message
func (m *DPOSMessage) Reset() {
	*m = DPOSMessage{}
}

// String implements proto.Message
func (m *DPOSMessage) String() string {
	return "DPOSMessage"
}

// ProtoMessage implements proto.Message
func (m *DPOSMessage) ProtoMessage() {}

// Convert TransportMessage to DPOSMessage
func (tm *TransportMessage) ToDPOSMessage() *DPOSMessage {
	data, _ := json.Marshal(tm)
	return &DPOSMessage{
		Message: &ibftMessages.Message{},
		Data:    data,
	}
}

// Convert DPOSMessage to TransportMessage
func (dm *DPOSMessage) ToTransport() *TransportMessage {
	var tm TransportMessage
	json.Unmarshal(dm.Data, &tm)
	return &tm
}

// NetworkIntegration DPoS网络集成管理器
type NetworkIntegration struct {
	logger hclog.Logger

	// 网络服务
	network *network.Server

	// 主题管理
	signatureRequestTopic  *network.Topic
	signatureResponseTopic *network.Topic
	voteTopic              *network.Topic
	delegateTopic          *network.Topic

	// 消息处理器
	handlers map[string]MessageHandler

	// 签名收集器
	signatureCollectors map[types.Hash]*SignatureCollector

	// 锁
	lock sync.RWMutex
}

// MessageHandler 消息处理器接口
type MessageHandler func(obj interface{}, from peer.ID) error

// SignatureCollector 签名收集器
type SignatureCollector struct {
	checkpointHash types.Hash
	signatureCh    chan *SignatureResponse
	receivedSigs   map[types.Address]bool
	timeout        time.Time
	logger         hclog.Logger
}

// NewNetworkIntegration 创建网络集成管理器
func NewNetworkIntegration(network *network.Server, logger hclog.Logger) *NetworkIntegration {
	ni := &NetworkIntegration{
		logger:              logger.Named("network-integration"),
		network:             network,
		handlers:            make(map[string]MessageHandler),
		signatureCollectors: make(map[types.Hash]*SignatureCollector),
	}

	// 注册消息处理器
	ni.registerHandlers()

	return ni
}

// Start 启动网络集成
func (ni *NetworkIntegration) Start() error {
	ni.logger.Info("starting DPoS network integration")

	// 创建主题
	if err := ni.createTopics(); err != nil {
		return fmt.Errorf("failed to create topics: %w", err)
	}

	// 订阅主题
	if err := ni.subscribeToTopics(); err != nil {
		return fmt.Errorf("failed to subscribe to topics: %w", err)
	}

	ni.logger.Info("DPoS network integration started successfully")
	return nil
}

// Stop 停止网络集成
func (ni *NetworkIntegration) Stop() error {
	ni.logger.Info("stopping DPoS network integration")

	// 关闭所有主题
	if ni.signatureRequestTopic != nil {
		ni.signatureRequestTopic.Close()
	}
	if ni.signatureResponseTopic != nil {
		ni.signatureResponseTopic.Close()
	}
	if ni.voteTopic != nil {
		ni.voteTopic.Close()
	}
	if ni.delegateTopic != nil {
		ni.delegateTopic.Close()
	}

	ni.logger.Info("DPoS network integration stopped")
	return nil
}

// createTopics 创建网络主题
func (ni *NetworkIntegration) createTopics() error {
	var err error

	// 创建签名请求主题 - 使用protobuf序列化
	ni.signatureRequestTopic, err = ni.network.NewTopic("dpos-signature-request", &DPOSMessage{})
	if err != nil {
		return fmt.Errorf("failed to create signature request topic: %w", err)
	}

	// 创建签名响应主题 - 使用protobuf序列化
	ni.signatureResponseTopic, err = ni.network.NewTopic("dpos-signature-response", &DPOSMessage{})
	if err != nil {
		return fmt.Errorf("failed to create signature response topic: %w", err)
	}

	// 创建投票主题 - 使用protobuf序列化
	ni.voteTopic, err = ni.network.NewTopic("dpos-vote", &DPOSMessage{})
	if err != nil {
		return fmt.Errorf("failed to create vote topic: %w", err)
	}

	// 创建委托主题 - 使用protobuf序列化
	ni.delegateTopic, err = ni.network.NewTopic("dpos-delegate", &DPOSMessage{})
	if err != nil {
		return fmt.Errorf("failed to create delegate topic: %w", err)
	}

	ni.logger.Info("created DPoS network topics")
	return nil
}

// subscribeToTopics 订阅网络主题
func (ni *NetworkIntegration) subscribeToTopics() error {
	// 订阅签名请求主题
	if err := ni.signatureRequestTopic.Subscribe(func(obj interface{}, from peer.ID) {
		ni.handleSignatureRequest(obj, from)
	}); err != nil {
		return fmt.Errorf("failed to subscribe to signature request topic: %w", err)
	}

	// 订阅签名响应主题
	if err := ni.signatureResponseTopic.Subscribe(func(obj interface{}, from peer.ID) {
		ni.handleSignatureResponse(obj, from)
	}); err != nil {
		return fmt.Errorf("failed to subscribe to signature response topic: %w", err)
	}

	// 订阅投票主题
	if err := ni.voteTopic.Subscribe(func(obj interface{}, from peer.ID) {
		ni.handleVoteMessage(obj, from)
	}); err != nil {
		return fmt.Errorf("failed to subscribe to vote topic: %w", err)
	}

	// 订阅委托主题
	if err := ni.delegateTopic.Subscribe(func(obj interface{}, from peer.ID) {
		ni.handleDelegateMessage(obj, from)
	}); err != nil {
		return fmt.Errorf("failed to subscribe to delegate topic: %w", err)
	}

	ni.logger.Info("subscribed to DPoS network topics")
	return nil
}

// registerHandlers 注册消息处理器
func (ni *NetworkIntegration) registerHandlers() {
	ni.handlers["signature_request"] = func(obj interface{}, from peer.ID) error {
		if dposMsg, ok := obj.(*DPOSMessage); ok {
			var request SignatureRequest
			if err := json.Unmarshal(dposMsg.Data, &request); err != nil {
				return fmt.Errorf("failed to unmarshal signature request: %w", err)
			}
			return ni.processSignatureRequest(&request)
		}
		return fmt.Errorf("invalid message type for signature request")
	}

	ni.handlers["signature_response"] = func(obj interface{}, from peer.ID) error {
		if dposMsg, ok := obj.(*DPOSMessage); ok {
			var response SignatureResponse
			if err := json.Unmarshal(dposMsg.Data, &response); err != nil {
				return fmt.Errorf("failed to unmarshal signature response: %w", err)
			}
			return ni.processSignatureResponse(&response)
		}
		return fmt.Errorf("invalid message type for signature response")
	}

	ni.handlers["vote"] = func(obj interface{}, from peer.ID) error {
		if dposMsg, ok := obj.(*DPOSMessage); ok {
			var vote VoteMessage
			if err := json.Unmarshal(dposMsg.Data, &vote); err != nil {
				return fmt.Errorf("failed to unmarshal vote message: %w", err)
			}
			return ni.processVoteMessage(&vote)
		}
		return fmt.Errorf("invalid message type for vote message")
	}

	ni.handlers["delegate"] = func(obj interface{}, from peer.ID) error {
		if dposMsg, ok := obj.(*DPOSMessage); ok {
			var delegate DelegateMessage
			if err := json.Unmarshal(dposMsg.Data, &delegate); err != nil {
				return fmt.Errorf("failed to unmarshal delegate message: %w", err)
			}
			return ni.processDelegateMessage(&delegate)
		}
		return fmt.Errorf("invalid message type for delegate message")
	}
}

// handleSignatureRequest 处理签名请求消息
func (ni *NetworkIntegration) handleSignatureRequest(obj interface{}, from peer.ID) {
	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("received invalid transport message for signature request", "from", from.String())
		return
	}

	var request SignatureRequest
	if err := json.Unmarshal(dposMsg.Data, &request); err != nil {
		ni.logger.Warn("failed to unmarshal signature request", "error", err, "from", from.String())
		return
	}

	ni.logger.Debug("received signature request",
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String(),
		"proposer", request.Proposer.String(),
		"from", from.String())

	// 处理签名请求
	if err := ni.processSignatureRequest(&request); err != nil {
		ni.logger.Error("failed to process signature request", "error", err)
	}
}

// handleSignatureResponse 处理签名响应消息
func (ni *NetworkIntegration) handleSignatureResponse(obj interface{}, from peer.ID) {
	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("received invalid transport message for signature response", "from", from.String())
		return
	}

	var response SignatureResponse
	if err := json.Unmarshal(dposMsg.Data, &response); err != nil {
		ni.logger.Warn("failed to unmarshal signature response", "error", err, "from", from.String())
		return
	}

	ni.logger.Debug("received signature response",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String(),
		"from", from.String())

	// 转发给对应的签名收集器
	ni.forwardSignatureResponse(&response)
}

// handleVoteMessage 处理投票消息
func (ni *NetworkIntegration) handleVoteMessage(obj interface{}, from peer.ID) {
	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("received invalid transport message for vote", "from", from.String())
		return
	}

	var vote VoteMessage
	if err := json.Unmarshal(dposMsg.Data, &vote); err != nil {
		ni.logger.Warn("failed to unmarshal vote message", "error", err, "from", from.String())
		return
	}

	ni.logger.Debug("received vote message",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"from", from.String())

	// 处理投票消息
	if err := ni.processVoteMessage(&vote); err != nil {
		ni.logger.Error("failed to process vote message", "error", err)
	}
}

// handleDelegateMessage 处理委托消息
func (ni *NetworkIntegration) handleDelegateMessage(obj interface{}, from peer.ID) {
	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("received invalid transport message for delegate", "from", from.String())
		return
	}

	var delegate DelegateMessage
	if err := json.Unmarshal(dposMsg.Data, &delegate); err != nil {
		ni.logger.Warn("failed to unmarshal delegate message", "error", err, "from", from.String())
		return
	}

	ni.logger.Debug("received delegate message",
		"delegate", delegate.Delegate.String(),
		"action", delegate.Action,
		"from", from.String())

	// 处理委托消息
	if err := ni.processDelegateMessage(&delegate); err != nil {
		ni.logger.Error("failed to process delegate message", "error", err)
	}
}

// processSignatureRequest 处理签名请求
func (ni *NetworkIntegration) processSignatureRequest(request *SignatureRequest) error {
	// 这里应该调用DPoS实例的签名请求处理逻辑
	ni.logger.Info("processing signature request", "blockNumber", request.BlockNumber)
	return nil
}

// processSignatureResponse 处理签名响应
func (ni *NetworkIntegration) processSignatureResponse(response *SignatureResponse) error {
	// 这里应该调用DPoS实例的签名响应处理逻辑
	ni.logger.Info("processing signature response", "validator", response.ValidatorAddr.String())
	return nil
}

// processVoteMessage 处理投票消息
func (ni *NetworkIntegration) processVoteMessage(vote *VoteMessage) error {
	// 这里应该调用DPoS实例的投票处理逻辑
	ni.logger.Info("processing vote message", "voter", vote.Voter.String())
	return nil
}

// processDelegateMessage 处理委托消息
func (ni *NetworkIntegration) processDelegateMessage(delegate *DelegateMessage) error {
	// 这里应该调用DPoS实例的委托处理逻辑
	ni.logger.Info("processing delegate message", "delegate", delegate.Delegate.String())
	return nil
}

// forwardSignatureResponse 转发签名响应给收集器
func (ni *NetworkIntegration) forwardSignatureResponse(response *SignatureResponse) {
	ni.lock.RLock()
	collector, exists := ni.signatureCollectors[response.CheckpointHash]
	ni.lock.RUnlock()

	if !exists {
		ni.logger.Debug("no signature collector found for checkpoint",
			"checkpointHash", response.CheckpointHash.String())
		return
	}

	// 检查是否已经收到过该验证者的签名
	if collector.receivedSigs[response.ValidatorAddr] {
		ni.logger.Debug("ignoring duplicate signature",
			"validator", response.ValidatorAddr.String())
		return
	}

	// 检查超时
	if time.Now().After(collector.timeout) {
		ni.logger.Debug("signature collector timeout",
			"checkpointHash", response.CheckpointHash.String())
		return
	}

	// 标记已收到签名
	collector.receivedSigs[response.ValidatorAddr] = true

	// 发送到签名通道
	select {
	case collector.signatureCh <- response:
		ni.logger.Info("forwarded signature response to collector",
			"validator", response.ValidatorAddr.String(),
			"checkpointHash", response.CheckpointHash.String())
	default:
		ni.logger.Warn("signature collector channel is full",
			"validator", response.ValidatorAddr.String())
	}
}

// RegisterSignatureCollector 注册签名收集器
func (ni *NetworkIntegration) RegisterSignatureCollector(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration) {
	collector := &SignatureCollector{
		checkpointHash: checkpointHash,
		signatureCh:    signatureCh,
		receivedSigs:   make(map[types.Address]bool),
		timeout:        time.Now().Add(timeout),
		logger:         ni.logger,
	}

	ni.lock.Lock()
	ni.signatureCollectors[checkpointHash] = collector
	ni.lock.Unlock()

	ni.logger.Info("registered signature collector",
		"checkpointHash", checkpointHash.String(),
		"timeout", timeout)
}

// UnregisterSignatureCollector 注销签名收集器
func (ni *NetworkIntegration) UnregisterSignatureCollector(checkpointHash types.Hash) {
	ni.lock.Lock()
	delete(ni.signatureCollectors, checkpointHash)
	ni.lock.Unlock()

	ni.logger.Info("unregistered signature collector",
		"checkpointHash", checkpointHash.String())
}

// BroadcastSignatureRequest 广播签名请求
func (ni *NetworkIntegration) BroadcastSignatureRequest(request *SignatureRequest) error {
	if ni.signatureRequestTopic == nil {
		return fmt.Errorf("signature request topic not initialized")
	}

	// 序列化请求
	requestData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal signature request: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Message: &ibftMessages.Message{},
		Data:    requestData,
	}

	if err := ni.signatureRequestTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish signature request: %w", err)
	}

	ni.logger.Info("broadcasted signature request",
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String())

	return nil
}

// BroadcastSignatureResponse 广播签名响应
func (ni *NetworkIntegration) BroadcastSignatureResponse(response *SignatureResponse) error {
	if ni.signatureResponseTopic == nil {
		return fmt.Errorf("signature response topic not initialized")
	}

	// 序列化响应
	responseData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal signature response: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Message: &ibftMessages.Message{},
		Data:    responseData,
	}

	if err := ni.signatureResponseTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish signature response: %w", err)
	}

	ni.logger.Info("broadcasted signature response",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String())

	return nil
}

// BroadcastVoteMessage 广播投票消息
func (ni *NetworkIntegration) BroadcastVoteMessage(vote *VoteMessage) error {
	if ni.voteTopic == nil {
		return fmt.Errorf("vote topic not initialized")
	}

	// 序列化投票消息
	voteData, err := json.Marshal(vote)
	if err != nil {
		return fmt.Errorf("failed to marshal vote message: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Message: &ibftMessages.Message{},
		Data:    voteData,
	}

	if err := ni.voteTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish vote message: %w", err)
	}

	ni.logger.Info("broadcasted vote message",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String())

	return nil
}

// BroadcastDelegateMessage 广播委托消息
func (ni *NetworkIntegration) BroadcastDelegateMessage(delegate *DelegateMessage) error {
	if ni.delegateTopic == nil {
		return fmt.Errorf("delegate topic not initialized")
	}

	// 序列化委托消息
	delegateData, err := json.Marshal(delegate)
	if err != nil {
		return fmt.Errorf("failed to marshal delegate message: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Message: &ibftMessages.Message{},
		Data:    delegateData,
	}

	if err := ni.delegateTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish delegate message: %w", err)
	}

	ni.logger.Info("broadcasted delegate message",
		"delegate", delegate.Delegate.String(),
		"action", delegate.Action)

	return nil
}

// StartCleanupWorker 启动清理工作协程
func (ni *NetworkIntegration) StartCleanupWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ni.cleanupExpiredCollectors()
			}
		}
	}()
}

// cleanupExpiredCollectors 清理过期的签名收集器
func (ni *NetworkIntegration) cleanupExpiredCollectors() {
	ni.lock.Lock()
	defer ni.lock.Unlock()

	now := time.Now()
	expiredCount := 0

	for checkpointHash, collector := range ni.signatureCollectors {
		if now.After(collector.timeout) {
			delete(ni.signatureCollectors, checkpointHash)
			expiredCount++

			ni.logger.Debug("cleaned up expired signature collector",
				"checkpointHash", checkpointHash.String())
		}
	}

	if expiredCount > 0 {
		ni.logger.Info("cleaned up expired signature collectors", "count", expiredCount)
	}
}
