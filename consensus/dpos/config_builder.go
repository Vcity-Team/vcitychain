package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// ConfigBuilder 配置构建器，用于构建 DPoSConfig
// 注意：完全信任 Server 层的解析结果，不再使用 ConfigParser
type ConfigBuilder struct {
	config *DPoSConfig
	logger hclog.Logger
	raw    map[string]interface{} // Server 层解析后的配置（已转换类型）
}

// NewConfigBuilder 创建配置构建器
func NewConfigBuilder(rawConfig map[string]interface{}, logger hclog.Logger) *ConfigBuilder {
	return &ConfigBuilder{
		config: &DPoSConfig{},
		logger: logger,
		raw:    rawConfig,
	}
}

// Build 构建并返回配置
func (b *ConfigBuilder) Build() *DPoSConfig {
	return b.config
}

// ParseBasicConfig 解析基础配置
// 注意：完全信任 Server 层的解析结果
func (b *ConfigBuilder) ParseBasicConfig() *ConfigBuilder {
	// 解析共识切换高度（Server 层已转换为 float64）
	if val, exists := b.raw["consensusSwitchHeight"]; exists {
		if height, ok := val.(float64); ok {
			b.config.ConsensusSwitchHeight = uint64(height)
			b.logger.Debug("🔄 设置共识切换高度", "height", uint64(height))
		} else {
			b.logger.Warn("🔄 consensusSwitchHeight类型不是float64", "type", fmt.Sprintf("%T", val))
		}
	}

	// 解析验证者数量（Server 层已转换为 float64）
	if val, exists := b.raw["dposValidatorsCount"]; exists {
		if count, ok := val.(float64); ok {
			b.config.DelegateCount = uint64(count)
			b.config.DPoSValidatorsCount = uint64(count)
			b.logger.Info("👥 设置验证者数量", "count", uint64(count))
		} else {
			b.logger.Warn("👥 dposValidatorsCount类型不是float64", "type", fmt.Sprintf("%T", val))
		}
	} else {
		b.logger.Warn("👥 未找到 dposValidatorsCount 配置")
	}

	// 解析区块时间配置（Server 层已转换为 string）
	if blockTimeStr, exists := b.raw["blockTime"]; exists {
		if blockTime, ok := blockTimeStr.(string); ok {
			if duration, err := time.ParseDuration(blockTime); err == nil {
				b.config.BlockTime = common.Duration{Duration: duration}
				b.logger.Info("⏰ 设置区块时间", "duration", duration.String())
			} else {
				b.logger.Warn("⏰ blockTime解析失败", "value", blockTime, "error", err)
			}
		} else {
			b.logger.Warn("⏰ blockTime类型不是string", "type", fmt.Sprintf("%T", blockTimeStr))
		}
	} else {
		b.logger.Warn("⏰ 未找到blockTime配置")
	}

	return b
}

// ParseCommissionConfig 解析佣金相关配置
// 注意：完全信任 Server 层的解析结果
func (b *ConfigBuilder) ParseCommissionConfig() *ConfigBuilder {
	// 解析默认佣金率（Server 层已转换为 float64）
	if val, exists := b.raw["dpos_commission_ratio"]; exists {
		if ratio, ok := val.(float64); ok && ratio > 0 {
			b.config.CommissionRateDefault = uint64(ratio)
			b.logger.Info("💼 设置默认佣金率", "ratio", uint64(ratio))
		} else {
			b.logger.Warn("💼 dpos_commission_ratio类型不是float64或值无效", "type", fmt.Sprintf("%T", val), "value", val)
		}
	}

	// 解析佣金生效周期（Server 层已转换为 time.Duration）
	if val, exists := b.raw["commissionEffectivePeriod"]; exists {
		if duration, ok := val.(time.Duration); ok && duration > 0 {
			b.config.CommissionEffectivePeriod = duration
			b.logger.Info("⏳ 设置佣金生效周期", "duration", duration.String())
		} else {
			b.logger.Warn("⏳ commissionEffectivePeriod类型不是time.Duration", "type", fmt.Sprintf("%T", val))
		}
	}

	return b
}

