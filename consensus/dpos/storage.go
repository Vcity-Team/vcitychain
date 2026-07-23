package dpos

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// debugDatabaseContents 调试数据库内容
func (d *DPoS) debugDatabaseContents() {
	if d.state != nil && d.state.StakeStore != nil {
		d.state.StakeStore.db.View(func(tx *bolt.Tx) error {
			// 检查VoterInfo bucket
			voterBucket := tx.Bucket([]byte("VoterInfo"))
			if voterBucket != nil {
				d.logger.Debug("🔍 VoterInfo bucket 存在，条目数", "count", voterBucket.Stats().KeyN)
			} else {
				d.logger.Debug("🔍 VoterInfo bucket 不存在")
			}

			// 检查StakingInfo bucket
			stakingBucket := tx.Bucket([]byte("StakingInfo"))
			if stakingBucket != nil {
				d.logger.Debug("🔍 StakingInfo bucket 存在，条目数", "count", stakingBucket.Stats().KeyN)
			} else {
				d.logger.Debug("🔍 StakingInfo bucket 不存在")
			}

			return nil
		})
	}
}

// persistVoteToDatabase 将投票信息持久化到数据库
func (d *DPoS) persistVoteToDatabase(voter types.Address, candidate types.Address, amount *big.Int, effectiveEpoch uint64, applied bool) error {
	// 检查状态存储是否可用
	d.logger.Debug("🔍 Checking state store availability",
		"d.state", d.state != nil,
		"d.state.StakeStore", func() interface{} {
			if d.state != nil {
				return d.state.StakeStore != nil
			}
			return "N/A"
		}(),
		"d.dataDir", d.dataDir)

	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available: d.state=%v, d.state.StakeStore=%v",
			d.state != nil,
			func() interface{} {
				if d.state != nil {
					return d.state.StakeStore != nil
				}
				return "N/A"
			}())
	}

	// 添加 defer 确保能看到是否进入了锁
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("❌ Panic in persistVoteToDatabase", "panic", r)
		}
	}()

	d.logger.Debug("💾 Persisting vote to database",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String(),
		"effectiveEpoch", effectiveEpoch,
		"applied", applied)

	// 保存 StakingInfo 到数据库（投票记录）
	// 注意：这里使用 voter 作为 staker，因为投票者就是质押者

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		d.logger.Error("❌ Failed to begin db transaction for staking info", "error", err)
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

	// 创建 StakeInfo（不再依赖 VoterInfo）
	now := uint64(time.Now().Unix())
	amountCopy := new(big.Int).Set(amount)
	stakeInfo := &StakeInfo{
		Staker:         voter,
		Amount:         amountCopy,
		OriginalAmount: new(big.Int).Set(amountCopy), // 当初的投票金额（撤销后仍可查询）
		StartTime:      now,
		EndTime:        now + d.config.VoteLockTime, // 锁定时间
		IsLocked:       d.config.VoteLockTime > 0,
		IsActive:       true,           // 投票记录都是活跃的
		Rewards:        big.NewInt(0),  // 奖励通过 RewardStore 独立存储，此字段保留用于兼容
		Delegate:       candidate,      // 投票给哪个 delegate
		EffectiveEpoch: effectiveEpoch, // ✅ 新增：保存生效的epoch
		Applied:        applied,        // ✅ 新增：保存是否已应用
	}

	// 保存到数据库
	// 传入 now 作为 timestamp，确保每次投票都有唯一 key
	if err := d.state.StakeStore.setStakingInfo(voter, stakeInfo, now, dbTx); err != nil {
		d.logger.Error("❌ Failed to save staking info to database", "error", err)
		return fmt.Errorf("failed to save staking info to database: %w", err)
	}

	// ✅ 关键修复：在同一个事务中更新 DelegateInfo 表
	// 因为 GetValidatorsWithFilter 从 DelegateInfo 表读取，所以需要同步更新
	if applied {
		// 从 DelegateInfo 表读取当前权重（在事务中）
		currentPower := big.NewInt(0)
		if delegateInfo, err := d.state.StakeStore.getDelegateInfo(candidate, dbTx); err == nil && delegateInfo != nil && delegateInfo.VotingPower != nil {
			currentPower = new(big.Int).Set(delegateInfo.VotingPower)
		}

		// 计算新的投票权重
		newPower := new(big.Int).Add(currentPower, amount)

		// 更新 DelegateInfo 表
		if err := d.updateVotingPowerInDatabaseWithTx(candidate, newPower, dbTx); err != nil {
			d.logger.Error("❌ 更新 DelegateInfo 失败", "error", err)
			return fmt.Errorf("failed to update delegate info: %w", err)
		}

	}

	// 提交事务
	if err := dbTx.Commit(); err != nil {
		d.logger.Error("❌ Failed to commit staking info transaction", "error", err)
		return fmt.Errorf("failed to commit staking info transaction: %w", err)
	}

	d.logger.Debug("Staking info saved to database successfully",
		"staker", voter.String(),
		"delegate", candidate.String(),
		"amount", amount.String(),
		"effectiveEpoch", effectiveEpoch,
		"applied", applied)

	// 保存投票记录到内存（用于边界应用）
	if d.voteRecords == nil {
		d.voteRecords = make(map[string]*VoteRecord)
	}
	voteKey := fmt.Sprintf("%s_%s_%d", voter.String(), candidate.String(), now)
	d.voteRecordsMutex.Lock()
	d.voteRecords[voteKey] = &VoteRecord{
		Voter:          voter,
		Delegate:       candidate,
		Amount:         new(big.Int).Set(amount),
		Timestamp:      now,
		EffectiveEpoch: effectiveEpoch,
		Applied:        applied,
	}
	d.voteRecordsMutex.Unlock()
	d.logger.Debug("✅ Vote record saved to memory",
		"voteKey", voteKey,
		"effectiveEpoch", effectiveEpoch,
		"applied", applied)

	return nil
}

