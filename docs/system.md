# VCityChain DPOS 系统说明

本文档旨在全面介绍 VCity Chain 的 DPoS（委托权益证明）共识模块，涵盖投票、经济系统、治理、故障处理等核心机制。

---

## **一、概述**

### **1.1 DPoS 简介**

- DPoS 共识机制的基本概念
- VCity Chain 中 DPoS 的特点和优势
- 模块架构概述

### **1.2 核心概念**

- 验证者（Validator/Delegate）
- 投票者（Voter）
- 候选人（Candidate/SR）
- 质押（Stake）
- 权重（Voting Power）

### **1.3 Epoch 机制**

- **Epoch 概念**：
- 固定时间窗口（默认 24 小时，可通过配置修改）
- 每个 epoch 包含固定数量的区块

  - **Epoch 大小计算**：`epochSize = epochDuration / blockTime`

    - 例如：epochDuration = 24 小时 = 86400 秒，blockTime = 3 秒，则 epochSize = 28800 个区块
    - 代码实现：`getEpochSize() = uint64(EpochDuration / BlockTime)`
- Epoch 是系统中奖励分配、故障检测、状态更新的基本时间单位
- **Epoch 计算**：
- Epoch 编号从共识切换高度（`ConsensusSwitchHeight`）开始计算
- 公式：`currentEpoch = ((blockNumber - consensusSwitchHeight) / epochSize) + 1`
- 第一个 DPoS 区块为 Epoch 1
- **Epoch 配置参数**：
- `EpochDuration`（epoch 时长）：

  - 代码读取：`d.config.EpochDuration`
  - YAML 配置字段：`epochDuration` 或 `dpos_epoch_duration`（类型：string，如"24h"）
  - 默认值：24 小时（1 天）
- `BlockTime`（区块时间）：

  - 代码读取：`d.config.BlockTime.Duration`
  - YAML 配置字段：`blockTime` 或 `block_time_s`（类型：string 如"2s"，或 uint64 秒数）
  - 默认值：3 秒
- **Epoch 边界的重要性**：
- 系统在 epoch 边界执行关键操作：

  - 计算和分配上一个 epoch 的奖励
  - 检测验证者故障
  - 更新验证者集合（投票权重变化生效）
  - 执行已通过的治理提案调度
- 这种设计确保了状态更新的原子性和一致性

---

## **二、出块机制与分叉处理 **

### **2.1 Slot 机制概述**

- **Slot 概念**：
- Slot 是固定时间窗口，每个验证者在自己的 slot 内出块
- 基于时间的绝对计算，不依赖区块号索引
- 基于固定时间窗口的 slot 机制，确保出块时间的精确性和可预测性
- **Slot 与区块的关系**：
- 每个 slot 对应一个区块生产机会
- 正常情况下，每个 slot 应该产生一个区块
- 如果某个验证者故障，会跳过该 slot，下一个验证者继续
- 如果某个 slot 内出块耗时超出 slot，那么立即截断结束出块轮转到下一个

### **2.2 Slot 计算机制**

- **Slot 计算公式**：
- `currentSlot = (当前时间 - 创世时间) / blockWindow`
- 例如：

  - 创世时间：2024-01-01 00:00:00
  - 当前时间：2024-01-01 00:00:12
  - blockWindow：3 秒
  - 则：`currentSlot = 12秒 / 3秒 = ` 4
- **创世时间（GenesisTime）的确定**：
- DPoS 的创世时间 = 共识切换前一个区块的时间戳
- 例如：共识切换高度为 7370，则使用区块 7369 的时间戳作为 DPoS 创世时间
- 代码实现：`genesisTime = consensusSwitchHeight前一个区块的Timestamp`
- 这样确保 DPoS 时间计算的连续性
- **区块时间窗口（BlockWindow）**：
- 代码读取：`d.config.BlockTime.Duration`
- 默认值：3 秒
- 每个 slot 的持续时间，验证者应在此窗口内完成出块

### **2.3 出块者选择机制**

- **基于 Slot 的出块者计算**：
- 公式：`expectedValidatorIndex = currentSlot % validatorCount`
- 例如：

  - 当前 slot = 100
  - 验证者数量 = 21
  - 则：`expectedValidatorIndex = 100 % 21 = 16`
  - 验证者列表中的第 16 个验证者（索引从 0 开始）应该出块
- **验证者列表排序规则**：
- 按投票权重从高到低排序
- 权重相同时，按地址字典序（升序）排序
- 确保所有节点计算的出块者完全一致
- **出块时间窗口检查**：

  - **Slot 开始时间**：`slotStart = genesisTime + (currentSlot × blockWindow)`
  - **Slot 结束时间**：`slotEnd = slotStart + blockWindow`
  - **检查逻辑**：

    - 如果当前时间 < slotStart：还未到出块时间，等待
    - 如果当前时间在 slotStart 和 slotEnd 之间：可以出块
    - 如果当前时间 > slotEnd（带容忍度 500ms）：时间窗口已过，跳过出块
- **出块者确定流程**：

1. 计算当前 slot：`currentSlot = (now - genesisTime) / blockWindow`
2. 计算应该出块的验证者索引：`index = currentSlot % validatorCount`
3. 从验证者列表中获取验证者地址
4. 检查当前节点地址是否匹配
5. 检查是否在时间窗口内
6. 如果都满足，开始构建区块

### **2.4 防分叉机制 **

- **出块前的分叉检测**：

  - **检查父区块是否变化**：

    - 在提交区块前，检查当前链头是否还是预期的父区块
    - 如果父区块已变化（其他节点已出块），丢弃当前构建的区块
    - 代码逻辑：`if currentHeader.Hash != block.Header.ParentHash { 丢弃区块 }`
  - **防止同一 slot 重复出块**：

    - 记录 `lastProducedSlot`，如果当前 slot 已经出过块，跳过
    - 避免同一验证者在同一 slot 内出多个区块
- **链重组（Reorg）处理**：

  - **分叉检测**：

    - 当收到新区块时，计算其总难度（Total Difficulty）
    - 比较新区块的总难度与当前链头的总难度
  - **难度计算机制**：

    - **DPoS 难度设置**：

      - 所有区块的 Difficulty 固定设置为 1（`header.Difficulty = 1`）
      - 代码实现：`h.Difficulty = 1`（在 `block_builder.go` 和 `dpos.go` 中）
      - 与 POW 不同，DPoS 不需要动态调整难度
    - **总难度计算**：

      - 公式：`总难度 = 父区块总难度 + 当前区块难度`
      - 由于每个区块难度都是 1，总难度实际上等于从创世区块到当前区块的区块数量
      - 代码实现：`incomingTD = parentTD + header.Difficulty`
  - **分叉解决规则**：

    - **最长链规则（基于总难度）**：

      - 如果新区块链的总难度 > 当前链头的总难度，触发链重组
      - 由于难度固定为 1，这实际上等同于选择区块数量更多的链（最长链）
      - 新区块链成为新的主链（canonical chain）
    - **代码实现**：

      - `if incomingTD > currentTD { handleReorg() }`
      - 总难度 = 所有区块难度的累加（实际上等于区块数量）
  - **链重组流程**（`handleReorg` 函数）：

    1. 找到新旧链的共同祖先区块（通过回溯父区块哈希）
    2. 回退旧链上的区块（从当前链头到共同祖先）
    3. 应用新链上的区块（从共同祖先到新链头）
    4. 更新 canonical chain 标记（更新 canonical hash）
    5. 触发状态回滚和重放（通过 Event 机制）
  - **分叉存储**：

    - 总难度较低的链作为分叉（fork）存储到数据库
    - 保留分叉信息，便于后续查询和分析
    - 代码实现：`batchWriter.PutForks(forks)`

### **2.5 出块流程示例**

**示例场景：**

