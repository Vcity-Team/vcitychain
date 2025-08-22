package dpos

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

// DPOSMessage 使用 protobuf 生成的 TransportMessage
type DPOSMessage = dposProto.TransportMessage

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

	// 协程管理器
	goroutineManager *GoroutineManager

	// 锁
	lock sync.RWMutex

	// 添加DPoS运行时回调
	dposRuntime interface{}
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

	// 增加重试机制
	retryCount    int
	maxRetries    int
	retryInterval time.Duration

	// 增加状态管理
	isActive       bool
	collectedCount int
	requiredCount  int

	// 增加互斥锁保护
	mutex sync.RWMutex

	// 协程管理器引用
	goroutineManager *GoroutineManager
}

// NewSignatureCollector 创建新的签名收集器
func NewSignatureCollector(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration, requiredCount int, goroutineManager *GoroutineManager) *SignatureCollector {
	return &SignatureCollector{
		checkpointHash:   checkpointHash,
		signatureCh:      signatureCh,
		receivedSigs:     make(map[types.Address]bool),
		timeout:          time.Now().Add(timeout),
		logger:           hclog.NewNullLogger(),
		maxRetries:       3,
		retryInterval:    2 * time.Second,
		isActive:         true,
		requiredCount:    requiredCount,
		goroutineManager: goroutineManager,
	}
}

// AddSignature 添加签名到收集器
func (sc *SignatureCollector) AddSignature(response *SignatureResponse) bool {
	// 快速检查基本状态，避免长时间持有锁
	sc.mutex.RLock()
	if !sc.isActive {
		sc.mutex.RUnlock()
		return false
	}

	// 检查是否已过期
	if time.Now().After(sc.timeout) {
		sc.mutex.RUnlock()
		sc.logger.Warn("签名收集器已过期", "checkpointHash", sc.checkpointHash.String())
		return false
	}

	// 检查是否已收到该验证者的签名
	if sc.receivedSigs[response.ValidatorAddr] {
		sc.mutex.RUnlock()
		sc.logger.Debug("重复签名，跳过", "validator", response.ValidatorAddr.String())
		return false
	}
	sc.mutex.RUnlock()

	// 升级为写锁进行状态更新
	sc.mutex.Lock()
	// 再次检查状态（可能在升级锁期间状态已改变）
	if !sc.isActive || time.Now().After(sc.timeout) || sc.receivedSigs[response.ValidatorAddr] {
		sc.mutex.Unlock()
		return false
	}

	// 添加签名
	sc.receivedSigs[response.ValidatorAddr] = true
	sc.collectedCount++

	// 检查是否达到所需数量
	completed := sc.collectedCount >= sc.requiredCount
	if completed {
		sc.isActive = false
	}

	sc.mutex.Unlock()

	sc.logger.Debug("签名收集器收到签名",
		"validator", response.ValidatorAddr.String(),
		"collected", sc.collectedCount,
		"required", sc.requiredCount,
		"checkpointHash", sc.checkpointHash.String())

	if completed {
		sc.logger.Debug("签名收集器完成",
			"checkpointHash", sc.checkpointHash.String(),
			"collected", sc.collectedCount,
			"required", sc.requiredCount)
	}

	// 尝试发送到签名通道，使用非阻塞方式
	select {
	case sc.signatureCh <- response:
		// 成功发送
		sc.logger.Debug("签名响应发送成功",
			"validator", response.ValidatorAddr.String(),
			"checkpointHash", sc.checkpointHash.String())
		return true
	default:
		// 通道满，记录警告但不丢弃签名
		sc.logger.Warn("签名响应通道已满，启动重试机制",
			"validator", response.ValidatorAddr.String(),
			"checkpointHash", sc.checkpointHash.String())

		// 使用协程管理器启动重试协程
		if sc.goroutineManager != nil {
			sc.goroutineManager.StartRetryGoroutine("signature-retry", func() {
				// 等待一段时间后重试
				time.Sleep(100 * time.Millisecond)

				select {
				case sc.signatureCh <- response:
					sc.logger.Debug("签名响应重试发送成功",
						"validator", response.ValidatorAddr.String(),
						"checkpointHash", sc.checkpointHash.String())
				case <-time.After(5 * time.Second):
					sc.logger.Error("签名响应重试发送失败，丢弃响应",
						"validator", response.ValidatorAddr.String(),
						"checkpointHash", sc.checkpointHash.String())
				}
			})
		} else {
			sc.logger.Error("协程管理器不可用，无法重试签名发送")
		}

		return false
	}
}

