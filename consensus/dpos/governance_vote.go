package dpos

import (
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// VoteOnParameterProposal 对参数提案进行投票
func (d *DPoS) VoteOnParameterProposal(voter types.Address, proposalID string, support bool, privateKeyHex string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 获取提案
	proposal, exists := d.parameterProposals[proposalID]
	if !exists {
		return fmt.Errorf("proposal not found")
	}

	// 检查投票时间
	currentBlock := d.getCurrentBlockNumber()
	if currentBlock < proposal.StartBlock || currentBlock > proposal.EndBlock {
		return fmt.Errorf("voting period has ended or not started")
	}

	// 🆕 检查投票者是否有余额（允许所有有余额的用户投票）
	var balance *big.Int
	var err error
	if d.balanceQuerier != nil {
		balance, err = d.balanceQuerier.GetNativeTokenBalance(voter)
		if err != nil {
			d.logger.Warn("Failed to query voter balance for proposal vote", "voter", voter.String(), "error", err)
			balance = big.NewInt(0)
		}
	} else {
		// 如果没有余额查询器，尝试使用getAccountBalance
		if d.config != nil && d.config.Executor != nil {
			currentHeader := d.config.Blockchain.Header()
			if currentHeader != nil {
				if snapshot, err2 := d.config.Executor.StateAt(currentHeader.StateRoot); err2 == nil {
					if account, err2 := snapshot.GetAccount(voter); err2 == nil && account != nil {
						balance = account.Balance
					}
				}
			}
		}
		if balance == nil {
			balance = big.NewInt(0)
		}
	}

	if balance == nil || balance.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("voter must have balance to vote (current balance: %s)", func() string {
			if balance == nil {
				return "0"
			}
			return balance.String()
		}())
	}

	// 检查是否已经投票
	if _, exists := proposal.Votes[voter]; exists {
		return fmt.Errorf("already voted on this proposal")
	}

	// 🆕 计算投票权重（基于余额，而不是质押）
	// 使用余额作为投票权重，这样余额越多权重越大
	weight := balance

	// 创建投票
	vote := &ParameterVote{
		Voter:      voter,
		ProposalID: proposalID,
		Support:    support,
		Weight:     weight,
		Timestamp:  uint64(time.Now().Unix()),
	}

	// 调试日志：记录投票参数
	d.logger.Info("🔍 创建投票记录",
		"proposalID", proposalID,
		"voter", voter.String(),
		"support", support,
		"weight", weight.String(),
		"timestamp", vote.Timestamp,
		"privateKey", privateKeyHex[:8]+"... (已隐藏)")

	// 签名投票（传递私钥）
	if err := d.signParameterVote(vote, privateKeyHex); err != nil {
		return fmt.Errorf("failed to sign vote: %w", err)
	}

	// 验证签名
	if err := d.verifyParameterVote(vote); err != nil {
		return fmt.Errorf("failed to verify vote signature: %w", err)
	}

	// 记录投票
	proposal.Votes[voter] = *vote

	// 保存更新后的提案到数据库
	if d.state != nil && d.state.ProposalStore != nil {
		if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
			d.logger.Error("Failed to save updated proposal to database", "error", err)
			// 注意：这里不返回错误，因为内存更新已经成功
			// 但记录错误日志以便调试
		} else {
			d.logger.Debug("✅ Updated proposal successfully saved to database",
				"proposalID", proposalID,
				"voter", voter.String(),
				"support", support)
		}
	}

	d.logger.Info("Parameter vote cast",
		"proposalID", proposalID,
		"voter", voter.String(),
		"support", support,
		"weight", weight.String())

	return nil
}

// CheckProposalResult 检查提案投票结果
func (d *DPoS) CheckProposalResult(proposalID string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	proposal, exists := d.parameterProposals[proposalID]
	if !exists {
		return fmt.Errorf("proposal not found")
	}

	// 检查是否在投票期内
	currentBlock := d.getCurrentBlockNumber()
	if currentBlock <= proposal.EndBlock {
		return nil // 投票期未结束
	}

	// 统计投票结果
	totalWeight := big.NewInt(0)
	supportWeight := big.NewInt(0)

	for _, vote := range proposal.Votes {
		totalWeight.Add(totalWeight, vote.Weight)
		if vote.Support {
			supportWeight.Add(supportWeight, vote.Weight)
		}
	}

	// 计算支持率
	if totalWeight.Cmp(big.NewInt(0)) == 0 {
		proposal.Status = ProposalRejected
		d.finalizeProposalLifecycle(proposalID, proposal)
		return nil
	}

	supportRate := new(big.Int).Mul(supportWeight, big.NewInt(100))
	supportRate.Div(supportRate, totalWeight)

	// 判断是否通过
	if supportRate.Cmp(big.NewInt(int64(proposal.Threshold))) >= 0 {
		proposal.Status = ProposalPassed
		d.logger.Info("Proposal passed",
			"proposalID", proposalID,
			"supportRate", supportRate.String(),
			"threshold", proposal.Threshold)
	} else {
		proposal.Status = ProposalRejected
		d.logger.Info("Proposal rejected",
			"proposalID", proposalID,
			"supportRate", supportRate.String(),
			"threshold", proposal.Threshold)
	}

	d.finalizeProposalLifecycle(proposalID, proposal)

	return nil
}

