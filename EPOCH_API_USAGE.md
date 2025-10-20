# Epoch查询接口使用指南

## 概述

本系统提供了完整的Epoch信息查询功能，包括当前Epoch信息、指定Epoch信息以及最新Epoch信息查询。支持命令行工具和JSON-RPC接口两种使用方式。

## 功能特性

- ✅ 查询当前Epoch详细信息
- ✅ 查询指定Epoch信息
- ✅ 查询最新Epoch信息
- ✅ 支持命令行工具和JSON-RPC接口
- ✅ 包含验证者信息、区块范围、时间信息等
- ✅ 自动端口检测和连接重试

## 修改点总结

### 1. 命令行工具改进 (`command/dpos/epoch/epoch.go`)

**主要修改：**
- 实现了真正的JSON-RPC调用，替换了模拟数据
- 添加了多端口自动检测（8545, 9632, 8080）
- 添加了错误处理和重试机制
- 改进了参数验证（epoch编号必须是数字）

**新增功能：**
- HTTP客户端超时设置（30秒）
- 自动端口扫描和连接
- 详细的错误信息返回

### 2. JSON-RPC接口扩展 (`jsonrpc/dpos_endpoint.go`)

**新增接口：**
- `dpos_getLatestEpochInfo` - 获取最新Epoch信息

**现有接口：**
- `dpos_getCurrentEpochInfo` - 获取当前Epoch信息
- `dpos_getEpochInfoByNumber` - 获取指定Epoch信息

### 3. DPoS引擎功能增强 (`consensus/dpos/dpos.go`)

**GetCurrentEpochInfo增强：**
- 添加了验证者详细信息
- 添加了Epoch状态（active/completed/pending）
- 添加了区块范围信息
- 添加了验证者数量和投票权重

**GetEpochInfoByNumber增强：**
- 改进了Epoch状态判断
- 添加了历史Epoch的基本信息
- 添加了区块范围计算

## 接口使用说明

### 命令行工具使用

#### 1. 查询当前Epoch信息

```bash
# 查询当前Epoch信息
./main dpos epoch

# 使用JSON格式输出
./main dpos epoch --json
```

**输出示例：**
```json
{
  "epochNumber": 1,
  "epochStatus": "active",
  "epochStartTime": "2024-01-01T00:00:00Z",
  "epochDuration": "10s",
  "timeRemaining": "5s",
  "nextEpochTime": "2024-01-01T00:00:10Z",
  "firstBlockInEpoch": 0,
  "lastBlockInEpoch": 9,
  "currentBlockNumber": 5,
  "epochSize": 10,
  "validators": [
    {
      "index": 0,
      "address": "0x1234567890123456789012345678901234567890",
      "votingPower": "1000000000000000000000",
      "isActive": true
    }
  ],
  "validatorCount": 4,
  "consensusSwitchHeight": 0
}
```

#### 2. 查询指定Epoch信息

```bash
# 查询Epoch 1的信息
./main dpos epoch 1

# 查询Epoch 5的信息
./main dpos epoch 5
```

**输出示例：**
```json
{
  "epochNumber": 1,
  "epochStatus": "completed",
  "epochDuration": "10s",
  "firstBlockInEpoch": 0,
  "lastBlockInEpoch": 9,
  "epochSize": 10,
  "currentEpoch": 3,
  "currentBlockNumber": 25,
  "consensusSwitchHeight": 0,
  "note": "Historical epoch data limited - only basic information available"
}
```

### JSON-RPC接口使用

#### 1. 获取当前Epoch信息

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getCurrentEpochInfo",
    "params": [],
    "id": 1
  }'
```

**响应：**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "epochNumber": 1,
    "epochStatus": "active",
    "epochStartTime": "2024-01-01T00:00:00Z",
    "epochDuration": "10s",
    "timeRemaining": "5s",
    "nextEpochTime": "2024-01-01T00:00:10Z",
    "firstBlockInEpoch": 0,
    "lastBlockInEpoch": 9,
    "currentBlockNumber": 5,
    "epochSize": 10,
    "validators": [
      {
        "index": 0,
        "address": "0x1234567890123456789012345678901234567890",
        "votingPower": "1000000000000000000000",
        "isActive": true
      }
    ],
    "validatorCount": 4,
    "consensusSwitchHeight": 0
  },
  "id": 1
}
```

