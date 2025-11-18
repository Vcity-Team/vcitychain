# 消减惩罚机制完整方案

## 一、概述

本方案实现了两种违规类型的检测和惩罚机制：
- **轻度违规（Minor Offense）**：漏块率超过阈值（missed_blocks_percentage）
- **严重违规（Severe Offense）**：双重签名（Double Signing）

**核心原则**：
- 消减从验证者的总 `VotingPower` 扣除（包含所有委托人的投票）
- 按比例更新每个委托人的 `DelegateVotes`（每个验证者对应的投票金额）
- 更新 `StakeInfo` 表中的所有相关记录
- 记录消减历史，便于查询和审计

## 二、配置参数

### 2.1 新增配置项

**文件**：`consensus/dpos/dpos_config.go` 和 `command/server/config/config.go`

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

### 3.1 扩展 `FaultFlagInfo`（`consensus/dpos/extra.go`）

```go
type FaultFlagInfo struct {
    NodeAddress            types.Address `json:"node_address"`
    IsFaulty               bool          `json:"is_faulty"`
    MissedBlocks           uint64        `json:"missed_blocks"`
    ActualBlocks           uint64        `json:"actual_blocks"`
    ExpectedBlocks          uint64        `json:"expected_blocks"`           // 🆕 预期出块数
    MissedBlocksPercentage uint64        `json:"missed_blocks_percentage"` // 🆕 漏块率（基点）
    LastUpdateTime         uint64        `json:"last_update_time"`
    EpochNumber            uint64        `json:"epoch_number"`
    LastFaultyEpoch        uint64        `json:"last_faulty_epoch"`
    Reason                 string        `json:"reason"`                    // 包含违规类型和削减率
}
```

### 3.2 扩展 `VoterInfo`（`consensus/dpos/types.go`）

```go
type VoterInfo struct {
    Address         types.Address                    `json:"address"`
    // VotingPower     *big.Int                         // ❌ 删除：由DelegateVotes累加得到
    // VotedDelegates  []types.Address                  // ❌ 删除：由DelegateVotes的key得到
    DelegateVotes   map[types.Address]*big.Int       `json:"delegateVotes"`   // 🆕 delegate -> 投票金额（削减后）
    SlashingRecords map[types.Address][]*SlashingRecord `json:"slashingRecords"` // 🆕 削减历史记录
    LastVoteTime    uint64                           `json:"lastVoteTime"`
    LockedUntil     uint64                           `json:"lockedUntil"`
    Nonce           map[uint64]bool                   `json:"nonce"`
}
```

**说明**：
- `VotingPower`：**已删除**，由 `DelegateVotes` 累加得到
- `VotedDelegates`：**已删除**，由 `DelegateVotes` 的 key 得到
- `DelegateVotes`：核心字段，存储每个验证者对应的投票金额（削减后）
- `SlashingRecords`：存储每个验证者的消减历史

### 3.3 新增 `SlashingRecord`（`consensus/dpos/types.go`）

```go
// SlashingRecord 削减记录（存储在VoterInfo中）
type SlashingRecord struct {
    ValidatorAddr          types.Address `json:"validator_addr"`
    BlockNumber            uint64        `json:"block_number"`
    EpochNumber            uint64        `json:"epoch_number"`
    Timestamp              uint64        `json:"timestamp"`
    SlashAmount            *big.Int      `json:"slash_amount"`            // 本次削减的金额
    OldVoteAmount          *big.Int      `json:"old_vote_amount"`        // 削减前的投票金额
    NewVoteAmount          *big.Int      `json:"new_vote_amount"`        // 削减后的投票金额
    SlashRate              uint64        `json:"slash_rate"`              // 削减率（基点）
    Reason                 string        `json:"reason"`                  // 削减原因
    MissedBlocks           uint64        `json:"missed_blocks,omitempty"`  // 漏块数（轻度违规）
    MissedBlocksPercentage uint64        `json:"missed_blocks_percentage,omitempty"` // 漏块率（轻度违规）
}
```

### 3.4 新增 `SlashingHistory`（`consensus/dpos/slashing.go`）

```go
// SlashingHistory 处罚历史记录（存储在数据库中，用于验证者）
type SlashingHistory struct {
    ValidatorAddr          types.Address `json:"validator_addr"`
    BlockNumber            uint64        `json:"block_number"`
    EpochNumber            uint64        `json:"epoch_number"`
    Timestamp              uint64        `json:"timestamp"`
    SlashAmount            *big.Int      `json:"slash_amount"`            // 本次削减的总金额
    OldVotingPower         *big.Int      `json:"old_voting_power"`        // 削减前的总投票权重
    NewVotingPower         *big.Int      `json:"new_voting_power"`        // 削减后的总投票权重
    SlashRate              uint64        `json:"slash_rate"`               // 削减率（基点）
    Reason                 string        `json:"reason"`                   // 削减原因
    MissedBlocks           uint64        `json:"missed_blocks,omitempty"`  // 漏块数（轻度违规）
    MissedBlocksPercentage uint64        `json:"missed_blocks_percentage,omitempty"` // 漏块率（轻度违规）
    DoubleSigningHeight    uint64        `json:"double_signing_height,omitempty"`     // 双重签名高度（严重违规）
}
```

### 3.5 扩展 `StakeInfo`（`consensus/dpos/types.go`）

