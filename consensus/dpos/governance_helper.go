package dpos

import (
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// isParameterVotable 检查参数是否可表决
func (d *DPoS) isParameterVotable(parameter string) bool {
	_, exists := d.votableParameters[parameter]
	return exists
}

// IsParameterVotable 检查参数是否可表决（公共方法，供RPC层使用）
func (d *DPoS) IsParameterVotable(parameter string) bool {
	return d.isParameterVotable(parameter)
}

// getCurrentParameterValue 获取当前参数值
func (d *DPoS) getCurrentParameterValue(parameter string) (interface{}, error) {
	// 优先从缓存获取
	d.parameterValuesMutex.RLock()
	if value, exists := d.parameterCurrentValues[parameter]; exists {
		d.parameterValuesMutex.RUnlock()
		return value, nil
	}
	d.parameterValuesMutex.RUnlock()

	// 如果缓存中没有，从不同来源获取
	switch parameter {
	case "dpos_validator_reward_ratio":
		return d.config.ValidatorRewardRatio, nil
	case "dpos_voter_reward_ratio":
		return d.config.VoterRewardRatio, nil
	case "dpos_reward_amount":
		if d.config.RewardAmount != nil {
			return d.config.RewardAmount.String(), nil
		}
		return "0", nil
	case "dpos_delegate_threshold":
		if d.config.MinVotingPower != nil {
			return d.config.MinVotingPower.String(), nil
		}
		return "0", nil
	case "block_time_s":
		return uint64(d.config.BlockTime.Duration.Seconds()), nil
	case "dpos_epoch_duration":
		return d.config.EpochDuration.String(), nil
	case "governance_voting_period":
		// 🆕 从YAML配置计算提案表决周期（区块数）
		if d.config != nil && d.config.ProposalVotePeriod > 0 {
			blockTime := d.config.BlockTime.Duration
			if blockTime > 0 {
				blocks := uint64(d.config.ProposalVotePeriod / blockTime)
				return blocks, nil
			}
		}
		// 默认值：1天 = 43200个区块（按2秒/区块）
		return uint64(43200), nil
	case "governance_voting_threshold":
		// 治理参数：投票通过阈值
		return uint64(51), nil
	case "governance_min_voting_threshold":
		// 治理参数：最小投票门槛
		return d.getMinVotingThreshold().String(), nil
	default:
		return nil, fmt.Errorf("unknown parameter: %s", parameter)
	}
}

// validateParameterValue 验证参数值
func (d *DPoS) validateParameterValue(parameter string, value interface{}) error {
	paramInfo, exists := d.votableParameters[parameter]
	if !exists {
		return fmt.Errorf("parameter not found")
	}

	switch paramInfo.Type {
	case "uint64":
		// 支持多种数值类型的转换
		var val uint64
		var err error

		switch v := value.(type) {
		case uint64:
			val = v
		case int:
			if v < 0 {
				return fmt.Errorf("negative value not allowed for uint64 parameter")
			}
			val = uint64(v)
		case int64:
			if v < 0 {
				return fmt.Errorf("negative value not allowed for uint64 parameter")
			}
			val = uint64(v)
		case float64:
			// JSON数值被解析为float64，需要转换
			if v < 0 {
				return fmt.Errorf("negative value not allowed for uint64 parameter")
			}
			if v != float64(uint64(v)) {
				return fmt.Errorf("invalid float value for uint64 parameter: %v", v)
			}
			val = uint64(v)
		case string:
			// 支持字符串转uint64
			val, err = strconv.ParseUint(v, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid string value for uint64 parameter: %s", v)
			}
		default:
			return fmt.Errorf("invalid type %T for uint64 parameter, expected uint64/int/int64/float64/string", value)
		}

		// 安全的类型断言和范围检查
		minVal, ok := paramInfo.MinValue.(uint64)
		if !ok {
			return fmt.Errorf("invalid MinValue type for parameter %s", parameter)
		}
		maxVal, ok := paramInfo.MaxValue.(uint64)
		if !ok {
			return fmt.Errorf("invalid MaxValue type for parameter %s", parameter)
		}

		if val < minVal || val > maxVal {
			return fmt.Errorf("value %d out of range [%d, %d]", val, minVal, maxVal)
		}

	case "string":
		var val string
		switch v := value.(type) {
		case string:
			val = v
		case int, int64, uint64, float64:
			// 数值类型转换为字符串
			val = fmt.Sprintf("%v", v)
		default:
			return fmt.Errorf("invalid type %T for string parameter", value)
		}

		// 对于大整数字符串，需要特殊处理
		if parameter == "dpos_reward_amount" || parameter == "dpos_delegate_threshold" {
			bigVal, ok := new(big.Int).SetString(val, 10)
			if !ok {
				return fmt.Errorf("invalid big integer string: %s", val)
			}

			// 安全的类型断言
			minValStr, ok := paramInfo.MinValue.(string)
			if !ok {
				return fmt.Errorf("invalid MinValue type for parameter %s", parameter)
			}
			maxValStr, ok := paramInfo.MaxValue.(string)
			if !ok {
				return fmt.Errorf("invalid MaxValue type for parameter %s", parameter)
			}

			minVal, ok := new(big.Int).SetString(minValStr, 10)
			if !ok {
				return fmt.Errorf("invalid MinValue format for parameter %s: %s", parameter, minValStr)
			}
			maxVal, ok := new(big.Int).SetString(maxValStr, 10)
			if !ok {
				return fmt.Errorf("invalid MaxValue format for parameter %s: %s", parameter, maxValStr)
			}

			if bigVal.Cmp(minVal) < 0 || bigVal.Cmp(maxVal) > 0 {
				return fmt.Errorf("value %s out of range [%s, %s]", val, minValStr, maxValStr)
			}
		}

	default:
		return fmt.Errorf("unsupported parameter type: %s", paramInfo.Type)
	}

	return nil
}

// updateParameterValue 更新参数值
func (d *DPoS) updateParameterValue(paramName string, value interface{}, source string) error {
	d.parameterValuesMutex.Lock()
	defer d.parameterValuesMutex.Unlock()

	// 更新内存缓存
	d.parameterCurrentValues[paramName] = value

	// 保存到数据库
	if d.state != nil && d.state.ParameterStore != nil {
		if err := d.state.ParameterStore.SaveParameterValue(paramName, value, source); err != nil {
			return fmt.Errorf("failed to save parameter value to database: %w", err)
		}
	}

	d.logger.Info("Parameter value updated",
		"param", paramName,
		"value", value,
		"source", source)

	return nil
}

// buildProposalMessage 构造提案签名消息
func (d *DPoS) buildProposalMessage(proposal *ParameterProposal) []byte {
	// 🆕 修改签名消息格式：不包含proposalID（因为proposalID在交易处理时才确定）
	// 构造签名消息：提案者地址 + 提案类型 + 参数/验证者地址 + 时间戳 + 链ID
	// 这样签名可以在proposalID确定前后都有效
	data := make([]byte, 0)

	// 1. 提案者地址 (20字节)
	data = append(data, proposal.Proposer.Bytes()...)

	// 2. 提案ID (移除，改为不包含，让签名与proposalID无关)
	// proposalIDBytes := []byte(proposal.ID)
	// proposalIDLen := make([]byte, 4)
	// binary.BigEndian.PutUint32(proposalIDLen, uint32(len(proposalIDBytes)))
	// data = append(data, proposalIDLen...)
	// data = append(data, proposalIDBytes...)

	// 3. 提案类型 (变长，添加长度前缀)
	proposalTypeBytes := []byte(proposal.ProposalType)
	proposalTypeLen := make([]byte, 4)
	binary.BigEndian.PutUint32(proposalTypeLen, uint32(len(proposalTypeBytes)))
	data = append(data, proposalTypeLen...)
	data = append(data, proposalTypeBytes...)

	// 4. 参数或验证者地址（根据类型）
	if proposal.ProposalType == "validator_recovery" && proposal.ValidatorAddress != (types.Address{}) {
		// 恢复提案：使用验证者地址
		data = append(data, proposal.ValidatorAddress.Bytes()...)
	} else {
		// 参数提案：使用参数名
		parameterBytes := []byte(proposal.Parameter)
		parameterLen := make([]byte, 4)
		binary.BigEndian.PutUint32(parameterLen, uint32(len(parameterBytes)))
		data = append(data, parameterLen...)
		data = append(data, parameterBytes...)
	}

	// 5. 时间戳 (8字节)
	timestampBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(timestampBytes, proposal.CreatedAt)
	data = append(data, timestampBytes...)

	// 6. 链ID (8字节，如果config中有chainID)
	var chainID uint64
	if d.config != nil && d.config.Blockchain != nil && d.config.Blockchain.Config() != nil {
		chainID = uint64(d.config.Blockchain.Config().ChainID)
	}
	chainIDBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(chainIDBytes, chainID)
	data = append(data, chainIDBytes...)

	// 计算Keccak256哈希
	message := crypto.Keccak256(data)

	d.logger.Debug("🔍 构造提案签名消息",
		"proposer", proposal.Proposer.String(),
		"proposalType", proposal.ProposalType,
		"parameter", proposal.Parameter,
		"createdAt", proposal.CreatedAt,
		"chainID", chainID,
		"messageHash", hex.EncodeToString(message))

	return message
}

// signProposal 签名提案
func (d *DPoS) signProposal(proposal *ParameterProposal, proposerPrivateKeyHex string) error {
	d.logger.Info("🔍 开始签名提案", "proposer", proposal.Proposer.String(), "proposalID", proposal.ID)

	// 1. 验证私钥格式
	if len(proposerPrivateKeyHex) != 64 {
		return fmt.Errorf("invalid private key length: expected 64, got %d", len(proposerPrivateKeyHex))
	}

	// 2. 验证私钥hex字符
	for i, char := range proposerPrivateKeyHex {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return fmt.Errorf("invalid hex character at position %d: %c", i, char)
		}
	}

	// 3. 解码私钥
	privateKeyBytes, err := hex.DecodeString(proposerPrivateKeyHex)
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}

	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	// 4. 创建ECDSA私钥对象
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	// 5. 验证私钥地址匹配
	calculatedAddr := crypto.PubKeyToAddress(&privateKey.PublicKey)
	if calculatedAddr != proposal.Proposer {
		return fmt.Errorf("private key does not match proposer address: calculated=%s, expected=%s",
			calculatedAddr.String(), proposal.Proposer.String())
	}

	d.logger.Info("✅ 私钥验证通过", "address", calculatedAddr.String())

	// 6. 构造签名消息
	message := d.buildProposalMessage(proposal)

	// 7. 签名
	signature, err := crypto.Sign(privateKey, message)
	if err != nil {
		return fmt.Errorf("failed to sign proposal: %w", err)
	}

	// 8. 保存签名
	proposal.ProposalSignature = signature

	d.logger.Info("✅ 提案签名成功",
		"proposer", proposal.Proposer.String(),
		"proposalID", proposal.ID,
		"signatureLength", len(signature))

	return nil
}

