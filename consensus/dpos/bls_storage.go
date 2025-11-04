package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// saveValidatorsWithBLSKeysToDatabase 保存验证者信息（含BLS公钥）到数据库
func (d *DPoS) saveValidatorsWithBLSKeysToDatabase() error {
	d.logger.Info("💾 开始保存验证者信息（含BLS公钥）到数据库...")

	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法保存验证者信息到数据库")
		return fmt.Errorf("state store not available")
	}

	validators := d.getAllValidators()
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需保存到数据库")
		return nil
	}

	syncedCount := 0
	for i, validator := range validators {
		// 🆕 添加详细日志：打印验证者信息
		d.logger.Info("🔍 准备保存验证者信息到数据库",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)

		// 创建DelegateInfo结构
		delegateInfo := &DelegateInfo{
			Address:        validator.Address,
			VotingPower:    new(big.Int).Set(validator.VotingPower),
			TotalVotes:     new(big.Int).Set(validator.VotingPower), // 使用VotingPower作为TotalVotes
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       validator.IsActive,
			BlsPublicKey:   []byte{}, // 初始为空
		}

		// 🆕 添加详细日志：打印DelegateInfo信息
		d.logger.Debug("🔍 DelegateInfo详细信息",
			"address", delegateInfo.Address.String(),
			"votingPower", delegateInfo.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", delegateInfo.VotingPower.Bytes()),
			"totalVotes", delegateInfo.TotalVotes.String(),
			"totalVotesHex", fmt.Sprintf("0x%x", delegateInfo.TotalVotes.Bytes()),
			"isActive", delegateInfo.IsActive,
			"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))

		// 如果有BLS公钥，保存到DelegateInfo中
		if validator.BlsKey != nil {
			blsKeyBytes := validator.BlsKey.Marshal()
			delegateInfo.BlsPublicKey = blsKeyBytes
			d.logger.Debug("✅ 保存验证者BLS公钥到数据库",
				"address", validator.Address.String(),
				"blsKeyLength", len(blsKeyBytes))
		}

		// 保存到数据库
		if err := d.state.StakeStore.setDelegateInfo(validator.Address, delegateInfo, nil); err != nil {
			d.logger.Warn("⚠️ 保存验证者信息到数据库失败",
				"address", validator.Address.String(),
				"error", err)
		} else {
			syncedCount++
			// 🆕 添加详细日志：打印保存成功后的信息
			d.logger.Info("✅ 验证者信息已保存到数据库",
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil)

			// 打印BLS公钥详细信息
			if validator.BlsKey != nil {
				blsKeyBytes := validator.BlsKey.Marshal()
				d.logger.Info("✅ 验证者信息（含BLS公钥）已保存到数据库",
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
					"isActive", validator.IsActive,
					"hasBlsKey", true,
					"blsKeyLength", len(blsKeyBytes),
					"blsKeyHex", fmt.Sprintf("%x", blsKeyBytes[:16])+"...")
			} else {
				d.logger.Info("✅ 验证者信息（无BLS公钥）已保存到数据库",
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
					"isActive", validator.IsActive,
					"hasBlsKey", false)
			}
		}
	}

	d.logger.Info("💾 验证者信息（含BLS公钥）保存到数据库完成", "syncedCount", syncedCount, "totalValidators", len(validators))
	return nil
}

