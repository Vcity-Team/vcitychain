package dpos

import (
	"fmt"
	"sort"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	governancemodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/governance"
	"github.com/Vcity-Team/vcitychain/types"
)

var errProposalStoreUnavailable = governancemodule.ErrProposalStoreUnavailable

func (d *DPoS) ensureGovernanceModule() core.GovernanceManager {
	if d.governance == nil {
		d.initGovernanceModule()
	}
	return d.governance
}

func (d *DPoS) governanceSaveProposal(proposal *ParameterProposal) error {
	mgr, err := d.requireGovernanceManager("save proposal")
	if err != nil {
		return err
	}
	return mgr.SaveProposal(proposal)
}

func (d *DPoS) governanceGetAllProposals() (map[string]*ParameterProposal, error) {
	mgr, err := d.requireGovernanceManager("get proposals")
	if err != nil {
		return nil, err
	}
	return mgr.GetAllProposals()
}

func (d *DPoS) governanceRecordVote(proposal *ParameterProposal) error {
	mgr, err := d.requireGovernanceManager("record vote")
	if err != nil {
		return err
	}
	return mgr.RecordVote(proposal)
}

func (d *DPoS) governanceLoadAllIntoMemory() error {
	mgr, err := d.requireGovernanceManager("load proposals")
	if err != nil {
		return err
	}
	return mgr.LoadAllIntoMemory()
}

func (d *DPoS) governanceHydrateProposal(proposalID string) (*ParameterProposal, error) {
	mgr, err := d.requireGovernanceManager("hydrate proposal")
	if err != nil {
		return nil, err
	}
	return mgr.HydrateProposal(proposalID)
}

func (d *DPoS) governanceLoadScheduled(epoch uint64) []*ParameterProposal {
	// 降级为DEBUG，避免刷屏
	d.logger.Debug("🔍🔍🔍 [governanceLoadScheduled] 开始查询待应用提案",
		"epoch", epoch,
		"说明", "查询effectiveEpoch等于此值的待应用提案")
	
	mgr := d.ensureGovernanceModule()
	if mgr == nil {
		d.logger.Warn("⚠️ [governanceLoadScheduled] GovernanceManager不可用", "epoch", epoch)
		return nil
	}
	result := mgr.LoadScheduled(epoch)
	d.logger.Debug("🔍🔍🔍 [governanceLoadScheduled] 查询完成",
		"epoch", epoch,
		"resultCount", len(result))
	return result
}

// governanceLoadScheduledUpTo 加载 EffectiveEpoch<=maxEpoch 且未应用的所有待应用提案（边界补跑用）
func (d *DPoS) governanceLoadScheduledUpTo(maxEpoch uint64) []*ParameterProposal {
	d.logger.Debug("🔍 [governanceLoadScheduledUpTo] 开始查询", "maxEpoch", maxEpoch)
	mgr := d.ensureGovernanceModule()
	if mgr == nil {
		d.logger.Warn("⚠️ [governanceLoadScheduledUpTo] GovernanceManager不可用", "maxEpoch", maxEpoch)
		return nil
	}
	result := mgr.LoadScheduledUpTo(maxEpoch)
	d.logger.Debug("🔍 [governanceLoadScheduledUpTo] 查询完成", "maxEpoch", maxEpoch, "resultCount", len(result))
	return result
}

func (d *DPoS) governanceMarkProposalApplied(proposalID string, appliedBlock uint64) error {
	mgr, err := d.requireGovernanceManager("mark proposal applied")
	if err != nil {
		return err
	}
	return mgr.MarkProposalApplied(proposalID, appliedBlock)
}

func (d *DPoS) requireGovernanceManager(action string) (core.GovernanceManager, error) {
	mgr := d.ensureGovernanceModule()
	if mgr == nil {
		return nil, fmt.Errorf("governance manager not initialized (%s)", action)
	}
	return mgr, nil
}

