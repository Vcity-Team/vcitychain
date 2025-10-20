# DPoS 参数治理 RPC API 使用指南

## 📋 概述

本文档详细介绍了 DPoS 参数治理系统的完整 RPC API 接口和命令行工具使用方法。

## 🚀 快速开始

### 1. 启动节点
```bash
./main server --config ./node1/node-config-validator.yaml --consensus-switch-height=7370
```

### 2. 查看可表决参数
```bash
./main dpos parameters
```

### 3. 查看当前参数值
```bash
./main dpos current-params
```

## 📚 RPC API 接口

### 1. 获取可表决参数列表
**方法**: `dpos_getVotableParameters`  
**参数**: 无  
**返回**: 包含参数定义和当前值的列表

```bash
# 命令行调用
./main dpos parameters

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getVotableParameters",
    "params": [],
    "id": 1
  }'
```

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "count": 6,
    "parameters": {
      "dpos_validator_reward_ratio": {
        "name": "Validator Reward Ratio",
        "type": "uint64",
        "minValue": 0,
        "maxValue": 100,
        "description": "验证者奖励比例 (0-100%)",
        "category": "economic",
        "currentValue": 70
      },
      "dpos_voter_reward_ratio": {
        "name": "Voter Reward Ratio",
        "type": "uint64",
        "minValue": 0,
        "maxValue": 100,
        "description": "投票者奖励比例 (0-100%)",
        "category": "economic",
        "currentValue": 30
      }
    }
  }
}
```

### 2. 获取当前参数值
**方法**: `dpos_getCurrentParameterValues`  
**参数**: 无  
**返回**: 当前所有参数的实际值

```bash
# 命令行调用
./main dpos current-params

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getCurrentParameterValues",
    "params": [],
    "id": 1
  }'
```

### 3. 创建参数提案
**方法**: `dpos_createParameterProposal`  
**参数**: 
- `parameter`: 参数名称
- `newValue`: 新值
- `description`: 提案描述
- `proposer`: 提案者地址（可选）

```bash
# 命令行调用
./main dpos proposal create-proposal \
  --parameter "dpos_validator_reward_ratio" \
  --new-value "80" \
  --description "提高验证者奖励比例到80%" \
  --proposer "0x1234567890abcdef..."

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_createParameterProposal",
    "params": [
      "dpos_validator_reward_ratio",
      80,
      "提高验证者奖励比例到80%",
      "0x1234567890abcdef..."
    ],
    "id": 1
  }'
```

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "success": true,
    "proposalId": "proposal_001",
    "message": "Parameter proposal created successfully"
  }
}
```

### 4. 对提案进行投票
**方法**: `dpos_voteOnParameterProposal`  
**参数**:
- `proposalId`: 提案ID
- `voter`: 投票者地址
- `support`: 是否支持（true/false）

```bash
# 命令行调用
./main dpos proposal vote \
  --proposal-id "proposal_001" \
  --voter "0x1234567890abcdef..." \
  --support true

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_voteOnParameterProposal",
    "params": [
      "proposal_001",
      "0x1234567890abcdef...",
      true
    ],
    "id": 1
  }'
```

### 5. 获取提案信息
**方法**: `dpos_getParameterProposal`  
**参数**: `proposalId` - 提案ID

```bash
# 命令行调用
./main dpos proposal get --proposal-id "proposal_001"

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getParameterProposal",
    "params": ["proposal_001"],
    "id": 1
  }'
```

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "success": true,
    "proposal": {
      "proposalId": "proposal_001",
      "parameter": "dpos_validator_reward_ratio",
      "oldValue": 70,
      "newValue": 80,
      "proposer": "0x1234567890abcdef...",
      "startBlock": 1000,
      "endBlock": 2000,
      "status": "active",
      "threshold": 50,
      "description": "提高验证者奖励比例到80%",
      "createdAt": "2025-01-20T10:00:00Z",
      "votes": {
        "0x1234567890abcdef...": {
          "voter": "0x1234567890abcdef...",
          "proposalId": "proposal_001",
          "support": true,
          "weight": "1000000000000000000",
          "timestamp": "2025-01-20T10:05:00Z"
        }
      }
    }
  }
}
```

### 6. 检查提案结果
**方法**: `dpos_checkProposalResult`  
**参数**: `proposalId` - 提案ID

```bash
# 命令行调用
./main dpos proposal check-result --proposal-id "proposal_001"

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_checkProposalResult",
    "params": ["proposal_001"],
    "id": 1
  }'
