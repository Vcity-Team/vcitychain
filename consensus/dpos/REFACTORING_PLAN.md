# DPoS 模块重构方案

## 0. NetworkIntegration 模块重构（已完成）✅

### 重构状态：✅ **已完成**

### 重构概述

`NetworkIntegration`模块已经从单一的大文件（~2200行）重构为多个职责清晰的小模块，采用组合模式，提高了代码的可维护性和可测试性。

### 已完成的工作

#### 0.1 TopicManager（已完成）✅

**文件**: `consensus/dpos/topic_manager.go`  
**状态**: ✅ 已完成并集成

**职责**:
- 统一管理所有DPoS topics的创建
- 配置表驱动，消除重复代码
- 统一错误处理（"topic already exists"）

**主要方法**:
- `CreateAllTopics()` - 创建所有预定义的topics
- `GetTopic(name)` - 获取已创建的topic
- `CloseAll()` - 关闭所有topics

#### 0.2 PeerRegistry（已完成）✅

**文件**: `consensus/dpos/peer_registry.go`  
**状态**: ✅ 已完成并集成

**职责**:
- 管理验证者地址与peer ID的映射
- 提供连接状态查询

**主要方法**:
- `RegisterValidatorPeer()` - 注册验证者-peer映射
- `RegisterValidatorPeerFromMultiAddr()` - 从MultiAddr注册
- `GetValidatorConnectivity()` - 获取验证者连接状态
- `GetAddressByPeerID()` - 通过peer ID获取验证者地址

#### 0.3 SignatureCollectorManager（已完成）✅

**文件**: `consensus/dpos/signature_collector_manager.go`  
**状态**: ✅ 已完成并集成

**职责**:
- 管理签名收集器的生命周期
- 自动清理过期收集器
- 状态监控

**主要方法**:
- `RegisterSignatureCollector()` - 注册签名收集器
- `GetSignatureCollector()` - 获取签名收集器
- `UnregisterSignatureCollector()` - 注销签名收集器
- `cleanupExpiredCollectors()` - 清理过期收集器

#### 0.4 BLSKeyManager（已完成）✅

**文件**: `consensus/dpos/bls_key_manager.go`  
**状态**: ✅ 已完成并集成

**职责**:
- BLS公钥缓存管理
- 持久化到数据库
- 网络广播和请求

**主要方法**:
- `GetBLSKey()` - 从缓存获取BLS公钥
- `SaveBLSKey()` - 保存BLS公钥到缓存和数据库
- `LoadBLSKeyToCache()` - 加载BLS公钥到缓存
- `BroadcastBLSKey()` - 广播BLS公钥
- `RequestBLSKey()` - 请求BLS公钥
- `RestoreBLSKeysFromDatabase()` - 从数据库恢复BLS公钥

#### 0.5 NetworkIntegration集成（已完成）✅

**文件**: `consensus/dpos/network_integration.go`  
**状态**: ✅ 已完成重构

**重构效果**:
- 代码行数：从 2291 行 → 约 1500 行（分散到5个文件）
- 单个文件最大：从 2291 行 → 约 400 行
- 可维护性：显著提升
- 可测试性：每个模块可独立测试

**集成方式**:
- `NetworkIntegration`现在作为协调器，组合使用上述4个管理器
- 保持向后兼容，公共接口不变
- 所有原有功能保持不变

### 重构收益

1. **可测试性**: 每个组件可独立测试
2. **可维护性**: 职责清晰，易于修改
3. **可读性**: 代码结构清晰，易于理解
4. **可扩展性**: 易于添加新功能

---

## 1. detectValidatorFaults 模块重构方案

### 重构状态：✅ **已完成**

### 当前问题分析

**函数位置**: `consensus/dpos/validator_mgmt_fault.go:112-403`  
**函数长度**: ~290行  
**复杂度**: 高（多个职责混合）

**主要职责**:
1. Epoch计算和验证
2. 验证者集合获取（多个来源）
3. 漏块数计算
4. 故障判断
5. 消减信息收集
6. 数据库持久化
7. 日志记录

**问题**:
- 单一函数承担过多职责
- 难以单独测试各个功能
- 代码可读性差
- 维护困难

### 重构方案

#### 1.1 创建 `FaultDetector` 结构体

**文件**: `consensus/dpos/fault_detector.go`

