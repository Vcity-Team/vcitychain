package dpos

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
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
func (d *DPoS) persistVoteToDatabase(voter types.Address, candidate types.Address, amount *big.Int) error {
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

	// 获取当前投票者信息
	d.logger.Debug("🔍 Getting voter info from memory...")

	// 添加 defer 确保能看到是否进入了锁
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("❌ Panic in persistVoteToDatabase", "panic", r)
		}
	}()

	// 🚨 注意：调用者已经持有写锁，所以这里不需要再获取锁
	d.logger.Debug("🔍 Accessing d.voters map (caller already holds write lock)...")

	voterInfo, exists := d.voters[voter]
	d.logger.Debug("🔍 Voter lookup completed", "exists", exists)

	d.logger.Debug("🔍 Voter info retrieved",
		"exists", exists,
		"voter", voter.String())

	if !exists {
		d.logger.Error("❌ Voter info not found in memory", "voter", voter.String())
		return fmt.Errorf("voter info not found in memory for address %s", voter.String())
	}

	d.logger.Info("✅ Voter info found",
		"votingPower", voterInfo.VotingPower.String(),
		"votedDelegatesCount", len(voterInfo.VotedDelegates))

	d.logger.Debug("💾 Persisting vote to database",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String(),
		"votingPower", voterInfo.VotingPower.String(),
		"votedDelegatesCount", len(voterInfo.VotedDelegates))

	// 🆕 添加详细日志：记录数据库更新前的内存状态
	d.logger.Info("🔍 Before database persistence - memory delegates state:")
	for i, del := range d.delegates {
		if del.Address == candidate {
			d.logger.Info("🔍 Target delegate before database persistence",
				"index", i,
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String(),
				"isActive", del.IsActive)
		}
	}

	// 保存投票者信息到数据库
	d.logger.Info("💾 Saving voter info to database...")
	if err := d.state.StakeStore.setVoterInfo(voter, voterInfo, nil); err != nil {
		d.logger.Error("❌ Failed to save voter info to database", "error", err)
		return fmt.Errorf("failed to save voter info to database: %w", err)
	}
	d.logger.Info("✅ Voter info saved to database successfully")

	// 🆕 移除：不再在这里更新 VotingPower，因为 processVoteInternal 已经更新了
	// VotingPower 的更新已经在 processVoteInternal 中通过 updateVotingPowerInDatabase 完成
	// 这里再次调用会导致重复累加
	d.logger.Debug("💾 VotingPower 已在 processVoteInternal 中更新，跳过重复更新")

	// 🆕 保存 StakingInfo 到数据库（投票记录）
	// 注意：这里使用 voter 作为 staker，因为投票者就是质押者
	d.logger.Info("💾 Saving staking info to database...")

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		d.logger.Error("❌ Failed to begin db transaction for staking info", "error", err)
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

	// 创建 StakeInfo
	stakeInfo := &StakeInfo{
		Staker:    voter,
		Amount:    new(big.Int).Set(amount),
		StartTime: voterInfo.LastVoteTime,
		EndTime:   voterInfo.LockedUntil,
		IsLocked:  voterInfo.LockedUntil > uint64(time.Now().Unix()),
		IsActive:  len(voterInfo.VotedDelegates) > 0,
		Rewards:   big.NewInt(0), // TODO: 实现奖励计算
		Delegate:  candidate,     // 投票给哪个 delegate
	}

	// 保存到数据库
	// 🆕 传入 voterInfo.LastVoteTime 作为 timestamp，确保每次投票都有唯一 key
	if err := d.state.StakeStore.setStakingInfo(voter, stakeInfo, voterInfo.LastVoteTime, dbTx); err != nil {
		d.logger.Error("❌ Failed to save staking info to database", "error", err)
		return fmt.Errorf("failed to save staking info to database: %w", err)
	}

	// 提交事务
	if err := dbTx.Commit(); err != nil {
		d.logger.Error("❌ Failed to commit staking info transaction", "error", err)
		return fmt.Errorf("failed to commit staking info transaction: %w", err)
	}

	d.logger.Info("✅ Staking info saved to database successfully",
		"staker", voter.String(),
		"delegate", candidate.String(),
		"amount", amount.String())

	return nil
}

