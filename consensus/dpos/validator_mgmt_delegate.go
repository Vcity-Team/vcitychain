package dpos

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"go.etcd.io/bbolt"
)

// initializeDelegates 初始化受托人集合
func (d *DPoS) initializeDelegates() error {
	d.delegates = make(validator.AccountSet, 0, d.config.DelegateCount)

	// 🆕 初始化故障检测相关字段
	d.faultyValidators = make(map[types.Address]bool)
	d.missedBlocksCount = make(map[types.Address]uint64)
	d.currentEpoch = 0

	// 🆕 显著日志：显示当前配置
	d.logger.Debug("🚀 ===== DPoS验证者初始化开始 =====")
	d.logger.Debug("📋 当前配置信息",
		"configDelegateCount", d.config.DelegateCount,
		"initialDelegatesCount", len(d.config.InitialDelegates),
		"dposValidatorsCount", d.config.DPoSValidatorsCount,
		"backupValidatorsCount", d.config.BackupValidatorsCount,
		"maxMissedBlocks", d.config.MaxMissedBlocks)

	// 🆕 首先尝试从数据库读取受托人（真正用于出块）
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Info("🔍 开始从数据库读取验证者信息...")
		// 🆕 使用公共函数获取排序和限制后的验证者（包含故障过滤）
		dbValidators, err := d.GetSortedValidatorsWithLimit()
		if err != nil {
			d.logger.Warn("⚠️ 从数据库读取受托人失败，将使用创世文件", "error", err)
		} else if len(dbValidators) > 0 {
			d.logger.Info("✅ 从数据库成功读取验证者", "count", len(dbValidators))

			// 🆕 显著日志：显示数据库验证者详细信息
			d.logger.Info("📊 数据库验证者详细信息:")
			for i, validator := range dbValidators {
				d.logger.Info("👤 验证者信息",
					"index", i+1,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive)
			}

			// 🆕 按权重倒序排序
			d.logger.Info("🔄 开始按权重倒序排序验证者...")
			sort.Slice(dbValidators, func(i, j int) bool {
				// 1. 首先按票数降序排序
				votingPowerCmp := dbValidators[i].VotingPower.Cmp(dbValidators[j].VotingPower)
				if votingPowerCmp != 0 {
					return votingPowerCmp > 0
				}
				// 2. 票数相同，按地址升序排序（确保完全一致）
				return bytes.Compare(dbValidators[i].Address[:], dbValidators[j].Address[:]) < 0
			})

			// 🆕 显著日志：显示排序后的验证者
			d.logger.Info("📈 排序后的验证者列表:")
			for i, validator := range dbValidators {
				d.logger.Info("🏆 排序后验证者",
					"rank", i+1,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String())
			}

			// 🆕 显著日志：显示截取逻辑
			maxDelegates := int(d.config.DPoSValidatorsCount)
			if maxDelegates == 0 {
				maxDelegates = int(d.config.DelegateCount) // 回退到旧配置
			}
			originalCount := len(dbValidators)

			d.logger.Info("🎯 ===== 验证者截取逻辑 =====")
			d.logger.Info("📊 截取前统计",
				"配置的最大验证者数量", maxDelegates,
				"数据库中的验证者数量", originalCount)

			if originalCount <= maxDelegates {
				d.logger.Info("✅ 数据库验证者数量 <= 配置数量，取全部验证者",
					"取用数量", originalCount,
					"配置数量", maxDelegates)
			} else {
				d.logger.Info("✂️ 数据库验证者数量 > 配置数量，截取前N个",
					"截取前", maxDelegates,
					"原始数量", originalCount,
					"截取后", maxDelegates)
			}

			// 应用限制
			if maxDelegates > 0 && originalCount > maxDelegates {
				dbValidators = dbValidators[:maxDelegates]
			}

			// 设置到内存
			d.delegates = dbValidators
			d.logger.Info("✅ 验证者集合已从数据库加载并设置到内存",
				"最终数量", len(d.delegates))

			return nil
		}
	}

	// 如果数据库没有，尝试从创世块解析
	d.logger.Info("🔍 开始从创世块解析验证者...")
	if err := d.parseValidatorsFromGenesis(); err != nil {
		d.logger.Warn("⚠️ 从创世块解析验证者失败", "error", err)
		// 如果创世块也失败，使用配置中的初始验证者
		d.logger.Info("🔄 回退到配置中的初始验证者")
		for _, genesisValidator := range d.config.InitialDelegates {
			votingPower, ok := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
			if !ok {
				votingPower = big.NewInt(0)
			}
			delegate := &validator.ValidatorMetadata{
				Address:     types.Address(genesisValidator.Address),
				VotingPower: votingPower,
				IsActive:    true,
			}
			d.delegates = append(d.delegates, delegate)
		}
	}

	d.logger.Info("✅ 验证者初始化完成", "count", len(d.delegates))
	return nil
}

// addDelegateSafely 安全地添加受托人（检查重复）
func (d *DPoS) addDelegateSafely(newDelegate *validator.ValidatorMetadata) {
	// 检查是否已存在相同地址的验证者
	for i, existingDelegate := range d.delegates {
		if existingDelegate.Address == newDelegate.Address {
			// 累计权重 - 修复：使用正确的加法方式
			oldPower := new(big.Int).Set(existingDelegate.VotingPower)
			existingDelegate.VotingPower = new(big.Int).Add(existingDelegate.VotingPower, newDelegate.VotingPower)
			d.logger.Warn("🔄 发现重复验证者地址，累计权重",
				"address", newDelegate.Address.String(),
				"originalVotingPower", newDelegate.VotingPower.String(),
				"oldPower", oldPower.String(),
				"newPower", existingDelegate.VotingPower.String(),
				"existingIndex", i)
			return
		}
	}

	// 如果没有重复，直接添加
	d.delegates = append(d.delegates, newDelegate)
	d.logger.Debug("✅ 添加新验证者",
		"address", newDelegate.Address.String(),
		"votingPower", newDelegate.VotingPower.String(),
		"isActive", newDelegate.IsActive,
		"totalDelegates", len(d.delegates))
}

// getGenesisValidators 获取创世验证者集合
func (d *DPoS) getGenesisValidators() validator.AccountSet {
	d.logger.Debug("🔍 获取创世验证者集合", "genesisValidatorsCount", len(d.genesisValidators))

	// 从内存中的创世验证者映射创建AccountSet
	var validators validator.AccountSet
	for address := range d.genesisValidators {
		votingPower, ok := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
		if !ok {
			// 如果SetString失败，使用默认值（使用SetString确保不会溢出）
			defaultPower, _ := new(big.Int).SetString("1000000000000000000000", 10)
			if defaultPower == nil {
				defaultPower = big.NewInt(0)
			}
			votingPower = defaultPower
		}

		validatorMetadata := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: votingPower,
			IsActive:    true,
		}
		validators = append(validators, validatorMetadata)
	}

	d.logger.Debug("✅ 创世验证者集合创建完成", "count", len(validators))
	return validators
}