```go
type StakeInfo struct {
    Staker         types.Address          `json:"staker"`
    Amount         *big.Int               `json:"amount"`                  // 当前金额（削减后）
    OriginalAmount *big.Int               `json:"originalAmount,omitempty"` // 🆕 原始投票金额（第一次投票时的金额，削减前）
    StartTime      uint64                 `json:"startTime"`
    EndTime        uint64                 `json:"endTime"`
    IsLocked       bool                   `json:"isLocked"`
    IsActive       bool                   `json:"isActive"`
    Rewards        *big.Int               `json:"rewards"`
    Delegate       types.Address          `json:"delegate"`                // 投票给哪个验证者
    FaultFlag      map[string]interface{} `json:"faultFlag,omitempty"`
    SlashingRecords []*SlashingRecord     `json:"slashingRecords,omitempty"` // 🆕 削减历史（按时间顺序）
}
```

**说明**：
- `OriginalAmount`：第一次投票时的原始金额，用于解质押时对比
- `Amount`：当前金额（经过削减后）
- `SlashingRecords`：所有削减历史记录，按时间顺序排列

## 四、故障检测机制

### 4.1 轻度违规检测（`consensus/dpos/validator_mgmt_fault.go`）

**触发时机**：每个 epoch 结束时（`block_builder.go`）

```go
func (d *DPoS) detectValidatorFaults(blockNumber uint64, epochNumber uint64) error {
    // 1. 计算每个验证者的漏块率
    for _, validator := range d.delegates {
        expectedBlocks := d.calculateExpectedBlocks(validator, epochNumber)
        actualBlocks := validator.ProducedBlocks
        missedBlocks := expectedBlocks - actualBlocks
        
        // 计算漏块率（基点）
        missedBlocksPercentage := uint64(0)
        if expectedBlocks > 0 {
            missedBlocksPercentage = (missedBlocks * 10000) / expectedBlocks
        }
        
        // 2. 判断是否超过阈值
        isFaulty := missedBlocksPercentage >= d.config.MissedBlocksPercentage
        
        if isFaulty {
            // 3. 创建故障标志
            faultInfo := &FaultFlagInfo{
                NodeAddress:            validator.Address,
                IsFaulty:               true,
                MissedBlocks:           missedBlocks,
                ActualBlocks:           actualBlocks,
                ExpectedBlocks:          expectedBlocks,
                MissedBlocksPercentage: missedBlocksPercentage,
                LastUpdateTime:         uint64(time.Now().Unix()),
                EpochNumber:            epochNumber,
                LastFaultyEpoch:        epochNumber,
                Reason:                 fmt.Sprintf("Minor Offense: missed_blocks_percentage=%d", missedBlocksPercentage),
            }
            
            // 4. 执行消减
            err := d.executeSlashing(
                validator.Address,
                d.config.MinorOffenseSlashRate,
                blockNumber,
                epochNumber,
                faultInfo.Reason,
                missedBlocks,
                missedBlocksPercentage,
            )
            if err != nil {
                d.logger.Error("Failed to execute slashing", "error", err)
            }
        }
    }
    
    return nil
}
```

### 4.2 严重违规检测（`consensus/dpos/extra.go`）

**触发时机**：区块验证时（`ValidateFinalizedData`）

```go
func (d *DPoS) ValidateFinalizedData(header *types.Header, extra *Extra) error {
    // 1. 检测双重签名
    if d.doubleSigningDetector != nil {
        validatorAddr := header.Miner
        blockHeight := header.Number
        blockHash := header.Hash
        
        isDoubleSigning, existingSig := d.doubleSigningDetector.DetectDoubleSigning(
            validatorAddr,
            blockHeight,
            blockHash,
        )
        
        if isDoubleSigning {
            // 2. 创建故障标志
            faultInfo := &FaultFlagInfo{
                NodeAddress:     validatorAddr,
                IsFaulty:        true,
                LastUpdateTime:  uint64(time.Now().Unix()),
                EpochNumber:     d.getEpochNumber(blockHeight),
                LastFaultyEpoch:  d.getEpochNumber(blockHeight),
                Reason:          fmt.Sprintf("Severe Offense: Double Signing at height %d", blockHeight),
            }
            
            // 3. 执行消减
            err := d.executeSlashing(
                validatorAddr,
                d.config.SevereOffenseSlashRate,
                blockHeight,
                d.getEpochNumber(blockHeight),
                faultInfo.Reason,
                0,   // missedBlocks
                0,   // missedBlocksPercentage
            )
            if err != nil {
                d.logger.Error("Failed to execute slashing for double signing", "error", err)
            }
            
            // 4. 永久封禁（标记为故障，后续通过治理提案恢复）
            d.updateMemoryFaultStatus(validatorAddr, faultInfo)
        }
    }
    
    return nil
}
```

## 五、消减执行机制

### 5.1 核心消减函数（`consensus/dpos/slashing.go`）

**核心逻辑**：按照比例消减每个投票者的质押金额，然后累加得到验证者的新 VotingPower

