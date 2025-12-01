package dpos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

// DPOSMessage 使用 protobuf 生成的 TransportMessage
type DPOSMessage = dposProto.TransportMessage

// GenesisDelegate genesis文件中delegate的结构
type GenesisDelegate struct {
	Address   string `json:"address"`
	BlsKey    string `json:"blsKey"`
	Stake     string `json:"stake"`
	MultiAddr string `json:"multiAddr"`
}

// GenesisEngine genesis文件中engine的结构
type GenesisEngine struct {
	Dpos struct {
		BlockTime        int64             `json:"blockTime"`
		DelegateCount    int64             `json:"delegateCount"`
		EpochSize        int64             `json:"epochSize"`
		InitialDelegates []GenesisDelegate `json:"initialDelegates"`
	} `json:"dpos"`
}

// GenesisConfig genesis文件的完整结构
type GenesisConfig struct {
	Engine GenesisEngine `json:"engine"`
}

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

	// 主题管理（使用TopicManager统一管理）
	topicManager *TopicManager

	// 主题引用（保持向后兼容，从topicManager获取）
	signatureRequestTopic  *network.Topic
	signatureResponseTopic *network.Topic
	voteTopic              *network.Topic
	delegateTopic          *network.Topic
	blsKeyBroadcastTopic   *network.Topic
	blsKeyAckTopic         *network.Topic
	blsKeyRequestTopic     *network.Topic
	blsKeyResponseTopic    *network.Topic

	// 消息处理器
	handlers map[string]MessageHandler

	// 签名收集器（使用SignatureCollectorManager统一管理）
	collectorManager *SignatureCollectorManager

	// 协程管理器
	goroutineManager *GoroutineManager

	// 锁
	lock sync.RWMutex

	// 添加DPoS运行时回调
	dposRuntime interface{}

	// BLS公钥管理（使用BLSKeyManager统一管理）
	blsKeyManager *BLSKeyManager

	// 🆕 DPOS实例引用，用于持久化操作
	dposInstance interface{}

	// 🆕 日志频率控制
	lastBroadcastLogTime      *time.Time
	lastBroadcastLogTimeMutex sync.Mutex

	// 🆕 验证者地址与peer映射（使用PeerRegistry统一管理）
	peerRegistry *PeerRegistry
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

	// 增强的清理机制
	createdAt      time.Time     // 创建时间
	lastActivity   time.Time     // 最后活动时间
	cleanupTimeout time.Duration // 清理超时时间
	maxIdleTime    time.Duration // 最大空闲时间
}

// NewSignatureCollector 创建新的签名收集器
func NewSignatureCollector(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration, requiredCount int, goroutineManager *GoroutineManager) *SignatureCollector {
	now := time.Now()
	return &SignatureCollector{
		checkpointHash:   checkpointHash,
		signatureCh:      signatureCh,
		receivedSigs:     make(map[types.Address]bool),
		timeout:          now.Add(timeout),
		logger:           hclog.NewNullLogger(),
		maxRetries:       3,
		retryInterval:    2 * time.Second,
		isActive:         true,
		requiredCount:    requiredCount,
		goroutineManager: goroutineManager,
		createdAt:        now,
		lastActivity:     now,
		cleanupTimeout:   15 * time.Minute, // 15分钟清理超时
		maxIdleTime:      5 * time.Minute,  // 5分钟最大空闲时间
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
		sc.logger.Debug("签名收集器已过期", "checkpointHash", sc.checkpointHash.String())
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
	sc.lastActivity = time.Now() // 更新最后活动时间

	// 检查是否达到所需数量
	completed := sc.collectedCount >= sc.requiredCount
	if completed {
		sc.isActive = false
	}

	sc.mutex.Unlock()

	if completed {
	}

	// 尝试发送到签名通道，使用非阻塞方式
	select {
	case sc.signatureCh <- response:
		// 成功发送
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

	now := time.Now()

	// 检查基本超时
	if now.After(sc.timeout) {
		return true
	}

	// 检查清理超时（创建后15分钟）
	if now.Sub(sc.createdAt) > sc.cleanupTimeout {
		return true
	}

	// 检查空闲超时（5分钟无活动）
	if now.Sub(sc.lastActivity) > sc.maxIdleTime {
		return true
	}

	return false
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
		logger:           logger.Named("network-integration"),
		network:          network,
		handlers:         make(map[string]MessageHandler),
		goroutineManager: NewGoroutineManager(logger, 1000, 100), // 最大1000个协程，100个重试工作器
		peerRegistry:     NewPeerRegistry(),
	}

	// 注册消息处理器
	ni.registerHandlers()

	return ni
}

// SetBLSKeyPersistCallback 设置BLS公钥持久化回调函数
func (ni *NetworkIntegration) SetBLSKeyPersistCallback(callback func(address types.Address, blsKeyBytes []byte) error) {
	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
	}
	ni.blsKeyManager.SetPersistCallback(callback)
}

// SetBLSKeyLookupCallback 设置BLS公钥查找回调函数
func (ni *NetworkIntegration) SetBLSKeyLookupCallback(callback func(address types.Address) ([]byte, error)) {
	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
	}
	ni.blsKeyManager.SetLookupCallback(callback)
}

// SetDPoSInstance 设置DPoS实例引用
func (ni *NetworkIntegration) SetDPoSInstance(dposInstance interface{}) {
	ni.dposInstance = dposInstance
}