// getDelegatesFromState 从状态获取历史验证者集合
func (d *DPoS) getDelegatesFromState(blockNumber uint64) (validator.AccountSet, error) {
	d.logger.Debug("🔍 ===== 开始获取历史验证者集合 =====",
		"blockNumber", blockNumber,
		"stateIsNil", d.state == nil,
		"stakeStoreIsNil", d.state != nil && d.state.StakeStore == nil)

	// 🆕 新增：检查是否在共识切换高度之前，如果是则跳过历史验证者集合获取
	if d.config.ConsensusSwitchHeight > 0 && blockNumber < d.config.ConsensusSwitchHeight {
		d.logger.Debug("🔄 区块在共识切换高度之前，跳过历史验证者集合获取",
			"blockNumber", blockNumber,
			"consensusSwitchHeight", d.config.ConsensusSwitchHeight,
			"reason", "切换高度之前不需要BLS验证，因此不需要历史验证者集合")

		// 返回创世验证者集合作为默认值
		return d.getGenesisValidators(), nil
	}

	// 🆕 关键修复：首先尝试从历史数据获取验证者集合
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Debug("🔍 开始数据库事务", "blockNumber", blockNumber)

		// 开始数据库事务
		dbTx, err := d.state.beginDBTransaction(false) // 只读事务，因为只是读取数据
		if err != nil {
			d.logger.Error("❌ 无法开始数据库事务",
				"blockNumber", blockNumber,
				"error", err,
				"errorType", fmt.Sprintf("%T", err))
		} else {
			d.logger.Debug("✅ 数据库事务开始成功", "blockNumber", blockNumber)

			d.logger.Info("🔍 ========== 开始调用getDelegatesAtBlock ==========",
				"blockNumber", blockNumber,
				"stakeStoreType", fmt.Sprintf("%T", d.state.StakeStore),
				"timestamp", time.Now().Format("2006-01-02 15:04:05.000"))

			startTime := time.Now()
			d.logger.Info("🚀 开始执行getDelegatesAtBlock",
				"blockNumber", blockNumber,
				"startTime", startTime.Format("2006-01-02 15:04:05.000"))

			if historicalDelegates, err := d.state.StakeStore.getDelegatesAtBlock(blockNumber, dbTx); err == nil {
				endTime := time.Now()
				duration := endTime.Sub(startTime)
				d.logger.Info("✅ getDelegatesAtBlock执行成功",
					"blockNumber", blockNumber,
					"duration", duration.String(),
					"endTime", endTime.Format("2006-01-02 15:04:05.000"),
					"delegatesCount", len(historicalDelegates))

				if err := dbTx.Rollback(); err != nil {
					d.logger.Warn("⚠️ 数据库事务回滚失败", "error", err)
				}

				if len(historicalDelegates) > 0 {
					d.logger.Info("✅ 从数据库获取历史验证者集合成功",
						"blockNumber", blockNumber,
						"count", len(historicalDelegates))
					return historicalDelegates, nil
				}
			} else {
				d.logger.Error("❌ getDelegatesAtBlock执行失败",
					"blockNumber", blockNumber,
					"error", err,
					"errorType", fmt.Sprintf("%T", err))

				if err := dbTx.Rollback(); err != nil {
					d.logger.Warn("⚠️ 数据库事务回滚失败", "error", err)
				}
			}
		}
	}

	// 如果数据库没有，返回创世验证者集合
	d.logger.Debug("🔄 数据库中没有历史验证者集合，返回创世验证者",
		"blockNumber", blockNumber)
	return d.getGenesisValidators(), nil
}

// getDelegatesFromStateWithTx 从状态获取历史验证者集合（带事务）
func (d *DPoS) getDelegatesFromStateWithTx(blockNumber uint64, dbTx *bbolt.Tx) (validator.AccountSet, error) {
	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接调用内存版本，避免复杂的数据库操作
	return d.getDelegatesFromState(blockNumber)
}

// updateValidatorStatus 更新验证者状态
func (d *DPoS) updateValidatorStatus(address types.Address, newStake *big.Int) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	for _, delegate := range d.delegates {
		if delegate.Address == address {
			oldActive := delegate.IsActive
			oldStake := delegate.VotingPower
			delegate.VotingPower = new(big.Int).Set(newStake)

			// 关键：根据新stake更新活跃状态
			if newStake.Cmp(big.NewInt(0)) > 0 {
				delegate.IsActive = true // 有stake了，变为活跃
			} else {
				delegate.IsActive = false // stake为0，变为不活跃
			}

			// 记录状态变化
			if oldActive != delegate.IsActive {
				d.logger.Info("validator status changed",
					"address", address,
					"oldStake", oldStake.String(),
					"newStake", newStake.String(),
					"oldActive", oldActive,
					"newActive", delegate.IsActive)
			}

			return nil
		}
	}

	return fmt.Errorf("validator not found: %s", address)
}

// canParticipateInConsensus 检查验证者是否可以参与共识
func (d *DPoS) canParticipateInConsensus(delegate *validator.ValidatorMetadata) bool {
	return delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0
}

// ==================== 委托者注册相关函数 ====================

// isGenesisValidator 检查地址是否为创世验证者
func (d *DPoS) isGenesisValidator(address types.Address) bool {
	// 检查地址是否在创世验证者映射中
	return d.genesisValidators[address]
}

// isDelegateRegistrationTransaction 检查交易是否是受托人注册交易
func (d *DPoS) isDelegateRegistrationTransaction(tx *types.Transaction) bool {
	// 检查交易是否有输入数据（受托人注册交易应该有输入数据）
	if len(tx.Input) == 0 {
		return false
	}

	// 检查是否是DPoS受托人注册交易
	if len(tx.Input) < 8 || string(tx.Input[:4]) != "DPOS" || string(tx.Input[4:7]) != "REG" {
		return false
	}

	return true
}

