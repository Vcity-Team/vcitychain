# 查询指定地址投票详情的 CLI 命令

## 1. 查询验证者的投票详情（如果地址是验证者）

如果指定地址是验证者，使用 `validator-voting-details` 命令查询：

```bash
# 基本用法
./main dpos validator-voting-details --chain-id 100 --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 --jsonrpc http://209.53.43.252:9545

# 使用 JSON 输出格式
./main dpos validator-voting-details --chain-id 100 --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 --jsonrpc http://209.53.43.252:9545 --json
```

**输出内容**：
- 验证者地址
- 投票权重（Voting Power）
- 是否激活（Is Active）
- 投票给该验证者的总投票权重（Total Staked to Me）
- 投票者数量（Stake Count）
- 所有投票者列表（Stakes）
- 该验证者投票给其他验证者的信息（My Votes）

**参数说明**：
- `--chain-id`: 链ID（必需）
- `--validator`: 验证者地址（必需）
- `--jsonrpc`: JSON-RPC服务器地址（默认：http://localhost:8545）
- `--data-dir`: 数据目录（可选）
- `--json`: 以JSON格式输出（可选）

## 2. 查询投票者的投票详情（如果地址是投票者）

如果指定地址是投票者，使用 `voting-staking-info` 命令并过滤：

```bash
# 查询所有投票信息并过滤特定投票者
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json | jq '.stakingInfo[] | select(.staker == "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127")'
```

**输出内容**：
- 投票者地址（staker）
- 投票给哪个验证者（delegate）
- 投票权重（amountWei）
- 开始时间（startTime）
- 结束时间（endTime）
- 是否锁定（isLocked）
- 是否激活（isActive）
- 奖励（rewards）

## 3. 综合查询脚本（自动判断是验证者还是投票者）

```bash
#!/bin/bash

CHAIN_ID=100
RPC_URL="http://209.53.43.252:9545"
TARGET_ADDR="0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"

echo "========== 查询地址投票详情 =========="
echo "目标地址: $TARGET_ADDR"
echo ""

# 1. 先尝试作为验证者查询
echo "尝试作为验证者查询..."
VALIDATOR_RESULT=$(./main dpos validator-voting-details --chain-id $CHAIN_ID --validator $TARGET_ADDR --jsonrpc $RPC_URL --json 2>/dev/null)

if [ $? -eq 0 ] && echo "$VALIDATOR_RESULT" | jq -e '.validator.address' > /dev/null 2>&1; then
    echo "✅ 该地址是验证者"
    echo ""
    echo "========== 验证者信息 =========="
    echo "$VALIDATOR_RESULT" | jq '.validator | {
        address: .address,
        votingPower: .votingPower,
        isActive: .isActive,
        totalStakedToMe: .totalStakedToMe,
        stakeCount: .stakeCount
    }'
    
    echo ""
    echo "========== 投票给该验证者的投票者列表 =========="
    echo "$VALIDATOR_RESULT" | jq '.validator.stakes[]? | {
        staker: .staker,
        amountWei: .amountWei,
        amountEther: .amountEther
    }'
    
    echo ""
    echo "========== 该验证者投票给其他验证者的信息 =========="
    echo "$VALIDATOR_RESULT" | jq '.validator.myVotes[]? | {
        delegate: .delegate,
        amountWei: .amountWei,
        amountEther: .amountEther
    }'
else
    echo "该地址不是验证者，尝试作为投票者查询..."
    
    # 2. 作为投票者查询
    STAKING_INFO=$(./main dpos voting-staking-info --chain-id $CHAIN_ID --jsonrpc $RPC_URL --json 2>/dev/null)
    
    if [ $? -eq 0 ]; then
        VOTER_INFO=$(echo "$STAKING_INFO" | jq ".stakingInfo[] | select(.staker == \"$TARGET_ADDR\")")
        
        if [ -n "$VOTER_INFO" ] && [ "$VOTER_INFO" != "null" ]; then
            echo "✅ 该地址是投票者"
            echo ""
            echo "========== 投票者信息 =========="
            echo "$VOTER_INFO" | jq '{
                staker: .staker,
                delegate: .delegate,
                amountWei: .amountWei,
                amountEther: .amountEther,
                startTime: .startTime,
                endTime: .endTime,
                isLocked: .isLocked,
                isActive: .isActive,
                rewardsWei: .rewardsWei,
                rewardsEther: .rewardsEther
            }'
        else
            echo "❌ 未找到该地址的投票信息"
        fi
    else
        echo "❌ 查询失败"
    fi
fi
```

