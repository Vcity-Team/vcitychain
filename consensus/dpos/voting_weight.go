package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// getVotersForValidator 获取投票给指定验证者的投票者列表
func (d *DPoS) getVotersForValidator(validatorAddress types.Address) []*VoterInfo {
	d.logger.Debug("🔍 获取投票者列表", "validatorAddress", validatorAddress.String())

	var voters []*VoterInfo

	// 🆕 修复：直接从内存中的d.voters获取数据，而不是从StakingInfo表
	// 因为StakingInfo表从未被写入数据，投票数据实际存储在VoterInfo表中
	for voterAddress, voterInfo := range d.voters {
		d.logger.Debug("🔍 检查投票者",
			"voterAddress", voterAddress.String(),
			"votedDelegatesCount", len(voterInfo.VotedDelegates),
			"targetValidator", validatorAddress.String())

		// 检查该投票者是否投票给了目标验证者
		for i, votedDelegate := range voterInfo.VotedDelegates {
			d.logger.Debug("🔍 比较验证者地址",
				"voterAddress", voterAddress.String(),
				"votedDelegateIndex", i,
				"votedDelegate", votedDelegate.String(),
				"targetValidator", validatorAddress.String(),
				"isMatch", votedDelegate == validatorAddress)

			if votedDelegate == validatorAddress {
				voters = append(voters, &VoterInfo{
					Address:        voterInfo.Address,
					VotingPower:    new(big.Int).Set(voterInfo.VotingPower),
					VotedDelegates: voterInfo.VotedDelegates,
					LastVoteTime:   voterInfo.LastVoteTime,
					LockedUntil:    voterInfo.LockedUntil,
					Nonce:          voterInfo.Nonce,
				})
				d.logger.Debug("✅ 找到投票者",
					"voterAddress", voterAddress.String(),
					"votingPower", voterInfo.VotingPower.String(),
					"targetValidator", validatorAddress.String())
				break // 找到后跳出内层循环
			}
		}
	}

	d.logger.Debug("投票者列表获取完成",
		"validatorAddress", validatorAddress.String(),
		"votersCount", len(voters))

	return voters
}

// getTotalVotesForValidator 获取投票给指定验证者的总投票权重
func (d *DPoS) getTotalVotesForValidator(validatorAddress types.Address) *big.Int {
	d.logger.Debug("🔍 计算总投票权重", "validatorAddress", validatorAddress.String())

	voters := d.getVotersForValidator(validatorAddress)
	totalVotes := big.NewInt(0)

	for _, voter := range voters {
		totalVotes.Add(totalVotes, voter.VotingPower)
		d.logger.Debug("累加投票权重",
			"voterAddress", voter.Address.String(),
			"votingPower", voter.VotingPower.String(),
			"totalSoFar", totalVotes.String())
	}

	d.logger.Debug("总投票权重计算完成",
		"validatorAddress", validatorAddress.String(),
		"totalVotes", totalVotes.String())

	return totalVotes
}

// getVoterVotingWeight 获取投票者的质押权重（所有用户都可以投票）
func (d *DPoS) getVoterVotingWeight(voter types.Address) *big.Int {
	// 优先从数据库的DelegateInfo中读取，确保数据最全且一致
	if d.state != nil && d.state.StakeStore != nil {
		// 从数据库获取所有验证者信息
		validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		if err != nil {
			d.logger.Warn("Failed to get validators from database for voter weight", "error", err)
			return big.NewInt(0)
		}

		// 查找指定投票者的权重
		for _, validator := range validators {
			if validator.Address == voter {
				d.logger.Debug("Voter voting weight from database",
					"voter", voter.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive)
				return validator.VotingPower
			}
		}

		// 如果没找到，说明不是验证者，尝试从质押信息中获取
		stakingInfo, err := d.state.StakeStore.GetStakingInfo()
		if err != nil {
			d.logger.Warn("Failed to get staking info for voter weight", "error", err)
			return big.NewInt(0)
		}

		totalStaked := big.NewInt(0)
		for _, stake := range stakingInfo {
			if stake.Staker == voter {
				totalStaked.Add(totalStaked, stake.Amount)
			}
		}

		d.logger.Debug("Voter voting weight from staking info",
			"voter", voter.String(),
			"totalStaked", totalStaked.String())

		return totalStaked
	}

	return big.NewInt(0)
}

