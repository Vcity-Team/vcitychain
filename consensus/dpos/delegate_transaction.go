package dpos

import (
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
)

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

	// 🆕 冻结资金（从账户余额中扣除，但不转账，而是冻结）
	frozenAt := uint64(time.Now().Unix())
	d.logger.Info("❄️ 开始冻结资金",
		"address", regInfo.Registrant.String(),
		"amount", regInfo.Deposit.String(),
		"frozenAt", frozenAt)

	// 🆕 从账户余额中扣除冻结金额（如果 Executor 可用）
	if d.config != nil && d.config.Executor != nil && d.config.Blockchain != nil {
		// 获取当前区块头
		currentHeader := d.config.Blockchain.Header()
		if currentHeader != nil {
			// 获取当前状态快照
			snapshot, err := d.config.Executor.StateAt(currentHeader.StateRoot)
			if err == nil && snapshot != nil {
				// 创建状态事务
				txn := state.NewTxn(snapshot)

				// 从账户余额中扣除冻结金额
				if err := txn.SubBalance(regInfo.Registrant, regInfo.Deposit); err != nil {
					d.logger.Error("❌ 扣除冻结金额失败", "error", err)
					return fmt.Errorf("failed to deduct frozen amount: %w", err)
				}

				// 提交状态变更
				objects, err := txn.Commit(true)
				if err != nil {
					d.logger.Error("❌ 提交冻结状态变更失败", "error", err)
					return fmt.Errorf("failed to commit freeze state changes: %w", err)
				}

				// 更新状态根（如果需要）
				if len(objects) > 0 {
					var newSnapshot state.Snapshot
					var newStateRoot []byte
					newSnapshot, newStateRoot, err = snapshot.Commit(objects)
					if err != nil {
						d.logger.Error("❌ 更新状态根失败", "error", err)
						return fmt.Errorf("failed to update state root: %w", err)
					}
					d.logger.Info("✅ 冻结金额已从账户余额中扣除",
						"address", regInfo.Registrant.String(),
						"amount", regInfo.Deposit.String(),
						"newStateRoot", fmt.Sprintf("%x", newStateRoot[:8]),
						"newSnapshot", newSnapshot != nil)
				}
			} else {
				d.logger.Warn("⚠️ 无法获取状态快照，跳过余额扣除", "error", err)
			}
		} else {
			d.logger.Warn("⚠️ 无法获取当前区块头，跳过余额扣除")
		}
	} else {
		d.logger.Warn("⚠️ Executor或Blockchain不可用，跳过余额扣除")
	}

	// 创建冻结信息
	freezeInfo := &FreezeInfo{
		Address:             regInfo.Registrant,
		FrozenAmount:        regInfo.Deposit,
		FrozenAt:            frozenAt,
		UnfreezeAt:          0,        // 未解冻
		UnfreezeAvailableAt: 0,        // 未解冻
		Status:              "frozen", // 冻结中
	}

	// 保存冻结信息
	if d.state != nil && d.state.FreezeStore != nil {
		if err := d.state.FreezeStore.SaveFreezeInfo(freezeInfo); err != nil {
			d.logger.Error("❌ 保存冻结信息失败", "error", err)
			return fmt.Errorf("failed to save freeze info: %w", err)
		}
		d.logger.Info("✅ 冻结信息已保存")
	}

	// 创建受托人候选人
	d.logger.Info("👤 开始创建受托人候选人...")
	registration := &DelegateRegistration{
		Address:             regInfo.Registrant,
		Name:                regInfo.Name,
		Website:             regInfo.Website,
		Description:         regInfo.Description,
		Deposit:             regInfo.Deposit,
		Status:              RegStatusCandidate, // 候选人状态
		CreatedAt:           frozenAt,
		TotalVotes:          big.NewInt(0),
		IsActive:            false,
		LastVoteTime:        0,
		FrozenAt:            frozenAt, // 🆕 冻结时间
		UnfreezeAt:          0,        // 🆕 未解冻
		UnfreezeAvailableAt: 0,        // 🆕 未解冻
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

// processCommissionUpdateTransaction 处理佣金率修改交易
func (d *DPoS) processCommissionUpdateTransaction(tx *types.Transaction, blockNumber uint64) error {
	d.logger.Info("🔧 ===== 开始处理佣金率修改交易 =====")

	if tx == nil {
		return fmt.Errorf("nil transaction")
	}

	if len(tx.Input) < 9 {
		return fmt.Errorf("invalid commission transaction payload length: %d", len(tx.Input))
	}

	newRate := binary.BigEndian.Uint16(tx.Input[7:9])
	if newRate < 500 || newRate > 8000 {
		return fmt.Errorf("commission rate out of range [500, 8000], got %d", newRate)
	}

	if tx.From == (types.Address{}) {
		return fmt.Errorf("commission update transaction missing sender")
	}

	validatorAddr := tx.From
	d.logger.Info("🔍 佣金率修改交易详情",
		"blockNumber", blockNumber,
		"txHash", tx.Hash.String(),
		"validator", validatorAddr.String(),
		"newRate", newRate)

	// 验证者必须已注册或是创世验证者
	if !d.IsDelegateRegistered(validatorAddr) && !d.isGenesisValidator(validatorAddr) {
		return fmt.Errorf("validator %s is not registered", validatorAddr.String())
	}

	store, err := d.getStateStore()
	if err != nil {
		return err
	}

	delegateInfo, err := store.GetDelegateInfo(validatorAddr)
	if err != nil {
		d.logger.Warn("⚠️ 获取受托人信息失败，使用默认值",
			"validator", validatorAddr.String(),
			"error", err)
	}

	if delegateInfo == nil {
		delegateInfo = &DelegateInfo{
			Address:        validatorAddr,
			VotingPower:    big.NewInt(0),
			TotalVotes:     big.NewInt(0),
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       true,
		}
		d.applyCommissionDefaults(delegateInfo)
	} else {
		if delegateInfo.VotingPower == nil {
			delegateInfo.VotingPower = big.NewInt(0)
		}
		if delegateInfo.TotalVotes == nil {
			delegateInfo.TotalVotes = big.NewInt(0)
		}
		d.applyCommissionDefaults(delegateInfo)
	}

	now := uint64(time.Now().Unix())
	effectivePeriod := d.config.CommissionEffectivePeriod
	if effectivePeriod <= 0 {
		effectivePeriod = 21 * 24 * time.Hour
	}
	cooldownSeconds := uint64(effectivePeriod.Seconds())

	if delegateInfo.PendingCommissionRate != 0 {
		canApply := delegateInfo.CommissionUpdateTime == 0 || cooldownSeconds == 0 ||
			now >= delegateInfo.CommissionUpdateTime+cooldownSeconds

		if canApply {
			d.logger.Info("⏳ 待生效佣金率已到期，自动转正",
				"validator", validatorAddr.String(),
				"pendingRate", delegateInfo.PendingCommissionRate)
			delegateInfo.CommissionRate = delegateInfo.PendingCommissionRate
			delegateInfo.PendingCommissionRate = 0
			delegateInfo.CommissionUpdateTime = now
		} else {
			remaining := delegateInfo.CommissionUpdateTime + cooldownSeconds - now
			return fmt.Errorf("commission rate update cooling down, remaining %d seconds", remaining)
		}
	}

	if delegateInfo.CommissionRate == uint64(newRate) && delegateInfo.PendingCommissionRate == 0 {
		d.logger.Info("ℹ️ 佣金率未变化，忽略本次交易",
			"validator", validatorAddr.String(),
			"currentRate", delegateInfo.CommissionRate)
		return nil
	}

	if delegateInfo.CommissionRate == 0 {
		d.logger.Info("💼 首次设置佣金率，立即生效",
			"validator", validatorAddr.String(),
			"rate", newRate)
		delegateInfo.CommissionRate = uint64(newRate)
		delegateInfo.PendingCommissionRate = 0
		delegateInfo.CommissionUpdateTime = now
	} else {
		d.logger.Info("💼 设置新的待生效佣金率",
			"validator", validatorAddr.String(),
			"rate", newRate)
		delegateInfo.PendingCommissionRate = uint64(newRate)
		delegateInfo.CommissionUpdateTime = now
	}

	if err := d.state.StakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil); err != nil {
		return fmt.Errorf("failed to persist commission rate: %w", err)
	}

	d.logger.Info("✅ 佣金率修改交易处理完成",
		"validator", validatorAddr.String(),
		"currentRate", delegateInfo.CommissionRate,
		"pendingRate", delegateInfo.PendingCommissionRate,
		"updateTime", delegateInfo.CommissionUpdateTime)

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