// isVoteTransaction 检查交易是否是投票交易
func (d *DPoS) isVoteTransaction(tx *types.Transaction) bool {
	// 检查交易是否有输入数据（投票交易应该有输入数据）
	if len(tx.Input) == 0 {
		return false
	}

	// 检查交易输入数据长度（DPoS投票交易格式：4字节"DPOS" + 20字节投票者 + 20字节受托人 + 32字节金额）
	const (
		dposPrefixLen  = 4
		addrLen        = 20
		amountLen      = 32
		expectedLength = dposPrefixLen + addrLen + addrLen + amountLen
	)

	if len(tx.Input) < expectedLength {
		return false
	}

	// 检查是否是DPoS投票交易（前4字节应该是"DPOS"）
	if !bytes.Equal(tx.Input[:4], []byte("DPOS")) {
		return false
	}

	d.logger.Debug("🔍 发现DPoS投票交易",
		"inputLength", len(tx.Input),
		"prefix", string(tx.Input[:4]),
		"expectedLength", expectedLength)

	return true
}

// processDelegateRegistrationTransaction 处理受托人注册交易
func (d *DPoS) processDelegateRegistrationTransaction(tx *types.Transaction, blockNumber uint64) error {
	d.logger.Info("🔧 ===== 开始处理受托人注册交易 =====")

	// 🚨 检测受托人注册交易的全零哈希问题
	if tx == nil {
		d.logger.Error("🚨 CRITICAL: processDelegateRegistrationTransaction called with nil transaction", "blockNumber", blockNumber)
		return fmt.Errorf("nil transaction")
	}

	if tx.Hash == (types.Hash{}) {
		d.logger.Error("🚨 CRITICAL: processDelegateRegistrationTransaction called with zero hash transaction",
			"blockNumber", blockNumber,
			"txType", tx.Type,
			"nonce", tx.Nonce,
			"gasPrice", tx.GasPrice.String(),
			"value", tx.Value.String(),
			"from", tx.From.String(),
			"to", func() string {
				if tx.To != nil {
					return tx.To.String()
				}
				return "nil"
			}(),
			"inputLength", len(tx.Input))
		return fmt.Errorf("zero hash transaction")
	}

	d.logger.Info("📋 受托人注册交易信息",
		"txHash", tx.Hash.String(),
		"blockNumber", blockNumber,
		"value", tx.Value.String(),
		"inputLength", len(tx.Input))

	// 解析受托人注册交易数据
	d.logger.Info("🔍 开始解析受托人注册交易数据...")
	regInfo, err := d.parseDelegateRegistrationTransactionData(tx)
	if err != nil {
		d.logger.Error("❌ 解析受托人注册交易数据失败", "error", err)
		return fmt.Errorf("failed to parse delegate registration data: %w", err)
	}

	d.logger.Info("✅ 受托人注册数据解析成功",
		"registrant", regInfo.Registrant.String(),
		"name", regInfo.Name,
		"website", regInfo.Website,
		"description", regInfo.Description,
		"deposit", regInfo.Deposit.String())

	// 检查是否已经注册
	d.logger.Info("🔍 检查受托人是否已注册...")
	if d.IsDelegateRegistered(regInfo.Registrant) {
		d.logger.Warn("⚠️ 受托人已注册，跳过处理",
			"registrant", regInfo.Registrant.String())
		return nil
	}
	d.logger.Info("✅ 受托人未注册，可以继续处理")

	// 创建受托人候选人
	d.logger.Info("👤 开始创建受托人候选人...")
	registration := &DelegateRegistration{
		Address:      regInfo.Registrant,
		Name:         regInfo.Name,
		Website:      regInfo.Website,
		Description:  regInfo.Description,
		Deposit:      regInfo.Deposit,
		Status:       RegStatusCandidate, // 候选人状态
		CreatedAt:    uint64(time.Now().Unix()),
		TotalVotes:   big.NewInt(0),
		IsActive:     false,
		LastVoteTime: 0,
	}
	d.logger.Info("✅ 受托人候选人对象创建完成")

	// 保存到数据库
	d.logger.Info("💾 开始保存受托人注册信息到数据库...")
	if d.state != nil && d.state.RegistrationStore != nil {
		if err := d.state.RegistrationStore.SaveRegistration(registration); err != nil {
			d.logger.Error("❌ 保存受托人注册信息失败", "error", err)
			return fmt.Errorf("failed to save registration: %w", err)
		}
		d.logger.Info("✅ 受托人注册信息已保存到数据库")
	} else {
		d.logger.Warn("⚠️ 数据库不可用，跳过保存")
	}

	// 创建受托人记录（可以立即接受投票）
	d.logger.Info("🔧 开始创建受托人验证者记录...")
	delegate := &validator.ValidatorMetadata{
		Address:     regInfo.Registrant,
		VotingPower: big.NewInt(0),
		BlsKey:      nil,
		IsActive:    false, // 初始为非活跃，需要投票激活
	}

	d.addDelegateSafely(delegate)
	d.logger.Info("✅ 受托人验证者记录创建完成")

	d.logger.Info("🎉 ===== 受托人注册处理完成 =====")
	d.logger.Info("📊 最终结果",
		"address", regInfo.Registrant.String(),
		"name", regInfo.Name,
		"website", regInfo.Website,
		"deposit", regInfo.Deposit.String(),
		"status", "candidate",
		"txHash", tx.Hash.String())
	d.logger.Info("🎯 受托人现在可以接受投票了！")

	return nil
}

// parseDelegateRegistrationTransactionData 解析DPoS受托人注册交易数据
func (d *DPoS) parseDelegateRegistrationTransactionData(tx *types.Transaction) (*DelegateRegistrationInfo, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is nil")
	}

	input := tx.Input
	if input == nil || len(input) < 4 {
		return nil, fmt.Errorf("input data too short or nil: length=%d", len(input))
	}

	// 检查是否是DPoS受托人注册交易
	if len(input) < 8 || string(input[:4]) != "DPOS" || string(input[4:7]) != "REG" {
		return nil, fmt.Errorf("not a DPoS delegate registration transaction")
	}

	// 跳过标识符 (8 bytes: "DPOS" + "REG" + 0x00)
	offset := 8

	// 解析注册者地址 (20 bytes)
	if offset+20 > len(input) {
		return nil, fmt.Errorf("input data too short for registrant address")
	}
	registrant := types.BytesToAddress(input[offset : offset+20])
	offset += 20

	// 解析名称长度 (4 bytes)
	if offset+4 > len(input) {
		return nil, fmt.Errorf("input data too short for name length")
	}
	nameLen := uint32(input[offset])<<24 | uint32(input[offset+1])<<16 | uint32(input[offset+2])<<8 | uint32(input[offset+3])
	offset += 4

	// 解析名称
	if offset+int(nameLen) > len(input) {
		return nil, fmt.Errorf("input data too short for name")
	}
	name := string(input[offset : offset+int(nameLen)])
	offset += int(nameLen)

	// 解析网站长度 (4 bytes)
	if offset+4 > len(input) {
		return nil, fmt.Errorf("input data too short for website length")
	}
	websiteLen := uint32(input[offset])<<24 | uint32(input[offset+1])<<16 | uint32(input[offset+2])<<8 | uint32(input[offset+3])
	offset += 4

	// 解析网站
	if offset+int(websiteLen) > len(input) {
		return nil, fmt.Errorf("input data too short for website")
	}
	website := string(input[offset : offset+int(websiteLen)])
	offset += int(websiteLen)

	// 解析描述长度 (4 bytes)
	if offset+4 > len(input) {
		return nil, fmt.Errorf("input data too short for description length")
	}
	descLen := uint32(input[offset])<<24 | uint32(input[offset+1])<<16 | uint32(input[offset+2])<<8 | uint32(input[offset+3])
	offset += 4

	// 解析描述
	if offset+int(descLen) > len(input) {
		return nil, fmt.Errorf("input data too short for description")
	}
	description := string(input[offset : offset+int(descLen)])

	// 保证金从交易的value字段获取
	deposit := tx.Value

	return &DelegateRegistrationInfo{
		Registrant:  registrant,
		Name:        name,
		Website:     website,
		Description: description,
		Deposit:     deposit,
	}, nil
}