- **配置参数**：
- 创世时间：2024-01-01 00:00:00
- 区块时间窗口：3 秒
- 验证者数量：21
- 验证者列表：[A, B, C, ..., U]（按权重排序）
- **时间点 T = 2024-01-01 00:00:10**：
- 计算当前 slot：

  - `timeSinceGenesis = 12秒`
  - `currentSlot = 12 / 3 = ` 4
- 计算应该出块的验证者：

  - `expectedIndex = 4 % 21 = ` 4
  - 验证者 E（索引 4）应该出块
- 计算 slot 时间窗口：

  - `slotStart = 创世时间 + 4 × 3秒 = 00:00:12`
  - `slotEnd = 00:00:12 + 3秒 = 00:00:15`
- 验证者 E 检查：

  - 当前时间在窗口内（00:00:12），可以出块
  - 开始构建区块
- **时间点 T = 2024-01-01 00:00:12（验证者 E 故障）**：
- 验证者 E 未能在时间窗口内出块
- 当前 E 的 slot 被浪费，因为故障未能出块
- 下一个 slot（slot=6）由验证者 G（索引 6）出块
- 验证者 E 会被记录漏块，在 epoch 结束时会进行故障检测，当 E 的漏块大于门槛时，下一个 epoch 的出块者列表中将其剔除

### **2.6 分叉处理示例**

**示例场景：**

- **初始状态**：
- 链 A：区块 100（总难度 100，因为每个区块难度为 1）
- 所有节点都在链 A 上
- **分叉发生**：
- 验证者 X 和验证者 Y 同时在 slot 101 出块（时间同步误差）
- 链 B：区块 100（总难度 100）→ 区块 101-X（总难度 101）
- 链 C：区块 100（总难度 100）→ 区块 101-Y（总难度 101）

  - **注意**：每个区块难度都是 1，所以总难度 = 区块数量
- **分叉解决**：
- 节点收到区块 101-X 和 101-Y
- 两条链总难度相同（都是 101），选择先收到的区块为主链
- 假设链 B 被选择为主链（区块 101-X）
- **链重组**：
- 节点如果之前在链 C 上，需要执行链重组：

  1. **找到共同祖先**：区块 100（两条链的共同父区块）
  2. **回退旧链**：回滚区块 101-Y（从链头回退到共同祖先）
  3. **应用新链**：应用区块 101-X（从共同祖先到新链头）
  4. **更新 canonical 标记**：将区块 101-X 标记为 canonical 链
  5. **状态更新**：通过切换主链头并保证新链头已基于父状态执行，确保状态与新链一致

---

## **三、投票机制 **

### **3.1 质押门槛与参数读取**

- **SR 候选人保证金阈值** (`SRThreshold`) - 候选人保证金门槛
- 代码读取：`d.config.SRThreshold`（通过 `getDelegateDepositAmount()` 方法获取）
- YAML 配置字段：`dpos_SR_threshold`（类型：string，单位：wei）
- 默认值：0（如果未设置，则使用代码默认值 100 VCITY）
- 说明：

  - 这是成为 SR 候选人的保证金门槛
  - 用户需要质押达到此阈值才能成为候选人并接受投票
  - 如果未配置或配置为 0，系统使用默认值 100 VCITY 作为保证金
- 用途：在成为候选人时需要满足此保证金要求
- **最小质押门槛** (`MinVotingPower`) - 治理投票门槛
- 代码读取：`d.config.MinVotingPower`
- YAML 配置字段：`dpos_delegate_threshold`（类型：string，单位：wei）
- 默认值：1000000000000000000000（1000 VCITY）
- 说明：

  - 用于治理投票的最小质押门槛（`governance_min_voting_threshold` 的默认值）
  - **VCity Chain 特色设计**：参与治理提案投票需要满足此质押门槛
  - **设计目的**：提高治理投票的质量，确保参与投票的用户有一定质押，防止恶意投票
  - **注意**：在实际使用中，通常与 `dpos_SR_threshold` 使用相同值
- **投票最小金额** (`MinVoteAmount`)
- 代码常量：`MinVoteAmount = "1000000000000000000"`（1 VCITY）
- 说明：单次投票的最小金额，硬编码在代码中

### **3.2 成为候选人**

- **注册候选人的条件**：

  - **必须满足 SR 候选人保证金门槛**（`dpos_SR_threshold`）：

    - 这是成为候选人的核心条件
    - 用户需要质押达到此阈值才能成为 SR 候选人
- 候选人注册流程：

  1. 用户质押 VCITY 代币达到 `dpos_SR_threshold` 阈值
  2. 系统验证质押金额是否满足要求
  3. 注册为候选人，可以接受其他用户的投票

### **3.3 投票给验证者**

- 投票操作流程：

  1. 用户质押 VCITY 代币
  2. 调用投票接口，指定候选人地址和投票金额
  3. 系统验证投票者余额和剩余可投票数
  4. 更新候选人的投票权重（内存中立即更新）
  5. 投票信息持久化到数据库（`persistVoteToDatabase`）
- 投票权重计算：基于投票金额
- 投票锁定机制：投票后一定时间内不可撤回（VoteLockTime）
- **状态持久化** ：
- 投票信息保存到数据库，确保数据不丢失
- 新节点可通过同步区块数据恢复投票状态
- 投票历史可追溯，便于审计和分析

### **3.4 权重达到出块标准与顶替机制**

- **出块标准**：
- 根据投票权重排序
- 取前 N 名验证者（如 21 个）作为出块者
- 使用 `maxValidatorSetSize` 参数限制
- **验证者集合更新机制** ：

  - **延迟更新设计**：

    - 投票后，候选人的权重立即在内存中更新
    - 但验证者集合（出块者列表）不会立即更新
    - 系统标记 `pendingValidatorUpdate = true`，等待 epoch 边界统一生效
  - **更新时机**：

    - 在 epoch 结束区块统一更新验证者集合
    - 基于最新的投票权重排序，重新计算前 N 名验证者
    - 新的出块者列表在下一个 epoch 开始时生效
  - **更新规则**：

    - 按投票权重从高到低排序
    - 权重相同时，按地址字典序排序
    - 取前 `maxValidatorSetSize` 名作为出块者
    - 被顶替的验证者自动退出出块者列表
- **出块调度机制** ：

  - **BlockScheduler（区块调度器）**：

    - 基于固定时间窗口的出块调度
    - 每个验证者在自己分配的 slot（时间段）内出块
    - 确保出块时间的精确性和可预测性
  - **出块轮次**：

    - 验证者按权重排序后，轮流出块
    - 每个 epoch 内，验证者按顺序轮流获得出块机会
    - Slot 机制保证公平性和时间稳定性
- **顶替出块机制**：
- 新验证者权重超过现有出块者时，在下一个 epoch 自动进入出块者集合
- 原有出块者如果权重被超越，会被顶替出局
- 整个过程在 epoch 边界统一处理，保证一致性

### **3.5 投票示例**

**示例场景 1：**

- 用户 A 拥有 5000 VCITY
- 用户 B（候选人）当前权重为 0
- 用户 A 向用户 B 投票 3000 VCITY
- 用户 B 的权重变为 3000 VCITY
- 如果 3000 VCITY 足够进入前 21 名，用户 B 开始参与出块（只要配置中出块者数量是 21，如果不是，只会截取前相应的数量）

**示例场景 2**：

- 你有 10,000 VCITY，可以拆成多笔给不同候选人，比如 4,000 + 3,000 + 3,000，系统会把这三笔都累计进去，因为每次投票前都会用 totalVotedAmount 检查你已经投出去的总量。
- 当累计额度达到或超过 10,000 后，再发新的投票就会被上面的检查拦下来，返回 “insufficient remaining votable amount…”。要再投，需要先增加余额或走赎回/撤票流程把已投的额度释放出来.

**注意**：

**委托投票任何人都能投，并且可以同时给多个候选人投票（投 A、再投 B、再投 C…），总投票额不超过你的余额，否则报错。**

---

## **四、经济系统 **

