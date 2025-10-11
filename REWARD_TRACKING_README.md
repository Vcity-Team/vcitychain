# DPoS奖励记录和查询功能

## 功能概述

本功能为DPoS共识机制添加了完整的奖励记录和查询系统，允许用户查看自己的奖励历史记录。

## 新增功能

### 1. 数据库存储
- 扩展了`State`结构，添加了`RewardStore`
- 使用BoltDB存储奖励记录
- 支持验证者奖励和投票者奖励的分别记录

### 2. 奖励记录结构
```go
type RewardRecordExtended struct {
    ID              uint64    `json:"id"`
    EpochNumber     uint64    `json:"epoch_number"`
    Recipient       string    `json:"recipient"`
    RewardType      string    `json:"reward_type"`      // "validator" 或 "voter"
    Amount          string    `json:"amount"`           // 奖励金额(Wei)
    BlockCount      uint64    `json:"block_count"`      // 出块数量
    VoteWeight      string    `json:"vote_weight"`      // 投票权重
    Timestamp       time.Time `json:"timestamp"`        // 发放时间
    TransactionHash string    `json:"transaction_hash"` // 相关交易哈希
    Status          string    `json:"status"`           // "completed"
}
```

### 3. JSON-RPC API接口

#### 3.1 查询验证者奖励历史
```bash
curl -X POST http://localhost:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getValidatorRewardHistory",
    "params": ["0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C", 1, 10],
    "id": 1
  }'
```

#### 3.2 查询投票者奖励历史
```bash
curl -X POST http://localhost:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getVoterRewardHistory",
    "params": ["0x1234567890123456789012345678901234567890", 1, 10],
    "id": 1
  }'
```

#### 3.3 查询指定Epoch的奖励详情
```bash
curl -X POST http://localhost:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getEpochRewardDetails",
    "params": [1],
    "id": 1
  }'
```

## 使用方法

### 1. 启动节点
确保DPoS节点正在运行，并且配置了正确的奖励分发参数。

### 2. 等待奖励分发
奖励会在每个epoch结束时自动分发，并记录到数据库中。

### 3. 查询奖励记录
使用上述JSON-RPC接口查询奖励记录。

### 4. 使用PowerShell测试脚本
```powershell
.\test_reward_tracking.ps1
```

## 配置要求

确保在节点配置中设置了以下参数：
```yaml
dpos_reward_distribution: "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe"  # 奖励分发账户
dpos_reward_amount: "1000000000000000000000"  # 每个epoch 1000 VCITY
dpos_validator_reward_ratio: 70  # 验证者奖励比例 70%
dpos_voter_reward_ratio: 30      # 投票者奖励比例 30%
```

## 数据存储位置

奖励记录存储在节点的数据目录中的`dpos.db`文件里，具体路径为：
```
{data_dir}/dpos.db
```

## 注意事项

1. 奖励记录会在每个epoch结束时自动创建
2. 如果节点重启，奖励记录会持久化保存
3. 查询接口支持按epoch范围过滤
4. 奖励金额以Wei为单位存储，需要转换为VCITY显示

## 故障排除

1. **没有奖励记录**：检查节点是否正常运行，是否配置了奖励分发
2. **查询失败**：检查JSON-RPC接口是否正常，参数是否正确
3. **数据不一致**：检查节点日志，确认奖励分发是否成功

## 扩展功能

未来可以考虑添加：
- 奖励统计汇总
- 奖励趋势分析
- 导出奖励记录
- 奖励通知功能