// NewNetworkIntegrationWithExistingTopics 创建使用现有主题的网络集成管理器
func NewNetworkIntegrationWithExistingTopics(network *network.Server, logger hclog.Logger, runtime interface{}) *NetworkIntegration {
	ni := &NetworkIntegration{
		logger:           logger.Named("network-integration"),
		network:          network,
		handlers:         make(map[string]MessageHandler),
		goroutineManager: NewGoroutineManager(logger, 1000, 100), // 最大1000个协程，100个重试工作器
		peerRegistry:     NewPeerRegistry(),
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
	// 检查是否已经有主题（使用现有主题的情况）
	if ni.signatureRequestTopic == nil || ni.signatureResponseTopic == nil {
		// 使用TopicManager创建所有topics
		if ni.topicManager == nil {
			ni.topicManager = NewTopicManager(ni.network, ni.logger)
		}

		if err := ni.topicManager.CreateAllTopics(); err != nil {
			ni.logger.Warn("主题创建失败，尝试使用现有主题", "error", err)
			// 尝试从网络服务获取现有主题（兼容旧逻辑）
			if err := ni.tryGetExistingTopics(); err != nil {
				return fmt.Errorf("failed to create or get existing topics: %w", err)
			}
		}

		// 从TopicManager获取所有topics并赋值给字段（保持向后兼容）
		ni.signatureRequestTopic = ni.topicManager.GetTopic("dpos-signature-request")
		ni.signatureResponseTopic = ni.topicManager.GetTopic("dpos-signature-response")
		ni.voteTopic = ni.topicManager.GetTopic("dpos-vote")
		ni.delegateTopic = ni.topicManager.GetTopic("dpos-delegate")
		ni.blsKeyBroadcastTopic = ni.topicManager.GetTopic("dpos-bls-key-broadcast")
		ni.blsKeyAckTopic = ni.topicManager.GetTopic("dpos-bls-key-ack")
		ni.blsKeyRequestTopic = ni.topicManager.GetTopic("dpos-bls-key-request")
		ni.blsKeyResponseTopic = ni.topicManager.GetTopic("dpos-bls-key-response")
	} else {
		ni.logger.Info("using existing topics")
	}

	// 检查关键主题是否可用
	if ni.signatureRequestTopic == nil && ni.signatureResponseTopic == nil {
		ni.logger.Error("关键主题不可用，无法启动网络集成")
		return fmt.Errorf("critical topics not available")
	}

	// 初始化签名收集器管理器
	if ni.collectorManager == nil {
		if ni.goroutineManager == nil {
			ni.goroutineManager = NewGoroutineManager(ni.logger, 1000, 100)
		}
		ni.collectorManager = NewSignatureCollectorManager(ni.logger, ni.goroutineManager)
	}

	// 初始化BLS公钥管理器
	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
		ni.blsKeyManager.SetDPoSInstance(ni.dposInstance)
	}

	// 订阅主题
	if err := ni.subscribeToTopics(); err != nil {
		return fmt.Errorf("failed to subscribe to topics: %w", err)
	}

	// 🆕 从数据库恢复BLS公钥到缓存
	if err := ni.restoreBLSKeysFromDatabase(); err != nil {
		ni.logger.Warn("从数据库恢复BLS公钥失败，但网络集成仍可继续运行", "error", err)
	} else {
	}

	return nil
}

// Stop 停止网络集成
func (ni *NetworkIntegration) Stop() error {
	ni.logger.Info("stopping DPoS network integration")

	// 关闭协程管理器
	if ni.goroutineManager != nil {
		ni.goroutineManager.Close()
	}

	// 使用TopicManager统一关闭所有主题
	if ni.topicManager != nil {
		ni.topicManager.CloseAll()
	} else {
		// 兼容旧逻辑：手动关闭各个topic
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
		if ni.blsKeyBroadcastTopic != nil {
			ni.blsKeyBroadcastTopic.Close()
		}
		if ni.blsKeyAckTopic != nil {
			ni.blsKeyAckTopic.Close()
		}
		if ni.blsKeyRequestTopic != nil {
			ni.blsKeyRequestTopic.Close()
		}
		if ni.blsKeyResponseTopic != nil {
			ni.blsKeyResponseTopic.Close()
		}
	}

	ni.logger.Info("DPoS network integration stopped")
	return nil
}

