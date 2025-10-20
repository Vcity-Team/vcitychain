# 可表决参数查询接口使用指南

## 概述

本系统提供了完整的可表决参数查询功能，允许用户查看当前系统中所有可以通过投票机制修改的参数及其详细信息。

## 功能特性

- ✅ 查询所有可表决参数列表
- ✅ 显示参数的详细信息（类型、范围、描述等）
- ✅ 支持命令行工具和JSON-RPC接口
- ✅ 参数分类（经济参数、共识参数等）
- ✅ 实时参数状态查询

## 接口使用说明

### 1. 命令行工具使用

#### 查询可表决参数列表

```bash
# 查询所有可表决参数
./main dpos parameters

# 使用JSON格式输出
./main dpos parameters --json
```

**输出示例：**
```json
{
  "parameters": {
    "dpos_validator_reward_ratio": {
      "name": "Validator Reward Ratio",
      "type": "uint64",
      "minValue": 0,
      "maxValue": 100,
      "description": "验证者奖励比例 (0-100%)",
      "category": "economic"
    },
    "dpos_voter_reward_ratio": {
      "name": "Voter Reward Ratio",
      "type": "uint64",
      "minValue": 0,
      "maxValue": 100,
      "description": "投票者奖励比例 (0-100%)",
      "category": "economic"
    },
    "dpos_reward_amount": {
      "name": "Reward Amount",
      "type": "string",
      "minValue": "0",
      "maxValue": "1000000000000000000000000",
      "description": "每个epoch奖励金额 (wei)",
      "category": "economic"
    },
    "dpos_delegate_threshold": {
      "name": "Delegate Threshold",
      "type": "string",
      "minValue": "1000000000000000000",
      "maxValue": "1000000000000000000000000",
      "description": "最小质押门槛 (wei)",
      "category": "economic"
    },
    "block_time_s": {
      "name": "Block Time",
      "type": "uint64",
      "minValue": 1,
      "maxValue": 60,
      "description": "区块间隔时间 (秒)",
      "category": "consensus"
    },
    "dpos_epoch_duration": {
      "name": "Epoch Duration",
      "type": "string",
      "minValue": "10s",
      "maxValue": "1h",
      "description": "Epoch持续时间",
      "category": "consensus"
    }
  },
  "count": 6
}
```

### 2. JSON-RPC接口使用

#### 获取可表决参数列表

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getVotableParameters",
    "params": [],
    "id": 1
  }'
```

**响应：**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "parameters": {
      "dpos_validator_reward_ratio": {
        "name": "Validator Reward Ratio",
        "type": "uint64",
        "minValue": 0,
        "maxValue": 100,
        "description": "验证者奖励比例 (0-100%)",
        "category": "economic"
      },
      "dpos_voter_reward_ratio": {
        "name": "Voter Reward Ratio",
        "type": "uint64",
        "minValue": 0,
        "maxValue": 100,
        "description": "投票者奖励比例 (0-100%)",
        "category": "economic"
      },
      "dpos_reward_amount": {
        "name": "Reward Amount",
        "type": "string",
        "minValue": "0",
        "maxValue": "1000000000000000000000000",
        "description": "每个epoch奖励金额 (wei)",
        "category": "economic"
      },
      "dpos_delegate_threshold": {
        "name": "Delegate Threshold",
        "type": "string",
        "minValue": "1000000000000000000",
        "maxValue": "1000000000000000000000000",
        "description": "最小质押门槛 (wei)",
        "category": "economic"
      },
      "block_time_s": {
        "name": "Block Time",
        "type": "uint64",
        "minValue": 1,
        "maxValue": 60,
        "description": "区块间隔时间 (秒)",
        "category": "consensus"
      },
      "dpos_epoch_duration": {
        "name": "Epoch Duration",
        "type": "string",
        "minValue": "10s",
        "maxValue": "1h",
        "description": "Epoch持续时间",
        "category": "consensus"
      }
    },
    "count": 6
  },
  "id": 1
}
```

## 可表决参数详细说明

### 经济参数 (Economic Parameters)

#### 1. dpos_validator_reward_ratio
- **名称**: 验证者奖励比例
- **类型**: uint64
- **范围**: 0-100
- **描述**: 验证者奖励比例 (0-100%)
- **当前值**: 70%
- **影响**: 控制验证者获得的奖励比例

#### 2. dpos_voter_reward_ratio
- **名称**: 投票者奖励比例
- **类型**: uint64
- **范围**: 0-100
- **描述**: 投票者奖励比例 (0-100%)
- **当前值**: 30%
- **影响**: 控制投票者获得的奖励比例

