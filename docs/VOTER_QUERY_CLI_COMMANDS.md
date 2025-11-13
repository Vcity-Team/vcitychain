# 查询投票者信息的 CLI 命令

## 1. 查询所有投票者的投票权重

使用 `voting-staking-info` 命令查询所有投票和质押信息：

```bash
# 基本用法
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545

# 使用 JSON 输出格式（便于解析）
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json

# 指定数据目录（如果使用本地节点）
./main dpos voting-staking-info --chain-id 100 --data-dir ./data1 --jsonrpc http://209.53.43.252:9545
```

**输出说明**：
- 显示所有验证者的质押信息
- 显示所有投票者的投票信息
- 按投票权重排序显示

**参数说明**：
- `--chain-id`: 链ID（必需）
- `--jsonrpc`: JSON-RPC服务器地址（默认：http://localhost:8545）
- `--data-dir`: 数据目录（可选）
- `--json`: 以JSON格式输出（可选）

## 2. 检查投票者奖励池的精确值

奖励池值需要从配置或查询当前 epoch 信息获取。可以使用以下命令：

```bash
# 查询当前 epoch 信息（包含奖励配置）
./main dpos epoch --chain-id 100 --current --jsonrpc http://209.53.43.252:9545

# 使用 JSON 输出
./main dpos epoch --chain-id 100 --current --jsonrpc http://209.53.43.252:9545 --json
```

**计算投票者奖励池**：
- 总奖励池 = 100 VCITY（从配置获取）
- 投票者奖励比例 = 30%
- 投票者奖励池 = 100 × 30% = 30 VCITY

## 3. 查询总投票权重

### 方法1：使用 voting-staking-info 命令

```bash
# 查询所有投票信息
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json | jq '.stakingInfo[] | select(.delegate != null) | .amountWei' | awk '{sum+=$1} END {print sum}'
```

### 方法2：使用 validator-voting-details 命令查询特定验证者

```bash
# 查询特定验证者的投票详情（包含投票给该验证者的所有投票者）
./main dpos validator-voting-details --chain-id 100 --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 --jsonrpc http://209.53.43.252:9545

# 使用 JSON 输出
./main dpos validator-voting-details --chain-id 100 --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 --jsonrpc http://209.53.43.252:9545 --json
```

**输出说明**：
- `totalStakedToMe`: 投票给该验证者的总投票权重
- `stakes`: 所有投票给该验证者的投票者列表

## 4. 查询特定投票者的权重占比

### 方法1：使用 voting-staking-info 并过滤

```bash
# 查询所有投票信息并过滤特定投票者
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json | jq '.stakingInfo[] | select(.staker == "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127")'
```

### 方法2：使用 validator-voting-details 查询该投票者投票的验证者

```bash
# 如果该地址是验证者，查询其投票详情
./main dpos validator-voting-details --chain-id 100 --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 --jsonrpc http://209.53.43.252:9545 --json
```

## 5. 综合查询脚本（使用 jq 解析）

创建一个脚本来计算总投票权重和特定投票者的占比：

```bash
#!/bin/bash

CHAIN_ID=100
RPC_URL="http://209.53.43.252:9545"
TARGET_ADDR="0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"

# 查询所有质押信息
echo "========== 查询所有投票者信息 =========="
./main dpos voting-staking-info --chain-id $CHAIN_ID --jsonrpc $RPC_URL --json > /tmp/staking_info.json

# 提取所有投票者（有 delegate 的条目）
echo "========== 提取投票者信息 =========="
jq -r '.stakingInfo[] | select(.delegate != null and .delegate != "") | "\(.staker)|\(.delegate)|\(.amountWei)"' /tmp/staking_info.json > /tmp/voters.txt

# 计算总投票权重
echo "========== 计算总投票权重 =========="
TOTAL_POWER=0
while IFS='|' read -r staker delegate amount; do
    TOTAL_POWER=$((TOTAL_POWER + amount))
done < /tmp/voters.txt

echo "总投票权重: $TOTAL_POWER Wei = $(echo "scale=18; $TOTAL_POWER / 1000000000000000000" | bc) VCITY"

# 查找目标投票者
echo "========== 查询目标投票者 =========="
TARGET_POWER=$(jq -r ".stakingInfo[] | select(.staker == \"$TARGET_ADDR\" and .delegate != null) | .amountWei" /tmp/staking_info.json | head -1)

if [ -n "$TARGET_POWER" ]; then
    RATIO=$(echo "scale=2; $TARGET_POWER * 100 / $TOTAL_POWER" | bc)
    echo "投票者地址: $TARGET_ADDR"
    echo "投票权重: $TARGET_POWER Wei = $(echo "scale=18; $TARGET_POWER / 1000000000000000000" | bc) VCITY"
    echo "权重占比: $RATIO%"
    
    # 计算预期奖励
    REWARD_AMOUNT=100000000000000000000  # 100 VCITY
    VOTER_RATIO=30  # 30%
    VOTER_REWARD_POOL=$((REWARD_AMOUNT * VOTER_RATIO / 100))
    EXPECTED_REWARD=$(echo "scale=0; $VOTER_REWARD_POOL * $TARGET_POWER / $TOTAL_POWER" | bc)
    echo "预期投票者奖励: $EXPECTED_REWARD Wei = $(echo "scale=18; $EXPECTED_REWARD / 1000000000000000000" | bc) VCITY"
else
    echo "未找到投票者: $TARGET_ADDR"
fi

# 显示所有投票者（按权重排序）
echo "========== 所有投票者列表（按权重排序） =========="
jq -r '.stakingInfo[] | select(.delegate != null and .delegate != "") | "\(.staker) -> \(.delegate): \(.amountWei) Wei"' /tmp/staking_info.json | sort -t: -k2 -n -r
```

