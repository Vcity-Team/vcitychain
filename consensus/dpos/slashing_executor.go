package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// executeSlashing 执行削减惩罚（按比例削减每个投票者的质押金额）
func (d *DPoS) executeSlashing(
	validatorAddr types.Address,
	slashRate uint64,
	blockNumber uint64,
	epochNumber uint64,
	reason string,
	missedBlocks uint64,
	missedBlocksPercentage uint64,
	doubleSigningHeight uint64,
) error {
	d.logger.Info("🔨 ===== 开始执行削减惩罚 =====",
		"validator", validatorAddr.String(),
		"slashRate", slashRate,
		"基点",
		"blockNumber", blockNumber,
		"epochNumber", epochNumber,
		"reason", reason)

	store, err := d.getStateStore()
	if err != nil {
		return err
	}

	// 🆕 幂等性检查：检查是否已经执行过该区块的消减
	if hasHistory, err := store.HasSlashingHistory(validatorAddr, blockNumber); err != nil {
		d.logger.Warn("⚠️ 检查消减历史失败，继续执行（可能重复）",
			"validator", validatorAddr.String(),
			"blockNumber", blockNumber,
			"error", err)
	} else if hasHistory {
		d.logger.Info("ℹ️ 消减已执行过，跳过重复执行（幂等性保护）",
			"validator", validatorAddr.String(),
			"blockNumber", blockNumber,
			"epochNumber", epochNumber,
			"note", "防止重复处理区块导致的重复消减")
		return nil // 已执行过，直接返回成功
	}

	// 1. 获取验证者的当前 VotingPower
	validator, err := store.GetDelegateInfo(validatorAddr)
	if err != nil || validator == nil {
		return fmt.Errorf("failed to get validator info: %w", err)
	}

	oldVotingPower := new(big.Int).Set(validator.VotingPower)
	if oldVotingPower.Sign() == 0 {
		d.logger.Warn("⚠️ 验证者投票权重为0，跳过削减", "validator", validatorAddr.String())
		return nil
	}

	d.logger.Info("📊 验证者当前投票权重",
		"validator", validatorAddr.String(),
		"oldVotingPower", oldVotingPower.String())

	// 2. 获取所有投票给该验证者的质押记录
	allStakes, err := store.GetStakingInfo()
	if err != nil {
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	// 筛选出投票给该验证者的记录
	var validatorStakes []*StakeInfo
	for _, stake := range allStakes {
		if stake != nil && stake.Delegate == validatorAddr && stake.Amount != nil && stake.Amount.Sign() > 0 {
			validatorStakes = append(validatorStakes, stake)
		}
	}

	if len(validatorStakes) == 0 {
		d.logger.Warn("⚠️ 没有找到该验证者的质押记录，跳过削减", "validator", validatorAddr.String())
		return nil
	}

	d.logger.Info("📋 找到质押记录",
		"validator", validatorAddr.String(),
		"stakesCount", len(validatorStakes))

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

	// 4. 按比例削减每个投票者的质押金额
	totalSlashAmount := big.NewInt(0)
	newVotingPower := big.NewInt(0)

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

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

		d.logger.Info("🔨 削减投票者",
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

				// 保存原始金额（如果还没有保存）
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
					NewVoteAmount:          recordNewAmount,                // 削减后的金额
					SlashRate:              slashRate,
					Reason:                 reason,
					MissedBlocks:           missedBlocks,
					MissedBlocksPercentage: missedBlocksPercentage,
					DoubleSigningHeight:    doubleSigningHeight,
				}

				// 更新 StakeInfo（使用外部事务）
				if err := d.updateStakingInfoAfterSlashing(
					voterAddr,
					validatorAddr,
					recordNewAmount,
					stake.Amount,
					slashingRecord,
					dbTx, // 🆕 传入外部事务，避免嵌套事务
				); err != nil {
					d.logger.Warn("⚠️ 更新质押记录失败",
						"voter", voterAddr.String(),
						"error", err)
				}
			}
		}

		// 4.4 更新 VoterInfo.DelegateVotes（使用外部事务）
		if err := d.updateVoterVoteAmountForValidator(
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
			doubleSigningHeight,
			dbTx, // 🆕 传入外部事务，避免嵌套事务
		); err != nil {
			d.logger.Warn("⚠️ 更新投票者信息失败",
				"voter", voterAddr.String(),
				"error", err)
		}
	}

	// 5. 更新验证者的 VotingPower（使用外部事务）
	if err := d.updateVotingPowerInDatabaseWithTx(validatorAddr, newVotingPower, dbTx); err != nil {
		return fmt.Errorf("failed to update validator voting power: %w", err)
	}

	// 提交事务
	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 6. 保存削减历史（验证者级别）
	store, err2 := d.getStateStore()
	if err2 != nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法保存削减历史", "error", err2)
		return err2
	}

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
		DoubleSigningHeight:    doubleSigningHeight,
	}
	if err := store.SaveSlashingHistory(slashingHistory); err != nil {
		d.logger.Warn("⚠️ 保存削减历史失败", "error", err)
	}

	d.logger.Info("✅ ===== 削减惩罚执行完成 =====",
		"validator", validatorAddr.String(),
		"oldVotingPower", oldVotingPower.String(),
		"totalSlashAmount", totalSlashAmount.String(),
		"newVotingPower", newVotingPower.String(),
		"slashRate", slashRate,
		"基点")

	return nil
}