// IsExpired 检查收集器是否已过期
func (sc *SignatureCollector) IsExpired() bool {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return time.Now().After(sc.timeout)
}

// IsComplete 检查收集是否完成
func (sc *SignatureCollector) IsComplete() bool {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.collectedCount >= sc.requiredCount
}

// GetCollectedCount 获取已收集的签名数量
func (sc *SignatureCollector) GetCollectedCount() int {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.collectedCount
}

// GetRequiredCount 获取所需签名数量
func (sc *SignatureCollector) GetRequiredCount() int {
	sc.mutex.RLock()
	defer sc.mutex.RUnlock()
	return sc.requiredCount
}

// Close 关闭收集器
func (sc *SignatureCollector) Close() {
	sc.mutex.Lock()
	defer sc.mutex.Unlock()
	sc.isActive = false
}

// NewNetworkIntegration 创建网络集成管理器
func NewNetworkIntegration(network *network.Server, logger hclog.Logger) *NetworkIntegration {
	ni := &NetworkIntegration{
		logger:              logger.Named("network-integration"),
		network:             network,
		handlers:            make(map[string]MessageHandler),
		signatureCollectors: make(map[types.Hash]*SignatureCollector),
		goroutineManager:    NewGoroutineManager(logger, 1000, 100), // 最大1000个协程，100个重试工作器
	}

	// 注册消息处理器
	ni.registerHandlers()

	return ni
}

// NewNetworkIntegrationWithExistingTopics 创建使用现有主题的网络集成管理器
func NewNetworkIntegrationWithExistingTopics(network *network.Server, logger hclog.Logger, runtime interface{}) *NetworkIntegration {
	ni := &NetworkIntegration{
		logger:              logger.Named("network-integration"),
		network:             network,
		handlers:            make(map[string]MessageHandler),
		signatureCollectors: make(map[types.Hash]*SignatureCollector),
		goroutineManager:    NewGoroutineManager(logger, 1000, 100), // 最大1000个协程，100个重试工作器
	}

	// 注册消息处理器
	ni.registerHandlers()

	// 尝试获取现有主题（如果可用）
	if runtime != nil {
		// 使用反射获取现有主题（这是一个临时的解决方案）
		// 在实际使用中，应该通过接口来获取主题
		ni.logger.Info("使用现有主题的网络集成管理器")
	}

	return ni
}

// SetExistingTopics 设置现有主题
func (ni *NetworkIntegration) SetExistingTopics(signatureRequestTopic, signatureResponseTopic *network.Topic) {
	ni.signatureRequestTopic = signatureRequestTopic
	ni.signatureResponseTopic = signatureResponseTopic
	ni.logger.Info("set existing topics for network integration")
}

// Start 启动网络集成
func (ni *NetworkIntegration) Start() error {
	ni.logger.Info("starting DPoS network integration")

	// 检查是否已经有主题（使用现有主题的情况）
	if ni.signatureRequestTopic == nil || ni.signatureResponseTopic == nil {
		ni.logger.Info("没有现有主题，尝试创建新主题")
		// 创建主题
		if err := ni.createTopics(); err != nil {
			ni.logger.Warn("主题创建失败，尝试使用现有主题", "error", err)
			// 尝试从网络服务获取现有主题
			if err := ni.tryGetExistingTopics(); err != nil {
				return fmt.Errorf("failed to create or get existing topics: %w", err)
			}
		}
	} else {
		ni.logger.Info("using existing topics")
	}

	// 检查关键主题是否可用
	if ni.signatureRequestTopic == nil && ni.signatureResponseTopic == nil {
		ni.logger.Error("关键主题不可用，无法启动网络集成")
		return fmt.Errorf("critical topics not available")
	}

	// 订阅主题
	if err := ni.subscribeToTopics(); err != nil {
		return fmt.Errorf("failed to subscribe to topics: %w", err)
	}

	ni.logger.Info("DPoS network integration started successfully",
		"signatureRequestTopic", ni.signatureRequestTopic != nil,
		"signatureResponseTopic", ni.signatureResponseTopic != nil,
		"voteTopic", ni.voteTopic != nil,
		"delegateTopic", ni.delegateTopic != nil)
	return nil
}