// saveValidatorSetForBlock 已移除数据库保存机制，改为日志记录
func (d *DPoS) saveValidatorSetForBlock(blockNumber uint64) error {
	// 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
	d.logger.Info("📝 验证者集合获取方式已更新",
		"blockNumber", blockNumber,
		"delegatesCount", len(d.delegates),
		"method", "从ExtraData直接解析",
		"note", "不再需要保存到数据库，验证节点将从区块数据直接获取")

	// 详细记录当前验证者集合信息
	d.logger.Info("📋 当前验证者集合详细信息:")
	for i, delegate := range d.delegates {
		d.logger.Info("📝 当前验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", delegate.Address.String(),
			"votingPower", delegate.VotingPower.String(),
			"isActive", delegate.IsActive,
			"hasBlsKey", delegate.BlsKey != nil)
	}

	return nil
}

// SaveValidatorSetForBlockWithValidators 保存指定区块的特定验证者集合到数据库（接口实现）
func (d *DPoS) SaveValidatorSetForBlockWithValidators(blockNumber uint64, validators validator.AccountSet) error {
	return d.saveValidatorSetForBlockWithValidators(blockNumber, validators)
}

// saveValidatorSetForBlockWithValidators 已移除数据库保存机制，改为日志记录
func (d *DPoS) saveValidatorSetForBlockWithValidators(blockNumber uint64, validators validator.AccountSet) error {
	// 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
	d.logger.Info("📝 验证者集合获取方式已更新",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"method", "从ExtraData直接解析",
		"note", "不再需要保存到数据库，验证节点将从区块数据直接获取")

	// 详细记录验证者集合信息
	d.logger.Info("📋 验证者集合详细信息:")
	for i, validator := range validators {
		d.logger.Info("📝 验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}

	return nil
}

// loadValidatorsFromDatabaseWithLimit 从数据库加载验证者并按voterpower排序截取前N个
func (d *DPoS) loadValidatorsFromDatabaseWithLimit() error {
	dbValidators, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		return fmt.Errorf("failed to get sorted validators with limit: %w", err)
	}

	if len(dbValidators) == 0 {
		return fmt.Errorf("no validators in database")
	}

	return nil
}

// persistSingleDelegateToDatabase 持久化单个验证者到数据库
func (d *DPoS) persistSingleDelegateToDatabase(del *validator.ValidatorMetadata) error {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, skipping single delegate persistence")
		return nil
	}

	d.logger.Debug("💾 Starting single delegate persistence", "address", del.Address.String())

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

	// 处理BLS公钥
	var blsPublicKey []byte
	if del.BlsKey != nil {
		blsPublicKey = del.BlsKey.Marshal()
		d.logger.Debug("🔑 保存BLS公钥到数据库（已存在）",
			"address", del.Address.String(),
			"publicKeyLength", len(blsPublicKey))
	} else {
		// BLS公钥为nil是正常的，将在验证时动态获取
		d.logger.Debug("🔑 BLS公钥为nil，将在验证时动态获取",
			"address", del.Address.String(),
			"note", "BLS公钥按需获取机制")
		d.logger.Debug("ℹ️ 受托人BLS公钥为nil，尝试从创世文件恢复",
			"address", del.Address.String())

		// 从validator-bls.key文件中查找BLS公钥（始终在 nodeRoot/consensus/）
		if keyFilePath := d.validatorBLSKeyPath(); keyFilePath != "" {
			// 检查文件是否存在
			if _, err := os.Stat(keyFilePath); err == nil {
				// 读取私钥文件
				privateKeyData, err := os.ReadFile(keyFilePath)
				if err == nil {
					// 获取十六进制字符串（去除可能的换行符）
					privateKeyHex := strings.TrimSpace(string(privateKeyData))

					// 检查并修正私钥长度
					if len(privateKeyHex)%2 != 0 {
						privateKeyHex = "0" + privateKeyHex
					}

					// 解析BLS私钥
					privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
					if err == nil {
						// 从私钥生成公钥
						publicKey := privateKey.PublicKey()
						blsPublicKey = publicKey.Marshal()
						d.logger.Info("✅ 从validator-bls.key文件恢复BLS公钥并保存到数据库",
							"address", del.Address.String(),
							"publicKeyLength", len(blsPublicKey),
							"filePath", keyFilePath)
					} else {
						d.logger.Warn("⚠️ 解析validator-bls.key文件失败",
							"address", del.Address.String(),
							"filePath", keyFilePath,
							"error", err)
					}
				} else {
					d.logger.Warn("⚠️ 读取validator-bls.key文件失败",
						"address", del.Address.String(),
						"filePath", keyFilePath,
						"error", err)
				}
			} else {
				d.logger.Debug("ℹ️ validator-bls.key文件不存在，将在验证时按需获取",
					"address", del.Address.String(),
					"filePath", keyFilePath)
			}
		}
	}

	// 从数据库读取当前的 VotingPower，避免用内存中的错误值覆盖数据库
	dbVotingPower, err := d.getVotingPowerFromDatabase(del.Address)
	if err != nil {
		d.logger.Warn("⚠️ 从数据库读取 VotingPower 失败，使用内存中的值",
			"address", del.Address.String(),
			"error", err)
		dbVotingPower = new(big.Int).Set(del.VotingPower)
	} else {
		d.logger.Debug("✅ 从数据库读取 VotingPower",
			"address", del.Address.String(),
			"dbVotingPower", dbVotingPower.String(),
			"memoryVotingPower", del.VotingPower.String())
	}

	// 创建受托人信息
	delegateInfo := &DelegateInfo{
		Address:        del.Address,
		VotingPower:    new(big.Int).Set(dbVotingPower), // 使用数据库中的值，而不是内存中的值
		TotalVotes:     new(big.Int).Set(dbVotingPower), // 使用VotingPower作为TotalVotes
		ProducedBlocks: 0,
		MissedBlocks:   0,
		LastBlockTime:  0,
		IsActive:       del.IsActive,
		BlsPublicKey:   blsPublicKey, // 保存BLS公钥（可以为nil）
	}

	d.populateCommissionFields(del.Address, delegateInfo)

	// 记录持久化信息
	d.logger.Info("💾 持久化单个受托人信息到数据库",
		"address", del.Address.String(),
		"votingPower", del.VotingPower.String(),
		"isActive", del.IsActive,
		"isActiveType", fmt.Sprintf("%T", del.IsActive),
		"blsPublicKeyLength", len(blsPublicKey),
		"blsKeyStatus", func() string {
			if len(blsPublicKey) > 0 {
				return "已保存"
			}
			return "验证时动态获取"
		}())

	// 保存到数据库
	if err := d.state.StakeStore.setDelegateInfo(del.Address, delegateInfo, dbTx); err != nil {
		d.logger.Error("❌ Failed to save single delegate info", "address", del.Address.String(), "error", err)
		return fmt.Errorf("failed to save single delegate info for %s: %w", del.Address.String(), err)
	}

	// 提交事务
	if err := dbTx.Commit(); err != nil {
		d.logger.Error("❌ Failed to commit single delegate transaction", "error", err)
		return fmt.Errorf("failed to commit single delegate transaction: %w", err)
	}

	d.logger.Debug("✅ Single delegate info saved successfully", "address", del.Address.String(), "votingPower", del.VotingPower.String())
	return nil
}

