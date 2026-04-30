package dpos

import (
	"errors"
	"fmt"
	"strconv"
)

// InitializeGovernance 初始化治理系统
func (d *DPoS) InitializeGovernance() error {
	d.lock.Lock()
	defer d.lock.Unlock()

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

	// 初始化参数缓存（强制从数据库同步）
	if err := d.initializeParameterCache(); err != nil {
		return fmt.Errorf("failed to initialize parameter cache: %w", err)
	}

	// 从数据库加载所有提案
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
	d.logger.Info("🔍 [loadProposalsFromDatabase] 开始从数据库加载所有提案")
	if err := d.governanceLoadAllIntoMemory(); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ [loadProposalsFromDatabase] ProposalStore不可用，跳过加载",
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
			return nil
		}
		d.logger.Error("❌ [loadProposalsFromDatabase] 从数据库加载提案失败", "error", err)
		return fmt.Errorf("failed to load proposals from database: %w", err)
	}

	d.logger.Info("✅ [loadProposalsFromDatabase] 从数据库加载提案完成", "loadedToMemory", len(d.parameterProposals))
	return nil
}

// initializeParameterCache 初始化参数缓存 - 数据库优先
func (d *DPoS) initializeParameterCache() error {
	d.parameterValuesMutex.Lock()
	defer d.parameterValuesMutex.Unlock()

	d.parameterCurrentValues = make(map[string]interface{})

	// 强制从数据库加载所有参数值
	for paramName := range d.votableParameters {
		// 优先从数据库读取（数据库是权威数据源）
		dbValue, err := d.state.ParameterStore.GetParameterValue(paramName)
		if err == nil {
			// 数据库有值，使用数据库值
			d.parameterCurrentValues[paramName] = dbValue
			continue
		}

		// 数据库没有值，使用配置文件默认值并保存到数据库
		if defaultValue, cfgErr := d.getConfigParameterValue(paramName); cfgErr == nil {
			d.parameterCurrentValues[paramName] = defaultValue
			saveErr := d.state.ParameterStore.SaveParameterValue(paramName, defaultValue, "config")
			_ = saveErr // 避免启动阶段刷屏日志；失败会在后续读取时暴露
			continue
		} else {
			d.logger.Error("Failed to get config value for parameter",
				"param", paramName,
				"error", cfgErr)
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
		"dpos_reward_amount": {
			Name:        "Reward Amount",
			Type:        "string",
			MinValue:    "0",
			MaxValue:    "1000000000000000000000000", // 100万VCITY
			Description: "Reward amount per epoch (wei)",
			Category:    "economic",
		},
		"dpos_voter_target_apy": {
			Name:        "Voter Target APY",
			Type:        "uint64",
			MinValue:    uint64(1),     // 0.01% 年化
			MaxValue:    uint64(10000), // 100% 年化（基点制）
			Description: "Target voter annual yield in basis points (500 = 5%). Overrides genesis voter_target_apy when set by governance.",
			Category:    "economic",
		},
		"dpos_delegate_threshold": {
			Name:        "Delegate Threshold",
			Type:        "string",
			MinValue:    "1000000000000000000",       // 1 VCITY
			MaxValue:    "1000000000000000000000000", // 100万VCITY
			Description: "Minimum staking threshold (wei)",
			Category:    "economic",
		},
		"dpos_proposal_vote_period": {
			Name:        "Proposal Vote Period",
			Type:        "uint64",
			MinValue:    uint64(100),     // 最少100个区块
			MaxValue:    uint64(1000000), // 最多100万个区块
			Description: "Proposal voting period (in blocks)",
			Category:    "governance",
		},
		// 冻结相关参数
		"min_freeze_period": {
			Name:        "Min Freeze Period",
			Type:        "uint64",
			MinValue:    uint64(86400),    // 最少1天（秒）
			MaxValue:    uint64(31536000), // 最多1年（秒）
			Description: "Minimum freeze period (seconds, 7 days default)",
			Category:    "governance",
		},
		"unfreeze_lock_period": {
			Name:        "Unfreeze Lock Period",
			Type:        "uint64",
			MinValue:    uint64(86400),    // 最少1天（秒）
			MaxValue:    uint64(31536000), // 最多1年（秒）
			Description: "Unfreeze lock period (seconds, 14 days default)",
			Category:    "governance",
		},
		// 削减相关参数
		"dpos_missed_blocks_percentage": {
			Name:        "Missed Blocks Percentage",
			Type:        "uint64",
			MinValue:    uint64(100),   // 最少1%（100基点）
			MaxValue:    uint64(20000), // 最多200%（20000基点）
			Description: "Missed blocks percentage threshold for slashing (basis points, 1000 = 10%)",
			Category:    "slashing",
		},
		"dpos_minor_offense_slash_rate": {
			Name:        "Minor Offense Slash Rate",
			Type:        "uint64",
			MinValue:    uint64(1),    // 最少0.01%（1基点）
			MaxValue:    uint64(5000), // 最多50%（5000基点）
			Description: "Slash rate for minor offense (basis points, 50 = 0.5%)",
			Category:    "slashing",
		},
		"dpos_severe_offense_slash_rate": {
			Name:        "Severe Offense Slash Rate",
			Type:        "uint64",
			MinValue:    uint64(100),   // 最少1%（100基点）
			MaxValue:    uint64(10000), // 最多100%（10000基点）
			Description: "Slash rate for severe offense (basis points, 1000 = 10%)",
			Category:    "slashing",
		},
	}
}

// getVotePeriod 获取当前投票期间长度（区块数）
func (d *DPoS) getVotePeriod() uint64 {
	// 优先从参数系统读取 dpos_proposal_vote_period（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue("dpos_proposal_vote_period"); err == nil {
		switch v := paramValue.(type) {
		case uint64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 proposal vote period", "blocks", v)
				return v
			}
		case int64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 proposal vote period", "blocks", uint64(v))
				return uint64(v)
			}
		case float64:
			if v > 0 {
				d.logger.Debug("从参数系统读取 proposal vote period", "blocks", uint64(v))
				return uint64(v)
			}
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed > 0 {
				d.logger.Debug("从参数系统读取 proposal vote period", "blocks", parsed)
				return parsed
			}
		}
	}

	// 如果参数系统没有值，从配置文件获取表决周期（YAML配置优先）
	if d.config != nil && d.config.ProposalVotePeriod > 0 {
		// 根据区块时间计算区块数
		blockTime := d.config.BlockTime.Duration
		if blockTime > 0 {
			blocks := uint64(d.config.ProposalVotePeriod / blockTime)
			d.logger.Debug("📋 计算提案表决周期区块数", "proposalVotePeriod", d.config.ProposalVotePeriod, "blockTime", blockTime, "blocks", blocks)
			return blocks
		}
	}

	// 使用默认值（1天 = 28800个区块，按3秒/区块计算）
	return 28800
}