// getCurrentDelegateInfo 获取当前的DelegateInfo（只更新BLS公钥时使用）
func (d *DPoS) getCurrentDelegateInfo(address types.Address) (*DelegateInfo, error) {
	if d.state == nil || d.state.StakeStore == nil {
		return nil, fmt.Errorf("state store not available")
	}

	// 从数据库读取当前的DelegateInfo
	var currentInfo *DelegateInfo
	err := d.state.StakeStore.db.View(func(tx *bolt.Tx) error {
		delegateBucket := tx.Bucket([]byte("DelegateInfo"))
		if delegateBucket == nil {
			return fmt.Errorf("DelegateInfo bucket not found")
		}

		data := delegateBucket.Get(address[:])
		if data == nil {
			return fmt.Errorf("delegate info not found for address %s", address.String())
		}

		currentInfo = &DelegateInfo{}
		return json.Unmarshal(data, currentInfo)
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get current delegate info: %w", err)
	}

	return currentInfo, nil
}

// saveBLSKeyToDatabase 保存BLS公钥到数据库
func (d *DPoS) saveBLSKeyToDatabase(address types.Address, blsKey *bls.PublicKey) {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法保存BLS公钥到数据库")
		return
	}

	// 🆕 方案3：只更新BLS公钥，保持其他字段不变
	// 从数据库读取当前的DelegateInfo
	currentInfo, err := d.getCurrentDelegateInfo(address)
	if err != nil {
		d.logger.Error("❌ 无法获取当前验证者信息", "address", address.String(), "error", err)
		return
	}

	// 记录更新前的信息
	d.logger.Info("🔍 准备更新BLS公钥（保持其他字段不变）",
		"address", address.String(),
		"currentVotingPower", currentInfo.VotingPower.String(),
		"currentTotalVotes", currentInfo.TotalVotes.String(),
		"currentIsActive", currentInfo.IsActive,
		"currentBlsKeyLength", len(currentInfo.BlsPublicKey))

	// 只更新BLS公钥字段，保持其他字段不变
	if blsKey != nil {
		blsKeyBytes := blsKey.Marshal()
		currentInfo.BlsPublicKey = blsKeyBytes

		d.logger.Info("🔍 更新后的DelegateInfo信息",
			"address", currentInfo.Address.String(),
			"votingPower", currentInfo.VotingPower.String(), // 保持不变
			"totalVotes", currentInfo.TotalVotes.String(), // 保持不变
			"isActive", currentInfo.IsActive, // 保持不变
			"newBlsKeyLength", len(currentInfo.BlsPublicKey)) // 只更新这个
	}

	// 保存到数据库（只更新BLS公钥，其他字段保持不变）
	if err := d.state.StakeStore.setDelegateInfo(address, currentInfo, nil); err != nil {
		d.logger.Error("❌ 保存BLS公钥失败", "address", address.String(), "error", err)
	} else {
		d.logger.Info("✅ BLS公钥已保存到数据库（其他字段保持不变）",
			"address", address.String(),
			"votingPower", currentInfo.VotingPower.String(),
			"totalVotes", currentInfo.TotalVotes.String(),
			"blsKeyLength", len(currentInfo.BlsPublicKey),
			"note", "只更新BLS公钥，权重和票数保持不变")
	}
}

// loadBLSKeysFromDatabase 从数据库加载BLS公钥到缓存
func (d *DPoS) loadBLSKeysFromDatabase() error {
	d.logger.Debug("📚 从数据库加载BLS公钥到缓存...")

	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法从数据库加载BLS公钥")
		return fmt.Errorf("state store not available")
	}

	// 从数据库获取所有验证者信息
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		d.logger.Warn("⚠️ 获取验证者信息失败", "error", err)
		return err
	}

	loadedCount := 0
	for _, validator := range validators {
		if validator.BlsKey != nil {
			// 将BLS公钥只加载到网络集成层缓存，不写入数据库
			if d.runtime != nil && d.runtime.networkIntegration != nil {
				blsKeyBytes := validator.BlsKey.Marshal()
				if err := d.runtime.networkIntegration.LoadBLSKeyToCache(validator.Address, blsKeyBytes); err != nil {
					d.logger.Warn("⚠️ 加载BLS公钥到缓存失败",
						"address", validator.Address.String(),
						"error", err)
				} else {
					loadedCount++
					d.logger.Debug("✅ 从数据库加载BLS公钥到缓存成功",
						"address", validator.Address.String(),
						"blsKeyLength", len(blsKeyBytes))
				}
			}
		}
	}

	d.logger.Debug("📚 数据库BLS公钥加载完成", "loadedCount", loadedCount, "totalValidators", len(validators))
	return nil
}

// persistBLSKeyToStakeStore 将BLS公钥持久化到StakeStore
func (d *DPoS) persistBLSKeyToStakeStore(address types.Address, blsKeyBytes []byte) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("StakeStore not available")
	}

	// 🆕 修复：从数据库读取当前的DelegateInfo，保持VotingPower和TotalVotes不变
	currentInfo, err := d.getCurrentDelegateInfo(address)
	if err != nil {
		// 如果读取失败，使用默认值
		d.logger.Warn("⚠️ 无法读取当前验证者信息，使用默认值", "address", address.String(), "error", err)
		currentInfo = &DelegateInfo{
			Address:        address,
			VotingPower:    big.NewInt(0),
			TotalVotes:     big.NewInt(0),
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       true,
		}
	}

	// 只更新BLS公钥，保持其他字段不变
	currentInfo.BlsPublicKey = blsKeyBytes

	d.logger.Info("🔍 persistBLSKeyToStakeStore: 准备保存BLS公钥（保持其他字段不变）",
		"address", address.String(),
		"votingPower", currentInfo.VotingPower.String(),
		"totalVotes", currentInfo.TotalVotes.String(),
		"isActive", currentInfo.IsActive,
		"blsKeyLength", len(blsKeyBytes))

	// 保存到StakeStore
	if err := d.state.StakeStore.setDelegateInfo(address, currentInfo, nil); err != nil {
		return fmt.Errorf("failed to save BLS key to StakeStore: %w", err)
	}

	d.logger.Info("✅ BLS公钥已保存到StakeStore（其他字段保持不变）",
		"address", address.String(),
		"votingPower", currentInfo.VotingPower.String(),
		"totalVotes", currentInfo.TotalVotes.String(),
		"blsKeyLength", len(blsKeyBytes))

	return nil
}