// persistDelegateSetToDatabase 持久化验证者集合到数据库
func (d *DPoS) persistDelegateSetToDatabase(delegates validator.AccountSet) error {
	return d.persistDelegateSetToDatabaseWithTarget(delegates, types.ZeroAddress)
}

// persistDelegateSetToDatabaseWithTarget 持久化验证者集合到数据库（指定目标验证者）
func (d *DPoS) persistDelegateSetToDatabaseWithTarget(delegates validator.AccountSet, targetDelegate types.Address) error {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, skipping database persistence")
		return nil
	}

	// 如果指定了目标验证者，只更新该验证者；否则更新所有验证者
	if targetDelegate != (types.Address{}) {
		d.logger.Debug("💾 Starting targeted delegate persistence", "targetDelegate", targetDelegate.String())

		// 查找目标验证者
		var targetDel *validator.ValidatorMetadata
		for _, del := range delegates {
			if del.Address == targetDelegate {
				targetDel = del
				break
			}
		}

		if targetDel == nil {
			d.logger.Warn("⚠️ Target delegate not found in delegates list", "targetDelegate", targetDelegate.String())
			return fmt.Errorf("target delegate %s not found in delegates list", targetDelegate.String())
		}

		// 只更新目标验证者
		return d.persistSingleDelegateToDatabase(targetDel)
	}

	d.logger.Debug("💾 Starting delegate set persistence", "count", len(delegates))

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

	// 保存每个验证者信息
	for _, del := range delegates {
		// 修改：BLS公钥按需获取，不在验证者集合中强制要求
		var blsPublicKey []byte
		if del.BlsKey != nil {
			blsPublicKey = del.BlsKey.Marshal()
			d.logger.Debug("🔑 保存BLS公钥到数据库（已存在）",
				"address", del.Address.String(),
				"publicKeyLength", len(blsPublicKey))
		} else {
			// BLS公钥为nil是正常的，将在验证时动态获取
			d.logger.Debug("🔑 BLS公钥为nil，将在验证时动态获取",
				"address", del.Address.String(),
				"note", "BLS公钥按需获取机制")
			d.logger.Debug("ℹ️ 受托人BLS公钥为nil，尝试从创世文件恢复",
				"address", del.Address.String())

			// 从validator-bls.key文件中查找BLS公钥（始终在 nodeRoot/consensus/）
			if keyFilePath := d.validatorBLSKeyPath(); keyFilePath != "" {
				// 检查文件是否存在
				if _, err := os.Stat(keyFilePath); err == nil {
					// 读取私钥文件
					privateKeyData, err := os.ReadFile(keyFilePath)
					if err == nil {
						// 获取十六进制字符串（去除可能的换行符）
						privateKeyHex := strings.TrimSpace(string(privateKeyData))

						// 检查并修正私钥长度
						if len(privateKeyHex)%2 != 0 {
							privateKeyHex = "0" + privateKeyHex
						}

						// 解析BLS私钥
						privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
						if err == nil {
							// 从私钥生成公钥
							publicKey := privateKey.PublicKey()
							blsPublicKey = publicKey.Marshal()
							d.logger.Info("✅ 从validator-bls.key文件恢复BLS公钥并保存到数据库",
								"address", del.Address.String(),
								"publicKeyLength", len(blsPublicKey),
								"filePath", keyFilePath)
						} else {
							d.logger.Warn("⚠️ 解析validator-bls.key文件失败",
								"address", del.Address.String(),
								"filePath", keyFilePath,
								"error", err)
						}
					} else {
						d.logger.Warn("⚠️ 读取validator-bls.key文件失败",
							"address", del.Address.String(),
							"filePath", keyFilePath,
							"error", err)
					}
				} else {
					d.logger.Debug("ℹ️ validator-bls.key文件不存在，将在验证时按需获取",
						"address", del.Address.String(),
						"filePath", keyFilePath)
				}
			}
		}

		// 从数据库读取当前的 VotingPower，避免用内存中的错误值覆盖数据库
		dbVotingPower, err := d.getVotingPowerFromDatabase(del.Address)
		if err != nil {
			d.logger.Warn("⚠️ 从数据库读取 VotingPower 失败，使用内存中的值",
				"address", del.Address.String(),
				"error", err)
			dbVotingPower = new(big.Int).Set(del.VotingPower)
		} else {
			d.logger.Debug("✅ 从数据库读取 VotingPower",
				"address", del.Address.String(),
				"dbVotingPower", dbVotingPower.String(),
				"memoryVotingPower", del.VotingPower.String())
		}

		// 即使BLS公钥为nil，也允许保存受托人信息
		delegateInfo := &DelegateInfo{
			Address:        del.Address,
			VotingPower:    new(big.Int).Set(dbVotingPower), // 使用数据库中的值，而不是内存中的值
			TotalVotes:     new(big.Int).Set(dbVotingPower), // 使用VotingPower作为TotalVotes
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       del.IsActive,
			BlsPublicKey:   blsPublicKey, // 保存BLS公钥（可以为nil）
		}

		d.populateCommissionFields(del.Address, delegateInfo)

		// 新增：重点记录持久化时的isActive状态
		d.logger.Info("💾 持久化受托人信息到数据库",
			"address", del.Address.String(),
			"votingPower", del.VotingPower.String(),
			"isActive", del.IsActive,
			"isActiveType", fmt.Sprintf("%T", del.IsActive),
			"blsPublicKeyLength", len(blsPublicKey),
			"blsKeyStatus", func() string {
				if len(blsPublicKey) > 0 {
					return "已保存"
				}
				return "验证时动态获取"
			}())

		// 记录BLS公钥状态
		if blsPublicKey == nil {
			d.logger.Debug("ℹ️ 受托人BLS公钥为空，将在验证时按需获取",
				"address", del.Address.String())
		} else {
			d.logger.Debug("✅ 受托人BLS公钥正常，准备保存到数据库",
				"address", del.Address.String(),
				"blsKeyLength", len(blsPublicKey))
		}

		// 添加详细日志：检查调用setDelegateInfo前的数据
		d.logger.Debug("🔍 调用setDelegateInfo前的详细检查 (第二个位置)",
			"delegate", del.Address.String(),
			"votingPower", delegateInfo.VotingPower.String(),
			"totalVotes", delegateInfo.TotalVotes.String(),
			"isActive", delegateInfo.IsActive,
			"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))

		// 检查内存中对应受托人的状态
		for _, memDel := range d.delegates {
			if memDel.Address == del.Address {
				d.logger.Debug("🔍 内存中受托人状态",
					"address", memDel.Address.String(),
					"votingPower", memDel.VotingPower.String(),
					"isActive", memDel.IsActive)
				break
			}
		}

		if err := d.state.StakeStore.setDelegateInfo(del.Address, delegateInfo, dbTx); err != nil {
			d.logger.Error("❌ Failed to save delegate info", "address", del.Address.String(), "error", err)
			return fmt.Errorf("failed to save delegate info for %s: %w", del.Address.String(), err)
		}

		d.logger.Debug("✅ Saved delegate info", "address", del.Address.String(), "votingPower", del.VotingPower.String())
	}

	// 提交事务 - 添加超时机制
	d.logger.Debug("🔍 开始提交数据库事务")

	// 使用超时机制防止卡死
	commitDone := make(chan error, 1)
	go func() {
		commitDone <- dbTx.Commit()
	}()

	select {
	case err := <-commitDone:
		if err != nil {
			return fmt.Errorf("failed to commit db transaction: %w", err)
		}
	case <-time.After(3 * time.Second):
		d.logger.Error("❌ 数据库事务提交超时，强制回滚")
		dbTx.Rollback()
		return fmt.Errorf("database transaction commit timeout")
	}

	d.logger.Debug("✅ Delegate set persistence completed successfully")
	return nil
}