## 4. 使用 curl 直接调用 JSON-RPC

### 方法1：查询验证者投票详情

```bash
curl -X POST http://209.53.43.252:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getValidatorVotingDetails",
    "params": ["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],
    "id": 1
  }' | jq '.result.validator'
```

### 方法2：查询所有质押信息并过滤

```bash
# 查询所有质押信息
curl -X POST http://209.53.43.252:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getStakingInfo",
    "params": ["latest"],
    "id": 1
  }' | jq '.result[] | select(.staker == "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127" or .delegate == "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127")'
```

## 5. 详细查询脚本（包含权重占比计算）

```bash
#!/bin/bash

CHAIN_ID=100
RPC_URL="http://209.53.43.252:9545"
TARGET_ADDR="0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"

echo "========== 查询地址投票详情 =========="
echo "目标地址: $TARGET_ADDR"
echo ""

# 查询所有质押信息
STAKING_INFO=$(./main dpos voting-staking-info --chain-id $CHAIN_ID --jsonrpc $RPC_URL --json)

# 1. 查询作为投票者的信息
echo "========== 作为投票者的信息 =========="
VOTER_ENTRIES=$(echo "$STAKING_INFO" | jq ".stakingInfo[] | select(.staker == \"$TARGET_ADDR\" and .delegate != null)")

if [ -n "$VOTER_ENTRIES" ] && [ "$VOTER_ENTRIES" != "null" ]; then
    echo "$VOTER_ENTRIES" | jq -s '.[] | {
        投票者地址: .staker,
        投票给: .delegate,
        投票权重: .amountWei,
        投票权重_VCITY: .amountEther,
        开始时间: .startTime,
        结束时间: .endTime,
        是否锁定: .isLocked,
        是否激活: .isActive
    }'
    
    # 计算总投票权重
    TOTAL_POWER=$(echo "$STAKING_INFO" | jq '[.stakingInfo[] | select(.delegate != null) | .amountWei | tonumber] | add')
    
    # 计算该投票者的总权重
    VOTER_TOTAL_POWER=$(echo "$VOTER_ENTRIES" | jq '[.amountWei | tonumber] | add')
    
    # 计算占比
    RATIO=$(echo "scale=2; $VOTER_TOTAL_POWER * 100 / $TOTAL_POWER" | bc)
    
    echo ""
    echo "该投票者总权重: $VOTER_TOTAL_POWER Wei = $(echo "scale=18; $VOTER_TOTAL_POWER / 1000000000000000000" | bc) VCITY"
    echo "全网总投票权重: $TOTAL_POWER Wei = $(echo "scale=18; $TOTAL_POWER / 1000000000000000000" | bc) VCITY"
    echo "权重占比: $RATIO%"
    
    # 计算预期奖励
    REWARD_AMOUNT=100000000000000000000  # 100 VCITY
    VOTER_RATIO=30  # 30%
    VOTER_REWARD_POOL=$((REWARD_AMOUNT * VOTER_RATIO / 100))
    EXPECTED_REWARD=$(echo "scale=0; $VOTER_REWARD_POOL * $VOTER_TOTAL_POWER / $TOTAL_POWER" | bc)
    echo "预期投票者奖励: $EXPECTED_REWARD Wei = $(echo "scale=18; $EXPECTED_REWARD / 1000000000000000000" | bc) VCITY"
else
    echo "该地址不是投票者"
fi

echo ""

# 2. 查询作为验证者的信息
echo "========== 作为验证者的信息 =========="
VALIDATOR_RESULT=$(./main dpos validator-voting-details --chain-id $CHAIN_ID --validator $TARGET_ADDR --jsonrpc $RPC_URL --json 2>/dev/null)

if [ $? -eq 0 ] && echo "$VALIDATOR_RESULT" | jq -e '.validator.address' > /dev/null 2>&1; then
    echo "$VALIDATOR_RESULT" | jq '.validator | {
        验证者地址: .address,
        投票权重: .votingPower,
        是否激活: .isActive,
        投票给我的总权重: .totalStakedToMe,
        投票给我的总权重_VCITY: .totalStakedToMeEther,
        投票者数量: .stakeCount
    }'
    
    echo ""
    echo "========== 投票给该验证者的投票者列表 =========="
    echo "$VALIDATOR_RESULT" | jq '.validator.stakes[]? | {
        投票者: .staker,
        投票权重: .amountWei,
        投票权重_VCITY: .amountEther
    }'
else
    echo "该地址不是验证者"
fi
```

