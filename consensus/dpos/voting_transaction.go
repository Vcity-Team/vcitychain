package dpos

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

// processVoteTransaction 处理投票交易
func (d *DPoS) processVoteTransaction(tx *types.Transaction, blockNumber uint64) error {
	// 🚨 检测投票交易的全零哈希问题
	if tx == nil {
		d.logger.Error("🚨 CRITICAL: processVoteTransaction called with nil transaction", "blockNumber", blockNumber)
		return fmt.Errorf("nil transaction")
	}

	if tx.Hash == (types.Hash{}) {
		d.logger.Error("🚨 CRITICAL: processVoteTransaction called with zero hash transaction",
			"blockNumber", blockNumber,
			"txType", tx.Type,
			"nonce", tx.Nonce,
			"gasPrice", tx.GasPrice.String(),
			"value", tx.Value.String(),
			"from", tx.From.String(),
			"to", func() string {
				if tx.To != nil {
					return tx.To.String()
				}
				return "nil"
			}(),
			"inputLength", len(tx.Input))
		return fmt.Errorf("zero hash transaction")
	}

	d.logger.Info("🔄 开始处理投票交易", "txHash", tx.Hash.String(), "blockNumber", blockNumber)

	// 1. 解析交易输入数据，提取投票信息
	voteInfo, err := d.parseVoteTransactionData(tx)
	if err != nil {
		d.logger.Error("❌ 解析投票交易数据失败", "txHash", tx.Hash.String(), "error", err)
		return fmt.Errorf("failed to parse vote transaction data: %w", err)
	}

	d.logger.Info("📋 投票交易信息解析成功",
		"txHash", tx.Hash.String(),
		"from", tx.From.String(),
		"to", func() string {
			if tx.To != nil {
				return tx.To.String()
			}
			return "nil"
		}(),
		"value", tx.Value.String(),
		"inputLength", len(tx.Input),
		"voter", voteInfo.Voter.String(),
		"candidate", voteInfo.Candidate.String(),
		"amount", voteInfo.Amount.String())

	// 2. 调用投票处理逻辑
	d.logger.Debug("🔄 调用投票处理逻辑...")
	if err := d.AddVote(voteInfo.Voter, voteInfo.Candidate, voteInfo.Amount); err != nil {
		d.logger.Error("❌ 投票处理失败", "txHash", tx.Hash.String(), "error", err)
		return fmt.Errorf("failed to process vote: %w", err)
	}

	d.logger.Debug("✅ 投票交易处理完成", "txHash", tx.Hash.String(),
		"voter", voteInfo.Voter.String(),
		"candidate", voteInfo.Candidate.String(),
		"amount", voteInfo.Amount.String())
	return nil
}

// parseVoteTransactionData 解析投票交易数据
func (d *DPoS) parseVoteTransactionData(tx *types.Transaction) (*VoteInfo, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is nil")
	}

	input := tx.Input
	if input == nil || len(input) < 4 {
		return nil, fmt.Errorf("input data too short or nil: length=%d", len(input))
	}

	// 检查是否是DPoS投票交易
	if !bytes.Equal(input[:4], []byte("DPOS")) {
		return nil, fmt.Errorf("not a DPoS vote transaction, prefix=%x", input[:4])
	}

	if isDelegateDepositMigrationCalldata(input) {
		return nil, fmt.Errorf("not a DPoS vote transaction: deposit migration calldata")
	}
	if isDelegateDepositEscrowPayoutCalldata(input) {
		return nil, fmt.Errorf("not a DPoS vote transaction: escrow payout calldata")
	}

	// 预期格式：4字节"DPOS" + 20字节投票者 + 20字节受托人 + 32字节金额
	const (
		dposPrefixLen  = 4
		addrLen        = 20
		amountLen      = 32
		expectedLength = dposPrefixLen + addrLen + addrLen + amountLen
	)

	if len(input) < expectedLength {
		return nil, fmt.Errorf("invalid DPoS vote tx input length: expected %d, got %d", expectedLength, len(input))
	}

	// 解析地址和金额
	voter := types.BytesToAddress(input[dposPrefixLen : dposPrefixLen+addrLen])
	candidate := types.BytesToAddress(input[dposPrefixLen+addrLen : dposPrefixLen+addrLen+addrLen])
	amountBytes := input[dposPrefixLen+addrLen+addrLen : expectedLength]

	// 转换金额字节为big.Int（移除前导零）
	// 🔧 修复：支持负数解码（全1表示 -1）
	amount := new(big.Int).SetBytes(amountBytes)

	// 检查是否为全1（0xFFFFFFFF...），表示 -1
	isAllOnes := true
	for _, b := range amountBytes {
		if b != 0xFF {
			isAllOnes = false
			break
		}
	}
	if isAllOnes {
		amount = big.NewInt(-1)
		d.logger.Info("🔧 [解析] 检测到全1编码，解析为 amount = -1")
	}

	if amount.Cmp(big.NewInt(-1)) == 0 {
		// amount = -1 表示撤销全部投票，允许通过
		d.logger.Info("🔧 [解析] amount = -1，撤销投票")
	} else if amount.Sign() <= 0 {
		return nil, fmt.Errorf("vote amount must be positive or -1 for unvote, got %s", amount.String())
	}

	d.logger.Info("DPoS投票数据解析成功", "voter", voter.String(), "candidate", candidate.String(), "amount", amount.String())

	return &VoteInfo{
		Voter:     voter,
		Candidate: candidate,
		Amount:    amount,
	}, nil
}

