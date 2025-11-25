package dpos

import (
	"errors"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// executeParameterProposalInTx 在交易中执行参数提案（所有节点都执行）
func (d *DPoS) executeParameterProposalInTx(proposalID string, proposal *ParameterProposal) error {
	d.logger.Info("开始执行参数提案（登记待生效）", "proposalID", proposalID, "parameter", proposal.Parameter)

	// 🆕 优化：压缩到1个epoch延迟
	// 如果执行时不是epoch结束区块，在当前epoch结束就生效
	// 如果执行时已经是epoch结束区块，在下一个epoch结束生效
	currentBlockNumber := d.getCurrentBlockNumber()
	currentEpochMeta := d.getEpochForBlock(currentBlockNumber)
	currentEpoch := currentEpochMeta.Number

	// 检查当前区块是否是epoch结束区块
	isEpochEnd := d.isEpochEndBlock(currentBlockNumber)

	var effectiveEpoch uint64
	if isEpochEnd {
		// 已经是epoch结束区块，在下一个epoch结束生效
		effectiveEpoch = currentEpoch + 1
		d.logger.Info("🔄 [参数提案] 执行时是epoch结束区块，将在下一个epoch结束生效",
			"proposalID", proposalID,
			"currentBlock", currentBlockNumber,
			"currentEpoch", currentEpoch,
			"effectiveEpoch", effectiveEpoch)
	} else {
		// 不是epoch结束区块，在当前epoch结束就生效
		effectiveEpoch = currentEpoch
		d.logger.Info("✅ [参数提案] 执行时不是epoch结束区块，将在当前epoch结束生效（压缩延迟）",
			"proposalID", proposalID,
			"currentBlock", currentBlockNumber,
			"currentEpoch", currentEpoch,
			"effectiveEpoch", effectiveEpoch)
	}

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

	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ ProposalStore不可用，无法持久化参数提案调度",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
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

	// 🆕 优化：压缩到1个epoch延迟
	// 如果执行时不是epoch结束区块，在当前epoch结束就生效
	// 如果执行时已经是epoch结束区块，在下一个epoch结束生效
	currentBlockNumber := d.getCurrentBlockNumber()
	currentEpochMeta := d.getEpochForBlock(currentBlockNumber)
	currentEpoch := currentEpochMeta.Number

	// 检查当前区块是否是epoch结束区块
	isEpochEnd := d.isEpochEndBlock(currentBlockNumber)

	var effectiveEpoch uint64
	if isEpochEnd {
		// 已经是epoch结束区块，在下一个epoch结束生效
		effectiveEpoch = currentEpoch + 1
		d.logger.Info("🔄 [恢复提案] 执行时是epoch结束区块，将在下一个epoch结束生效",
			"proposalID", proposalID,
			"currentBlock", currentBlockNumber,
			"currentEpoch", currentEpoch,
			"effectiveEpoch", effectiveEpoch)
	} else {
		// 不是epoch结束区块，在当前epoch结束就生效
		effectiveEpoch = currentEpoch
		d.logger.Info("✅ [恢复提案] 执行时不是epoch结束区块，将在当前epoch结束生效（压缩延迟）",
			"proposalID", proposalID,
			"currentBlock", currentBlockNumber,
			"currentEpoch", currentEpoch,
			"effectiveEpoch", effectiveEpoch)
	}

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
	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ ProposalStore不可用，无法持久化恢复提案调度",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("保存提案状态失败", "error", err)
		}
	}

	d.logger.Info("✅ ==============================验证者恢复提案已登记，待边界生效",
		"proposalID", proposalID,
		"validator", validatorAddr.String(),
		"effectiveEpoch", effectiveEpoch)

	return nil
}
