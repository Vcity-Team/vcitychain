package dpos

import "strconv"

// getEffectiveVoterPoolStakeWeightActivationEpoch 返回质押池按 SR 质押权重分配的激活 epoch；0 表示未启用（仍按出块数分配）。
func (d *DPoS) getEffectiveVoterPoolStakeWeightActivationEpoch() uint64 {
	if v, err := d.getCurrentParameterValue("dpos_voter_pool_stake_weight_activation_epoch"); err == nil {
		switch t := v.(type) {
		case uint64:
			return t
		case int:
			if t >= 0 {
				return uint64(t)
			}
		case int64:
			if t >= 0 {
				return uint64(t)
			}
		case float64:
			if t >= 0 {
				return uint64(t)
			}
		case string:
			if u, err := strconv.ParseUint(t, 10, 64); err == nil {
				return u
			}
		}
	}
	if d.config != nil {
		return d.config.VoterPoolStakeWeightActivationEpoch
	}
	return 0
}

// GetVoterPoolStakeWeightActivationEpoch 返回计划启用质押权重分配的 epoch（0=尚未配置，沿用出块比例分配）。
func (d *DPoS) GetVoterPoolStakeWeightActivationEpoch() uint64 {
	return d.getEffectiveVoterPoolStakeWeightActivationEpoch()
}

// IsVoterPoolStakeWeightSplitActive 判断当前 epoch 是否已启用质押权重分配。
func (d *DPoS) IsVoterPoolStakeWeightSplitActive() bool {
	return d.isVoterPoolStakeWeightSplitAtEpoch(d.GetCurrentEpochNumber())
}

// isVoterPoolStakeWeightSplitAtEpoch 判断指定 epoch 是否使用 SR 质押权重 × 完成度分配 voter 池。
func (d *DPoS) isVoterPoolStakeWeightSplitAtEpoch(epochNumber uint64) bool {
	activation := d.getEffectiveVoterPoolStakeWeightActivationEpoch()
	if activation == 0 {
		return false
	}
	return epochNumber >= activation
}

// logVoterPoolStakeWeightActivationIfNeeded 在首次到达激活 epoch 的分发时打印显著日志。
func (d *DPoS) logVoterPoolStakeWeightActivationIfNeeded(epochNumber uint64) {
	activation := d.getEffectiveVoterPoolStakeWeightActivationEpoch()
	if activation == 0 || epochNumber != activation {
		return
	}
	d.logger.Info("🎯🎯🎯 ========== 质押池 SR 分配规则已激活（按质押权重×完成度） ==========",
		"epoch", epochNumber,
		"activationEpoch", activation,
		"governanceParam", "dpos_voter_pool_stake_weight_activation_epoch",
		"previousRule", "voter_pool_split_by_block_count",
		"newRule", "voter_pool_split_by_stake_weight_times_completion",
		"note", "从本 epoch 起 SR 间 voter 池按委托质押权重分配，不再按出块数均分")
}
