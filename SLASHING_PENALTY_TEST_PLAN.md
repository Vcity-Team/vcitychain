# DPoS 削减惩罚机制测试计划

## 一、测试环境准备

### 1.1 测试配置
- **漏块率阈值**: `dpos_missed_blocks_percentage: 1000` (10%)
- **轻度违规削减率**: `dpos_minor_offense_slash_rate: 50` (0.5%)
- **严重违规削减率**: `dpos_severe_offense_slash_rate: 1000` (10%)
- **Epoch 时长**: `dpos_epoch_duration: 300s` (5分钟)
- **区块时间**: `block_time_s: 3` (3秒)

### 1.2 测试账户准备
- **验证者账户**: 至少3个验证者节点
- **投票者账户**: 至少5个投票者账户，每个账户有足够的代币
- **测试代币**: 每个投票者至少准备 10000 VCITY (10000000000000000000000 wei)

### 1.3 测试工具
- CLI 命令行工具 (`./main dpos`)
- JSON-RPC 客户端 (可选，用于高级查询)
- 区块浏览器或查询工具
- 日志监控工具

---

## 二、测试用例分类

### 测试类别 A: 轻度违规削减测试

#### A1. 基础漏块削减测试
**目标**: 验证当验证者漏块率超过阈值时，能正确触发削减

**前置条件**:
1. 启动至少3个验证者节点
2. 至少2个投票者向同一个验证者投票
3. 验证者初始 VotingPower > 0

**测试步骤**:
1. 记录验证者初始状态:
   ```bash
   # 查询验证者初始 VotingPower
   ./main dpos validator-voting-details \
     --chain-id 100 \
     --validator 0x验证者地址 \
     --jsonrpc http://localhost:8545
   ```

2. 记录投票者初始投票金额:
   ```bash
   # 查询投票者质押信息
   ./main dpos voting-staking-info \
     --chain-id 100 \
     --jsonrpc http://localhost:8545 \
     --json | jq '.stakingInfo[] | select(.staker == "0x投票者地址" and .delegate == "0x验证者地址")'
   ```

3. 模拟验证者漏块:
   - 停止验证者节点（或使其无法出块）
   - 等待一个完整的 epoch (300秒)
   - 确保漏块率 >= 10% (missed_blocks_percentage)

4. 等待 epoch 结束，触发故障检测

5. 验证削减结果:
   - 验证者 VotingPower 应减少 0.5%
   - 每个投票者的投票金额应减少 0.5%
   - 验证削减历史记录

**预期结果**:
- ✅ 验证者 VotingPower 正确减少
- ✅ 投票者投票金额按比例减少
- ✅ 削减历史记录正确保存
- ✅ 日志中显示削减执行信息

**验证命令**:
```bash
# 1. 验证削减后的 VotingPower
./main dpos validator-voting-details \
  --chain-id 100 \
  --validator 0x验证者地址 \
  --jsonrpc http://localhost:8545

# 2. 验证投票者削减信息（通过 dpos vote 查询，amount=0）
./main dpos vote \
  --chain-id 100 \
  --voter 0x投票者地址 \
  --candidate 0x验证者地址 \
  --amount 0 \
  --jsonrpc http://localhost:8545

# 3. 查询验证者故障信息（通过 validator-voting-details 查看故障标志）
./main dpos validator-voting-details \
  --chain-id 100 \
  --validator 0x验证者地址 \
  --jsonrpc http://localhost:8545 \
  --json | jq '.validator.faultFlag'
```

---

#### A2. 边界条件测试 - 刚好达到阈值
**目标**: 验证漏块率刚好等于阈值时的行为

**测试步骤**:
1. 计算刚好达到 10% 漏块率的场景
   - 假设 epoch 内预期出块数 = 100
   - 漏块数 = 10 (刚好 10%)
2. 模拟验证者漏块 10 个
3. 等待 epoch 结束
4. 验证是否触发削减

**预期结果**:
- ✅ 当漏块率 = 10% 时，应触发削减
- ✅ 削减金额 = VotingPower × 0.5%

---

#### A3. 边界条件测试 - 略低于阈值
**目标**: 验证漏块率略低于阈值时不触发削减

