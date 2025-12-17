package dpos

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// ValidatorProvider 负责提供验证者集合
type ValidatorProvider struct {
	dposInstance *DPoS
	logger       hclog.Logger
}

// NewValidatorProvider 创建验证者提供器
func NewValidatorProvider(dposInstance *DPoS, logger hclog.Logger) *ValidatorProvider {
	return &ValidatorProvider{
		dposInstance: dposInstance,
		logger:       logger.Named("validator-provider"),
	}
}

// EpochInfo epoch信息
type EpochInfo struct {
	CurrentEpoch        uint64
	CurrentEpochNumber  uint64
	PreviousEpochNumber uint64
	EpochToCheck        uint64
	EpochToCheckNumber  uint64
}

// GetValidatorsForDetection 获取用于故障检测的验证者集合
// 🔧 修复：优先使用当前活跃验证者（runtime.delegates），而不是数据库中的历史记录
// 原因：故障检测的目的是判断"当前应该出块的验证者"是否正常
// 被剔除的验证者不应该被检测（它们不需要出块）
func (vp *ValidatorProvider) GetValidatorsForDetection(epochInfo EpochInfo) (validator.AccountSet, error) {
	var epochValidators validator.AccountSet
	validatorSource := "unknown"

	// 🔧 优先使用当前活跃验证者（最准确）
	if vp.dposInstance.runtime != nil && vp.dposInstance.runtime.delegates != nil && len(vp.dposInstance.runtime.delegates) > 0 {
		epochValidators = vp.dposInstance.runtime.delegates.Copy()
		validatorSource = "runtime.delegates"
	} else if len(vp.dposInstance.delegates) > 0 {
		epochValidators = vp.dposInstance.delegates.Copy()
		validatorSource = "d.delegates"
	} else {
		// 备用方案：从数据库获取验证者 A，与配置中的验证者数量 B 比较，返回较小者
		epochNumberForValidators := epochInfo.EpochToCheckNumber
		dbValidators, err := vp.dposInstance.getValidatorsForEpoch(epochNumberForValidators)
		if err != nil || len(dbValidators) == 0 {
			vp.logger.Error("❌ 无法获取要检测epoch的验证者集合",
				"epoch", epochNumberForValidators,
				"error", err)
			return nil, fmt.Errorf("no validators available for epoch %d", epochNumberForValidators)
		}

		// 获取配置中的最大验证者数量
		maxValidators := int(vp.dposInstance.config.DPoSValidatorsCount)

		// 比较 A 和 B，返回较小者
		if maxValidators > 0 && len(dbValidators) > maxValidators {
			// A > B，截取前 B 个（按投票权排序后截取）
			// 先按投票权倒序排序（权重相同时按地址字节升序排序）
			sort.Slice(dbValidators, func(i, j int) bool {
				votingPowerCmp := dbValidators[i].VotingPower.Cmp(dbValidators[j].VotingPower)
				if votingPowerCmp != 0 {
					return votingPowerCmp > 0
				}
				return bytes.Compare(dbValidators[i].Address[:], dbValidators[j].Address[:]) < 0
			})
			epochValidators = dbValidators[:maxValidators]
			validatorSource = fmt.Sprintf("database(truncated:%d->%d)", len(dbValidators), maxValidators)
			vp.logger.Info("📋 备用方案：从数据库获取验证者并截取",
				"dbCount", len(dbValidators),
				"configCount", maxValidators,
				"finalCount", len(epochValidators))
		} else {
			// A <= B，直接返回 A
			epochValidators = dbValidators
			validatorSource = "database"
		}
	}

	vp.logger.Debug("📋 故障检测使用的验证者集合",
		"source", validatorSource,
		"count", len(epochValidators))

	return epochValidators, nil
}

// GetPreviousEpochValidators 获取上一个epoch的验证者集合
func (vp *ValidatorProvider) GetPreviousEpochValidators(previousEpochNumber uint64) validator.AccountSet {
	if previousEpochNumber == 0 {
		return validator.AccountSet{}
	}

	prevValidators, err := vp.dposInstance.getValidatorsForEpoch(previousEpochNumber)
	if err == nil && len(prevValidators) > 0 {
		vp.logger.Info("✅ 获取到上一个epoch验证者集合",
			"previousEpoch", previousEpochNumber,
			"validatorsCount", len(prevValidators))
		return prevValidators
	}

	vp.logger.Warn("⚠️ 无法获取上一个epoch验证者集合，将无法判断新加入的验证者",
		"previousEpoch", previousEpochNumber,
		"error", err)
	vp.logger.Info("ℹ️ 上一个epoch验证者集合缺失，所有验证者将按既有节点处理",
		"previousEpoch", previousEpochNumber)

	return validator.AccountSet{}
}

// BuildPreviousEpochValidatorMap 构建上一个epoch验证者地址映射，用于快速查找
func (vp *ValidatorProvider) BuildPreviousEpochValidatorMap(previousEpochValidators validator.AccountSet) map[types.Address]bool {
	previousEpochValidatorMap := make(map[types.Address]bool)
	for _, v := range previousEpochValidators {
		previousEpochValidatorMap[v.Address] = true
	}
	return previousEpochValidatorMap
}
