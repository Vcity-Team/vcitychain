package dpos

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// ProcessProposalCreateTransaction 处理创建提案交易（所有节点都会执行）
func (d *DPoS) ProcessProposalCreateTransaction(tx *types.Transaction, blockNumber uint64) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 1. 解析交易数据
	var txData ProposalCreateTxData
	if err := json.Unmarshal(tx.Input, &txData); err != nil {
		return fmt.Errorf("failed to unmarshal proposal create tx data: %w", err)
	}

	d.logger.Info("🔄 处理创建提案交易", "from", tx.From.String(), "blockNumber", blockNumber, "proposalType", txData.ProposalType)

	// 1.1 提案者必须是当前验证者（与 JSON-RPC 层约束对齐，防止绕过 RPC 直接发交易）
	validators, err := d.GetSortedValidatorsWithLimit()
	if err != nil || len(validators) == 0 {
		return fmt.Errorf("cannot determine validators set for proposal creation: %w", err)
	}
	isValidator := false
	for _, v := range validators {
		if v.Address == tx.From {
			isValidator = true
			break
		}
	}
	if !isValidator {
		return fmt.Errorf("proposer %s is not a validator", tx.From.String())
	}

	// 2. 根据提案类型创建提案
	// 使用交易哈希生成确定性的proposalID（所有节点相同）
	proposalID := fmt.Sprintf("proposal_%s", tx.Hash.String()[:16]) // 使用交易哈希前16个字符

	var proposal *ParameterProposal

	if txData.ProposalType == "validator_recovery" {
		// 恢复提案
		validatorAddr := types.StringToAddress(txData.Parameter)
		if validatorAddr == (types.Address{}) {
			return fmt.Errorf("invalid validator address: %s", txData.Parameter)
		}

		// 获取当前故障状态
		faultInfo := d.getValidatorFaultInfo(validatorAddr)
		isFaulty, ok := faultInfo["isFaulty"].(bool)
		if !ok || !isFaulty {
			return fmt.Errorf("%w: validator %s is not in faulty status", ErrBusinessInvalid, validatorAddr.String())
		}

		oldValue := map[string]interface{}{
			"isFaulty":        faultInfo["isFaulty"],
			"missedBlocks":    faultInfo["missedBlocks"],
			"lastFaultyEpoch": faultInfo["lastFaultyEpoch"],
			"reason":          faultInfo["reason"],
		}

		newValue := map[string]interface{}{
			"isFaulty":        false,
			"missedBlocks":    0,
			"lastFaultyEpoch": 0,
			"reason":          fmt.Sprintf("Fault cleared (Transaction Hash: %s)", tx.Hash.String()),
		}

		// 更新proposalID为recovery前缀
		proposalID = fmt.Sprintf("recovery_%s", tx.Hash.String()[:16])

		proposal = &ParameterProposal{
			ID:                proposalID,
			ProposalType:      "validator_recovery",
			Parameter:         validatorAddr.String(),
			ValidatorAddress:  validatorAddr,
			OldValue:          oldValue,
			NewValue:          newValue,
			Proposer:          tx.From,
			StartBlock:        blockNumber + 1,
			EndBlock:          blockNumber + d.getVotePeriod(),
			ValidEndBlock:     blockNumber + d.getValidPeriod(),
			Status:            ProposalPending,
			Votes:             make(map[types.Address]ParameterVote),
			Threshold:         d.getVotingThreshold(),
			Description:       txData.Description,
			RecoveryReason:    txData.RecoveryReason,
			CreatedAt:         txData.CreatedAt,
			ProposalSignature: txData.ProposerSignature,
		}
	} else {
		// 参数提案
		// 获取当前参数值
		oldValue, err := d.getCurrentParameterValue(txData.Parameter)
		if err != nil {
			return fmt.Errorf("failed to get current parameter value: %w", err)
		}

		if !d.isParameterVotable(txData.Parameter) {
			return fmt.Errorf("parameter %s is not votable", txData.Parameter)
		}

		// proposalID已在上面用交易哈希生成

		proposal = &ParameterProposal{
			ID:                proposalID,
			ProposalType:      "parameter",
			Parameter:         txData.Parameter,
			OldValue:          oldValue,
			NewValue:          txData.NewValue,
			Proposer:          tx.From,
			StartBlock:        blockNumber + 1,
			EndBlock:          blockNumber + d.getVotePeriod(),
			ValidEndBlock:     blockNumber + d.getValidPeriod(),
			Status:            ProposalPending,
			Votes:             make(map[types.Address]ParameterVote),
			Threshold:         d.getVotingThreshold(),
			Description:       txData.Description,
			CreatedAt:         txData.CreatedAt,
			ProposalSignature: txData.ProposerSignature,
		}
	}

	// 3. 验证提案签名（现在签名消息不包含proposalID，所以可以直接验证）
	if err := d.verifyProposalSignature(proposal); err != nil {
		return fmt.Errorf("failed to verify proposal signature: %w", err)
	}

	// 4. 保存到数据库（所有节点都执行）
	if err := d.governanceSaveProposal(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ [SaveProposal] ProposalStore不可用，跳过数据库保存",
				"proposalID", proposal.ID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("❌ [SaveProposal] 保存提案到数据库失败", "error", err, "proposalID", proposal.ID)
			return fmt.Errorf("failed to save proposal to database: %w", err)
		}
	} else {
		d.logger.Info("✅ [SaveProposal] 提案已成功保存到数据库", "proposalID", proposal.ID, "proposalType", proposal.ProposalType)
	}

	// 5. 更新内存（所有节点都执行）
	d.parameterProposals[proposal.ID] = proposal
	d.activeProposals[proposal.ID] = true

	d.logger.Info("✅ 提案创建交易处理成功", "proposalID", proposal.ID, "proposalType", proposal.ProposalType)

	return nil
}