```go
// executeSlashing 执行消减（按比例消减每个投票者的质押金额）
func (d *DPoS) executeSlashing(
    validatorAddr types.Address,
    slashRate uint64,
    blockNumber uint64,
    epochNumber uint64,
    reason string,
    missedBlocks uint64,
    missedBlocksPercentage uint64,
) error {
    d.logger.Info("🔨 开始执行消减",
        "validator", validatorAddr.String(),
        "slashRate", slashRate,
        "blockNumber", blockNumber,
        "reason", reason)
    
    // 1. 获取验证者的当前 VotingPower（用于记录历史）
    validator, err := d.state.StakeStore.GetDelegateInfo(validatorAddr)
    if err != nil || validator == nil {
        return fmt.Errorf("failed to get validator info: %w", err)
    }
    
    oldVotingPower := new(big.Int).Set(validator.VotingPower)
    if oldVotingPower.Sign() == 0 {
        d.logger.Warn("Validator has zero voting power, skip slashing")
        return nil
    }
    
    // 2. 获取所有投票给该验证者的记录
    allStakes, err := d.state.StakeStore.GetStakingInfo()
    if err != nil {
        return fmt.Errorf("failed to get staking info: %w", err)
    }
    
    // 筛选出投票给该验证者的记录
    var validatorStakes []*StakeInfo
    for _, stake := range allStakes {
        if stake != nil && stake.Delegate == validatorAddr {
            validatorStakes = append(validatorStakes, stake)
        }
    }
    
    if len(validatorStakes) == 0 {
        d.logger.Warn("No stakes found for validator, skip slashing")
        return nil
    }
    
    // 3. 按投票者聚合（一个投票者可能有多条记录）
    voterStakesMap := make(map[types.Address]*big.Int) // voter -> 总投票金额
    for _, stake := range validatorStakes {
        if stake.Amount != nil && stake.Amount.Sign() > 0 {
            if existing, exists := voterStakesMap[stake.Staker]; exists {
                existing.Add(existing, stake.Amount)
            } else {
                voterStakesMap[stake.Staker] = new(big.Int).Set(stake.Amount)
            }
        }
    }
    
    // 4. 按比例消减每个投票者的质押金额
    totalSlashAmount := big.NewInt(0)  // 总削减金额
    newVotingPower := big.NewInt(0)    // 新的总投票权重
    
    for voterAddr, oldVoteAmount := range voterStakesMap {
        // 4.1 计算该投票者的削减金额（按比例）
        voterSlashAmount := new(big.Int).Mul(oldVoteAmount, big.NewInt(int64(slashRate)))
        voterSlashAmount.Div(voterSlashAmount, big.NewInt(10000)) // 基点转换
        
        // 4.2 计算削减后的金额
        newVoteAmount := new(big.Int).Sub(oldVoteAmount, voterSlashAmount)
        if newVoteAmount.Sign() < 0 {
            newVoteAmount = big.NewInt(0)
        }
        
        totalSlashAmount.Add(totalSlashAmount, voterSlashAmount)
        newVotingPower.Add(newVotingPower, newVoteAmount)
        
        d.logger.Info("🔨 消减投票者",
            "voter", voterAddr.String(),
            "oldAmount", oldVoteAmount.String(),
            "slashAmount", voterSlashAmount.String(),
            "newAmount", newVoteAmount.String())
        
        // 4.3 更新该投票者的所有 StakeInfo 记录
        for _, stake := range validatorStakes {
            if stake.Staker == voterAddr {
                // 按该记录在总金额中的比例计算削减金额
                recordSlashAmount := new(big.Int).Mul(stake.Amount, big.NewInt(int64(slashRate)))
                recordSlashAmount.Div(recordSlashAmount, big.NewInt(10000))
                
                recordNewAmount := new(big.Int).Sub(stake.Amount, recordSlashAmount)
                if recordNewAmount.Sign() < 0 {
                    recordNewAmount = big.NewInt(0)
                }
                
                // 🆕 保存原始金额（如果还没有保存）
                if stake.OriginalAmount == nil {
                    stake.OriginalAmount = new(big.Int).Set(stake.Amount)
                }
                
                // 创建削减记录
                slashingRecord := &SlashingRecord{
                    ValidatorAddr:          validatorAddr,
                    BlockNumber:            blockNumber,
                    EpochNumber:            epochNumber,
                    Timestamp:              uint64(time.Now().Unix()),
                    SlashAmount:            recordSlashAmount,
                    OldVoteAmount:          new(big.Int).Set(stake.Amount), // 削减前的金额
                    NewVoteAmount:          recordNewAmount,              // 削减后的金额
                    SlashRate:              slashRate,
                    Reason:                 reason,
                    MissedBlocks:           missedBlocks,
                    MissedBlocksPercentage: missedBlocksPercentage,
                }
                
                // 更新 StakeInfo
                err := d.updateStakingInfoAfterSlashing(
                    voterAddr,
                    validatorAddr,
                    recordNewAmount,
                    stake.Amount,
                    slashingRecord,
                )
                if err != nil {
                    d.logger.Warn("Failed to update staking info",
                        "voter", voterAddr.String(),
                        "error", err)
                }
            }
        }
        
        // 4.4 更新 VoterInfo.DelegateVotes
        err := d.updateVoterVoteAmountForValidator(
            voterAddr,
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
            d.logger.Warn("Failed to update voter vote amount",
                "voter", voterAddr.String(),
                "error", err)
        }
    }
    
    // 5. 更新验证者的 VotingPower（累加所有削减后的金额）
    validator.VotingPower = newVotingPower
    if err := d.state.StakeStore.setDelegateInfo(validatorAddr, validator); err != nil {
        return fmt.Errorf("failed to update validator voting power: %w", err)
    }
    
    // 6. 更新内存中的验证者信息
    for i, delegate := range d.delegates {
        if delegate.Address == validatorAddr {
            d.delegates[i].VotingPower = newVotingPower
            break
        }
    }
    
    // 7. 保存消减历史（验证者级别）
    slashingHistory := &SlashingHistory{
        ValidatorAddr:          validatorAddr,
        BlockNumber:            blockNumber,
        EpochNumber:            epochNumber,
        Timestamp:              uint64(time.Now().Unix()),
        SlashAmount:            totalSlashAmount,
        OldVotingPower:         oldVotingPower,
        NewVotingPower:         newVotingPower,
        SlashRate:              slashRate,
        Reason:                 reason,
        MissedBlocks:           missedBlocks,
        MissedBlocksPercentage: missedBlocksPercentage,
    }
    if err := d.state.StakeStore.SaveSlashingHistory(slashingHistory); err != nil {
        d.logger.Warn("Failed to save slashing history", "error", err)
    }
    
    // 8. 记录代币销毁（不实际扣除账户余额，只记录）
    if err := d.burnSlashedTokens(totalSlashAmount); err != nil {
        d.logger.Warn("Failed to record token burn", "error", err)
    }
    
    d.logger.Info("✅ 消减执行完成",
        "validator", validatorAddr.String(),
        "oldVotingPower", oldVotingPower.String(),
        "totalSlashAmount", totalSlashAmount.String(),
        "newVotingPower", newVotingPower.String())
    
    return nil
}
```