// ApplyScheduledProposalsUpTo 手动触发补跑：应用所有 EffectiveEpoch<=currentEpoch 且未应用的提案。blockNumber 为 0 时使用当前链高。
// 返回应用成功的提案数量。供 RPC dpos_applyScheduledProposals 调用。
func (d *DPoS) ApplyScheduledProposalsUpTo(blockNumber uint64) (int, error) {
	if blockNumber == 0 {
		blockNumber = d.getCurrentBlockNumber()
	}
	if blockNumber == 0 {
		return 0, fmt.Errorf("cannot get current block number")
	}
	meta := d.getEpochForBlock(blockNumber)
	if meta == nil {
		return 0, fmt.Errorf("cannot get epoch for block %d", blockNumber)
	}
	currentEpoch := meta.Number
	if currentEpoch == 0 {
		return 0, fmt.Errorf("epoch is 0 for block %d", blockNumber)
	}

	scheduledProps := d.governanceLoadScheduledUpTo(currentEpoch)
	seen := make(map[string]bool)
	uniq := make([]*ParameterProposal, 0, len(scheduledProps))
	for _, pprop := range scheduledProps {
		if pprop == nil || pprop.ID == "" {
			continue
		}
		if seen[pprop.ID] {
			continue
		}
		seen[pprop.ID] = true
		uniq = append(uniq, pprop)
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i].Schedule.EffectiveEpoch < uniq[j].Schedule.EffectiveEpoch })

	applied := 0
	for _, prop := range uniq {
		if !prop.Schedule.Scheduled || prop.Schedule.EffectiveEpoch > currentEpoch || prop.Schedule.Applied {
			continue
		}
		d.logger.Info("🔍 [ApplyScheduledProposalsUpTo] 应用提案", "proposalID", prop.ID, "proposalType", prop.ProposalType, "effectiveEpoch", prop.Schedule.EffectiveEpoch, "currentEpoch", currentEpoch)
		switch prop.ProposalType {
		case "validator_recovery":
			validatorAddr := prop.ValidatorAddress
			if validatorAddr == (types.Address{}) {
				validatorAddr = types.StringToAddress(prop.Parameter)
			}
			if validatorAddr == (types.Address{}) {
				d.logger.Error("补跑恢复提案失败：无法获取验证者地址", "proposalID", prop.ID)
				continue
			}
			if d.state != nil && d.state.StakeStore != nil {
				if err := d.state.StakeStore.ClearValidatorFaultStatus(validatorAddr, prop.ID); err != nil {
					d.logger.Error("补跑恢复提案失败：清除故障标志失败", "error", err, "proposalID", prop.ID)
					continue
				}
				if d.faultyValidators != nil {
					delete(d.faultyValidators, validatorAddr)
				}
				if err := d.reloadValidatorsAfterRecovery(); err != nil {
					d.logger.Error("补跑恢复提案后重新加载验证者失败", "error", err, "proposalID", prop.ID)
				}
				prop.Schedule.Applied = true
				prop.Schedule.AppliedAtBlock = blockNumber
				prop.Status = ProposalExecuted
				if err := d.governanceSaveProposal(prop); err != nil {
					d.logger.Error("保存提案状态失败", "error", err, "proposalID", prop.ID)
				} else {
					applied++
					d.logger.Info("✅ [ApplyScheduledProposalsUpTo] 恢复提案已应用", "proposalID", prop.ID, "appliedAtBlock", blockNumber)
				}
			}
		case "parameter":
			if err := d.updateParameterValue(prop.Parameter, prop.NewValue, fmt.Sprintf("proposal_%s", prop.ID)); err != nil {
				d.logger.Error("补跑参数提案失败", "error", err, "proposalID", prop.ID, "parameter", prop.Parameter)
				continue
			}
			prop.Schedule.Applied = true
			prop.Schedule.AppliedAtBlock = blockNumber
			if err := d.governanceSaveProposal(prop); err != nil {
				d.logger.Error("保存提案状态失败", "error", err, "proposalID", prop.ID)
			} else {
				applied++
				d.logger.Info("✅ [ApplyScheduledProposalsUpTo] 参数提案已应用", "proposalID", prop.ID, "parameter", prop.Parameter, "newValue", prop.NewValue, "appliedAtBlock", blockNumber)
			}
		default:
			d.logger.Debug("跳过未知类型提案", "proposalID", prop.ID, "proposalType", prop.ProposalType)
		}
	}
	return applied, nil
}
