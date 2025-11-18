# 故障检测与削减惩罚机制完整方案

## 一、概述

本方案实现了两种违规类型的检测和惩罚机制：
- **轻度违规（Minor Offense）**：漏块率超过阈值
- **严重违规（Severe Offense）**：双重签名

## 二、配置参数

### 2.1 新增配置项（`dpos_config.go` 和 `config.go`）

```go
// 漏块率阈值（基点，1000 = 10%）
MissedBlocksPercentage uint64 `json:"missed_blocks_percentage" yaml:"missed_blocks_percentage"`

// 轻度违规削减率（基点，50 = 0.5%）
MinorOffenseSlashRate uint64 `json:"minor_offense_slash_rate" yaml:"minor_offense_slash_rate"`

// 严重违规削减率（基点，1000 = 10%）
SevereOffenseSlashRate uint64 `json:"severe_offense_slash_rate" yaml:"severe_offense_slash_rate"`

// 削减代币销毁地址（黑洞地址）
SlashBurnAddress types.Address `json:"slash_burn_address" yaml:"slash_burn_address"`
```

### 2.2 默认值

```go
MissedBlocksPercentage: 1000,  // 10%
MinorOffenseSlashRate:   50,   // 0.5%
SevereOffenseSlashRate:  1000, // 10%
SlashBurnAddress:        types.StringToAddress("0x0000000000000000000000000000000000000000"),
```

## 三、数据结构修改

### 3.1 扩展 `FaultFlagInfo`（`extra.go`）

```go
type FaultFlagInfo struct {
    NodeAddress          types.Address `json:"node_address"`
    IsFaulty             bool          `json:"is_faulty"`
    MissedBlocks         uint64        `json:"missed_blocks"`
    ActualBlocks         uint64        `json:"actual_blocks"`
    ExpectedBlocks       uint64        `json:"expected_blocks"`           // 🆕 预期出块数
    MissedBlocksPercentage uint64       `json:"missed_blocks_percentage"` // 🆕 漏块率（基点）
    LastUpdateTime       uint64        `json:"last_update_time"`
    EpochNumber          uint64        `json:"epoch_number"`
    LastFaultyEpoch      uint64        `json:"last_faulty_epoch"`
    Reason               string        `json:"reason"`                    // 包含违规类型和削减率
}
```

### 3.2 扩展 `VoterInfo`（`types.go`）

```go
type VoterInfo struct {
    Address         types.Address                    `json:"address"`
    VotingPower     *big.Int                         `json:"votingPower"`     // 总投票权重（可删除，由DelegateVotes累加）
    VotedDelegates []types.Address                   `json:"votedDelegates"`  // 投票的验证者列表
    DelegateVotes   map[types.Address]*big.Int       `json:"delegateVotes"`  // 🆕 delegate -> 投票金额（削减后）
    SlashingRecords map[types.Address][]*SlashingRecord `json:"slashingRecords"` // 🆕 削减历史记录
    LastVoteTime    uint64                           `json:"lastVoteTime"`
    LockedUntil     uint64                           `json:"lockedUntil"`
    Nonce           map[uint64]bool                   `json:"nonce"`
}
```

### 3.3 新增 `SlashingRecord`（`types.go`）

```go
// SlashingRecord 削减记录（存储在VoterInfo中）
type SlashingRecord struct {
    ValidatorAddr          types.Address `json:"validator_addr"`
    BlockNumber            uint64        `json:"block_number"`
    EpochNumber            uint64        `json:"epoch_number"`
    Timestamp              uint64        `json:"timestamp"`
    SlashAmount            *big.Int      `json:"slash_amount"`
    OldVoteAmount          *big.Int      `json:"old_vote_amount"`
    NewVoteAmount          *big.Int      `json:"new_vote_amount"`
    SlashRate              uint64        `json:"slash_rate"`
    Reason                 string        `json:"reason"`
    MissedBlocks           uint64        `json:"missed_blocks,omitempty"`
    MissedBlocksPercentage uint64        `json:"missed_blocks_percentage,omitempty"`
}
```

### 3.4 新增 `SlashingHistory`（`slashing.go`）

```go
// SlashingHistory 处罚历史记录（存储在数据库中）
type SlashingHistory struct {
    ValidatorAddr          types.Address `json:"validator_addr"`
    BlockNumber            uint64        `json:"block_number"`
    EpochNumber            uint64        `json:"epoch_number"`
    Timestamp              uint64        `json:"timestamp"`
    SlashAmount            *big.Int      `json:"slash_amount"`
    OldVotingPower         *big.Int      `json:"old_voting_power"`
    NewVotingPower         *big.Int      `json:"new_voting_power"`
    SlashRate              uint64        `json:"slash_rate"`
    Reason                 string        `json:"reason"`
    MissedBlocks           uint64        `json:"missed_blocks,omitempty"`
    MissedBlocksPercentage uint64        `json:"missed_blocks_percentage,omitempty"`
    DoubleSigningHeight    uint64        `json:"double_signing_height,omitempty"`
}
```

