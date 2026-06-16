package dpos

import (
	"fmt"
	"strconv"

	"github.com/Vcity-Team/vcitychain/types"
)

// parseAddressParameterValue 解析治理参数中的地址字符串。
func parseAddressParameterValue(value interface{}) (types.Address, error) {
	var addrStr string
	switch v := value.(type) {
	case string:
		addrStr = v
	case types.Address:
		return v, nil
	default:
		return types.ZeroAddress, fmt.Errorf("unsupported address value type: %T", value)
	}
	if err := types.IsValidAddress(addrStr); err != nil {
		return types.ZeroAddress, fmt.Errorf("invalid address: %w", err)
	}
	addr := types.StringToAddress(addrStr)
	if addr == types.ZeroAddress {
		return types.ZeroAddress, fmt.Errorf("address cannot be zero")
	}
	return addr, nil
}

// getGovernedRewardAccount 返回治理参数中的奖励账户地址（未配置时为零地址）。
func (d *DPoS) getGovernedRewardAccount() types.Address {
	if v, err := d.getCurrentParameterValue("dpos_reward_distribution_account"); err == nil {
		if addr, err := parseAddressParameterValue(v); err == nil {
			return addr
		}
	}
	return types.ZeroAddress
}

// getEffectiveRewardAccountActivationEpoch 返回奖励账户切换激活 epoch；0 表示仍使用配置账户。
func (d *DPoS) getEffectiveRewardAccountActivationEpoch() uint64 {
	if v, err := d.getCurrentParameterValue("dpos_reward_distribution_activation_epoch"); err == nil {
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
		return d.config.RewardAccountActivationEpoch
	}
	return 0
}

// GetRewardAccountActivationEpoch 返回计划切换奖励账户的 epoch（0=仍用配置账户）。
func (d *DPoS) GetRewardAccountActivationEpoch() uint64 {
	return d.getEffectiveRewardAccountActivationEpoch()
}

// GetEffectiveRewardAccountForEpoch 返回指定 epoch 结算时应使用的奖励账户。
// 激活前：config.RewardAccount（genesis/server 配置）；激活后：治理参数 dpos_reward_distribution_account。
func (d *DPoS) GetEffectiveRewardAccountForEpoch(epochNumber uint64) types.Address {
	if d.config != nil && d.config.RewardAccount != types.ZeroAddress {
		activation := d.getEffectiveRewardAccountActivationEpoch()
		if activation > 0 && epochNumber >= activation {
			if governed := d.getGovernedRewardAccount(); governed != types.ZeroAddress {
				return governed
			}
		}
		return d.config.RewardAccount
	}
	if governed := d.getGovernedRewardAccount(); governed != types.ZeroAddress {
		return governed
	}
	return types.ZeroAddress
}

// GetEffectiveRewardAccount 返回当前 epoch 应使用的奖励账户。
func (d *DPoS) GetEffectiveRewardAccount() types.Address {
	return d.GetEffectiveRewardAccountForEpoch(d.GetCurrentEpochNumber())
}