// getValidPeriod 获取提案有效期（区块数）
func (d *DPoS) getValidPeriod() uint64 {
	// 从配置文件获取有效期（YAML配置优先）
	if d.config != nil && d.config.ProposalValidPeriod > 0 {
		// 根据区块时间计算区块数
		blockTime := d.config.BlockTime.Duration
		if blockTime > 0 {
			blocks := uint64(d.config.ProposalValidPeriod / blockTime)
			d.logger.Debug("📋 计算提案有效期区块数", "proposalValidPeriod", d.config.ProposalValidPeriod, "blockTime", blockTime, "blocks", blocks)
			return blocks
		}
	}

	// 使用默认值（7天 = 201600个区块，按3秒/区块计算）
	return 201600
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
		timeInfo = fmt.Sprintf("%s (%d blocks)", d.config.ProposalVotePeriod.String(), votePeriod)
	} else {
		// 只显示区块数
		timeInfo = fmt.Sprintf("%d blocks", votePeriod)
	}

	return map[string]interface{}{
		"blocks":       votePeriod,
		"timeInfo":     timeInfo,
		"currentBlock": currentBlock,
	}
}

// getVotingThreshold 获取当前投票通过阈值
func (d *DPoS) getVotingThreshold() uint64 {
	// 默认固定 51%，不再从参数系统读取
	return 51
}

// getConfigParameterValue 从配置文件获取参数值
func (d *DPoS) getConfigParameterValue(paramName string) (interface{}, error) {
	switch paramName {
	case "dpos_reward_amount":
		if d.config.RewardAmount != nil {
			return d.config.RewardAmount.String(), nil
		}
		return "0", nil
	case "dpos_voter_target_apy":
		if d.config != nil && d.config.VoterTargetAPYBps > 0 {
			return d.config.VoterTargetAPYBps, nil
		}
		return uint64(500), nil
	case "dpos_delegate_threshold":
		// 直接读取配置，避免参数缓存初始化时递归持锁
		if d.config != nil && d.config.DPoSDelegateThreshold != nil {
			return d.config.DPoSDelegateThreshold.String(), nil
		}
		// 默认1000 VCITY
		return "1000000000000000000000", nil
	case "block_time_s":
		return d.config.BlockTime.Duration.Seconds(), nil
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
		return d.getConfigParameterValue("dpos_proposal_vote_period")
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
		// 削减参数：漏块率阈值（从配置读取）
		if val := d.getConfigUint64("dpos_missed_blocks_percentage", "missed_blocks_percentage"); val > 0 {
			return val, nil
		}
		return nil, fmt.Errorf("dpos_missed_blocks_percentage 未配置或无效")
	case "dpos_minor_offense_slash_rate":
		// 削减参数：轻度违规削减率（从配置读取）
		if val := d.getConfigUint64("dpos_minor_offense_slash_rate", "minor_offense_slash_rate"); val > 0 {
			return val, nil
		}
		// 默认值：50基点 = 0.5%
		return uint64(50), nil
	case "dpos_severe_offense_slash_rate":
		// 削减参数：严重违规削减率（从配置读取）
		if val := d.getConfigUint64("dpos_severe_offense_slash_rate", "severe_offense_slash_rate"); val > 0 {
			return val, nil
		}
		// 默认值：1000基点 = 10%
		return uint64(1000), nil
	default:
		return nil, fmt.Errorf("unknown parameter: %s", paramName)
	}
}