// createTopics 创建网络主题
func (ni *NetworkIntegration) createTopics() error {
	var err error
	var criticalTopicsCreated int

	// 创建签名请求主题 - 使用有效的默认值避免网络层发送零值消息
	// 使用模板消息作为主题初始化，这样网络层就不会发送无效的默认消息
	defaultSignatureRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,
		BlockHash:      []byte("TEMPLATE_MSG"),
		CheckpointHash: []byte("TEMPLATE_MSG"),
		Round:          0,
		Proposer:       []byte("DEFAULT_PROPOSER"),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认请求作为模板
	defaultRequestData, err := proto.Marshal(defaultSignatureRequest)
	if err != nil {
		ni.logger.Warn("failed to marshal default signature request template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultRequestData = []byte("TEMPLATE_MSG")
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
		// 记录实际使用的protoID
		if ni.signatureRequestTopic != nil {
			actualProtoID := ni.signatureRequestTopic.GetActualProtoID()
			ni.logger.Debug("🔍 网络集成层签名请求Topic名称对比", "原始名称", "dpos-signature-request", "实际名称", actualProtoID)
		}
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
		// 记录实际使用的protoID
		if ni.signatureResponseTopic != nil {
			actualProtoID := ni.signatureResponseTopic.GetActualProtoID()
			ni.logger.Debug("🔍 网络集成层签名响应Topic名称对比", "原始名称", "dpos-signature-response", "实际名称", actualProtoID)
		}
		//ni.logger.Info("成功创建签名响应主题")
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
		//ni.logger.Info("成功创建投票主题")
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
		//ni.logger.Info("成功创建委托主题")
	}

	// 创建BLS公钥广播主题
	ni.blsKeyBroadcastTopic, err = ni.network.NewTopic("dpos-bls-key-broadcast", &DPOSMessage{
		Data: nil,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("BLS公钥广播主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create BLS key broadcast topic: %w", err)
		}
	} else {
	}

	// 创建BLS公钥确认主题
	ni.blsKeyAckTopic, err = ni.network.NewTopic("dpos-bls-key-ack", &DPOSMessage{
		Data: nil,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("BLS公钥确认主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create BLS key ack topic: %w", err)
		}
	} else {
	}

	// 创建BLS公钥请求主题
	ni.blsKeyRequestTopic, err = ni.network.NewTopic("dpos-bls-key-request", &DPOSMessage{
		Data: nil,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("BLS公钥请求主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create BLS key request topic: %w", err)
		}
	} else {
	}

	// 创建BLS公钥响应主题
	ni.blsKeyResponseTopic, err = ni.network.NewTopic("dpos-bls-key-response", &DPOSMessage{
		Data: nil,
	})
	if err != nil {
		// 检查是否是"topic already exists"错误
		if strings.Contains(err.Error(), "topic already exists") {
			ni.logger.Warn("BLS公钥响应主题已存在，跳过创建")
		} else {
			return fmt.Errorf("failed to create BLS key response topic: %w", err)
		}
	} else {
	}

	// 检查是否至少有一个关键主题可用
	if ni.signatureRequestTopic == nil && ni.signatureResponseTopic == nil {
		ni.logger.Error("无法创建任何关键主题")
		return fmt.Errorf("failed to create any critical topics")
	}

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
		actualTopicName := ni.signatureRequestTopic.GetActualProtoID()
		ni.logger.Debug("📡 订阅签名请求主题", "原始名称", "dpos-signature-request", "实际名称", actualTopicName)
		if err := ni.signatureRequestTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleSignatureRequest(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to signature request topic: %w", err)
		}
	} else {
		ni.logger.Warn("签名请求主题不可用，跳过订阅")
	}

	// 订阅签名响应主题
	if ni.signatureResponseTopic != nil {
		actualTopicName := ni.signatureResponseTopic.GetActualProtoID()
		ni.logger.Info("📡 订阅签名响应主题", "原始名称", "dpos-signature-response", "实际名称", actualTopicName)
		if err := ni.signatureResponseTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleSignatureResponse(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to signature response topic: %w", err)
		}
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
	} else {
		ni.logger.Warn("委托主题不可用，跳过订阅")
	}

	// 订阅BLS公钥广播主题
	if ni.blsKeyBroadcastTopic != nil {
		if err := ni.blsKeyBroadcastTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleBLSKeyBroadcast(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to BLS key broadcast topic: %w", err)
		}
	} else {
		ni.logger.Warn("BLS公钥广播主题不可用，跳过订阅")
	}

	// 订阅BLS公钥确认主题
	if ni.blsKeyAckTopic != nil {
		if err := ni.blsKeyAckTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleBLSKeyAck(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to BLS key ack topic: %w", err)
		}
	} else {
		ni.logger.Warn("BLS公钥确认主题不可用，跳过订阅")
	}

	// 订阅BLS公钥请求主题
	if ni.blsKeyRequestTopic != nil {
		if err := ni.blsKeyRequestTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleBLSKeyRequest(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to BLS key request topic: %w", err)
		}
	} else {
		ni.logger.Warn("BLS公钥请求主题不可用，跳过订阅")
	}

	// 订阅BLS公钥响应主题
	if ni.blsKeyResponseTopic != nil {
		if err := ni.blsKeyResponseTopic.Subscribe(func(obj interface{}, from peer.ID) {
			ni.handleBLSKeyResponse(obj, from)
		}); err != nil {
			return fmt.Errorf("failed to subscribe to BLS key response topic: %w", err)
		}
	} else {
		ni.logger.Warn("BLS公钥响应主题不可用，跳过订阅")
	}

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
			// 收到无效的签名请求：BlockNumber 为 0 但不是查询请求，忽略此请求（静默处理）
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
	ni.logger.Debug("processing signature request", "blockNumber", request.BlockNumber)

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
	if ni.collectorManager == nil {
		if ni.goroutineManager == nil {
			ni.goroutineManager = NewGoroutineManager(ni.logger, 1000, 100)
		}
		ni.collectorManager = NewSignatureCollectorManager(ni.logger, ni.goroutineManager)
	}
	ni.collectorManager.ForwardSignatureResponse(response)
}

// RegisterSignatureCollector 注册签名收集器
func (ni *NetworkIntegration) RegisterSignatureCollector(checkpointHash types.Hash, signatureCh chan *SignatureResponse, timeout time.Duration, requiredCount int) {
	if ni.collectorManager == nil {
		if ni.goroutineManager == nil {
			ni.goroutineManager = NewGoroutineManager(ni.logger, 1000, 100)
		}
		ni.collectorManager = NewSignatureCollectorManager(ni.logger, ni.goroutineManager)
	}
	ni.collectorManager.RegisterSignatureCollector(checkpointHash, signatureCh, timeout, requiredCount)
}

// SetDPoSRuntime 设置DPoS运行时回调
func (ni *NetworkIntegration) SetDPoSRuntime(runtime interface{}) {
	ni.dposRuntime = runtime
}

// GetSignatureRequestTopic 获取签名请求主题
func (ni *NetworkIntegration) GetSignatureRequestTopic() *network.Topic {
	return ni.signatureRequestTopic
}

// GetSignatureResponseTopic 获取签名响应主题
func (ni *NetworkIntegration) GetSignatureResponseTopic() *network.Topic {
	return ni.signatureResponseTopic
}