// verifyProposalSignature 验证提案签名
func (d *DPoS) verifyProposalSignature(proposal *ParameterProposal) error {
	if len(proposal.ProposalSignature) == 0 {
		return fmt.Errorf("proposal signature is empty")
	}

	// 1. 构造消息（与签名时相同）
	message := d.buildProposalMessage(proposal)

	// 2. 恢复公钥
	pubKey, err := crypto.RecoverPubkey(proposal.ProposalSignature, message)
	if err != nil {
		return fmt.Errorf("failed to recover public key: %w", err)
	}

	// 3. 计算地址
	recoveredAddr := crypto.PubKeyToAddress(pubKey)

	// 4. 验证地址匹配
	if recoveredAddr != proposal.Proposer {
		return fmt.Errorf("signature does not match proposer address: recovered=%s, expected=%s",
			recoveredAddr.String(), proposal.Proposer.String())
	}

	// 5. 验证提案者是验证者
	if !d.isValidator(proposal.Proposer) {
		return fmt.Errorf("proposer %s is not a validator", proposal.Proposer.String())
	}

	d.logger.Debug("✅ 提案签名验证成功", "proposer", proposal.Proposer.String())

	return nil
}

// SignProposalForTx 为交易签名提案（用于RPC层，使用临时proposalID）
func (d *DPoS) SignProposalForTx(proposal *ParameterProposal, proposerPrivateKeyHex string) ([]byte, error) {
	// 直接调用signProposal
	if err := d.signProposal(proposal, proposerPrivateKeyHex); err != nil {
		return nil, err
	}
	return proposal.ProposalSignature, nil
}