#### 3. dpos_reward_amount
- **名称**: 奖励金额
- **类型**: string (大整数)
- **范围**: 0 - 1,000,000 VCITY (wei)
- **描述**: 每个epoch奖励金额 (wei)
- **当前值**: 5 VCITY
- **影响**: 控制每个epoch的总奖励金额

#### 4. dpos_delegate_threshold
- **名称**: 质押门槛
- **类型**: string (大整数)
- **范围**: 1 - 1,000,000 VCITY (wei)
- **描述**: 最小质押门槛 (wei)
- **当前值**: 1000 VCITY
- **影响**: 控制成为验证者的最小质押要求

### 共识参数 (Consensus Parameters)

#### 5. block_time_s
- **名称**: 区块时间
- **类型**: uint64
- **范围**: 1-60秒
- **描述**: 区块间隔时间 (秒)
- **当前值**: 2秒
- **影响**: 控制区块生成频率

#### 6. dpos_epoch_duration
- **名称**: Epoch持续时间
- **类型**: string (时间间隔)
- **范围**: 10秒 - 1小时
- **描述**: Epoch持续时间
- **当前值**: 10秒
- **影响**: 控制Epoch的长度

## 参数分类

### 经济参数 (Economic)
- `dpos_validator_reward_ratio` - 验证者奖励比例
- `dpos_voter_reward_ratio` - 投票者奖励比例
- `dpos_reward_amount` - 奖励金额
- `dpos_delegate_threshold` - 质押门槛

### 共识参数 (Consensus)
- `block_time_s` - 区块时间
- `dpos_epoch_duration` - Epoch持续时间

## 使用场景

### 1. 参数查询
```bash
# 查看所有可表决参数
./main dpos parameters

# 查看特定分类的参数
./main dpos parameters --json | jq '.parameters | to_entries | map(select(.value.category == "economic"))'
```

### 2. 参数验证
```bash
# 检查参数范围
./main dpos parameters --json | jq '.parameters.dpos_validator_reward_ratio | {minValue, maxValue, currentValue}'
```

### 3. 治理准备
```bash
# 查看需要投票的参数
./main dpos parameters --json | jq '.parameters | keys'
```

## 参数修改流程

### 1. 查看当前参数
```bash
./main dpos parameters
```

### 2. 创建参数提案
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_createParameterProposal",
    "params": {
      "proposer": "0x1234567890123456789012345678901234567890",
      "parameter": "dpos_validator_reward_ratio",
      "newValue": 75,
      "description": "Increase validator reward ratio to 75%"
    },
    "id": 1
  }'
```

### 3. 投票表决
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_voteOnParameterProposal",
    "params": {
      "voter": "0x1234567890123456789012345678901234567890",
      "proposalId": "proposal_1_1",
      "support": true
    },
    "id": 2
  }'
```

### 4. 执行参数更新
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_executeParameterUpdate",
    "params": ["proposal_1_1"],
    "id": 3
  }'
```

## 错误处理

### 常见错误

1. **连接错误**
   ```
   failed to connect to JSON-RPC server on any port [8545 9632 8080]: connection refused
   ```
   - 确保节点正在运行
   - 检查JSON-RPC端口配置

2. **引擎不可用**
   ```json
   {
     "error": "DPoS engine not available"
   }
   ```
   - 确保DPoS引擎已正确初始化

3. **参数不存在**
   ```json
   {
     "error": "parameter not found"
   }
   ```
   - 检查参数名称是否正确

## 配置要求

### 节点配置

确保在 `node-config-validator.yaml` 中正确配置了JSON-RPC端口：

```yaml
jsonrpc_addr: "0.0.0.0:8545"
```

### 权限要求

- 需要访问JSON-RPC接口的权限
- 查询参数不需要特殊权限
- 创建提案和投票需要验证者权限

## 性能说明

- 参数查询是只读操作，性能较高
- 支持并发查询
- 参数信息缓存在内存中，响应快速

## 注意事项

1. **参数范围**: 修改参数时必须在允许的范围内
2. **类型匹配**: 参数值必须与定义的类型匹配
3. **权限控制**: 只有验证者可以创建提案和投票
4. **生效时间**: 参数更新需要等待投票通过并执行

## 扩展说明

如需添加新的可表决参数，需要：

1. 在 `getDefaultVotableParameters()` 中添加参数定义
2. 在 `getCurrentParameterValue()` 中添加获取逻辑
3. 在 `UpdateParameterValue()` 中添加更新逻辑
4. 在 `validateParameterValue()` 中添加验证逻辑

这样就完成了可表决参数查询功能的完整实施！
