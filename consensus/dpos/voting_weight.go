package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// getVotersForValidator 获取投票给指定验证者的投票者列表
// 从 StakeInfo 计算，不再依赖 VoterInfo 数据库
func (d *DPoS) getVotersForValidator(validatorAddress types.Address) []*VoterInfo {
	d.logger.Debug("🔍 获取投票者列表", "validatorAddress", validatorAddress.String())

	var voters []*VoterInfo

	// 从 StakeInfo 获取投票记录
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ StakeStore 不可用，无法获取投票者列表")
		return voters
	}

	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		d.logger.Warn("⚠️ 获取 StakeInfo 失败", "error", err)
		return voters
	}

	// 按 staker 分组，计算每个投票者的 VotingPower
	voterMap := make(map[types.Address]*VoterInfo)

	for _, stake := range stakingInfos {
		if stake == nil || stake.Staker == (types.Address{}) {
			continue
		}

		// 只统计已应用的投票（Applied=true）
		if !stake.Applied {
			continue
		}

		// 只统计投票给目标验证者的记录
		if stake.Delegate != validatorAddress {
			continue
		}

		voterAddr := stake.Staker

		// 如果该投票者不存在，创建新的 VoterInfo
		if _, exists := voterMap[voterAddr]; !exists {
			voterMap[voterAddr] = &VoterInfo{
				Address:        voterAddr,
				VotingPower:    big.NewInt(0),
				VotedDelegates: []types.Address{validatorAddress}, // 只包含目标验证者
				LastVoteTime:   stake.StartTime,
				LockedUntil:    stake.EndTime,
				Nonce:          make(map[uint64]bool),
			}
		}

		voter := voterMap[voterAddr]

		// 累加 VotingPower
		if stake.Amount != nil && stake.Amount.Sign() > 0 {
			voter.VotingPower.Add(voter.VotingPower, stake.Amount)
		}

		// 更新 LastVoteTime（取最新的）
		if stake.StartTime > voter.LastVoteTime {
			voter.LastVoteTime = stake.StartTime
		}
		// 更新 LockedUntil（取最新的）
		if stake.EndTime > voter.LockedUntil {
			voter.LockedUntil = stake.EndTime
		}
	}

	// 转换为列表
	for _, voter := range voterMap {
		voters = append(voters, voter)
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
// GetVotingPower 获取指定区块的投票权重（公共方法，供外部调用）
// 注意：不能通过接口调用，因为GetVotingPower本身就是接口的实现
// 直接使用原有逻辑，避免无限递归
func (d *DPoS) GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error) {
	// 直接使用原有逻辑，避免通过接口调用造成无限递归
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
	// 修复：简化实现，避免数据库事务死锁
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
		TotalVotes:     new(big.Int).Set(newPower), // 修复：初始化TotalVotes字段
		ProducedBlocks: 0,
		MissedBlocks:   0,
		LastBlockTime:  0,
		IsActive:       newPower.Cmp(big.NewInt(0)) > 0,
		IsRegistered:   true, // 假设已注册
		BlsPublicKey:   nil,  // BLS密钥由其他逻辑处理
	}

	d.populateCommissionFields(delegate, delegateInfo)

	// 使用外部事务，避免嵌套事务
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

	// 检查是否为创世验证者
	if d.isGenesisValidator(delegate) {
		d.logger.Info("🔒 创世验证者权重保持不变，跳过更新",
			"address", delegate.String(),
			"amount", amount.String(),
			"note", "创世验证者权重永远不变")
		return
	}

	// 新的实现：直接操作数据库
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

	// 新的实现：直接从数据库读取当前权重，不依赖内存数据
	currentPower, err := d.getVotingPowerFromDatabase(delegate)
	if err != nil {
		d.logger.Error("❌ 从数据库读取验证者权重失败",
			"delegate", delegate.String(),
			"error", err)
		return fmt.Errorf("failed to get voting power from database: %w", err)
	}

	// 计算新的投票权重
	newPower := new(big.Int).Add(currentPower, amount)

	// 注意：创世验证者的权重现在也包含用户投票
	// 初始权重（1000 ETH）在初始化时设置，后续用户投票会累加在上面
	if d.isGenesisValidator(delegate) {
		d.logger.Info("✅ 创世验证者权重更新（包含用户投票）",
			"delegate", delegate.String(),
			"oldPower", currentPower.String(),
			"addedAmount", amount.String(),
			"newPower", newPower.String())
	}

	// 创建或更新受托人信息，直接使用计算出的新权重
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

	// 直接保存到数据库
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

