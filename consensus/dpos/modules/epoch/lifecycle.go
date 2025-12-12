package epoch

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// LifecycleDependencies 定义Epoch生命周期模块所需依赖
type LifecycleDependencies struct {
	Logger hclog.Logger

	ResolveEpochNumber func(blockNumber uint64) uint64

	LoadScheduledRecoveries       func(epochNumber uint64) []core.RecoveryProposalInfo
	CheckRecoveryProposal         func(validatorAddress types.Address, currentEpoch uint64) bool
	ClearValidatorFaultStatus     func(address types.Address, proposalID string) error
	ClearMemoryFaultStatus        func(address types.Address)
	ReloadValidatorsAfterRecovery func() error
	MarkProposalApplied           func(proposalID string, appliedBlock uint64) error

	TriggerEpochSwitch         func(nextBlockNumber uint64)
	BuildPendingHeader         func(parentHash types.Hash, nextBlockNumber uint64) *types.Header
	SetPendingEpochEndHeader   func(header *types.Header)
	ClearPendingEpochEndHeader func(blockNumber uint64)

	DetectFaults            func(blockNumber uint64) ([]core.FaultFlagInfo, error)
	SaveFaultStatus         func(flag core.FaultFlagInfo) error
	UpdateMemoryFaultStatus func(flag core.FaultFlagInfo)
	SaveCurrentEpoch        func(epochNumber uint64, blockNumber uint64) error
	UpdateBlockProducers    func(flags []core.FaultFlagInfo) error

	CalculateNextEpochValidators func(blockNumber uint64) (validator.AccountSet, error)

	UpdateValidatorCaches func(validators validator.AccountSet)
}

type lifecycleManager struct {
	deps   LifecycleDependencies
	logger hclog.Logger
}

// NewLifecycleManager 创建Epoch生命周期管理器
func NewLifecycleManager(deps LifecycleDependencies) core.EpochLifecycleManager {
	logger := deps.Logger
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	return &lifecycleManager{
		deps:   deps,
		logger: logger,
	}
}

// ProcessBoundary 在epoch结束时执行恢复提案、故障检测和下个epoch验证者准备
func (m *lifecycleManager) ProcessBoundary(ctx core.EpochBoundaryContext) (core.EpochBoundaryResult, error) {
	result := core.EpochBoundaryResult{}

	if ctx.NextBlockNumber == 0 {
		return result, fmt.Errorf("next block number is zero")
	}

	// 🔧 修复：在epoch结束区块时，应该查询当前epoch（即将结束的epoch）的提案
	// NextBlockNumber 是下一个区块号，当前epoch结束区块是 NextBlockNumber - 1
	// 例如：NextBlockNumber=7418（下一个区块），当前epoch结束区块=7417（epoch 3的最后一个区块）
	// 应该使用 NextBlockNumber - 1 来获取当前epoch，而不是 NextBlockNumber
	currentEpoch := uint64(0)
	if m.deps.ResolveEpochNumber != nil && ctx.NextBlockNumber > 0 {
		// 使用 NextBlockNumber - 1 来获取当前epoch（即将结束的epoch）
		// 因为 NextBlockNumber 是下一个区块号，NextBlockNumber - 1 是当前epoch结束区块
		currentEpoch = m.deps.ResolveEpochNumber(ctx.NextBlockNumber - 1)
		m.logger.Info("🔍 [ProcessBoundary] 计算当前epoch",
			"nextBlockNumber", ctx.NextBlockNumber,
			"currentBlockNumber", ctx.NextBlockNumber-1,
			"currentEpoch", currentEpoch)
	}

	// 🔧 调整顺序：先进行故障检测（此时恢复提案还是未应用状态，CheckRecoveryProposal可以找到），
	// 然后再应用恢复提案并标记为已应用
	faultFlags, err := m.runFaultDetection(ctx, currentEpoch)
	if err != nil {
		return result, err
	}

	m.applyScheduledRecoveries(currentEpoch, ctx.NextBlockNumber)

	nextValidators, err := m.prepareNextEpochValidators(ctx.NextBlockNumber)
	if err != nil {
		return result, err
	}

	result.FaultFlags = faultFlags
	result.NextEpochValidators = nextValidators

	return result, nil
}

func (m *lifecycleManager) applyScheduledRecoveries(epochNumber, blockNumber uint64) {
	if m.deps.LoadScheduledRecoveries == nil {
		return
	}

	proposals := m.deps.LoadScheduledRecoveries(epochNumber)
	if len(proposals) == 0 {
		return
	}

	seen := make(map[string]struct{})
	for _, prop := range proposals {
		if prop.ID == "" {
			continue
		}
		if _, exists := seen[prop.ID]; exists {
			continue
		}
		seen[prop.ID] = struct{}{}

		if prop.ProposalType != core.ProposalTypeValidatorRecovery {
			continue
		}
		if !prop.Schedule.Scheduled ||
			prop.Schedule.EffectiveEpoch != epochNumber ||
			prop.Schedule.Applied {
			continue
		}

		if m.deps.ClearValidatorFaultStatus != nil {
			if err := m.deps.ClearValidatorFaultStatus(prop.ValidatorAddress, prop.ID); err != nil {
				m.logger.Error("failed to clear validator fault status",
					"proposalID", prop.ID,
					"validator", prop.ValidatorAddress.String(),
					"error", err)
				continue
			}
		}

		if m.deps.ClearMemoryFaultStatus != nil {
			m.deps.ClearMemoryFaultStatus(prop.ValidatorAddress)
		}

		if m.deps.ReloadValidatorsAfterRecovery != nil {
			if err := m.deps.ReloadValidatorsAfterRecovery(); err != nil {
				m.logger.Error("failed to reload validators after recovery",
					"proposalID", prop.ID,
					"validator", prop.ValidatorAddress.String(),
					"error", err)
			}
		}

		if m.deps.MarkProposalApplied != nil {
			if err := m.deps.MarkProposalApplied(prop.ID, blockNumber); err != nil {
				m.logger.Warn("failed to mark recovery proposal as applied",
					"proposalID", prop.ID,
					"error", err)
			}
		}

		m.logger.Info("recovery proposal applied",
			"proposalID", prop.ID,
			"validator", prop.ValidatorAddress.String(),
			"epoch", epochNumber)
	}
}