// ProcessProposalVoteTransaction 处理投票交易（所有节点都会执行）
func (d *DPoS) ProcessProposalVoteTransaction(tx *types.Transaction, blockNumber uint64) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 1. 解析交易数据
	var txData ProposalVoteTxData
	if err := json.Unmarshal(tx.Input, &txData); err != nil {
		return fmt.Errorf("failed to unmarshal proposal vote tx data: %w", err)
	}

	d.logger.Info("🔄 处理投票交易", "from", tx.From.String(), "proposalID", txData.ProposalID, "support", txData.Support, "blockNumber", blockNumber)

	// 2. 获取提案
	proposal, err := d.governanceHydrateProposal(txData.ProposalID)
	if err != nil || proposal == nil {
		return fmt.Errorf("proposal not found: %s", txData.ProposalID)
	}

	// 3. 检查提案是否已过期（投票期是否已结束）
	if blockNumber > proposal.EndBlock {
		d.logger.Warn("❌ [ProcessProposalVoteTransaction] 提案已过期，无法投票",
			"proposalID", txData.ProposalID,
			"currentBlock", blockNumber,
			"endBlock", proposal.EndBlock)
		return fmt.Errorf("proposal %s has expired (current block %d > end block %d)", txData.ProposalID, blockNumber, proposal.EndBlock)
	}

	// 4. 检查投票是否已存在
	if _, exists := proposal.Votes[tx.From]; exists {
		return fmt.Errorf("voter %s has already voted on proposal %s", tx.From.String(), txData.ProposalID)
	}

	// 5. 仅超级代表可对提案投票：投票者必须在配置的 SR 集合（前 DPoSValidatorsCount 个验证者）内
	srSet, err := d.GetSortedValidatorsWithLimit()
	if err != nil {
		d.logger.Warn("Failed to get super representatives for proposal vote transaction", "voter", tx.From.String(), "error", err)
		return fmt.Errorf("cannot determine super representatives: %w", err)
	}
	srMap := make(map[types.Address]struct{}, len(srSet))
	for _, v := range srSet {
		srMap[v.Address] = struct{}{}
	}
	if _, ok := srMap[tx.From]; !ok {
		return fmt.Errorf("voter %s is not a super representative (only SRs can vote on proposals)", tx.From.String())
	}
	// 一 SR 一票，权重固定为 1
	voterWeight := big.NewInt(1)

	// 6. 获取区块头的时间戳
	var blockTimestamp uint64
	if d.config != nil && d.config.Blockchain != nil {
		if header, ok := d.config.Blockchain.GetHeaderByNumber(blockNumber); ok && header != nil {
			blockTimestamp = header.Timestamp
		} else {
			// 如果获取失败，使用当前时间作为后备
			blockTimestamp = uint64(time.Now().Unix())
			d.logger.Warn("Failed to get block header timestamp, using current time", "blockNumber", blockNumber)
		}
	} else {
		// 如果无法获取区块头，使用当前时间
		blockTimestamp = uint64(time.Now().Unix())
		d.logger.Warn("Blockchain config not available, using current time for timestamp")
	}

	// 7. 验证投票签名
	vote := ParameterVote{
		Voter:      tx.From,
		ProposalID: txData.ProposalID,
		Support:    txData.Support,
		Weight:     voterWeight,
		Timestamp:  blockTimestamp,
		Signature:  txData.VoteSignature,
	}

	if err := d.verifyParameterVote(&vote); err != nil {
		return fmt.Errorf("failed to verify vote signature: %w", err)
	}

	// 8. 添加投票（所有节点都执行）
	proposal.Votes[tx.From] = vote

	// 9. 保存到数据库（所有节点都执行）
	if err := d.governanceRecordVote(proposal); err != nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ [ProcessProposalVoteTransaction] ProposalStore不可用，跳过保存",
				"proposalID", txData.ProposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else {
			d.logger.Error("❌ [ProcessProposalVoteTransaction] 保存投票后的提案失败", "error", err, "proposalID", txData.ProposalID)
			return fmt.Errorf("failed to save proposal: %w", err)
		}
	} else {
		d.logger.Info("✅ [ProcessProposalVoteTransaction] 投票后的提案已保存", "proposalID", txData.ProposalID)
	}

	d.logger.Info("✅ 投票交易处理成功", "proposalID", txData.ProposalID, "voter", tx.From.String(), "support", txData.Support)

	return nil
}