// updateStakingInfoInDatabase 更新数据库中的质押记录（使用相同的 key）
func (d *DPoS) updateStakingInfoInDatabase(stake *StakeInfo, dbTx *bolt.Tx) error {
	bucket := dbTx.Bucket([]byte("StakingInfo"))
	if bucket == nil {
		return fmt.Errorf("staking info bucket not found")
	}

	// 🆕 使用统一的复合键构建函数
	key := buildStakingCompositeKey(stake.Staker, stake.Delegate, stake.StartTime)

	// 序列化更新后的记录
	data, err := json.Marshal(stake)
	if err != nil {
		return fmt.Errorf("failed to marshal staking info: %w", err)
	}

	// 更新数据库中的记录（如果记录存在）
	if existingData := bucket.Get(key); existingData != nil {
		// 记录存在，更新它
		if err := bucket.Put(key, data); err != nil {
			return fmt.Errorf("failed to update staking info: %w", err)
		}
		d.logger.Debug("✅ 更新现有质押记录",
			"staker", stake.Staker.String(),
			"delegate", stake.Delegate.String(),
			"newAmount", stake.Amount.String())
	} else {
		// 记录不存在，遍历所有记录，找到 staker + delegate 匹配的记录
		cursor := bucket.Cursor()
		found := false
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			_ = v // 避免未使用变量警告
			if len(k) >= 40 {
				// 检查 staker 和 delegate 是否匹配
				stakerMatch := len(k) >= 20 && types.Address(k[0:20]) == stake.Staker
				delegateMatch := len(k) >= 40 && types.Address(k[20:40]) == stake.Delegate
				if stakerMatch && delegateMatch {
					// 更新这条记录
					if err := bucket.Put(k, data); err != nil {
						d.logger.Error("❌ 更新质押记录失败", "error", err)
						continue
					}
					found = true
					d.logger.Debug("✅ 找到并更新匹配的质押记录",
						"staker", stake.Staker.String(),
						"delegate", stake.Delegate.String(),
						"newAmount", stake.Amount.String())
				}
			}
		}
		if !found {
			d.logger.Warn("⚠️ 未找到匹配的质押记录",
				"staker", stake.Staker.String(),
				"delegate", stake.Delegate.String())
		}
	}

	return nil
}

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
	doubleSigningHeight uint64,
	dbTx *bolt.Tx, // 🆕 使用外部事务，避免嵌套事务
) error {
	store, err := d.getStateStore()
	if err != nil {
		return err
	}

	// 1. 获取 VoterInfo（使用外部事务）
	voterInfo, err := store.getVoterInfo(voterAddr, dbTx)
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
		DoubleSigningHeight:    doubleSigningHeight,
	}

	// 5. 添加到 SlashingRecords
	voterInfo.SlashingRecords[validatorAddr] = append(
		voterInfo.SlashingRecords[validatorAddr],
		slashingRecord,
	)

	// 6. 更新内存中的 VoterInfo
	d.voters[voterAddr] = voterInfo

	// 7. 保存到数据库（使用外部事务）
	if err := store.setVoterInfo(voterAddr, voterInfo, dbTx); err != nil {
		return fmt.Errorf("failed to save voter info: %w", err)
	}

	return nil
}

// updateStakingInfoAfterSlashing 更新StakingInfo记录（削减后）
func (d *DPoS) updateStakingInfoAfterSlashing(
	voterAddr types.Address,
	validatorAddr types.Address,
	newAmount *big.Int,
	oldAmount *big.Int,
	slashingRecord *SlashingRecord,
	dbTx *bolt.Tx, // 🆕 使用外部事务，避免嵌套事务
) error {
	store, err := d.getStateStore()
	if err != nil {
		return err
	}

	// 如果 dbTx 为 nil，开启新事务（兼容性）
	if dbTx == nil {
		var err error
		dbTx, err = d.state.beginDBTransaction(true)
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer dbTx.Rollback()
	}

	// 1. 获取所有 StakeInfo 记录（在事务中读取）
	allStakes, err := store.GetStakingInfo()
	if err != nil {
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	bucket := dbTx.Bucket([]byte("StakingInfo"))
	if bucket == nil {
		return fmt.Errorf("staking info bucket not found")
	}

	for _, stake := range allStakes {
		if stake != nil && stake.Staker == voterAddr && stake.Delegate == validatorAddr {
			// 3. 更新金额
			stake.Amount = newAmount

			// 4. 保存原始金额（如果还没有保存）
			if stake.OriginalAmount == nil {
				stake.OriginalAmount = new(big.Int).Set(oldAmount)
			}

			// 5. 添加削减记录
			if stake.SlashingRecords == nil {
				stake.SlashingRecords = make([]*SlashingRecord, 0)
			}
			stake.SlashingRecords = append(stake.SlashingRecords, slashingRecord)

			// 6. 保存到数据库（使用复合 key）
			// 🆕 使用统一的复合键构建函数
			key := buildStakingCompositeKey(stake.Staker, stake.Delegate, stake.StartTime)

			data, err := json.Marshal(stake)
			if err != nil {
				d.logger.Warn("⚠️ 序列化质押记录失败",
					"staker", stake.Staker.String(),
					"error", err)
				continue
			}

			if err := bucket.Put(key, data); err != nil {
				d.logger.Warn("⚠️ 更新质押记录失败",
					"staker", stake.Staker.String(),
					"error", err)
				continue
			}
		}
	}

	// 🆕 如果使用的是外部事务，不在这里提交（由调用者提交）
	// 如果开启的是新事务，需要提交（但这种情况不应该发生，因为现在总是传入事务）
	// 注意：这里不提交外部事务，由 executeSlashing 统一提交

	return nil
}