```go
package dpos

import (
    "github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
    "github.com/Vcity-Team/vcitychain/types"
    "github.com/hashicorp/go-hclog"
)

// FaultDetector 负责检测验证者故障
type FaultDetector struct {
    dposInstance *DPoS
    logger       hclog.Logger
    
    // 依赖组件
    validatorProvider ValidatorProvider
    blockCounter      BlockCounter
    faultCalculator   FaultCalculator
    slashingCollector SlashingCollector
}

// NewFaultDetector 创建故障检测器
func NewFaultDetector(dposInstance *DPoS, logger hclog.Logger) *FaultDetector {
    return &FaultDetector{
        dposInstance:      dposInstance,
        logger:            logger.Named("fault-detector"),
        validatorProvider: NewValidatorProvider(dposInstance, logger),
        blockCounter:      NewBlockCounter(dposInstance, logger),
        faultCalculator:   NewFaultCalculator(dposInstance, logger),
        slashingCollector: NewSlashingCollector(dposInstance, logger),
    }
}

// DetectFaults 检测验证者故障（主入口）
func (fd *FaultDetector) DetectFaults(blockNumber uint64) ([]FaultFlagInfo, error) {
    // 1. 检查epoch变化
    epochInfo, shouldSkip := fd.checkEpochChange(blockNumber)
    if shouldSkip {
        return nil, nil
    }
    
    // 2. 获取验证者集合
    validators, err := fd.validatorProvider.GetValidatorsForDetection(epochInfo)
    if err != nil {
        return nil, err
    }
    
    // 3. 检测每个验证者的故障
    faultFlags := fd.detectValidatorFaults(epochInfo, validators)
    
    // 4. 收集消减信息
    fd.slashingCollector.CollectSlashingInfo(faultFlags, epochInfo)
    
    // 5. 更新epoch
    fd.dposInstance.currentEpoch = epochInfo.CurrentEpoch
    
    return faultFlags, nil
}
```

#### 1.2 创建 `ValidatorProvider` 结构体

**文件**: `consensus/dpos/validator_provider.go`

```go
package dpos

// ValidatorProvider 负责提供验证者集合
type ValidatorProvider struct {
    dposInstance *DPoS
    logger       hclog.Logger
}

// NewValidatorProvider 创建验证者提供器
func NewValidatorProvider(dposInstance *DPoS, logger hclog.Logger) *ValidatorProvider {
    return &ValidatorProvider{
        dposInstance: dposInstance,
        logger:       logger.Named("validator-provider"),
    }
}

// EpochInfo epoch信息
type EpochInfo struct {
    CurrentEpoch        uint64
    CurrentEpochNumber   uint64
    PreviousEpochNumber  uint64
    EpochToCheck        uint64
    EpochToCheckNumber  uint64
}

// GetValidatorsForDetection 获取用于故障检测的验证者集合
func (vp *ValidatorProvider) GetValidatorsForDetection(epochInfo EpochInfo) (validator.AccountSet, error) {
    // 优先从ExtraData/数据库获取
    // 备用方案：runtime.delegates
    // 最后：d.delegates
    // 返回验证者集合和来源信息
}

// GetPreviousEpochValidators 获取上一个epoch的验证者集合
func (vp *ValidatorProvider) GetPreviousEpochValidators(epochNumber uint64) validator.AccountSet {
    // 从数据库或ExtraData获取上一个epoch的验证者集合
}
```

#### 1.3 创建 `BlockCounter` 结构体

**文件**: `consensus/dpos/block_counter.go`

```go
package dpos

// BlockCounter 负责计算验证者的出块统计
type BlockCounter struct {
    dposInstance *DPoS
    logger       hclog.Logger
}

// NewBlockCounter 创建出块计数器
func NewBlockCounter(dposInstance *DPoS, logger hclog.Logger) *BlockCounter {
    return &BlockCounter{
        dposInstance: dposInstance,
        logger:       logger.Named("block-counter"),
    }
}

// BlockStats 出块统计信息
type BlockStats struct {
    ExpectedBlocks uint64
    ActualBlocks   uint64
    MissedBlocks   uint64
}

// CalculateBlockStats 计算验证者的出块统计
func (bc *BlockCounter) CalculateBlockStats(
    validatorAddr types.Address,
    startEpoch, endEpoch uint64,
) BlockStats {
    // 调用 dposInstance.calculateMissedBlocksWithActual
    // 返回统计信息
}
```

#### 1.4 创建 `FaultCalculator` 结构体

**文件**: `consensus/dpos/fault_calculator.go`