// restoreVotingDataFromDatabase 从数据库恢复投票数据
func (d *DPoS) restoreVotingDataFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, cannot restore voting data")
		return fmt.Errorf("state store not available")
	}

	// 恢复投票者信息
	if err := d.restoreVotersFromDatabase(); err != nil {
		d.logger.Error("Failed to restore voters from database", "error", err)
		return err
	}

	// 恢复受托人信息
	if err := d.restoreDelegatesFromDatabase(); err != nil {
		d.logger.Error("Failed to restore delegates from database", "error", err)
		return err
	}

	d.logger.Info("✅ Voting data restored from database",
		"votersCount", len(d.voters),
		"delegatesCount", len(d.delegates))

	return nil
}

// restoreVoteRecordsFromDatabase 从数据库恢复投票记录（用于边界应用）
func (d *DPoS) restoreVoteRecordsFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, cannot restore vote records")
		return fmt.Errorf("state store not available")
	}

	// 初始化 voteRecords map
	if d.voteRecords == nil {
		d.voteRecords = make(map[string]*VoteRecord)
	}

	// 从数据库读取所有 StakingInfo
	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		d.logger.Warn("Failed to get staking info for vote records", "error", err)
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	restoredCount := 0
	d.voteRecordsMutex.Lock()
	defer d.voteRecordsMutex.Unlock()

	for _, stakeInfo := range stakingInfos {
		// 只恢复未应用的投票记录（Applied=false）
		// 如果 EffectiveEpoch 为 0，说明是旧数据，跳过
		if stakeInfo.EffectiveEpoch == 0 {
			continue
		}

		// 创建投票记录
		voteKey := fmt.Sprintf("%s_%s_%d", stakeInfo.Staker.String(), stakeInfo.Delegate.String(), stakeInfo.StartTime)
		d.voteRecords[voteKey] = &VoteRecord{
			Voter:          stakeInfo.Staker,
			Delegate:       stakeInfo.Delegate,
			Amount:         new(big.Int).Set(stakeInfo.Amount),
			Timestamp:      stakeInfo.StartTime,
			EffectiveEpoch: stakeInfo.EffectiveEpoch,
			Applied:        stakeInfo.Applied,
		}
		restoredCount++
	}

	d.logger.Info("✅ Vote records restored from database",
		"restoredCount", restoredCount,
		"totalVoteRecords", len(d.voteRecords))

	return nil
}