// Stop 停止网络集成
func (ni *NetworkIntegration) Stop() error {
	ni.logger.Info("stopping DPoS network integration")

	// 关闭协程管理器
	if ni.goroutineManager != nil {
		ni.goroutineManager.Close()
	}

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
	var criticalTopicsCreated int

	// 创建签名请求主题 - 使用有效的默认值避免网络层发送零值消息
	// 使用查询请求作为模板，这样网络层就不会发送无效的默认消息
	defaultSignatureRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,
		BlockHash:      []byte("QUERY_REQUEST"),
		CheckpointHash: []byte("QUERY_REQUEST"),
		Round:          0,
		Proposer:       []byte("DEFAULT_PROPOSER"),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认请求作为模板
	defaultRequestData, err := proto.Marshal(defaultSignatureRequest)
	if err != nil {
		ni.logger.Warn("failed to marshal default signature request template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultRequestData = []byte("QUERY_REQUEST")
	}

	ni.signatureRequestTopic, err = ni.network.NewTopic("dpos-signature-request", &dposProto.TransportMessage{
		Data: defaultRequestData,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("签名请求主题已存在，跳过创建")
			// 主题已存在，我们无法直接获取，但可以继续
		} else {
			return fmt.Errorf("failed to create signature request topic: %w", err)
		}
	} else {
		criticalTopicsCreated++
		ni.logger.Info("成功创建签名请求主题")
	}

	// 创建签名响应主题 - 使用有效的默认值避免网络层发送零值消息
	defaultSignatureResponse := &dposProto.SignatureResponse{
		ValidatorAddr:  []byte("DEFAULT_VALIDATOR"),
		Signature:      []byte("DEFAULT_SIGNATURE"),
		CheckpointHash: []byte("DEFAULT_CHECKPOINT"),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认响应作为模板
	defaultResponseData, err := proto.Marshal(defaultSignatureResponse)
	if err != nil {
		ni.logger.Warn("failed to marshal default signature response template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultResponseData = []byte("DEFAULT_RESPONSE")
	}

	ni.signatureResponseTopic, err = ni.network.NewTopic("dpos-signature-response", &dposProto.TransportMessage{
		Data: defaultResponseData,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("签名响应主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create signature response topic: %w", err)
		}
	} else {
		criticalTopicsCreated++
		ni.logger.Info("成功创建签名响应主题")
	}

	// 创建投票主题 - 使用protobuf序列化
	ni.voteTopic, err = ni.network.NewTopic("dpos-vote", &DPOSMessage{
		Data: nil,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("投票主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create vote topic: %w", err)
		}
	} else {
		ni.logger.Info("成功创建投票主题")
	}

	// 创建委托主题 - 使用protobuf序列化
	ni.delegateTopic, err = ni.network.NewTopic("dpos-delegate", &DPOSMessage{
		Data: nil,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("委托主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create delegate topic: %w", err)
		}
	} else {
		ni.logger.Info("成功创建委托主题")
	}

	// 检查是否至少有一个关键主题可用
	if ni.signatureRequestTopic == nil && ni.signatureResponseTopic == nil {
		ni.logger.Error("无法创建任何关键主题")
		return fmt.Errorf("failed to create any critical topics")
	}

	ni.logger.Info("DPoS网络主题创建完成",
		"criticalTopicsCreated", criticalTopicsCreated,
		"signatureRequestTopic", ni.signatureRequestTopic != nil,
		"signatureResponseTopic", ni.signatureResponseTopic != nil)
	return nil
}

// tryGetExistingTopics 尝试获取现有主题
func (ni *NetworkIntegration) tryGetExistingTopics() error {
	ni.logger.Info("尝试获取现有主题")

	// 由于网络服务器没有GetTopic方法，我们使用一个更简单的策略
	// 尝试创建主题，如果失败则使用回退模式
	ni.logger.Info("无法直接获取现有主题，使用回退模式")

	// 设置一个标志，表示我们处于回退模式
	ni.logger.Warn("网络集成将使用回退模式，某些功能可能受限")

	// 在这种情况下，我们仍然可以尝试创建主题
	// 如果主题已存在，createTopics会处理这种情况
	if err := ni.createTopics(); err != nil {
		ni.logger.Error("回退模式下的主题创建也失败", "error", err)
		return err
	}

	ni.logger.Info("回退模式下成功创建或使用现有主题")
	return nil
}

// subscribeToTopics 订阅网络主题
func (ni *NetworkIntegration) subscribeToTopics() error {
	// 订阅签名请求主题
	if ni.signatureRequestTopic != nil {
		if err := ni.signatureRequestTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleSignatureRequest(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to signature request topic: %w", err)
		}
		ni.logger.Info("成功订阅签名请求主题")
	} else {
		ni.logger.Warn("签名请求主题不可用，跳过订阅")
	}

	// 订阅签名响应主题
	if ni.signatureResponseTopic != nil {
		if err := ni.signatureResponseTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleSignatureResponse(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to signature response topic: %w", err)
		}
		ni.logger.Info("成功订阅签名响应主题")
	} else {
		ni.logger.Warn("签名响应主题不可用，跳过订阅")
	}

	// 订阅投票主题
	if ni.voteTopic != nil {
		if err := ni.voteTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleVoteMessage(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to vote topic: %w", err)
		}
		ni.logger.Info("成功订阅投票主题")
	} else {
		ni.logger.Warn("投票主题不可用，跳过订阅")
	}

	// 订阅委托主题
	if ni.delegateTopic != nil {
		if err := ni.delegateTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleDelegateMessage(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to delegate topic: %w", err)
		}
		ni.logger.Info("成功订阅委托主题")
	} else {
		ni.logger.Warn("委托主题不可用，跳过订阅")
	}

	ni.logger.Info("DPoS网络主题订阅完成")
	return nil
}

// registerHandlers 注册消息处理器
func (ni *NetworkIntegration) registerHandlers() {
	ni.handlers["signature_request"] = func(obj interface{}, from peer.ID) error {
		if dposMsg, ok := obj.(*DPOSMessage); ok {
			// 使用 protobuf 反序列化
			var request dposProto.SignatureRequest
			if err := proto.Unmarshal(dposMsg.Data, &request); err != nil {
				return fmt.Errorf("failed to unmarshal signature request: %w", err)
			}
			// 转换为内部类型
			internalRequest := &SignatureRequest{
				BlockNumber:    request.BlockNumber,
				BlockHash:      types.BytesToHash(request.BlockHash),
				CheckpointHash: types.BytesToHash(request.CheckpointHash),
				Round:          request.Round,
				Proposer:       types.BytesToAddress(request.Proposer),
				Timestamp:      request.Timestamp,
			}
			return ni.processSignatureRequest(internalRequest)
		}
		return fmt.Errorf("invalid message type for signature request")
	}

	ni.handlers["signature_response"] = func(obj interface{}, from peer.ID) error {
		if dposMsg, ok := obj.(*DPOSMessage); ok {
			// 使用 protobuf 反序列化
			var response dposProto.SignatureResponse
			if err := proto.Unmarshal(dposMsg.Data, &response); err != nil {
				return fmt.Errorf("failed to unmarshal signature response: %w", err)
			}
			// 转换为内部类型
			internalResponse := &SignatureResponse{
				ValidatorAddr:  types.BytesToAddress(response.ValidatorAddr),
				Signature:      response.Signature,
				CheckpointHash: types.BytesToHash(response.CheckpointHash),
				Timestamp:      response.Timestamp,
			}
			return ni.processSignatureResponse(internalResponse)
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
	// 首先验证消息对象的有效性
	if !ni.isValidSignatureRequestMessage(obj) {
		ni.logger.Debug("收到无效的签名请求消息，跳过处理",
			"from", from.String(),
			"messageType", fmt.Sprintf("%T", obj))
		return
	}

	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("received invalid transport message for signature request", "from", from.String())
		return
	}

	// 添加调试信息
	ni.logger.Debug("received DPOSMessage",
		"dataLength", len(dposMsg.Data),
		"dataPreview", string(dposMsg.Data[:min(len(dposMsg.Data), 100)]),
		"from", from.String())

	// 使用 protobuf 反序列化
	var request dposProto.SignatureRequest
	if err := proto.Unmarshal(dposMsg.Data, &request); err != nil {
		ni.logger.Warn("failed to unmarshal signature request",
			"error", err,
			"dataLength", len(dposMsg.Data),
			"dataPreview", fmt.Sprintf("%x", dposMsg.Data[:min(len(dposMsg.Data), 20)]),
			"from", from.String())
		return
	}

	// 转换为内部类型
	internalRequest := &SignatureRequest{
		BlockNumber:    request.BlockNumber,
		BlockHash:      types.BytesToHash(request.BlockHash),
		CheckpointHash: types.BytesToHash(request.CheckpointHash),
		Round:          request.Round,
		Proposer:       types.BytesToAddress(request.Proposer),
		Timestamp:      request.Timestamp,
	}

	ni.logger.Debug("received signature request",
		"blockNumber", internalRequest.BlockNumber,
		"checkpointHash", internalRequest.CheckpointHash.String(),
		"proposer", internalRequest.Proposer.String(),
		"from", from.String())

	// 验证签名请求的有效性
	// 首先检查是否是查询请求（BlockNumber=0 且包含查询标识符）
	if internalRequest.BlockNumber == 0 {
		// 检查是否是查询请求 - 统一使用字符串比较
		blockHashStr := string(internalRequest.BlockHash.Bytes())
		checkpointHashStr := string(internalRequest.CheckpointHash.Bytes())

		if blockHashStr == "QUERY_REQUEST" || checkpointHashStr == "QUERY_REQUEST" {
			// 这是查询请求，正常处理
			ni.logger.Debug("收到查询请求，正常处理",
				"blockNumber", internalRequest.BlockNumber,
				"blockHash", internalRequest.BlockHash.String(),
				"checkpointHash", internalRequest.CheckpointHash.String(),
				"from", from.String())
		} else {
			// 这是无效的请求
			ni.logger.Warn("收到无效的签名请求：BlockNumber 为 0 但不是查询请求，忽略此请求",
				"blockHash", internalRequest.BlockHash.String(),
				"checkpointHash", internalRequest.CheckpointHash.String(),
				"proposer", internalRequest.Proposer.String(),
				"from", from.String())
			return
		}
	} else {
		// 对于正常的签名请求，检查其他字段
		if internalRequest.CheckpointHash == (types.Hash{}) {
			ni.logger.Warn("收到无效的签名请求：CheckpointHash 为全零，忽略此请求",
				"blockNumber", internalRequest.BlockNumber,
				"proposer", internalRequest.Proposer.String(),
				"from", from.String())
			return
		}

		if internalRequest.Proposer == (types.Address{}) {
			ni.logger.Warn("收到无效的签名请求：Proposer 地址为空，忽略此请求",
				"checkpointHash", internalRequest.CheckpointHash.String(),
				"from", from.String())
			return
		}
	}

	// 处理签名请求
	if err := ni.processSignatureRequest(internalRequest); err != nil {
		ni.logger.Error("failed to process signature request", "error", err)
	}
}

// isValidSignatureRequestMessage 验证签名请求消息的有效性
func (ni *NetworkIntegration) isValidSignatureRequestMessage(obj interface{}) bool {
	if obj == nil {
		return false
	}

	// 检查是否是 DPOSMessage 类型
	if dposMsg, ok := obj.(*DPOSMessage); ok {
		// 检查 Data 字段是否为空
		if len(dposMsg.Data) == 0 {
			return false
		}

		// 尝试反序列化为 SignatureRequest
		var request dposProto.SignatureRequest
		if err := proto.Unmarshal(dposMsg.Data, &request); err != nil {
			return false
		}

		// 验证 SignatureRequest 字段
		return ni.isValidSignatureRequest(&request)
	}

	// 如果不是 DPOSMessage 类型，尝试直接作为 SignatureRequest 处理
	if request, ok := obj.(*dposProto.SignatureRequest); ok {
		return ni.isValidSignatureRequest(request)
	}

	return false
}

// isValidSignatureRequest 验证 SignatureRequest 的有效性
func (ni *NetworkIntegration) isValidSignatureRequest(request *dposProto.SignatureRequest) bool {
	if request == nil {
		return false
	}

	// 检查是否是查询请求（BlockNumber=0）
	if request.BlockNumber == 0 {
		// 查询请求必须包含有效的标识符
		blockHashStr := string(request.BlockHash)
		checkpointHashStr := string(request.CheckpointHash)

		if blockHashStr == "QUERY_REQUEST" || checkpointHashStr == "QUERY_REQUEST" {
			return true // 有效的查询请求
		}
		// BlockNumber=0 但没有有效标识符，认为是无效消息
		return false
	}

	// 对于非查询请求，检查其他必要字段
	if len(request.CheckpointHash) == 0 {
		return false // CheckpointHash 为空
	}

	if len(request.Proposer) == 0 {
		return false // Proposer 为空
	}

	return true
}

// handleSignatureResponse 处理签名响应消息
func (ni *NetworkIntegration) handleSignatureResponse(obj interface{}, from peer.ID) {
	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("received invalid transport message for signature response", "from", from.String())
		return
	}

	// 添加调试信息
	ni.logger.Debug("received DPOSMessage for signature response",
		"dataLength", len(dposMsg.Data),
		"dataPreview", fmt.Sprintf("%x", dposMsg.Data[:min(len(dposMsg.Data), 20)]))

	// 使用 protobuf 反序列化
	var response dposProto.SignatureResponse
	if err := proto.Unmarshal(dposMsg.Data, &response); err != nil {
		ni.logger.Warn("failed to unmarshal signature response",
			"error", err,
			"dataLength", len(dposMsg.Data),
			"dataPreview", fmt.Sprintf("%x", dposMsg.Data[:min(len(dposMsg.Data), 20)]),
			"from", from.String())
		return
	}

	// 转换为内部类型
	internalResponse := &SignatureResponse{
		ValidatorAddr:  types.BytesToAddress(response.ValidatorAddr),
		Signature:      response.Signature,
		CheckpointHash: types.BytesToHash(response.CheckpointHash),
		Timestamp:      response.Timestamp,
	}

	ni.logger.Debug("received signature response",
		"validator", internalResponse.ValidatorAddr.String(),
		"checkpointHash", internalResponse.CheckpointHash.String(),
		"from", from.String())

	// 转发给对应的签名收集器
	ni.forwardSignatureResponse(internalResponse)
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
	ni.logger.Info("processing signature request", "blockNumber", request.BlockNumber)

	// 如果有DPoS运行时回调，调用它处理签名请求
	if ni.dposRuntime != nil {
		// 使用反射调用DPoS运行时的签名请求处理方法
		if runtime, ok := ni.dposRuntime.(interface {
			HandleSignatureRequest(*SignatureRequest) error
		}); ok {
			return runtime.HandleSignatureRequest(request)
		}

		// 尝试其他可能的接口
		if runtime, ok := ni.dposRuntime.(interface {
			ProcessSignatureRequest(*SignatureRequest) error
		}); ok {
			return runtime.ProcessSignatureRequest(request)
		}
	}

	ni.logger.Warn("没有可用的DPoS运行时回调来处理签名请求")
	return nil
}

// processSignatureResponse 处理签名响应
func (ni *NetworkIntegration) processSignatureResponse(response *SignatureResponse) error {
	ni.logger.Info("processing signature response", "validator", response.ValidatorAddr.String())

	// 如果有DPoS运行时回调，调用它处理签名响应
	if ni.dposRuntime != nil {
		// 使用反射调用DPoS运行时的签名响应处理方法
		if runtime, ok := ni.dposRuntime.(interface {
			HandleSignatureResponse(*SignatureResponse) error
		}); ok {
			return runtime.HandleSignatureResponse(response)
		}

		// 尝试其他可能的接口
		if runtime, ok := ni.dposRuntime.(interface {
			ProcessSignatureResponse(*SignatureResponse) error
		}); ok {
			return runtime.ProcessSignatureResponse(response)
		}
	}

	ni.logger.Warn("没有可用的DPoS运行时回调来处理签名响应")
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

// forwardSignatureResponse 转发签名响应到相应的收集器
func (ni *NetworkIntegration) forwardSignatureResponse(response *SignatureResponse) {
	// 使用读锁快速查找收集器
	ni.lock.RLock()
	collector, exists := ni.signatureCollectors[response.CheckpointHash]
	ni.lock.RUnlock()

	if !exists {
		//ni.logger.Warn("未找到签名收集器，忽略响应",
		//	"checkpointHash", response.CheckpointHash.String(),
		//	"validator", response.ValidatorAddr.String())
		return
	}

	ni.logger.Debug("处理签名响应",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String(),
		"signatureLength", len(response.Signature))

	// 使用AddSignature方法处理签名，它会自动更新内部状态
	if collector.AddSignature(response) {
		ni.logger.Debug("签名响应处理成功",
			"validator", response.ValidatorAddr.String(),
			"checkpointHash", response.CheckpointHash.String(),
			"collectedCount", collector.GetCollectedCount(),
			"requiredCount", collector.GetRequiredCount())
	} else {
		ni.logger.Debug("签名响应处理失败",
			"validator", response.ValidatorAddr.String(),
			"checkpointHash", response.CheckpointHash.String())
	}
}

// RegisterSignatureCollector 注册签名收集器
func (ni *NetworkIntegration) RegisterSignatureCollector(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration, requiredCount int) {
	ni.lock.Lock()
	defer ni.lock.Unlock()

	// 检查是否已存在相同checkpointHash的收集器
	if existingCollector, exists := ni.signatureCollectors[checkpointHash]; exists {
		ni.logger.Warn("签名收集器已存在，关闭旧的收集器",
			"checkpointHash", checkpointHash.String(),
			"existingRequiredCount", existingCollector.requiredCount,
			"newRequiredCount", requiredCount)
		existingCollector.Close()
	}

	// 创建新的签名收集器
	collector := NewSignatureCollector(checkpointHash, signatureCh, timeout, requiredCount, ni.goroutineManager)
	collector.logger = ni.logger.Named("signature-collector")

	ni.signatureCollectors[checkpointHash] = collector

	ni.logger.Info("注册签名收集器成功",
		"checkpointHash", checkpointHash.String(),
		"timeout", timeout,
		"requiredCount", requiredCount,
		"totalCollectors", len(ni.signatureCollectors))

	// 启动清理工作器
	ni.goroutineManager.StartGoroutine("collector-cleanup", func() {
		ni.startCollectorCleanupWorker(context.Background(), checkpointHash)
	})

	// 启动定期状态检查
	ni.goroutineManager.StartGoroutine("collector-status-check", func() {
		ni.monitorCollectorStatus(checkpointHash)
	})
}

// 为了向后兼容，添加一个重载方法
func (ni *NetworkIntegration) RegisterSignatureCollectorLegacy(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration) {
	ni.RegisterSignatureCollector(checkpointHash, signatureCh, timeout, 1)
}

// SetDPoSRuntime 设置DPoS运行时回调
func (ni *NetworkIntegration) SetDPoSRuntime(runtime interface{}) {
	ni.dposRuntime = runtime
	ni.logger.Info("DPoS运行时回调已设置")
}

// GetSignatureRequestTopic 获取签名请求主题
func (ni *NetworkIntegration) GetSignatureRequestTopic() *network.Topic {
	return ni.signatureRequestTopic
}

// GetSignatureResponseTopic 获取签名响应主题
func (ni *NetworkIntegration) GetSignatureResponseTopic() *network.Topic {
	return ni.signatureResponseTopic
}

// startCollectorCleanupWorker 启动收集器清理工作器
func (ni *NetworkIntegration) startCollectorCleanupWorker(ctx context.Context, checkpointHash types.Hash) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// 添加超时控制，避免无限运行
	timeout := time.After(10 * time.Minute) // 10分钟后自动退出

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			ni.logger.Debug("收集器清理工作器超时，自动退出",
				"checkpointHash", checkpointHash.String())
			return
		case <-ticker.C:
			// 快速检查收集器是否存在
			ni.lock.RLock()
			collector, exists := ni.signatureCollectors[checkpointHash]
			ni.lock.RUnlock()

			if !exists {
				return
			}

			// 检查是否过期或完成（这些方法使用收集器内部的锁，不会与外部锁冲突）
			if collector.IsExpired() || collector.IsComplete() {
				ni.logger.Debug("清理签名收集器",
					"checkpointHash", checkpointHash.String(),
					"expired", collector.IsExpired(),
					"complete", collector.IsComplete())

				// 使用单独的锁操作来注销收集器，避免长时间持有锁
				ni.UnregisterSignatureCollector(checkpointHash)
				return
			}
		}
	}
}

// UnregisterSignatureCollector 注销签名收集器
func (ni *NetworkIntegration) UnregisterSignatureCollector(checkpointHash types.Hash) {
	ni.lock.Lock()
	delete(ni.signatureCollectors, checkpointHash)
	ni.lock.Unlock()

	ni.logger.Debug("unregistered signature collector",
		"checkpointHash", checkpointHash.String())
}

// BroadcastSignatureRequest 广播签名请求
func (ni *NetworkIntegration) BroadcastSignatureRequest(request *SignatureRequest) error {
	if ni.signatureRequestTopic == nil {
		ni.logger.Warn("签名请求主题不可用，无法广播")
		return fmt.Errorf("signature request topic not initialized")
	}

	// 验证签名请求的有效性，防止发送无效消息
	if request == nil {
		return fmt.Errorf("signature request is nil")
	}

	// 检查是否是查询请求
	if request.BlockNumber == 0 {
		// 查询请求必须包含有效的标识符
		if string(request.BlockHash.Bytes()) != "QUERY_REQUEST" && string(request.CheckpointHash.Bytes()) != "QUERY_REQUEST" {
			ni.logger.Warn("阻止发送无效的查询请求",
				"blockHash", request.BlockHash.String(),
				"checkpointHash", request.CheckpointHash.String())
			return fmt.Errorf("invalid query request: missing QUERY_REQUEST identifier")
		}
	} else {
		// 正常签名请求必须包含有效字段
		if request.BlockNumber == 0 {
			return fmt.Errorf("invalid block number: cannot be 0 for non-query requests")
		}
		if request.CheckpointHash == (types.Hash{}) {
			return fmt.Errorf("invalid checkpoint hash: cannot be zero hash")
		}
		if request.Proposer == (types.Address{}) {
			return fmt.Errorf("invalid proposer: cannot be zero address")
		}
	}

	// 转换为 protobuf 消息
	protoRequest := &dposProto.SignatureRequest{
		BlockNumber:    request.BlockNumber,
		BlockHash:      request.BlockHash.Bytes(),
		CheckpointHash: request.CheckpointHash.Bytes(),
		Round:          request.Round,
		Proposer:       request.Proposer.Bytes(),
		Timestamp:      request.Timestamp,
	}

	// 使用 protobuf 序列化
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal signature request: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Data: requestData,
	}

	// 添加调试信息
	ni.logger.Debug("broadcasting signature request",
		"dataLength", len(requestData),
		"dataPreview", fmt.Sprintf("%x", requestData[:min(len(requestData), 20)]))

	if err := ni.signatureRequestTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish signature request: %w", err)
	}

	ni.logger.Info("成功广播签名请求",
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String())

	return nil
}