**关键改进**：
1. **先消减每个投票者**：遍历所有投票记录，按比例消减每个投票者的质押金额
2. **累加得到新 VotingPower**：将所有削减后的金额累加，得到验证者的新 VotingPower
3. **数据一致性**：确保 `DelegateVotes`、`StakeInfo` 和 `DelegateInfo.VotingPower` 一致

### 5.2 更新委托人的投票金额（`consensus/dpos/slashing.go`）

```go
// updateVoterVoteAmountForValidator 更新委托人对特定验证者的投票金额并记录削减
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
    // 1. 获取 VoterInfo
    voterInfo, err := d.state.StakeStore.getVoterInfo(voterAddr, nil)
    if err != nil {
        return fmt.Errorf("failed to get voter info: %w", err)
    }
    
    if voterInfo == nil {
        return fmt.Errorf("voter info not found: %s", voterAddr.String())
    }
    
    // 2. 初始化 DelegateVotes 和 SlashingRecords（如果不存在）
    if voterInfo.DelegateVotes == nil {
        voterInfo.DelegateVotes = make(map[types.Address]*big.Int)
    }
    if voterInfo.SlashingRecords == nil {
        voterInfo.SlashingRecords = make(map[types.Address][]*SlashingRecord)
    }
    
    // 3. 更新 DelegateVotes
    voterInfo.DelegateVotes[validatorAddr] = newVoteAmount
    
    // 4. VotedDelegates 已删除，由 DelegateVotes 的 key 得到（不需要存储）
    
    // 5. 创建削减记录
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
    
    // 6. 添加到 SlashingRecords
    voterInfo.SlashingRecords[validatorAddr] = append(
        voterInfo.SlashingRecords[validatorAddr],
        slashingRecord,
    )
    
    // 7. VotingPower 已删除，由 DelegateVotes 累加得到（不需要存储）
    
    // 8. 更新内存中的 VoterInfo
    d.voters[voterAddr] = voterInfo
    
    // 9. 保存到数据库
    if err := d.state.StakeStore.setVoterInfo(voterAddr, voterInfo, nil); err != nil {
        return fmt.Errorf("failed to save voter info: %w", err)
    }
    
    return nil
}
```

### 5.3 更新 StakeInfo 记录（`consensus/dpos/slashing.go`）

```go
// updateStakingInfoAfterSlashing 更新StakingInfo记录（削减后）
func (d *DPoS) updateStakingInfoAfterSlashing(
    voterAddr types.Address,
    validatorAddr types.Address,
    newAmount *big.Int,
    oldAmount *big.Int,
    slashingRecord *SlashingRecord,
) error {
    // 注意：StakingInfo 使用复合 key (staker + delegate + timestamp)
    // 需要找到所有相关的记录并更新
    
    // 1. 获取所有 StakeInfo 记录
    allStakes, err := d.state.StakeStore.GetStakingInfo()
    if err != nil {
        return fmt.Errorf("failed to get staking info: %w", err)
    }
    
    // 2. 找到所有匹配的记录并更新
    dbTx, err := d.state.beginDBTransaction(true)
    if err != nil {
        return fmt.Errorf("failed to begin transaction: %w", err)
    }
    defer dbTx.Rollback()
    
    for _, stake := range allStakes {
        if stake != nil && stake.Staker == voterAddr && stake.Delegate == validatorAddr {
            // 3. 更新金额（已经在 executeSlashing 中计算好）
            stake.Amount = newAmount
            
            // 4. 🆕 保存原始金额（如果还没有保存）
            if stake.OriginalAmount == nil {
                stake.OriginalAmount = new(big.Int).Set(oldAmount) // 使用削减前的金额作为原始金额
            }
            
            // 5. 添加削减记录
            if stake.SlashingRecords == nil {
                stake.SlashingRecords = make([]*SlashingRecord, 0)
            }
            stake.SlashingRecords = append(stake.SlashingRecords, slashingRecord)
            
            // 6. 保存到数据库
            // 注意：需要从原始记录的 timestamp 来更新
            // 这里需要从数据库中找到对应的 key（staker + delegate + timestamp）
            // 简化处理：遍历所有可能的 timestamp，找到匹配的记录
            // TODO: 优化：在 executeSlashing 中传递 timestamp
            // 或者：使用新的更新方法，直接通过 staker + delegate 查找所有记录
        }
    }
    
    if err := dbTx.Commit(); err != nil {
        return fmt.Errorf("failed to commit transaction: %w", err)
    }
    
    return nil
}
```

