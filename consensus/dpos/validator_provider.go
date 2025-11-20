package dpos

import (
	"fmt"

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
func (vp *ValidatorProvider) GetValidatorsForDetection(epochInfo EpochInfo) (validator.AccountSet, error) {
	// 🔧 修复：应该检测上一个epoch的验证者故障，所以获取要检测的epoch（EpochToCheck）的验证者集合
	validatorSource := "unknown"
	epochNumberForValidators := epochInfo.EpochToCheckNumber // 使用要检测的epoch，而不是当前epoch
	var epochValidators validator.AccountSet
	var err error

	// 🔧 修复：使用 = 而不是 :=，确保使用外部声明的 epochValidators 变量
	if epochValidators, err = vp.dposInstance.getValidatorsForEpoch(epochNumberForValidators); err == nil && len(epochValidators) > 0 {
		validatorSource = "ExtraData/StakeStore"
		vp.logger.Info("✅ 从ExtraData获取要检测epoch的验证者集合",
			"epoch", epochNumberForValidators,
			"count", len(epochValidators))
	} else if vp.dposInstance.runtime != nil && vp.dposInstance.runtime.delegates != nil && len(vp.dposInstance.runtime.delegates) > 0 {
		// 备用方案：使用 runtime.delegates（最实时）
		epochValidators = vp.dposInstance.runtime.delegates.Copy()
		validatorSource = "runtime.delegates"
		vp.logger.Info("✅ 使用runtime.delegates作为要检测epoch的验证者集合",
			"epoch", epochNumberForValidators,
			"count", len(epochValidators))
	} else if len(vp.dposInstance.delegates) > 0 {
		// 最后使用 d.delegates
		epochValidators = vp.dposInstance.delegates.Copy()
		validatorSource = "d.delegates"
		vp.logger.Info("✅ 使用d.delegates作为要检测epoch的验证者集合",
			"epoch", epochNumberForValidators,
			"count", len(epochValidators))
	} else {
		vp.logger.Error("❌ 无法获取要检测epoch的验证者集合",
			"epoch", epochNumberForValidators)
		return nil, fmt.Errorf("no validators available for epoch %d", epochNumberForValidators)
	}

	vp.logger.Info("ℹ️ 要检测epoch的验证者集合来源",
		"epoch", epochNumberForValidators,
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