### **4.1 奖励参数读取**

- **Epoch 奖励总额** (`RewardAmount`)
- 代码读取：`d.config.RewardAmount`
- YAML 配置字段：`dpos_reward_amount`（类型：string，单位：wei）
- 默认值：1000000000000000000000（1000 VCITY）
- 可通过治理提案修改（参数名：`dpos_reward_amount`）
- **区块时间** (`BlockTime`)
- 代码读取：`d.config.BlockTime.Duration`
- YAML 配置字段：`blockTime` 或 `block_time_s`（类型：string，如"2s"，或 uint64 秒数）
- 默认值：2 秒
- 用于计算预期出块数
- **Epoch 时长** (`EpochDuration`)
- 代码读取：`d.config.EpochDuration`
- YAML 配置字段：`epochDuration` 或 `dpos_epoch_duration`（类型：string，如"24h"）
- 默认值：24 小时（1 天）
- 用于计算每个 epoch 的预期区块数

### **4.2 奖励计算机制**

- **基于固定时间窗口的计算**：

  1. **计算 epoch 预期出块数**：

     - 公式：`预期出块数 = epochDuration / blockTime`
     - 例如：epochDuration = 24 小时 = 86400 秒，blockTime = 3 秒，则预期出块数 = 86400 ÷ 3 = 28800 块
     - 代码实现：`expectedBlocks = int64(epochDuration / blockTime)`
  2. **计算每块奖励**：

     - 公式：`每块奖励 = RewardAmount / 预期出块数`
     - 例如：总奖励 1000 VCITY，预期 5 块，则每块奖励 = 1000 ÷ 5 = 200 VCITY
  3. **计算验证者奖励**：

     - 公式：`验证者奖励 = 每块奖励 × 实际出块数 `
     - 例如：每块 200 VCITY，实际出块 8 块，则奖励 = 200 × 8  = 1600 VCITY
  4. **计算投票者奖励**：

     -按照每个投票者的投票比例瓜分对应验证者的奖励

     - 例如：出块者 A 获得出块奖励 1600，然后它的投票中 10% 是投票者 B 所投，假设这个时候配置文件中设置佣金比例 dpos_commission_radio:30%,那么出块者 A 的佣金收入 1600*30% 投票者 B 的收入：1600*10%*(1-30%)

### **4.3 奖励分配流程**

- **分配时机**：每个 epoch 结束区块
- **区块生产跟踪** ：

  - **BlockProductionTracker（区块生产跟踪器）**：

    - 实时记录每个验证者的出块历史
    - 统计每个 epoch 内每个验证者的实际出块数
    - 记录数据包括：验证者地址、出块时间、区块号等
  - **出块统计用途**：

    - 用于奖励计算：根据实际出块数分配验证者奖励
    - 用于故障检测：统计漏块数判断是否故障
    - 数据持久化到数据库，便于历史查询和分析
- **分配流程**：

  - 在 epoch 结束区块，调用 `distributeEpochRewards` 计算奖励
  - 从 `BlockProductionTracker` 获取每个验证者的实际出块数
  - 计算验证者和投票者的奖励（基于实际出块数）
  - 将奖励分配信息（`RewardDistributionInfo`）写入区块 ExtraData
  - 所有节点在验证区块时，从 ExtraData 读取奖励信息并执行转账
  - 从奖励账户（`RewardAccount`）扣除总奖励，分配到各个地址
- **奖励账户** (`RewardAccount`)：系统奖励账户地址，存储待分配的奖励资金

---

## **五、治理机制 **

### **5.1 治理概述**

- 提案类型：
  - 参数提案（Parameter Proposal）
  - 验证者恢复提案（Validator Recovery Proposal）

### **5.2 提案创建**

- **提案创建**：

  只有验证者可以创建提案
- **参数配置系统** ⚙️：

  - **完整的可治理参数列表**：

    - `dpos_reward_amount`：Epoch 奖励总额（类型：string，单位：wei）

      - 取值范围：1000000000000000000 ~ 1000000000000000000000000
      - 默认值：1000000000000000000000（1000 VCITY）
      - YAML 配置字段：`dpos_reward_amount`
    - `dpos_delegate_threshold`：成为验证者候选人时需要的最小质押（保证金）（string，单位：wei）

      - 取值范围：1000000000000000000 ~ 100000000000000000000000
      - 默认值：1000000000000000000000（1000 VCITY）
      - YAML 配置字段：`dpos_delegate_threshold`
  - **参数验证机制**：

    - 每个参数都有取值范围验证
    - 新值必须合法且在范围内
    - 参数修改会影响系统行为，需要社区慎重考虑
- **参数提案创建**：
- 创建流程：

  1. 验证参数是否在可投票列表中
  2. 获取当前参数值作为 `OldValue`
  3. 验证新参数值是否合法（类型、范围）
  4. 生成提案 ID 并签名（提案者使用私钥签名）
  5. **设置提案区块范围**（基于当前区块号 `currentBlock`）：

     - `StartBlock = currentBlock + 1`（投票从下一个区块开始）
     - `EndBlock = currentBlock + getVotePeriod()`（投票期结束区块）
     - `ValidEndBlock = currentBlock + getValidPeriod()`（提案有效期结束区块）
     - **投票期计算**：

       - `getVotePeriod()` = `ProposalVotePeriod / BlockTime`（转换为区块数）
       - YAML 配置字段：`dpos_proposal_vote_period`（类型：string，如"24h"）
       - 默认值：24 小时 = 28800 个区块（按 3 秒/区块计算）
     - **有效期计算**：

       - `getValidPeriod()` = `ProposalValidPeriod / BlockTime`（转换为区块数）
       - YAML 配置字段：`dpos_proposal_valid_period`（类型：string，如"7d"）
       - 默认值：7 天 = 201600 个区块（按 3 秒/区块计算）
  6. 保存提案到数据库（通过 `ProposalStore` 持久化）
  7. 提案状态初始化为 `Pending`
- **恢复提案创建**：
- 只能为故障验证者创建
- 创建前提：验证节点当前状态为 `isFaulty = true`
- 包含恢复理由（RecoveryReason），说明节点已修复的原因
- 提案执行后，故障标记在下一个 epoch 边界生效（通过调度系统）

### **5.3 提案表决**

- **投票条件**：

  - 验证者才可以参与投票
- 在投票期内（`StartBlock` 到 `EndBlock`）
- 每个地址只能投票一次
- **投票期计算说明**：
- `StartBlock`：提案创建时的区块号 + 1（从下一个区块开始投票）
- `EndBlock`：`StartBlock + getVotePeriod()`
- `getVotePeriod()` 的计算：

  - 从 YAML 配置读取 `dpos_proposal_vote_period`（如"24h"）
  - 转换为区块数：`区块数 = ProposalVotePeriod / BlockTime`
  - 例如：24 小时 = 86400 秒，BlockTime = 3 秒，则 `getVotePeriod() = 86400 / 3 = 28800` 个区块
  - 如果未配置，使用默认值 `28800` 个区块（约 24 小时，按 3 秒/区块）
- **投票权重**：一个验证者一票，和金额无关
- **通过条件**：
- 支持率 >= 投票通过阈值 (`governance_voting_threshold`)

### **5.4 提案查看**

- 可通过 RPC 接口查看：
  - 所有提案列表
  - 提案详情（状态、投票情况、执行情况）
  - 提案状态：Pending → Active → Passed/Rejected → Executed

### **5.5 提案执行**