**测试步骤**:
1. 模拟验证者漏块，但漏块率 < 10% (例如 9.9%)
2. 等待 epoch 结束
3. 验证不触发削减

**预期结果**:
- ✅ 漏块率 < 10% 时，不触发削减
- ✅ VotingPower 保持不变
- ✅ 无削减历史记录

---

#### A4. 多投票者场景测试
**目标**: 验证多个投票者向同一验证者投票时，削减按比例正确分配

**测试步骤**:
1. 准备3个投票者:
   - 投票者A: 投票 5000 VCITY
   - 投票者B: 投票 3000 VCITY
   - 投票者C: 投票 2000 VCITY
   - 总计: 10000 VCITY
2. 触发验证者漏块削减
3. 验证每个投票者的削减金额

**预期结果**:
- ✅ 投票者A削减: 5000 × 0.5% = 25 VCITY
- ✅ 投票者B削减: 3000 × 0.5% = 15 VCITY
- ✅ 投票者C削减: 2000 × 0.5% = 10 VCITY
- ✅ 验证者总削减: 50 VCITY
- ✅ 削减后 VotingPower = 9950 VCITY

---

#### A5. 连续削减测试
**目标**: 验证验证者连续多个 epoch 漏块时的累积削减

**测试步骤**:
1. 第一个 epoch: 触发削减，VotingPower 减少 0.5%
2. 第二个 epoch: 继续漏块，再次触发削减
3. 验证第二次削减基于新的 VotingPower 计算

**预期结果**:
- ✅ 第一次削减: 10000 × 0.5% = 50, 剩余 9950
- ✅ 第二次削减: 9950 × 0.5% = 49.75, 剩余 9900.25
- ✅ 削减历史记录包含两次削减记录

---

### 测试类别 B: 严重违规削减测试（双重签名）

#### B1. 双重签名检测与削减
**目标**: 验证检测到双重签名时，立即触发严重削减

**前置条件**:
1. 验证者节点正常运行
2. 至少1个投票者向验证者投票

**测试步骤**:
1. 记录验证者初始 VotingPower
2. 模拟双重签名:
   - 方法1: 在同一高度签名两个不同的区块
   - 方法2: 在相邻高度签名冲突的区块哈希
3. 等待区块验证流程
4. 验证立即触发削减

**预期结果**:
- ✅ 检测到双重签名
- ✅ 立即触发削减，削减率 = 10%
- ✅ VotingPower 减少 10%
- ✅ 削减历史记录 reason 包含 "Severe Offense: double signing"
- ✅ FaultFlagInfo 中 DoubleSigningHeight 字段正确记录

**验证命令**:
```bash
# 查询验证者故障信息，应包含 doubleSigningHeight
./main dpos validator-voting-details \
  --chain-id 100 \
  --validator 0x验证者地址 \
  --jsonrpc http://localhost:8545 \
  --json | jq '.validator.faultFlag'
```

---

#### B2. 双重签名后的连续削减
**目标**: 验证双重签名后，如果继续漏块，是否还会触发轻度削减

**测试步骤**:
1. 触发双重签名，执行严重削减 (10%)
2. 在后续 epoch 中继续漏块
3. 验证是否还会触发轻度削减

**预期结果**:
- ✅ 双重签名削减后，VotingPower 减少 10%
- ✅ 后续漏块仍可触发轻度削减 (0.5%)
- ✅ 两次削减都正确记录

---

### 测试类别 C: 数据一致性测试

#### C1. VotingPower 与投票金额一致性
**目标**: 验证削减后，验证者的 VotingPower 等于所有投票者投票金额之和

**测试步骤**:
1. 记录削减前:
   - 验证者 VotingPower
   - 所有投票者的投票金额总和
2. 触发削减
3. 验证削减后:
   - 验证者 VotingPower
   - 所有投票者的投票金额总和
4. 验证: VotingPower == Σ(投票金额)

**预期结果**:
- ✅ 削减前: VotingPower == Σ(投票金额)
- ✅ 削减后: VotingPower == Σ(投票金额)
- ✅ 数据一致性保持