// GetSignatureCollector 获取签名收集器
func (ni *NetworkIntegration) GetSignatureCollector(checkpointHash types.Hash) *SignatureCollector {
	if ni.collectorManager == nil {
		return nil
	}
	return ni.collectorManager.GetSignatureCollector(checkpointHash)
}

// startCollectorCleanupWorker 启动收集器清理工作器
func (ni *NetworkIntegration) startCollectorCleanupWorker(ctx context.Context, checkpointHash types.Hash) {
	ticker := time.NewTicker(10 * time.Second) // 降低频率到10秒，减少goroutine压力
	defer ticker.Stop()

	// 添加超时控制，避免无限运行
	timeout := time.After(15 * time.Minute) // 缩短到15分钟，更快清理

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
			if ni.collectorManager == nil {
				return
			}
			collector := ni.collectorManager.GetSignatureCollector(checkpointHash)
			if collector == nil {
				return
			}

			// 检查是否过期或完成（这些方法使用收集器内部的锁，不会与外部锁冲突）
			expired := collector.IsExpired()
			complete := collector.IsComplete()

			if expired || complete {
				// 清理签名收集器，静默处理

				// 使用单独的锁操作来注销收集器，避免长时间持有锁
				ni.UnregisterSignatureCollector(checkpointHash)
				return
			}

			// 定期记录收集器状态（每30秒）
			if time.Now().Unix()%30 == 0 {
				ni.logger.Debug("签名收集器状态检查",
					"checkpointHash", checkpointHash.String(),
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount(),
					"isActive", collector.isActive)
			}
		}
	}
}

// UnregisterSignatureCollector 注销签名收集器
func (ni *NetworkIntegration) UnregisterSignatureCollector(checkpointHash types.Hash) {
	if ni.collectorManager == nil {
		return
	}
	ni.collectorManager.UnregisterSignatureCollector(checkpointHash)
}

// cleanupExpiredCollectors 清理所有过期的签名收集器
func (ni *NetworkIntegration) cleanupExpiredCollectors() {
	if ni.collectorManager == nil {
		return
	}
	ni.collectorManager.cleanupExpiredCollectors()
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
	actualTopicName := ni.signatureRequestTopic.GetActualProtoID()
	ni.logger.Info("🚀 网络集成层广播签名请求", "原始名称", "dpos-signature-request", "实际名称", actualTopicName, "dataLength", len(requestData))

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

	actualTopicName := ni.signatureResponseTopic.GetActualProtoID()

	// 🆕 统一日志间隔控制（10秒）
	ni.lastBroadcastLogTimeMutex.Lock()
	now := time.Now()
	shouldLog := true
	if ni.lastBroadcastLogTime != nil {
		if now.Sub(*ni.lastBroadcastLogTime) < 10*time.Second {
			shouldLog = false
		}
	}
	if shouldLog {
		ni.lastBroadcastLogTime = &now
		ni.logger.Debug("🚀 网络集成层广播签名响应", "原始名称", "dpos-signature-response", "实际名称", actualTopicName)
	}
	ni.lastBroadcastLogTimeMutex.Unlock()

	if err := ni.signatureResponseTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("failed to publish signature response: %w", err)
	}

	// 签名响应已广播

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
				ni.cleanupExpiredBLSKeys()
			}
		}
	})
}

// cleanupExpiredBLSKeys 清理过期的BLS公钥缓存
func (ni *NetworkIntegration) cleanupExpiredBLSKeys() {
	if ni.blsKeyManager == nil {
		return
	}
	ni.blsKeyManager.cleanupExpiredBLSKeys()
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
			if ni.collectorManager == nil {
				return
			}
			collector := ni.collectorManager.GetSignatureCollector(checkpointHash)
			if collector == nil {
				// 签名收集器已不存在，停止监控
				return
			}

			// 检查收集器状态（这些方法使用收集器内部的锁，不会与外部锁冲突）
			if collector.IsExpired() {
				ni.logger.Debug("签名收集器已过期",
					"checkpointHash", checkpointHash.String(),
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount())
			} else if collector.IsComplete() {
				// 签名收集器已完成，静默处理
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

// handleBLSKeyBroadcast 处理BLS公钥广播消息
func (ni *NetworkIntegration) handleBLSKeyBroadcast(obj interface{}, from peer.ID) {
	ni.logger.Debug("收到BLS公钥广播消息", "from", from.String())

	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("收到无效的BLS公钥广播消息", "from", from.String())
		return
	}

	var blsKeyMsg BLSKeyBroadcastMessage
	if err := json.Unmarshal(dposMsg.Data, &blsKeyMsg); err != nil {
		ni.logger.Warn("解析BLS公钥广播消息失败", "error", err, "from", from.String())
		return
	}

	ni.logger.Info("处理BLS公钥广播消息",
		"address", blsKeyMsg.Address.String(),
		"nodeType", blsKeyMsg.NodeType,
		"blsKeyLength", len(blsKeyMsg.BLSPublicKey),
		"from", from.String())

	// 保存BLS公钥到缓存
	if err := ni.SaveBLSKey(blsKeyMsg.Address, blsKeyMsg.BLSPublicKey); err != nil {
		ni.logger.Error("保存BLS公钥失败", "error", err, "address", blsKeyMsg.Address.String())
		return
	}

	// 发送确认消息
	if err := ni.sendBLSKeyAck(blsKeyMsg.Address, "received", "BLS公钥已接收并保存"); err != nil {
		ni.logger.Warn("发送BLS公钥确认消息失败", "error", err, "address", blsKeyMsg.Address.String())
	}
}

// handleBLSKeyAck 处理BLS公钥确认消息
func (ni *NetworkIntegration) handleBLSKeyAck(obj interface{}, from peer.ID) {
	ni.logger.Debug("收到BLS公钥确认消息", "from", from.String())

	dposMsg, ok := obj.(*DPOSMessage)
	if !ok {
		ni.logger.Warn("收到无效的BLS公钥确认消息", "from", from.String())
		return
	}

	var ackMsg BLSKeyAckMessage
	if err := json.Unmarshal(dposMsg.Data, &ackMsg); err != nil {
		ni.logger.Warn("解析BLS公钥确认消息失败", "error", err, "from", from.String())
		return
	}

	ni.logger.Info("收到BLS公钥确认消息",
		"address", ackMsg.Address.String(),
		"status", ackMsg.Status,
		"message", ackMsg.Message,
		"from", from.String())
}

// saveBLSKey 保存BLS公钥到缓存和数据库（内部方法，委托给BLSKeyManager）
func (ni *NetworkIntegration) saveBLSKey(address types.Address, blsKeyBytes []byte) error {
	return ni.SaveBLSKey(address, blsKeyBytes)
}

// GetBLSKey 从缓存获取BLS公钥
func (ni *NetworkIntegration) GetBLSKey(address types.Address) ([]byte, bool) {
	if ni.blsKeyManager == nil {
		return nil, false
	}
	return ni.blsKeyManager.GetBLSKey(address)
}

// SaveBLSKey 保存BLS公钥到缓存和数据库
func (ni *NetworkIntegration) SaveBLSKey(address types.Address, blsKeyBytes []byte) error {
	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
		ni.blsKeyManager.SetDPoSInstance(ni.dposInstance)
	}
	return ni.blsKeyManager.SaveBLSKey(address, blsKeyBytes)
}

