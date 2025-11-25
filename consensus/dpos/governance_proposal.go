package dpos

import (
	"errors"
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// CreateParameterProposal 创建参数表决提案
func (d *DPoS) CreateParameterProposal(proposer types.Address, parameter string, newValue interface{}, description string, proposerPrivateKeyHex string) (*ParameterProposal, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 🆕 验证私钥是否提供
	if proposerPrivateKeyHex == "" {
		return nil, fmt.Errorf("proposer private key is required for signing the proposal")
	}

	// 验证参数是否可表决
	if !d.isParameterVotable(parameter) {
		return nil, fmt.Errorf("parameter %s is not votable", parameter)
	}

	// 获取当前参数值
	oldValue, err := d.getCurrentParameterValue(parameter)
	if err != nil {
		return nil, fmt.Errorf("failed to get current parameter value: %w", err)
	}

	// 验证新参数值
	if err := d.validateParameterValue(parameter, newValue); err != nil {
		return nil, fmt.Errorf("invalid parameter value: %w", err)
	}

	// 创建提案 - 使用可读的时间格式确保唯一性
	now := time.Now()
	timeStr := now.Format("200601021504") // 格式：202510211029
	proposalID := fmt.Sprintf("proposal_%s_%d", timeStr, d.proposalCounter)
	d.proposalCounter++

	currentBlock := d.getCurrentBlockNumber()
	proposal := &ParameterProposal{
		ID:            proposalID,
		ProposalType:  "parameter", // 🆕 标记为参数修改提案
		Parameter:     parameter,
		OldValue:      oldValue,
		NewValue:      newValue,
		Proposer:      proposer,
		StartBlock:    currentBlock + 1,
		EndBlock:      currentBlock + d.getVotePeriod(),  // 表决期结束区块
		ValidEndBlock: currentBlock + d.getValidPeriod(), // 🆕 有效期结束区块
		Status:        ProposalPending,
		Votes:         make(map[types.Address]ParameterVote),
		Threshold:     d.getVotingThreshold(), // 动态通过阈值
		Description:   description,
		CreatedAt:     uint64(time.Now().Unix()),
	}

	// 🆕 签名提案
	if err := d.signProposal(proposal, proposerPrivateKeyHex); err != nil {
		return nil, fmt.Errorf("failed to sign proposal: %w", err)
	}

	// 🆕 验证签名
	if err := d.verifyProposalSignature(proposal); err != nil {
		return nil, fmt.Errorf("failed to verify proposal signature: %w", err)
	}

	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ ProposalStore不可用，无法持久化参数提案",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("Failed to save proposal to database", "error", err)
			return nil, fmt.Errorf("failed to save proposal to database: %w", err)
		}
	}

	d.parameterProposals[proposalID] = proposal
	d.activeProposals[proposalID] = true

	d.logger.Info("Parameter proposal created",
		"proposalID", proposalID,
		"parameter", parameter,
		"oldValue", oldValue,
		"newValue", newValue,
		"proposer", proposer.String(),
		"startBlock", proposal.StartBlock,
		"endBlock", proposal.EndBlock)

	return proposal, nil
}

// CreateRecoveryProposal 创建验证者恢复提案
func (d *DPoS) CreateRecoveryProposal(proposer types.Address, validatorAddr types.Address, recoveryReason string, description string, proposerPrivateKeyHex string) (*ParameterProposal, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 🆕 验证私钥是否提供
	if proposerPrivateKeyHex == "" {
		return nil, fmt.Errorf("proposer private key is required for signing the proposal")
	}

	// 验证要恢复的验证者是否存在故障
	faultInfo := d.getValidatorFaultInfo(validatorAddr)
	isFaulty, ok := faultInfo["isFaulty"].(bool)
	if !ok || !isFaulty {
		return nil, fmt.Errorf("validator %s is not in faulty status, cannot create recovery proposal", validatorAddr.String())
	}

	// 创建提案ID
	now := time.Now()
	timeStr := now.Format("200601021504")
	proposalID := fmt.Sprintf("recovery_%s_%d", timeStr, d.proposalCounter)
	d.proposalCounter++

	// 获取当前故障状态作为OldValue
	oldValue := map[string]interface{}{
		"isFaulty":        faultInfo["isFaulty"],
		"missedBlocks":    faultInfo["missedBlocks"],
		"lastFaultyEpoch": faultInfo["lastFaultyEpoch"],
		"reason":          faultInfo["reason"],
	}

	// 恢复后的状态作为NewValue
	newValue := map[string]interface{}{
		"isFaulty":        false,
		"missedBlocks":    0,
		"lastFaultyEpoch": 0,
		"reason":          fmt.Sprintf("Fault cleared (Proposal ID: %s)", proposalID),
	}

	currentBlock := d.getCurrentBlockNumber()
	proposal := &ParameterProposal{
		ID:               proposalID,
		ProposalType:     "validator_recovery",   // 🆕 标记为验证者恢复提案
		Parameter:        validatorAddr.String(), // 存储验证者地址
		ValidatorAddress: validatorAddr,          // 🆕 验证者地址
		OldValue:         oldValue,
		NewValue:         newValue,
		Proposer:         proposer,
		StartBlock:       currentBlock + 1,
		EndBlock:         currentBlock + d.getVotePeriod(),  // 表决期结束区块
		ValidEndBlock:    currentBlock + d.getValidPeriod(), // 🆕 有效期结束区块
		Status:           ProposalPending,
		Votes:            make(map[types.Address]ParameterVote),
		Threshold:        d.getVotingThreshold(),
		Description:      description,
		RecoveryReason:   recoveryReason, // 🆕 恢复理由
		CreatedAt:        uint64(time.Now().Unix()),
	}

	// 🆕 签名提案
	if err := d.signProposal(proposal, proposerPrivateKeyHex); err != nil {
		return nil, fmt.Errorf("failed to sign proposal: %w", err)
	}

	// 🆕 验证签名
	if err := d.verifyProposalSignature(proposal); err != nil {
		return nil, fmt.Errorf("failed to verify proposal signature: %w", err)
	}

	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ ProposalStore不可用，无法持久化恢复提案",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("Failed to save recovery proposal to database", "error", err)
			return nil, fmt.Errorf("failed to save recovery proposal to database: %w", err)
		}
	}

	d.parameterProposals[proposalID] = proposal
	d.activeProposals[proposalID] = true

	d.logger.Info("Recovery proposal created",
		"proposalID", proposalID,
		"validator", validatorAddr.String(),
		"proposer", proposer.String(),
		"reason", recoveryReason,
		"startBlock", proposal.StartBlock,
		"endBlock", proposal.EndBlock)

	return proposal, nil
}