// BroadcastSignatureResponse 广播签名响应
func (ni *NetworkIntegration) BroadcastSignatureResponse(response *SignatureResponse) error {
	if ni.signatureResponseTopic == nil {
		return fmt.Errorf("signature response topic not initialized")
	}

	// 转换为 protobuf 消息
	protoResponse := &dposProto.SignatureResponse{
		ValidatorAddr:  response.ValidatorAddr.Bytes(),
		Signature:      response.Signature,
		CheckpointHash: response.CheckpointHash.Bytes(),
		Timestamp:      response.Timestamp,
	}

	// 使用 protobuf 序列化
	responseData, err := proto.Marshal(protoResponse)
	if err != nil {
		return fmt.Errorf("failed to marshal signature response: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Data: responseData,
	}

	if err := ni.signatureResponseTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish signature response: %w", err)
	}

	ni.logger.Debug("broadcasted signature response",
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
		Data: voteData,
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
		Data: delegateData,
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
	ni.goroutineManager.StartGoroutine("global-cleanup", func() {
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
	})
}

// cleanupExpiredCollectors 清理过期的签名收集器
func (ni *NetworkIntegration) cleanupExpiredCollectors() {
	// 先收集需要清理的收集器，避免在持有锁时进行耗时操作
	var expiredCollectors []types.Hash

	ni.lock.RLock()
	for checkpointHash, collector := range ni.signatureCollectors {
		if time.Now().After(collector.timeout) {
			expiredCollectors = append(expiredCollectors, checkpointHash)
		}
	}
	ni.lock.RUnlock()

	// 批量清理过期的收集器
	if len(expiredCollectors) > 0 {
		ni.lock.Lock()
		for _, checkpointHash := range expiredCollectors {
			if collector, exists := ni.signatureCollectors[checkpointHash]; exists {
				// 再次检查是否过期（可能在检查期间状态已改变）
				if time.Now().After(collector.timeout) {
					delete(ni.signatureCollectors, checkpointHash)
					ni.logger.Debug("cleaned up expired signature collector",
						"checkpointHash", checkpointHash.String())
				}
			}
		}
		ni.lock.Unlock()

		ni.logger.Info("cleaned up expired signature collectors", "count", len(expiredCollectors))
	}
}

// monitorCollectorStatus 监控签名收集器状态
func (ni *NetworkIntegration) monitorCollectorStatus(checkpointHash types.Hash) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// 添加超时控制，避免无限运行
	timeout := time.After(15 * time.Minute) // 15分钟后自动退出

	for {
		select {
		case <-timeout:
			ni.logger.Debug("收集器状态监控超时，自动退出",
				"checkpointHash", checkpointHash.String())
			return
		case <-ticker.C:
			// 快速检查收集器是否存在
			ni.lock.RLock()
			collector, exists := ni.signatureCollectors[checkpointHash]
			ni.lock.RUnlock()

			if !exists {
				ni.logger.Debug("签名收集器已不存在，停止监控",
					"checkpointHash", checkpointHash.String())
				return
			}

			// 检查收集器状态（这些方法使用收集器内部的锁，不会与外部锁冲突）
			if collector.IsExpired() {
				ni.logger.Warn("签名收集器已过期",
					"checkpointHash", checkpointHash.String(),
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount())
			} else if collector.IsComplete() {
				ni.logger.Info("签名收集器已完成",
					"checkpointHash", checkpointHash.String(),
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount())
			} else {
				ni.logger.Debug("签名收集器状态",
					"checkpointHash", checkpointHash.String(),
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount(),
					"isActive", collector.isActive)
			}
		}
	}
}
