package dpos

import "strconv"

// getEffectiveCommissionRemovalActivationEpoch 返回关闭佣金的激活 epoch；0 表示尚未关闭（沿用旧佣金逻辑）。
func (d *DPoS) getEffectiveCommissionRemovalActivationEpoch() uint64 {
	if v, err := d.getCurrentParameterValue("dpos_commission_removal_activation_epoch"); err == nil {
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
		return d.config.CommissionRemovalActivationEpoch
	}
	return 0
}

// GetCommissionRemovalActivationEpoch 返回计划关闭佣金的 epoch（0=尚未配置）。
func (d *DPoS) GetCommissionRemovalActivationEpoch() uint64 {
	return d.getEffectiveCommissionRemovalActivationEpoch()
}

// IsCommissionRemoved 判断当前 epoch 是否已关闭佣金（质押池全给投票者）。
func (d *DPoS) IsCommissionRemoved() bool {
	return d.isCommissionRemovedAtEpoch(d.GetCurrentEpochNumber())
}

// isCommissionRemovedAtEpoch 判断指定 epoch 是否已关闭佣金。
func (d *DPoS) isCommissionRemovedAtEpoch(epochNumber uint64) bool {
	activation := d.getEffectiveCommissionRemovalActivationEpoch()
	if activation == 0 {
		return false
	}
	return epochNumber >= activation
}