// calculateTotalVotedAmount 计算投票者的总已投票金额
func (d *DPoS) calculateTotalVotedAmount(voter types.Address) *big.Int {
	d.logger.Debug("🔍 计算投票者总已投票金额", "voter", voter.String())

	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("StakeStore 不可用", "voter", voter.String())
		return big.NewInt(0)
	}

	// 从 VoterInfo 表直接获取投票者的总投票权重
	voterInfo, err := d.state.StakeStore.getVoterInfo(voter, nil)
	if err != nil {
		d.logger.Warn("从数据库获取投票者信息失败",
			"voter", voter.String(),
			"error", err)
		return big.NewInt(0)
	}

	if voterInfo != nil && voterInfo.VotingPower != nil {
		d.logger.Info("投票者总已投票金额获取成功",
			"voter", voter.String(),
			"totalVotedAmount", voterInfo.VotingPower.String())
		return new(big.Int).Set(voterInfo.VotingPower)
	}

	d.logger.Info("投票者无投票记录", "voter", voter.String())
	return big.NewInt(0)
}

// IsDelegateRegistered 检查受托人是否已注册
func (d *DPoS) IsDelegateRegistered(address types.Address) bool {
	d.logger.Info("🔍 检查受托人注册状态", "address", address.String())

	if d.state == nil || d.state.RegistrationStore == nil {
		d.logger.Warn("❌ 注册存储不可用", "address", address.String())
		return false
	}

	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		d.logger.Warn("❌ 查询受托人注册信息失败",
			"address", address.String(),
			"error", err.Error())
		return false
	}

	isRegistered := reg != nil
	if isRegistered {
		d.logger.Info("✅ 受托人已注册",
			"address", address.String(),
			"name", reg.Name,
			"status", reg.Status.String())
	} else {
		d.logger.Warn("❌ 受托人未注册", "address", address.String())
	}

	return isRegistered
}

// IsDelegateCandidate 检查受托人是否为候选人状态（可以接受投票）
func (d *DPoS) IsDelegateCandidate(address types.Address) bool {
	d.logger.Info("🔍 检查受托人候选人状态", "address", address.String())

	if d.state == nil || d.state.RegistrationStore == nil {
		d.logger.Warn("❌ 注册存储不可用", "address", address.String())
		return false
	}

	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		d.logger.Warn("❌ 查询受托人注册信息失败",
			"address", address.String(),
			"error", err.Error())
		return false
	}

	if reg == nil {
		d.logger.Warn("❌ 受托人未注册", "address", address.String())
		return false
	}

	isCandidate := reg.Status == RegStatusCandidate || reg.Status == RegStatusActive
	if isCandidate {
		d.logger.Info("✅ 受托人是候选人状态，可以接受投票",
			"address", address.String(),
			"name", reg.Name,
			"status", reg.Status.String(),
			"isActive", reg.IsActive)
	} else {
		d.logger.Warn("❌ 受托人不是候选人状态，不能接受投票",
			"address", address.String(),
			"name", reg.Name,
			"status", reg.Status.String(),
			"isActive", reg.IsActive)
	}

	return isCandidate
}

// createDelegateRegistrationTransactionData 创建受托人注册交易数据
func (d *DPoS) createDelegateRegistrationTransactionData(registrant types.Address, name, website, description string) []byte {
	// 创建DPoS受托人注册交易的标识数据
	data := make([]byte, 0, 128) // 预分配足够空间

	// 添加DPoS受托人注册标识符 (4 bytes)
	data = append(data, []byte("DPOS")...)

	// 添加操作类型标识 (4 bytes) - "REG" + 0x00
	data = append(data, []byte("REG")...)
	data = append(data, 0x00)

	// 添加注册者地址 (20 bytes)
	data = append(data, registrant.Bytes()...)

	// 添加名称长度和名称 (4 bytes + name)
	nameBytes := []byte(name)
	nameLen := uint32(len(nameBytes))
	data = append(data, []byte{
		byte(nameLen >> 24),
		byte(nameLen >> 16),
		byte(nameLen >> 8),
		byte(nameLen),
	}...)
	data = append(data, nameBytes...)

	// 添加网站长度和网站 (4 bytes + website)
	websiteBytes := []byte(website)
	websiteLen := uint32(len(websiteBytes))
	data = append(data, []byte{
		byte(websiteLen >> 24),
		byte(websiteLen >> 16),
		byte(websiteLen >> 8),
		byte(websiteLen),
	}...)
	data = append(data, websiteBytes...)

	// 添加描述长度和描述 (4 bytes + description)
	descBytes := []byte(description)
	descLen := uint32(len(descBytes))
	data = append(data, []byte{
		byte(descLen >> 24),
		byte(descLen >> 16),
		byte(descLen >> 8),
		byte(descLen),
	}...)
	data = append(data, descBytes...)

	return data
}

// RegisterDelegate 注册受托人（改进的TRON风格）
func (d *DPoS) RegisterDelegate(registrant types.Address, name, website, description string) error {
	d.logger.Info("🚀 ===== 开始受托人注册（无私钥） =====")
	d.logger.Info("📝 受托人信息",
		"registrant", registrant.String(),
		"name", name,
		"website", website,
		"description", description)

	d.logger.Error("❌ 无私钥提供，无法创建交易")
	d.logger.Error("💡 请使用带私钥的命令：./main dpos delegate register --address <address> --name <name> --website <website> --description <description> --private-key <private_key>")

	return fmt.Errorf("private key is required for delegate registration. Please provide --private-key parameter")
}

