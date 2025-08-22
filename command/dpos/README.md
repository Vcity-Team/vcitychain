# DPoS 共识命令

这个包提供了与DPoS（委托权益证明）共识机制交互的命令行工具。

## 概述

DPoS是一种高效的共识机制，通过委托投票的方式选择区块生产者。本命令包实现了查询DPoS网络状态、验证者信息、质押详情等核心功能。

## 可用命令

### `validator-info` - 查询验证者信息

获取DPoS网络的详细验证者信息，包括：

- 当前受托人集合
- 质押信息
- 共识状态
- 投票权重
- 网络统计

#### 使用方法

```bash
# 基本用法
./main dpos validator-info --chain-id 100 --jsonrpc http://localhost:8545

# 指定数据目录
./main dpos validator-info --chain-id 100 --data-dir ./data1 --jsonrpc http://localhost:8545

# JSON输出格式
./main dpos validator-info --chain-id 100 --jsonrpc http://localhost:8545 --json
```

#### 参数说明

| 参数 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `--chain-id` | uint64 | 是 | 要查询的链ID |
| `--data-dir` | string | 否 | Polygon Edge数据目录 |
| `--jsonrpc` | string | 否 | JSON-RPC端点地址（默认：http://0.0.0.0:8545） |
| `--json` | bool | 否 | 以JSON格式输出结果 |

#### 输出示例

```
[DPoS VALIDATOR INFO]
Chain ID|100
Block Height|12345
Current Round|67
Current Delegate|0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed
Total Stake|1000000000000000000000
Active Delegates|21

[DELEGATES]
Delegate 1:
Address|0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed
Voting Power|100000000000000000000
Stake Amount|100000000000000000000
Is Active|true
Index|0

[STAKING INFO]
Staker 1:
Staker Address|0x1234567890123456789012345678901234567890
Amount|50000000000000000000
Delegate|0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed
Start Time|1640995200
End Time|0
Is Locked|false
Is Active|true
Rewards|0
```

## 架构说明

### 命令结构

```
dpos/
├── dpos_command.go          # DPoS主命令
└── validator_info/          # 验证者信息子命令
    ├── validator_info.go    # 主要逻辑
    └── params.go           # 参数和结果结构
```

### 数据流

1. **命令解析**：解析命令行参数和标志
2. **RPC连接**：建立到节点的JSON-RPC连接
3. **状态查询**：通过RPC调用获取DPoS状态
4. **数据转换**：将内部数据结构转换为用户友好的格式
5. **结果输出**：格式化并输出结果

### 扩展性

本命令包设计为可扩展的，未来可以轻松添加：

- 委托管理命令
- 投票操作命令
- 质押操作命令
- 共识状态监控
- 奖励查询命令

## 实现状态

### ✅ 已完成

- 基础命令结构
- 参数验证
- 输出格式化
- 错误处理
- 文档和示例

### 🔄 待实现

- 真正的DPoS状态查询（需要实现RPC方法）
- 实时数据更新
- 高级查询选项
- 性能优化

### 📋 技术债务

当前实现使用模拟数据，因为：

1. **RPC方法缺失**：节点需要实现自定义RPC方法来暴露DPoS状态
2. **共识引擎访问**：需要直接访问DPoS共识引擎的状态
3. **数据同步**：需要确保查询的数据与当前共识状态一致

## 下一步计划

### 短期目标

1. 实现基本的DPoS RPC方法
2. 集成到现有节点架构
3. 添加单元测试

### 长期目标

1. 完整的DPoS管理功能
2. 实时监控和告警
3. 性能优化和缓存
4. 多链支持

## 贡献指南

欢迎贡献代码！请遵循以下步骤：

1. Fork项目
2. 创建功能分支
3. 实现功能并添加测试
4. 提交Pull Request

## 许可证

本项目采用与主项目相同的许可证。