// restoreVotersFromDatabase 从数据库恢复投票者信息
func (d *DPoS) restoreVotersFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 使用数据库事务来读取所有投票者信息
	err := d.state.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("VoterInfo"))
		if bucket == nil {
			d.logger.Debug("VoterInfo bucket not found, no voters to restore")
			return nil
		}

		// 遍历所有投票者
		cursor := bucket.Cursor()
		restoredCount := 0

		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var voterInfo VoterInfo
			if err := json.Unmarshal(value, &voterInfo); err != nil {
				d.logger.Warn("Failed to unmarshal voter info", "key", hex.EncodeToString(key), "error", err)
				continue
			}

			// 恢复投票者信息到内存
			d.lock.Lock()
			d.voters[voterInfo.Address] = &voterInfo
			d.lock.Unlock()

			restoredCount++
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to restore voters from database: %w", err)
	}

	return nil
}

// restoreDelegatesFromDatabase 从数据库恢复受托人信息
func (d *DPoS) restoreDelegatesFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 使用数据库事务来读取所有受托人信息
	err := d.state.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("DelegateInfo"))
		if bucket == nil {
			d.logger.Debug("DelegateInfo bucket not found, no delegates to restore")
			return nil
		}

		// 遍历所有受托人
		cursor := bucket.Cursor()
		restoredCount := 0

		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var delegateInfo DelegateInfo
			if err := json.Unmarshal(value, &delegateInfo); err != nil {
				d.logger.Warn("Failed to unmarshal delegate info", "key", hex.EncodeToString(key), "error", err)
				continue
			}

			// 恢复受托人信息到内存
			// 注意：这里需要将 DelegateInfo 转换为 validator.ValidatorMetadata
			// 或者更新现有的 delegates 集合
			d.logger.Debug("Found delegate in database",
				"address", delegateInfo.Address.String(),
				"votingPower", delegateInfo.VotingPower.String())

			restoredCount++
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to restore delegates from database: %w", err)
	}

	return nil
}

