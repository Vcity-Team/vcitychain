# NetworkIntegration 重构方案

## 目标
- 拆分职责，提高可维护性
- 消除重复代码
- 保持向后兼容（平滑升级）
- 不影响IBFT运行

## 重构步骤

### 阶段1：提取 Topic 管理器（优先级最高）

#### 1.1 创建 `topic_manager.go`
```go
// TopicConfig 定义topic配置
type TopicConfig struct {
    Name         string
    MessageType  proto.Message
    IsCritical   bool  // 关键topic（创建失败会阻塞）
    TemplateData []byte
}

// TopicManager 管理所有DPoS topics
type TopicManager struct {
    network *network.Server
    logger  hclog.Logger
    topics  map[string]*network.Topic
    mutex   sync.RWMutex
}

// NewTopicManager 创建topic管理器
func NewTopicManager(network *network.Server, logger hclog.Logger) *TopicManager {
    return &TopicManager{
        network: network,
        logger:  logger,
        topics:  make(map[string]*network.Topic),
    }
}

// CreateTopic 创建单个topic（统一错误处理）
func (tm *TopicManager) CreateTopic(config TopicConfig) (*network.Topic, error) {
    // 统一处理逻辑：
    // 1. 尝试创建
    // 2. 如果"topic already exists"，记录警告但继续
    // 3. 返回topic或nil
}

// CreateAllTopics 批量创建所有topics
func (tm *TopicManager) CreateAllTopics() error {
    // 使用配置表驱动，消除重复代码
    configs := []TopicConfig{
        {"dpos-signature-request", &dposProto.TransportMessage{}, true, ...},
        {"dpos-signature-response", &dposProto.TransportMessage{}, true, ...},
        // ... 其他topics
    }
    // 统一创建逻辑
}

// GetTopic 获取已创建的topic
func (tm *TopicManager) GetTopic(name string) *network.Topic {
    tm.mutex.RLock()
    defer tm.mutex.RUnlock()
    return tm.topics[name]
}

// CloseAll 关闭所有topics
func (tm *TopicManager) CloseAll() {
    // 统一关闭逻辑
}
```

**收益**：
- `createTopics` 从170行减少到~30行
- 新增topic只需添加配置，无需复制代码
- 统一错误处理逻辑

---

### 阶段2：拆分 BLS 管理模块

#### 2.1 创建 `bls_key_manager.go`
```go
// BLSKeyManager 管理BLS公钥的缓存、持久化、网络请求
type BLSKeyManager struct {
    cache     map[types.Address][]byte
    cacheTime map[types.Address]time.Time
    mutex     sync.RWMutex
    
    // 持久化回调
    persistCallback func(address types.Address, blsKeyBytes []byte) error
    lookupCallback  func(address types.Address) ([]byte, error)
    
    // 网络相关
    broadcastTopic *network.Topic
    requestTopic   *network.Topic
    responseTopic  *network.Topic
    
    logger hclog.Logger
}

// 职责：
// - GetBLSKey / SaveBLSKey
// - BroadcastBLSKey / RequestBLSKey
// - restoreBLSKeysFromDatabase
// - handleBLSKeyBroadcast / handleBLSKeyRequest / handleBLSKeyResponse
```

**收益**：
- 从NetworkIntegration中移除~600行BLS相关代码
- BLS逻辑独立，易于测试

---

### 阶段3：拆分签名收集器模块

#### 3.1 创建 `signature_collector_manager.go`
```go
// SignatureCollectorManager 管理签名收集器
type SignatureCollectorManager struct {
    collectors map[types.Hash]*SignatureCollector
    mutex      sync.RWMutex
    logger     hclog.Logger
    
    // 协程管理
    goroutineManager *GoroutineManager
}

// 职责：
// - RegisterSignatureCollector
// - GetSignatureCollector
// - UnregisterSignatureCollector
// - cleanupExpiredCollectors
// - startCollectorCleanupWorker
```

**收益**：
- 签名收集逻辑独立
- 便于单独测试和优化