// RegisterDelegateWithKey 注册受托人（带私钥，用于创建交易）
func (d *DPoS) RegisterDelegateWithKey(registrant types.Address, name, website, description, privateKey string) error {
	// 使用默认chainID调用新方法
	return d.RegisterDelegateWithKeyAndChainID(registrant, name, website, description, privateKey, 20230826)
}

// RegisterDelegateWithKeyAndChainID 注册受托人（带私钥和chainID，用于创建交易）
func (d *DPoS) RegisterDelegateWithKeyAndChainID(registrant types.Address, name, website, description, privateKey string, chainID uint64) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.logger.Info("🚀 ===== 开始受托人注册（带chainID） =====")
	d.logger.Info("📝 受托人信息",
		"registrant", registrant.String(),
		"name", name,
		"website", website,
		"description", description,
		"chainID", chainID)

	// 检查是否已经注册
	if d.IsDelegateRegistered(registrant) {
		return fmt.Errorf("delegate %s already registered", registrant.String())
	}

	// 获取保证金金额（可配置）
	depositAmount := d.getDelegateDepositAmount()

	// 检查余额
	if d.balanceQuerier != nil {
		balance, err := d.balanceQuerier.GetNativeTokenBalance(registrant)
		if err != nil {
			return fmt.Errorf("failed to query balance: %w", err)
		}
		if balance.Cmp(depositAmount) < 0 {
			return fmt.Errorf("insufficient balance for delegate registration: required %s, available %s",
				depositAmount.String(), balance.String())
		}
	}

	// 根据是否有私钥决定是创建交易还是直接更新状态
	if privateKey != "" {
		// 🆕 有私钥，创建交易
		return d.createDelegateRegistrationTransactionWithChainID(registrant, name, website, description, depositAmount, privateKey, chainID)
	} else {
		// 🆕 没有私钥，无法创建交易
		d.logger.Error("❌ 无私钥提供，无法创建受托人注册交易")
		return fmt.Errorf("private key is required for delegate registration")
	}
}

// createDelegateRegistrationTransaction 创建受托人注册交易（使用默认chainID）
func (d *DPoS) createDelegateRegistrationTransaction(registrant types.Address, name, website, description string, depositAmount *big.Int, privateKey string) error {
	return d.createDelegateRegistrationTransactionWithChainID(registrant, name, website, description, depositAmount, privateKey, 20230826)
}

// createDelegateRegistrationTransactionWithChainID 创建受托人注册交易（带chainID）
func (d *DPoS) createDelegateRegistrationTransactionWithChainID(registrant types.Address, name, website, description string, depositAmount *big.Int, privateKey string, chainID uint64) error {
	d.logger.Info("🚀 ===== 开始创建受托人注册交易（带chainID） =====")
	d.logger.Info("📝 受托人注册信息",
		"registrant", registrant.String(),
		"name", name,
		"website", website,
		"description", description,
		"deposit", depositAmount.String(),
		"chainID", chainID)

	// 获取账户nonce
	var nonce uint64
	// 直接使用传入的registrant作为发送者地址
	senderAddress := registrant

	d.logger.Info("🔍 使用传入的registrant作为发送者地址", "senderAddress", senderAddress.String())

	// 尝试从区块链获取nonce
	// 通过JSON-RPC调用获取nonce
	nonce, err := d.getAccountNonce(senderAddress)
	if err != nil {
		d.logger.Warn("⚠️ 无法获取账户nonce，使用默认nonce 0", "error", err)
		nonce = 0
	} else {
		d.logger.Info("✅ 成功获取账户nonce", "nonce", nonce, "senderAddress", senderAddress.String())
	}

	d.logger.Info("🔢 交易参数设置", "nonce", nonce, "senderAddress", senderAddress.String(), "note", "使用获取到的nonce")

	// 获取gas价格
	gasPrice := big.NewInt(1000000000) // 1 Gwei
	d.logger.Info("⛽ Gas设置", "gasPrice", gasPrice.String(), "gasLimit", 100000)

	// 创建受托人注册交易
	d.logger.Info("🔨 开始构建交易对象...")
	tx := &types.Transaction{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      100000,        // 固定gas限制
		To:       nil,           // 合约调用，To为nil
		Value:    depositAmount, // 保证金作为value
		Input:    d.createDelegateRegistrationTransactionData(registrant, name, website, description),
		// Don't set From field - let transaction pool recover it from signature
		// This ensures consistency between From field and signature
		V:    big.NewInt(0), // 将在签名后设置
		R:    big.NewInt(0), // 将在签名后设置
		S:    big.NewInt(0), // 将在签名后设置
		Hash: types.Hash{},
	}

	// 设置交易类型
	tx.Type = types.LegacyTx
	d.logger.Info("📋 交易对象创建完成", "type", "LegacyTx", "value", depositAmount.String(), "inputLength", len(tx.Input))

	// 计算交易哈希
	d.logger.Info("🔍 计算交易哈希...")
	tx.ComputeHash(0)

	// 检查交易哈希
	if tx.Hash == (types.Hash{}) {
		d.logger.Error("🚨 CRITICAL: delegate registration transaction has zero hash after creation",
			"nonce", nonce,
			"gasPrice", gasPrice.String(),
			"registrant", registrant.String(),
			"name", name)
		return fmt.Errorf("transaction hash is zero after creation")
	}

	d.logger.Info("✅ 交易哈希计算成功", "txHash", tx.Hash.String())
	d.logger.Info("📊 交易详情",
		"txHash", tx.Hash.String(),
		"nonce", nonce,
		"gasPrice", gasPrice.String(),
		"deposit", depositAmount.String(),
		"from", registrant.String())

	// 签名交易
	d.logger.Info("✍️ 开始签名交易...")
	if err := d.signTransactionWithChainID(tx, registrant, privateKey, chainID); err != nil {
		d.logger.Error("❌ 交易签名失败", "error", err)
		return fmt.Errorf("failed to sign transaction: %w", err)
	}
	d.logger.Info("✅ 交易签名成功")

	// 重新计算哈希
	d.logger.Info("🔄 重新计算交易哈希...")
	tx.ComputeHash(0)
	d.logger.Info("✅ 交易哈希重新计算完成", "txHash", tx.Hash.String())

	// 尝试添加交易到交易池
	d.logger.Info("🏊 ===== 开始添加交易到交易池 =====")

	var txAdded bool
	if d.txPool != nil {
		d.logger.Info("🔍 检查交易池支持...")
		// 尝试将 txPoolInterface 转换为 *TxPool 来访问 AddTx 方法
		if realTxPool, ok := d.txPool.(interface {
			AddTx(tx *types.Transaction) error
		}); ok {
			d.logger.Info("✅ 交易池支持AddTx方法，开始添加交易...")
			if err := realTxPool.AddTx(tx); err != nil {
				d.logger.Error("❌ 添加交易到交易池失败", "error", err)
				// 继续执行，作为fallback直接更新状态
			} else {
				d.logger.Info("🎉 受托人注册交易已成功添加到交易池！",
					"txHash", tx.Hash.String(),
					"registrant", registrant.String())
				txAdded = true
			}
		} else {
			d.logger.Warn("⚠️ 交易池不支持AddTx方法")
		}
	} else {
		d.logger.Warn("⚠️ 交易池为空")
	}

	// 如果交易池添加失败，返回错误
	if !txAdded {
		d.logger.Error("❌ 交易池添加失败，无法完成受托人注册")
		return fmt.Errorf("failed to add delegate registration transaction to pool")
	}

	d.logger.Info("🎊 ===== 受托人注册交易提交成功 =====")
	d.logger.Info("📋 最终状态",
		"address", registrant.String(),
		"name", name,
		"website", website,
		"txHash", tx.Hash.String(),
		"status", "pending",
		"txAdded", txAdded)
	d.logger.Info("🌐 交易将被广播到网络并等待打包进区块")

	return nil
}