// finalizeProposalLifecycle 移除活跃状态并持久化提案
func (d *DPoS) finalizeProposalLifecycle(proposalID string, proposal *ParameterProposal) {
	delete(d.activeProposals, proposalID)

	if d.state != nil && d.state.ProposalStore != nil {
		if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
			d.logger.Error("Failed to persist proposal status", "proposalID", proposalID, "error", err)
		}
	}
}

// buildVoteMessage 构造投票签名消息
func (d *DPoS) buildVoteMessage(vote *ParameterVote) []byte {
	// 构造签名消息：投票者地址 + 提案ID + 支持/反对 + 链ID（去掉时间戳，避免时序不一致）
	data := make([]byte, 0)

	// 1. 投票者地址 (20字节)
	data = append(data, vote.Voter.Bytes()...)

	// 2. 提案ID (变长，添加长度前缀以避免冲突)
	proposalIDBytes := []byte(vote.ProposalID)
	proposalIDLen := make([]byte, 4)
	binary.BigEndian.PutUint32(proposalIDLen, uint32(len(proposalIDBytes)))
	data = append(data, proposalIDLen...)
	data = append(data, proposalIDBytes...)

	// 3. 支持/反对 (1字节: 0x01=true, 0x00=false)
	if vote.Support {
		data = append(data, byte(0x01))
	} else {
		data = append(data, byte(0x00))
	}

	// 4. 链ID (8字节，如果config中有chainID)
	var chainID uint64
	if d.config != nil && d.config.Blockchain != nil && d.config.Blockchain.Config() != nil {
		chainID = uint64(d.config.Blockchain.Config().ChainID)
	}
	chainIDBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(chainIDBytes, chainID)
	data = append(data, chainIDBytes...)

	// 计算Keccak256哈希
	message := crypto.Keccak256(data)

	d.logger.Debug("🔍 构造投票签名消息",
		"voter", vote.Voter.String(),
		"proposalID", vote.ProposalID,
		"support", vote.Support,
		"chainID", chainID,
		"messageHash", hex.EncodeToString(message))

	return message
}

// signParameterVote 签名参数投票
func (d *DPoS) signParameterVote(vote *ParameterVote, privateKeyHex string) error {
	d.logger.Info("🔍 开始签名投票", "voter", vote.Voter.String(), "proposalID", vote.ProposalID)

	// 1. 验证私钥格式
	if len(privateKeyHex) != 64 {
		return fmt.Errorf("invalid private key length: expected 64, got %d", len(privateKeyHex))
	}

	// 2. 验证私钥hex字符
	for i, char := range privateKeyHex {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return fmt.Errorf("invalid hex character at position %d: %c", i, char)
		}
	}

	// 3. 解码私钥
	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return fmt.Errorf("failed to decode private key: %w", err)
	}

	if len(privateKeyBytes) != 32 {
		return fmt.Errorf("invalid private key bytes length: expected 32, got %d", len(privateKeyBytes))
	}

	// 4. 创建ECDSA私钥对象
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: crypto.S256,
		},
		D: new(big.Int).SetBytes(privateKeyBytes),
	}
	privateKey.PublicKey.X, privateKey.PublicKey.Y = privateKey.Curve.ScalarBaseMult(privateKeyBytes)

	// 5. 验证私钥地址匹配
	calculatedAddr := crypto.PubKeyToAddress(&privateKey.PublicKey)
	if calculatedAddr != vote.Voter {
		return fmt.Errorf("private key does not match voter address: calculated=%s, expected=%s",
			calculatedAddr.String(), vote.Voter.String())
	}

	d.logger.Info("✅ 私钥验证通过", "address", calculatedAddr.String())

	// 6. 构造签名消息
	message := d.buildVoteMessage(vote)

	// 7. 签名
	signature, err := crypto.Sign(privateKey, message)
	if err != nil {
		return fmt.Errorf("failed to sign vote: %w", err)
	}

	// 8. 保存签名
	vote.Signature = signature

	d.logger.Info("✅ 投票签名成功",
		"voter", vote.Voter.String(),
		"proposalID", vote.ProposalID,
		"signatureLength", len(signature))

	return nil
}

// verifyParameterVote 验证参数投票签名
func (d *DPoS) verifyParameterVote(vote *ParameterVote) error {
	if len(vote.Signature) == 0 {
		return fmt.Errorf("vote signature is empty")
	}

	// 1. 构造消息（与签名时相同）
	message := d.buildVoteMessage(vote)

	// 2. 恢复公钥
	pubKey, err := crypto.RecoverPubkey(vote.Signature, message)
	if err != nil {
		return fmt.Errorf("failed to recover public key: %w", err)
	}

	// 3. 计算地址
	recoveredAddr := crypto.PubKeyToAddress(pubKey)

	// 4. 验证地址匹配
	if recoveredAddr != vote.Voter {
		return fmt.Errorf("signature does not match voter address: recovered=%s, expected=%s",
			recoveredAddr.String(), vote.Voter.String())
	}

	d.logger.Debug("✅ 投票签名验证成功", "voter", vote.Voter.String())

	return nil
}

// SignVoteForTx 为交易签名投票（用于RPC层）
func (d *DPoS) SignVoteForTx(vote *ParameterVote, privateKeyHex string) ([]byte, error) {
	// 直接调用signParameterVote
	if err := d.signParameterVote(vote, privateKeyHex); err != nil {
		return nil, err
	}
	return vote.Signature, nil
}
