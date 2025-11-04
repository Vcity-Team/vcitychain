package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// executeParameterProposalInTx 在交易中执行参数提案（所有节点都执行）
func (d *DPoS) executeParameterProposalInTx(proposalID string, proposal *ParameterProposal) error {
	d.logger.Info("开始执行参数提案（登记待生效）", "proposalID", proposalID, "parameter", proposal.Parameter)

	// 改为登记待生效：下一个 epoch 生效
	effectiveEpoch := d.getEpochForBlock(d.getCurrentBlockNumber()).Number + 1
	proposal.Schedule = ProposalScheduleMeta{
		Scheduled:      true,
		EffectiveEpoch: effectiveEpoch,
		Applied:        false,
		AppliedAtBlock: 0,
	}

	// 状态置为 executed（表示已通过执行流程），但实际参数将在边界应用
	proposal.Status = ProposalExecuted
	proposal.ExecutedAt = uint64(time.Now().Unix())
	proposal.ExecutedBy = proposalID

	if d.state != nil && d.state.ProposalStore != nil {
		if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
			d.logger.Error("Failed to save scheduled parameter proposal", "error", err)
		}
	}

	d.logger.Info("✅ ================================参数提案已登记，待边界生效", "proposalID", proposalID, "effectiveEpoch", effectiveEpoch)

	return nil
}

// executeRecoveryProposalInTx 在交易中执行恢复提案（所有节点都执行）
func (d *DPoS) executeRecoveryProposalInTx(proposalID string, proposal *ParameterProposal) error {
	validatorAddr := proposal.ValidatorAddress
	if validatorAddr == (types.Address{}) {
		validatorAddr = types.StringToAddress(proposal.Parameter)
	}

	d.logger.Info("开始执行验证者恢复提案（登记待生效）", "proposalID", proposalID, "validator", validatorAddr.String())

	// 不立即清除故障标志，登记到提案调度：下一个 epoch 生效
	effectiveEpoch := d.getEpochForBlock(d.getCurrentBlockNumber()).Number + 1
	proposal.Schedule = ProposalScheduleMeta{
		Scheduled:      true,
		EffectiveEpoch: effectiveEpoch,
		Applied:        false,
		AppliedAtBlock: 0,
	}

	// 更新提案状态
	proposal.ExecutedAt = uint64(time.Now().Unix())
	proposal.ExecutedBy = proposalID
	proposal.Status = ProposalExecuted

	// 保存到数据库（登记待生效）
	if d.state != nil && d.state.ProposalStore != nil {
		if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
			d.logger.Error("保存提案状态失败", "error", err)
		}
	}

	d.logger.Info("✅ ==============================验证者恢复提案已登记，待边界生效",
		"proposalID", proposalID,
		"validator", validatorAddr.String(),
		"effectiveEpoch", effectiveEpoch)

	return nil
}

