package dpos

import (
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"time"

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

	// 🆕 如果缓存中没有，优先从数据库（ParameterStore）读取（经过治理流程修改的值是权威数据源）
	if d.state != nil && d.state.ParameterStore != nil {
		if dbValue, err := d.state.ParameterStore.GetParameterValue(parameter); err == nil {
			d.logger.Debug("从数据库读取参数值", "param", parameter, "value", dbValue)
			// 更新缓存
			d.parameterValuesMutex.Lock()
			d.parameterCurrentValues[parameter] = dbValue
			d.parameterValuesMutex.Unlock()
			return dbValue, nil
		}
	}

	// 如果数据库中没有，从配置文件获取（初始默认值）
	switch parameter {
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
	case "dpos_proposal_vote_period":
		// 从YAML配置计算提案表决周期（区块数）
		if d.config != nil && d.config.ProposalVotePeriod > 0 {
			blockTime := d.config.BlockTime.Duration
			if blockTime > 0 {
				blocks := uint64(d.config.ProposalVotePeriod / blockTime)
				return blocks, nil
			}
		}
		// 默认值：1天 = 28800个区块（按3秒/区块）
		return uint64(28800), nil
	case "governance_voting_period":
		// 兼容旧参数名，重定向到 dpos_proposal_vote_period
		return d.getCurrentParameterValue("dpos_proposal_vote_period")
	case "governance_pass_threshold":
		// 治理参数：投票通过阈值
		return uint64(51), nil
	case "min_freeze_period":
		// 冻结参数：最小冻结期（从配置读取）
		if d.config != nil && d.config.MinFreezePeriod > 0 {
			return d.config.MinFreezePeriod, nil
		}
		// 默认值：7天 = 604800秒
		return uint64(604800), nil
	case "unfreeze_lock_period":
		// 冻结参数：解冻锁定期（从配置读取）
		if d.config != nil && d.config.UnfreezeLockPeriod > 0 {
			return d.config.UnfreezeLockPeriod, nil
		}
		// 默认值：14天 = 1209600秒
		return uint64(1209600), nil
	case "dpos_missed_blocks_percentage":
		// 🆕 削减参数：漏块率阈值（从配置读取）
		if val := d.getConfigUint64("dpos_missed_blocks_percentage", "missed_blocks_percentage"); val > 0 {
			return val, nil
		}
		// 默认值：1000基点 = 10%
		return uint64(1000), nil
	case "dpos_minor_offense_slash_rate":
		// 🆕 削减参数：轻度违规削减率（从配置读取）
		if val := d.getConfigUint64("dpos_minor_offense_slash_rate", "minor_offense_slash_rate"); val > 0 {
			return val, nil
		}
		// 默认值：50基点 = 0.5%
		return uint64(50), nil
	case "dpos_severe_offense_slash_rate":
		// 🆕 削减参数：严重违规削减率（从配置读取）
		if val := d.getConfigUint64("dpos_severe_offense_slash_rate", "severe_offense_slash_rate"); val > 0 {
			return val, nil
		}
		// 默认值：1000基点 = 10%
		return uint64(1000), nil
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

	// 🆕 根据参数类型更新对应的配置值
	switch paramName {
	case "dpos_epoch_duration":
		var epochDuration time.Duration

		// 解析参数值（可能是字符串或数字）
		switch v := value.(type) {
		case string:
			// 尝试解析为时间字符串（如 "48s", "2m", "1h"）
			if parsedDuration, parseErr := time.ParseDuration(v); parseErr == nil {
				epochDuration = parsedDuration
			} else {
				// 如果不是时间字符串，尝试解析为秒数（如 "48"）
				if seconds, parseErr := strconv.ParseUint(v, 10, 64); parseErr == nil {
					epochDuration = time.Duration(seconds) * time.Second
				} else {
					d.logger.Warn("无法解析 epoch duration 值", "value", v, "error", parseErr)
					return fmt.Errorf("invalid epoch duration value: %v", v)
				}
			}
		case uint64:
			epochDuration = time.Duration(v) * time.Second
		case int64:
			epochDuration = time.Duration(v) * time.Second
		case float64:
			epochDuration = time.Duration(uint64(v)) * time.Second
		default:
			d.logger.Warn("不支持的 epoch duration 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported epoch duration type: %T", value)
		}

		// 更新 d.config.EpochDuration
		if d.config != nil {
			d.config.EpochDuration = epochDuration
			d.logger.Info("✅ 已更新 d.config.EpochDuration", "newDuration", epochDuration.String())
		}

		// 更新 TimeBasedEpochManager.epochDuration
		if d.epochManager != nil {
			d.epochManager.UpdateEpochDuration(epochDuration)
		}

	case "dpos_reward_amount":
		// 解析奖励金额（字符串格式的 wei）
		var rewardAmount *big.Int
		switch v := value.(type) {
		case string:
			var ok bool
			rewardAmount, ok = new(big.Int).SetString(v, 10)
			if !ok {
				d.logger.Warn("无法解析 reward amount 值", "value", v)
				return fmt.Errorf("invalid reward amount value: %v", v)
			}
		case *big.Int:
			rewardAmount = new(big.Int).Set(v)
		default:
			d.logger.Warn("不支持的 reward amount 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported reward amount type: %T", value)
		}

		// 更新 d.config.RewardAmount
		if d.config != nil {
			d.config.RewardAmount = rewardAmount
			d.logger.Info("✅ 已更新 d.config.RewardAmount", "newAmount", rewardAmount.String())
		}

		// 更新 RewardDistributor.rewardAmount
		if d.rewardDistributor != nil {
			d.rewardDistributor.UpdateRewardAmount(rewardAmount)
		}

	case "dpos_delegate_threshold":
		// 解析最小质押门槛（字符串格式的 wei）
		var threshold *big.Int
		switch v := value.(type) {
		case string:
			var ok bool
			threshold, ok = new(big.Int).SetString(v, 10)
			if !ok {
				d.logger.Warn("无法解析 delegate threshold 值", "value", v)
				return fmt.Errorf("invalid delegate threshold value: %v", v)
			}
		case *big.Int:
			threshold = new(big.Int).Set(v)
		default:
			d.logger.Warn("不支持的 delegate threshold 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported delegate threshold type: %T", value)
		}

		// 更新 d.config.MinVotingPower
		if d.config != nil {
			d.config.MinVotingPower = threshold
			d.logger.Info("✅ 已更新 d.config.MinVotingPower", "newThreshold", threshold.String())
		}

		// 更新 d.minStakeAmount
		d.minStakeAmount = threshold

	case "dpos_proposal_vote_period":
		// 解析提案投票周期（区块数）
		var votePeriod uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				votePeriod = parsed
			} else {
				d.logger.Warn("无法解析 proposal vote period 值", "value", v, "error", err)
				return fmt.Errorf("invalid proposal vote period value: %v", v)
			}
		case uint64:
			votePeriod = v
		case int64:
			if v >= 0 {
				votePeriod = uint64(v)
			} else {
				return fmt.Errorf("invalid proposal vote period value: %v", v)
			}
		case float64:
			if v >= 0 {
				votePeriod = uint64(v)
			} else {
				return fmt.Errorf("invalid proposal vote period value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 proposal vote period 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported proposal vote period type: %T", value)
		}

		// 更新 d.config.ProposalVotePeriod（需要转换为 time.Duration）
		if d.config != nil && d.config.BlockTime.Duration > 0 {
			d.config.ProposalVotePeriod = time.Duration(votePeriod) * d.config.BlockTime.Duration
			d.logger.Info("✅ 已更新 d.config.ProposalVotePeriod", "newPeriod", d.config.ProposalVotePeriod.String(), "blocks", votePeriod)
		}

	case "governance_pass_threshold":
		// 解析提案通过阈值（百分比）
		var threshold uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				threshold = parsed
			} else {
				d.logger.Warn("无法解析 governance pass threshold 值", "value", v, "error", err)
				return fmt.Errorf("invalid governance pass threshold value: %v", v)
			}
		case uint64:
			threshold = v
		case int64:
			if v >= 0 {
				threshold = uint64(v)
			} else {
				return fmt.Errorf("invalid governance pass threshold value: %v", v)
			}
		case float64:
			if v >= 0 {
				threshold = uint64(v)
			} else {
				return fmt.Errorf("invalid governance pass threshold value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 governance pass threshold 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported governance pass threshold type: %T", value)
		}

		// governance_pass_threshold 不需要更新配置，因为它只从参数系统读取
		d.logger.Info("✅ 已更新 governance_pass_threshold", "newThreshold", threshold)

	case "min_freeze_period":
		// 解析最小冻结期（秒）
		var freezePeriod uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				freezePeriod = parsed
			} else {
				d.logger.Warn("无法解析 min freeze period 值", "value", v, "error", err)
				return fmt.Errorf("invalid min freeze period value: %v", v)
			}
		case uint64:
			freezePeriod = v
		case int64:
			if v >= 0 {
				freezePeriod = uint64(v)
			} else {
				return fmt.Errorf("invalid min freeze period value: %v", v)
			}
		case float64:
			if v >= 0 {
				freezePeriod = uint64(v)
			} else {
				return fmt.Errorf("invalid min freeze period value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 min freeze period 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported min freeze period type: %T", value)
		}

		// 更新 d.config.MinFreezePeriod
		if d.config != nil {
			d.config.MinFreezePeriod = freezePeriod
			d.logger.Info("✅ 已更新 d.config.MinFreezePeriod", "newPeriod", freezePeriod, "seconds")
		}

	case "unfreeze_lock_period":
		// 解析解冻锁定期（秒）
		var lockPeriod uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				lockPeriod = parsed
			} else {
				d.logger.Warn("无法解析 unfreeze lock period 值", "value", v, "error", err)
				return fmt.Errorf("invalid unfreeze lock period value: %v", v)
			}
		case uint64:
			lockPeriod = v
		case int64:
			if v >= 0 {
				lockPeriod = uint64(v)
			} else {
				return fmt.Errorf("invalid unfreeze lock period value: %v", v)
			}
		case float64:
			if v >= 0 {
				lockPeriod = uint64(v)
			} else {
				return fmt.Errorf("invalid unfreeze lock period value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 unfreeze lock period 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported unfreeze lock period type: %T", value)
		}

		// 更新 d.config.UnfreezeLockPeriod
		if d.config != nil {
			d.config.UnfreezeLockPeriod = lockPeriod
			d.logger.Info("✅ 已更新 d.config.UnfreezeLockPeriod", "newPeriod", lockPeriod, "seconds")
		}

	case "dpos_missed_blocks_percentage":
		// 解析漏块率阈值（基点）
		var percentage uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				percentage = parsed
			} else {
				d.logger.Warn("无法解析 missed blocks percentage 值", "value", v, "error", err)
				return fmt.Errorf("invalid missed blocks percentage value: %v", v)
			}
		case uint64:
			percentage = v
		case int64:
			if v >= 0 {
				percentage = uint64(v)
			} else {
				return fmt.Errorf("invalid missed blocks percentage value: %v", v)
			}
		case float64:
			if v >= 0 {
				percentage = uint64(v)
			} else {
				return fmt.Errorf("invalid missed blocks percentage value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 missed blocks percentage 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported missed blocks percentage type: %T", value)
		}

		// dpos_missed_blocks_percentage 不需要更新配置，因为它从参数系统读取
		d.logger.Info("✅ 已更新 dpos_missed_blocks_percentage", "newPercentage", percentage, "basis points")

	case "dpos_minor_offense_slash_rate":
		// 解析轻度违规削减率（基点）
		var slashRate uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				slashRate = parsed
			} else {
				d.logger.Warn("无法解析 minor offense slash rate 值", "value", v, "error", err)
				return fmt.Errorf("invalid minor offense slash rate value: %v", v)
			}
		case uint64:
			slashRate = v
		case int64:
			if v >= 0 {
				slashRate = uint64(v)
			} else {
				return fmt.Errorf("invalid minor offense slash rate value: %v", v)
			}
		case float64:
			if v >= 0 {
				slashRate = uint64(v)
			} else {
				return fmt.Errorf("invalid minor offense slash rate value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 minor offense slash rate 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported minor offense slash rate type: %T", value)
		}

		// dpos_minor_offense_slash_rate 不需要更新配置，因为它从参数系统读取
		d.logger.Info("✅ 已更新 dpos_minor_offense_slash_rate", "newRate", slashRate, "basis points")

	case "dpos_severe_offense_slash_rate":
		// 解析严重违规削减率（基点）
		var slashRate uint64
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				slashRate = parsed
			} else {
				d.logger.Warn("无法解析 severe offense slash rate 值", "value", v, "error", err)
				return fmt.Errorf("invalid severe offense slash rate value: %v", v)
			}
		case uint64:
			slashRate = v
		case int64:
			if v >= 0 {
				slashRate = uint64(v)
			} else {
				return fmt.Errorf("invalid severe offense slash rate value: %v", v)
			}
		case float64:
			if v >= 0 {
				slashRate = uint64(v)
			} else {
				return fmt.Errorf("invalid severe offense slash rate value: %v", v)
			}
		default:
			d.logger.Warn("不支持的 severe offense slash rate 类型", "type", fmt.Sprintf("%T", value), "value", value)
			return fmt.Errorf("unsupported severe offense slash rate type: %T", value)
		}

		// dpos_severe_offense_slash_rate 不需要更新配置，因为它从参数系统读取
		d.logger.Info("✅ 已更新 dpos_severe_offense_slash_rate", "newRate", slashRate, "basis points")
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