## 6. 快速查询命令（一行）

```bash
# 查询验证者详情（如果地址是验证者）
./main dpos validator-voting-details --chain-id 100 --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 --jsonrpc http://209.53.43.252:9545 --json

# 查询投票者详情（如果地址是投票者）
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545 --json | jq '.stakingInfo[] | select(.staker == "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127")'

# 使用 curl 查询验证者详情
curl -X POST http://209.53.43.252:9545 -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"dpos_getValidatorVotingDetails","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}' | jq '.result.validator'
```

## 命令参数总结

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

### voting-staking-info 命令
```bash
./main dpos voting-staking-info [flags]

Flags:
  --chain-id uint     链ID（必需）
  --jsonrpc string    JSON-RPC服务器地址（默认：http://localhost:8545）
  --data-dir string   数据目录（可选）
  --json             以JSON格式输出
```

## 输出字段说明

### 验证者详情输出字段
- `address`: 验证者地址
- `votingPower`: 验证者投票权重
- `isActive`: 是否激活
- `totalStakedToMe`: 投票给该验证者的总投票权重（Wei）
- `totalStakedToMeEther`: 投票给该验证者的总投票权重（VCITY）
- `stakeCount`: 投票者数量
- `stakes`: 投票者列表（包含每个投票者的地址和投票权重）
- `totalVotedByMe`: 该验证者投票给其他验证者的总权重
- `myVotes`: 该验证者投票给其他验证者的列表

### 投票者详情输出字段
- `staker`: 投票者地址
- `delegate`: 投票给哪个验证者
- `amountWei`: 投票权重（Wei）
- `amountEther`: 投票权重（VCITY）
- `startTime`: 开始时间
- `endTime`: 结束时间
- `isLocked`: 是否锁定
- `isActive`: 是否激活
- `rewardsWei`: 奖励（Wei）
- `rewardsEther`: 奖励（VCITY）

## 使用示例

### 示例1：查询验证者详情

```bash
./main dpos validator-voting-details \
  --chain-id 100 \
  --validator 0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127 \
  --jsonrpc http://209.53.43.252:9545 \
  --json | jq '.validator | {
    address: .address,
    votingPower: .votingPower,
    totalStakedToMe: .totalStakedToMe,
    stakeCount: .stakeCount,
    stakes: .stakes
  }'
```

### 示例2：查询投票者详情

```bash
./main dpos voting-staking-info \
  --chain-id 100 \
  --jsonrpc http://209.53.43.252:9545 \
  --json | jq '.stakingInfo[] | select(.staker == "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127") | {
    staker: .staker,
    delegate: .delegate,
    amountWei: .amountWei,
    amountEther: .amountEther
  }'
```

## 注意事项

1. **地址格式**：确保地址格式正确（42字符，以0x开头）
2. **链ID**：确保使用正确的链ID（当前为100）
3. **节点在线**：确保JSON-RPC服务器地址正确且节点在线
4. **安装jq**：如果使用JSON输出和jq解析，需要安装jq工具
5. **权限**：某些查询可能需要节点有相应的权限