---

#### C2. StakeInfo 与 VoterInfo 一致性
**目标**: 验证 StakeInfo.Amount 与 VoterInfo.DelegateVotes 一致

**测试步骤**:
1. 查询 StakeInfo 中某个投票者对验证者的投票金额
2. 查询 VoterInfo 中该投票者对同一验证者的投票金额
3. 验证两者一致

**预期结果**:
- ✅ StakeInfo.Amount == VoterInfo.DelegateVotes[validator]
- ✅ 削减后两者仍保持一致

---

#### C3. OriginalAmount 字段测试
**目标**: 验证 OriginalAmount 字段正确保存初始投票金额

**测试步骤**:
1. 投票者首次投票 10000 VCITY
2. 触发削减，金额变为 9950 VCITY
3. 再次触发削减，金额变为 9900.25 VCITY
4. 验证 OriginalAmount 始终为 10000 VCITY

**预期结果**:
- ✅ OriginalAmount = 10000 VCITY (不变)
- ✅ Amount = 9900.25 VCITY (削减后)
- ✅ OriginalAmount 在首次投票时设置，后续不变

---

### 测试类别 D: 削减历史记录测试

#### D1. SlashingRecord 记录完整性
**目标**: 验证每次削减都正确记录详细信息

**测试步骤**:
1. 触发一次削减
2. 查询削减历史记录
3. 验证记录包含所有必要字段

**预期结果**:
- ✅ 记录包含: validatorAddr, blockNumber, epochNumber, timestamp
- ✅ 记录包含: slashAmount, oldVoteAmount, newVoteAmount, slashRate
- ✅ 记录包含: reason, missedBlocks, missedBlocksPercentage
- ✅ 对于双重签名，记录包含: doubleSigningHeight

**验证命令**:
```bash
# 查询投票者削减历史（通过 dpos vote amount=0 查询）
./main dpos vote \
  --chain-id 100 \
  --voter 0x投票者地址 \
  --candidate 0x验证者地址 \
  --amount 0 \
  --jsonrpc http://localhost:8545 \
  --json | jq '.slashingHistory'
```

---

#### D2. SlashingHistory 聚合记录
**目标**: 验证验证者级别的削减历史记录

**测试步骤**:
1. 触发多次削减
2. 查询验证者的削减历史
3. 验证历史记录按时间排序

**预期结果**:
- ✅ 每次削减都有对应的 SlashingHistory 记录
- ✅ 记录包含验证者级别的汇总信息
- ✅ 记录按时间顺序排列

---

### 测试类别 E: RPC 接口测试

#### E1. dpos_vote 解质押信息查询
**目标**: 验证通过 dpos_vote (amount=0) 查询削减信息

**测试步骤**:
1. 投票者向验证者投票
2. 触发削减
3. 调用 dpos vote 查询:
   ```bash
   ./main dpos vote \
     --chain-id 100 \
     --voter 0x投票者地址 \
     --candidate 0x验证者地址 \
     --amount 0 \
     --jsonrpc http://localhost:8545
   ```

**预期结果**:
- ✅ 返回 UnvoteResponse 结构
- ✅ 包含: withdrawAmount, originalAmount, totalSlashAmount
- ✅ 包含: slashCount, slashingHistory
- ✅ 如果发生削减，message 字段包含削减说明

---

#### E2. 削减信息展示测试
**目标**: 验证削减信息的可读性和准确性

**测试步骤**:
1. 触发削减
2. 查询削减信息
3. 验证返回的数据格式和内容

**预期结果**:
- ✅ 金额以字符串形式返回（避免精度丢失）
- ✅ 削减历史按时间倒序排列
- ✅ 削减原因清晰可读
- ✅ 削减率以基点形式显示

---

### 测试类别 F: 边界和异常测试

#### F1. 零 VotingPower 测试
**目标**: 验证 VotingPower 为 0 时不触发削减

**测试步骤**:
1. 验证者 VotingPower = 0
2. 尝试触发削减
3. 验证不执行削减

**预期结果**:
- ✅ VotingPower = 0 时，跳过削减
- ✅ 日志中显示警告信息

---