// GetVotingPower 获取指定区块号和委托者的投票权重
func (d *DPoS) GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 获取受托人的投票权重
	for _, d := range d.delegates {
		if d.Address == delegate {
			return new(big.Int).Set(d.VotingPower), nil
		}
	}

	return big.NewInt(0), nil
}

// GetVotingPowerWithTx 在事务中获取投票权重
func (d *DPoS) GetVotingPowerWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// 在事务中获取投票权重
	return d.getVotingPowerFromStateWithTx(blockNumber, delegate, dbTx)
}

// getVotingPowerFromStateWithTx 从状态获取投票权重（带事务）
func (d *DPoS) getVotingPowerFromStateWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接调用内存版本，避免复杂的数据库操作
	return d.GetVotingPower(blockNumber, delegate)
}

// getVotingPowerFromDatabase 从数据库获取验证者的投票权重
func (d *DPoS) getVotingPowerFromDatabase(delegate types.Address) (*big.Int, error) {
	if d.state == nil || d.state.StakeStore == nil {
		return nil, fmt.Errorf("state store not available")
	}

	// 从数据库获取所有验证者信息
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return nil, fmt.Errorf("failed to get validators from database: %w", err)
	}

	// 查找指定验证者的权重
	for _, validator := range validators {
		if validator.Address == delegate {
			d.logger.Debug("🔍 从数据库读取验证者权重",
				"delegate", delegate.String(),
				"votingPower", validator.VotingPower.String(),
				"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
				"isActive", validator.IsActive)
			return validator.VotingPower, nil
		}
	}

	// 如果没找到，返回0（新验证者）
	d.logger.Debug("🔍 验证者在数据库中不存在，返回0权重",
		"delegate", delegate.String())
	return big.NewInt(0), nil
}

// updateVotingPowerInDatabase 直接更新数据库中的验证者投票权重
func (d *DPoS) updateVotingPowerInDatabase(delegate types.Address, newPower *big.Int) error {
	return d.updateVotingPowerInDatabaseWithTx(delegate, newPower, nil)
}

// updateVotingPowerInDatabaseWithTx 使用外部事务更新数据库中的验证者投票权重
func (d *DPoS) updateVotingPowerInDatabaseWithTx(delegate types.Address, newPower *big.Int, dbTx *bolt.Tx) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 创建或更新验证者信息
	delegateInfo := &DelegateInfo{
		Address:        delegate,
		VotingPower:    new(big.Int).Set(newPower),
		TotalVotes:     new(big.Int).Set(newPower), // 🆕 修复：初始化TotalVotes字段
		ProducedBlocks: 0,
		MissedBlocks:   0,
		LastBlockTime:  0,
		IsActive:       newPower.Cmp(big.NewInt(0)) > 0,
		IsRegistered:   true, // 假设已注册
		BlsPublicKey:   nil,  // BLS密钥由其他逻辑处理
	}

	d.populateCommissionFields(delegate, delegateInfo)

	// 🆕 使用外部事务，避免嵌套事务
	err := d.state.StakeStore.setDelegateInfo(delegate, delegateInfo, dbTx)
	if err != nil {
		d.logger.Error("❌ 更新数据库验证者投票权重失败",
			"delegate", delegate.String(),
			"newPower", newPower.String(),
			"error", err)
		return fmt.Errorf("failed to update delegate voting power in database: %w", err)
	}

	d.logger.Debug("✅ 数据库验证者投票权重更新成功",
		"delegate", delegate.String(),
		"newPower", newPower.String(),
		"newPowerHex", fmt.Sprintf("0x%x", newPower.Bytes()))

	return nil
}

