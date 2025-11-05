# max_account_enqueued 参数说明

## 参数定义

**参数名**：`max_account_enqueued`

**类型**：`uint64`（无符号整数）

**默认值**：`128`

**配置位置**：YAML配置文件中的 `tx_pool` 部分

**示例**：
```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 16384
  max_account_enqueued: 1024  # 每个账户最多1024个enqueued交易
```

---

## 参数含义

### 核心概念

`max_account_enqueued` 定义了**每个账户在交易池中最多可以有多少个处于 `enqueued`（入队）状态的交易**。

### 交易池的两个队列

交易池为每个账户维护两个队列：

1. **Enqueued队列（入队队列）**
   - 存储等待提升的交易
   - 条件：交易的 `nonce > 账户当前nonce`
   - 限制：每个账户最多 `max_account_enqueued` 个交易（默认128）

2. **Promoted队列（已提升队列）**
   - 存储可以被打包的交易
   - 条件：交易的 `nonce <= 账户当前nonce`
   - 只有promoted队列中的交易才能被打包到区块中

### 交易流程

```
新交易到达
  ↓
检查交易的nonce
  ↓
nonce > accountNonce → 进入Enqueued队列（等待）
nonce == accountNonce → 立即提升到Promoted队列
nonce < accountNonce → 拒绝（ErrNonceTooLow）
  ↓
区块打包后 → 更新accountNonce → Enqueued队列中的交易可以提升
```

---

## 为什么需要这个限制？

### 1. 防止内存溢出

如果没有限制，单个账户可以发送无限数量的交易，导致：
- 内存占用过大
- 节点性能下降
- 可能的内存溢出

### 2. 防止DoS攻击

恶意用户可能发送大量交易来：
- 消耗节点资源
- 阻塞交易池
- 影响正常交易处理

### 3. 保证交易顺序

Enqueued队列中的交易必须按nonce顺序提升：
- 如果nonce不连续，后面的交易无法提升
- 限制队列大小可以防止无限等待

---

## 错误触发条件

### 错误信息

```
maximum number of enqueued transactions reached
```

### 触发条件

当以下**两个条件同时满足**时，会返回此错误：

1. **Enqueued队列已满**：`account.enqueued.length() == max_account_enqueued`
2. **新交易的nonce不等于账户当前nonce**：`tx.Nonce != accountNonce`

### 代码位置

**文件**：`txpool/txpool.go:991-992`

```go
if account.enqueued.length() == account.maxEnqueued && tx.Nonce != accountNonce {
    return ErrMaxEnqueuedLimitReached
}
```

---

## 实际应用场景

### 场景1：正常使用

**配置**：`max_account_enqueued: 128`（默认值）

**场景**：
- 账户当前nonce = 0
- 用户发送nonce=0, 1, 2, ..., 127的交易
- nonce=0立即提升到promoted
- nonce=1-127进入enqueued队列（127个）
- **结果**：正常工作

### 场景2：队列满

**配置**：`max_account_enqueued: 128`

**场景**：
- 账户当前nonce = 0
- 用户发送nonce=0, 1, 2, ..., 128的交易
- nonce=0立即提升到promoted
- nonce=1-128进入enqueued队列（128个，已满）
- 用户尝试发送nonce=129的交易
- **结果**：返回错误 `maximum number of enqueued transactions reached`

### 场景3：压测场景

**配置**：`max_account_enqueued: 128`（默认值太小）

**场景**：
- 压测快速发送大量交易
- 出块速度跟不上交易发送速度
- enqueued队列很快填满
- **结果**：大量交易被拒绝

**解决方案**：
- 增加 `max_account_enqueued` 到 1024 或更高
- 降低压测速度
- 优化出块速度

### 场景4：Nonce不连续

**配置**：`max_account_enqueued: 128`

**场景**：
- 账户当前nonce = 0
- 用户发送nonce=0, 1, 2, 5, 6, 7...（跳过了3和4）
- nonce=0, 1, 2可以提升到promoted
- nonce=5, 6, 7...进入enqueued队列，但无法提升（因为nonce=3, 4缺失）
- 队列很快填满
- **结果**：返回错误

---

## 如何配置？

### 推荐值

根据网络特点选择：

| 网络类型 | 推荐值 | 说明 |
|---------|--------|------|
| 测试网络 | 128-512 | 低负载，默认值即可 |
| 开发网络 | 512-1024 | 中等负载 |
| **生产网络（TRON对标）** | **1024-2048** | 高负载，支持大量交易 |
| 压测场景 | 2048-4096 | 极端负载，需要足够缓冲 |

### TRON对标配置

**TRON网络特点**：
- 区块时间：3秒
- 高TPS（每秒数千笔交易）
- 支持大量并发交易

**推荐配置**：
```yaml
tx_pool:
  max_slots: 16384           # 总容量（slots）
  max_account_enqueued: 1024 # 每个账户最大enqueued交易数
```

---

## 与其他参数的关系

### max_slots（总容量）

- `max_slots`：整个交易池的总容量（以slots为单位）
- `max_account_enqueued`：每个账户的enqueued队列限制
- **关系**：`max_account_enqueued` 是账户级别的限制，`max_slots` 是全局限制

**示例**：
- `max_slots: 16384`（总容量）
- `max_account_enqueued: 1024`（每个账户最多1024个enqueued交易）
- 如果每个账户平均占用100 slots，理论上可以支持约160个账户同时有enqueued交易

---

## 验证配置

### 方法1：使用RPC查询

**PowerShell命令**：
```powershell
$response = Invoke-RestMethod -Uri "http://127.0.0.1:8545" -Method Post -ContentType "application/json" -Body '{"jsonrpc":"2.0","method":"txpool_inspect","params":[],"id":1}'
Write-Host "每个账户最大enqueued交易数: $($response.result.maxAccountEnqueued)"
```

**返回示例**：
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "pending": {},
    "queued": {},
    "currentCapacity": 0,
    "maxCapacity": 16384,
    "maxAccountEnqueued": 1024
  }
}
```

### 方法2：查看启动日志

启动日志中应该显示：
```
🔧 初始化交易池配置 MaxSlots=16384 MaxAccountEnqueued=1024 PriceLimit=1000000000
```

---

## 总结

- **参数作用**：限制每个账户在交易池中最多可以有多少个enqueued交易
- **默认值**：128（可能对高负载场景太小）
- **推荐值**：生产环境使用1024-2048，压测场景使用2048-4096
- **错误触发**：当enqueued队列已满且新交易的nonce不等于账户当前nonce时
- **配置位置**：YAML文件的 `tx_pool.max_account_enqueued` 字段
- **验证方法**：使用 `txpool_inspect` RPC方法查询