// signTransaction 签名交易（使用默认chainID）
func (d *DPoS) signTransaction(tx *types.Transaction, expectedAddr types.Address, privateKeyHex string) error {
	return d.signTransactionWithChainID(tx, expectedAddr, privateKeyHex, 20230826)
}

// signTransactionWithChainID 签名交易（带chainID）
func (d *DPoS) signTransactionWithChainID(tx *types.Transaction, expectedAddr types.Address, privateKeyHex string, chainID uint64) error {
	d.logger.Info("Signing DPoS transaction with user-provided private key")

	// Force user to provide private key
	if privateKeyHex == "" {
		return fmt.Errorf("private key is required for signing DPoS transactions")
	}

	d.logger.Info("Decoding user-provided private key", "privateKeyHex", privateKeyHex, "length", len(privateKeyHex))

	// Validate hex string first
	if len(privateKeyHex) != 64 {
		return fmt.Errorf("invalid private key length: expected 64, got %d", len(privateKeyHex))
	}

	// Check if string contains only valid hex characters
	for i, char := range privateKeyHex {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return fmt.Errorf("invalid hex character at position %d: %c (U+%04X)", i, char, char)
		}
	}

	d.logger.Info("Private key hex string validation passed")

	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		d.logger.Error("Failed to decode user-provided private key", "error", err)
		return fmt.Errorf("failed to decode user-provided private key: %w", err)
	}

	d.logger.Info("User-provided private key decoded", "length", len(privateKeyBytes))

	// Use direct ECDSA private key creation instead of crypto.BytesToECDSAPrivateKey
	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	// Create ECDSA private key directly using secp256k1 curve
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}

	// Calculate the public key from the private key
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	d.logger.Info("User-provided private key created successfully", "privateKeyD", privateKey.D.String())

	// Calculate transaction hash for signing using EIP-155 scheme to match txpool signer
	// Use provided chainID
	eip155Signer := crypto.NewEIP155Signer(chainID, false)

	d.logger.Info("=== 标记2: 开始签名交易 ===")
	// For EIP-155 signing, we need to use the signer's SignTx method
	// This ensures the hash calculation and V value are correct
	signedTx, err := eip155Signer.SignTx(tx, privateKey)
	if err != nil {
		d.logger.Error("Failed to sign transaction with EIP-155 signer", "error", err)
		return fmt.Errorf("failed to sign transaction with EIP-155 signer: %w", err)
	}

	// Copy the signature components from the signed transaction
	tx.R = signedTx.R
	tx.S = signedTx.S
	tx.V = signedTx.V

	d.logger.Info("=== 标记3: 签名完成，R=", tx.R.String(), "S=", tx.S.String(), "V=", tx.V.String(), "===")
	d.logger.Info("Transaction signed successfully with EIP-155 signer", "r", tx.R.String(), "s", tx.S.String(), "v", tx.V.String())

	// Recover sender with the same signer and set tx.From for logging / consistency
	senderAddr, err := eip155Signer.Sender(tx)
	if err == nil {
		tx.From = senderAddr
		d.logger.Info("=== 标记4: 恢复发送者地址=", tx.From.String(), "===")
		d.logger.Info("Sender recovered and set on tx", "from", tx.From.String())
	} else {
		d.logger.Warn("Failed to recover sender after signing", "error", err)
	}

	return nil
}

// ApproveDelegate 批准受托人注册
func (d *DPoS) ApproveDelegate(address types.Address) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 更新注册状态
	if d.state != nil && d.state.RegistrationStore != nil {
		if err := d.state.RegistrationStore.UpdateRegistrationStatus(address, RegStatusActive); err != nil {
			return fmt.Errorf("failed to update registration status: %w", err)
		}
	}

	// 创建受托人记录
	delegate := &validator.ValidatorMetadata{
		Address:     address,
		VotingPower: big.NewInt(0),
		BlsKey:      nil,
		IsActive:    false,
	}

	d.addDelegateSafely(delegate)

	d.logger.Info("Delegate registration approved",
		"address", address.String())

	return nil
}

// RejectDelegate 拒绝受托人注册
func (d *DPoS) RejectDelegate(address types.Address, reason string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 更新注册状态
	if d.state != nil && d.state.RegistrationStore != nil {
		if err := d.state.RegistrationStore.UpdateRegistrationStatus(address, RegStatusWithdrawn); err != nil {
			return fmt.Errorf("failed to update registration status: %w", err)
		}
	}

	d.logger.Info("Delegate registration rejected",
		"address", address.String(),
		"reason", reason)

	return nil
}