```go
package dpos

// FaultCalculator 负责计算故障状态
type FaultCalculator struct {
    dposInstance *DPoS
    logger       hclog.Logger
}

// NewFaultCalculator 创建故障计算器
func NewFaultCalculator(dposInstance *DPoS, logger hclog.Logger) *FaultCalculator {
    return &FaultCalculator{
        dposInstance: dposInstance,
        logger:       logger.Named("fault-calculator"),
    }
}

// CalculateFaultFlag 计算验证者的故障标志
func (fc *FaultCalculator) CalculateFaultFlag(
    validator *validator.ValidatorMetadata,
    stats BlockStats,
    epochInfo EpochInfo,
    isNewlyAdded bool,
) FaultFlagInfo {
    // 计算漏块率
    // 判断是否故障
    // 获取上次故障epoch
    // 构建故障原因
    // 返回FaultFlagInfo
}
```

#### 1.5 创建 `SlashingCollector` 结构体

**文件**: `consensus/dpos/slashing_collector.go`

```go
package dpos

// SlashingCollector 负责收集消减信息
type SlashingCollector struct {
    dposInstance *DPoS
    logger       hclog.Logger
}

// NewSlashingCollector 创建消减收集器
func NewSlashingCollector(dposInstance *DPoS, logger hclog.Logger) *SlashingCollector {
    return &SlashingCollector{
        dposInstance: dposInstance,
        logger:       logger.Named("slashing-collector"),
    }
}

// CollectSlashingInfo 收集消减信息
func (sc *SlashingCollector) CollectSlashingInfo(
    faultFlags []FaultFlagInfo,
    epochInfo EpochInfo,
) {
    // 遍历故障标志
    // 收集需要消减的验证者信息
    // 初始化pendingSlashingInfo
    // 添加消减操作
}
```

### 重构步骤

1. **第一步**: 创建 `FaultDetector` 结构体和主入口方法
2. **第二步**: 提取 `ValidatorProvider` - 验证者集合获取逻辑
3. **第三步**: 提取 `BlockCounter` - 出块统计计算逻辑
4. **第四步**: 提取 `FaultCalculator` - 故障计算逻辑
5. **第五步**: 提取 `SlashingCollector` - 消减信息收集逻辑
6. **第六步**: 更新 `detectValidatorFaults` 调用新组件
7. **第七步**: 添加单元测试
8. **第八步**: 清理旧代码

---

## 2. requestBLSPublicKeyFromNetwork 模块重构方案

### 重构状态：✅ **已完成**

### 当前问题分析

**函数位置**: `consensus/dpos/bls_network.go:22-113`  
**函数长度**: ~90行  
**复杂度**: 中等（职责混合）

**主要职责**:
1. 网络可用性检查
2. 请求消息创建
3. Peer连接状态检查
4. 响应处理器注册/注销
5. 广播请求发送
6. 响应等待和超时处理

**问题**:
- 同步等待响应，可能阻塞
- 响应处理器管理分散
- 错误处理不够清晰
- 难以测试

### 重构方案

#### 2.1 创建 `BLSKeyRequester` 结构体

**文件**: `consensus/dpos/bls_key_requester.go`

```go
package dpos

import (
    "context"
    "time"
    
    "github.com/Vcity-Team/vcitychain/bls"
    "github.com/Vcity-Team/vcitychain/types"
    "github.com/hashicorp/go-hclog"
)

// BLSKeyRequester 负责从网络请求BLS公钥
type BLSKeyRequester struct {
    dposInstance      *DPoS
    networkIntegration *NetworkIntegration  // 使用已完成的NetworkIntegration
    blsKeyManager     *BLSKeyManager       // 使用已完成的BLSKeyManager
    logger            hclog.Logger
    
    // 响应处理器管理
    responseManager   *BLSResponseManager
    
    // 配置
    requestTimeout    time.Duration
}

// NewBLSKeyRequester 创建BLS公钥请求器
func NewBLSKeyRequester(
    dposInstance *DPoS,
    networkIntegration *NetworkIntegration,  // 使用已完成的NetworkIntegration
    blsKeyManager *BLSKeyManager,            // 使用已完成的BLSKeyManager
    logger hclog.Logger,
) *BLSKeyRequester {
    return &BLSKeyRequester{
        dposInstance:      dposInstance,
        networkIntegration: networkIntegration,
        blsKeyManager:     blsKeyManager,     // 使用已完成的BLSKeyManager
        logger:            logger.Named("bls-key-requester"),
        responseManager:   NewBLSResponseManager(logger),
        requestTimeout:    30 * time.Second,
    }
}

// RequestBLSKey 请求BLS公钥（主入口）
func (bkr *BLSKeyRequester) RequestBLSKey(
    ctx context.Context,
    address types.Address,
) (*bls.PublicKey, error) {
    // 1. 验证网络可用性
    if err := bkr.validateNetwork(); err != nil {
        return nil, err
    }
    
    // 2. 检查peer连接状态
    connectivity := bkr.checkConnectivity(address)
    
    // 3. 创建请求
    request := bkr.createRequest(address)
    
    // 4. 发送请求并等待响应
    return bkr.sendRequestAndWait(ctx, request, connectivity)
}
```