- **执行人**：
- 提案通过后，在有效期内任何人均可执行
- **执行时机**：
- 提案通过后，在有效期内可以执行
- 有效期：`EndBlock` 到 `ValidEndBlock`
- **有效期计算说明**：
- `EndBlock`：投票期结束区块（见 5.3 节说明）
- `ValidEndBlock`：提案有效期结束区块
- `ValidEndBlock` 的计算：

  - 在创建提案时计算：`ValidEndBlock = currentBlock + getValidPeriod()`
  - `getValidPeriod()` 的计算：

    - 从 YAML 配置读取 `dpos_proposal_valid_period`（如"7d"）
    - 转换为区块数：`区块数 = ProposalValidPeriod / BlockTime`
    - 例如：7 天 = 604800 秒，BlockTime = 3 秒，则 `getValidPeriod() = 604800 / 3 = 201600` 个区块
    - 如果未配置，使用默认值 201600 个区块（约 7 天，按 3 秒/区块）
  - **注意**：`ValidEndBlock` 是基于提案创建时的区块号计算的，而非基于 `EndBlock` 计算
- **提案调度机制** ：

  - **延迟生效设计**：

    - 参数提案：执行后立即生效（更新参数值）
    - 恢复提案：执行后不立即生效，登记到调度系统（`ProposalScheduleMeta`）

      - `Scheduled: true`
      - `EffectiveEpoch: 下一个epoch编号`
      - `Applied: false`（待生效）
  - **调度生效时机**：

    - 在指定 epoch 的边界（epoch 结束区块）
    - 系统统一处理所有待生效的调度
    - 确保状态更新的原子性
- **执行流程**：

1. 验证提案状态（必须是 `Passed`）和有效期（当前区块号在 `ValidEndBlock` 之前）
2. 根据提案类型执行相应操作：

   - **参数提案**：立即更新参数值（`updateParameterValue`）

     - 更新内存中的参数值（`parameterCurrentValues`）
     - 更新配置缓存
     - 保存到数据库（如果需要）
   - **恢复提案**：登记到调度系统（`executeRecoveryProposalInTx`）

     - 设置 `Schedule` 元数据，指定生效 epoch
     - 不立即清除故障标志
     - 等待下个 epoch 边界统一处理
3. 更新提案状态为 `Executed`
4. 记录执行时间（`ExecutedAt`）和执行者（`ExecutedBy`）
5. 保存到数据库（通过 `ProposalStore` 持久化）

**两种提案的要点如下**：

### **5.6 治理示例**

**示例：修改奖励总额提案**

- **配置参数**：
- `dpos_proposal_vote_period = "24h"`（投票期 24 小时）
- `dpos_proposal_valid_period = "7d"`（有效期 7 天）
- `blockTime = 3秒`
- 计算：`getVotePeriod() = 86400秒 / 3秒 = 28800个区块`，`getValidPeriod() = 604800秒 / 3秒 = 201600个区块`

1. **创建提案**（当前区块号：1000）：

- 提案者：用户 A
- 参数：`dpos_reward_amount`
- 旧值：1000 VCITY
- 新值：1500 VCITY
- 描述：提高 epoch 奖励以激励更多参与者

  - **区块范围设置**：

    - `StartBlock = 1000 + 1 = 1001`（从区块 1001 开始投票）
    - `EndBlock = 1000 + 43200 = 44200`（投票期结束于区块 44200）
    - `ValidEndBlock = 1000 + 201600= 202600`（有效期结束于区块 `202600`）

2. **投票期**（区块 1001-44200）：

- 用户 B（质押 5000 VCITY）在区块 5000 投支持票
- 用户 C（质押 3000 VCITY）在区块 8000 投支持票
- 总质押权重：8000 VCITY
- 支持质押权重：8000 VCITY
- 支持率：`8000 / 8000 = 100%`（超过阈值 51%）

3. **提案通过**（区块 44200）：

- 投票期结束，计算支持率
- 支持率达到阈值，状态变为 `Passed`

4. **执行提案**（区块 50000，在有效期内）：

- 用户在区块 50000 执行提案（50000 < `202600`，在有效期内）
- 奖励总额从 1000 VCITY 更新为 1500 VCITY
- 提案状态更新为 `Executed`

---

## **六、故障发现与惩罚 **

### **6.1 故障检测机制**

- **检测时机**：每个 epoch 结束区块统计刚刚结束得 epoch
- **基于区块生产跟踪的检测** ：
- 利用 `BlockProductionTracker` 记录的出块历史
- 统计每个验证者在当前 epoch 内的实际出块数
- 与预期出块数进行对比，计算漏块数
- **检测方法**：

1. 从 `BlockProductionTracker` 获取验证者在 epoch 内的实际出块数（`actualBlocks`）
2. **计算预期出块数**：

   - 公式：`预期出块数 = epochSize = epochDuration / blockTime`
   - 例如：epochDuration = 24 小时 = 86400 秒，blockTime = 3 秒，则预期出块数 = 86400 ÷ 3 = 28800 块
   - 代码实现：`expectedBlocks = epochDuration / blockTime`
   - **注意**：预期出块数是针对整个 epoch 的，每个验证者在 epoch 内应该出块的数量取决于验证者数量和轮次安排
3. **计算漏块率**：

   - 公式：`漏块数 = 预期出块数 - 实际出块数`
   - 例如：预期出块 5 块，实际出块 3 块，则漏块数 = 5 - 3 = 2 块 漏块率 2/5
4. **判断是否故障**：

   - 条件：`漏块率 >= dpos_missed_blocks_percentage`
   - 如果满足条件，标记验证者为故障（`IsFaulty = true`）

- **故障阈值** (`dpos_missed_blocks_percentage`)：
- 代码读取：`d.config.MissedBlockPercentage`
- YAML 配置字段：dpos_missed_blocks_percentage（类型：uint64）
- 默认值：根据网络情况设定（通常在配置文件中指定）
- 阈值设置需要平衡：过小可能误报（正常网络延迟也会触发），过大可能遗漏故障
- 建议根据网络稳定性和区块时间调整

### **6.2 故障惩罚机制 **

故障分类：

--**常规故障**：节点由于故障下线导致不能出块 对应的配置文件字段 dpos_minor_offense_slash_rate

--**双签故障**：节点检测到同一验证者在同一区块高度对不同的区块哈希进行签名 对应的配置文件字段 dpos_severe_offense_slash_rate

- **惩罚触发条件**：
- 当验证者 `漏块率` >= dpos_missed_blocks_percentage（故障阈值）时触发惩罚
- 节点接受到区块，检测到同一验证者在同一区块高度对不同的区块哈希进行签名
- 惩罚在 epoch 结束区块统一执行，确保所有节点状态一致
- **惩罚措施：执行消减并踢出出块者序列**：

  - **立即生效**：故障验证者被从出块者列表中移除
  - **失去出块资格**：无法继续参与区块生产，失去出块奖励
  - **状态标记**：验证者被标记为 `IsFaulty = true`
  - **自动消减**：验证者之上得所有投票者的质押按照配置文件中的比例扣除，消减的钱去往黑洞地址
  - **自动顶替**：后续候选节点按权重排序自动顶替，确保出块者数量满足要求
  - **持久化惩罚**：故障状态保存到数据库，即使节点重启也会保持故障状态

### **6.3 故障处理流程**

- **故障标记**：
- 在 epoch 结束区块，调用 `detectValidatorFaults` 生成故障标志（`FaultFlagInfo` 数组）
- 每个验证者的故障信息包括：

  - `NodeAddress`：验证者地址
  - `IsFaulty`：是否故障（true/false）
  - `MissedBlocks`：漏块数
  - `ActualBlocks`：实际出块数
  - `EpochNumber`：统计的 epoch 编号
  - `LastFaultyEpoch`：上次故障的 epoch（如果是首次故障则为当前 epoch）
  - `Reason`：故障原因描述
- 故障标志写入区块 ExtraData（`extra.FaultFlags`）
- 所有节点在验证区块时，从 ExtraData 读取并同步故障状态
- **状态持久化** ：
- 故障状态保存到数据库（通过 `StakeStore`）
- 存储内容包括：故障状态、漏块数、故障 epoch、最后更新时间
- 新节点可通过同步区块数据恢复故障状态
- 内存中同时维护故障状态映射（`faultyValidators`）用于快速查询
- **故障处理与惩罚执行**：

  所有节点在处理包含故障标志的区块时，调用 `updateBlockProducersFromFaultFlags`