// saveValidatorSetForBlock 已移除数据库保存机制，改为日志记录
func (d *DPoS) saveValidatorSetForBlock(blockNumber uint64) error {
	// 🆕 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
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
	// 🆕 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
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
	d.logger.Info("🔍 开始从数据库加载验证者并按voterpower排序截取前N个")

	// 🆕 使用公共函数获取排序和限制后的验证者
	dbValidators, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		return fmt.Errorf("failed to get sorted validators with limit: %w", err)
	}

	if len(dbValidators) == 0 {
		return fmt.Errorf("no validators in database")
	}

	d.logger.Info("✅ 从数据库成功读取验证者", "count", len(dbValidators))

	// 🆕 添加详细日志：打印从数据库读取的验证者信息
	d.logger.Info("🔍 数据库验证者详细信息:")
	for i, validator := range dbValidators {
		// 🆕 获取验证者的故障标志信息
		faultInfo := d.getValidatorFaultInfo(validator.Address)
		d.logger.Info("🔍 数据库验证者",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil,
			"faultFlag", faultInfo) // 🆕 添加故障标志信息
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

		// 从validator-bls.key文件中查找BLS公钥
		dataDir := d.getDataDir()
		if dataDir != "" {
			// 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
			// dataDir = "node1\dpos"，需要回到 "node1\consensus"
			parentDir := filepath.Dir(dataDir) // 获取 "node1"
			keyFilePath := filepath.Join(parentDir, "consensus", "validator-bls.key")

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

	// 🆕 从数据库读取当前的 VotingPower，避免用内存中的错误值覆盖数据库
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
		VotingPower:    new(big.Int).Set(dbVotingPower), // 🆕 使用数据库中的值，而不是内存中的值
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
		// 🆕 修改：BLS公钥按需获取，不在验证者集合中强制要求
		var blsPublicKey []byte
		if del.BlsKey != nil {
			blsPublicKey = del.BlsKey.Marshal()
			d.logger.Debug("🔑 保存BLS公钥到数据库（已存在）",
				"address", del.Address.String(),
				"publicKeyLength", len(blsPublicKey))
		} else {
			// 🆕 BLS公钥为nil是正常的，将在验证时动态获取
			d.logger.Debug("🔑 BLS公钥为nil，将在验证时动态获取",
				"address", del.Address.String(),
				"note", "BLS公钥按需获取机制")
			d.logger.Debug("ℹ️ 受托人BLS公钥为nil，尝试从创世文件恢复",
				"address", del.Address.String())

			// 从validator-bls.key文件中查找BLS公钥
			dataDir := d.getDataDir()
			if dataDir != "" {
				// 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
				// dataDir = "node1\dpos"，需要回到 "node1\consensus"
				parentDir := filepath.Dir(dataDir) // 获取 "node1"
				keyFilePath := filepath.Join(parentDir, "consensus", "validator-bls.key")

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

		// 🆕 从数据库读取当前的 VotingPower，避免用内存中的错误值覆盖数据库
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
			VotingPower:    new(big.Int).Set(dbVotingPower), // 🆕 使用数据库中的值，而不是内存中的值
			TotalVotes:     new(big.Int).Set(dbVotingPower), // 使用VotingPower作为TotalVotes
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       del.IsActive,
			BlsPublicKey:   blsPublicKey, // 🆕 保存BLS公钥（可以为nil）
		}

		d.populateCommissionFields(del.Address, delegateInfo)

		// 🆕 新增：重点记录持久化时的isActive状态
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

		// 🆕 添加详细日志：检查调用setDelegateInfo前的数据
		d.logger.Debug("🔍 调用setDelegateInfo前的详细检查 (第二个位置)",
			"delegate", del.Address.String(),
			"votingPower", delegateInfo.VotingPower.String(),
			"totalVotes", delegateInfo.TotalVotes.String(),
			"isActive", delegateInfo.IsActive,
			"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))

		// 🆕 检查内存中对应受托人的状态
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