// LoadBLSKeyToCache 只加载BLS公钥到缓存，不写入数据库（用于从数据库加载场景）
func (ni *NetworkIntegration) LoadBLSKeyToCache(address types.Address, blsKeyBytes []byte) error {
	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
	}
	return ni.blsKeyManager.LoadBLSKeyToCache(address, blsKeyBytes)
}

// BroadcastBLSKey 广播BLS公钥
func (ni *NetworkIntegration) BroadcastBLSKey(address types.Address, blsKeyBytes []byte, nodeType string) error {
	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
	}
	return ni.blsKeyManager.BroadcastBLSKey(address, blsKeyBytes, nodeType)
}

// sendBLSKeyAck 发送BLS公钥确认消息
func (ni *NetworkIntegration) sendBLSKeyAck(address types.Address, status, message string) error {
	if ni.blsKeyAckTopic == nil {
		return fmt.Errorf("BLS公钥确认主题不可用")
	}

	// 创建确认消息
	ackMsg := &BLSKeyAckMessage{
		Address:   address,
		Status:    status,
		Message:   message,
		Timestamp: uint64(time.Now().Unix()),
	}

	// 序列化消息
	data, err := json.Marshal(ackMsg)
	if err != nil {
		return fmt.Errorf("序列化BLS公钥确认消息失败: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Data: data,
	}

	// 发布消息
	if err := ni.blsKeyAckTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("发布BLS公钥确认消息失败: %w", err)
	}

	ni.logger.Info("BLS公钥确认消息已发送",
		"address", address.String(),
		"status", status,
		"message", message)

	return nil
}

// persistBLSKeyToDatabase 将BLS公钥持久化到数据库
func (ni *NetworkIntegration) persistBLSKeyToDatabase(address types.Address, blsKeyBytes []byte) error {
	// 🆕 优先使用DPoS实例直接调用
	if ni.dposInstance != nil {
		if dpos, ok := ni.dposInstance.(*DPoS); ok {
			if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
				return fmt.Errorf("通过DPoS实例持久化BLS公钥失败: %w", err)
			}

			return nil
		}
	}

	// 🆕 通过全局注册表查找DPoS实例（优先使用固定key）
	if dpos, exists := GetDPoSInstance("vcity_dpos"); exists && dpos != nil {
		if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
			return fmt.Errorf("通过全局注册表（固定key）持久化BLS公钥失败: %w", err)
		}

		return nil
	}

	// 🆕 备用方案：通过地址查找DPoS实例
	if dpos, exists := GetDPoSInstance(address.String()); exists && dpos != nil {
		if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
			return fmt.Errorf("通过全局注册表（地址key）持久化BLS公钥失败: %w", err)
		}

		return nil
	}

	// 🆕 备用方案：使用BLSKeyManager的回调函数进行持久化
	if ni.blsKeyManager != nil && ni.blsKeyManager.persistCallback != nil {
		if err := ni.blsKeyManager.persistCallback(address, blsKeyBytes); err != nil {
			return fmt.Errorf("BLS公钥持久化回调失败: %w", err)
		}
		return nil
	}

	// 如果都没有设置，记录警告但不返回错误
	ni.logger.Warn("BLS公钥持久化DPoS实例和回调函数都未设置，跳过数据库持久化",
		"address", address.String(),
		"blsKeyLength", len(blsKeyBytes))

	return nil
}

