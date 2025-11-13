# dpos_getCurrentEpochInfo 返回字段说明

## 概述

`dpos_getCurrentEpochInfo` 是一个 JSON-RPC 方法，用于获取当前 Epoch 的详细信息，包括 Epoch 基本信息、时间信息、区块范围、验证者列表等。

## 返回字段详解

### 顶层字段

#### `epochNumber` (uint64)
- **说明**: 当前 Epoch 的编号
- **计算方式**: 
  - 如果当前区块高度 < 共识切换高度，返回 0
  - 否则：`(当前区块高度 - 共识切换高度) / epochSize + 1`
- **示例**: `1`, `2`, `3`

#### `epochStatus` (string)
- **说明**: 当前 Epoch 的状态
- **可能值**:
  - `"active"`: Epoch 正在进行中
  - `"completed"`: Epoch 已完成
  - `"pending"`: Epoch 尚未开始
- **判断逻辑**: 基于当前时间与 Epoch 时间范围的比较

#### `epochStartTime` (string)
- **说明**: Epoch 开始时间
- **格式**: RFC3339 格式的时间字符串（ISO 8601）
- **示例**: `"2024-01-01T00:00:00Z"`

#### `epochDuration` (string)
- **说明**: Epoch 的持续时间
- **格式**: Go time.Duration 字符串格式
- **示例**: `"24h0m0s"`, `"3600s"`

#### `timeRemaining` (string)
- **说明**: 距离下一个 Epoch 的剩余时间
- **格式**: Go time.Duration 字符串格式
- **计算**: 如果当前时间 < 下一个 Epoch 时间，返回剩余时间；否则返回 `"0s"`
- **示例**: `"2h30m15s"`, `"0s"`

#### `nextEpochTime` (string)
- **说明**: 下一个 Epoch 的开始时间
- **格式**: RFC3339 格式的时间字符串
- **计算**: `epochStartTime + epochDuration`
- **示例**: `"2024-01-02T00:00:00Z"`

#### `firstBlockInEpoch` (uint64)
- **说明**: 当前 Epoch 的第一个区块号
- **计算方式**:
  - 如果 epochNumber = 0，返回 0
  - 否则：`共识切换高度 + (epochNumber - 1) * epochSize`
- **示例**: `1000`, `2000`

#### `lastBlockInEpoch` (uint64)
- **说明**: 当前 Epoch 的最后一个区块号
- **计算方式**: `firstBlockInEpoch + epochSize - 1`
- **示例**: `1999`, `2999`

#### `currentBlockNumber` (uint64)
- **说明**: 当前链上的最新区块号
- **来源**: 从区块链头部获取
- **示例**: `1500`, `2500`

#### `epochSize` (uint64)
- **说明**: 每个 Epoch 包含的区块数量
- **来源**: 从配置中获取（`DPoSValidatorsCount` 或 `DelegateCount`）
- **示例**: `1000`, `2000`

#### `validators` (array)
- **说明**: 当前 Epoch 的验证者列表
- **排序**: 按投票权重（votingPower）倒序排列
- **限制**: 受配置中的 `DPoSValidatorsCount` 限制
- **过滤**: 已过滤掉故障验证者
- **类型**: 数组，每个元素是一个验证者对象（见下方验证者字段说明）

#### `validatorCount` (uint64)
- **说明**: 验证者列表中的验证者数量
- **计算**: `len(validators)`
- **示例**: `21`, `50`

#### `consensusSwitchHeight` (uint64)
- **说明**: 共识机制切换的区块高度（从 PoW/PoA 切换到 DPoS）
- **来源**: 从配置中获取
- **用途**: 用于计算 DPoS Epoch 的起始位置
- **示例**: `1000`, `5000`

### 验证者对象字段（validators 数组中的每个元素）

#### `index` (uint64)
- **说明**: 验证者在列表中的索引位置（从 0 开始）
- **排序**: 按投票权重从高到低排序
- **示例**: `0`, `1`, `2`

#### `address` (string)
- **说明**: 验证者的地址（20 字节，十六进制格式）
- **格式**: 0x 开头的十六进制字符串
- **示例**: `"0x1234567890123456789012345678901234567890"`

#### `votingPower` (string)
- **说明**: 验证者的投票权重（总质押量）
- **格式**: 大整数字符串（支持任意精度）
- **单位**: 最小单位（wei）
- **示例**: `"1000000000000000000000"` (1000 tokens)