// ParseSlashingConfig 解析削减相关配置
// 注意：这些配置在 Server 层可能没有解析，直接从 raw 中读取（如果存在）
func (b *ConfigBuilder) ParseSlashingConfig() *ConfigBuilder {
	// 解析漏块率阈值（如果 Server 层有设置）
	if val, exists := b.raw["dpos_missed_blocks_percentage"]; exists {
		if percentage, ok := val.(float64); ok {
			b.logger.Info("🔨 从配置文件读取漏块率阈值", "percentage", uint64(percentage), "基点")
		} else if percentage, ok := val.(uint64); ok {
			b.logger.Info("🔨 从配置文件读取漏块率阈值", "percentage", percentage, "基点")
		}
	} else {
		b.logger.Warn("🔨 未找到dpos_missed_blocks_percentage配置，将使用默认值1000 (10%)")
	}

	// 解析轻度违规削减率（如果 Server 层有设置）
	if val, exists := b.raw["dpos_minor_offense_slash_rate"]; exists {
		if rate, ok := val.(float64); ok {
			b.logger.Info("🔨 从配置文件读取轻度违规削减率", "rate", uint64(rate), "基点")
		} else if rate, ok := val.(uint64); ok {
			b.logger.Info("🔨 从配置文件读取轻度违规削减率", "rate", rate, "基点")
		}
	} else {
		b.logger.Warn("🔨 未找到dpos_minor_offense_slash_rate配置，将使用默认值50 (0.5%)")
	}

	// 解析严重违规削减率（如果 Server 层有设置）
	if val, exists := b.raw["dpos_severe_offense_slash_rate"]; exists {
		if rate, ok := val.(float64); ok {
			b.logger.Info("🔨 从配置文件读取严重违规削减率", "rate", uint64(rate), "基点")
		} else if rate, ok := val.(uint64); ok {
			b.logger.Info("🔨 从配置文件读取严重违规削减率", "rate", rate, "基点")
		}
	} else {
		b.logger.Warn("🔨 未找到dpos_severe_offense_slash_rate配置，将使用默认值1000 (10%)")
	}

	return b
}

// ParseEpochConfig 解析 Epoch 和奖励相关配置
// 注意：完全信任 Server 层的解析结果
func (b *ConfigBuilder) ParseEpochConfig() *ConfigBuilder {
	// 解析 Epoch 持续时间（Server 层已转换为 time.Duration）
	if val, exists := b.raw["epochDuration"]; exists {
		if duration, ok := val.(time.Duration); ok {
			b.config.EpochDuration = duration
			b.logger.Info("⏰ 设置 Epoch 持续时间", "duration", duration.String())
		} else {
			b.logger.Warn("⏰ epochDuration类型不是time.Duration", "type", fmt.Sprintf("%T", val))
		}
	} else {
		b.logger.Warn("⏰ 未找到epochDuration配置")
	}

	// 解析奖励账户（Server 层已转换为 types.Address）
	if val, exists := b.raw["rewardAccount"]; exists {
		if account, ok := val.(types.Address); ok {
			b.config.RewardAccount = account
			b.logger.Info("💰 设置奖励账户", "account", account.String())
		} else {
			b.logger.Warn("💰 rewardAccount类型不是types.Address", "type", fmt.Sprintf("%T", val))
		}
	} else {
		b.logger.Warn("💰 未找到rewardAccount配置")
	}

	// 解析奖励金额（Server 层已转换为 *big.Int）
	if val, exists := b.raw["rewardAmount"]; exists {
		if amount, ok := val.(*big.Int); ok && amount != nil {
			b.config.RewardAmount = amount
			b.logger.Info("💰 设置奖励金额", "amount", amount.String())
		} else {
			b.logger.Warn("💰 rewardAmount类型不是*big.Int", "type", fmt.Sprintf("%T", val))
		}
	} else {
		b.logger.Warn("💰 未找到rewardAmount配置")
	}

	return b
}

// ParseProposalConfig 解析提案相关配置（统一处理，消除重复代码）
func (b *ConfigBuilder) ParseProposalConfig() *ConfigBuilder {
	b.logger.Info("📋 检查Config中的所有键", "keys", func() []string {
		keys := make([]string, 0, len(b.raw))
		for k := range b.raw {
			keys = append(keys, k)
		}
		return keys
	}())

	// 统一处理提案周期配置（消除重复代码）
	b.parseProposalPeriod("proposalVotePeriod", &b.config.ProposalVotePeriod, "提案表决周期")
	b.parseProposalPeriod("proposalValidPeriod", &b.config.ProposalValidPeriod, "提案有效期")

	return b
}