## 六、数据表更新

### 6.1 需要更新的数据表

**1. DelegateInfo 表**
- 更新 `VotingPower`（削减后）

**2. VoterInfo 表**
- 更新 `DelegateVotes[validatorAddr]`（削减后的金额）
- 更新 `SlashingRecords[validatorAddr]`（添加削减记录）
- 注意：`VotingPower` 和 `VotedDelegates` 已删除，由 `DelegateVotes` 计算得到

**3. StakingInfo 表**
- 更新所有 `stake.Delegate == validatorAddr` 的记录的 `Amount`（削减后）
- 添加 `SlashingRecords`（削减历史）
- 可选：添加 `OriginalAmount`（原始金额）

**4. SlashingHistory 表（新增）**
- 保存验证者级别的削减历史

### 6.2 数据一致性

**关键点**：
- `VoterInfo.DelegateVotes[validatorAddr]` = 该投票者对该验证者的当前投票金额（削减后）
- `StakeInfo.Amount`（`stake.Delegate == validatorAddr` 且 `stake.Staker == voterAddr`）的累加 = `VoterInfo.DelegateVotes[validatorAddr]`
- `DelegateInfo.VotingPower` = 所有 `VoterInfo.DelegateVotes[validatorAddr]` 的累加
- `VoterInfo.VotingPower`（已删除）= 所有 `VoterInfo.DelegateVotes` 的累加
- `VoterInfo.VotedDelegates`（已删除）= `VoterInfo.DelegateVotes` 的所有 key

## 七、GetStakingInfo 修复

### 7.1 问题

`DPoS.GetStakingInfo()` 之前只返回第一个验证者，丢失了其他验证者的信息。

### 7.2 修复方案

**文件**：`consensus/dpos/validator_mgmt_stake.go`

```go
func (d *DPoS) GetStakingInfo(blockNumber uint64, staker types.Address) (*StakeInfo, error) {
    // 1. 优先从数据库读取，确保数据准确（包含削减后的金额）
    if d.state != nil && d.state.StakeStore != nil {
        allStakes, err := d.state.StakeStore.GetStakingInfo()
        if err == nil {
            // 2. 筛选出该投票者的所有记录
            var stakes []*StakeInfo
            for _, stake := range allStakes {
                if stake != nil && stake.Staker == staker {
                    stakes = append(stakes, stake)
                }
            }
            
            if len(stakes) > 0 {
                // 3. 聚合所有记录：累加金额，合并时间范围等
                totalAmount := big.NewInt(0)
                var earliestStartTime uint64 = ^uint64(0)
                var latestEndTime uint64 = 0
                isLocked := false
                var primaryDelegate types.Address
                var maxAmount *big.Int
                
                for _, stake := range stakes {
                    if stake.Amount != nil {
                        totalAmount.Add(totalAmount, stake.Amount)
                        // 找到金额最大的验证者作为主要验证者
                        if maxAmount == nil || stake.Amount.Cmp(maxAmount) > 0 {
                            maxAmount = stake.Amount
                            primaryDelegate = stake.Delegate
                        }
                    }
                    // ... 聚合其他字段
                }
                
                return &StakeInfo{
                    Staker:    staker,
                    Amount:    totalAmount,      // 所有验证者的总金额
                    Delegate:  primaryDelegate,   // 金额最大的验证者
                    // ...
                }, nil
            }
        }
    }
    
    // 4. 回退方案：从内存获取
    // ...
}
```

## 八、调用流程

### 8.1 轻度违规检测流程

```
block_builder.go: buildBlock()
  └─> detectValidatorFaults()
      └─> 计算漏块率
      └─> 判断是否超过阈值
      └─> executeSlashing()
          └─> 更新 DelegateInfo.VotingPower
          └─> 更新所有 VoterInfo.DelegateVotes[validatorAddr]
          └─> 更新所有 StakeInfo（stake.Delegate == validatorAddr）
          └─> 保存 SlashingHistory
          └─> 保存 SlashingRecord（每个委托人）
```

### 8.2 严重违规检测流程

```
extra.go: ValidateFinalizedData()
  └─> doubleSigningDetector.DetectDoubleSigning()
      └─> executeSlashing()
          └─> 同上
```

## 九、关键设计决策

### 9.1 为什么从 VotingPower 扣除？

