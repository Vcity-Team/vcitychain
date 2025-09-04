package dpos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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

	// 主题管理
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

	// 签名收集器
	signatureCollectors map[types.Hash]*SignatureCollector

	// 协程管理器
	goroutineManager *GoroutineManager

	// 锁
	lock sync.RWMutex

	// 添加DPoS运行时回调
	dposRuntime interface{}

	// BLS公钥管理
	blsKeyCache     map[types.Address][]byte    // 缓存BLS公钥
	blsKeyCacheTime map[types.Address]time.Time // 缓存时间
	blsKeyMutex     sync.RWMutex

	// 🆕 BLS公钥持久化回调函数
	blsKeyPersistCallback func(address types.Address, blsKeyBytes []byte) error

	// 🆕 BLS公钥查找回调函数
	blsKeyLookupCallback func(address types.Address) ([]byte, error)

	// 🆕 DPOS实例引用，用于持久化操作
	dposInstance interface{}
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
	sc.lastActivity = time.Now() // 更新最后活动时间

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
		logger:              logger.Named("network-integration"),
		network:             network,
		handlers:            make(map[string]MessageHandler),
		signatureCollectors: make(map[types.Hash]*SignatureCollector),
		goroutineManager:    NewGoroutineManager(logger, 1000, 100), // 最大1000个协程，100个重试工作器
		blsKeyCache:         make(map[types.Address][]byte),
		blsKeyCacheTime:     make(map[types.Address]time.Time),
	}

	// 注册消息处理器
	ni.registerHandlers()

	return ni
}

// SetBLSKeyPersistCallback 设置BLS公钥持久化回调函数
func (ni *NetworkIntegration) SetBLSKeyPersistCallback(callback func(address types.Address, blsKeyBytes []byte) error) {
	ni.blsKeyPersistCallback = callback
	ni.logger.Debug("BLS公钥持久化回调函数已设置")
}

// SetBLSKeyLookupCallback 设置BLS公钥查找回调函数
func (ni *NetworkIntegration) SetBLSKeyLookupCallback(callback func(address types.Address) ([]byte, error)) {
	ni.blsKeyLookupCallback = callback
	ni.logger.Debug("BLS公钥查找回调函数已设置")
}

// SetDPoSInstance 设置DPoS实例引用
func (ni *NetworkIntegration) SetDPoSInstance(dposInstance interface{}) {
	ni.dposInstance = dposInstance
	ni.logger.Info("DPoS实例引用已设置")
}

