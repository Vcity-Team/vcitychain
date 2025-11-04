package dpos

import (
	"fmt"
)

// InitializeGovernance 初始化治理系统
func (d *DPoS) InitializeGovernance() error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 初始化治理相关字段
	if d.parameterProposals == nil {
		d.parameterProposals = make(map[string]*ParameterProposal)
	}
	if d.activeProposals == nil {
		d.activeProposals = make(map[string]bool)
	}
	if d.parameterUpdates == nil {
		d.parameterUpdates = make([]*ParameterUpdate, 0)
	}
	if d.votableParameters == nil {
		d.votableParameters = d.getDefaultVotableParameters()
	}

	// 🆕 初始化参数缓存（强制从数据库同步）
	if err := d.initializeParameterCache(); err != nil {
		return fmt.Errorf("failed to initialize parameter cache: %w", err)
	}

	// 🆕 从数据库加载所有提案
	if err := d.loadProposalsFromDatabase(); err != nil {
		return fmt.Errorf("failed to load proposals from database: %w", err)
	}

	d.logger.Info("✅ 治理系统初始化完成",
		"votableParameters", len(d.votableParameters),
		"activeProposals", len(d.activeProposals),
		"loadedProposals", len(d.parameterProposals))

	return nil
}

// loadProposalsFromDatabase 从数据库加载所有提案
func (d *DPoS) loadProposalsFromDatabase() error {
	if d.state == nil || d.state.ProposalStore == nil {
		d.logger.Debug("ProposalStore not available, skipping proposal loading")
		return nil
	}

	proposals, err := d.state.ProposalStore.GetAllProposals()
	if err != nil {
		return fmt.Errorf("failed to load proposals from database: %w", err)
	}

	d.logger.Debug("从数据库加载提案", "count", len(proposals))

	// 加载到内存中
	for proposalID, proposal := range proposals {
		d.parameterProposals[proposalID] = proposal

		// 根据提案状态设置活跃状态
		if proposal.Status == ProposalPending || proposal.Status == ProposalActive {
			d.activeProposals[proposalID] = true
		}

		d.logger.Debug("加载提案",
			"proposalID", proposalID,
			"parameter", proposal.Parameter,
			"status", proposal.Status.String())
	}

	d.logger.Info("✅ 从数据库加载提案完成", "count", len(proposals))
	return nil
}

// initializeParameterCache 初始化参数缓存 - 数据库优先
func (d *DPoS) initializeParameterCache() error {
	d.parameterValuesMutex.Lock()
	defer d.parameterValuesMutex.Unlock()

	d.parameterCurrentValues = make(map[string]interface{})

	d.logger.Info("Starting parameter cache initialization",
		"votableParamsCount", len(d.votableParameters))

	// 强制从数据库加载所有参数值
	for paramName := range d.votableParameters {
		// 优先从数据库读取（数据库是权威数据源）
		dbValue, err := d.state.ParameterStore.GetParameterValue(paramName)
		if err == nil {
			// 数据库有值，使用数据库值
			d.parameterCurrentValues[paramName] = dbValue
			d.logger.Info("Loaded parameter from database",
				"param", paramName,
				"value", dbValue)
		} else {
			// 数据库没有值，使用配置文件默认值并保存到数据库
			if defaultValue, err := d.getConfigParameterValue(paramName); err == nil {
				d.parameterCurrentValues[paramName] = defaultValue
				d.state.ParameterStore.SaveParameterValue(paramName, defaultValue, "config")
				d.logger.Info("Loaded parameter from config",
					"param", paramName,
					"value", defaultValue)
			} else {
				d.logger.Error("Failed to get config value for parameter",
					"param", paramName,
					"error", err)
			}
		}
	}

	d.logger.Info("Parameter cache initialized",
		"count", len(d.parameterCurrentValues),
		"cacheKeys", func() []string {
			keys := make([]string, 0, len(d.parameterCurrentValues))
			for k := range d.parameterCurrentValues {
				keys = append(keys, k)
			}
			return keys
		}())

	return nil
}