// processBlockVotes 处理区块中的投票事件
func (d *DPoS) processBlockVotes(block *types.FullBlock) error {
	if block == nil || block.Block == nil {
		d.logger.Warn("⚠️ 区块为空，跳过投票事件处理")
		return nil
	}

	// 修复：统一在区块广播接收后处理投票，确保只计算一次
	d.logger.Debug("✅ 处理区块中的投票事件",
		"blockNumber", block.Block.Number(),
		"blockCreator", func() string {
			if creator, err := d.GetBlockCreator(block.Block.Header); err == nil {
				return creator.String()
			}
			return "unknown"
		}())

	// 获取区块中的所有交易
	transactions := block.Block.Transactions
	d.logger.Debug("📋 区块交易数量", "blockNumber", block.Block.Number(), "txCount", len(transactions))

	// 处理投票交易
	voteCount := 0
	if len(transactions) > 0 {
		// 遍历所有交易，查找投票交易
		for i, tx := range transactions {
			// 检查是否是受托人注册交易
			if d.isDelegateRegistrationTransaction(tx) {
				d.logger.Info("🎯 ===== 发现受托人注册交易 =====")
				d.logger.Info("📍 交易位置", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())

				// 处理受托人注册交易
				if err := d.processDelegateRegistrationTransaction(tx, block.Block.Number(), block.Block.Header.Timestamp); err != nil {
					d.logger.Error("❌ 处理受托人注册交易失败", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String(), "error", err)
					// 不返回错误，继续处理其他交易
				} else {
					d.logger.Info("🎉 受托人注册交易处理成功！", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())
				}
			}

			// 检查是否是佣金率修改交易
			if d.isCommissionUpdateTransaction(tx) {
				d.logger.Info("💼 ===== 发现佣金率修改交易 =====",
					"blockNumber", block.Block.Number(),
					"txIndex", i,
					"txHash", tx.Hash.String())

				if err := d.processCommissionUpdateTransaction(tx, block.Block.Number()); err != nil {
					d.logger.Error("❌ 处理佣金率修改交易失败",
						"blockNumber", block.Block.Number(),
						"txIndex", i,
						"txHash", tx.Hash.String(),
						"error", err)
				} else {
					d.logger.Info("🎉 佣金率修改交易处理成功！",
						"blockNumber", block.Block.Number(),
						"txIndex", i,
						"txHash", tx.Hash.String())
				}
			}

			// 检查是否是投票交易
			if d.isVoteTransaction(tx) {
				d.logger.Info("🗳️ ===== 发现投票交易 =====")
				d.logger.Info("📍 投票交易位置", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())

				// 处理投票交易
				if err := d.processVoteTransaction(tx, block.Block.Number()); err != nil {
					d.logger.Error("❌ 处理投票交易失败", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String(), "error", err)
					// 不返回错误，继续处理其他交易
				} else {
					voteCount++
					d.logger.Info("🎉 投票交易处理成功！", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())
				}
			}
		}

		// 只在有投票交易时打印处理完成日志
		if voteCount > 0 {
			d.logger.Info("🎊 ===== 区块投票事件处理完成 =====", "blockNumber", block.Block.Number(), "totalTx", len(transactions), "voteTx", voteCount)
		}
	}

	// 新增：处理经济系统逻辑（静默执行）
	if err := d.processEconomicSystem(block); err != nil {
		d.logger.Error("❌ 处理经济系统失败", "blockNumber", block.Block.Number(), "error", err)
		// 不返回错误，继续处理
	}

	return nil
}

// processBlockVotesFromHeader 从区块头处理投票事件
func (d *DPoS) processBlockVotesFromHeader(header *types.Header) error {
	if header == nil {
		d.logger.Warn("⚠️ 区块头部为空，跳过投票事件处理")
		return nil
	}

	// 修复：获取完整的区块数据，包括交易信息
	// 通过区块哈希获取完整的区块数据
	block, exists := d.blockchain.GetBlockByHash(header.Hash, true)
	if !exists {
		d.logger.Warn("⚠️ 无法获取区块数据，跳过投票事件处理", "blockHash", header.Hash)
		return nil
	}

	if block == nil {
		d.logger.Warn("⚠️ 区块数据为空，跳过投票事件处理", "blockHash", header.Hash)
		return nil
	}

	// 转换为FullBlock格式
	fullBlock := &types.FullBlock{
		Block:    block,
		Receipts: []*types.Receipt{}, // 空的receipts，投票处理不需要
	}

	// 调用现有的投票处理逻辑
	return d.processBlockVotes(fullBlock)
}