### 3.5 扩展 `StakeInfo`（`types.go`）

```go
type StakeInfo struct {
    Staker          types.Address          `json:"staker"`
    Amount          *big.Int               `json:"amount"`
    OriginalAmount  *big.Int               `json:"original_amount"`  // 🆕 原始投票金额（削减前）
    StartTime       uint64                 `json:"startTime"`
    EndTime         uint64                 `json:"endTime"`
    IsLocked        bool                   `json:"isLocked"`
    IsActive        bool                   `json:"isActive"`
    Rewards         *big.Int               `json:"rewards"`
    Delegate        types.Address          `json:"delegate"`
    SlashingRecords []*SlashingRecord     `json:"slashing_records,omitempty"` // 🆕 削减历史
    FaultFlag       map[string]interface{} `json:"faultFlag,omitempty"`
}
```

## 四、故障检测机制

### 4.1 轻度违规检测（漏块率检测）

**位置**：`consensus/dpos/validator_mgmt_fault.go` - `detectValidatorFaults`

**检测逻辑**：
1. 在每个 epoch 结束时计算每个验证者的漏块率
2. 计算公式：`missedBlocksPercentage = (missedBlocks / expectedBlocks) * 10000`（基点）
3. 判断条件：`missedBlocksPercentage >= MissedBlocksPercentage`
4. 如果满足条件，标记 `isFaulty = true`

**关键代码**：
```go
// 计算漏块率
expectedBlocks := d.calculateExpectedBlocks(validator.Address, epochToCheck)
missedBlocksPercentage := uint64(0)
if expectedBlocks > 0 {
    missedBlocksPercentage = (missedBlocks * 10000) / expectedBlocks
}

// 判断是否故障
isFaulty := missedBlocksPercentage >= d.config.MissedBlocksPercentage

// 创建故障标志
faultFlag := FaultFlagInfo{
    NodeAddress:          validator.Address,
    IsFaulty:             isFaulty,
    MissedBlocks:         missedBlocks,
    ActualBlocks:         actualBlocks,
    ExpectedBlocks:       expectedBlocks,
    MissedBlocksPercentage: missedBlocksPercentage,
    EpochNumber:          epochToCheck,
    Reason:               fmt.Sprintf("Minor Offense: missed blocks percentage %d >= %d", 
                                     missedBlocksPercentage, d.config.MissedBlocksPercentage),
}
```

### 4.2 严重违规检测（双重签名检测）

**位置**：`consensus/dpos/extra.go` - `ValidateFinalizedData`

**检测逻辑**：
1. 使用 `DoubleSigningDetector` 检测双重签名
2. 检测条件：同一验证者在相同或相近高度（±1）签署了不同的区块
3. 如果检测到双重签名，立即标记为严重违规

**关键代码**：
```go
// 检测双重签名
if d.doubleSigningDetector != nil {
    isDoubleSigning, conflictingSig := d.doubleSigningDetector.DetectDoubleSigning(
        validatorAddr,
        block.Number(),
        block.Hash(),
    )
    
    if isDoubleSigning {
        // 创建严重违规故障标志
        faultFlag := FaultFlagInfo{
            NodeAddress: validatorAddr,
            IsFaulty:    true,
            EpochNumber: d.getCurrentEpochByBlock(block.Number()),
            Reason:      fmt.Sprintf("Severe Offense: double signing detected at height %d", 
                                    conflictingSig.BlockHeight),
        }
        
        // 执行严重违规削减
        err := d.executeSlashing(
            validatorAddr,
            d.config.SevereOffenseSlashRate,
            block.Number(),
            d.getCurrentEpochByBlock(block.Number()),
            faultFlag.Reason,
            0, 0, // 双重签名不涉及漏块
        )
    }
}
```

### 4.3 双重签名检测器（`double_signing.go`）

```go
type DoubleSigningDetector struct {
    signatures map[string][]*BlockSignature // key: validatorAddr.String()
    mutex      sync.RWMutex
    logger     hclog.Logger
}

type BlockSignature struct {
    ValidatorAddr types.Address
    BlockHeight   uint64
    BlockHash     types.Hash
    Timestamp     uint64
}

// DetectDoubleSigning 检测双重签名
func (d *DoubleSigningDetector) DetectDoubleSigning(
    validatorAddr types.Address,
    blockHeight uint64,
    blockHash types.Hash,
) (bool, *BlockSignature)
```

