package dpos

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
)

func parseDurationAllowDays(input string) (time.Duration, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	if duration, err := time.ParseDuration(input); err == nil {
		return duration, nil
	}

	// 支持形如 "21d" 的天单位（可带小数）
	if strings.ContainsAny(input, "dD") {
		lower := strings.ToLower(input)
		var value float64
		var suffix string
		if _, err := fmt.Sscanf(lower, "%f%s", &value, &suffix); err == nil && strings.HasPrefix(suffix, "d") {
			remaining := strings.TrimPrefix(suffix, "d")
			hours := value * 24
			normalized := fmt.Sprintf("%.0fh%s", hours, remaining)
			return time.ParseDuration(normalized)
		}
		// 简单处理：去掉最后的 d
		if strings.HasSuffix(lower, "d") {
			numberPart := strings.TrimSuffix(lower, "d")
			if numberPart == "" {
				return 0, fmt.Errorf("invalid duration: %s", input)
			}
			if v, err := strconv.ParseFloat(numberPart, 64); err == nil {
				hours := v * 24
				return time.ParseDuration(fmt.Sprintf("%.0fh", hours))
			}
			return 0, fmt.Errorf("invalid duration: %s", input)
		}
	}

	return 0, fmt.Errorf("unsupported duration format: %s", input)
}

func toUint64(value interface{}) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// DefaultDPoSConfig 返回默认配置
func DefaultDPoSConfig() *DPoSConfig {
	return &DPoSConfig{
		DelegateCount:             21,
		BlockTime:                 common.Duration{Duration: 15 * time.Second},
		RoundTime:                 common.Duration{Duration: 30 * time.Second},
		MinVotingPower:            big.NewInt(1000000000000000000), // 1 token
		VoteLockTime:              86400,                           // 24 hours
		RewardRatio:               100,                             // 1%
		ProposalVotePeriod:        24 * time.Hour,                  // 默认提案表决周期 24小时
		ProposalValidPeriod:       7 * 24 * time.Hour,              // 默认提案有效期 7天
		MinFreezePeriod:           604800,                          // 默认最小冻结期 7天（秒）
		UnfreezeLockPeriod:        1209600,                         // 默认解冻锁定期 14天（秒）
		CommissionRateDefault:     1000,                            // 默认佣金率 10%
		CommissionEffectivePeriod: 21 * 24 * time.Hour,             // 默认佣金生效周期 21天
	}
}

// Validate 验证配置
func (c *DPoSConfig) Validate() error {
	if c.BlockTime.Duration <= 0 {
		return fmt.Errorf("block_time must be positive")
	}
	if c.RoundTime.Duration <= 0 {
		return fmt.Errorf("round_time must be positive")
	}
	if c.DelegateCount == 0 {
		return fmt.Errorf("delegate_count must be positive")
	}
	// 🆕 使用统一的零值检查函数
	if isNonPositive(c.MinVotingPower) {
		return fmt.Errorf("min_voting_power must be positive")
	}

	// 验证经济系统配置
	if c.RewardAccount == types.ZeroAddress {
		return fmt.Errorf("reward_account is required")
	}
	// 🆕 使用统一的零值检查函数
	if isNonPositive(c.RewardAmount) {
		return fmt.Errorf("reward_amount must be positive")
	}
	return nil
}

// GetConfigSummary 获取配置摘要
func (c *DPoSConfig) GetConfigSummary() map[string]interface{} {
	return map[string]interface{}{
		"delegate_count":   c.DelegateCount,
		"block_time":       c.BlockTime.String(),
		"round_time":       c.RoundTime.String(),
		"min_voting_power": c.MinVotingPower.String(),
		"vote_lock_time":   c.VoteLockTime,
		"reward_ratio":     c.RewardRatio,
		"epoch_duration":   c.EpochDuration.String(),
		"reward_account":   c.RewardAccount.String(),
		"reward_amount":    c.RewardAmount.String(),
	}
}