// getDelegateThreshold 获取委托门槛值（dpos_delegate_threshold）
// 优先级：参数系统 > 配置 > 默认值
func (d *DPoS) getDelegateThreshold() *big.Int {
	// 1. 优先从参数系统读取（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue("dpos_delegate_threshold"); err == nil {
		switch v := paramValue.(type) {
		case string:
			if bigAmount, ok := new(big.Int).SetString(v, 10); ok && bigAmount.Cmp(big.NewInt(0)) > 0 {
				d.logger.Debug("从参数系统读取 dpos_delegate_threshold", "value", v)
				return bigAmount
			}
		case *big.Int:
			if v != nil && v.Cmp(big.NewInt(0)) > 0 {
				d.logger.Debug("从参数系统读取 dpos_delegate_threshold", "value", v.String())
				return new(big.Int).Set(v)
			}
		}
	}

	// 2. 从配置读取
	if d.config != nil && d.config.DPoSDelegateThreshold != nil && d.config.DPoSDelegateThreshold.Cmp(big.NewInt(0)) > 0 {
		d.logger.Debug("从配置读取 dpos_delegate_threshold", "value", d.config.DPoSDelegateThreshold.String())
		return new(big.Int).Set(d.config.DPoSDelegateThreshold)
	}

	// 3. 使用默认值（如果配置和参数系统都没有）
	defaultValue, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
	d.logger.Warn("⚠️ 未找到 dpos_delegate_threshold 配置，使用默认值", "defaultValue", defaultValue.String())
	return defaultValue
}

