package dpos

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

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

	// 🆕 从私钥推导地址，确保地址和私钥匹配
	privateKeyBytes, err := hex.DecodeString(strings.TrimPrefix(privateKey, "0x"))
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}
	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key length: expected 32 bytes, got %d", len(privateKeyBytes))
	}

	// 从私钥推导公钥和地址
	// 注意：privateKeyBytes 已经是解码后的32字节，直接使用 ParseECDSAPrivateKey
	privKey, err := crypto.ParseECDSAPrivateKey(privateKeyBytes)
	if err != nil {
		return fmt.Errorf("failed to create ECDSA private key: %w", err)
	}
	derivedAddress := crypto.PubKeyToAddress(&privKey.PublicKey)

	// 验证私钥地址和 registrant 是否匹配
	if derivedAddress != registrant {
		d.logger.Warn("⚠️ 私钥地址与注册地址不匹配",
			"registrant", registrant.String(),
			"derivedAddress", derivedAddress.String(),
			"note", "将使用私钥推导的地址作为发送者")
		// 使用私钥推导的地址作为发送者（这是实际签名的地址）
		registrant = derivedAddress
	}

	// 使用正确的地址获取nonce
	senderAddress := registrant
	d.logger.Info("🔍 使用发送者地址", "senderAddress", senderAddress.String())

	// 尝试从区块链获取nonce
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
	var addTxErr error
	if d.txPool != nil {
		d.logger.Info("🔍 检查交易池支持...")
		// 尝试将 txPoolInterface 转换为 *TxPool 来访问 AddTx 方法
		if realTxPool, ok := d.txPool.(interface {
			AddTx(tx *types.Transaction) error
		}); ok {
			d.logger.Info("✅ 交易池支持AddTx方法，开始添加交易...")
			if err := realTxPool.AddTx(tx); err != nil {
				d.logger.Error("❌ 添加交易到交易池失败", "error", err)
				addTxErr = err
			} else {
				d.logger.Info("🎉 受托人注册交易已成功添加到交易池！",
					"txHash", tx.Hash.String(),
					"registrant", registrant.String())
				txAdded = true
			}
		} else {
			d.logger.Warn("⚠️ 交易池不支持AddTx方法")
			addTxErr = fmt.Errorf("txpool does not support AddTx method")
		}
	} else {
		d.logger.Warn("⚠️ 交易池为空")
		addTxErr = fmt.Errorf("txpool is nil")
	}

	// 如果交易池添加失败，返回错误（包含原始错误信息）
	if !txAdded {
		d.logger.Error("❌ 交易池添加失败，无法完成受托人注册", "error", addTxErr)
		if addTxErr != nil {
			return fmt.Errorf("failed to add delegate registration transaction to pool: %w", addTxErr)
		}
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

// GetDelegateRegistrations 获取所有受托人注册信息
// 🆕 修改：返回所有验证人（包括非活跃的），而不仅仅是出块的验证人
func (d *DPoS) GetDelegateRegistrations() ([]*DelegateRegistration, error) {
	if d.state == nil || d.state.RegistrationStore == nil {
		return nil, fmt.Errorf("registration store not available")
	}

	// 1. 从数据库获取已注册的受托人信息
	dbRegistrations, err := d.state.RegistrationStore.GetAllRegistrations()
	if err != nil {
		return nil, fmt.Errorf("failed to get registrations from database: %w", err)
	}

	// 2. 🆕 从数据库获取所有验证者（包括非活跃的），而不仅仅是从内存中获取活跃的验证者
	// 这样可以确保返回所有验证人，而不仅仅是出块的验证人
	var allValidators validator.AccountSet
	if d.state != nil && d.state.StakeStore != nil {
		// 使用 GetValidatorsWithFilter(false) 获取所有验证者，包括投票权重为0的
		if dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false); err == nil && len(dbValidators) > 0 {
			allValidators = dbValidators
			d.logger.Debug("从数据库读取所有验证者", "count", len(allValidators))
		}
	}

	// 3. 创建已注册地址的映射，用于去重
	registeredAddresses := make(map[types.Address]bool)
	for _, reg := range dbRegistrations {
		registeredAddresses[reg.Address] = true
	}

	// 4. 将数据库中的所有验证者转换为 DelegateRegistration 格式
	// 只添加未在注册表中注册的验证者（已注册的验证者信息更完整，优先使用注册表的数据）
	result := make([]*DelegateRegistration, 0, len(dbRegistrations)+len(allValidators))
	result = append(result, dbRegistrations...)

	// 将验证者转换为 DelegateRegistration
	zeroDeposit := big.NewInt(0)

	for _, validator := range allValidators {
		// 如果该验证者已经在注册表中，跳过（注册表的数据更完整）
		if registeredAddresses[validator.Address] {
			continue
		}

		// 判断是否为创世验证者
		isGenesis := d.isGenesisValidator(validator.Address)

		// 确定状态
		var status RegStatus
		if validator.IsActive {
			if isGenesis {
				status = RegStatusActive
			} else {
				status = RegStatusCandidate // 非创世验证者默认为候选人
			}
		} else {
			status = RegStatusInactive
		}

		reg := &DelegateRegistration{
			Address: validator.Address,
			Name:    fmt.Sprintf("Validator %s", validator.Address.String()[:10]),
			Website: "",
			Description: func() string {
				if isGenesis {
					return "Genesis validator"
				}
				return "Validator"
			}(),
			Deposit:             new(big.Int).Set(zeroDeposit),
			Status:              status,
			CreatedAt:           0,
			TotalVotes:          new(big.Int).Set(validator.VotingPower),
			IsActive:            validator.IsActive,
			LastVoteTime:        0,
			FrozenAt:            0,
			UnfreezeAt:          0,
			UnfreezeAvailableAt: 0,
		}

		// 如果是创世验证者，使用更友好的名称
		if isGenesis {
			reg.Name = fmt.Sprintf("Genesis Validator %s", validator.Address.String()[:10])
			reg.Description = "Genesis validator with default weight 1000 VCITY"
		}

		result = append(result, reg)
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
			votingPower := DefaultVotingPower() // 1000 VCITY

			genesisValidators = append(genesisValidators, &validator.ValidatorMetadata{
				Address:     genesisValidator.Address,
				VotingPower: votingPower,
				IsActive:    true,
			})
		}
	}

	// 转换为 DelegateRegistration 格式
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