// ProcessProposalExecuteTransaction 处理执行提案交易（所有节点都会执行）
func (d *DPoS) ProcessProposalExecuteTransaction(tx *types.Transaction, blockNumber uint64) error {
	startTime := time.Now()
	d.logger.Info("🔒 [ProcessProposalExecuteTransaction] 尝试获取锁", "blockNumber", blockNumber, "startTime", startTime.Format("15:04:05.000000"))
	d.lock.Lock()
	lockAcquiredTime := time.Now()
	lockWaitDuration := lockAcquiredTime.Sub(startTime)
	if lockWaitDuration > 100*time.Millisecond {
		d.logger.Warn("⚠️ [ProcessProposalExecuteTransaction] 获取锁耗时较长", "blockNumber", blockNumber, "waitDuration", lockWaitDuration.String())
	}
	d.logger.Info("🔓 [ProcessProposalExecuteTransaction] 锁已获取", "blockNumber", blockNumber, "waitDuration", lockWaitDuration.String())
	defer func() {
		d.lock.Unlock()
		totalDuration := time.Since(startTime)
		d.logger.Info("🔓 [ProcessProposalExecuteTransaction] 锁已释放", "blockNumber", blockNumber, "totalDuration", totalDuration.String())
	}()

	// 1. 解析交易数据
	parseStartTime := time.Now()
	var txData ProposalExecuteTxData
	if err := json.Unmarshal(tx.Input, &txData); err != nil {
		return fmt.Errorf("failed to unmarshal proposal execute tx data: %w", err)
	}
	parseDuration := time.Since(parseStartTime)
	d.logger.Info("🔄 [ProcessProposalExecuteTransaction] 处理执行提案交易", "from", tx.From.String(), "proposalID", txData.ProposalID, "blockNumber", blockNumber, "parseDuration", parseDuration.String())

	// 2. 获取提案
	hydrateStartTime := time.Now()
	proposal, err := d.governanceHydrateProposal(txData.ProposalID)
	hydrateDuration := time.Since(hydrateStartTime)
	if hydrateDuration > 100*time.Millisecond {
		d.logger.Warn("⚠️ [ProcessProposalExecuteTransaction] 加载提案耗时较长", "proposalID", txData.ProposalID, "duration", hydrateDuration.String())
	}
	if err != nil || proposal == nil {
		d.logger.Error("❌ [ProcessProposalExecuteTransaction] 无法加载提案", "proposalID", txData.ProposalID, "error", err, "duration", hydrateDuration.String())
		return fmt.Errorf("proposal not found: %s", txData.ProposalID)
	}
	d.logger.Info("📋 [ProcessProposalExecuteTransaction] 提案已加载", "proposalID", txData.ProposalID, "status", proposal.Status.String(), "duration", hydrateDuration.String())

	// 检查1：投票期必须已结束
	if blockNumber <= proposal.EndBlock {
		d.logger.Warn("❌ [ProcessProposalExecuteTransaction] 投票期未结束，拒绝执行",
			"proposalID", txData.ProposalID,
			"currentBlock", blockNumber,
			"endBlock", proposal.EndBlock)
		return fmt.Errorf("proposal %s voting period has not ended yet (current block %d <= end block %d), cannot execute", txData.ProposalID, blockNumber, proposal.EndBlock)
	}

	// 检查1.5：提案有效期必须未过期
	if blockNumber > proposal.ValidEndBlock {
		d.logger.Warn("❌ [ProcessProposalExecuteTransaction] 提案有效期已过期，拒绝执行",
			"proposalID", txData.ProposalID,
			"currentBlock", blockNumber,
			"validEndBlock", proposal.ValidEndBlock)
		return fmt.Errorf("proposal %s has expired (current block %d > valid end block %d), cannot execute", txData.ProposalID, blockNumber, proposal.ValidEndBlock)
	}

	// 检查2：如果投票期已结束但状态未更新，先检查投票结果
	if proposal.Status != ProposalPassed && proposal.Status != ProposalRejected {
		checkStartTime := time.Now()
		d.logger.Info("🔄 [ProcessProposalExecuteTransaction] 投票期已结束但状态未更新，先检查投票结果", "proposalID", txData.ProposalID)
		if err := d.CheckProposalResult(txData.ProposalID); err != nil {
			checkDuration := time.Since(checkStartTime)
			d.logger.Warn("⚠️ [ProcessProposalExecuteTransaction] 检查投票结果失败", "proposalID", txData.ProposalID, "error", err, "duration", checkDuration.String())
		} else {
			checkDuration := time.Since(checkStartTime)
			if checkDuration > 100*time.Millisecond {
				d.logger.Warn("⚠️ [ProcessProposalExecuteTransaction] 检查投票结果耗时较长", "proposalID", txData.ProposalID, "duration", checkDuration.String())
			}
		}
		// 重新获取提案以获取更新后的状态
		reloadStartTime := time.Now()
		proposal, err = d.governanceHydrateProposal(txData.ProposalID)
		reloadDuration := time.Since(reloadStartTime)
		if err != nil || proposal == nil {
			return fmt.Errorf("failed to get updated proposal: %s", txData.ProposalID)
		}
		d.logger.Info("📋 [ProcessProposalExecuteTransaction] 提案已重新加载", "proposalID", txData.ProposalID, "status", proposal.Status.String(), "duration", reloadDuration.String())
	}

	// 检查3：提案状态必须为 Passed
	if proposal.Status != ProposalPassed {
		d.logger.Warn("❌ [ProcessProposalExecuteTransaction] 提案状态不是 Passed，拒绝执行",
			"proposalID", txData.ProposalID,
			"status", proposal.Status.String())
		return fmt.Errorf("proposal %s status is %s, must be 'passed' to execute", txData.ProposalID, proposal.Status.String())
	}

	d.logger.Info("✅ [ProcessProposalExecuteTransaction] 提案检查通过，开始执行", "proposalID", txData.ProposalID, "status", proposal.Status.String())

	// 3. 执行提案（所有节点都执行）
	if proposal.ProposalType == "" {
		proposal.ProposalType = "parameter"
	}

	executeStartTime := time.Now()
	switch proposal.ProposalType {
	case "parameter":
		d.logger.Info("🔄 [ProcessProposalExecuteTransaction] 开始执行参数提案", "proposalID", txData.ProposalID)
		if err := d.executeParameterProposalInTx(txData.ProposalID, proposal); err != nil {
			executeDuration := time.Since(executeStartTime)
			d.logger.Error("❌ [ProcessProposalExecuteTransaction] 执行参数提案失败", "proposalID", txData.ProposalID, "error", err, "duration", executeDuration.String())
			return fmt.Errorf("failed to execute parameter proposal: %w", err)
		}
		executeDuration := time.Since(executeStartTime)
		if executeDuration > 100*time.Millisecond {
			d.logger.Warn("⚠️ [ProcessProposalExecuteTransaction] 执行参数提案耗时较长", "proposalID", txData.ProposalID, "duration", executeDuration.String())
		}
	case "validator_recovery":
		d.logger.Info("🔄 [ProcessProposalExecuteTransaction] 开始执行恢复提案", "proposalID", txData.ProposalID)
		if err := d.executeRecoveryProposalInTx(txData.ProposalID, proposal); err != nil {
			executeDuration := time.Since(executeStartTime)
			d.logger.Error("❌ [ProcessProposalExecuteTransaction] 执行恢复提案失败", "proposalID", txData.ProposalID, "error", err, "duration", executeDuration.String())
			return fmt.Errorf("failed to execute recovery proposal: %w", err)
		}
		executeDuration := time.Since(executeStartTime)
		if executeDuration > 100*time.Millisecond {
			d.logger.Warn("⚠️ [ProcessProposalExecuteTransaction] 执行恢复提案耗时较长", "proposalID", txData.ProposalID, "duration", executeDuration.String())
		}
	default:
		return fmt.Errorf("unknown proposal type: %s", proposal.ProposalType)
	}

	totalDuration := time.Since(startTime)
	d.logger.Info("✅ [ProcessProposalExecuteTransaction] 执行提案交易处理成功", "proposalID", txData.ProposalID, "totalDuration", totalDuration.String())

	return nil
}