// getGenesisVoteAmount 获取创世根账户给每个创世验证者的初始投票金额
// 优先级：参数系统 > 配置 > 回退到 dpos_delegate_threshold（向后兼容）
func (d *DPoS) getGenesisVoteAmount() *big.Int {
	// 1. 参数系统（如果未来走治理修改，这里会自动生效；未初始化/未配置则忽略）
	if paramValue, err := d.getCurrentParameterValue("dpos_genesis_vote_amount"); err == nil {
		switch v := paramValue.(type) {
		case string:
			if bigAmount, ok := new(big.Int).SetString(v, 10); ok && bigAmount.Cmp(big.NewInt(0)) > 0 {
				d.logger.Debug("从参数系统读取 dpos_genesis_vote_amount", "value", v)
				return bigAmount
			}
		case *big.Int:
			if v != nil && v.Cmp(big.NewInt(0)) > 0 {
				d.logger.Debug("从参数系统读取 dpos_genesis_vote_amount", "value", v.String())
				return new(big.Int).Set(v)
			}
		}
	}

	// 2. 配置读取
	if d.config != nil && d.config.DPoSGenesisVoteAmount != nil && d.config.DPoSGenesisVoteAmount.Cmp(big.NewInt(0)) > 0 {
		d.logger.Debug("从配置读取 dpos_genesis_vote_amount", "value", d.config.DPoSGenesisVoteAmount.String())
		return new(big.Int).Set(d.config.DPoSGenesisVoteAmount)
	}

	// 3. 回退：沿用 dpos_delegate_threshold（历史行为）
	return d.getDelegateThreshold()
}

// CreateGenesisVoteRecord 在共识切换高度创建根账户对创世验证者的投票记录（公开方法）
func (d *DPoS) CreateGenesisVoteRecord(voter types.Address, delegate types.Address, amount *big.Int, effectiveEpoch uint64, applied bool) error {
	return d.persistVoteToDatabase(voter, delegate, amount, effectiveEpoch, applied)
}