// restoreBLSKeysFromDatabase 从数据库恢复BLS公钥到缓存
func (ni *NetworkIntegration) restoreBLSKeysFromDatabase() error {
	// 从DPoS实例获取StakeStore，预加载数据库中的BLS公钥到内存缓存
	var dpos *DPoS

	// 优先使用已注入的实例
	if ni.dposInstance != nil {
		if inst, ok := ni.dposInstance.(*DPoS); ok {
			dpos = inst
		}
	}

	// 退化到从全局注册表获取
	if dpos == nil {
		if inst, exists := GetDPoSInstance("vcity_dpos"); exists && inst != nil {
			dpos = inst
		}
	}

	if dpos == nil || dpos.state == nil || dpos.state.StakeStore == nil {
		ni.logger.Debug("无法获取StakeStore，跳过从数据库恢复BLS公钥",
			"hasDpos", dpos != nil,
			"hasState", dpos != nil && dpos.state != nil)
		return nil
	}

	validators, err := dpos.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		ni.logger.Warn("从数据库获取验证者失败，跳过恢复BLS公钥", "error", err)
		return nil
	}

	restored := 0
	for _, v := range validators {
		if v == nil || v.BlsKey == nil {
			continue
		}

		keyBytes := v.BlsKey.Marshal()
		if len(keyBytes) == 0 {
			continue
		}

		if err := ni.LoadBLSKeyToCache(v.Address, keyBytes); err == nil {
			restored++
		} else {
			ni.logger.Debug("加载BLS公钥到缓存失败",
				"address", v.Address.String(),
				"error", err)
		}
	}

	ni.logger.Debug("数据库BLS公钥恢复完成",
		"totalValidators", len(validators),
		"restored", restored)

	return nil
}

// 🆕 新增：批量恢复BLS公钥
func (ni *NetworkIntegration) RestoreBLSKeysForDelegates(delegates []*validator.ValidatorMetadata) error {
	if ni.blsKeyManager == nil {
		ni.logger.Debug("BLS公钥管理器未设置，跳过批量恢复")
		return nil
	}

	ni.logger.Debug("🔄 开始批量恢复BLS公钥", "delegatesCount", len(delegates))

	restoredCount := 0
	for _, delegate := range delegates {
		if delegate.BlsKey == nil {
			// 尝试从缓存获取
			blsKeyBytes, exists := ni.GetBLSKey(delegate.Address)
			if exists && len(blsKeyBytes) > 0 {
				blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
				if err == nil {
					delegate.BlsKey = blsKey
					restoredCount++
					ni.logger.Debug("✅ 从缓存恢复BLS公钥",
						"address", delegate.Address.String())
				}
			}
		}
	}

	ni.logger.Debug("🔄 批量恢复BLS公钥完成",
		"totalDelegates", len(delegates),
		"restoredCount", restoredCount)

	return nil
}

// handleBLSKeyRequest 处理BLS公钥请求消息
func (ni *NetworkIntegration) handleBLSKeyRequest(obj interface{}, from peer.ID) {
	if dposMsg, ok := obj.(*DPOSMessage); ok {
		var requestMsg BLSKeyRequestMessage
		if err := json.Unmarshal(dposMsg.Data, &requestMsg); err != nil {
			ni.logger.Error("反序列化BLS公钥请求消息失败", "error", err)
			return
		}

		// 🆕 检查是否是本地节点的请求，如果是则忽略
		// 简单判断：如果请求者地址等于请求的地址，可能是本地请求
		if requestMsg.Requester == requestMsg.RequestedAddress {
			ni.logger.Debug("🔄 忽略可能的本地BLS公钥请求",
				"requestedAddress", requestMsg.RequestedAddress.String(),
				"requester", requestMsg.Requester.String())
			return
		}

		// 检查是否是本地节点地址的请求
		isLocalRequest := ni.isLocalNode(requestMsg.RequestedAddress)

		// 获取本地节点地址用于日志
		var localNodeAddress string
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance.key != nil {
			localNodeAddress = types.Address(dposInstance.key.Address()).String()
		}

		// 只对本地节点地址的请求进行详细跟踪
		if isLocalRequest {
			// 静默处理，不打印日志
		} else {
			// 非本地节点请求，完全静默处理，不打印任何日志
			// 直接发送"未找到"响应
			responseMsg := &BLSKeyResponseMessage{
				RequestedAddress: requestMsg.RequestedAddress,
				Requester:        requestMsg.Requester,
				BLSPublicKey:     nil,
				Found:            false,
				Timestamp:        uint64(time.Now().Unix()),
			}

			// 发送响应
			if err := ni.sendBLSKeyResponse(responseMsg); err != nil {
				ni.logger.Error("❌ 发送BLS公钥响应失败", "error", err)
			}
			return
		}

		// 检查本地创世文件是否有该地址的BLS公钥
		found := false
		var blsPublicKey []byte

		// 首先尝试从缓存获取
		if cachedKey, exists := ni.GetBLSKey(requestMsg.RequestedAddress); exists {
			found = true
			blsPublicKey = cachedKey
			// 从缓存找到BLS公钥，静默处理
		} else {
			// 如果缓存中没有，检查是否是本地节点的地址
			// 只有本地节点才能提供自己的BLS公钥
			if isLocalRequest {

				// 先获取文件路径用于日志
				dataDir := ni.getDataDir()
				var keyFilePath string
				if dataDir != "" {
					if strings.Contains(dataDir, "consensus") {
						keyFilePath = filepath.Join(dataDir, "validator-bls.key")
					} else {
						parentDir := filepath.Dir(dataDir)
						keyFilePath = filepath.Join(parentDir, "consensus", "validator-bls.key")
					}
				}

				if keyBytes, err := ni.findBLSKeyFromGenesisFile(requestMsg.RequestedAddress); err == nil && len(keyBytes) > 0 {
					found = true
					blsPublicKey = keyBytes
					// 成功从文件加载本地BLS公钥，静默处理
				} else {
					ni.logger.Warn("❌ 从文件加载本地BLS公钥失败",
						"requestedAddress", requestMsg.RequestedAddress.String(),
						"filePath", keyFilePath,
						"error", err)
				}
			} else {
				// 不是本地节点，无法提供BLS公钥 - 只记录DEBUG级别
				ni.logger.Debug("❌ 无法提供非本地节点的BLS公钥",
					"requestedAddress", requestMsg.RequestedAddress.String(),
					"localNodeAddress", localNodeAddress,
					"note", "只有本地节点才能提供自己的BLS公钥")
			}
		}

		// 发送响应消息
		responseMsg := &BLSKeyResponseMessage{
			RequestedAddress: requestMsg.RequestedAddress,
			Requester:        requestMsg.Requester,
			BLSPublicKey:     blsPublicKey,
			Found:            found,
			Timestamp:        uint64(time.Now().Unix()),
		}

		if err := ni.sendBLSKeyResponse(responseMsg); err != nil {
			ni.logger.Error("发送BLS公钥响应失败", "error", err)
		} else {
			// BLS公钥响应已发送，静默处理
		}
	} else {
		ni.logger.Error("无效的BLS公钥请求消息类型")
	}
}

