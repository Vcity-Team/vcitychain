package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/common"
)

// GetParameterProposal 获取提案信息
func (d *DPoS) GetParameterProposal(proposalID string) (*ParameterProposal, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 先从内存中查找
	proposal, exists := d.parameterProposals[proposalID]
	if exists {
		return proposal, nil
	}

	// 如果内存中没有，从数据库加载
	if d.state != nil && d.state.ProposalStore != nil {
		dbProposal, err := d.state.ProposalStore.GetProposal(proposalID)
		if err == nil {
			// 加载到内存中
			d.parameterProposals[proposalID] = dbProposal
			return dbProposal, nil
		}
		d.logger.Debug("Failed to load proposal from database", "proposalID", proposalID, "error", err)
	}

	return nil, fmt.Errorf("proposal not found")
}

// GetActiveProposals 获取活跃提案列表
func (d *DPoS) GetActiveProposals() ([]*ParameterProposal, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	var activeProposals []*ParameterProposal
	for proposalID := range d.activeProposals {
		if proposal, exists := d.parameterProposals[proposalID]; exists {
			activeProposals = append(activeProposals, proposal)
		}
	}

	return activeProposals, nil
}

// GetVotableParameters 获取可表决参数列表（包含当前值）
func (d *DPoS) GetVotableParameters() map[string]*ParameterInfo {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 返回副本以避免外部修改
	result := make(map[string]*ParameterInfo)
	for name, info := range d.votableParameters {
		// 创建副本
		infoCopy := *info

		// 🆕 添加当前值
		d.parameterValuesMutex.RLock()
		if currentValue, exists := d.parameterCurrentValues[name]; exists {
			infoCopy.CurrentValue = currentValue
			d.logger.Debug("Found current value for parameter",
				"param", name,
				"value", currentValue)
		} else {
			// 如果缓存中没有，尝试从不同来源获取默认值
			var defaultValue interface{}
			var err error

			// 优先尝试从治理参数获取
			if defaultValue, err = d.getGovernanceParameterValue(name); err == nil {
				infoCopy.CurrentValue = defaultValue
				d.logger.Debug("Found governance value for parameter",
					"param", name,
					"value", defaultValue)
			} else {
				// 如果治理参数中没有，尝试从配置文件获取
				if defaultValue, err = d.getConfigParameterValue(name); err == nil {
					infoCopy.CurrentValue = defaultValue
					d.logger.Debug("Found config value for parameter",
						"param", name,
						"value", defaultValue)
				} else {
					d.logger.Debug("No current value found for parameter",
						"param", name,
						"cacheSize", len(d.parameterCurrentValues))
				}
			}
		}
		d.parameterValuesMutex.RUnlock()

		// 调试：确认CurrentValue是否被设置
		d.logger.Debug("Setting result for parameter",
			"param", name,
			"currentValue", infoCopy.CurrentValue,
			"hasCurrentValue", infoCopy.CurrentValue != nil)

		result[name] = &infoCopy
	}
	return result
}

// GetCurrentParameterValues 获取当前参数的实际值
func (d *DPoS) GetCurrentParameterValues() map[string]interface{} {
	d.lock.RLock()
	defer d.lock.RUnlock()

	result := make(map[string]interface{})

	// 获取所有可表决参数的当前值
	for paramName := range d.votableParameters {
		if value, err := d.getCurrentParameterValue(paramName); err == nil {
			result[paramName] = value
		} else {
			d.logger.Warn("Failed to get current value for parameter", "parameter", paramName, "error", err)
		}
	}

	// 添加 dpos_proposal_vote_period（以区块数表示，来自YAML配置）
	var proposalVotePeriod time.Duration
	if d.config != nil {
		proposalVotePeriod = d.config.ProposalVotePeriod
	}

	if proposalVotePeriod == 0 {
		proposalVotePeriod = 24 * time.Hour
		d.logger.Warn("📋 ProposalVotePeriod为0或未设置，使用默认值24小时")
	}

	blockTime := 2 * time.Second
	if d.config != nil && d.config.BlockTime.Duration > 0 {
		blockTime = d.config.BlockTime.Duration
	}
	if blockTime > 0 {
		blocks := uint64(proposalVotePeriod / blockTime)
		result["dpos_proposal_vote_period"] = blocks
	}

	// 添加一些额外的系统信息
	result["lastUpdated"] = time.Now().Format(time.RFC3339)
	// 仅排除 lastUpdated，不排除其它键
	result["totalParameters"] = len(result) - 1

	return result
}

// UpdateParameterValue 更新参数值（公共方法）
func (d *DPoS) UpdateParameterValue(parameter string, newValue interface{}) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 根据参数类型更新相应的配置
	switch parameter {
	case "dpos_validator_reward_ratio":
		if ratio, ok := newValue.(uint64); ok {
			d.config.ValidatorRewardRatio = ratio
			d.logger.Info("Updated validator reward ratio", "newValue", ratio)
		}
	case "dpos_voter_reward_ratio":
		if ratio, ok := newValue.(uint64); ok {
			d.config.VoterRewardRatio = ratio
			d.logger.Info("Updated voter reward ratio", "newValue", ratio)
		}
	case "dpos_reward_amount":
		if amount, ok := newValue.(string); ok {
			if bigAmount, ok := new(big.Int).SetString(amount, 10); ok {
				d.config.RewardAmount = bigAmount
				d.logger.Info("Updated reward amount", "newValue", amount)
			}
		}
	case "dpos_delegate_threshold":
		if threshold, ok := newValue.(string); ok {
			if bigThreshold, ok := new(big.Int).SetString(threshold, 10); ok {
				d.config.MinVotingPower = bigThreshold
				d.logger.Info("Updated delegate threshold", "newValue", threshold)
			}
		}
	case "block_time_s":
		if blockTime, ok := newValue.(uint64); ok {
			d.config.BlockTime = common.Duration{Duration: time.Duration(blockTime) * time.Second}
			d.logger.Info("Updated block time", "newValue", blockTime)
		}
	case "dpos_epoch_duration":
		if duration, ok := newValue.(string); ok {
			if parsedDuration, err := time.ParseDuration(duration); err == nil {
				d.config.EpochDuration = parsedDuration
				d.logger.Info("Updated epoch duration", "newValue", duration)
			}
		}
	default:
		return fmt.Errorf("unknown parameter: %s", parameter)
	}

	return nil
}