#### 2.2 创建 `BLSResponseManager` 结构体

**文件**: `consensus/dpos/bls_response_manager.go`

```go
package dpos

import (
    "sync"
    "time"
    
    "github.com/Vcity-Team/vcitychain/bls"
    "github.com/hashicorp/go-hclog"
)

// BLSResponseManager 管理BLS响应处理器
type BLSResponseManager struct {
    handlers map[string]*ResponseHandler
    mutex    sync.RWMutex
    logger   hclog.Logger
}

// ResponseHandler 响应处理器
type ResponseHandler struct {
    ResponseCh chan *bls.PublicKey
    ErrorCh    chan error
    CreatedAt  time.Time
}

// NewBLSResponseManager 创建BLS响应管理器
func NewBLSResponseManager(logger hclog.Logger) *BLSResponseManager {
    return &BLSResponseManager{
        handlers: make(map[string]*ResponseHandler),
        logger:   logger.Named("bls-response-manager"),
    }
}

// RegisterHandler 注册响应处理器
func (brm *BLSResponseManager) RegisterHandler(requestID string) (*ResponseHandler, error) {
    brm.mutex.Lock()
    defer brm.mutex.Unlock()
    
    if _, exists := brm.handlers[requestID]; exists {
        return nil, fmt.Errorf("handler already exists for requestID: %s", requestID)
    }
    
    handler := &ResponseHandler{
        ResponseCh: make(chan *bls.PublicKey, 1),
        ErrorCh:    make(chan error, 1),
        CreatedAt:  time.Now(),
    }
    
    brm.handlers[requestID] = handler
    return handler, nil
}

// UnregisterHandler 注销响应处理器
func (brm *BLSResponseManager) UnregisterHandler(requestID string) {
    brm.mutex.Lock()
    defer brm.mutex.Unlock()
    
    if handler, exists := brm.handlers[requestID]; exists {
        close(handler.ResponseCh)
        close(handler.ErrorCh)
        delete(brm.handlers, requestID)
    }
}

// HandleResponse 处理响应
func (brm *BLSResponseManager) HandleResponse(requestID string, blsKey *bls.PublicKey) error {
    brm.mutex.RLock()
    handler, exists := brm.handlers[requestID]
    brm.mutex.RUnlock()
    
    if !exists {
        return fmt.Errorf("handler not found for requestID: %s", requestID)
    }
    
    select {
    case handler.ResponseCh <- blsKey:
        return nil
    default:
        return fmt.Errorf("response channel is full")
    }
}

// HandleError 处理错误
func (brm *BLSResponseManager) HandleError(requestID string, err error) error {
    brm.mutex.RLock()
    handler, exists := brm.handlers[requestID]
    brm.mutex.RUnlock()
    
    if !exists {
        return fmt.Errorf("handler not found for requestID: %s", requestID)
    }
    
    select {
    case handler.ErrorCh <- err:
        return nil
    default:
        return fmt.Errorf("error channel is full")
    }
}
```

#### 2.3 创建 `BLSRequestBuilder` 结构体

**文件**: `consensus/dpos/bls_request_builder.go`