- **执行惩罚**：故障验证者从出块者列表中移除（`updateMemoryFaultStatus`）

更新验证者集合（移除故障节点）

后续验证者按权重排序自动顶替

故障状态持久化到数据库（`updateValidatorFaultStatus`）

### **6.4 节点顶替机制**

- **自动顶替**：
- 故障节点被移除后，按权重排序的下一个候选节点自动顶替
- 顶替在下一个 epoch 开始时生效
- **出块者列表更新**：
- 确保出块者数量满足要求

---

## **七、故障节点恢复 **

### **7.1 恢复申请机制**

- **前提条件**：
- 节点必须处于故障状态
- 节点已恢复正常运行（但还不能自动恢复）
- **恢复方式**：通过治理提案申请恢复

### **7.2 创建恢复提案**

- **提案创建人**：
- 只有验证者可以创建提案
- **提案创建流程**：
- 验证节点当前为故障状态

  创建验证者恢复提案（`ProposalType: "validator_recovery"`）

  填写恢复理由（RecoveryReason）

  提案签名并提交
- **提案内容**：

旧值：故障状态信息（isFaulty=true, missedBlocks, lastFaultyEpoch）

新值：恢复后状态（isFaulty=false, missedBlocks=0）

### **7.3 恢复提案生命周期**

- **投票期**：
- 社区对恢复提案进行投票
- 投票规则与参数提案相同

**注意：只有验证者可以参与当前投票**

- **执行期**：
- 提案通过后，在有效期内执行
- 任何人可以执行提案
- **生效时机**：
- 执行后不立即生效
- 登记到调度系统（Schedule）
- 在下一个 epoch 边界统一生效

### **7.4 恢复示例**

**示例场景：验证者 A 故障恢复**

1. **故障状态**（Epoch 5 结束）：

- 验证者 A 因漏块被标记为故障
- 从出块者列表中移除

2. **节点修复**（Epoch 6 期间）：

- 验证者 A 修复了网络问题，节点恢复正常运行
- 但仍是故障状态，无法参与出块

3. **创建恢复提案**（Epoch 6，区块 300）：

- 用户 B 创建恢复提案
- 提案 ID：`recovery_xxxxx`
- 恢复理由：网络故障已修复，节点已稳定运行 48 小时

4. **投票期**（区块 301-400）：

- 社区投票支持恢复
- 支持率达到阈值，提案通过

5. **执行提案**（区块 401）：

- 提案执行，登记到调度系统
- 生效 epoch：Epoch 7

6. **恢复生效**（Epoch 7 开始，区块 450）：

- 验证者 A 故障标志清除
- 验证者 A 重新加入出块者列表
- 恢复参与出块

---

## 八、交易类型与 gas 流向说明

当前代码同时支持两种情况，会根据交易类型自动选择：

### 8.1：EIP-1559 动态费用交易（DynamicFeeTx）

#### 8.1.1 关键字段

- maxFeePerGas (GasFeeCap)：愿意支付的最大总费用
- maxPriorityFeePerGas (GasTipCap)：愿意给验证者的小费

#### 8.1.2 小费 Tip

Effective Tip = min(GasTipCap, GasFeeCap - BaseFee)

#### 8.1.3 费用分配：

- Base Fee = gasUsed × BaseFee → Coinbase（验证者）
- Effective Tip = gasUsed × Effective Tip → Coinbase（验证者）

所以目前是 gas 都归矿工所有

### 8.2 传统交易（LegacyTx）

Effective Tip = GasPrice - BaseFee（会扣除 BaseFee）

下面是代码逻辑流程

![](static/Om99b6dXjoCrFLxiHFTcHNOsnUd.png)

配置文件中

base_fee_config: "1000000000:2:8"  # baseFee:baseFeeEM:baseFeeChangeDenom

- BaseFee = 1000000000 wei = 1 Gwei
- BaseFeeEM = 2（弹性乘数）
- BaseFeeChangeDenom = 8（基础费用变化分母）

目前测试设置黑洞为单独地址，和奖励分配地址分隔开，方便查询收到的 gas 估计交易量。

## **九、回滚设置**

回滚仅作为备用手段，影响比较大，请慎重使用。

### 9.1 实施步骤

首先停止程序，假设直接二进制运行 ：kill -9 pid

删除程序的锁：rm node*/blockchain/LOCK

执行回滚：（假设需要回滚到高度 100000）

./vcitychain rollback --config ./node1/node-config-validator.yaml --target-height 100000 --force

返回

[ROLLBACK]

Rollback completed successfully:

Current Height | 100500

Target Height  | 100000

Target Hash    | 0xabc123def456...

Blocks Deleted | 500

Keep Blocks    | false

返回字段：

命令中 force 参数的作用：跳过交互式确认提示

### 9.2 回滚逻辑与影响范围

#### 9.2.1 回滚逻辑

1. 检查节点是否停止运行
2. 打开区块链数据库（LevelDB）
3. 验证目标高度有效
4. 加载 Epoch 配置参数
5. 执行区块链回滚：

├── 更新链头 (HeadHash, HeadNumber)

├── 删除规范链映射

├── 删除区块数据 (Header, Body, Receipts, Difficulty)

├── 删除交易索引

└── 删除状态快照

1. 执行 DPoS 状态回滚：

├── 清理 epochs bucket

├── 清理 validatorSnapshots bucket

├── 清理 EpochBlocks bucket

├── 清理 VotingPowerAtBlock bucket

├── 回滚参数值 (parameters) ← 新增

├── 清理提案 (proposals)

└── 清理验证者故障状态 (validatorFaultStatus)

1. 清理奖励数据库 (独立 dpos.db.rewards)
2. 输出回滚结果

#### 9.2.2 影响范围

##### 9.2.2.1、区块链数据库（LevelDB - blockchain/）

---

##### 9.2.2.2、DPoS 数据库（BoltDB - dpos.db）

---

##### 9.2.2.3、奖励数据库（BoltDB - dpos.db.rewards）

## **十、配置讲解**

### **10.1 线上测试网配置**

dpos_validators_count: 21

dpos_missed_blocks_percentage: 1000  #1000 个基点 10%

dpos_minor_offense_slash_rate: 50    #50 个基点 0.5%

dpos_severe_offense_slash_rate: 1000  #1000 个基点 10%

base_fee_config: "1000000000:2:8"  # baseFee:baseFeeEM:baseFeeChangeDenom

burn_contract: "0:0x0000000000000000000000000000000000000000" #目前配置为空，base fee 目前直接给矿工

dpos_proposal_vote_period: "1d"  # 表决期 1 天

dpos_proposal_valid_period: "5d"  # 有效期 5 天

dpos_delegate_threshold: "1000000000000000000000"  # 1000 VCITY

# （可选）创世投票金额：root 账户在共识切换高度给“每个创世验证者”创建的初始投票记录金额（wei）
# 不配置则回退使用 dpos_delegate_threshold（向后兼容）
dpos_genesis_vote_amount: "1000000000000000000000"  # 1000 VCITY

# （可选）创世投票金额：root 账户在共识切换高度给“每个创世验证者”创建的初始投票记录金额（wei）
# 不配置则回退使用 dpos_delegate_threshold（向后兼容）
dpos_genesis_vote_amount: "1000000000000000000000"  # 1000 VCITY

dpos_epoch_duration: "1h"

dpos_reward_distribution: "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe"  # 奖励分发账户，目前采用创世中的根账户

dpos_reward_amount: "120000000000000000000"  # 每个 epoch 120 VCITY

dpos_min_freeze_period: 300      # 最小冻结期（秒，5 分钟）

dpos_unfreeze_lock_period: 600   # 解冻锁定期（秒，10 分钟）

dpos_commission_radio: 1000       # 佣金比例 1000 个基点 10%