## 五、削减惩罚机制

### 5.1 削减执行流程（`slashing.go`）

**核心函数**：`executeSlashing`

**执行步骤**：
1. 获取验证者当前 `VotingPower`（从 `DelegateInfo` 表）
2. 计算削减金额：`slashAmount = (votingPower * slashRate) / 10000`
3. 计算新的 `VotingPower`：`newVotingPower = votingPower - slashAmount`
4. 更新验证者的 `VotingPower`（直接更新 `DelegateInfo` 表）
5. 按比例削减每个投票者的投票金额：
   - 获取所有投票给该验证者的投票者
   - 对每个投票者，按比例削减其 `DelegateVotes[validatorAddr]`
   - 更新 `VoterInfo.DelegateVotes` 和 `VoterInfo.SlashingRecords`
   - 更新 `StakeInfo.Amount` 和 `StakeInfo.SlashingRecords`
6. 记录削减历史：
   - 保存 `SlashingHistory` 到数据库
   - 更新每个投票者的 `SlashingRecord`
7. 记录代币销毁（不实际扣除账户余额，仅记录）

**关键代码**：
```go
func (d *DPoS) executeSlashing(
    validatorAddr types.Address,
    slashRate uint64,
    blockNumber uint64,
    epochNumber uint64,
    reason string,
    missedBlocks uint64,
    missedBlocksPercentage uint64,
) error {
    // 1. 获取当前VotingPower
    oldVotingPower, err := d.getVotingPowerFromDatabase(validatorAddr)
    if err != nil {
        return fmt.Errorf("failed to get voting power: %w", err)
    }
    
    // 2. 计算削减金额
    slashAmount := new(big.Int).Mul(oldVotingPower, big.NewInt(int64(slashRate)))
    slashAmount.Div(slashAmount, big.NewInt(10000))
    
    // 3. 计算新VotingPower
    newVotingPower := new(big.Int).Sub(oldVotingPower, slashAmount)
    
    // 4. 更新验证者VotingPower
    err = d.updateVotingPowerInDatabase(validatorAddr, newVotingPower)
    if err != nil {
        return fmt.Errorf("failed to update voting power: %w", err)
    }
    
    // 5. 获取所有投票者并按比例削减
    voters := d.getVotersForValidator(validatorAddr)
    for _, voter := range voters {
        // 获取该投票者对验证者的投票金额
        oldVoteAmount := voter.DelegateVotes[validatorAddr]
        if oldVoteAmount == nil || oldVoteAmount.Sign() == 0 {
            continue
        }
        
        // 计算削减金额（按比例）
        voterSlashAmount := new(big.Int).Mul(oldVoteAmount, big.NewInt(int64(slashRate)))
        voterSlashAmount.Div(voterSlashAmount, big.NewInt(10000))
        
        // 计算新投票金额
        newVoteAmount := new(big.Int).Sub(oldVoteAmount, voterSlashAmount)
        
        // 更新投票者信息
        err = d.updateVoterVoteAmountForValidator(
            voter.Address,
            validatorAddr,
            newVoteAmount,
            oldVoteAmount,
            voterSlashAmount,
            slashRate,
            blockNumber,
            epochNumber,
            reason,
            missedBlocks,
            missedBlocksPercentage,
        )
        if err != nil {
            d.logger.Error("Failed to update voter vote amount", "error", err)
            continue
        }
    }
    
    // 6. 记录削减历史
    slashingHistory := &SlashingHistory{
        ValidatorAddr:          validatorAddr,
        BlockNumber:            blockNumber,
        EpochNumber:            epochNumber,
        Timestamp:              uint64(time.Now().Unix()),
        SlashAmount:            slashAmount,
        OldVotingPower:         oldVotingPower,
        NewVotingPower:         newVotingPower,
        SlashRate:              slashRate,
        Reason:                 reason,
        MissedBlocks:           missedBlocks,
        MissedBlocksPercentage: missedBlocksPercentage,
    }
    
    err = d.state.StakeStore.SaveSlashingHistory(validatorAddr, slashingHistory)
    if err != nil {
        d.logger.Error("Failed to save slashing history", "error", err)
    }
    
    // 7. 记录代币销毁（不实际扣除账户余额）
    err = d.burnSlashedTokens(slashAmount)
    if err != nil {
        d.logger.Error("Failed to record token burn", "error", err)
    }
    
    return nil
}
```

### 5.2 更新投票者投票金额（`slashing.go`）

