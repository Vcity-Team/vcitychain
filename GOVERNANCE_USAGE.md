# 参数表决机制使用指南

## 概述

本系统实现了基于DPoS投票系统的参数表决机制，允许验证者通过投票来修改系统参数，如 `dpos_validator_reward_ratio` 等。

## 功能特性

- ✅ 基于现有DPoS投票系统，无需智能合约
- ✅ 支持多种参数类型（uint64、string、duration等）
- ✅ 完整的提案生命周期管理
- ✅ 安全的投票权重计算
- ✅ JSON-RPC接口支持
- ✅ 参数验证和范围检查

## 可表决参数

| 参数名 | 类型 | 范围 | 描述 | 分类 |
|--------|------|------|------|------|
| `dpos_validator_reward_ratio` | uint64 | 0-100 | 验证者奖励比例 | economic |
| `dpos_voter_reward_ratio` | uint64 | 0-100 | 投票者奖励比例 | economic |
| `dpos_reward_amount` | string | 0-1000000 VCITY | 每个epoch奖励金额 | economic |
| `dpos_delegate_threshold` | string | 1-1000000 VCITY | 最小质押门槛 | economic |
| `block_time_s` | uint64 | 1-60 | 区块间隔时间(秒) | consensus |
| `dpos_epoch_duration` | string | 10s-1h | Epoch持续时间 | consensus |

## JSON-RPC API

### 1. 创建参数提案

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

**响应:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "proposalId": "proposal_1_1",
    "parameter": "dpos_validator_reward_ratio",
    "oldValue": 70,
    "newValue": 75,
    "proposer": "0x1234567890123456789012345678901234567890",
    "startBlock": 1001,
    "endBlock": 1100,
    "status": "pending",
    "threshold": 51,
    "description": "Increase validator reward ratio to 75%",
    "createdAt": 1640995200
  },
  "id": 1
}
```

### 2. 对提案进行投票

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

**响应:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "success": true,
    "proposalId": "proposal_1_1",
    "voter": "0x1234567890123456789012345678901234567890",
    "support": true
  },
  "id": 2
}
```

### 3. 获取提案信息

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getParameterProposal",
    "params": ["proposal_1_1"],
    "id": 3
  }'
```

**响应:**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "proposalId": "proposal_1_1",
    "parameter": "dpos_validator_reward_ratio",
    "oldValue": 70,
    "newValue": 75,
    "proposer": "0x1234567890123456789012345678901234567890",
    "startBlock": 1001,
    "endBlock": 1100,
    "status": "active",
    "threshold": 51,
    "description": "Increase validator reward ratio to 75%",
    "createdAt": 1640995200,
    "votes": {
      "0x1234567890123456789012345678901234567890": {
        "voter": "0x1234567890123456789012345678901234567890",
        "proposalId": "proposal_1_1",
        "support": true,
        "weight": "1000000000000000000000",
        "timestamp": 1640995200
      }
    }
  },
  "id": 3
}
```

### 4. 获取活跃提案列表

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getActiveProposals",
    "params": [],
    "id": 4
  }'
```

### 5. 获取可表决参数列表

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getVotableParameters",
    "params": [],
    "id": 5
  }'
```

### 6. 检查提案结果

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_checkProposalResult",
    "params": ["proposal_1_1"],
    "id": 6
  }'
```

### 7. 执行参数更新

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_executeParameterUpdate",
    "params": ["proposal_1_1"],
    "id": 7
  }'
```

## 使用流程

### 1. 创建提案
只有验证者可以创建参数表决提案。提案包含：
- 参数名称
- 新参数值
- 提案描述
- 投票期（100个区块）

### 2. 投票阶段
- 只有验证者可以投票
- 投票权重基于验证者的投票权重
- 支持/反对两种选择
- 投票期结束后自动统计结果

### 3. 结果统计
- 计算支持率
- 与阈值（默认51%）比较
- 更新提案状态（通过/拒绝）

### 4. 参数更新
- 通过的提案可以执行参数更新
- 参数更新在下一个区块生效
- 记录参数变更历史

## 安全特性

1. **权限控制**: 只有验证者可以创建提案和投票
2. **参数验证**: 严格的参数值范围检查
3. **防重放**: 投票签名验证
4. **时间窗口**: 固定的投票期，防止长期悬而未决
5. **权重计算**: 基于验证者实际投票权重

## 配置示例

在 `node-config-validator.yaml` 中，以下参数可以通过表决修改：

```yaml
dpos_validator_reward_ratio: 70  # 验证者奖励比例 70%
dpos_voter_reward_ratio: 30      # 投票者奖励比例 30%
dpos_reward_amount: "5000000000000000000"  # 每个epoch 5 VCITY
dpos_delegate_threshold: "1000000000000000000000"  # 1000 VCITY
block_time_s: 2                  # 区块间隔时间 2秒
dpos_epoch_duration: "10s"       # Epoch持续时间 10秒
```

## 注意事项

1. 提案创建后需要等待投票期结束才能统计结果
2. 参数更新需要手动执行，不会自动生效
3. 建议在测试网络上充分测试后再在主网使用
4. 重大参数变更建议提前通知社区

## 故障排除

### 常见错误

1. **"only validators can create proposals"**
   - 确保提案者地址是当前验证者

2. **"parameter X is not votable"**
   - 检查参数名称是否正确
   - 确认参数在可表决列表中

3. **"invalid parameter value"**
   - 检查参数值是否在允许范围内
   - 确认参数类型是否正确

4. **"voting period has ended"**
   - 检查当前区块是否在投票期内
   - 确认提案状态是否为active

## 开发说明

如需扩展新的可表决参数，需要：

1. 在 `getDefaultVotableParameters()` 中添加参数定义
2. 在 `getCurrentParameterValue()` 中添加获取逻辑
3. 在 `UpdateParameterValue()` 中添加更新逻辑
4. 在 `validateParameterValue()` 中添加验证逻辑

这样就完成了参数表决机制的完整实施！