// handleBLSKeyResponse 处理BLS公钥响应消息
func (ni *NetworkIntegration) handleBLSKeyResponse(obj interface{}, from peer.ID) {
	if dposMsg, ok := obj.(*DPOSMessage); ok {
		var responseMsg BLSKeyResponseMessage
		if err := json.Unmarshal(dposMsg.Data, &responseMsg); err != nil {
			ni.logger.Error("反序列化BLS公钥响应消息失败", "error", err)
			return
		}

		// 🆕 根据响应更新验证者的peer映射
		if from != "" {
			ni.RegisterValidatorPeer(responseMsg.RequestedAddress, from)
		}

		if responseMsg.Found && len(responseMsg.BLSPublicKey) > 0 {
			// 保存BLS公钥到缓存和数据库
			if err := ni.saveBLSKey(responseMsg.RequestedAddress, responseMsg.BLSPublicKey); err != nil {
				ni.logger.Error("保存BLS公钥失败", "error", err)
			}

			// 将响应转发给DPoS实例的BLS请求处理器
			if err := ni.forwardBLSResponseToDPoS(&responseMsg); err != nil {
				//ni.logger.Error("转发BLS响应到DPoS失败", "error", err)
			}
		}
	} else {
		ni.logger.Error("无效的BLS公钥响应消息类型")
	}
}

// sendBLSKeyResponse 发送BLS公钥响应消息
func (ni *NetworkIntegration) sendBLSKeyResponse(responseMsg *BLSKeyResponseMessage) error {
	if ni.blsKeyResponseTopic == nil {
		return fmt.Errorf("BLS公钥响应主题不可用")
	}

	// 序列化消息
	data, err := json.Marshal(responseMsg)
	if err != nil {
		return fmt.Errorf("序列化BLS公钥响应消息失败: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Data: data,
	}

	// 发布消息
	if err := ni.blsKeyResponseTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("发布BLS公钥响应消息失败: %w", err)
	}

	return nil
}

// findBLSKeyFromGenesisFile 从validator-bls.key文件中查找BLS公钥
func (ni *NetworkIntegration) findBLSKeyFromGenesisFile(address types.Address) ([]byte, error) {
	// 🆕 修改：不再从创世文件查找，而是从validator-bls.key文件查找

	// 1. 获取数据目录路径
	dataDir := ni.getDataDir()
	if dataDir == "" {
		return nil, fmt.Errorf("data directory not available")
	}

	// 2. 构建BLS私钥文件路径
	// 需要确保路径是 nodeX\consensus\validator-bls.key
	var keyFilePath string

	// 检查dataDir的结构
	if strings.Contains(dataDir, "consensus") {
		// dataDir包含consensus，需要检查是否还有dpos子目录
		if strings.Contains(dataDir, "dpos") {
			// dataDir = "node4\consensus\dpos"，需要回到 "node4\consensus"
			parentDir := filepath.Dir(dataDir) // 获取 "node4\consensus"
			keyFilePath = filepath.Join(parentDir, "validator-bls.key")
		} else {
			// dataDir = "node4\consensus"，直接使用
			keyFilePath = filepath.Join(dataDir, "validator-bls.key")
		}
	} else {
		// dataDir不包含consensus目录，需要添加
		// dataDir = "node1\dpos"，需要回到 "node1\consensus"
		parentDir := filepath.Dir(dataDir) // 获取 "node1"
		keyFilePath = filepath.Join(parentDir, "consensus", "validator-bls.key")
	}

	// 3. 检查文件是否存在
	// 检查是否是本地节点地址，只对本地节点打印详细日志
	isLocalNode := ni.isLocalNode(address)

	if isLocalNode {
	}

	if _, err := os.Stat(keyFilePath); os.IsNotExist(err) {
		if isLocalNode {
			ni.logger.Warn("❌ BLS private key file not found",
				"address", address.String(),
				"filePath", keyFilePath)
		}
		return nil, fmt.Errorf("BLS private key file not found: %s", keyFilePath)
	}

	if isLocalNode {
		// 静默处理，不打印日志
	}

	// 4. 读取私钥文件
	privateKeyData, err := os.ReadFile(keyFilePath)
	if err != nil {
		ni.logger.Error("❌ Failed to read BLS private key file",
			"address", address.String(),
			"filePath", keyFilePath,
			"error", err)
		return nil, fmt.Errorf("failed to read BLS private key file: %w", err)
	}

	// 5. 获取十六进制字符串（去除可能的换行符）
	privateKeyHex := strings.TrimSpace(string(privateKeyData))

	// 6. 检查并修正私钥长度
	if len(privateKeyHex)%2 != 0 {
		privateKeyHex = "0" + privateKeyHex
	}

	// 7. 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
	if err != nil {
		ni.logger.Error("❌ Failed to unmarshal BLS private key",
			"address", address.String(),
			"error", err)
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}

	// 8. 从私钥生成公钥
	publicKey := privateKey.PublicKey()
	publicKeyBytes := publicKey.Marshal()

	// 9. 验证公钥长度（应该是128字节）
	if len(publicKeyBytes) != 128 {
		return nil, fmt.Errorf("invalid BLS public key length: expected 128 bytes, got %d", len(publicKeyBytes))
	}

	// 静默处理，不打印日志

	return publicKeyBytes, nil
}