dpos_commission_effective :  30m # 30 分生效 验证者调整佣金后 30 分钟以后下一个 epoch 结束即可自动转正，无需手动操作

**漏块率门槛**

dpos_missed_blocks_percentage，漏块超过此门槛视为漏块故障，执行

**漏块扣除比例**

dpos_minor_offense_slash_rate

**双签扣除比例**

dpos_severe_offense_slash_rate

**验证者规模 / 容错**

- dpos_validators_count: 每轮参与出块的验证者数量（例如 21 个）。决定委员会规模、容错阈值（默认拜占庭容错 ⌊(n−1)/3⌋）。

**EIP-1559 / 交易费用**

- base_fee_config: baseFee:baseFeeEM:baseFeeChangeDenom
- baseFee: 创世或切换时的初始 base fee（wei）。
- baseFeeEM: Elasticity multiplier，控制 gas 目标区间。
- baseFeeChangeDenom: 算 base fee 调整幅度的分母。
- burn_contract: blockNumber:address 格式，指定从哪个高度开始把 base fee 或罚没发送到哪个合约地址（为 0 表示销毁）。

**治理提案周期**

- dpos_proposal_vote_period: 每个治理提案的投票窗口长度（如 "1d" = 1 天）。投票期内投出支持率需达标，否则提案失败。
- dpos_proposal_valid_period: 提案自创建起的总有效期（如 "5d"）。过期后自动作废，即使还没投票或投票未结束。

**受托注册 / 押金门槛**

- dpos_delegate_threshold: 注册成为受托人的保证金（以 wei 表示，1e21 = 1000 VCITY）。同时也是投票阈值（谁拥有 ≥ 该额度可参与投票）。
- dpos_genesis_vote_amount: 创世根账户在共识切换高度给每个创世验证者创建的初始投票金额（wei）。不配置则回退使用 dpos_delegate_threshold。
- dpos_epoch_duration: 重新计算排名、发放奖励、轮换委员会的周期（如 "1h" = 1 小时）。
- dpos_reward_distribution: 奖励发放的系统账户地址（受托奖励从这里发）。
- dpos_reward_amount: 每个 epoch 要分配到奖励池的总金额（wei），后续再按比例拆给验证者 / 投票者。

**奖励分配比例**

- dpos_validator_reward_ratio: 每个 epoch 发放给验证者的百分比（整数）。例如 70 表示 70% 给验证者。
- dpos_voter_reward_ratio: 发给所有投票者的百分比。例如 30 表示 30% 给投票者。两者相加应为 100。内部会根据投票权重再行二次分配。

**冻结 / 解冻流程**

- dpos_min_freeze_period: 注册受托人后必须冻结多少秒，期间不能退出（例如 300 秒 = 5 分钟）。
- dpos_unfreeze_lock_period: 当满足最小冻结期并退出受托人后，保证金进入锁定状态的时间（例如 600 秒 = 10 分钟）。锁定期结束才真正解锁回可用余额。

### **10.2 线上生产配置**