#### 2. 获取指定Epoch信息

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getEpochInfoByNumber",
    "params": [1],
    "id": 2
  }'
```

#### 3. 获取最新Epoch信息

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getLatestEpochInfo",
    "params": [],
    "id": 3
  }'
```

## 返回字段说明

### 当前Epoch信息字段

| 字段名 | 类型 | 描述 |
|--------|------|------|
| `epochNumber` | uint64 | Epoch编号 |
| `epochStatus` | string | Epoch状态：active/completed/pending |
| `epochStartTime` | string | Epoch开始时间（RFC3339格式） |
| `epochDuration` | string | Epoch持续时间 |
| `timeRemaining` | string | 剩余时间 |
| `nextEpochTime` | string | 下一个Epoch时间 |
| `firstBlockInEpoch` | uint64 | Epoch内第一个区块号 |
| `lastBlockInEpoch` | uint64 | Epoch内最后一个区块号 |
| `currentBlockNumber` | uint64 | 当前区块号 |
| `epochSize` | uint64 | Epoch大小（区块数） |
| `validators` | array | 验证者列表 |
| `validatorCount` | uint64 | 验证者数量 |
| `consensusSwitchHeight` | uint64 | 共识切换高度 |

### 验证者信息字段

| 字段名 | 类型 | 描述 |
|--------|------|------|
| `index` | uint64 | 验证者索引 |
| `address` | string | 验证者地址 |
| `votingPower` | string | 投票权重 |
| `isActive` | bool | 是否活跃 |

### Epoch状态说明

- **active**: 当前正在进行的Epoch
- **completed**: 已完成的Epoch
- **pending**: 尚未开始的Epoch
- **future**: 未来的Epoch

## 错误处理

### 常见错误

1. **连接错误**
   ```
   failed to connect to JSON-RPC server on any port [8545 9632 8080]: connection refused
   ```
   - 确保节点正在运行
   - 检查JSON-RPC端口配置

2. **参数错误**
   ```
   invalid epoch number: strconv.ParseUint: parsing "abc": invalid syntax
   ```
   - Epoch编号必须是数字

3. **引擎不可用**
   ```json
   {
     "error": "DPoS engine not available"
   }
   ```
   - 确保DPoS引擎已正确初始化

### 故障排除

1. **端口检测**
   - 命令行工具会自动尝试端口 8545, 9632, 8080
   - 如果都失败，检查节点配置和防火墙设置

2. **超时设置**
   - HTTP客户端超时设置为30秒
   - 如果网络较慢，可能需要调整超时时间

3. **权限问题**
   - 确保有权限访问JSON-RPC接口
   - 检查CORS设置

## 配置要求

### 节点配置

确保在 `node-config-validator.yaml` 中正确配置了JSON-RPC端口：

```yaml
jsonrpc_addr: "0.0.0.0:8545"
```

### 网络要求

- 节点必须正在运行
- JSON-RPC接口必须可访问
- 网络连接正常

## 使用示例

### 监控Epoch变化

```bash
# 每10秒查询一次当前Epoch信息
while true; do
  echo "=== $(date) ==="
  ./main dpos epoch --json
  sleep 10
done
```

### 查询历史Epoch

```bash
# 查询最近5个Epoch的信息
for i in {1..5}; do
  echo "=== Epoch $i ==="
  ./main dpos epoch $i --json
  echo
done
```

### 验证者状态监控

```bash
# 获取当前Epoch的验证者信息
./main dpos epoch --json | jq '.validators[] | {address, votingPower, isActive}'
```

## 性能说明

- 命令行工具支持多端口自动检测
- HTTP请求超时设置为30秒
- 支持并发查询多个Epoch信息
- 历史Epoch信息有限，主要提供基本信息

## 注意事项

1. **历史数据限制**: 历史Epoch只提供基本信息，不包含详细的验证者状态
2. **时间精度**: 时间信息基于系统时间，可能存在微小误差
3. **网络依赖**: 需要网络连接才能查询Epoch信息
4. **权限要求**: 需要访问JSON-RPC接口的权限

这样就完成了Epoch查询接口的完整实施和使用说明！