// 🆕 新增：获取数据目录路径
func (ni *NetworkIntegration) getDataDir() string {
	// 尝试从DPoS实例获取数据目录
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		// 这里需要添加获取数据目录的逻辑
		// 可以通过配置或其他方式获取
		return dposInstance.getDataDir()
	}

	// 备用方案：从环境变量或默认路径获取
	if dataDir := os.Getenv("VCITY_DATA_DIR"); dataDir != "" {
		return dataDir
	}

	return "" // 返回空字符串表示未找到
}

// getAvailableMethods 获取结构体的所有可用方法（用于调试）
func getAvailableMethods(v reflect.Value) []string {
	var methods []string
	t := v.Type()

	// 获取所有方法
	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)
		methods = append(methods, method.Name)
	}

	return methods
}

// getAvailableFields 获取结构体的所有可用字段（用于调试）
func getAvailableFields(v reflect.Value) []string {
	var fields []string
	t := v.Type()

	// 获取所有字段
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fields = append(fields, field.Name)
	}

	return fields
}

// RequestBLSKey 请求BLS公钥
func (ni *NetworkIntegration) RequestBLSKey(requestedAddress types.Address, requester types.Address) error {
	// 🔧 修复：确保BLS相关主题已初始化
	if ni.blsKeyRequestTopic == nil && ni.topicManager != nil {
		ni.blsKeyRequestTopic = ni.topicManager.GetTopic("dpos-bls-key-request")
	}
	if ni.blsKeyResponseTopic == nil && ni.topicManager != nil {
		ni.blsKeyResponseTopic = ni.topicManager.GetTopic("dpos-bls-key-response")
	}
	if ni.blsKeyBroadcastTopic == nil && ni.topicManager != nil {
		ni.blsKeyBroadcastTopic = ni.topicManager.GetTopic("dpos-bls-key-broadcast")
	}
	if ni.blsKeyAckTopic == nil && ni.topicManager != nil {
		ni.blsKeyAckTopic = ni.topicManager.GetTopic("dpos-bls-key-ack")
	}

	if ni.blsKeyManager == nil {
		ni.blsKeyManager = NewBLSKeyManager(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
			ni.logger,
		)
		ni.blsKeyManager.SetDPoSInstance(ni.dposInstance)
	} else {
		// 🔧 修复：如果BLSKeyManager已存在但主题为nil，更新主题
		ni.blsKeyManager.SetTopics(
			ni.blsKeyBroadcastTopic,
			ni.blsKeyRequestTopic,
			ni.blsKeyResponseTopic,
			ni.blsKeyAckTopic,
		)
	}
	return ni.blsKeyManager.RequestBLSKey(requestedAddress, requester)
}

// RegisterValidatorPeerFromMultiAddr 使用MultiAddr注册验证者与peer的映射
func (ni *NetworkIntegration) RegisterValidatorPeerFromMultiAddr(address types.Address, multiAddr string) error {
	if ni.peerRegistry == nil {
		ni.peerRegistry = NewPeerRegistry()
	}
	return ni.peerRegistry.RegisterValidatorPeerFromMultiAddr(address, multiAddr)
}

// RegisterValidatorPeer 注册（或更新）验证者与peer的映射
func (ni *NetworkIntegration) RegisterValidatorPeer(address types.Address, peerID peer.ID) {
	if ni.peerRegistry == nil {
		ni.peerRegistry = NewPeerRegistry()
	}
	ni.peerRegistry.RegisterValidatorPeer(address, peerID)
}

// GetValidatorConnectivity 返回验证者的peer连接状态
func (ni *NetworkIntegration) GetValidatorConnectivity(address types.Address) (peer.ID, bool, bool) {
	if ni.peerRegistry == nil {
		ni.peerRegistry = NewPeerRegistry()
	}
	return ni.peerRegistry.GetValidatorConnectivity(address, ni.network)
}

// isLocalNode 检查给定的地址是否是本地节点的地址
func (ni *NetworkIntegration) isLocalNode(address types.Address) bool {
	// 获取DPoS实例来检查本地节点地址
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		if dposInstance.key != nil {
			return address == types.Address(dposInstance.key.Address())
		}
	}
	return false
}

// forwardBLSResponseToDPoS 将BLS响应转发给DPoS实例
func (ni *NetworkIntegration) forwardBLSResponseToDPoS(responseMsg *BLSKeyResponseMessage) error {
	// 获取DPoS实例
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		// 解析BLS公钥
		blsKey, err := bls.UnmarshalPublicKey(responseMsg.BLSPublicKey)
		if err != nil {
			return fmt.Errorf("failed to unmarshal BLS public key: %w", err)
		}

		// 构造请求ID（与发送请求时保持一致）
		requestID := fmt.Sprintf("bls_request_%s_%d",
			responseMsg.RequestedAddress.String(),
			responseMsg.Timestamp)

		// 将响应发送给等待的请求处理器
		if err := dposInstance.handleBLSResponse(requestID, blsKey); err != nil {
			return fmt.Errorf("failed to handle BLS response: %w", err)
		}

		ni.logger.Debug("✅ BLS响应已转发给DPoS",
			"requestedAddress", responseMsg.RequestedAddress.String(),
			"requestID", requestID)
	} else {
		return fmt.Errorf("DPoS instance not found")
	}

	return nil
}