#### `isActive` (bool)
- **说明**: 验证者是否处于活跃状态
- **可能值**: `true` 或 `false`
- **含义**: 
  - `true`: 验证者当前可以参与出块
  - `false`: 验证者当前不可参与出块

#### `faultFlag` (object)
- **说明**: 验证者的故障标志信息
- **类型**: 对象，包含以下子字段：

##### `faultFlag.isFaulty` (bool)
- **说明**: 验证者是否有故障
- **判断标准**: 如果漏块数 >= 配置的最大漏块数阈值，则为 `true`
- **可能值**: `true` 或 `false`

##### `faultFlag.missedBlocks` (uint64)
- **说明**: 验证者在上一个 Epoch 中漏掉的区块数
- **计算**: 期望出块数 - 实际出块数
- **示例**: `0`, `5`, `10`

##### `faultFlag.lastUpdateTime` (uint64)
- **说明**: 故障信息最后更新的时间戳
- **格式**: Unix 时间戳（秒）
- **示例**: `1704067200`

##### `faultFlag.lastFaultyEpoch` (uint64)
- **说明**: 验证者最后一次出现故障的 Epoch 编号
- **说明**: 如果从未故障，则为 0
- **示例**: `0`, `5`, `10`

##### `faultFlag.reason` (string)
- **说明**: 故障原因说明
- **格式**: 人类可读的字符串
- **示例**: 
  - `"Epoch 5: missed blocks reached threshold: 10 >= 10"`
  - `"Epoch 5: missed blocks normal: 2 less than 10"`

#### `blocksProduced` (uint64)
- **说明**: 验证者在当前 Epoch 中实际出块的数量
- **来源**: 从区块追踪器（blockTracker）获取
- **说明**: 如果区块追踪器不可用，则为 0
- **示例**: `50`, `100`, `0`

## 返回示例

```json
{
  "epochNumber": 5,
  "epochStatus": "active",
  "epochStartTime": "2024-01-01T00:00:00Z",
  "epochDuration": "24h0m0s",
  "timeRemaining": "12h30m15s",
  "nextEpochTime": "2024-01-02T00:00:00Z",
  "firstBlockInEpoch": 5000,
  "lastBlockInEpoch": 5999,
  "currentBlockNumber": 5500,
  "epochSize": 1000,
  "validators": [
    {
      "index": 0,
      "address": "0x1234567890123456789012345678901234567890",
      "votingPower": "1000000000000000000000",
      "isActive": true,
      "faultFlag": {
        "isFaulty": false,
        "missedBlocks": 0,
        "lastUpdateTime": 1704067200,
        "lastFaultyEpoch": 0,
        "reason": "Epoch 4: missed blocks normal: 0 less than 10"
      },
      "blocksProduced": 50
    },
    {
      "index": 1,
      "address": "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
      "votingPower": "900000000000000000000",
      "isActive": true,
      "faultFlag": {
        "isFaulty": false,
        "missedBlocks": 2,
        "lastUpdateTime": 1704067200,
        "lastFaultyEpoch": 0,
        "reason": "Epoch 4: missed blocks normal: 2 less than 10"
      },
      "blocksProduced": 48
    }
  ],
  "validatorCount": 21,
  "consensusSwitchHeight": 1000
}
```

## 错误情况

如果 Epoch Manager 未初始化，返回：

```json
{
  "error": "epoch manager not initialized"
}
```

## 使用场景

1. **监控 Epoch 进度**: 查看当前 Epoch 编号、状态和剩余时间
2. **验证者信息查询**: 查看当前 Epoch 的验证者列表及其状态
3. **故障检测**: 通过 `faultFlag` 字段了解验证者的故障情况
4. **出块统计**: 通过 `blocksProduced` 字段查看验证者的出块表现
5. **区块范围查询**: 了解当前 Epoch 对应的区块范围

## 相关方法

- `dpos_getEpochInfoByNumber`: 获取指定 Epoch 的信息
- `dpos_getLatestEpochInfo`: 获取最新 Epoch 信息（与 `dpos_getCurrentEpochInfo` 相同）

## 注意事项

1. **Epoch 计算**: Epoch 编号从共识切换高度开始计算，在此之前的所有区块都属于 Epoch 0
2. **验证者排序**: 验证者列表按投票权重从高到低排序
3. **故障过滤**: 故障验证者会被自动过滤，不会出现在验证者列表中
4. **时间格式**: 所有时间字段使用 RFC3339 格式（ISO 8601）
5. **大整数**: `votingPower` 使用字符串格式，以避免 JavaScript 等语言的精度丢失问题