// getDefaultVotableParameters 获取默认可表决参数配置
func (d *DPoS) getDefaultVotableParameters() map[string]*ParameterInfo {
	return map[string]*ParameterInfo{
		"dpos_validator_reward_ratio": {
			Name:        "Validator Reward Ratio",
			Type:        "uint64",
			MinValue:    uint64(0),
			MaxValue:    uint64(100),
			Description: "验证者奖励比例 (0-100%)",
			Category:    "economic",
		},
		"dpos_voter_reward_ratio": {
			Name:        "Voter Reward Ratio",
			Type:        "uint64",
			MinValue:    uint64(0),
			MaxValue:    uint64(100),
			Description: "投票者奖励比例 (0-100%)",
			Category:    "economic",
		},
		"dpos_reward_amount": {
			Name:        "Reward Amount",
			Type:        "string",
			MinValue:    "0",
			MaxValue:    "1000000000000000000000000", // 100万VCITY
			Description: "每个epoch奖励金额 (wei)",
			Category:    "economic",
		},
		"dpos_delegate_threshold": {
			Name:        "Delegate Threshold",
			Type:        "string",
			MinValue:    "1000000000000000000",       // 1 VCITY
			MaxValue:    "1000000000000000000000000", // 100万VCITY
			Description: "最小质押门槛 (wei)",
			Category:    "economic",
		},
		"block_time_s": {
			Name:        "Block Time",
			Type:        "uint64",
			MinValue:    uint64(1),
			MaxValue:    uint64(60),
			Description: "区块间隔时间 (秒)",
			Category:    "consensus",
		},
		"dpos_epoch_duration": {
			Name:        "Epoch Duration",
			Type:        "string",
			MinValue:    "10s",
			MaxValue:    "1h",
			Description: "Epoch持续时间",
			Category:    "consensus",
		},
		// 🆕 治理参数
		"governance_voting_threshold": {
			Name:        "Voting Threshold",
			Type:        "uint64",
			MinValue:    uint64(30), // 最少30%
			MaxValue:    uint64(90), // 最多90%
			Description: "提案通过所需的最低支持率 (%)",
			Category:    "governance",
		},
		"governance_min_voting_threshold": {
			Name:        "Min Voting Threshold",
			Type:        "string",
			MinValue:    "1000000000000000000",    // 最少1 VIC
			MaxValue:    "1000000000000000000000", // 最多1000 VIC
			Description: "参与投票所需的最小质押门槛 (wei)",
			Category:    "governance",
		},
	}
}

// getGovernanceParameterValue 获取治理参数的当前值
func (d *DPoS) getGovernanceParameterValue(paramName string) (interface{}, error) {
	switch paramName {
	case "governance_voting_threshold":
		// 默认51%的通过门槛
		return uint64(51), nil
	case "governance_min_voting_threshold":
		// 默认最小投票门槛
		return d.getMinVotingThreshold().String(), nil
	default:
		return nil, fmt.Errorf("unknown governance parameter: %s", paramName)
	}
}

// getVotePeriod 获取当前投票期间长度（区块数）
func (d *DPoS) getVotePeriod() uint64 {
	// 🆕 从配置文件获取表决周期（YAML配置优先）
	if d.config != nil && d.config.ProposalVotePeriod > 0 {
		// 根据区块时间计算区块数
		blockTime := d.config.BlockTime.Duration
		if blockTime > 0 {
			blocks := uint64(d.config.ProposalVotePeriod / blockTime)
			d.logger.Debug("📋 计算提案表决周期区块数", "proposalVotePeriod", d.config.ProposalVotePeriod, "blockTime", blockTime, "blocks", blocks)
			return blocks
		}
	}

	// 使用默认值（1天 = 43200个区块，按2秒/区块计算）
	return 43200
}

// getValidPeriod 获取提案有效期（区块数）
func (d *DPoS) getValidPeriod() uint64 {
	// 🆕 从配置文件获取有效期（YAML配置优先）
	if d.config != nil && d.config.ProposalValidPeriod > 0 {
		// 根据区块时间计算区块数
		blockTime := d.config.BlockTime.Duration
		if blockTime > 0 {
			blocks := uint64(d.config.ProposalValidPeriod / blockTime)
			d.logger.Debug("📋 计算提案有效期区块数", "proposalValidPeriod", d.config.ProposalValidPeriod, "blockTime", blockTime, "blocks", blocks)
			return blocks
		}
	}

	// 使用默认值（7天 = 302400个区块，按2秒/区块计算）
	return 302400
}

// GetCurrentProposalPeriod 获取当前提案周期信息（用于显示）
func (d *DPoS) GetCurrentProposalPeriod() map[string]interface{} {
	// 获取当前投票期间长度
	votePeriod := d.getVotePeriod()

	// 获取当前区块高度
	currentBlock := d.GetCurrentBlockNumber()

	// 计算时间信息
	var timeInfo string
	if d.config != nil && d.config.ProposalVotePeriod > 0 {
		// 显示时间格式
		timeInfo = fmt.Sprintf("%s (%d个区块)", d.config.ProposalVotePeriod.String(), votePeriod)
	} else {
		// 只显示区块数
		timeInfo = fmt.Sprintf("%d个区块", votePeriod)
	}

	return map[string]interface{}{
		"blocks":       votePeriod,
		"timeInfo":     timeInfo,
		"currentBlock": currentBlock,
	}
}

// getVotingThreshold 获取当前投票通过阈值
func (d *DPoS) getVotingThreshold() uint64 {
	// 优先从缓存获取
	d.parameterValuesMutex.RLock()
	if value, exists := d.parameterCurrentValues["governance_voting_threshold"]; exists {
		if threshold, ok := value.(uint64); ok {
			d.parameterValuesMutex.RUnlock()
			return threshold
		}
	}
	d.parameterValuesMutex.RUnlock()

	// 如果缓存中没有，使用默认值
	return 51
}

// getConfigParameterValue 从配置文件获取参数值
func (d *DPoS) getConfigParameterValue(paramName string) (interface{}, error) {
	switch paramName {
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
		return "1000000000000000000", nil
	case "block_time_s":
		return d.config.BlockTime.Duration.Seconds(), nil
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
	default:
		return nil, fmt.Errorf("unknown parameter: %s", paramName)
	}
}