// GetDelegateRegistrations 获取所有受托人注册信息
func (d *DPoS) GetDelegateRegistrations() ([]*DelegateRegistration, error) {
	if d.state == nil || d.state.RegistrationStore == nil {
		return nil, fmt.Errorf("registration store not available")
	}

	// 1. 从数据库获取已注册的受托人信息
	dbRegistrations, err := d.state.RegistrationStore.GetAllRegistrations()
	if err != nil {
		return nil, fmt.Errorf("failed to get registrations from database: %w", err)
	}

	// 2. 创建已注册地址的映射，用于去重
	registeredAddresses := make(map[types.Address]bool)
	for _, reg := range dbRegistrations {
		registeredAddresses[reg.Address] = true
	}

	// 3. 获取创世验证者并转换为 DelegateRegistration
	genesisRegistrations := d.getGenesisValidatorsAsRegistrations()

	// 4. 合并：只添加未在数据库中注册的创世验证者
	result := make([]*DelegateRegistration, 0, len(dbRegistrations)+len(genesisRegistrations))
	result = append(result, dbRegistrations...)

	for _, genesisReg := range genesisRegistrations {
		if !registeredAddresses[genesisReg.Address] {
			result = append(result, genesisReg)
		}
	}

	return result, nil
}

// getGenesisValidatorsAsRegistrations 将创世验证者转换为 DelegateRegistration 格式
func (d *DPoS) getGenesisValidatorsAsRegistrations() []*DelegateRegistration {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 获取创世验证者（优先从内存中的delegates获取，如果为空则从genesisValidators映射获取）
	var genesisValidators validator.AccountSet

	// 方法1: 从 d.delegates 中筛选创世验证者
	if len(d.delegates) > 0 {
		for _, delegate := range d.delegates {
			if d.isGenesisValidator(delegate.Address) {
				genesisValidators = append(genesisValidators, delegate)
			}
		}
	}

	// 方法2: 如果方法1没有找到，从 d.runtime.delegates 中筛选
	if len(genesisValidators) == 0 && d.runtime != nil && len(d.runtime.delegates) > 0 {
		for _, delegate := range d.runtime.delegates {
			if d.isGenesisValidator(delegate.Address) {
				genesisValidators = append(genesisValidators, delegate)
			}
		}
	}

	// 方法3: 如果前两种方法都没有找到，直接从 genesisValidators 映射创建
	if len(genesisValidators) == 0 && len(d.genesisValidators) > 0 {
		genesisValidators = d.getGenesisValidators()
	}

	// 方法4: 如果仍然为空，尝试从数据库中读取验证者信息
	if len(genesisValidators) == 0 && d.state != nil && d.state.StakeStore != nil {
		if dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false); err == nil && len(dbValidators) > 0 {
			genesisValidators = dbValidators
		}
	}

	// 方法5: 仍未获取到时，回退到配置中的初始验证者
	if len(genesisValidators) == 0 && d.config != nil && len(d.config.InitialDelegates) > 0 {
		for _, genesisValidator := range d.config.InitialDelegates {
			votingPower, ok := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
			if !ok {
				votingPower = big.NewInt(0)
			}

			genesisValidators = append(genesisValidators, &validator.ValidatorMetadata{
				Address:     genesisValidator.Address,
				VotingPower: votingPower,
				IsActive:    true,
			})
		}
	}

	// 转换为 DelegateRegistration
	result := make([]*DelegateRegistration, 0, len(genesisValidators))
	fixedVotingPower, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
	zeroDeposit := big.NewInt(0)

	for _, validator := range genesisValidators {
		reg := &DelegateRegistration{
			Address:      validator.Address,
			Name:         fmt.Sprintf("Genesis Validator %s", validator.Address.String()[:10]),
			Website:      "",
			Description:  "Genesis validator with default weight 1000 VCITY",
			Deposit:      new(big.Int).Set(zeroDeposit), // 创世验证者没有保证金
			Status:       RegStatusActive,               // 创世验证者默认为活跃状态
			CreatedAt:    0,                             // 创世时间
			TotalVotes:   new(big.Int).Set(validator.VotingPower),
			IsActive:     validator.IsActive,
			LastVoteTime: 0,
		}

		// 如果验证者的投票权重不是1000 VCITY，使用实际权重
		if validator.VotingPower != nil && validator.VotingPower.Cmp(fixedVotingPower) != 0 {
			reg.TotalVotes = new(big.Int).Set(validator.VotingPower)
		}

		result = append(result, reg)
	}

	return result
}

// getDelegateDepositAmount 获取受托人保证金金额
func (d *DPoS) getDelegateDepositAmount() *big.Int {
	// 优先从配置文件读取
	if d.config != nil && d.config.SRThreshold != nil {
		// 如果配置为0，表示不需要保证金
		if d.config.SRThreshold.Cmp(big.NewInt(0)) == 0 {
			return big.NewInt(0)
		}
		return new(big.Int).Set(d.config.SRThreshold)
	}

	// 默认保证金：100 VCITY（比TRON的1000 TRX低）
	depositAmount := new(big.Int)
	depositAmount.SetString("100000000000000000000", 10) // 100 VCITY

	// 可以从治理参数中读取（动态修改）
	if d.parameterCurrentValues != nil {
		d.parameterValuesMutex.RLock()
		if value, exists := d.parameterCurrentValues["delegate_deposit_amount"]; exists {
			if amount, ok := value.(string); ok {
				if bigAmount, ok := new(big.Int).SetString(amount, 10); ok {
					depositAmount = bigAmount
				}
			}
		}
		d.parameterValuesMutex.RUnlock()
	}

	return depositAmount
}

// getMaxActiveDelegates 获取最大活跃受托人数量
func (d *DPoS) getMaxActiveDelegates() int {
	// 默认21个活跃受托人
	maxActive := 21

	// 可以从配置参数中读取
	if d.parameterCurrentValues != nil {
		d.parameterValuesMutex.RLock()
		if value, exists := d.parameterCurrentValues["max_active_delegates"]; exists {
			if count, ok := value.(uint64); ok {
				maxActive = int(count)
			}
		}
		d.parameterValuesMutex.RUnlock()
	}

	return maxActive
}

// updateActiveDelegates 更新活跃受托人（基于投票排名）
func (d *DPoS) updateActiveDelegates() {
	// 获取所有注册的受托人
	registrations, err := d.GetDelegateRegistrations()
	if err != nil {
		d.logger.Error("Failed to get delegate registrations", "error", err)
		return
	}

	// 按投票数排序
	sort.Slice(registrations, func(i, j int) bool {
		return registrations[i].TotalVotes.Cmp(registrations[j].TotalVotes) > 0
	})

	// 获取最大活跃受托人数量
	maxActive := d.getMaxActiveDelegates()

	// 更新活跃状态
	for i, reg := range registrations {
		wasActive := reg.IsActive
		reg.IsActive = i < maxActive

		// 更新状态
		if reg.IsActive {
			reg.Status = RegStatusActive
		} else {
			reg.Status = RegStatusCandidate
		}

		// 保存更新
		if d.state != nil && d.state.RegistrationStore != nil {
			if err := d.state.RegistrationStore.SaveRegistration(reg); err != nil {
				d.logger.Error("Failed to save registration update", "error", err)
			}
		}

		// 更新受托人记录
		for _, delegate := range d.delegates {
			if delegate.Address == reg.Address {
				delegate.IsActive = reg.IsActive
				break
			}
		}

		// 记录状态变化
		if wasActive != reg.IsActive {
			status := "inactive"
			if reg.IsActive {
				status = "active"
			}
			d.logger.Info("Delegate status changed",
				"address", reg.Address.String(),
				"name", reg.Name,
				"status", status,
				"votes", reg.TotalVotes.String(),
				"rank", i+1)
		}
	}

	d.logger.Info("Active delegates updated",
		"totalCandidates", len(registrations),
		"activeDelegates", maxActive)
}