以 [DPoS 经济学设计](https://lcnxt0aw7kvz.feishu.cn/wiki/VQo2wYWPZi3crEkWw8GcwzVSnDg)为准，设置参数如下：

dpos_validators_count: 21

dpos_missed_blocks_percentage: 1000  #1000 个基点 10%

dpos_minor_offense_slash_rate: 50    #50 个基点 0.5%

dpos_severe_offense_slash_rate: 1000  #1000 个基点 10%

base_fee_config: "1000000000:2:8"  # baseFee:baseFeeEM:baseFeeChangeDenom

burn_contract: "0:0x0000000000000000000000000000000000000000" #目前配置为空，base fee 目前直接给矿工

dpos_proposal_vote_period: "1d"  # 表决期 1 天，表决期过了后才能投票

dpos_proposal_valid_period: "5d"  # 有效期 5 天

dpos_delegate_threshold: "1000000000000000000000"  # 1000 VCITY

dpos_epoch_duration: "1h"

dpos_reward_distribution: "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe**"  # 奖励分发账户，目前是创世文件中的根账户

dpos_reward_amount: "120000000000000000000"  # 每个 epoch 120 VCITY

dpos_min_freeze_period: 86400      # 最小冻结期（秒，1 天）

dpos_unfreeze_lock_period: 432000      # 解冻锁定期（秒，5 天）

dpos_commission_radio: 1000       # 佣金比例 1000 个基点 10%

dpos_commission_effective : 604800  # 7 天生效 验证者调整佣金后 7 天下一个 epoch 结束即可自动转正，无需手动操作

### 10.3 收益率计算

下面基于上面的参数，列出 apy：

1. 实际 APY 取决于总质押量：质押越多，APY 越低
2. 验证者数量影响：21 个验证者会分配奖励

- 每个 Epoch 奖励: 120 VCITY
- Epoch 时长: 1 小时
- 每年 Epoch 数: 8,760 个
- 年总奖励: 1,051,200 VCITY
- 佣金率: 10% (1000 基点)
- 验证者数量: 21 个

#### 投票者 APY（基于总质押量）

投票者年总奖励 = 1,051,200 × 90% = 945,600 VCITY

投票者 APY = (945,600 / 总质押量) × 100%

#### 验证者 APY（基于验证者质押量）

验证者年总佣金 = 1,051,200 × 10% = 105,120 VCITY

验证者 APY = (105,120 / 验证者质押量) × 100%

注意：验证者实际 APY 还取决于：

- 验证者的出块比例（21 个验证者平均分配时，每个验证者约 4.76%）
- 验证者自己的质押量
- 验证者获得的投票量

#### 总质押量与投票者 APY 对照表

![](static/ICVqbpbino5TXrxskXeccZS5n62.jpg)

#### 4.验证者 APY 示例

假设验证者平均出块，每个验证者年佣金 = 105,120 / 21 = 5,005.71 VCITY

![](static/Pt68boQMVo8biRxPIdScdbYRnNh.jpg)

#### 5.通用计算公式

epoch_reward = 120  # VCITY per epoch

epochs_per_year = 365 * 24  # 8,760 epochs

annual_reward = epoch_reward * epochs_per_year  # 1,051,200 VCITY

commission_rate = 0.10  # 10%

voter_reward_ratio = 1 - commission_rate  # 90%

#投票者奖励

voter_annual_reward = annual_reward * voter_reward_ratio  # 945,600 VCITY

#验证者佣金

validator_annual_commission = annual_reward * commission_rate  # 105,120 VCITY

#计算 APY

total_staked = 10000000  # 10,000,000 VCITY (示例)

voter_apy = (voter_annual_reward / total_staked) * 100

print(f"投票者 APY: {voter_apy:.2f}%")

validator_staked = 100000  # 100,000 VCITY per validator (示例)

validator_commission_per_validator = validator_annual_commission / 21  # 假设 21 个验证者平均分配

validator_apy = (validator_commission_per_validator / validator_staked) * 100

print(f"验证者 APY: {validator_apy:.2f}%")

#### **6.总结**

1. 投票者 APY 与总质押量成反比：质押越多，APY 越低
2. 验证者 APY 取决于：

- 验证者自己的质押量
- 验证者的出块比例（影响获得的佣金）
- 验证者获得的投票量（影响奖励池大小）

1. 10% 佣金率意味着：投票者获得 90% 奖励，验证者获得 10% 佣金

## **十一、DPoS RPC 接口**

### 接口参见文档 [VCityChain DPOS 接口说明](https://lcnxt0aw7kvz.feishu.cn/wiki/EWJBwpYb4i87EnkFykNcBhPEnSf?fromScene=spaceOverview)

---

## 十二、故障和提案的生命周期

### 12.1 故障

#### **12.1.1 故障发生阶段**

- **时间点**：节点在某个 Epoch（例如 Epoch N）期间发生故障
- **故障表现**：节点漏块，漏块率超过阈值（默认 10%）
- **状态**：节点仍在当前 Epoch 的验证者集合中，但已标记为故障

#### **12.1.2 故障检测阶段**

- **时间点**：Epoch N 的结束区块（epoch end block）
- **检测内容**：

  - 统计 Epoch N 期间每个验证者的出块情况
  - 计算漏块数和漏块率
  - 判断是否超过阈值
- **处理流程**：

  1. 执行 `detectValidatorFaults()` 检测故障
  2. 立即保存故障状态到数据库（`saveFaultStatusToDatabase()`）
  3. 更新内存中的故障状态（`updateMemoryFaultStatus()`）
  4. 将故障标志写入 `pendingFaultFlags`，后续写入 ExtraData

#### **12.1.3 验证者集合计算阶段**

- **时间点**：Epoch N 的结束区块（与故障检测在同一区块）
- **处理流程**：

  1. **先应用恢复提案**（如果有）：清除已恢复节点的故障标志
  2. **执行故障检测**：检测并保存故障状态
  3. **计算下一个 Epoch 的验证者集合**：调用 `calculateNextEpochValidators()`

     - 读取数据库中的故障状态
     - 过滤掉所有故障节点
     - 生成新的验证者集合
  4. **写入 ExtraData**：将新的验证者集合写入区块的 ExtraData

#### **12.1.4 故障生效阶段**

- **时间点**：下一个 Epoch（Epoch N+1）开始
- **效果**：故障节点被排除在出块者列表之外，不再参与出块

### 12.2 提案

#### **12.2.1 提案执行**

- **时间点**：提案执行交易被打包到区块中
- **处理流程**：

1. 调用 `executeRecoveryProposalInTx()` 执行提案
2. 计算生效 Epoch（`effectiveEpoch`）：

   - **如果执行时不是 epoch 结束区块**：`effectiveEpoch = currentEpoch`（当前 Epoch 结束生效）
   - **如果执行时是 epoch 结束区块**：`effectiveEpoch = currentEpoch + 1`（下一个 Epoch 结束生效）
3. 标记提案为 `Scheduled=true`，状态为 `ProposalExecuted`
4. 保存到数据库，等待边界应用

#### **12.2.2 提案应用**

- **时间点**：`effectiveEpoch` 的结束区块（epoch end block）
- **处理流程**（在 `buildBlock` 中，计算验证者集合之前）：

1. 收集所有 `EffectiveEpoch == currentEpoch` 且未应用的恢复提案
2. 应用恢复提案：

   - 清除验证者的故障标志（数据库和内存）
   - 重新加载验证者集合
   - 标记提案为 `Applied=true`
3. **关键**：在应用恢复提案之后，才执行故障检测和计算验证者集合

#### **12.2.3 恢复生效**

- **时间点**：下一个 Epoch（Epoch N+1）开始
- **效果**：恢复的节点被包含在新的验证者集合中，重新参与出块

### **12.3 示例**

#### **示例 1：节点故障后排除**

**假设**：

- Epoch 大小：40 个区块
- 共识切换高度：7370
- Epoch 10：区块 7370-7409
- Epoch 11：区块 7410-7449

**时间线**：

1. **Epoch 10 期间（区块 7370-7409）**

- 节点 A 在 Epoch 10 期间漏块，漏块率超过阈值
- 节点 A 仍参与 Epoch 10 的出块（因为故障检测还未执行）

2. **Epoch 10 结束区块（区块 7409）**

- 执行故障检测：检测到节点 A 在 Epoch 10 期间故障
- 保存故障状态到数据库

  - 计算 Epoch 11 的验证者集合：**排除节点 A**
- 将新的验证者集合写入 ExtraData

3. **Epoch 11 开始（区块 7410）**

- 所有节点从 ExtraData 读取新的验证者集合

  - **节点 A 被排除，不再参与出块**

#### **示例 2：恢复提案生效**

**假设**：

- Epoch 10：区块 7370-7409
- Epoch 11：区块 7410-7449
- 节点 A 在 Epoch 10 期间故障，被排除在 Epoch 11 的验证者集合外

**时间线**：

1. **Epoch 11 期间（区块 7410-7449）**

- 区块 7420：创建恢复提案（提案 ID：`recovery_0x...`）
- 区块 7430：投票通过

  - 区块 7440：**执行恢复提案**

    - 当前 Epoch = 11
    - 当前区块不是 epoch 结束区块
    - `effectiveEpoch = 11`（当前 Epoch 结束生效）

2. **Epoch 11 结束区块（区块 7449）**

   - **先应用恢复提案**：

     - 清除节点 A 的故障标志（数据库和内存）
     - 重新加载验证者集合
   - **然后执行故障检测**：检测 Epoch 11 的故障（节点 A 不在 Epoch 11 的验证者集合中，不会被检测）
   - **最后计算 Epoch 12 的验证者集合**：**包含节点 A**（因为故障标志已清除）

- 将新的验证者集合写入 ExtraData

3. **Epoch 12 开始（区块 7450）**

- 所有节点从 ExtraData 读取新的验证者集合

  - **节点 A 重新参与出块**

#### **示例 3：在 Epoch 结束区块执行恢复提案**

**假设**：

- Epoch 10：区块 7370-7409
- Epoch 11：区块 7410-7449
- 节点 A 在 Epoch 10 期间故障

**时间线**：

1. **Epoch 10 结束区块（区块 7409）**

- 执行故障检测：检测到节点 A 故障
- 计算 Epoch 11 的验证者集合：排除节点 A

2. **Epoch 11 期间（区块 7410-7449）**

- 区块 7440：创建并投票通过恢复提案

  - 区块 7449：**执行恢复提案**（恰好是 epoch 结束区块）

    - 当前 Epoch = 11
    - 当前区块是 epoch 结束区块
    - `effectiveEpoch = 12`（下一个 Epoch 结束生效）

3. **Epoch 12 结束区块（区块 7489）**

- 应用恢复提案：清除节点 A 的故障标志
- 执行故障检测
- 计算 Epoch 13 的验证者集合：包含节点 A

4. **Epoch 13 开始（区块 7490）**

- 节点 A 重新参与出块

#### **11.11.4 关键时间点总结**

##### **故障检测**

<table>
<tr>
<td>阶段<br/></td><td>时间点<br/></td><td>操作<br/></td><td>影响<br/></td></tr>
<tr>
<td>故障发生<br/></td><td>Epoch N 期间<br/></td><td>节点漏块<br/></td><td>节点仍参与出块<br/></td></tr>
<tr>
<td>故障检测<br/></td><td>Epoch N 结束区块<br/></td><td>检测并保存故障状态<br/></td><td>故障状态写入数据库<br/></td></tr>
<tr>
<td>验证者集合计算<br/></td><td>Epoch N 结束区块<br/></td><td>排除故障节点<br/></td><td>新集合写入 ExtraData<br/></td></tr>
<tr>
<td>故障生效<br/></td><td>Epoch N+1 开始<br/></td><td>从 ExtraData 读取<br/></td><td>故障节点被排除<br/></td></tr>
</table>

##### **恢复提案**

<table>
<tr>
<td>阶段<br/></td><td>时间点<br/></td><td>操作<br/></td><td>影响<br/></td></tr>
<tr>
<td>提案执行<br/></td><td>Epoch N 期间（非结束区块）<br/></td><td>计算 `effectiveEpoch = N`<br/></td><td>提案标记为待生效<br/></td></tr>
<tr>
<td>提案应用<br/></td><td>Epoch N 结束区块<br/></td><td>清除故障标志<br/></td><td>故障标志被清除<br/></td></tr>
<tr>
<td>验证者集合计算<br/></td><td>Epoch N 结束区块<br/></td><td>包含恢复节点<br/></td><td>新集合写入 ExtraData<br/></td></tr>
<tr>
<td>恢复生效<br/></td><td>Epoch N+1 开始<br/></td><td>从 ExtraData 读取<br/></td><td>恢复节点重新参与出块<br/></td></tr>
</table>

##### **恢复提案（在结束区块执行）**

<table>
<tr>
<td>阶段<br/></td><td>时间点<br/></td><td>操作<br/></td><td>影响<br/></td></tr>
<tr>
<td>提案执行<br/></td><td>Epoch N 结束区块<br/></td><td>计算 `effectiveEpoch = N+1`<br/></td><td>提案标记为待生效<br/></td></tr>
<tr>
<td>提案应用<br/></td><td>Epoch N+1 结束区块<br/></td><td>清除故障标志<br/></td><td>故障标志被清除<br/></td></tr>
<tr>
<td>验证者集合计算<br/></td><td>Epoch N+1 结束区块<br/></td><td>包含恢复节点<br/></td><td>新集合写入 ExtraData<br/></td></tr>
<tr>
<td>恢复生效<br/></td><td>Epoch N+2 开始<br/></td><td>从 ExtraData 读取<br/></td><td>恢复节点重新参与出块<br/></td></tr>
</table>

---

## **十二、系统特色与设计分析 **

### **12.1 VCity Chain 系统核心特色 **

#### **12.1.1 BLS 聚合签名机制**

- **VCity Chain 特色**：

  - **BLS（Boneh-Lynn-Shacham）聚合签名**：多个验证者签名可以聚合成一个签名
  - **签名聚合流程**：

    1. 每个验证者使用 BLS 私钥对 checkpoint hash 签名
    2. 所有签名通过 `blsSignatures.Aggregate()` 聚合成单个 64 字节签名
    3. 聚合签名写入区块 ExtraData
    4. 验证时使用 BLS 公钥集合验证聚合签名
  - **技术优势**：

    - **带宽节省**：无论多少验证者，区块只需存储一个 64 字节的聚合签名（而非 N 个签名）
    - **验证效率**：验证聚合签名比验证多个独立签名更快
    - **可扩展性**：验证者数量增加时，签名大小不变
    - **安全性**：BLS 签名具有密码学安全性保证
  - **代码实现**：

    - 签名聚合：`consensus/dpos/dpos.go: 聚合签名逻辑（blsSignatures.Aggregate()）`
    - 签名验证：`consensus/dpos/extra.go: Signature.Verify`
    - BLS 公钥管理：`consensus/dpos/validator/validator_metadata.go: BlsKey字段`

#### **12.1.2 治理投票门槛机制**

- **VCity Chain 特色**：

  - **最小质押门槛**（`governance_min_voting_threshold`）：参与治理投票需要满足最小质押要求
  - **设计目的**：提高治理投票质量，确保参与者有一定质押，避免恶意投票
  - **优势**：

    - 提高治理决策的质量和严肃性
    - 减少恶意投票和垃圾投票
    - 确保参与者与网络利益绑定
    - 防止治理攻击

#### **12.1.3 固定 Epoch 时长机制**

- **VCity Chain 设计**：
- Epoch 时长可配置（默认 24 小时）
- 基于固定时间窗口的奖励计算和状态更新
- Epoch 边界统一生效，确保状态一致性
- **优势**：
- 更灵活的配置，可根据需求调整
- 24 小时周期更适合长期奖励分配
- 时间窗口固定，便于预测和规划

#### **12.1.4 完善的故障检测与恢复机制**

- **VCity Chain 机制**：

  - **故障检测**：基于 BlockProductionTracker 实时跟踪出块
  - **故障惩罚**：自动踢出故障验证者
  - **恢复机制**：通过治理提案申请恢复（需社区投票通过）
- **优势**：
- 更严格的故障处理，确保网络稳定性
- 恢复需社区同意，避免恶意节点快速恢复
- 实时监控和自动响应

### **12.2 系统设计与优化建议 **

#### **12.2.1 验证者数量管理**

- **当前设计**：
- 验证者数量可配置（`DPoSValidatorsCount`）
- 默认可灵活调整
- **潜在影响**：
- 如果验证者数量频繁变化，可能影响网络稳定性
- 法定人数计算会随验证者数量变化
- **优化建议**：
- 考虑设置合理的验证者数量上限和下限
- 避免验证者数量频繁变化
- 可以设置最小验证者数量阈值，低于阈值时暂停某些操作

#### **12.2.2 区块确认与最终性**

- **当前设计**：

  - **动态法定人数**：`requiredQuorumCount = activeValidators / 2`（半数）
- 法定人数随验证者数量变化
- 代码实现：`calculateMinRequiredSignatures() = activeValidatorsCount / 2`
- **潜在影响**：
- 没有固定的确认阈值，应用层判断最终性可能较复杂
- 不同验证者数量下，确认数不同
- **优化建议**：
- 考虑提供固定的"稳定区块"确认数概念
- 定义：某个区块被 N 个验证者确认后视为稳定（N 可以是固定值，如 19 或 21）
- 提供 API 让应用层查询区块是否已达到稳定确认数
- 便于应用层判断交易最终性

#### **12.2.3 轮次（Round）机制**

- **当前设计**：
- 主要基于 Epoch 概念，轮次概念相对弱化
- 轮次计算基于 slot：`round = (currentSlot / validatorCount) + 1`
- **优化建议**：
- 可以考虑明确轮次的概念和边界
- 或者完全基于 Epoch，统一时间窗口概念
- 如果引入轮次，可以定义固定时长的轮次（如每 N 小时一个轮次）

#### **12.2.4 分叉处理与稳定区块**

- **当前设计**：
- 最长链规则（基于总难度），总难度=区块数量
- 缺少明确的"稳定区块"概念
- **优化建议**：
- 可以引入"稳定区块"概念：被固定数量验证者确认后的区块视为稳定
- 提供 API 让应用层查询区块是否已达到稳定确认数
- 稳定区块减少被回滚的可能性
- 提高分叉处理的用户体验

#### **12.2.5 验证者注册审批机制**

- **当前设计**：
- 有 SR 候选人保证金机制（`dpos_SR_threshold`）
- 有 `approveDelegate` 和 `rejectDelegate` API
- 审批机制的具体实现可能需要完善
- **优化建议**：
- 完善验证者注册的审批流程
- 明确审批权限和审批标准
- 可以设置审批者（如当前验证者集合）或社区投票审批

### **12.3 系统设计亮点总结**

**VCity Chain 的核心优势**：

- ✅ **BLS 聚合签名**：显著提升签名效率和可扩展性，这是行业领先的设计
- ✅ **治理投票门槛**：提高治理质量和安全性，防止恶意投票
- ✅ **完善的故障处理**：实时检测、自动惩罚、治理恢复，确保网络稳定性
- ✅ **灵活的配置机制**：Epoch 时长、验证者数量等关键参数可配置
- ✅ **基于固定时间窗口**：精确且可预测的奖励计算和状态更新

---

## **十三、总结与最佳实践 **

### **13.1 系统设计亮点**

- **BLS 聚合签名机制**：相比传统 ECDSA 签名，显著提升签名效率和可扩展性
- **延迟状态更新机制**：epoch 边界统一生效，确保状态一致性
- **基于固定时间窗口的奖励计算**：精确且可预测的奖励分配
- **完善的故障检测和恢复机制**：实时跟踪、自动惩罚、治理恢复
- **灵活的治理系统**：完善的提案投票和执行机制
- **治理投票门槛机制**：提高治理质量，确保参与者与网络利益绑定

### **13.2 使用建议**

- 投票者：合理分散投票以降低风险
- 验证者：保持节点稳定运行，避免故障
- 提案者：充分论证提案的必要性和影响

### **13.3 注意事项**

- 投票后有一定锁定时间
- 提案执行后不会立即生效，需等待 epoch 边界
- 故障恢复需要通过治理提案，无法自动恢复

---

## **附录**

### **A. 配置参数总览**

- 所有可配置参数及默认值
- 参数说明和取值范围

### 

---