```go
package dpos

import (
    "encoding/json"
    "fmt"
    "time"
    
    "github.com/Vcity-Team/vcitychain/types"
)

// BLSPublicKeyRequest BLS公钥请求消息
type BLSPublicKeyRequest struct {
    RequesterAddress types.Address `json:"requester_address"`
    TargetAddress    types.Address `json:"target_address"`
    Timestamp        uint64        `json:"timestamp"`
}

// BLSRequestBuilder 构建BLS请求
type BLSRequestBuilder struct {
    dposInstance *DPoS
}

// NewBLSRequestBuilder 创建请求构建器
func NewBLSRequestBuilder(dposInstance *DPoS) *BLSRequestBuilder {
    return &BLSRequestBuilder{
        dposInstance: dposInstance,
    }
}

// BuildRequest 构建请求消息
func (brb *BLSRequestBuilder) BuildRequest(targetAddress types.Address) (*BLSPublicKeyRequest, string) {
    request := &BLSPublicKeyRequest{
        RequesterAddress: types.Address(brb.dposInstance.key.Address()),
        TargetAddress:    targetAddress,
        Timestamp:        uint64(time.Now().Unix()),
    }
    
    requestID := fmt.Sprintf("bls_request_%s_%d", targetAddress.String(), request.Timestamp)
    return request, requestID
}

// MarshalRequest 序列化请求
func (brb *BLSRequestBuilder) MarshalRequest(request *BLSPublicKeyRequest) ([]byte, error) {
    return json.Marshal(request)
}
```

#### 2.4 创建 `BLSRequestSender` 结构体

**文件**: `consensus/dpos/bls_request_sender.go`

```go
package dpos

import (
    "context"
    "time"
    
    "github.com/Vcity-Team/vcitychain/bls"
    "github.com/Vcity-Team/vcitychain/types"
    "github.com/hashicorp/go-hclog"
)

// ConnectivityInfo 连接信息
type ConnectivityInfo struct {
    HasPeerMapping  bool
    IsPeerConnected bool
    PeerID          string
}

// BLSRequestSender 发送BLS请求
type BLSRequestSender struct {
    networkIntegration *NetworkIntegration  // 使用已完成的NetworkIntegration
    blsKeyManager     *BLSKeyManager       // 使用已完成的BLSKeyManager
    logger            hclog.Logger
}

// NewBLSRequestSender 创建请求发送器
func NewBLSRequestSender(
    networkIntegration *NetworkIntegration,  // 使用已完成的NetworkIntegration
    blsKeyManager *BLSKeyManager,          // 使用已完成的BLSKeyManager
    logger hclog.Logger,
) *BLSRequestSender {
    return &BLSRequestSender{
        networkIntegration: networkIntegration,
        blsKeyManager:     blsKeyManager,
        logger:            logger.Named("bls-request-sender"),
    }
}

// CheckConnectivity 检查连接状态
func (brs *BLSRequestSender) CheckConnectivity(address types.Address) ConnectivityInfo {
    if brs.networkIntegration == nil {
        return ConnectivityInfo{HasPeerMapping: false}
    }
    
    peerID, hasMapping, isConnected := brs.networkIntegration.GetValidatorConnectivity(address)
    return ConnectivityInfo{
        HasPeerMapping:  hasMapping,
        IsPeerConnected: isConnected,
        PeerID:          peerID.String(),
    }
}

// SendRequest 发送请求
func (brs *BLSRequestSender) SendRequest(
    request *BLSPublicKeyRequest,
) error {
    // 优先使用BLSKeyManager（已完成的组件）
    if brs.blsKeyManager != nil {
        return brs.blsKeyManager.RequestBLSKey(
            request.TargetAddress,
            request.RequesterAddress,
        )
    }
    
    // 备用方案：使用NetworkIntegration
    if brs.networkIntegration != nil {
        return brs.networkIntegration.RequestBLSKey(
            request.TargetAddress,
            request.RequesterAddress,
        )
    }
    
    return fmt.Errorf("both BLSKeyManager and NetworkIntegration are unavailable")
}

// WaitForResponse 等待响应
func (brs *BLSRequestSender) WaitForResponse(
    ctx context.Context,
    handler *ResponseHandler,
    timeout time.Duration,
) (*bls.PublicKey, error) {
    timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    
    select {
    case blsKey := <-handler.ResponseCh:
        return blsKey, nil
    case err := <-handler.ErrorCh:
        return nil, err
    case <-timeoutCtx.Done():
        return nil, fmt.Errorf("BLS request timeout")
    }
}
```

### 重构后的主函数

```go
// requestBLSPublicKeyFromNetwork 从网络请求BLS公钥（重构后）
func (d *DPoS) requestBLSPublicKeyFromNetwork(address types.Address) (*bls.PublicKey, error) {
    ctx := context.Background()
    
    // 获取BLSKeyManager（从已完成的NetworkIntegration中）
    var blsKeyManager *BLSKeyManager
    if d.runtime != nil && d.runtime.networkIntegration != nil {
        blsKeyManager = d.runtime.networkIntegration.blsKeyManager
    }
    
    requester := NewBLSKeyRequester(
        d,
        d.runtime.networkIntegration,  // 使用已完成的NetworkIntegration
        blsKeyManager,                  // 使用已完成的BLSKeyManager
        d.logger,
    )
    
    return requester.RequestBLSKey(ctx, address)
}
```