// WithdrawDelegate 退出受托人（退还保证金）
func (d *DPoS) WithdrawDelegate(address types.Address) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 获取注册信息
	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		return fmt.Errorf("failed to get registration: %w", err)
	}

	if reg == nil {
		return fmt.Errorf("delegate not found")
	}

	// 检查是否还有投票
	if reg.TotalVotes.Cmp(big.NewInt(0)) > 0 {
		return fmt.Errorf("cannot withdraw while having votes")
	}

	// 更新状态
	reg.Status = RegStatusWithdrawn
	reg.IsActive = false

	// 保存更新
	if err := d.state.RegistrationStore.SaveRegistration(reg); err != nil {
		return fmt.Errorf("failed to save registration: %w", err)
	}

	// 从受托人列表中移除
	for i, delegate := range d.delegates {
		if delegate.Address == address {
			d.delegates = append(d.delegates[:i], d.delegates[i+1:]...)
			break
		}
	}

	d.logger.Info("Delegate withdrawn successfully",
		"address", address.String(),
		"name", reg.Name,
		"deposit", reg.Deposit.String())

	return nil
}

// compareDelegateSets 比较受托人集合变化
func (d *DPoS) compareDelegateSets(oldSet, newSet validator.AccountSet) (added, removed []types.Address) {
	oldMap := make(map[types.Address]bool)
	newMap := make(map[types.Address]bool)

	for _, del := range oldSet {
		oldMap[del.Address] = true
	}
	for _, del := range newSet {
		newMap[del.Address] = true
	}

	for _, del := range newSet {
		if !oldMap[del.Address] {
			added = append(added, del.Address)
		}
	}

	for _, del := range oldSet {
		if !newMap[del.Address] {
			removed = append(removed, del.Address)
		}
	}

	return added, removed
}

// updateDelegates 增强的受托人更新（公共接口，需要获取锁）
func (d *DPoS) updateDelegates(block *types.FullBlock) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	return d.updateDelegatesInternal(block)
}

// updateDelegatesInternal 内部方法，不需要获取锁（由调用者负责锁管理）
func (d *DPoS) updateDelegatesInternal(block *types.FullBlock) error {
	d.logger.Info("🔄 updateDelegatesInternal called", "block", block, "currentDelegatesCount", len(d.delegates))

	// 🆕 方案1+方案2：检查是否需要重新排序验证者集合
	if d.pendingValidatorUpdate {
		d.logger.Info("🔄 轮次边界：重新排序验证者集合")

		// 按标准化规则排序，确保所有节点完全一致
		sort.Slice(d.delegates, func(i, j int) bool {
			// 1. 首先按票数降序排序
			votingPowerCmp := d.delegates[i].VotingPower.Cmp(d.delegates[j].VotingPower)
			if votingPowerCmp != 0 {
				return votingPowerCmp > 0
			}
			// 2. 票数相同，按地址升序排序（确保完全一致）
			return bytes.Compare(d.delegates[i].Address[:], d.delegates[j].Address[:]) < 0
		})

		d.logger.Info("✅ 轮次边界：验证者集合重新排序完成")
		for i, delegate := range d.delegates {
			d.logger.Info("🔍 重新排序后的验证者",
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}

		// 🆕 已删除 currentDelegateIndex 的重新计算
		// 现在完全通过 getCurrentDelegate() 基于时间slot实时计算
		// 只同步验证者集合
		if d.runtime != nil {
			if d.runtime.lock.TryLock() {
				// 🆕 关键修复：同步重新排序后的验证者集合到runtime
				d.logger.Info("🔄 同步重新排序后的验证者集合到runtime")
				d.runtime.delegates = make(validator.AccountSet, len(d.delegates))
				copy(d.runtime.delegates, d.delegates)
				d.logger.Info("✅ 验证者集合同步完成",
					"runtimeDelegatesCount", len(d.runtime.delegates),
					"sourceDelegatesCount", len(d.delegates))
				d.runtime.lock.Unlock()
			} else {
				d.logger.Warn("⚠️ 无法获取 runtime.lock，跳过同步验证者集合")
			}
		}
	}

	// 🆕 落盘时：直接保存当前受托人集合，不进行排名和截取
	d.logger.Debug("💾 落盘时：直接保存当前受托人集合，不进行排名和截取")

	// 🆕 详细记录要落盘的见证人信息
	d.logger.Info("📋 准备落盘的见证人详情:")
	for i, delegate := range d.delegates {
		d.logger.Info("👤 见证人详情",
			"序号", i+1,
			"地址", delegate.Address.String(),
			"投票权重", delegate.VotingPower.String(),
			"是否活跃", delegate.IsActive,
			"isActiveType", fmt.Sprintf("%T", delegate.IsActive),
			"BLS密钥", delegate.BlsKey != nil,
			"BLS获取方式", func() string {
				if delegate.BlsKey != nil {
					return "已存在"
				}
				return "验证时动态获取"
			}())
	}

	// 直接保存当前的 d.delegates 到数据库，不改变受托人集合
	// 如果是在投票处理过程中，只更新投票的验证者
	if d.pendingValidatorUpdate && d.lastVotedDelegate != (types.Address{}) {
		d.logger.Info("🎯 投票处理中，只更新投票的验证者", "targetDelegate", d.lastVotedDelegate.String())
		if err := d.persistDelegateSetToDatabaseWithTarget(d.delegates, d.lastVotedDelegate); err != nil {
			d.logger.Error("❌ Failed to persist target delegate to database", "error", err)
			return err
		}
	} else {
		// 正常情况，保存所有验证者
		if err := d.persistDelegateSetToDatabase(d.delegates); err != nil {
			d.logger.Error("❌ Failed to persist delegate set to database", "error", err)
			return err
		}
	}

	d.logger.Debug("✅ 受托人集合落盘完成", "count", len(d.delegates))
	return nil
}