```

### 7. 执行参数更新
**方法**: `dpos_executeParameterUpdate`  
**参数**: `proposalId` - 提案ID

```bash
# 命令行调用
./main dpos proposal execute --proposal-id "proposal_001"

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_executeParameterUpdate",
    "params": ["proposal_001"],
    "id": 1
  }'
```

### 8. 获取活跃提案列表
**方法**: `dpos_getActiveProposals`  
**参数**: 无

```bash
# 命令行调用
./main dpos active-proposals

# 直接 RPC 调用
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getActiveProposals",
    "params": [],
    "id": 1
  }'
```

## 🛠️ 命令行工具

### 参数治理命令组
```bash
./main dpos proposal --help
```

**子命令**:
- `create-proposal`: 创建参数提案
- `vote`: 对提案进行投票
- `get`: 获取提案信息
- `check-result`: 检查提案结果
- `execute`: 执行参数更新

### 参数查询命令组
```bash
./main dpos parameters --help    # 获取可表决参数列表
./main dpos current-params --help # 获取当前参数值
```

## 📊 可表决参数列表

| 参数名称 | 类型 | 最小值 | 最大值 | 描述 | 分类 |
|---------|------|--------|--------|------|------|
| `dpos_validator_reward_ratio` | uint64 | 0 | 100 | 验证者奖励比例 (0-100%) | economic |
| `dpos_voter_reward_ratio` | uint64 | 0 | 100 | 投票者奖励比例 (0-100%) | economic |
| `dpos_reward_amount` | string | "0" | "1000000000000000000000000" | 每个epoch奖励金额 (wei) | economic |
| `dpos_delegate_threshold` | string | "1000000000000000000" | "1000000000000000000000000" | 最小质押门槛 (wei) | economic |
| `block_time_s` | uint64 | 1 | 60 | 区块间隔时间 (秒) | consensus |
| `dpos_epoch_duration` | string | "10s" | "1h" | Epoch持续时间 | consensus |

## 🔄 提案生命周期

1. **创建提案** → 提案状态: `pending`
2. **开始投票** → 提案状态: `active`
3. **投票结束** → 提案状态: `passed` 或 `rejected`
4. **执行更新** → 提案状态: `executed`

## 💾 数据持久化

- **参数值存储**: 存储在 `consensus/dpos/dpos.db` 的 `parameters` bucket 中
- **缓存机制**: 内存缓存 + 数据库持久化，启动时强制同步
- **一致性保证**: Write-Through 模式，确保缓存和数据库同步更新

## 🚨 错误处理

所有 RPC 接口都返回统一的错误格式：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "success": false,
    "error": "错误描述信息"
  }
}
```

## 🔧 配置要求

确保节点配置文件中包含必要的参数：

```yaml
# node-config-validator.yaml
dpos:
  validator_reward_ratio: 70
  voter_reward_ratio: 30
  reward_amount: "5000000000000000000"
  min_voting_power: "1000000000000000000"
  block_time: "2s"
  epoch_duration: "10s"
```

## 📝 注意事项

1. **权限要求**: 只有验证者可以创建提案和投票
2. **投票权重**: 基于验证者的质押数量计算
3. **提案阈值**: 需要达到一定比例的投票才能通过
4. **参数验证**: 新值必须在定义的范围内
5. **并发安全**: 使用读写锁保证并发安全

## 🎯 使用示例

### 完整流程示例

```bash
# 1. 查看当前参数
./main dpos current-params

# 2. 创建提案
./main dpos proposal create-proposal \
  --parameter "dpos_validator_reward_ratio" \
  --new-value "80" \
  --description "提高验证者奖励比例"

# 3. 投票支持
./main dpos proposal vote \
  --proposal-id "proposal_001" \
  --voter "0x1234..." \
  --support true

# 4. 检查结果
./main dpos proposal check-result --proposal-id "proposal_001"

# 5. 执行更新
./main dpos proposal execute --proposal-id "proposal_001"

# 6. 验证更新
./main dpos current-params
```

---

**版本**: 1.0.0  
**更新时间**: 2025-01-20  
**维护者**: VCity Team