func (m *lifecycleManager) runFaultDetection(ctx core.EpochBoundaryContext, epochNumber uint64) ([]core.FaultFlagInfo, error) {
	if m.deps.TriggerEpochSwitch != nil {
		m.deps.TriggerEpochSwitch(ctx.NextBlockNumber)
	}

	if m.deps.BuildPendingHeader != nil && m.deps.SetPendingEpochEndHeader != nil {
		if header := m.deps.BuildPendingHeader(ctx.ParentHash, ctx.NextBlockNumber); header != nil {
			m.deps.SetPendingEpochEndHeader(header)
		}
	}

	defer func() {
		if m.deps.ClearPendingEpochEndHeader != nil {
			m.deps.ClearPendingEpochEndHeader(ctx.NextBlockNumber)
		}
	}()

	var faultFlags []core.FaultFlagInfo
	if m.deps.DetectFaults != nil {
		var err error
		// 🔧 修复：在epoch结束区块时，应该使用当前区块号（NextBlockNumber - 1）来检测故障
		// 因为 DetectFaults 需要知道当前epoch的区块号，而不是下一个epoch的区块号
		currentBlockNumber := ctx.NextBlockNumber - 1
		faultFlags, err = m.deps.DetectFaults(currentBlockNumber)
		if err != nil {
			return nil, err
		}
	}

	// 🔧 修复：如果验证者有恢复提案，从faultFlags中移除该验证者的故障标志，
	// 避免写入ExtraData，导致同步节点再次保存旧的故障状态
	filteredFaultFlags := make([]core.FaultFlagInfo, 0, len(faultFlags))
	for _, flag := range faultFlags {
		// 检查该验证者是否有待生效的恢复提案
		// 如果有，跳过保存故障状态，避免覆盖恢复结果
		if m.deps.CheckRecoveryProposal != nil {
			hasRecoveryProposal := m.deps.CheckRecoveryProposal(flag.ValidatorAddress, epochNumber)
			if hasRecoveryProposal {
				m.logger.Info("🔄 [runFaultDetection] 跳过保存故障状态并从faultFlags中移除：验证者有恢复提案",
					"validator", flag.ValidatorAddress.String(),
					"currentEpoch", epochNumber,
					"detectedFaulty", flag.IsFaulty,
					"detectedMissedBlocks", flag.MissedBlocks)
				continue // 不添加到filteredFaultFlags，也不保存
			}
		}

		// 没有恢复提案，保留该故障标志
		filteredFaultFlags = append(filteredFaultFlags, flag)
	}

	// 使用过滤后的faultFlags
	faultFlags = filteredFaultFlags

	for _, flag := range faultFlags {

		if m.deps.SaveFaultStatus != nil {
			if err := m.deps.SaveFaultStatus(flag); err != nil {
				m.logger.Warn("failed to persist fault status",
					"validator", flag.ValidatorAddress.String(),
					"error", err)
			}
		}

		if m.deps.UpdateMemoryFaultStatus != nil {
			m.deps.UpdateMemoryFaultStatus(flag)
		}
	}

	if m.deps.SaveCurrentEpoch != nil {
		if err := m.deps.SaveCurrentEpoch(epochNumber, ctx.NextBlockNumber); err != nil {
			m.logger.Warn("failed to persist current epoch index",
				"epoch", epochNumber,
				"error", err)
		}
	}

	if m.deps.UpdateBlockProducers != nil && len(faultFlags) > 0 {
		if err := m.deps.UpdateBlockProducers(faultFlags); err != nil {
			m.logger.Error("failed to update block producers from fault flags", "error", err)
		}
	}

	return faultFlags, nil
}

func (m *lifecycleManager) prepareNextEpochValidators(nextBlockNumber uint64) (validator.AccountSet, error) {
	if m.deps.CalculateNextEpochValidators == nil {
		return nil, fmt.Errorf("calculateNextEpochValidators dependency not provided")
	}

	return m.deps.CalculateNextEpochValidators(nextBlockNumber)
}

// ApplyNextValidatorsFromExtra 使用ExtraData中的验证者集合覆盖本地缓存
func (m *lifecycleManager) ApplyNextValidatorsFromExtra(validators validator.AccountSet, blockNumber uint64) error {
	if len(validators) == 0 {
		return nil
	}

	if m.deps.UpdateValidatorCaches != nil {
		m.deps.UpdateValidatorCaches(validators)
	}

	return nil
}