## 6. 使用 curl 直接调用 JSON-RPC

如果 CLI 命令不可用，可以直接使用 curl 调用 JSON-RPC：

```bash
# 查询所有质押信息
curl -X POST http://209.53.43.252:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getStakingInfo",
    "params": ["latest"],
    "id": 1
  }' | jq '.result[] | select(.delegate != null) | {staker: .staker, delegate: .delegate, amountWei: .amountWei}'
```

## 命令参数总结

### voting-staking-info 命令
```bash
./main dpos voting-staking-info [flags]

Flags:
  --chain-id uint     链ID（必需）
  --jsonrpc string    JSON-RPC服务器地址（默认：http://localhost:8545）
  --data-dir string   数据目录（可选）
  --json             以JSON格式输出
```

### validator-voting-details 命令
```bash
./main dpos validator-voting-details [flags]

Flags:
  --chain-id uint     链ID（必需）
  --validator string   验证者地址（必需）
  --jsonrpc string    JSON-RPC服务器地址（默认：http://localhost:8545）
  --data-dir string   数据目录（可选）
  --json             以JSON格式输出
```

### epoch 命令
```bash
./main dpos epoch [flags]

Flags:
  --chain-id uint     链ID（必需）
  --current           查询当前Epoch（默认）
  --number uint       查询指定Epoch编号（0表示当前Epoch）
  --jsonrpc string    JSON-RPC服务器地址（默认：http://localhost:8545）
  --data-dir string   数据目录（可选）
  --json             以JSON格式输出
```

## 使用示例

### 示例1：查询所有投票者并计算总投票权重

```bash
# 查询并保存到文件
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json > staking_info.json

# 使用 jq 提取投票者信息
jq '.stakingInfo[] | select(.delegate != null) | {staker: .staker, delegate: .delegate, amount: .amountWei}' staking_info.json

# 计算总投票权重
jq '[.stakingInfo[] | select(.delegate != null) | .amountWei | tonumber] | add' staking_info.json
```

### 示例2：查询特定投票者的权重占比

```bash
# 查询所有投票信息
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json > staking_info.json

# 提取目标投票者的权重
TARGET_ADDR="0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
TARGET_POWER=$(jq -r ".stakingInfo[] | select(.staker == \"$TARGET_ADDR\" and .delegate != null) | .amountWei" staking_info.json | head -1)

# 计算总投票权重
TOTAL_POWER=$(jq '[.stakingInfo[] | select(.delegate != null) | .amountWei | tonumber] | add' staking_info.json)

# 计算占比
RATIO=$(echo "scale=2; $TARGET_POWER * 100 / $TOTAL_POWER" | bc)
echo "投票者权重占比: $RATIO%"
```

## 注意事项

1. **确保节点在线**：确保 JSON-RPC 服务器地址正确且节点在线
2. **安装 jq**：如果使用 JSON 输出和 jq 解析，需要安装 jq 工具
   ```bash
   # Ubuntu/Debian
   sudo apt-get install jq
   
   # macOS
   brew install jq
   ```
3. **链ID**：确保使用正确的链ID（当前为 100）
4. **Wei 转换**：1 VCITY = 10^18 Wei，计算时注意精度

