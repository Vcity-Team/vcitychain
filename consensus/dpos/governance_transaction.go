package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"

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

	// 2. 根据提案类型创建提案
	// 🆕 使用交易哈希生成确定性的proposalID（所有节点相同）
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
	if d.state != nil && d.state.ProposalStore != nil {
		if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
			d.logger.Error("Failed to save proposal to database", "error", err)
			return fmt.Errorf("failed to save proposal to database: %w", err)
		}
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
	proposal, exists := d.parameterProposals[txData.ProposalID]
	if !exists {
		// 从数据库加载
		if d.state != nil && d.state.ProposalStore != nil {
			var err error
			proposal, err = d.state.ProposalStore.GetProposal(txData.ProposalID)
			if err != nil {
				return fmt.Errorf("proposal not found: %s", txData.ProposalID)
			}
			// 加载到内存
			d.parameterProposals[txData.ProposalID] = proposal
		} else {
			return fmt.Errorf("proposal not found: %s", txData.ProposalID)
		}
	}

	// 3. 检查投票是否已存在
	if _, exists := proposal.Votes[tx.From]; exists {
		return fmt.Errorf("voter %s has already voted on proposal %s", tx.From.String(), txData.ProposalID)
	}

	// 🆕 4. 获取投票者余额（允许所有有余额的用户投票，与VoteOnParameterProposal保持一致）
	var voterWeight *big.Int
	var err error
	if d.balanceQuerier != nil {
		voterWeight, err = d.balanceQuerier.GetNativeTokenBalance(tx.From)
		if err != nil {
			d.logger.Warn("Failed to query voter balance for proposal vote transaction", "voter", tx.From.String(), "error", err)
			voterWeight = big.NewInt(0)
		}
	} else {
		// 如果没有余额查询器，尝试使用getAccountBalance
		if d.config != nil && d.config.Executor != nil {
			currentHeader := d.config.Blockchain.Header()
			if currentHeader != nil {
				if snapshot, err2 := d.config.Executor.StateAt(currentHeader.StateRoot); err2 == nil {
					if account, err2 := snapshot.GetAccount(tx.From); err2 == nil && account != nil {
						voterWeight = account.Balance
					}
				}
			}
		}
		if voterWeight == nil {
			voterWeight = big.NewInt(0)
		}
	}

	if voterWeight == nil || voterWeight.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("voter %s has no balance to vote (must have balance to vote, current balance: %s)", tx.From.String(), func() string {
			if voterWeight == nil {
				return "0"
			}
			return voterWeight.String()
		}())
	}

	// 5. 验证投票签名
	vote := ParameterVote{
		Voter:      tx.From,
		ProposalID: txData.ProposalID,
		Support:    txData.Support,
		Weight:     voterWeight,
		Timestamp:  blockNumber,
		Signature:  txData.VoteSignature,
	}

	if err := d.verifyParameterVote(&vote); err != nil {
		return fmt.Errorf("failed to verify vote signature: %w", err)
	}

	// 6. 添加投票（所有节点都执行）
	proposal.Votes[tx.From] = vote

	// 7. 保存到数据库（所有节点都执行）
	if d.state != nil && d.state.ProposalStore != nil {
		if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
			d.logger.Error("Failed to save proposal after vote", "error", err)
			return fmt.Errorf("failed to save proposal: %w", err)
		}
	}

	d.logger.Info("✅ 投票交易处理成功", "proposalID", txData.ProposalID, "voter", tx.From.String(), "support", txData.Support)

	return nil
}

// ProcessProposalExecuteTransaction 处理执行提案交易（所有节点都会执行）
func (d *DPoS) ProcessProposalExecuteTransaction(tx *types.Transaction, blockNumber uint64) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 1. 解析交易数据
	var txData ProposalExecuteTxData
	if err := json.Unmarshal(tx.Input, &txData); err != nil {
		return fmt.Errorf("failed to unmarshal proposal execute tx data: %w", err)
	}

	d.logger.Info("🔄 处理执行提案交易", "from", tx.From.String(), "proposalID", txData.ProposalID, "blockNumber", blockNumber)

	// 2. 获取提案
	proposal, exists := d.parameterProposals[txData.ProposalID]
	if !exists {
		// 从数据库加载
		if d.state != nil && d.state.ProposalStore != nil {
			var err error
			proposal, err = d.state.ProposalStore.GetProposal(txData.ProposalID)
			if err != nil {
				return fmt.Errorf("proposal not found: %s", txData.ProposalID)
			}
			d.parameterProposals[txData.ProposalID] = proposal
		} else {
			return fmt.Errorf("proposal not found: %s", txData.ProposalID)
		}
	}

	// 3. 执行提案（所有节点都执行）
	if proposal.ProposalType == "" {
		proposal.ProposalType = "parameter"
	}

	switch proposal.ProposalType {
	case "parameter":
		if err := d.executeParameterProposalInTx(txData.ProposalID, proposal); err != nil {
			return fmt.Errorf("failed to execute parameter proposal: %w", err)
		}
	case "validator_recovery":
		if err := d.executeRecoveryProposalInTx(txData.ProposalID, proposal); err != nil {
			return fmt.Errorf("failed to execute recovery proposal: %w", err)
		}
	default:
		return fmt.Errorf("unknown proposal type: %s", proposal.ProposalType)
	}

	d.logger.Info("✅ 执行提案交易处理成功", "proposalID", txData.ProposalID)

	return nil
}