- `VotingPower` 是验证者的总投票权重，包含所有委托人的投票
- 消减应该影响验证者的整体权重，而不是只扣除验证者自己的余额
- 简化实现，不需要区分验证者自己的投票和委托人的投票

### 9.2 为什么需要 DelegateVotes？

- 一个投票者可能投给多个验证者
- 需要记录每个验证者对应的投票金额（削减后）
- 用于按比例削减和查询

### 9.3 为什么需要更新 StakeInfo？

- `StakeInfo` 表存储历史投票记录
- 需要更新所有相关记录，确保查询时数据一致
- `GetValidatorVotingDetails` 依赖 `StakeInfo` 表

### 9.4 为什么不需要实际扣除账户余额？

- `VotingPower` 不等于账户余额
- 验证者的 `VotingPower` 可能主要来自委托人的投票
- 消减的是投票权重，不是实际的代币余额
- 代币仍然在委托人和验证者的账户中，只是投票权重被削减
- 因此 `burnSlashedTokens` 已移除，避免重复记录“销毁”行为；真实削减已通过更新 `DelegateInfo`、`VoterInfo`、`StakeInfo` 等数据库表完成

## 十、测试要点

1. **轻度违规检测**：验证漏块率计算和阈值判断
2. **严重违规检测**：验证双重签名检测
3. **消减执行**：验证 VotingPower 和 DelegateVotes 的更新
4. **数据一致性**：验证所有数据表的更新一致性
5. **历史记录**：验证 SlashingHistory 和 SlashingRecord 的保存
6. **查询接口**：验证 GetStakingInfo 和 GetValidatorVotingDetails 的准确性

## 十一、解质押时金额减少的查询方案

### 11.1 问题

当投票者解质押时，发现金额比当初投票时少，需要知道原因。

### 11.2 解决方案

#### 方案1：在投票时保存原始金额

**修改投票逻辑**（`consensus/dpos/storage.go`）：

```go
// 创建 StakeInfo
stakeInfo := &StakeInfo{
    Staker:         voter,
    Amount:         new(big.Int).Set(amount),
    OriginalAmount: new(big.Int).Set(amount), // 🆕 保存原始金额
    StartTime:      voterInfo.LastVoteTime,
    EndTime:        voterInfo.LockedUntil,
    IsLocked:       voterInfo.LockedUntil > uint64(time.Now().Unix()),
    IsActive:       len(voterInfo.VotedDelegates) > 0,
    Rewards:        big.NewInt(0),
    Delegate:       candidate,
    SlashingRecords: make([]*SlashingRecord, 0), // 🆕 初始化削减历史
}
```

#### 方案2：解质押时查询削减历史

**解质押函数**（`consensus/dpos/voting_transaction.go`）：

```go
// processUnvoteTransaction 处理解质押交易
func (d *DPoS) processUnvoteTransaction(tx *types.Transaction, blockNumber uint64) error {
    // ... 解析交易 ...
    
    // 1. 获取投票者信息
    voterInfo, err := d.state.StakeStore.getVoterInfo(voterAddr, nil)
    if err != nil {
        return fmt.Errorf("failed to get voter info: %w", err)
    }
    
    // 2. 检查是否有该验证者的投票
    currentAmount := voterInfo.DelegateVotes[validatorAddr]
    if currentAmount == nil || currentAmount.Sign() == 0 {
        return fmt.Errorf("no vote found for validator %s", validatorAddr.String())
    }
    
    // 3. 获取削减历史
    slashingRecords := voterInfo.SlashingRecords[validatorAddr]
    
    // 4. 获取原始投票金额（从 StakeInfo）
    allStakes, err := d.state.StakeStore.GetStakingInfo()
    if err != nil {
        return fmt.Errorf("failed to get staking info: %w", err)
    }
    
    var originalAmount *big.Int
    var totalSlashAmount *big.Int = big.NewInt(0)
    
    for _, stake := range allStakes {
        if stake != nil && stake.Staker == voterAddr && stake.Delegate == validatorAddr {
            // 使用第一条记录的原始金额（或累加所有记录的原始金额）
            if stake.OriginalAmount != nil {
                if originalAmount == nil {
                    originalAmount = new(big.Int).Set(stake.OriginalAmount)
                } else {
                    originalAmount.Add(originalAmount, stake.OriginalAmount)
                }
            }
            
            // 累加所有削减记录
            if stake.SlashingRecords != nil {
                for _, record := range stake.SlashingRecords {
                    if record.SlashAmount != nil {
                        totalSlashAmount.Add(totalSlashAmount, record.SlashAmount)
                    }
                }
            }
        }
    }
    
    // 5. 计算可解质押金额（从 VoterInfo.DelegateVotes 获取，已经削减后）
    withdrawAmount := new(big.Int).Set(currentAmount) // 🆕 关键：从 DelegateVotes 获取当前金额
    
    // 6. 如果原始金额存在，记录差异信息（用于提示用户）
    if originalAmount != nil && originalAmount.Cmp(withdrawAmount) > 0 {
        difference := new(big.Int).Sub(originalAmount, withdrawAmount)
        d.logger.Info("⚠️ 解质押金额减少",
            "voter", voterAddr.String(),
            "validator", validatorAddr.String(),
            "originalAmount", originalAmount.String(),
            "currentAmount", withdrawAmount.String(),
            "difference", difference.String(),
            "slashCount", len(slashingRecords))
        
        // 🆕 返回削减信息给用户（在响应中包含）
        // 用户可以通过 RPC 接口 dpos_getVoterSlashingHistory 查询详细历史
    }
    
    // 7. 执行解质押逻辑（使用 withdrawAmount，即削减后的金额）
    // 注意：只能解质押削减后的金额，不能解质押原始金额
    // ...
    
    return nil
}
```