```go
func (d *DPoS) updateVoterVoteAmountForValidator(
    voterAddr types.Address,
    validatorAddr types.Address,
    newVoteAmount *big.Int,
    oldVoteAmount *big.Int,
    slashAmount *big.Int,
    slashRate uint64,
    blockNumber uint64,
    epochNumber uint64,
    reason string,
    missedBlocks uint64,
    missedBlocksPercentage uint64,
) error {
    // 1. 获取VoterInfo
    voterInfo, err := d.state.StakeStore.getVoterInfo(voterAddr, nil)
    if err != nil {
        return fmt.Errorf("failed to get voter info: %w", err)
    }
    
    // 2. 初始化DelegateVotes和SlashingRecords
    if voterInfo.DelegateVotes == nil {
        voterInfo.DelegateVotes = make(map[types.Address]*big.Int)
    }
    if voterInfo.SlashingRecords == nil {
        voterInfo.SlashingRecords = make(map[types.Address][]*SlashingRecord)
    }
    
    // 3. 更新DelegateVotes
    voterInfo.DelegateVotes[validatorAddr] = newVoteAmount
    
    // 4. 创建削减记录
    slashingRecord := &SlashingRecord{
        ValidatorAddr:          validatorAddr,
        BlockNumber:            blockNumber,
        EpochNumber:            epochNumber,
        Timestamp:              uint64(time.Now().Unix()),
        SlashAmount:            slashAmount,
        OldVoteAmount:          oldVoteAmount,
        NewVoteAmount:          newVoteAmount,
        SlashRate:              slashRate,
        Reason:                 reason,
        MissedBlocks:           missedBlocks,
        MissedBlocksPercentage: missedBlocksPercentage,
    }
    
    // 5. 添加到削减历史
    voterInfo.SlashingRecords[validatorAddr] = append(
        voterInfo.SlashingRecords[validatorAddr],
        slashingRecord,
    )
    
    // 6. 保存VoterInfo
    err = d.state.StakeStore.setVoterInfo(voterAddr, voterInfo, nil)
    if err != nil {
        return fmt.Errorf("failed to save voter info: %w", err)
    }
    
    // 7. 更新StakeInfo
    err = d.updateStakingInfoAfterSlashing(
        voterAddr,
        validatorAddr,
        newVoteAmount,
        oldVoteAmount,
        slashingRecord,
    )
    if err != nil {
        d.logger.Warn("Failed to update staking info", "error", err)
    }
    
    return nil
}
```

### 5.3 代币销毁记录（`slashing.go`）

```go
func (d *DPoS) burnSlashedTokens(amount *big.Int) error {
    // 注意：不实际扣除账户余额，因为VotingPower不等于账户余额
    // VotingPower是质押权重，可能来自多个投票者
    // 这里仅记录销毁金额，用于统计和审计
    
    // 可以记录到数据库的销毁统计表中
    // 或者记录到日志中
    
    d.logger.Info("🔥 记录代币销毁",
        "amount", amount.String(),
        "note", "VotingPower削减，不扣除账户余额")
    
    return nil
}
```

## 六、调用点

### 6.1 轻度违规削减调用

**位置1**：`consensus/dpos/validator_mgmt_fault.go` - `detectValidatorFaults`

```go
// 在检测到故障后
if isFaulty {
    // 执行轻度违规削减
    err := d.executeSlashing(
        validator.Address,
        d.config.MinorOffenseSlashRate,
        blockNumber,
        epochToCheck,
        faultFlag.Reason,
        missedBlocks,
        missedBlocksPercentage,
    )
    if err != nil {
        d.logger.Error("Failed to execute slashing", "error", err)
    }
}
```

**位置2**：`consensus/dpos/block_builder.go` - epoch 结束时

```go
// 在epoch结束时调用detectValidatorFaults后
faultFlags, err := d.detectValidatorFaults(blockNumber)
if err == nil {
    for _, faultFlag := range faultFlags {
        if faultFlag.IsFaulty {
            // 执行削减
            d.executeSlashing(...)
        }
    }
}
```

### 6.2 严重违规削减调用

**位置**：`consensus/dpos/extra.go` - `ValidateFinalizedData`

```go
// 在验证区块时检测双重签名
if isDoubleSigning {
    err := d.executeSlashing(
        validatorAddr,
        d.config.SevereOffenseSlashRate,
        block.Number(),
        d.getCurrentEpochByBlock(block.Number()),
        "Severe Offense: double signing",
        0, 0,
    )
}
```

## 七、数据库存储

### 7.1 新增数据库表/桶

