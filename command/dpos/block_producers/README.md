# Block Producers 查询命令

## 功能

查询指定区块范围内的出块者信息，支持按区块范围或 Epoch 查询。

## 使用方法

### 1. 按区块范围查询

```bash
# 查询区块 1000 到 2000 的出块者
./main.exe dpos block-producers --chain-id 20230826 --start-block 1000 --end-block 2000

# 查询区块 7370 到 7380 的出块者
./main.exe dpos block-producers --chain-id 20230826 --start-block 7370 --end-block 7380
```

### 2. 按 Epoch 查询

```bash
# 查询 Epoch 1 的出块者（会自动转换为对应的区块范围）
./main.exe dpos block-producers --chain-id 20230826 --epoch 1

# 查询 Epoch 2 的出块者
./main.exe dpos block-producers --chain-id 20230826 --epoch 2
```

## 返回结果

### 输出格式

```
Block Producers Information
=====================================

Query Mode: Block Range
Block Range: [7370, 7380]

Total Blocks in Range: 11
Actual Blocks Found: 11

Block Producers Summary:
------------------------------------
Address                                    Blocks   
------------------------------------
0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C 4        
0x7744E828e4Bd34D4B8B52BA6BC8e093Be17935FB 3        
0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9 2        
0xa5Ce949C933e06E8395194AE147283e091EcF3Cb 2        

Detailed Block List:
------------------------------------
Producer: 0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C (produced 4 blocks)
  Blocks: 7370, 7374, 7378, 7382

Producer: 0x7744E828e4Bd34D4B8B52BA6BC8e093Be17935FB (produced 3 blocks)
  Blocks: 7371, 7375, 7379

Producer: 0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9 (produced 2 blocks)
  Blocks: 7372, 7376

Producer: 0xa5Ce949C933e06E8395194AE147283e091EcF3Cb (produced 2 blocks)
  Blocks: 7373, 7377
```

### JSON 格式输出

```bash
# 使用 --json 标志获取 JSON 格式输出
./main.exe dpos block-producers --chain-id 20230826 --start-block 7370 --end-block 7380 --json
```

## 参数说明

| 参数 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `--chain-id` | uint64 | 是 | 链 ID |
| `--start-block` | uint64 | 条件必需 | 起始区块号（区块范围模式） |
| `--end-block` | uint64 | 条件必需 | 结束区块号（区块范围模式） |
| `--epoch` | uint64 | 条件必需 | Epoch 编号（Epoch 模式） |
| `--jsonrpc` | string | 否 | JSON-RPC 接口地址（默认：http://0.0.0.0:8545） |
| `--data-dir` | string | 否 | 数据目录 |

## 限制

- 最多查询 1000 个区块
- 详细出块列表仅当出块者数量 <= 5 时显示

## 示例场景

### 场景 1：查询最近 100 个区块的出块者分布

```bash
# 先获取最新区块号（假设为 7400）
./main.exe eth blockNumber

# 查询区块 7300-7400 的出块者
./main.exe dpos block-producers --chain-id 20230826 --start-block 7300 --end-block 7400
```

### 场景 2：查询某个 Epoch 的出块者分布

```bash
# 查询 Epoch 5 的出块者
./main.exe dpos block-producers --chain-id 20230826 --epoch 5
```

### 场景 3：检查特定验证者的出块情况

```bash
# 查询并过滤某个地址
./main.exe dpos block-producers --chain-id 20230826 --start-block 7370 --end-block 7380 --json | grep "0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C"
```

## RPC 直接调用

也可以通过 JSON-RPC 直接调用：

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getBlockProducers",
    "params": [7370, 7380],
    "id": 1
  }'
```

或按 Epoch 查询：

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getBlockProducers",
    "params": [1],
    "id": 1
  }'
```