---

### 阶段4：拆分 Peer 映射模块

#### 4.1 创建 `peer_registry.go`
```go
// PeerRegistry 管理验证者地址与peer ID的映射
type PeerRegistry struct {
    validatorPeerMap map[types.Address]peer.ID
    peerValidatorMap map[peer.ID]types.Address
    mutex            sync.RWMutex
}

// 职责：
// - RegisterValidatorPeer
// - GetValidatorConnectivity
// - isLocalNode (如果需要)
```

**收益**：
- 简单的数据结构，独立管理
- 便于扩展（如添加连接状态监控）

---

### 阶段5：重构 NetworkIntegration（主协调器）

#### 5.1 简化后的 NetworkIntegration
```go
type NetworkIntegration struct {
    logger hclog.Logger
    network *network.Server
    
    // 子模块（组合而非继承）
    topicManager      *TopicManager
    blsKeyManager     *BLSKeyManager
    collectorManager  *SignatureCollectorManager
    peerRegistry      *PeerRegistry
    
    // 运行时回调
    dposRuntime interface{}
    dposInstance interface{}
    
    // 消息处理器（简化）
    handlers map[string]MessageHandler
}
```

#### 5.2 重构后的方法
```go
func (ni *NetworkIntegration) Start() error {
    // 1. 创建topic管理器并初始化所有topics
    ni.topicManager = NewTopicManager(ni.network, ni.logger)
    if err := ni.topicManager.CreateAllTopics(); err != nil {
        return err
    }
    
    // 2. 初始化BLS管理器
    ni.blsKeyManager = NewBLSKeyManager(
        ni.topicManager.GetTopic("dpos-bls-key-broadcast"),
        ni.topicManager.GetTopic("dpos-bls-key-request"),
        ni.topicManager.GetTopic("dpos-bls-key-response"),
        ni.logger,
    )
    
    // 3. 初始化签名收集器管理器
    ni.collectorManager = NewSignatureCollectorManager(ni.logger)
    
    // 4. 初始化peer注册表
    ni.peerRegistry = NewPeerRegistry()
    
    // 5. 订阅topics
    if err := ni.subscribeToTopics(); err != nil {
        return err
    }
    
    // 6. 注册消息处理器
    ni.registerHandlers()
    
    return nil
}
```

---

## 文件结构

重构后的文件结构：
```
consensus/dpos/
├── network_integration.go          (~300行，主协调器)
├── topic_manager.go                 (~200行，topic管理)
├── bls_key_manager.go               (~400行，BLS管理)
├── signature_collector_manager.go   (~200行，签名收集)
├── peer_registry.go                 (~100行，peer映射)
└── network_integration.go (原文件保留作为参考)
```

---

## 实施顺序

1. **第一步**：创建 `topic_manager.go`，提取 `createTopics` 逻辑
   - 风险低，影响范围小
   - 立即消除重复代码

2. **第二步**：创建 `peer_registry.go`
   - 最简单，无依赖
   - 快速验证重构模式

3. **第三步**：创建 `signature_collector_manager.go`
   - 中等复杂度
   - 验证模块间协作

4. **第四步**：创建 `bls_key_manager.go`
   - 最复杂，依赖最多
   - 最后实施

5. **第五步**：重构 `NetworkIntegration`，整合所有子模块
   - 保持向后兼容的接口
   - 逐步迁移调用方

---

## 向后兼容性

- 保持所有公共方法签名不变
- 内部实现改为委托给子模块
- 例如：`ni.GetBLSKey()` → `ni.blsKeyManager.GetBLSKey()`

---

## 测试策略

- 每个子模块独立单元测试
- NetworkIntegration 集成测试
- 保持现有e2e测试通过

---

## 预期收益

- 代码行数：2291行 → ~1200行（分散到5个文件）
- 单个文件最大：2291行 → ~400行
- 可维护性：显著提升
- 可测试性：每个模块可独立测试
- 扩展性：新增topic/功能更容易