// collectGenesisValidatorAddresses 收集创世验证者地址（Initialize 后 runtime 已有、delegates 可能尚未同步）。
func (d *DPoS) collectGenesisValidatorAddresses() []types.Address {
	seen := make(map[types.Address]struct{})
	out := make([]types.Address, 0)

	addAddr := func(addr types.Address) {
		if addr == (types.Address{}) {
			return
		}
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	addSet := func(set validator.AccountSet) {
		for _, v := range set {
			if v != nil {
				addAddr(v.Address)
			}
		}
	}

	if d.config != nil {
		for _, gv := range d.config.InitialDelegates {
			addAddr(types.Address(gv.Address))
		}
	}
	addSet(d.GetCurrentDelegates())
	if d.runtime != nil {
		addSet(d.runtime.delegates)
	}
	addSet(d.GetGenesisValidatorSet())
	if len(out) == 0 {
		if bootstrap, _, err := d.getBootstrapValidators(); err == nil {
			addSet(bootstrap)
		}
	}
	return out
}

// CreateGenesisVotesForAllValidators 在共识切换高度为所有创世验证者创建根账户的投票记录（公开方法）
func (d *DPoS) CreateGenesisVotesForAllValidators(blockNumber uint64) error {
	// 检查是否是共识切换高度
	if d.config == nil || d.config.ConsensusSwitchHeight == 0 {
		return nil
	}

	if blockNumber != d.config.ConsensusSwitchHeight {
		return nil
	}

	// 根账户地址：从配置中获取（在Initialize时从创世文件alloc中读取并保存）
	rootAccount := d.config.GenesisRootAccount
	if rootAccount == (types.Address{}) {
		d.logger.Error("❌ 无法获取根账户地址：GenesisRootAccount未配置（应在Initialize时从创世文件alloc中读取）")
		return fmt.Errorf("root account address not found: GenesisRootAccount not configured")
	}

	// 从配置读取投票金额（优先 dpos_genesis_vote_amount，未配置则回退 dpos_delegate_threshold）
	voteAmount := d.getGenesisVoteAmount()

	// 从配置 / 内存 / runtime / bootstrap 收集创世验证者
	genesisValidators := d.collectGenesisValidatorAddresses()

	if len(genesisValidators) == 0 {
		d.logger.Error("❌ 创世验证者列表为空，无法创建投票记录",
			"blockNumber", blockNumber,
			"runtimeDelegates", func() int {
				if d.runtime != nil {
					return len(d.runtime.delegates)
				}
				return 0
			}())
		return fmt.Errorf("genesis validator list is empty at consensus switch height %d", blockNumber)
	}

	d.logger.Info("📋 准备为创世验证者创建投票记录",
		"blockNumber", blockNumber,
		"genesisValidatorsCount", len(genesisValidators))

	// 检查是否已创建投票记录（幂等性检查）
	var stakingInfos []*StakeInfo
	if d.state != nil && d.state.StakeStore != nil {
		var err error
		stakingInfos, err = d.state.StakeStore.GetStakingInfo()
		if err != nil {
			d.logger.Warn("⚠️ 获取投票记录失败，将尝试创建", "error", err)
		}
	}

	// 检查根账户是否已投票给所有创世验证者
	hasAllVotes := true
	for _, validatorAddr := range genesisValidators {
		hasVote := false
		for _, stakeInfo := range stakingInfos {
			if stakeInfo != nil && stakeInfo.Staker == rootAccount && stakeInfo.Delegate == validatorAddr {
				hasVote = true
				break
			}
		}
		if !hasVote {
			hasAllVotes = false
			break
		}
	}

	if hasAllVotes {
		d.logger.Info("✅ 根账户对创世验证者的投票记录已存在，跳过创建",
			"blockNumber", blockNumber,
			"genesisValidatorsCount", len(genesisValidators))
		return nil
	}

	// 创建投票记录
	d.logger.Debug("开始创建根账户对创世验证者的投票记录",
		"blockNumber", blockNumber,
		"rootAccount", rootAccount.String(),
		"genesisValidatorsCount", len(genesisValidators),
		"voteAmount", voteAmount.String())

	createdCount := 0
	for _, validatorAddr := range genesisValidators {
		// 再次检查是否已存在（双重检查）
		hasVote := false
		for _, stakeInfo := range stakingInfos {
			if stakeInfo != nil && stakeInfo.Staker == rootAccount && stakeInfo.Delegate == validatorAddr {
				hasVote = true
				break
			}
		}
		if hasVote {
			d.logger.Debug("投票记录已存在，跳过",
				"validator", validatorAddr.String())
			continue
		}

		// 使用公开方法创建投票记录
		if err := d.CreateGenesisVoteRecord(rootAccount, validatorAddr, voteAmount, 1, true); err != nil {
			d.logger.Error("❌ 创建投票记录失败",
				"validator", validatorAddr.String(),
				"error", err)
			return fmt.Errorf("failed to create vote record for validator %s: %w", validatorAddr.String(), err)
		}

		createdCount++
	}

	d.logger.Info("✅ 根账户对创世验证者的投票记录创建完成",
		"blockNumber", blockNumber,
		"createdCount", createdCount,
		"totalValidators", len(genesisValidators))

	return nil
}