// parseProposalPeriod 统一解析提案周期配置
// 注意：完全信任 Server 层的解析结果，Server 层已转换为 time.Duration
func (b *ConfigBuilder) parseProposalPeriod(key string, target *time.Duration, description string) {
	if val, exists := b.raw[key]; exists {
		// Server 层应该已经解析为 time.Duration，直接使用
		if period, ok := val.(time.Duration); ok {
			*target = period
			b.logger.Info("📋 ✅ 使用server层解析的"+description, "period", period.String(), "seconds", period.Seconds())
		} else {
			// 完全信任 Server 层，如果类型不匹配只记录警告
			b.logger.Warn("📋 ⚠️ "+key+"类型不是time.Duration，期望Server层已转换",
				"type", fmt.Sprintf("%T", val),
				"value", val)
		}
	} else {
		// 如果不存在，说明 Server 层没有设置，将使用默认值（在 SetDefaults 中设置）
		b.logger.Warn("📋 ❌ 未找到" + key + "配置，将使用默认值")
	}
}

// ParseFreezeConfig 解析冻结相关配置
// 注意：完全信任 Server 层的解析结果
func (b *ConfigBuilder) ParseFreezeConfig() *ConfigBuilder {
	// 解析冻结相关配置（Server 层已转换为 uint64）
	if val, exists := b.raw["dpos_min_freeze_period"]; exists {
		if period, ok := val.(uint64); ok {
			b.config.MinFreezePeriod = period
			b.logger.Info("❄️ 设置最小冻结期", "period", period, "seconds", period)
		} else {
			b.logger.Warn("❄️ dpos_min_freeze_period类型不是uint64", "type", fmt.Sprintf("%T", val))
			// 使用默认值
			b.config.MinFreezePeriod = 604800
		}
	} else {
		b.logger.Warn("❄️ 未找到dpos_min_freeze_period配置，使用默认值604800秒（7天）")
		b.config.MinFreezePeriod = 604800
	}

	// 解析解冻锁定期（Server 层已转换为 uint64）
	if val, exists := b.raw["dpos_unfreeze_lock_period"]; exists {
		if period, ok := val.(uint64); ok {
			b.config.UnfreezeLockPeriod = period
			b.logger.Info("🔓 设置解冻锁定期", "period", period, "seconds", period)
		} else {
			b.logger.Warn("🔓 dpos_unfreeze_lock_period类型不是uint64", "type", fmt.Sprintf("%T", val))
			// 使用默认值
			b.config.UnfreezeLockPeriod = 1209600
		}
	} else {
		b.logger.Warn("🔓 未找到dpos_unfreeze_lock_period配置，使用默认值1209600秒（14天）")
		b.config.UnfreezeLockPeriod = 1209600
	}

	return b
}

// SetDefaults 设置默认值
func (b *ConfigBuilder) SetDefaults() *ConfigBuilder {
	if b.config.EpochDuration == 0 {
		b.logger.Warn("⚠️ epochDuration为0，设置默认值86400秒")
		b.config.EpochDuration = 86400 * time.Second
	}

	if b.config.RewardAmount == nil {
		b.logger.Warn("⚠️ rewardAmount为nil，设置默认值")
		b.config.RewardAmount = DefaultVotingPower() // 1000 VCITY
	}

	if b.config.CommissionRateDefault == 0 {
		b.logger.Warn("💼 commissionRateDefault为0，设置默认值10% (1000 基点)")
		b.config.CommissionRateDefault = 1000
	}

	if b.config.CommissionEffectivePeriod == 0 {
		defaultCommissionEffective := 21 * 24 * time.Hour
		b.logger.Warn("⏳ commissionEffectivePeriod为0，设置默认值21天", "duration", defaultCommissionEffective.String())
		b.config.CommissionEffectivePeriod = defaultCommissionEffective
	}

	if b.config.ProposalVotePeriod == 0 {
		b.logger.Warn("⚠️ proposalVotePeriod为0，设置默认值24小时")
		b.config.ProposalVotePeriod = 24 * time.Hour
	}
	if b.config.ProposalValidPeriod == 0 {
		b.logger.Warn("⚠️ proposalValidPeriod为0，设置默认值7天")
		b.config.ProposalValidPeriod = 7 * 24 * time.Hour
	}

	if b.config.BlockTime.Duration == 0 {
		b.config.BlockTime = common.Duration{Duration: 3 * time.Second}
		b.logger.Info("⏰ 使用默认DPoS区块时间3秒", "duration", b.config.BlockTime.Duration.String())
	} else {
		b.logger.Info("⏰ 使用配置文件中的DPoS区块时间", "duration", b.config.BlockTime.Duration.String())
	}

	return b
}