// NewNetworkIntegrationWithExistingTopics 创建使用现有主题的网络集成管理器
func NewNetworkIntegrationWithExistingTopics(network *network.Server, logger hclog.Logger, runtime interface{}) *NetworkIntegration {
	ni := &NetworkIntegration{
		logger:              logger.Named("network-integration"),
		network:             network,
		handlers:            make(map[string]MessageHandler),
		signatureCollectors: make(map[types.Hash]*SignatureCollector),
		goroutineManager:    NewGoroutineManager(logger, 1000, 100), // 最大1000个协程，100个重试工作器
		blsKeyCache:         make(map[types.Address][]byte),
		blsKeyCacheTime:     make(map[types.Address]time.Time),
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
	ni.logger.Debug("starting DPoS network integration")

	// 检查是否已经有主题（使用现有主题的情况）
	if ni.signatureRequestTopic == nil || ni.signatureResponseTopic == nil {
		ni.logger.Debug("没有现有主题，尝试创建新主题")
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

	// 🆕 从数据库恢复BLS公钥到缓存
	if err := ni.restoreBLSKeysFromDatabase(); err != nil {
		ni.logger.Warn("从数据库恢复BLS公钥失败，但网络集成仍可继续运行", "error", err)
	} else {
		ni.logger.Info("BLS公钥缓存恢复完成")
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
		ni.logger.Debug("成功创建签名请求主题")
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
		ni.logger.Debug("成功创建BLS公钥广播主题")
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
		ni.logger.Debug("成功创建BLS公钥确认主题")
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
		ni.logger.Debug("成功创建BLS公钥请求主题")
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
		ni.logger.Debug("成功创建BLS公钥响应主题")
	}

	// 检查是否至少有一个关键主题可用
	if ni.signatureRequestTopic == nil && ni.signatureResponseTopic == nil {
		ni.logger.Error("无法创建任何关键主题")
		return fmt.Errorf("failed to create any critical topics")
	}

	ni.logger.Debug("DPoS网络主题创建完成",
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
		ni.logger.Debug("成功订阅签名请求主题")
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
		ni.logger.Debug("成功订阅签名响应主题")
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
		ni.logger.Debug("成功订阅投票主题")
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
		ni.logger.Debug("成功订阅委托主题")
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
		ni.logger.Debug("成功订阅BLS公钥广播主题")
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
		ni.logger.Debug("成功订阅BLS公钥确认主题")
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
		ni.logger.Debug("成功订阅BLS公钥请求主题")
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
		ni.logger.Info("成功订阅BLS公钥响应主题")
	} else {
		ni.logger.Warn("BLS公钥响应主题不可用，跳过订阅")
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
			ni.logger.Debug("收到无效的签名请求：BlockNumber 为 0 但不是查询请求，忽略此请求",
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
		ni.logger.Debug("签名收集器已存在，关闭旧的收集器",
			"checkpointHash", checkpointHash.String(),
			"existingRequiredCount", existingCollector.requiredCount,
			"newRequiredCount", requiredCount)
		existingCollector.Close()
	}

	// 创建新的签名收集器
	collector := NewSignatureCollector(checkpointHash, signatureCh, timeout, requiredCount, ni.goroutineManager)
	collector.logger = ni.logger.Named("signature-collector")

	ni.signatureCollectors[checkpointHash] = collector

	ni.logger.Debug("注册签名收集器成功",
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
	ni.logger.Debug("DPoS运行时回调已设置")
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
	ni.lock.RLock()
	defer ni.lock.RUnlock()
	return ni.signatureCollectors[checkpointHash]
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
			ni.lock.RLock()
			collector, exists := ni.signatureCollectors[checkpointHash]
			ni.lock.RUnlock()

			if !exists {
				return
			}

			// 检查是否过期或完成（这些方法使用收集器内部的锁，不会与外部锁冲突）
			expired := collector.IsExpired()
			complete := collector.IsComplete()

			if expired || complete {
				ni.logger.Debug("清理签名收集器",
					"checkpointHash", checkpointHash.String(),
					"expired", expired,
					"complete", complete,
					"collectedCount", collector.GetCollectedCount(),
					"requiredCount", collector.GetRequiredCount())

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
	ni.lock.Lock()
	delete(ni.signatureCollectors, checkpointHash)
	ni.lock.Unlock()

	ni.logger.Debug("unregistered signature collector",
		"checkpointHash", checkpointHash.String())
}

// cleanupExpiredCollectors 清理所有过期的签名收集器
func (ni *NetworkIntegration) cleanupExpiredCollectors() {
	ni.lock.RLock()
	collectors := make(map[types.Hash]*SignatureCollector)
	for hash, collector := range ni.signatureCollectors {
		collectors[hash] = collector
	}
	ni.lock.RUnlock()

	expiredHashes := make([]types.Hash, 0)

	// 检查所有收集器
	for hash, collector := range collectors {
		if collector.IsExpired() || collector.IsComplete() {
			expiredHashes = append(expiredHashes, hash)
		}
	}

	// 清理过期的收集器
	for _, hash := range expiredHashes {
		ni.UnregisterSignatureCollector(hash)
	}

	if len(expiredHashes) > 0 {
		ni.logger.Debug("批量清理过期签名收集器",
			"清理数量", len(expiredHashes),
			"剩余数量", len(ni.signatureCollectors))
	}
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
				ni.cleanupExpiredBLSKeys()
			}
		}
	})
}

// cleanupExpiredBLSKeys 清理过期的BLS公钥缓存
func (ni *NetworkIntegration) cleanupExpiredBLSKeys() {
	ni.blsKeyMutex.Lock()
	defer ni.blsKeyMutex.Unlock()

	now := time.Now()
	expiredKeys := make([]types.Address, 0)

	// 收集过期的缓存项（超过1小时）
	for address, cacheTime := range ni.blsKeyCacheTime {
		if now.Sub(cacheTime) > time.Hour {
			expiredKeys = append(expiredKeys, address)
		}
	}

	// 清理过期的缓存项
	for _, address := range expiredKeys {
		delete(ni.blsKeyCache, address)
		delete(ni.blsKeyCacheTime, address)
	}

	if len(expiredKeys) > 0 {
		ni.logger.Debug("cleaned up expired BLS key cache", "count", len(expiredKeys))
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
				ni.logger.Debug("签名收集器已完成",
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
	if err := ni.saveBLSKey(blsKeyMsg.Address, blsKeyMsg.BLSPublicKey); err != nil {
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

// saveBLSKey 保存BLS公钥到缓存和数据库
func (ni *NetworkIntegration) saveBLSKey(address types.Address, blsKeyBytes []byte) error {
	ni.blsKeyMutex.Lock()
	defer ni.blsKeyMutex.Unlock()

	// 🆕 检查是否已经存在相同的BLS公钥，避免重复保存
	if existingKey, exists := ni.blsKeyCache[address]; exists {
		if bytes.Equal(existingKey, blsKeyBytes) {
			// BLS公钥已存在且相同，跳过保存
			ni.logger.Debug("BLS公钥已存在，跳过重复保存",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes))
			return nil
		}
	}

	// 保存到内存缓存
	ni.blsKeyCache[address] = blsKeyBytes
	ni.blsKeyCacheTime[address] = time.Now()

	// 🆕 尝试持久化到数据库
	if err := ni.persistBLSKeyToDatabase(address, blsKeyBytes); err != nil {
		ni.logger.Debug("BLS公钥持久化到数据库失败，但缓存已保存",
			"address", address.String(),
			"error", err)
		// 不返回错误，因为缓存已经保存成功
	} else {
		ni.logger.Debug("BLS公钥已持久化到数据库",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))
	}

	return nil
}

// GetBLSKey 从缓存获取BLS公钥
func (ni *NetworkIntegration) GetBLSKey(address types.Address) ([]byte, bool) {
	ni.blsKeyMutex.RLock()
	defer ni.blsKeyMutex.RUnlock()

	blsKey, exists := ni.blsKeyCache[address]
	return blsKey, exists
}

// BroadcastBLSKey 广播BLS公钥
func (ni *NetworkIntegration) BroadcastBLSKey(address types.Address, blsKeyBytes []byte, nodeType string) error {
	if ni.blsKeyBroadcastTopic == nil {
		return fmt.Errorf("BLS公钥广播主题不可用")
	}

	// 创建BLS公钥广播消息
	blsKeyMsg := &BLSKeyBroadcastMessage{
		Address:      address,
		BLSPublicKey: blsKeyBytes,
		Timestamp:    uint64(time.Now().Unix()),
		NodeType:     nodeType,
	}

	// 序列化消息
	data, err := json.Marshal(blsKeyMsg)
	if err != nil {
		return fmt.Errorf("序列化BLS公钥广播消息失败: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Data: data,
	}

	// 发布消息
	if err := ni.blsKeyBroadcastTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("发布BLS公钥广播消息失败: %w", err)
	}

	ni.logger.Info("BLS公钥广播消息已发送",
		"address", address.String(),
		"nodeType", nodeType,
		"blsKeyLength", len(blsKeyBytes))

	return nil
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

			ni.logger.Debug("BLS公钥已成功持久化到数据库（通过DPoS实例）",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes))
			return nil
		}
	}

	// 🆕 通过全局注册表查找DPoS实例（优先使用固定key）
	if dpos, exists := GetDPoSInstance("vcity_dpos"); exists && dpos != nil {
		if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
			return fmt.Errorf("通过全局注册表（固定key）持久化BLS公钥失败: %w", err)
		}

		ni.logger.Debug("BLS公钥已成功持久化到数据库（通过全局注册表-固定key）",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))
		return nil
	}

	// 🆕 备用方案：通过地址查找DPoS实例
	if dpos, exists := GetDPoSInstance(address.String()); exists && dpos != nil {
		if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
			return fmt.Errorf("通过全局注册表（地址key）持久化BLS公钥失败: %w", err)
		}

		ni.logger.Debug("BLS公钥已成功持久化到数据库（通过全局注册表-地址key）",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))
		return nil
	}

	// 🆕 备用方案：使用回调函数进行持久化
	if ni.blsKeyPersistCallback != nil {
		if err := ni.blsKeyPersistCallback(address, blsKeyBytes); err != nil {
			return fmt.Errorf("BLS公钥持久化回调失败: %w", err)
		}

		ni.logger.Debug("BLS公钥已成功持久化到数据库（通过回调函数）",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))
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
	// 🆕 简化版本：如果没有回调函数，跳过恢复
	if ni.blsKeyPersistCallback == nil {
		ni.logger.Debug("BLS公钥持久化回调函数未设置，跳过数据库恢复")
		return nil
	}

	// 这里可以添加从数据库恢复的逻辑
	// 目前先跳过，因为主要目的是解决持久化失败的问题
	ni.logger.Info("BLS公钥恢复功能待实现，当前跳过数据库恢复")

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

		ni.logger.Debug("📨 收到BLS公钥请求",
			"requestedAddress", requestMsg.RequestedAddress.String(),
			"requester", requestMsg.Requester.String(),
			"from", from.String(),
			"timestamp", requestMsg.Timestamp)

		// 检查本地创世文件是否有该地址的BLS公钥
		found := false
		var blsPublicKey []byte

		// 首先尝试从缓存获取
		if cachedKey, exists := ni.GetBLSKey(requestMsg.RequestedAddress); exists {
			found = true
			blsPublicKey = cachedKey
			ni.logger.Debug("✅ 从缓存找到BLS公钥", "address", requestMsg.RequestedAddress.String())
		} else {
			// 如果缓存中没有，直接从genesis.json文件中查找
			if keyBytes, err := ni.findBLSKeyFromGenesisFile(requestMsg.RequestedAddress); err == nil && len(keyBytes) > 0 {
				found = true
				blsPublicKey = keyBytes
				ni.logger.Info("✅ 从genesis.json找到BLS公钥", "address", requestMsg.RequestedAddress.String())
			} else {
				ni.logger.Debug("⚠️ 在genesis.json中未找到BLS公钥",
					"address", requestMsg.RequestedAddress.String(),
					"error", err)
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
			if found {
				ni.logger.Info("📤 已发送BLS公钥响应（找到）",
					"requestedAddress", requestMsg.RequestedAddress.String(),
					"requester", requestMsg.Requester.String())
			} else {
				ni.logger.Info("📤 已发送BLS公钥响应（未找到）",
					"requestedAddress", requestMsg.RequestedAddress.String(),
					"requester", requestMsg.Requester.String())
			}
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

		if responseMsg.Found && len(responseMsg.BLSPublicKey) > 0 {
			ni.logger.Info("📥 收到BLS公钥响应（找到）",
				"address", responseMsg.RequestedAddress.String(),
				"blsKeyLength", len(responseMsg.BLSPublicKey),
				"from", from.String())

			// 保存BLS公钥到缓存和数据库
			if err := ni.saveBLSKey(responseMsg.RequestedAddress, responseMsg.BLSPublicKey); err != nil {
				ni.logger.Error("保存BLS公钥失败", "error", err)
			} else {
				ni.logger.Info("✅ 成功保存从网络获取的BLS公钥",
					"address", responseMsg.RequestedAddress.String(),
					"blsKeyLength", len(responseMsg.BLSPublicKey))
			}
		} else {
			ni.logger.Info("📥 收到BLS公钥响应（未找到）",
				"found", responseMsg.Found,
				"blsKeyLength", len(responseMsg.BLSPublicKey),
				"address", responseMsg.RequestedAddress.String(),
				"from", from.String(),
				"timestamp", time.Now().Unix(),
				"reason", fmt.Sprintf("found=%v, blsKeyLength=%d", responseMsg.Found, len(responseMsg.BLSPublicKey)))
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

// findBLSKeyFromGenesisFile 从genesis.json文件中查找BLS公钥
func (ni *NetworkIntegration) findBLSKeyFromGenesisFile(address types.Address) ([]byte, error) {
	// 由于无法通过反射访问未导出字段，我们使用回调函数来获取BLS公钥
	if ni.blsKeyLookupCallback != nil {
		if blsKeyBytes, err := ni.blsKeyLookupCallback(address); err == nil && len(blsKeyBytes) > 0 {
			ni.logger.Debug("✅ 通过回调找到BLS公钥", "address", address.String())
			return blsKeyBytes, nil
		}
	}

	// 🆕 尝试从全局注册表获取BLS公钥
	// 首先尝试常见的实例名称，然后尝试使用第一个可用的实例
	var dposInstance interface{}
	var exists bool

	// 尝试常见的实例名称
	for _, instanceName := range []string{"vcity_dpos", "dpos", "polygon_dpos"} {
		if instance, found := GetDPoSInstance(instanceName); found {
			dposInstance = instance
			exists = true

			break
		}
	}

	// 如果没找到，使用第一个可用的实例
	if !exists {
		allInstances := GetAllDPoSInstances()
		if len(allInstances) > 0 {
			for _, instance := range allInstances {
				if instance != nil {
					dposInstance = instance
					exists = true

					break
				}
			}
		}
	}

	// 🆕 直接打印创世文件中的initialDelegates内容
	if exists && dposInstance != nil {

		// 使用反射获取config字段
		if reflectValue := reflect.ValueOf(dposInstance); reflectValue.IsValid() {
			// 获取config字段
			if configField := reflectValue.Elem().FieldByName("config"); configField.IsValid() {

				// 🆕 如果configField是指针，需要先解引用
				var configValue reflect.Value
				if configField.Kind() == reflect.Ptr {
					if configField.IsNil() {
						ni.logger.Warn("⚠️ config字段为nil指针",
							"address", address.String())
						return nil, fmt.Errorf("config field is nil pointer")
					}
					configValue = configField.Elem()

				} else {
					configValue = configField
				}

				// 获取InitialDelegates字段
				if initialDelegatesField := configValue.FieldByName("InitialDelegates"); initialDelegatesField.IsValid() {

					// 打印InitialDelegates的数量
					if initialDelegatesField.Kind() == reflect.Slice {

						// 遍历并打印每个delegate的详细信息
						for i := 0; i < initialDelegatesField.Len(); i++ {
							delegate := initialDelegatesField.Index(i)
							if delegate.IsValid() {

								// 🆕 如果delegate是指针，需要先解引用
								var delegateValue reflect.Value
								if delegate.Kind() == reflect.Ptr {
									if delegate.IsNil() {
										ni.logger.Warn("⚠️ delegate元素为nil指针",
											"index", i,
											"address", address.String())
										continue
									}
									delegateValue = delegate.Elem()

								} else {
									delegateValue = delegate
								}

								// 获取Address字段
								if addressField := delegateValue.FieldByName("Address"); addressField.IsValid() {

									// 🆕 检查字段是否可以被访问
									if addressField.CanInterface() {
										_ = addressField.Interface()

										// 获取BlsKey字段
										if blsKeyField := delegateValue.FieldByName("BlsKey"); blsKeyField.IsValid() {

											if blsKeyField.CanInterface() {
												_ = blsKeyField.Interface()

											} else {
												ni.logger.Warn("⚠️ BlsKey字段无法访问（可能是未导出的）",
													"index", i,
													"address", address.String())
											}
										} else {
											ni.logger.Warn("⚠️ 未找到BlsKey字段",
												"index", i,
												"address", address.String(),
												"availableFields", getAvailableFields(delegateValue))
										}
									} else {

										// 🆕 尝试使用String()方法（如果存在）
										if stringMethod := delegateValue.MethodByName("String"); stringMethod.IsValid() {

											if results := stringMethod.Call(nil); len(results) > 0 {
												if result := results[0]; result.CanInterface() {
													_ = result.Interface().(string)

												}
											}
										}
									}
								} else {
									ni.logger.Warn("⚠️ 未找到Address字段",
										"index", i,
										"address", address.String(),
										"availableFields", getAvailableFields(delegateValue))
								}
							}
						}
					}
				} else {
					ni.logger.Warn("⚠️ 未找到InitialDelegates字段",
						"address", address.String(),
						"availableFields", getAvailableFields(configField))
				}
			} else {
				ni.logger.Warn("⚠️ 未找到config字段",
					"address", address.String(),
					"availableFields", getAvailableFields(reflectValue.Elem()))
			}
		}

		// 使用反射调用GetBLSKeyBytesFromGenesis方法
		if reflectValue := reflect.ValueOf(dposInstance); reflectValue.IsValid() {

			// 🆕 尝试多个可能的方法名
			var method reflect.Value
			var methodName string

			// 按优先级尝试不同的方法名
			for _, name := range []string{"GetBLSKeyBytesFromGenesis", "GetBLSKey"} {
				if foundMethod := reflectValue.MethodByName(name); foundMethod.IsValid() {
					method = foundMethod
					methodName = name

					break
				}
			}

			if method.IsValid() {

				// 根据方法名和签名调用相应的方法
				var results []reflect.Value
				var callErr error

				switch methodName {
				case "GetBLSKeyBytesFromGenesis", "GetBLSKey":
					// 这些方法只需要address参数
					results = method.Call([]reflect.Value{reflect.ValueOf(address)})
				default:
					callErr = fmt.Errorf("unsupported method: %s", methodName)
				}

				if callErr != nil {
					ni.logger.Error("❌ 方法调用失败",
						"address", address.String(),
						"methodName", methodName,
						"error", callErr.Error())
					// 跳过这个方法，尝试下一个
					return nil, callErr
				}

				ni.logger.Debug("🔍 方法调用完成",
					"address", address.String(),
					"methodName", methodName,
					"resultsCount", len(results))

				if len(results) >= 2 {
					// 检查错误
					if results[1].IsNil() { // err == nil
						ni.logger.Debug("🔍 方法调用无错误",
							"address", address.String())

						if blsKeyBytes, ok := results[0].Interface().([]byte); ok {
							ni.logger.Debug("🔍 成功获取BLS公钥字节",
								"address", address.String(),
								"blsKeyLength", len(blsKeyBytes))

							if len(blsKeyBytes) > 0 {
								ni.logger.Debug("✅ 从全局注册表找到BLS公钥", "address", address.String())
								return blsKeyBytes, nil
							} else {
								ni.logger.Debug("⚠️ BLS公钥字节长度为0",
									"address", address.String())
							}
						} else {
							ni.logger.Debug("⚠️ 无法将结果转换为字节数组",
								"address", address.String(),
								"resultType", fmt.Sprintf("%T", results[0].Interface()))
						}
					} else {
						// 有错误
						if err, ok := results[1].Interface().(error); ok {
							ni.logger.Debug("⚠️ 方法调用返回错误",
								"address", address.String(),
								"error", err.Error())
						} else {
							ni.logger.Debug("⚠️ 方法调用返回非错误类型",
								"address", address.String(),
								"errorType", fmt.Sprintf("%T", results[1].Interface()))
						}
					}
				} else {
					ni.logger.Debug("⚠️ 方法返回结果数量不足",
						"address", address.String(),
						"expectedCount", 2,
						"actualCount", len(results))
				}
			} else {
				ni.logger.Debug("⚠️ 未找到可用的BLS公钥获取方法",
					"address", address.String(),
					"availableMethods", getAvailableMethods(reflectValue))
			}
		} else {
			ni.logger.Debug("⚠️ 反射值无效",
				"address", address.String())
		}
	}

	return nil, fmt.Errorf("BLS key not found for address %s", address.String())
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
	if ni.blsKeyRequestTopic == nil {
		return fmt.Errorf("BLS公钥请求主题不可用")
	}

	// 创建请求消息
	requestMsg := &BLSKeyRequestMessage{
		RequestedAddress: requestedAddress,
		Requester:        requester,
		Timestamp:        uint64(time.Now().Unix()),
	}

	// 序列化消息
	data, err := json.Marshal(requestMsg)
	if err != nil {
		return fmt.Errorf("序列化BLS公钥请求消息失败: %w", err)
	}

	// 创建DPoS消息
	dposMsg := &DPOSMessage{
		Data: data,
	}

	// 发布消息
	if err := ni.blsKeyRequestTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("发布BLS公钥请求消息失败: %w", err)
	}

	ni.logger.Info("📨 已广播BLS公钥请求",
		"requestedAddress", requestedAddress.String(),
		"requester", requester.String())

	return nil
}