1. **SlashingHistory** 桶：存储验证者的削减历史
   - Key: `validatorAddr + blockNumber`
   - Value: JSON 序列化的 `SlashingHistory`

2. **VoterInfo** 桶：扩展存储 `DelegateVotes` 和 `SlashingRecords`

3. **StakingInfo** 桶：扩展存储 `OriginalAmount` 和 `SlashingRecords`

### 7.2 数据库操作函数（`state_store_stake.go`）

```go
// SaveSlashingHistory 保存削减历史
func (s *StakeStore) SaveSlashingHistory(validatorAddr types.Address, history *SlashingHistory) error

// GetSlashingHistory 获取削减历史
func (s *StakeStore) GetSlashingHistory(validatorAddr types.Address) ([]*SlashingHistory, error)
```

## 八、状态管理

### 8.1 故障状态与削减的关系

- **故障状态（isFaulty）**：
  - `isFaulty = true`：验证者被标记为故障（相当于"入狱"）
  - `isFaulty = false`：验证者恢复正常（通过恢复提案）

- **削减惩罚**：
  - 轻度违规：削减 0.5%，标记 `isFaulty = true`
  - 严重违规：削减 10%，标记 `isFaulty = true`（永久封禁）

- **恢复机制**：
  - 使用现有的恢复提案机制
  - 提案通过后，设置 `isFaulty = false`

### 8.2 削减后的状态同步

1. **验证者权重**：直接更新 `DelegateInfo.VotingPower`
2. **投票者投票金额**：更新 `VoterInfo.DelegateVotes[validatorAddr]`
3. **质押记录**：更新 `StakingInfo.Amount`（削减后金额）
4. **削减历史**：保存到 `SlashingHistory` 和 `SlashingRecord`

## 九、查询接口

### 9.1 削减历史查询

- 查询验证者的削减历史：`GetSlashingHistory(validatorAddr)`
- 查询投票者的削减记录：从 `VoterInfo.SlashingRecords` 读取

### 9.2 RPC 接口扩展

可以添加以下 RPC 接口：
- `dpos_getSlashingHistory`：查询验证者削减历史
- `dpos_getVoterSlashingRecords`：查询投票者削减记录

## 十、注意事项

1. **VotingPower 与账户余额的区别**：
   - `VotingPower` 是质押权重，不等于账户余额
   - 削减时只减少 `VotingPower`，不扣除账户余额
   - 代币销毁仅记录，不实际转账

2. **按比例削减**：
   - 验证者的 `VotingPower` 按削减率直接扣除
   - 每个投票者的投票金额按相同比例削减
   - 确保削减后总权重的一致性

3. **数据一致性**：
   - 削减后需要同步更新：
     - `DelegateInfo.VotingPower`
     - `VoterInfo.DelegateVotes`
     - `StakingInfo.Amount`
   - 确保 `GetValidatorVotingDetails` 能正确显示削减后的金额

4. **性能考虑**：
   - 削减时需要遍历所有投票者，可能影响性能
   - 可以考虑批量更新或异步处理

5. **恢复机制**：
   - 削减是不可逆的，即使恢复故障状态，削减的权重不会恢复
   - 恢复提案只能恢复 `isFaulty` 状态，不能恢复被削减的权重

## 十一、实现文件清单

### 11.1 需要修改的文件

1. `consensus/dpos/dpos_config.go` - 添加配置参数
2. `command/server/config/config.go` - 添加配置参数和默认值
3. `consensus/dpos/types.go` - 扩展数据结构
4. `consensus/dpos/extra.go` - 扩展 `FaultFlagInfo`，添加双重签名检测
5. `consensus/dpos/validator_mgmt_fault.go` - 修改漏块检测逻辑
6. `consensus/dpos/block_builder.go` - 添加削减调用
7. `consensus/dpos/state_store_stake.go` - 添加削减历史存储
8. `consensus/dpos/voting_weight.go` - 可能需要更新投票权重计算

### 11.2 需要新建的文件

1. `consensus/dpos/slashing.go` - 削减执行逻辑
2. `consensus/dpos/double_signing.go` - 双重签名检测器

## 十二、测试要点

1. **轻度违规测试**：
   - 验证漏块率计算正确性
   - 验证削减金额计算正确性
   - 验证投票者投票金额按比例削减

2. **严重违规测试**：
   - 验证双重签名检测准确性
   - 验证严重违规削减执行

3. **数据一致性测试**：
   - 验证削减后 `VotingPower` 一致性
   - 验证 `GetValidatorVotingDetails` 返回正确数据

4. **恢复机制测试**：
   - 验证恢复提案能正确恢复 `isFaulty` 状态
   - 验证削减历史正确保存