// updateDelegateVotingPower 更新委托者投票权重（已废弃但保留用于向后兼容）
func (d *DPoS) updateDelegateVotingPower(delegate types.Address, amount *big.Int) {
	d.logger.Warn("⚠️ updateDelegateVotingPower已废弃，请使用数据库直接操作",
		"delegate", delegate.String(),
		"amount", amount.String(),
		"note", "此函数保留仅用于向后兼容性")

	// 🆕 检查是否为创世验证者
	if d.isGenesisValidator(delegate) {
		d.logger.Info("🔒 创世验证者权重保持不变，跳过更新",
			"address", delegate.String(),
			"amount", amount.String(),
			"note", "创世验证者权重永远不变")
		return
	}

	// 🆕 新的实现：直接操作数据库
	// 1. 从数据库读取当前权重
	currentPower, err := d.getVotingPowerFromDatabase(delegate)
	if err != nil {
		d.logger.Error("❌ 从数据库读取验证者权重失败",
			"delegate", delegate.String(),
			"error", err)
		return
	}

	// 2. 计算新权重
	newPower := new(big.Int).Add(currentPower, amount)

	// 3. 直接更新数据库
	err = d.updateVotingPowerInDatabase(delegate, newPower)
	if err != nil {
		d.logger.Error("❌ 更新数据库验证者权重失败",
			"delegate", delegate.String(),
			"newPower", newPower.String(),
			"error", err)
		return
	}

	// 4. 数据库更新成功后，同步到内存
	err = d.syncDelegateFromDatabase(delegate)
	if err != nil {
		d.logger.Warn("⚠️ 同步验证者信息到内存失败",
			"delegate", delegate.String(),
			"error", err)
		// 不返回错误，因为数据库更新已经成功
	}

	d.logger.Info("✅ 验证者投票权重更新完成（数据库直接操作）",
		"delegate", delegate.String(),
		"originalPower", currentPower.String(),
		"addedAmount", amount.String(),
		"newPower", newPower.String(),
		"dataSource", "database")
}

// persistDelegateVotingPower 持久化委托者的投票权重
func (d *DPoS) persistDelegateVotingPower(delegate types.Address, amount *big.Int) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	d.logger.Info("🔄 persistDelegateVotingPower: 开始持久化验证者投票权重",
		"delegate", delegate.String(),
		"amount", amount.String(),
		"dataSource", "database")

	// 🆕 新的实现：直接从数据库读取当前权重，不依赖内存数据
	currentPower, err := d.getVotingPowerFromDatabase(delegate)
	if err != nil {
		d.logger.Error("❌ 从数据库读取验证者权重失败",
			"delegate", delegate.String(),
			"error", err)
		return fmt.Errorf("failed to get voting power from database: %w", err)
	}

	// 🆕 计算新的投票权重
	newPower := new(big.Int).Add(currentPower, amount)

	// 🆕 检查是否为创世验证者
	if d.isGenesisValidator(delegate) {
		fixedVotingPower := new(big.Int)
		fixedVotingPower.SetString("1000000000000000000000", 10) // 1000 VCITY
		newPower = fixedVotingPower
		d.logger.Info("🔒 创世验证者使用固定权重",
			"delegate", delegate.String(),
			"fixedPower", newPower.String())
	}

	// 🆕 创建或更新受托人信息，直接使用计算出的新权重
	delegateInfo := &DelegateInfo{
		Address:        delegate,
		VotingPower:    new(big.Int).Set(newPower),
		TotalVotes:     new(big.Int).Set(newPower), // TotalVotes等于VotingPower
		ProducedBlocks: 0,
		MissedBlocks:   0,
		LastBlockTime:  0,
		IsActive:       newPower.Cmp(big.NewInt(0)) > 0,
	}

	d.populateCommissionFields(delegate, delegateInfo)

	d.logger.Info("🔍 准备保存验证者信息到数据库",
		"delegate", delegate.String(),
		"originalPower", currentPower.String(),
		"addedAmount", amount.String(),
		"newPower", newPower.String(),
		"isActive", delegateInfo.IsActive,
		"dataSource", "database")

	// 🆕 直接保存到数据库
	if err := d.state.StakeStore.setDelegateInfo(delegate, delegateInfo, nil); err != nil {
		d.logger.Error("❌ 保存验证者信息到数据库失败",
			"delegate", delegate.String(),
			"newPower", newPower.String(),
			"error", err)
		return fmt.Errorf("failed to save delegate info to database: %w", err)
	}

	d.logger.Info("✅ 验证者信息已成功保存到数据库",
		"delegate", delegate.String(),
		"newPower", newPower.String(),
		"isActive", delegateInfo.IsActive,
		"dataSource", "database")

	return nil
}