#### 方案3：添加 RPC 接口查询削减详情

**新增 RPC 接口**（`jsonrpc/dpos_endpoint.go`）：

```go
// GetVoterSlashingHistory 获取投票者的削减历史
// RPC: dpos_getVoterSlashingHistory
func (d *DPOS) GetVoterSlashingHistory(ctx context.Context, params interface{}) (interface{}, error) {
    // 解析参数
    paramsMap, ok := params.(map[string]interface{})
    if !ok {
        return nil, fmt.Errorf("invalid params")
    }
    
    voterAddrStr, ok := paramsMap["voter"].(string)
    if !ok {
        return nil, fmt.Errorf("voter address required")
    }
    
    validatorAddrStr, _ := paramsMap["validator"].(string) // 可选，如果提供则只查询该验证者
    
    voterAddr := types.StringToAddress(voterAddrStr)
    var validatorAddr types.Address
    if validatorAddrStr != "" {
        validatorAddr = types.StringToAddress(validatorAddrStr)
    }
    
    // 1. 获取 VoterInfo
    voterInfo, err := d.store.GetVoterInfo(voterAddr)
    if err != nil {
        return nil, fmt.Errorf("failed to get voter info: %w", err)
    }
    
    if voterInfo == nil {
        return map[string]interface{}{
            "voter": voterAddr.String(),
            "slashingHistory": []interface{}{},
        }, nil
    }
    
    // 2. 获取削减历史
    var slashingHistory []interface{}
    
    if validatorAddr != types.ZeroAddress {
        // 只查询指定验证者的削减历史
        records := voterInfo.SlashingRecords[validatorAddr]
        for _, record := range records {
            slashingHistory = append(slashingHistory, map[string]interface{}{
                "validatorAddr":          record.ValidatorAddr.String(),
                "blockNumber":            record.BlockNumber,
                "epochNumber":            record.EpochNumber,
                "timestamp":              record.Timestamp,
                "slashAmount":            record.SlashAmount.String(),
                "oldVoteAmount":          record.OldVoteAmount.String(),
                "newVoteAmount":          record.NewVoteAmount.String(),
                "slashRate":              record.SlashRate,
                "reason":                 record.Reason,
                "missedBlocks":           record.MissedBlocks,
                "missedBlocksPercentage": record.MissedBlocksPercentage,
            })
        }
    } else {
        // 查询所有验证者的削减历史
        for validator, records := range voterInfo.SlashingRecords {
            for _, record := range records {
                slashingHistory = append(slashingHistory, map[string]interface{}{
                    "validatorAddr":          record.ValidatorAddr.String(),
                    "blockNumber":            record.BlockNumber,
                    "epochNumber":            record.EpochNumber,
                    "timestamp":              record.Timestamp,
                    "slashAmount":            record.SlashAmount.String(),
                    "oldVoteAmount":          record.OldVoteAmount.String(),
                    "newVoteAmount":          record.NewVoteAmount.String(),
                    "slashRate":              record.SlashRate,
                    "reason":                 record.Reason,
                    "missedBlocks":           record.MissedBlocks,
                    "missedBlocksPercentage": record.MissedBlocksPercentage,
                })
            }
        }
    }
    
    // 3. 计算总削减金额和原始金额
    totalSlashAmount := big.NewInt(0)
    totalOriginalAmount := big.NewInt(0)
    currentAmount := big.NewInt(0)
    
    // 从 StakeInfo 获取原始金额
    allStakes, err := d.store.GetStakingInfo()
    if err == nil {
        for _, stake := range allStakes {
            if stake != nil && stake.Staker == voterAddr {
                if validatorAddr == types.ZeroAddress || stake.Delegate == validatorAddr {
                    if stake.OriginalAmount != nil {
                        totalOriginalAmount.Add(totalOriginalAmount, stake.OriginalAmount)
                    }
                    if stake.Amount != nil {
                        currentAmount.Add(currentAmount, stake.Amount)
                    }
                    if stake.SlashingRecords != nil {
                        for _, record := range stake.SlashingRecords {
                            if record.SlashAmount != nil {
                                totalSlashAmount.Add(totalSlashAmount, record.SlashAmount)
                            }
                        }
                    }
                }
            }
        }
    }
    
    return map[string]interface{}{
        "voter":              voterAddr.String(),
        "validator":          validatorAddr.String(),
        "originalAmount":     totalOriginalAmount.String(),
        "currentAmount":      currentAmount.String(),
        "totalSlashAmount":   totalSlashAmount.String(),
        "slashCount":         len(slashingHistory),
        "slashingHistory":    slashingHistory,
    }, nil
}
```

#### 方案4：解质押时返回削减信息

**解质押响应增强**：