// RecalculateValidatorVotingPowerFromStakes 从历史投票记录重新计算验证者权重
// 用于修复创世验证者权重未包含用户投票的问题
// forceUpdate: 如果为 true，强制更新；如果为 false，只在权重不正确时更新
func (d *DPoS) RecalculateValidatorVotingPowerFromStakes(validatorAddr types.Address, forceUpdate bool) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	d.logger.Info("🔧 开始重新计算验证者权重",
		"validator", validatorAddr.String())

	// 1. 获取所有投票记录
	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	// 2. 累加所有投票给该验证者的记录（只统计已应用且活跃的）
	totalVotes := big.NewInt(0)
	for _, stake := range stakingInfos {
		if stake == nil {
			continue
		}
		// 只统计已应用且活跃的投票
		if stake.Delegate == validatorAddr && stake.Applied && stake.IsActive {
			if stake.Amount != nil && stake.Amount.Sign() > 0 {
				totalVotes.Add(totalVotes, stake.Amount)
				d.logger.Debug("累加投票记录",
					"staker", stake.Staker.String(),
					"amount", stake.Amount.String(),
					"totalSoFar", totalVotes.String())
			}
		}
	}

	// 3. 对于创世验证者，需要加上初始的 1000 ETH（注册保证金）
	// 注意：初始的 1000 ETH 不在 StakeInfo 中，需要单独加上
	if d.isGenesisValidator(validatorAddr) {
		genesisBasePower, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
		// 总权重 = 基础权重（1000 ETH） + 用户投票
		totalVotes.Add(totalVotes, genesisBasePower)
		d.logger.Info("🔧 创世验证者：基础权重 + 用户投票",
			"validator", validatorAddr.String(),
			"userVotes", new(big.Int).Sub(totalVotes, genesisBasePower).String(),
			"genesisBase", genesisBasePower.String(),
			"totalPower", totalVotes.String())
	}

	// 4. 检查是否需要更新
	currentPower, err := d.getVotingPowerFromDatabase(validatorAddr)
	if err != nil {
		d.logger.Warn("⚠️ 获取当前权重失败，强制更新",
			"validator", validatorAddr.String(),
			"error", err)
		currentPower = big.NewInt(0)
	}

	needsUpdate := forceUpdate || currentPower.Cmp(totalVotes) != 0

	if needsUpdate {
		err = d.updateVotingPowerInDatabase(validatorAddr, totalVotes)
		if err != nil {
			return fmt.Errorf("failed to update voting power: %w", err)
		}

		d.logger.Info("✅ 验证者权重已更新",
			"validator", validatorAddr.String(),
			"oldPower", currentPower.String(),
			"newPower", totalVotes.String())
	} else {
		d.logger.Debug("⏭️ 验证者权重已正确，跳过更新",
			"validator", validatorAddr.String(),
			"votingPower", currentPower.String())
	}

	return nil
}

// RecalculateAllValidatorsVotingPower 重新计算所有验证者的权重
// forceUpdate: 如果为 true，强制更新所有验证者；如果为 false，只更新权重不正确的验证者
func (d *DPoS) RecalculateAllValidatorsVotingPower(forceUpdate bool) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	d.logger.Info("🔧 开始重新计算所有验证者权重",
		"forceUpdate", forceUpdate)

	// 1. 获取所有验证者
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return fmt.Errorf("failed to get validators: %w", err)
	}

	// 2. 获取所有投票记录
	stakingInfos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return fmt.Errorf("failed to get staking info: %w", err)
	}

	// 3. 按验证者聚合投票
	validatorVotes := make(map[types.Address]*big.Int)
	for _, stake := range stakingInfos {
		if stake == nil {
			continue
		}
		// 只统计已应用且活跃的投票
		if stake.Applied && stake.IsActive && stake.Amount != nil && stake.Amount.Sign() > 0 {
			if validatorVotes[stake.Delegate] == nil {
				validatorVotes[stake.Delegate] = big.NewInt(0)
			}
			validatorVotes[stake.Delegate].Add(validatorVotes[stake.Delegate], stake.Amount)
			d.logger.Debug("🔍 统计投票记录",
				"staker", stake.Staker.String(),
				"delegate", stake.Delegate.String(),
				"amount", stake.Amount.String(),
				"applied", stake.Applied,
				"isActive", stake.IsActive)
		} else if stake.Delegate != (types.Address{}) && stake.Amount != nil && stake.Amount.Sign() > 0 {
			// 记录被跳过的投票（用于调试）
			d.logger.Debug("⏭️ 跳过投票记录（未应用或非活跃）",
				"staker", stake.Staker.String(),
				"delegate", stake.Delegate.String(),
				"amount", stake.Amount.String(),
				"applied", stake.Applied,
				"isActive", stake.IsActive)
		}
	}

	// 4. 更新每个验证者的权重
	genesisBasePower, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
	updatedCount := 0
	skippedCount := 0
	for _, validator := range validators {
		userVotes := validatorVotes[validator.Address]
		if userVotes == nil {
			userVotes = big.NewInt(0)
		}

		// 对于创世验证者，总权重 = 基础权重（1000 ETH） + 用户投票
		if d.isGenesisValidator(validator.Address) {
			userVotes.Add(userVotes, genesisBasePower)
		}

		// 检查是否需要更新
		currentPower := big.NewInt(0)
		if validator.VotingPower != nil {
			currentPower = validator.VotingPower
		}

		needsUpdate := forceUpdate || currentPower.Cmp(userVotes) != 0

		if needsUpdate {
			err = d.updateVotingPowerInDatabase(validator.Address, userVotes)
			if err != nil {
				d.logger.Warn("⚠️ 更新验证者权重失败",
					"validator", validator.Address.String(),
					"error", err)
				continue
			}
			updatedCount++
			d.logger.Info("✅ 验证者权重已更新",
				"validator", validator.Address.String(),
				"oldPower", currentPower.String(),
				"newPower", userVotes.String())
		} else {
			skippedCount++
			d.logger.Debug("⏭️ 验证者权重已正确，跳过更新",
				"validator", validator.Address.String(),
				"votingPower", currentPower.String())
		}
	}

	d.logger.Info("✅ 所有验证者权重重新计算完成",
		"totalValidators", len(validators),
		"updatedCount", updatedCount,
		"skippedCount", skippedCount)

	return nil
}
