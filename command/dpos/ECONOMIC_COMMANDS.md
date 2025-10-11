# DPoS经济系统命令行工具

本文档介绍如何使用DPoS经济系统的命令行查询工具。

## 概述

DPoS经济系统提供了3个主要的命令行查询工具：

1. **epoch** - 查询Epoch信息
2. **stats** - 查询验证者出块统计
3. **rewards** - 查询验证者奖励信息

## 命令使用

### 1. Epoch查询命令

查询当前或指定Epoch的信息。

```bash
# 查询当前Epoch信息
./main dpos epoch --chain-id 1 --current

# 查询指定Epoch信息
./main dpos epoch --chain-id 1 --number 5

# 使用JSON-RPC服务器查询
./main dpos epoch --chain-id 1 --jsonrpc http://localhost:8545 --current
```

**参数说明：**
- `--chain-id`: 链ID（必需）
- `--current`: 查询当前Epoch（默认）
- `--number`: 查询指定Epoch编号（0表示当前Epoch）
- `--jsonrpc`: JSON-RPC服务器地址（可选，默认http://localhost:8545）
- `--data-dir`: 数据目录（可选）

**输出示例：**
```
[DPoS EPOCH INFO]
Chain ID|1
Block Height|12345
Epoch Number|5
Epoch Start Time|2024-01-15 10:30:00 UTC
Epoch Duration|2m
Time Remaining|1m30s
Next Epoch Time|2024-01-15 10:32:00 UTC
```

### 2. 验证者出块统计命令

查询验证者的出块统计信息。

```bash
# 查询指定验证者的出块统计
./main dpos stats --chain-id 1 --validator 0x1234567890123456789012345678901234567890 --epoch 5

# 查询所有验证者的出块统计
./main dpos stats --chain-id 1 --all --epoch 5

# 查询当前Epoch的所有验证者统计
./main dpos stats --chain-id 1 --all --epoch 0
```

**参数说明：**
- `--chain-id`: 链ID（必需）
- `--validator`: 验证者地址（与--all二选一）
- `--all`: 查询所有验证者（与--validator二选一）
- `--epoch`: Epoch编号（0表示当前Epoch）
- `--jsonrpc`: JSON-RPC服务器地址（可选）
- `--data-dir`: 数据目录（可选）

**输出示例：**
```
[DPoS ALL VALIDATORS STATS]
Chain ID|1
Block Height|12345
Epoch Number|5
Total Blocks|48
Validators Count|4

[VALIDATORS]
地址                 出块数     总出块     占比%      活跃     投票权重   
--------------------------------------------------------------------------------
0x1234...           12        48         25.00     true     1 ETH
0x5678...           12        48         25.00     true     1 ETH
0x9abc...           12        48         25.00     true     1 ETH
0xdef0...           12        48         25.00     true     1 ETH
```

### 3. 验证者奖励查询命令

查询验证者的奖励信息。

```bash
# 查询指定验证者的奖励信息
./main dpos rewards --chain-id 1 --validator 0x1234567890123456789012345678901234567890 --epoch 5

# 查询所有验证者的奖励信息
./main dpos rewards --chain-id 1 --all --epoch 5

# 查询当前Epoch的所有验证者奖励
./main dpos rewards --chain-id 1 --all --epoch 0
```

**参数说明：**
- `--chain-id`: 链ID（必需）
- `--validator`: 验证者地址（与--all二选一）
- `--all`: 查询所有验证者（与--validator二选一）
- `--epoch`: Epoch编号（0表示当前Epoch）
- `--jsonrpc`: JSON-RPC服务器地址（可选）
- `--data-dir`: 数据目录（可选）

**输出示例：**
```
[DPoS ALL VALIDATORS REWARDS]
Chain ID|1
Block Height|12345
Epoch Number|5
Total Reward|4000 VCITY
Validators Count|4

[VALIDATORS REWARDS]
地址                 验证者奖励        投票者奖励        出块数     每块奖励        
--------------------------------------------------------------------------------
0x1234...           700 VCITY        300 VCITY        12        58.33 VCITY
0x5678...           700 VCITY        300 VCITY        12        58.33 VCITY
0x9abc...           700 VCITY        300 VCITY        12        58.33 VCITY
0xdef0...           700 VCITY        300 VCITY        12        58.33 VCITY
```

## 帮助信息

```bash
# 查看DPoS命令帮助
./main dpos --help

# 查看子命令帮助
./main dpos epoch --help
./main dpos stats --help
./main dpos rewards --help
```

## 注意事项

1. **必需参数**: `--chain-id` 是必需参数，必须指定
2. **地址格式**: 验证者地址必须是有效的以太坊地址格式
3. **Epoch编号**: 使用 `0` 表示当前Epoch
4. **JSON-RPC**: 如果JSON-RPC服务器不可用，命令会使用模拟数据
5. **数据目录**: 如果指定了数据目录，命令会尝试从本地文件读取数据

## 技术实现

这些命令通过以下方式获取数据：

1. **优先使用JSON-RPC**: 如果JSON-RPC服务器可用，优先从运行中的节点获取实时数据
2. **回退到本地数据**: 如果JSON-RPC不可用，尝试从本地数据文件读取
3. **模拟数据**: 如果都不可用，使用模拟数据进行演示

## 扩展性

这些命令设计为可扩展的，未来可以添加更多功能：

- 历史Epoch数据查询
- 奖励分发历史
- 验证者性能分析
- 经济系统配置查询