```go
// UnvoteResponse 解质押响应
type UnvoteResponse struct {
    Success          bool                `json:"success"`
    Voter            types.Address       `json:"voter"`
    Validator        types.Address       `json:"validator"`
    WithdrawAmount   *big.Int            `json:"withdrawAmount"`   // 可提取金额（削减后）
    OriginalAmount   *big.Int            `json:"originalAmount"`   // 原始投票金额
    TotalSlashAmount *big.Int            `json:"totalSlashAmount"` // 总削减金额
    SlashCount       int                 `json:"slashCount"`       // 削减次数
    SlashingHistory  []*SlashingRecord   `json:"slashingHistory"`  // 削减历史
    Message          string              `json:"message,omitempty"` // 提示信息
}
```

**解质押时返回信息**：

```go
response := &UnvoteResponse{
    Success:          true,
    Voter:            voterAddr,
    Validator:        validatorAddr,
    WithdrawAmount:   withdrawAmount,
    OriginalAmount:   originalAmount,
    TotalSlashAmount: totalSlashAmount,
    SlashCount:       len(slashingRecords),
    SlashingHistory:  slashingRecords,
}

if originalAmount != nil && originalAmount.Cmp(withdrawAmount) > 0 {
    difference := new(big.Int).Sub(originalAmount, withdrawAmount)
    response.Message = fmt.Sprintf(
        "您的投票金额因验证者违规被削减。原始金额: %s, 当前金额: %s, 削减金额: %s, 削减次数: %d",
        originalAmount.String(),
        withdrawAmount.String(),
        difference.String(),
        len(slashingRecords),
    )
}
```

### 11.3 实现要点

1. **保存原始金额**：投票时在 `StakeInfo.OriginalAmount` 保存原始金额
2. **记录削减历史**：每次削减时在 `StakeInfo.SlashingRecords` 和 `VoterInfo.SlashingRecords` 中记录
3. **解质押金额来源**：**从 `VoterInfo.DelegateVotes[validatorAddr]` 获取**（已经削减后的金额）
4. **查询接口**：提供 RPC 接口查询削减历史
5. **解质押提示**：解质押时返回削减信息，告知用户原因

### 11.4 解质押流程

**关键点**：
1. **解质押金额来源**：`VoterInfo.DelegateVotes[validatorAddr]`（削减后的当前金额）
2. **不能解质押原始金额**：只能解质押削减后的金额
3. **查询削减原因**：通过 `dpos_getVoterSlashingHistory` 查询详细历史
4. **StakeInfo 的作用**：存储历史记录和原始金额，用于查询和审计

**流程**：
```
用户发起解质押
  └─> 从 VoterInfo.DelegateVotes[validatorAddr] 获取当前金额（削减后）
  └─> 从 StakeInfo 获取原始金额（用于对比）
  └─> 如果金额减少，返回提示信息
  └─> 执行解质押（只能解质押削减后的金额）
  └─> 用户可以通过 RPC 接口查询削减历史
```

### 11.5 数据来源

用户可以通过以下方式查询削减原因：

1. **RPC 接口**：`dpos_getVoterSlashingHistory`
   ```json
   {
     "voter": "0x...",
     "validator": "0x..." // 可选
   }
   ```
   返回：原始金额、当前金额、总削减金额、削减次数、详细削减历史

2. **解质押响应**：解质押时自动返回削减信息（如果金额减少）

3. **StakeInfo 查询**：通过 `dpos_getStakingInfo` 查询，包含 `OriginalAmount` 和 `SlashingRecords`

### 11.6 关键理解

**你的理解基本正确，但需要澄清**：

1. ✅ **更新削减后的权重**：`DelegateInfo.VotingPower` 和 `VoterInfo.DelegateVotes[validatorAddr]` 都更新为削减后的金额

2. ✅ **更新 StakeInfo 记录**：`StakeInfo` 存储历史投票记录，包含：
   - `Amount`：当前金额（削减后）
   - `OriginalAmount`：原始投票金额（第一次投票时的金额）
   - `SlashingRecords`：削减历史

3. ✅ **解质押金额来源**：**从 `VoterInfo.DelegateVotes[validatorAddr]` 获取**（削减后的金额），不是从 `StakeInfo` 获取

4. ✅ **金额减少提示**：如果 `OriginalAmount > currentAmount`，返回提示信息，用户可以通过 RPC 接口查询详细历史

**数据流向**：
```
投票时：
  StakeInfo.OriginalAmount = 投票金额
  StakeInfo.Amount = 投票金额
  VoterInfo.DelegateVotes[validator] = 投票金额

削减时：
  StakeInfo.Amount = 削减后的金额（更新）
  StakeInfo.OriginalAmount = 保持不变（原始金额）
  StakeInfo.SlashingRecords = 添加削减记录
  VoterInfo.DelegateVotes[validator] = 削减后的金额（更新）
  VoterInfo.SlashingRecords[validator] = 添加削减记录

解质押时：
  从 VoterInfo.DelegateVotes[validator] 获取当前金额（削减后）
  从 StakeInfo.OriginalAmount 获取原始金额（用于对比）
  如果金额减少，提示用户查询削减历史
```

## 十二、后续优化

1. **VotedDelegates 删除**：如果确认不需要，可以从 VoterInfo 中删除
2. **VotingPower 删除**：如果可以从 DelegateVotes 累加，可以删除
3. **StakeInfo 更新优化**：优化复合 key 的更新逻辑
4. **削减历史查询优化**：添加索引，提高查询效率