#### F2. 削减后 VotingPower 归零测试
**目标**: 验证削减后 VotingPower 可能归零的情况

**测试步骤**:
1. 验证者 VotingPower 很小（例如 100 VCITY）
2. 触发严重削减 (10%)
3. 验证削减后 VotingPower = 90 VCITY
4. 继续削减直到接近 0

**预期结果**:
- ✅ 削减正确执行
- ✅ VotingPower 可以接近 0 但不能为负
- ✅ 当 VotingPower 接近 0 时，验证者可能失去验证资格

---

#### F3. 并发削减测试
**目标**: 验证多个验证者同时触发削减时的处理

**测试步骤**:
1. 多个验证者同时漏块
2. 在同一 epoch 结束时触发多个削减
3. 验证所有削减都正确执行

**预期结果**:
- ✅ 所有削减都正确执行
- ✅ 数据一致性保持
- ✅ 无死锁或数据竞争

---

#### F4. 数据库持久化测试
**目标**: 验证削减记录在节点重启后仍然存在

**测试步骤**:
1. 触发削减
2. 验证削减记录保存到数据库
3. 重启节点
4. 查询削减历史，验证记录仍然存在

**预期结果**:
- ✅ 削减记录持久化到数据库
- ✅ 节点重启后记录仍然可查询
- ✅ 数据完整性保持

---

## 三、测试执行检查清单

### 3.1 测试前检查
- [ ] 所有节点正常启动
- [ ] 配置文件参数正确设置
- [ ] 测试账户有足够代币
- [ ] 日志级别设置为 DEBUG 或 INFO
- [ ] 区块浏览器或查询工具可用

### 3.2 测试执行记录
对每个测试用例，记录:
- [ ] 测试时间
- [ ] 测试环境（节点数量、配置参数）
- [ ] 测试步骤执行情况
- [ ] 实际结果 vs 预期结果
- [ ] 发现的问题（如有）
- [ ] 日志关键信息截图

### 3.3 测试后验证
- [ ] 所有削减记录正确保存
- [ ] 数据一致性验证通过
- [ ] 无异常错误日志
- [ ] 性能影响可接受

---

## 四、测试脚本示例

### 4.1 基础查询脚本
```bash
#!/bin/bash

# 配置
CHAIN_ID=100
RPC_URL="http://localhost:8545"
VALIDATOR_ADDR="0x..."
VOTER_ADDR="0x..."

# 查询验证者 VotingPower
echo "=== 查询验证者 VotingPower ==="
./main dpos validator-voting-details \
  --chain-id $CHAIN_ID \
  --validator $VALIDATOR_ADDR \
  --jsonrpc $RPC_URL \
  --json | jq '.validator.votingPower'

# 查询投票者削减信息
echo "=== 查询投票者削减信息 ==="
./main dpos vote \
  --chain-id $CHAIN_ID \
  --voter $VOTER_ADDR \
  --candidate $VALIDATOR_ADDR \
  --amount 0 \
  --jsonrpc $RPC_URL \
  --json | jq .

# 查询验证者故障信息
echo "=== 查询验证者故障信息 ==="
./main dpos validator-voting-details \
  --chain-id $CHAIN_ID \
  --validator $VALIDATOR_ADDR \
  --jsonrpc $RPC_URL \
  --json | jq '.validator.faultFlag'
```

### 4.2 削减监控脚本
```bash
#!/bin/bash

# 配置
CHAIN_ID=100
RPC_URL="http://localhost:8545"
VALIDATOR_ADDR="0x..."

# 持续监控削减事件
while true; do
  echo "=== $(date) ==="
  
  # 查询验证者状态和 VotingPower
  echo "验证者 VotingPower:"
  ./main dpos validator-voting-details \
    --chain-id $CHAIN_ID \
    --validator $VALIDATOR_ADDR \
    --jsonrpc $RPC_URL \
    --json | jq '.validator.votingPower'
  
  # 查询故障信息
  echo "故障信息:"
  ./main dpos validator-voting-details \
    --chain-id $CHAIN_ID \
    --validator $VALIDATOR_ADDR \
    --jsonrpc $RPC_URL \
    --json | jq '.validator.faultFlag'
  
  # 检查日志中的削减信息
  tail -n 100 node.log | grep -i "削减\|slashing" | tail -n 5
  
  sleep 10
done
```