### 重构步骤

1. **第一步**: 创建 `BLSResponseManager` - 统一管理响应处理器
2. **第二步**: 创建 `BLSRequestBuilder` - 请求消息构建
3. **第三步**: 创建 `BLSRequestSender` - 请求发送和响应等待
4. **第四步**: 创建 `BLSKeyRequester` - 主协调器
5. **第五步**: 更新 `requestBLSPublicKeyFromNetwork` 调用新组件
6. **第六步**: 添加单元测试
7. **第七步**: 清理旧代码

---

## 重构收益

### detectValidatorFaults 重构收益

1. **可测试性**: 每个组件可独立测试
2. **可维护性**: 职责清晰，易于修改
3. **可读性**: 代码结构清晰，易于理解
4. **可扩展性**: 易于添加新的故障检测规则

### requestBLSPublicKeyFromNetwork 重构收益

1. **可测试性**: 各组件可独立测试
2. **可维护性**: 响应管理集中化
3. **可扩展性**: 易于支持异步请求、重试机制等
4. **错误处理**: 更清晰的错误处理流程

---

## 注意事项

1. **保持向后兼容**: 公共接口不变
2. **逐步迁移**: 分步骤实施，确保每步都能编译通过
3. **充分测试**: 每个组件都要有单元测试
4. **文档更新**: 更新相关文档和注释

---

## 重构进度总结

| 模块 | 状态 | 文件数 | 说明 |
|------|------|--------|------|
| **NetworkIntegration** | ✅ **已完成** | **5个文件** | **TopicManager, PeerRegistry, SignatureCollectorManager, BLSKeyManager已创建并集成到NetworkIntegration** |
| **detectValidatorFaults** | ✅ **已完成** | **5个文件** | **FaultDetector, ValidatorProvider, BlockCounter, FaultCalculator, SlashingCollector已创建并集成** |
| **requestBLSPublicKeyFromNetwork** | ✅ **已完成** | **4个文件** | **BLSKeyRequester, BLSResponseManager, BLSRequestBuilder, BLSRequestSender已创建并集成** |

### 已完成文件清单

**NetworkIntegration重构已完成文件**:
1. ✅ `consensus/dpos/topic_manager.go` - TopicManager
2. ✅ `consensus/dpos/peer_registry.go` - PeerRegistry
3. ✅ `consensus/dpos/signature_collector_manager.go` - SignatureCollectorManager
4. ✅ `consensus/dpos/bls_key_manager.go` - BLSKeyManager
5. ✅ `consensus/dpos/network_integration.go` - NetworkIntegration（已重构）

**detectValidatorFaults重构已完成文件**:
1. ✅ `consensus/dpos/validator_provider.go` - ValidatorProvider
2. ✅ `consensus/dpos/block_counter.go` - BlockCounter
3. ✅ `consensus/dpos/fault_calculator.go` - FaultCalculator
4. ✅ `consensus/dpos/slashing_collector.go` - SlashingCollector
5. ✅ `consensus/dpos/fault_detector.go` - FaultDetector
6. ✅ `consensus/dpos/validator_mgmt_fault.go` - detectValidatorFaults（已重构）

**requestBLSPublicKeyFromNetwork重构已完成文件**:
1. ✅ `consensus/dpos/bls_response_manager.go` - BLSResponseManager
2. ✅ `consensus/dpos/bls_request_builder.go` - BLSRequestBuilder
3. ✅ `consensus/dpos/bls_request_sender.go` - BLSRequestSender
4. ✅ `consensus/dpos/bls_key_requester.go` - BLSKeyRequester
5. ✅ `consensus/dpos/bls_network.go` - requestBLSPublicKeyFromNetwork（已重构）

### 重要说明

**对于`requestBLSPublicKeyFromNetwork`重构**:
- ✅ `BLSKeyManager`已经完成，应该直接使用，而不是重新实现
- ✅ `NetworkIntegration`已经完成，应该直接使用其方法
- ⏳ `BLSKeyRequester`应该组合使用`BLSKeyManager`和`NetworkIntegration`
- ⏳ `BLSResponseManager`需要新建，用于管理响应处理器
- ⏳ `BLSRequestBuilder`和`BLSRequestSender`是辅助组件，可以简化实现