---

## 五、预期问题与排查

### 5.1 常见问题
1. **削减未触发**
   - 检查漏块率是否真的 >= 阈值
   - 检查 epoch 是否正常结束
   - 检查日志中的故障检测信息

2. **削减金额不正确**
   - 检查削减率配置是否正确
   - 验证计算逻辑（基点转换）
   - 检查 VotingPower 是否为 0

3. **数据不一致**
   - 检查数据库事务是否正确提交
   - 验证更新操作的原子性
   - 检查是否有并发问题

### 5.2 日志关键词
监控以下日志关键词:
- `🔨 开始执行消减` / `executeSlashing`
- `🔨 已执行轻度违规削减`
- `🚨 检测到双签并执行削减`
- `✅ 消减执行完成`
- `Failed to execute slashing`

---

## 六、测试报告模板

### 6.1 测试结果汇总
| 测试类别 | 测试用例 | 状态 | 备注 |
|---------|---------|------|------|
| A | A1. 基础漏块削减 | ✅/❌ | |
| A | A2. 边界条件-达到阈值 | ✅/❌ | |
| A | A3. 边界条件-低于阈值 | ✅/❌ | |
| A | A4. 多投票者场景 | ✅/❌ | |
| A | A5. 连续削减 | ✅/❌ | |
| B | B1. 双重签名削减 | ✅/❌ | |
| B | B2. 双重签名后连续削减 | ✅/❌ | |
| C | C1. VotingPower一致性 | ✅/❌ | |
| C | C2. StakeInfo一致性 | ✅/❌ | |
| C | C3. OriginalAmount字段 | ✅/❌ | |
| D | D1. SlashingRecord完整性 | ✅/❌ | |
| D | D2. SlashingHistory聚合 | ✅/❌ | |
| E | E1. dpos_vote查询 | ✅/❌ | |
| E | E2. 削减信息展示 | ✅/❌ | |
| F | F1. 零VotingPower | ✅/❌ | |
| F | F2. VotingPower归零 | ✅/❌ | |
| F | F3. 并发削减 | ✅/❌ | |
| F | F4. 数据库持久化 | ✅/❌ | |

### 6.2 问题记录
| 问题编号 | 测试用例 | 问题描述 | 严重程度 | 状态 |
|---------|---------|---------|---------|------|
| | | | | |

---

## 七、测试完成标准

### 7.1 功能完整性
- ✅ 所有测试用例执行完成
- ✅ 核心功能（轻度/严重削减）验证通过
- ✅ 数据一致性验证通过
- ✅ RPC 接口功能正常

### 7.2 数据准确性
- ✅ 削减金额计算正确
- ✅ 削减历史记录完整
- ✅ 数据持久化正常

### 7.3 稳定性
- ✅ 无严重错误或崩溃
- ✅ 并发场景下数据一致性保持
- ✅ 节点重启后数据完整

---

## 八、测试时间估算

| 测试类别 | 预计时间 |
|---------|---------|
| 环境准备 | 30分钟 |
| 轻度违规削减测试 (A1-A5) | 2小时 |
| 严重违规削减测试 (B1-B2) | 1小时 |
| 数据一致性测试 (C1-C3) | 1小时 |
| 削减历史测试 (D1-D2) | 30分钟 |
| RPC接口测试 (E1-E2) | 30分钟 |
| 边界异常测试 (F1-F4) | 1小时 |
| 问题修复与复测 | 2小时 |
| **总计** | **约8小时** |

---

## 九、注意事项

1. **测试环境隔离**: 建议在测试网络进行，避免影响主网
2. **数据备份**: 测试前备份重要数据，便于恢复
3. **日志收集**: 保持详细日志，便于问题排查
4. **逐步验证**: 先验证基础功能，再测试复杂场景
5. **性能监控**: 关注削减操作对系统性能的影响

---

**测试计划版本**: v1.0  
**创建日期**: 2024  
**最后更新**: 2024

