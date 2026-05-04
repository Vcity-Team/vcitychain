package dpos

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/umbracle/ethgo"
	"github.com/umbracle/ethgo/contract"
)

// executorAdapter 适配器，将blockchain_wrapper包装为blockchain.Executor
type executorAdapter struct {
	wrapper *blockchainWrapper
}

// ProcessBlock 实现 blockchain.Executor 接口
func (e *executorAdapter) ProcessBlock(parentRoot types.Hash, block *types.Block, blockCreator types.Address) (*state.Transition, error) {
	return e.wrapper.ProcessBlockExecutor(parentRoot, block, blockCreator)
}

const (
	consensusSource = "consensus"
)

var (
	errSendTxnUnsupported = errors.New("system state does not support send transactions")
)

// blockchain is an interface that wraps the methods called on blockchain
type blockchainBackend interface {
	// CurrentHeader returns the header of blockchain block head
	CurrentHeader() *types.Header

	// CommitBlock commits a block to the chain.
	CommitBlock(block *types.FullBlock) error

	// SetBlockProductionStartTime sets the block production start time (for tracking production duration)
	SetBlockProductionStartTime()

	// NewBlockBuilder is a factory method that returns a block builder on top of 'parent'.
	NewBlockBuilder(parent *types.Header, coinbase types.Address,
		txPool txPoolInterface, blockTime time.Duration, logger hclog.Logger) (blockBuilder, error)

	// ProcessBlock builds a final block from given 'block' on top of 'parent'.
	ProcessBlock(parent *types.Header, block *types.Block) (*types.FullBlock, error)

	// GetStateProviderForBlock returns a reference to make queries to the state at 'block'.
	GetStateProviderForBlock(block *types.Header) (contract.Provider, error)

	// GetStateProvider returns a reference to make queries to the provided state.
	GetStateProvider(transition *state.Transition) contract.Provider

	// GetHeaderByNumber returns a reference to block header for the given block number.
	GetHeaderByNumber(number uint64) (*types.Header, bool)

	// GetHeaderByHash returns a reference to block header for the given block hash
	GetHeaderByHash(hash types.Hash) (*types.Header, bool)

	// GetBlockByHash returns a reference to block for the given block hash
	GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool)

	// GetSystemState creates a new instance of SystemState interface
	GetSystemState(provider contract.Provider) SystemState

	// SubscribeEvents subscribes to blockchain events
	SubscribeEvents() blockchain.Subscription

	// UnubscribeEvents unsubscribes from blockchain events
	UnubscribeEvents(subscription blockchain.Subscription)

	// GetChainID returns chain id of the current blockchain
	GetChainID() uint64

	// GetReceiptsByHash retrieves receipts by hash
	GetReceiptsByHash(hash types.Hash) ([]*types.Receipt, error)

	// ReadTxLookup returns the block hash using the transaction hash
	ReadTxLookup(hash types.Hash) (types.Hash, bool)
}

var _ blockchainBackend = &blockchainWrapper{}

type blockchainWrapper struct {
	executor   *state.Executor
	blockchain *blockchain.Blockchain
	keyAddr    types.Address // 当前节点的地址
	config     *DPoSConfig   // 添加DPoS配置引用
	logger     hclog.Logger  // 添加logger字段
	state      *State        // 添加State字段

	// 添加验证者更新回调函数
	onValidatorsUpdated func(validators validator.AccountSet) error
}

// CurrentHeader returns the header of blockchain block head
func (p *blockchainWrapper) CurrentHeader() *types.Header {
	return p.blockchain.Header()
}

// CommitBlock commits a block to the chain
func (p *blockchainWrapper) CommitBlock(block *types.FullBlock) error {
	// 注意：WriteFullBlock 内部已经有写锁跟踪日志
	err := p.blockchain.WriteFullBlock(block, consensusSource)
	if err != nil {
		// 关键诊断信息：parentHash / 本地head / parent是否存在
		var (
			parentHash     = block.Block.Header.ParentHash
			localHead      = p.blockchain.Header()
			parentExists   = false
			parentHashText = parentHash.String()
		)

		if _, ok := p.blockchain.GetHeaderByHash(parentHash); ok {
			parentExists = true
		}

		p.logger.Error(
			"❌ [CommitBlock] WriteFullBlock失败",
			"blockNumber", block.Block.Number(),
			"blockHash", block.Block.Hash().String(),
			"parentHash", parentHashText,
			"parentExists", parentExists,
			"localHeadNumber", localHead.Number,
			"localHeadHash", localHead.Hash.String(),
			"error", err,
		)
		return err
	}

	// 生产节点路径：BuildBlock 只走了 Executor.BeginTxn + transition.Write，未走 ProcessBlock，
	// 因此本节点不会执行 ProcessProposalCreateTransaction 等。此处对区块内提案交易做与 ProcessBlock 一致的后置处理，
	// 保证生产节点本地也能查到刚出的区块中的提案。
	p.applyProposalTxsInBlock(block.Block)

	// 方案四：生产节点本地也执行故障消减（与奖励一致）。奖励在 BuildBlock 里已执行，消减只在 ProcessBlock 路径执行；
	// 生产节点不走 ProcessBlock，此处从本区块 Extra 解析并执行消减，保证生产节点与同步节点状态一致。
	if p.isEpochEndBlock(block.Block.Number()) {
		if e := p.applySlashingFromBlockExtra(block.Block); e != nil {
			p.logger.Warn("⚠️ [CommitBlock] 生产节点故障消减后置处理失败(不影响已写入区块)", "blockNumber", block.Block.Number(), "error", e)
		}
		// 出块/同步节点若未走 ProcessBlock 或 ProcessBlockExecutor，此处在 CommitBlock 统一补做边界应用提案（待生效参数/恢复提案）。
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
			p.logger.Info("🔍 [边界应用提案] 开始查询待应用提案", "blockNumber", block.Block.Number(), "source", "CommitBlock")
			applied, err := dposInstance.ApplyScheduledProposalsUpTo(block.Block.Number())
			if err != nil {
				p.logger.Warn("⚠️ [CommitBlock] 边界应用提案失败(不影响已写入区块)", "blockNumber", block.Block.Number(), "error", err)
			} else if applied > 0 {
				p.logger.Info("✅ [CommitBlock] 边界应用提案完成", "blockNumber", block.Block.Number(), "appliedCount", applied)
			}
		}
	}

	// 在共识切换高度创建根账户对创世验证者的投票记录（生产节点）
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
		if err := dposInstance.CreateGenesisVotesForAllValidators(block.Block.Number()); err != nil {
			p.logger.Error("❌ 创建创世投票记录失败", "blockNumber", block.Block.Number(), "error", err)
			// 不返回错误，因为区块已经写入，只记录日志
		}
	} else {
		p.logger.Warn("⚠️ [CommitBlock] DPoS实例不存在",
			"blockNumber", block.Block.Number(),
			"exists", exists)
	}

	return nil
}

// applySlashingFromBlockExtra 从区块 Extra 解析 SlashingInfo 并执行消减（与 processSlashingInBlock 逻辑一致），
// 用于生产节点在 CommitBlock 时补做本机未走的 ProcessBlock 消减处理。
func (p *blockchainWrapper) applySlashingFromBlockExtra(block *types.Block) error {
	extra := &Extra{}
	if err := extra.UnmarshalRLP(block.Header.ExtraData); err != nil {
		return fmt.Errorf("parse extra for slashing: %w", err)
	}
	if extra.SlashingInfo == nil {
		return nil
	}
	dposInstance, exists := GetDPoSInstance("vcity_dpos")
	if !exists || dposInstance == nil {
		return fmt.Errorf("DPoS instance not found")
	}
	slashingInfo := extra.SlashingInfo
	for _, slashingOp := range slashingInfo.Slashings {
		if err := dposInstance.executeSlashing(
			slashingOp.ValidatorAddr,
			slashingOp.SlashRate,
			block.Number(),
			slashingInfo.EpochNumber,
			slashingOp.Reason,
			slashingOp.MissedBlocks,
			slashingOp.MissedBlocksPercentage,
			0,
		); err != nil {
			p.logger.Warn("⚠️ [CommitBlock] 执行消减失败(生产节点)", "validator", slashingOp.ValidatorAddr.String(), "error", err)
			// 不返回错误，与 processSlashingInBlock 中“已执行过则跳过”的语义一致，区块已写入
		}
	}
	p.logger.Info("✅ [CommitBlock] 生产节点故障消减后置处理完成", "blockNumber", block.Number(), "slashingsCount", len(slashingInfo.Slashings))
	return nil
}

// applyProposalTxsInBlock 对区块内的提案交易执行 DPoS 后置处理（与 ProcessBlock 中逻辑一致），
// 用于生产节点在 CommitBlock 时补做本机未走的 ProcessBlock 提案处理。
func (p *blockchainWrapper) applyProposalTxsInBlock(block *types.Block) {
	dposInstance, exists := GetDPoSInstance("vcity_dpos")
	if !exists || dposInstance == nil {
		return
	}
	for _, tx := range block.Transactions {
		if len(tx.Input) == 0 || tx.To == nil {
			continue
		}
		if tx.From == (types.Address{}) {
			forks := p.blockchain.Config().Forks.At(block.Number())
			chainID := p.GetChainID()
			signer := crypto.NewSigner(forks, chainID)
			if addr, err := signer.Sender(tx); err == nil {
				tx.From = addr
			} else {
				p.logger.Debug("⚠️ [CommitBlock] 无法恢复提案交易 From，跳过", "txHash", tx.Hash.String(), "error", err)
				continue
			}
		}
		kind, err := ParseProposalInput(tx.Input)
		if err != nil {
			continue
		}
		switch kind {
		case "create":
			if e := dposInstance.ProcessProposalCreateTransaction(tx, block.Number()); e != nil {
				p.logger.Warn("⚠️ [CommitBlock] 提案创建后置处理失败(不影响已写入区块)", "txHash", tx.Hash.String(), "blockNumber", block.Number(), "error", e)
			} else {
				p.logger.Info("✅ [CommitBlock] 提案创建后置处理成功(生产节点)", "txHash", tx.Hash.String(), "blockNumber", block.Number())
			}
		case "vote":
			_ = dposInstance.ProcessProposalVoteTransaction(tx, block.Number())
		case "execute":
			_ = dposInstance.ProcessProposalExecuteTransaction(tx, block.Number())
		}
	}
}

// SetBlockProductionStartTime 设置区块生产开始时间（用于统计生产耗时）
func (p *blockchainWrapper) SetBlockProductionStartTime() {
	p.blockchain.SetBlockProductionStartTime()
}

// ProcessBlockExecutor 实现 blockchain.Executor 接口
func (p *blockchainWrapper) ProcessBlockExecutor(parentRoot types.Hash, block *types.Block, blockCreator types.Address) (*state.Transition, error) {

	header := block.Header.Copy()
	start := time.Now().UTC()

	transition, err := p.executor.BeginTxn(parentRoot, header, blockCreator)
	if err != nil {
		return nil, err
	}

	// apply transactions from block
	for _, tx := range block.Transactions {
		// 确保从区块读取的交易补齐 From（RLP不含From，需要本地恢复）
		if tx.From == (types.Address{}) {
			// 根据当前区块的 forks 状态创建正确的 signer，这样可以正确处理 EIP-1559 (DynamicFeeTx) 交易
			forks := p.blockchain.Config().Forks.At(block.Number())
			chainID := p.GetChainID()
			signer := crypto.NewSigner(forks, chainID)
			if addr, err := signer.Sender(tx); err == nil {
				tx.From = addr
				p.logger.Debug("🧩 从区块交易恢复发送者地址", "txHash", tx.Hash.String(), "from", tx.From.String())
			} else {
				p.logger.Error("🚨 无法从区块交易恢复发送者地址，将拒绝处理该交易", "txHash", tx.Hash.String(), "error", err)
				return nil, fmt.Errorf("failed to recover sender from block tx: %w", err)
			}
		}

		// 统一进入 EVM 执行
		if err = transition.Write(tx); err != nil {
			return nil, fmt.Errorf("process block tx error, tx = %v, err = %w", tx.Hash, err)
		}

		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
			if err := dposInstance.ApplyDelegateDepositMigrationAfterTx(transition, tx, block.Number()); err != nil {
				return nil, err
			}
		}

		// 执行后识别是否为提案交易，并触发 DPoS 业务处理（不影响 EVM 结果）
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			if len(tx.Input) > 0 && (tx.To != nil) {
				if kind, err := ParseProposalInput(tx.Input); err == nil {
					p.logger.Info("检测到提案交易(EVM后置处理)", "kind", kind, "txHash", tx.Hash.String())
					switch kind {
					case "create":
						if e := dposInstance.ProcessProposalCreateTransaction(tx, block.Number()); e != nil {
							p.logger.Warn("提案创建业务处理失败(不影响EVM)", "err", e, "txHash", tx.Hash.String())
						}
					case "vote":
						if e := dposInstance.ProcessProposalVoteTransaction(tx, block.Number()); e != nil {
							p.logger.Warn("提案投票业务处理失败(不影响EVM)", "err", e, "txHash", tx.Hash.String())
						}
					case "execute":
						if e := dposInstance.ProcessProposalExecuteTransaction(tx, block.Number()); e != nil {
							p.logger.Warn("提案执行业务处理失败(不影响EVM)", "err", e, "txHash", tx.Hash.String())
						}
					}
				}
			}
		}
	}

	// 检查是否是共识切换高度，如果是则创建根账户对创世验证者的投票记录
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
		if err := dposInstance.CreateGenesisVotesForAllValidators(block.Number()); err != nil {
			p.logger.Error("❌ 创建创世投票记录失败", "blockNumber", block.Number(), "error", err)
			// 不返回错误，因为这是共识切换高度的特殊处理，只记录日志
		}
	} else {
		p.logger.Warn("⚠️ [ProcessBlockExecutor] DPoS实例不存在",
			"blockNumber", block.Number(),
			"exists", exists)
	}

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 如果是epoch结束区块，处理奖励分发
	if isEpochEnd {
		p.logger.Info("🎯 =====epoch结束区块，开始处理奖励分配和边界应用提案和投票=====", "blockNumber", block.Number())

		if err := p.processRewardDistributionInBlock(block, transition); err != nil {
			p.logger.Error("❌❌❌ ========== 奖励分配执行失败 ========== ❌❌❌",
				"blockNumber", block.Number(),
				"blockHash", block.Hash().String()[:16],
				"error", err)
			return nil, fmt.Errorf("failed to process reward distribution: %w", err)
		}

		p.logger.Debug("✅======= 奖励分配执行成功 ======✅",
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String()[:16])

		// 处理故障消减（从ExtraData执行）- 同步节点也需要执行
		p.logger.Info("🔍 [ProcessBlockExecutor] epoch结束区块，准备处理故障消减", "blockNumber", block.Number(), "extraDataLength", len(block.Header.ExtraData))
		if err := p.processSlashingInBlock(block, transition); err != nil {
			p.logger.Error("❌❌❌ [ProcessBlockExecutor] ========== 故障消减执行失败 ========== ❌❌❌",
				"blockNumber", block.Number(),
				"blockHash", block.Hash().String()[:16],
				"error", err)
			return nil, fmt.Errorf("failed to process slashing: %w", err)
		}

		p.logger.Info("✅✅✅ [ProcessBlockExecutor] ========== 故障消减执行成功 ========== ✅✅✅",
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String()[:16])

		// 奖励分配与故障统计完成后，再在边界应用已登记的待生效提案和投票，避免被同区块统计覆盖
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			// 因为 getEpochForBlock(block.Number()) 在epoch结束区块时可能返回下一个epoch
			var currentEpoch uint64
			if block.Number() > 0 {
				// 使用前一个区块号来获取当前epoch（即将结束的epoch）
				currentEpochMeta := dposInstance.getEpochForBlock(block.Number() - 1)
				if currentEpochMeta != nil {
					currentEpoch = currentEpochMeta.Number
				} else {
					currentEpochMeta = dposInstance.getEpochForBlock(block.Number())
					if currentEpochMeta != nil {
						currentEpoch = currentEpochMeta.Number
					}
				}
			} else {
				// 如果区块号为0，直接使用当前区块号
				currentEpochMeta := dposInstance.getEpochForBlock(block.Number())
				if currentEpochMeta != nil {
					currentEpoch = currentEpochMeta.Number
				}
			}
			if currentEpoch == 0 {
				p.logger.Warn("⚠️ [ProcessBlockExecutor] 无法获取epoch信息", "blockNumber", block.Number())
			}
			p.logger.Info("🔍 [边界应用提案] 开始查询待应用提案", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			// 使用 UpTo 查询，包含本 epoch 及之前未应用的提案（补跑逾期）
			scheduledProps := dposInstance.governanceLoadScheduledUpTo(currentEpoch)
			p.logger.Info("🔍 [边界应用提案] 查询结果", "blockNumber", block.Number(), "currentEpoch", currentEpoch, "scheduledCount", len(scheduledProps))

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
			// 按 EffectiveEpoch 升序应用，先到期的先应用
			sort.Slice(uniq, func(i, j int) bool { return uniq[i].Schedule.EffectiveEpoch < uniq[j].Schedule.EffectiveEpoch })

			for _, prop := range uniq {
				p.logger.Info("🔍 [边界应用提案] 检查提案", "proposalID", prop.ID, "proposalType", prop.ProposalType, "scheduled", prop.Schedule.Scheduled, "effectiveEpoch", prop.Schedule.EffectiveEpoch, "applied", prop.Schedule.Applied, "currentEpoch", currentEpoch)
				// 应用条件：已登记、未应用、且生效 epoch 不晚于当前 epoch（含逾期补跑）
				if prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch <= currentEpoch && !prop.Schedule.Applied {
					p.logger.Info("✅ [边界应用提案] 提案条件满足，开始应用", "proposalID", prop.ID, "proposalType", prop.ProposalType)
					switch prop.ProposalType {
					case "validator_recovery":
						p.logger.Info("开始边界应用恢复提案", "proposalID", prop.ID, "status", prop.Status.String(), "currentEpoch", currentEpoch, "effectiveEpoch", prop.Schedule.EffectiveEpoch)

						// 应用恢复提案：清除验证者故障标志
						validatorAddr := prop.ValidatorAddress
						if validatorAddr == (types.Address{}) {
							validatorAddr = types.StringToAddress(prop.Parameter)
						}
						if validatorAddr == (types.Address{}) {
							p.logger.Error("边界应用恢复提案失败：无法获取验证者地址", "proposalID", prop.ID, "parameter", prop.Parameter)
						} else {
							p.logger.Info("准备清除验证者故障标志", "proposalID", prop.ID, "validator", validatorAddr.String())

							// 清除故障标志（数据库和内存）
							if dposInstance.state != nil && dposInstance.state.StakeStore != nil {
								if err := dposInstance.state.StakeStore.ClearValidatorFaultStatus(validatorAddr, prop.ID); err != nil {
									p.logger.Error("边界应用恢复提案失败：清除故障标志失败", "error", err, "proposalID", prop.ID, "validator", validatorAddr.String())
								} else {
									p.logger.Info("验证者故障标志已清除（数据库）", "proposalID", prop.ID, "validator", validatorAddr.String())

									// 同时清除内存中的故障状态
									if dposInstance.faultyValidators != nil {
										delete(dposInstance.faultyValidators, validatorAddr)
										p.logger.Info("验证者故障标志已清除（内存）", "proposalID", prop.ID, "validator", validatorAddr.String())
									}

									// 重新加载验证者集合，确保内存缓存与数据库同步
									if err := dposInstance.reloadValidatorsAfterRecovery(); err != nil {
										p.logger.Error("重新加载验证者集合失败", "error", err, "proposalID", prop.ID, "validator", validatorAddr.String())
									} else {
										p.logger.Info("✅ 验证者集合已重新加载", "proposalID", prop.ID, "validator", validatorAddr.String())
									}

									prop.Schedule.Applied = true
									prop.Schedule.AppliedAtBlock = block.Number()
									prop.Status = ProposalExecuted // 更新提案状态为已执行

									if err := dposInstance.governanceSaveProposal(prop); err != nil {
										p.logger.Error("保存提案状态失败", "error", err, "proposalID", prop.ID)
									} else {
										p.logger.Info("提案状态已保存", "proposalID", prop.ID, "status", prop.Status.String())
									}
									p.logger.Info("✅ =========================================边界应用恢复提案成功", "proposalID", prop.ID, "validator", validatorAddr.String(), "appliedAtBlock", block.Number(), "status", prop.Status.String())
								}
							} else {
								p.logger.Error("边界应用恢复提案失败：StakeStore不可用", "proposalID", prop.ID, "validator", validatorAddr.String())
							}
						}
					case "parameter":
						p.logger.Info("🔄 [边界应用参数] 开始更新参数", "proposalID", prop.ID, "parameter", prop.Parameter, "oldValue", prop.OldValue, "newValue", prop.NewValue)

						if err := dposInstance.updateParameterValue(prop.Parameter, prop.NewValue, fmt.Sprintf("proposal_%s", prop.ID)); err != nil {
							p.logger.Error("边界应用参数更新失败", "error", err, "proposalID", prop.ID, "parameter", prop.Parameter)
						} else {
							prop.Schedule.Applied = true
							prop.Schedule.AppliedAtBlock = block.Number()
							_ = dposInstance.governanceSaveProposal(prop)
							p.logger.Info("✅ =========================================边界应用参数更新成功", "proposalID", prop.ID, "parameter", prop.Parameter, "oldValue", prop.OldValue, "newValue", prop.NewValue, "appliedAtBlock", block.Number())
						}
					}
				}
			}
			// 边界应用撤销（先于投票，避免权重突变）
			p.logger.Info("🔍 [边界应用撤销] 开始查询待撤销", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			if err := dposInstance.applyScheduledUnvotes(currentEpoch, block.Number()); err != nil {
				p.logger.Error("❌ [边界应用撤销] 应用撤销失败", "error", err, "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			} else {
				p.logger.Info("✅ [边界应用撤销] 撤销应用完成", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			}
			// 边界应用投票
			p.logger.Info("🔍 [边界应用投票] 开始查询待应用投票", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			if err := dposInstance.applyScheduledVotes(currentEpoch, block.Number()); err != nil {
				p.logger.Error("❌ [边界应用投票] 应用投票失败", "error", err, "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			} else {
				p.logger.Info("✅ [边界应用投票] 投票应用完成", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			}
		}
	} else {
		p.logger.Debug("ℹ️ 不是epoch结束区块，跳过奖励分配",
			"blockNumber", block.Number(),
			"isEpochEnd", isEpochEnd)
	}

	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
		if err := dposInstance.applyDelegateDepositRefunds(transition, header.Timestamp, block.Number()); err != nil {
			return nil, err
		}
	}

	updateBlockExecutionMetric(start)

	return transition, nil
}

// ProcessBlock builds a final block from given 'block' on top of 'parent'
func (p *blockchainWrapper) ProcessBlock(parent *types.Header, block *types.Block) (*types.FullBlock, error) {
	header := block.Header.Copy()
	start := time.Now().UTC()

	transition, err := p.executor.BeginTxn(parent.StateRoot, header, types.BytesToAddress(header.Miner))
	if err != nil {
		return nil, err
	}

	// apply transactions from block
	for _, tx := range block.Transactions {
		// 确保从区块读取的交易补齐 From（RLP不含From，需要本地恢复）
		if tx.From == (types.Address{}) {
			// 根据当前区块的 forks 状态创建正确的 signer
			// 这样可以正确处理 EIP-1559 (DynamicFeeTx) 交易
			forks := p.blockchain.Config().Forks.At(block.Number())
			chainID := p.GetChainID()
			signer := crypto.NewSigner(forks, chainID)
			if addr, err := signer.Sender(tx); err == nil {
				tx.From = addr
				p.logger.Debug("🧩 [ProcessBlock] 从区块交易恢复发送者地址", "txHash", tx.Hash.String(), "from", tx.From.String())
			} else {
				p.logger.Error("🚨 [ProcessBlock] 无法从区块交易恢复发送者地址，将拒绝处理该交易", "txHash", tx.Hash.String(), "error", err)
				return nil, fmt.Errorf("failed to recover sender from block tx: %w", err)
			}
		}

		// 统一进入 EVM 执行
		if err = transition.Write(tx); err != nil {
			return nil, fmt.Errorf("process block tx error, tx = %v, err = %w", tx.Hash, err)
		}

		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
			if err := dposInstance.ApplyDelegateDepositMigrationAfterTx(transition, tx, block.Number()); err != nil {
				return nil, err
			}
		}

		// 执行后识别是否为提案交易，并触发 DPoS 业务处理（不影响 EVM 结果）
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			if len(tx.Input) > 0 && (tx.To != nil) {
				if kind, err := ParseProposalInput(tx.Input); err == nil {
					p.logger.Info("✅ [ProcessBlock] 检测到提案交易(EVM后置处理)", "kind", kind, "txHash", tx.Hash.String(), "blockNumber", block.Number())
					switch kind {
					case "create":
						p.logger.Info("🔄 [ProcessBlock] 开始处理创建提案交易", "txHash", tx.Hash.String(), "blockNumber", block.Number())
						if e := dposInstance.ProcessProposalCreateTransaction(tx, block.Number()); e != nil {
							p.logger.Error("❌ [ProcessBlock] 提案创建业务处理失败(不影响EVM)", "err", e, "txHash", tx.Hash.String(), "blockNumber", block.Number())
						} else {
							p.logger.Info("✅ [ProcessBlock] 提案创建业务处理成功", "txHash", tx.Hash.String(), "blockNumber", block.Number())
						}
					case "vote":
						if e := dposInstance.ProcessProposalVoteTransaction(tx, block.Number()); e != nil {
							// 投票业务处理失败不影响EVM，静默处理（如重复投票等正常业务校验）
						}
					case "execute":
						executeStartTime := time.Now()
						p.logger.Info("🔄 [ProcessBlock] 开始处理执行提案交易", "txHash", tx.Hash.String(), "blockNumber", block.Number(), "startTime", executeStartTime.Format("15:04:05.000000"))
						if e := dposInstance.ProcessProposalExecuteTransaction(tx, block.Number()); e != nil {
							executeDuration := time.Since(executeStartTime)
							p.logger.Warn("❌ [ProcessBlock] 提案执行业务处理失败(不影响EVM)", "err", e, "txHash", tx.Hash.String(), "duration", executeDuration.String())
						} else {
							executeDuration := time.Since(executeStartTime)
							p.logger.Info("✅ [ProcessBlock] 提案执行业务处理完成", "txHash", tx.Hash.String(), "blockNumber", block.Number(), "duration", executeDuration.String())
						}
					}
				} else {
					p.logger.Debug("ℹ️ [ProcessBlock] 交易不是提案交易或解析失败", "txHash", tx.Hash.String(), "error", err)
				}
			} else {
				p.logger.Debug("ℹ️ [ProcessBlock] 跳过提案交易检查", "txHash", tx.Hash.String(), "inputLength", len(tx.Input), "toIsNil", tx.To == nil)
			}
		} else {
			p.logger.Debug("⚠️ [ProcessBlock] DPoS实例不存在，跳过提案交易处理", "txHash", tx.Hash.String())
		}
	}

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 如果是epoch结束区块且不是生产节点自己生产的区块，处理奖励分发和下一个epoch验证者集合
	if isEpochEnd {
		p.logger.Debug("🎯🎯🎯 ========== 开始执行奖励分配 ========== 🎯🎯🎯",
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String()[:16])

		if err := p.processRewardDistributionInBlock(block, transition); err != nil {
			p.logger.Error("❌❌❌ ========== 奖励分配执行失败 ========== ❌❌❌",
				"blockNumber", block.Number(),
				"blockHash", block.Hash().String()[:16],
				"error", err)
			return nil, fmt.Errorf("failed to process reward distribution: %w", err)
		}

		p.logger.Debug("✅✅✅ ========== 奖励分配执行成功 ========== ✅✅✅",
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String()[:16])

		// 处理故障消减（从ExtraData执行）
		p.logger.Info("🔍 [ProcessBlock] epoch结束区块，准备处理故障消减", "blockNumber", block.Number(), "extraDataLength", len(block.Header.ExtraData))
		if err := p.processSlashingInBlock(block, transition); err != nil {
			p.logger.Error("❌❌❌ ========== 故障消减执行失败 ========== ❌❌❌",
				"blockNumber", block.Number(),
				"blockHash", block.Hash().String()[:16],
				"error", err)
			return nil, fmt.Errorf("failed to process slashing: %w", err)
		}

		p.logger.Info("✅✅✅ ========== 故障消减执行成功 ========== ✅✅✅",
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String()[:16])

		// 奖励分配与故障统计完成后，再在边界应用已登记的待生效提案和投票，避免被同区块统计覆盖
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			// 与 ProcessBlockExecutor 一致：epoch 结束区块时 getEpochForBlock(block.Number()) 可能返回下一 epoch，用 block.Number()-1 取「即将结束的 epoch」
			var currentEpoch uint64
			if block.Number() > 0 {
				currentEpochMeta := dposInstance.getEpochForBlock(block.Number() - 1)
				if currentEpochMeta != nil {
					currentEpoch = currentEpochMeta.Number
				} else {
					if m := dposInstance.getEpochForBlock(block.Number()); m != nil {
						currentEpoch = m.Number
					}
				}
			} else if m := dposInstance.getEpochForBlock(block.Number()); m != nil {
				currentEpoch = m.Number
			}

			p.logger.Info("🔍 [边界应用提案] 开始查询待应用提案", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			// 使用 UpTo 查询，包含本 epoch 及之前未应用的提案（补跑逾期）
			scheduledProps := dposInstance.governanceLoadScheduledUpTo(currentEpoch)
			p.logger.Info("🔍 [边界应用提案] 查询结果", "blockNumber", block.Number(), "currentEpoch", currentEpoch, "scheduledCount", len(scheduledProps))

			// 去重（按ID）
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
			// 按 EffectiveEpoch 升序应用，先到期的先应用
			sort.Slice(uniq, func(i, j int) bool { return uniq[i].Schedule.EffectiveEpoch < uniq[j].Schedule.EffectiveEpoch })

			for _, prop := range uniq {
				p.logger.Info("🔍 [边界应用提案] 检查提案", "proposalID", prop.ID, "proposalType", prop.ProposalType, "scheduled", prop.Schedule.Scheduled, "effectiveEpoch", prop.Schedule.EffectiveEpoch, "applied", prop.Schedule.Applied, "currentEpoch", currentEpoch)
				// 应用条件：已登记、未应用、且生效 epoch 不晚于当前 epoch（含逾期补跑）
				if prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch <= currentEpoch && !prop.Schedule.Applied {
					p.logger.Info("✅ [边界应用提案] 提案条件满足，开始应用", "proposalID", prop.ID, "proposalType", prop.ProposalType)
					switch prop.ProposalType {
					case "validator_recovery":
						p.logger.Info("开始边界应用恢复提案", "proposalID", prop.ID, "status", prop.Status.String(), "currentEpoch", currentEpoch, "effectiveEpoch", prop.Schedule.EffectiveEpoch)

						// 应用恢复提案：清除验证者故障标志
						validatorAddr := prop.ValidatorAddress
						if validatorAddr == (types.Address{}) {
							validatorAddr = types.StringToAddress(prop.Parameter)
						}
						if validatorAddr == (types.Address{}) {
							p.logger.Error("边界应用恢复提案失败：无法获取验证者地址", "proposalID", prop.ID, "parameter", prop.Parameter)
						} else {
							p.logger.Info("准备清除验证者故障标志", "proposalID", prop.ID, "validator", validatorAddr.String())

							// 清除故障标志（数据库和内存）
							if dposInstance.state != nil && dposInstance.state.StakeStore != nil {
								if err := dposInstance.state.StakeStore.ClearValidatorFaultStatus(validatorAddr, prop.ID); err != nil {
									p.logger.Error("边界应用恢复提案失败：清除故障标志失败", "error", err, "proposalID", prop.ID, "validator", validatorAddr.String())
								} else {
									p.logger.Info("验证者故障标志已清除（数据库）", "proposalID", prop.ID, "validator", validatorAddr.String())

									// 同时清除内存中的故障状态
									if dposInstance.faultyValidators != nil {
										delete(dposInstance.faultyValidators, validatorAddr)
										p.logger.Info("验证者故障标志已清除（内存）", "proposalID", prop.ID, "validator", validatorAddr.String())
									}

									// 重新加载验证者集合，确保内存缓存与数据库同步
									if err := dposInstance.reloadValidatorsAfterRecovery(); err != nil {
										p.logger.Error("重新加载验证者集合失败", "error", err, "proposalID", prop.ID, "validator", validatorAddr.String())
									} else {
										p.logger.Info("✅ 验证者集合已重新加载", "proposalID", prop.ID, "validator", validatorAddr.String())
									}

									prop.Schedule.Applied = true
									prop.Schedule.AppliedAtBlock = block.Number()
									prop.Status = ProposalExecuted // 更新提案状态为已执行

									if err := dposInstance.governanceSaveProposal(prop); err != nil {
										p.logger.Error("保存提案状态失败", "error", err, "proposalID", prop.ID)
									} else {
										p.logger.Info("提案状态已保存", "proposalID", prop.ID, "status", prop.Status.String())
									}
									p.logger.Info("✅ =========================================边界应用恢复提案成功", "proposalID", prop.ID, "validator", validatorAddr.String(), "appliedAtBlock", block.Number(), "status", prop.Status.String())
								}
							} else {
								p.logger.Error("边界应用恢复提案失败：StakeStore不可用", "proposalID", prop.ID, "validator", validatorAddr.String())
							}
						}
					case "parameter":
						// 应用参数更新
						p.logger.Info("🔄 [边界应用参数] 开始更新参数", "proposalID", prop.ID, "parameter", prop.Parameter, "oldValue", prop.OldValue, "newValue", prop.NewValue)

						if err := dposInstance.updateParameterValue(prop.Parameter, prop.NewValue, fmt.Sprintf("proposal_%s", prop.ID)); err != nil {
							p.logger.Error("边界应用参数更新失败", "error", err, "proposalID", prop.ID, "parameter", prop.Parameter)
						} else {
							prop.Schedule.Applied = true
							prop.Schedule.AppliedAtBlock = block.Number()
							_ = dposInstance.governanceSaveProposal(prop)
							p.logger.Info("✅ =========================================边界应用参数更新成功", "proposalID", prop.ID, "parameter", prop.Parameter, "oldValue", prop.OldValue, "newValue", prop.NewValue, "appliedAtBlock", block.Number())
						}
					}
				}
			}

			// 边界应用撤销（先于投票，避免权重突变）
			p.logger.Info("🔍 [边界应用撤销] 开始查询待撤销", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			if err := dposInstance.applyScheduledUnvotes(currentEpoch, block.Number()); err != nil {
				p.logger.Error("❌ [边界应用撤销] 应用撤销失败", "error", err, "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			} else {
				p.logger.Info("✅ [边界应用撤销] 撤销应用完成", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			}
			// 边界应用投票
			p.logger.Info("🔍 [边界应用投票] 开始查询待应用投票", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			if err := dposInstance.applyScheduledVotes(currentEpoch, block.Number()); err != nil {
				p.logger.Error("❌ [边界应用投票] 应用投票失败", "error", err, "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			} else {
				p.logger.Info("✅ [边界应用投票] 投票应用完成", "blockNumber", block.Number(), "currentEpoch", currentEpoch)
			}
		}
	} else {
		p.logger.Debug("ℹ️ 跳过奖励分配",
			"blockNumber", block.Number(),
			"isEpochEnd", isEpochEnd,
			"reason", func() string {
				if !isEpochEnd {
					return "不是epoch结束区块"
				}
				return "未知原因"
			}())
	}

	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
		if err := dposInstance.applyDelegateDepositRefunds(transition, header.Timestamp, block.Number()); err != nil {
			return nil, err
		}
	}

	_, root, err := transition.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to commit the state changes: %w", err)
	}

	updateBlockExecutionMetric(start)

	if root != block.Header.StateRoot {
		return nil, fmt.Errorf("incorrect state root: (%s, %s)", root, block.Header.StateRoot)
	}

	// build the block
	builtBlock := consensus.BuildBlock(consensus.BuildBlockParams{
		Header:   header,
		Txns:     block.Transactions,
		Receipts: transition.Receipts(),
	})

	if builtBlock.Header.TxRoot != block.Header.TxRoot {
		return nil, fmt.Errorf("incorrect tx root (expected: %s, actual: %s)",
			builtBlock.Header.TxRoot, block.Header.TxRoot)
	}

	return &types.FullBlock{
		Block:    builtBlock,
		Receipts: transition.Receipts(),
	}, nil
}

// isEpochEndBlock 检查是否是epoch结束区块
func (p *blockchainWrapper) isEpochEndBlock(blockNumber uint64) bool {
	// 使用配置计算epoch大小
	epochSize := p.getEpochSize()

	// 获取共识切换高度
	consensusSwitchHeight := uint64(0)
	if p.config != nil {
		consensusSwitchHeight = p.config.ConsensusSwitchHeight
	}

	// 从共识切换高度开始计算epoch
	if blockNumber < consensusSwitchHeight {
		// 在共识切换之前，不是epoch结束区块
		p.logger.Debug("🔍 isEpochEndBlock: 共识切换前",
			"blockNumber", blockNumber,
			"consensusSwitchHeight", consensusSwitchHeight,
			"isEpochEnd", false)
		return false
	}

	// 计算DPoS epoch：从共识切换高度开始
	dposBlockNumber := blockNumber - consensusSwitchHeight
	currentEpoch := (dposBlockNumber / epochSize) + 1
	firstBlockInEpoch := consensusSwitchHeight + (currentEpoch-1)*epochSize

	// 检查是否是epoch的最后一个区块
	isEpochEnd := firstBlockInEpoch+epochSize-1 == blockNumber

	return isEpochEnd
}

// getEpochSize 根据配置计算epoch大小（区块数）
func (p *blockchainWrapper) getEpochSize() uint64 {
	if p.config == nil {
		p.logger.Error("❌ config 为空，无法计算 epoch 大小")
		return 0
	}

	epochDuration := p.config.EpochDuration
	blockTime := p.config.BlockTime.Duration

	if blockTime == 0 {
		p.logger.Error("❌ blockTime 配置为0，无法计算 epoch 大小")
		return 0
	}

	if epochDuration == 0 {
		p.logger.Error("❌ epochDuration 配置为0，无法计算 epoch 大小")
		return 0
	}

	epochSize := uint64(epochDuration / blockTime)
	if epochSize == 0 {
		p.logger.Error("❌ 计算出的 epoch 大小为0")
		return 0
	}

	return epochSize
}

// processRewardDistributionInBlock 在区块执行时处理奖励分发
func (p *blockchainWrapper) processRewardDistributionInBlock(block *types.Block, transition *state.Transition) error {
	// 解析ExtraData获取奖励分发信息
	extra := &Extra{}
	if err := extra.UnmarshalRLP(block.Header.ExtraData); err != nil {
		p.logger.Error("❌ 解析ExtraData失败",
			"blockNumber", block.Number(),
			"error", err,
			"extraDataLength", len(block.Header.ExtraData))
		return fmt.Errorf("failed to unmarshal extra data: %w", err)
	}

	// 添加详细的奖励信息日志
	if extra.RewardDistribution != nil {
		p.logger.Info("💰 ExtraData包含奖励信息",
			"blockNumber", block.Number(),
			"epoch", extra.RewardDistribution.EpochNumber,
			"rewardCount", len(extra.RewardDistribution.Rewards),
			"voterRewardCount", len(extra.RewardDistribution.VoterRewards),
			"totalReward", extra.RewardDistribution.TotalReward.String())

		rewardInfo := extra.RewardDistribution

		p.logger.Debug("1.🎯🎯🎯🎯🎯🎯🎯🎯验证节点开始处理奖励分发",
			"blockNumber", block.Number(),
			"rewardCount", len(rewardInfo.Rewards),
			"epoch", rewardInfo.EpochNumber,
			"totalReward", rewardInfo.TotalReward.String())

		// 获取奖励账户地址（从配置中获取）
		// 🔧 修复：从DPoS实例获取奖励账户地址，而不是硬编码
		var rewardAccount types.Address
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			rewardAccount = dposInstance.config.RewardAccount
			p.logger.Info("✅ 从DPoS配置获取奖励账户地址",
				"rewardAccount", rewardAccount.String())
		} else {
			// 如果无法获取DPoS实例，使用硬编码地址（向后兼容）
			rewardAccount = types.StringToAddress("0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe")
			p.logger.Warn("⚠️ 无法获取DPoS实例，使用硬编码奖励账户地址",
				"rewardAccount", rewardAccount.String())
		}

		// 计算总奖励金额
		totalReward := new(big.Int)
		for addrStr, amount := range rewardInfo.Rewards {
			totalReward.Add(totalReward, amount)
			p.logger.Debug("💰 奖励详情",
				"blockNumber", block.Number(),
				"validator", addrStr,
				"amount", amount.String())
		}

		p.logger.Debug("💰 总奖励计算完成",
			"blockNumber", block.Number(),
			"totalReward", totalReward.String(),
			"rewardAccount", rewardAccount.String())

		// 检查奖励账户余额是否足够
		currentBalance := transition.GetBalance(rewardAccount)

		p.logger.Debug("💳 检查奖励账户余额",
			"blockNumber", block.Number(),
			"currentBalance", currentBalance.String(),
			"requiredAmount", totalReward.String())

		if currentBalance.Cmp(totalReward) < 0 {
			p.logger.Info("❌ 奖励账户余额不足",
				"blockNumber", block.Number(),
				"currentBalance", currentBalance.String(),
				"requiredAmount", totalReward.String())
			return fmt.Errorf("insufficient balance in reward account: current=%s, required=%s",
				currentBalance.String(), totalReward.String())
		}

		// 从奖励账户扣除总奖励
		transition.Txn().SubBalance(rewardAccount, totalReward)
		p.logger.Info("从奖励账户扣除总奖励",
			"blockNumber", block.Number(),
			"amount", totalReward.String())

		cnt := 0
		// 直接给每个验证者增加余额（不消耗gas，符合TRON做法）
		for addrStr, amount := range rewardInfo.Rewards {
			addr := types.StringToAddress(addrStr)

			transition.Txn().AddBalance(addr, amount)
			cnt++
		}
		if cnt == len(rewardInfo.Rewards) {
		}

		// 同步节点直接使用 ExtraData 中的奖励信息记录到数据库（无需重新计算）
		// 获取DPoS实例和RewardStore
		p.logger.Info("🔍 [Epoch奖励诊断] 同步节点收到ExtraData奖励信息",
			"blockNumber", block.Number(),
			"epoch", rewardInfo.EpochNumber,
			"rewardCount", len(rewardInfo.Rewards),
			"voterRewardCount", len(rewardInfo.VoterRewards),
			"totalReward", rewardInfo.TotalReward.String())

		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			if dposInstance.state != nil && dposInstance.state.RewardStore != nil {
				// 直接使用 ExtraData 中的奖励信息记录到数据库（无需重新计算）
				// 如果 VoterRewards 有数据，直接使用（包含 validator_address）
				// 否则使用 Rewards（不包含 validator_address，兼容旧版本）
				if err := dposInstance.recordRewardsFromExtraData(rewardInfo); err != nil {
					p.logger.Error("❌ 同步节点记录奖励失败",
						"blockNumber", block.Number(),
						"epoch", rewardInfo.EpochNumber,
						"error", err)
				} else {
					p.logger.Info("✅ 同步节点记录奖励成功",
						"blockNumber", block.Number(),
						"epoch", rewardInfo.EpochNumber,
						"rewardCount", len(rewardInfo.Rewards),
						"voterRewardCount", len(rewardInfo.VoterRewards))
				}
			} else {
				p.logger.Warn("⚠️ RewardStore不可用，跳过奖励记录",
					"blockNumber", block.Number(),
					"epoch", rewardInfo.EpochNumber)
			}
		}
	} else {
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			epochSize := dposInstance.getEpochSize()
			consensusSwitchHeight := dposInstance.config.ConsensusSwitchHeight
			dposBlockNumber := block.Number() - consensusSwitchHeight
			currentEpoch := (dposBlockNumber / epochSize) + 1
			firstBlockInEpoch := consensusSwitchHeight + (currentEpoch-1)*epochSize
			isEpochEndBlock := (block.Number() == firstBlockInEpoch+epochSize-1)

			if isEpochEndBlock {
				p.logger.Warn("⚠️ ExtraData解析后RewardDistribution为nil（epoch结束区块）",
					"blockNumber", block.Number(),
					"epoch", currentEpoch,
					"isEpochEndBlock", isEpochEndBlock)
			} else {
				p.logger.Debug("ℹ️ ExtraData解析后RewardDistribution为nil（非epoch结束区块）",
					"blockNumber", block.Number())
			}
		} else {
			p.logger.Warn("⚠️ ExtraData解析后RewardDistribution为nil",
				"blockNumber", block.Number(),
				"extraDataLength", len(block.Header.ExtraData))
		}
	}

	// 预先收集：需要在本epoch边界应用的恢复提案
	recoveredValidators := make(map[types.Address]*ParameterProposal)
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		epochForLog := uint64(0)
		if meta := dposInstance.getEpochForBlock(block.Number()); meta != nil {
			epochForLog = meta.Number
		}

		if allProposals, err := dposInstance.governanceGetAllProposals(); err == nil {
			p.logger.Debug("当前所有提案总数", "count", len(allProposals), "currentEpoch", epochForLog)
			for pid, prop := range allProposals {
				if prop == nil {
					p.logger.Warn("提案记录为空，跳过", "proposalID", pid, "currentEpoch", epochForLog)
					continue
				}

				p.logger.Debug("提案详情", "id", pid, "Type", prop.ProposalType, "Epoch", prop.Schedule.EffectiveEpoch, "Scheduled", prop.Schedule.Scheduled, "Validator", prop.ValidatorAddress.String(), "Status", prop.Status, "Start", prop.StartBlock, "End", prop.EndBlock, "Description", prop.Description, "currentEpoch", epochForLog)
				if prop.ProposalType == "validator_recovery" && prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch == epochForLog {
					vaddr := prop.ValidatorAddress
					if vaddr == (types.Address{}) {
						vaddr = types.StringToAddress(prop.Parameter)
					}
					recoveredValidators[vaddr] = prop
					p.logger.Info("[边界恢复提案调试] 收集需要apply的恢复提案", "ID", prop.ID, "Type", prop.ProposalType, "Scheduled", prop.Schedule.Scheduled, "EffEpoch", prop.Schedule.EffectiveEpoch, "Validator", vaddr.String(), "Parameter", prop.Parameter, "currentEpoch", epochForLog)
				}
			}
		} else {
			p.logger.Error("GetAllProposals error", "error", err, "currentEpoch", epochForLog)
		}
	}

	return nil
}

// processSlashingInBlock 从ExtraData读取消减信息并执行消减
func (p *blockchainWrapper) processSlashingInBlock(block *types.Block, transition *state.Transition) error {
	p.logger.Info("🔍 [processSlashingInBlock] 开始处理消减信息",
		"blockNumber", block.Number(),
		"extraDataLength", len(block.Header.ExtraData))

	// 解析ExtraData获取消减信息
	extra := &Extra{}
	if err := extra.UnmarshalRLP(block.Header.ExtraData); err != nil {
		p.logger.Error("❌ [processSlashingInBlock] 解析ExtraData失败",
			"blockNumber", block.Number(),
			"error", err,
			"extraDataLength", len(block.Header.ExtraData))
		return fmt.Errorf("failed to unmarshal extra data: %w", err)
	}

	p.logger.Info("🔍 [processSlashingInBlock] ExtraData解析成功",
		"blockNumber", block.Number(),
		"hasSlashingInfo", extra.SlashingInfo != nil,
		"hasRewardDistribution", extra.RewardDistribution != nil,
		"faultFlagsCount", len(extra.FaultFlags),
		"hasValidators", extra.Validators != nil,
		"hasCheckpoint", extra.Checkpoint != nil)

	if extra.SlashingInfo == nil {
		// 尝试手动解析ExtraData，检查第8个元素是否存在
		extraRaw := block.Header.ExtraData
		if len(extraRaw) > 97 { // ExtraVanity = 32, 至少需要一些数据
			p.logger.Info("ℹ️ ℹ️ ℹ️ ℹ️ [processSlashingInBlock] ExtraData中没有消减信息，跳过处理",
				"blockNumber", block.Number(),
				"extraDataLength", len(extraRaw),
				"note", "生成节点可能没有写入SlashingInfo，或ExtraData格式不正确（需要8个元素，第8个是SlashingInfo）")
		} else {
			p.logger.Info("ℹ️ ℹ️ ℹ️ ℹ️ [processSlashingInBlock] ExtraData中没有消减信息，跳过处理",
				"blockNumber", block.Number(),
				"extraDataLength", len(extraRaw),
				"note", "ExtraData长度不足，可能不包含SlashingInfo")
		}
		return nil
	}

	dposInstance, exists := GetDPoSInstance("vcity_dpos")
	if !exists {
		return fmt.Errorf("DPoS instance not found")
	}

	slashingInfo := extra.SlashingInfo
	p.logger.Info("🔨 🔨 🔨 🔨 开始执行故障消减",
		"blockNumber", block.Number(),
		"epochNumber", slashingInfo.EpochNumber,
		"slashingsCount", len(slashingInfo.Slashings))

	// 对每个消减操作执行消减
	for i, slashingOp := range slashingInfo.Slashings {
		p.logger.Info("🔨 🔨 🔨 🔨 执行消减操作",
			"blockNumber", block.Number(),
			"index", i,
			"validator", slashingOp.ValidatorAddr.String(),
			"slashRate", slashingOp.SlashRate,
			"missedBlocks", slashingOp.MissedBlocks,
			"missedBlocksPercentage", slashingOp.MissedBlocksPercentage,
			"reason", slashingOp.Reason)

		if err := dposInstance.executeSlashing(
			slashingOp.ValidatorAddr,
			slashingOp.SlashRate,
			block.Number(),
			slashingInfo.EpochNumber,
			slashingOp.Reason,
			slashingOp.MissedBlocks,
			slashingOp.MissedBlocksPercentage,
			0, // doubleSigningHeight = 0（故障消减不是双重签名）
		); err != nil {
			p.logger.Error("❌ 执行故障消减失败",
				"blockNumber", block.Number(),
				"validator", slashingOp.ValidatorAddr.String(),
				"error", err)
			return fmt.Errorf("failed to execute slashing for validator %s: %w", slashingOp.ValidatorAddr.String(), err)
		}

		p.logger.Info("✅ 故障消减执行成功（或已执行过，跳过）",
			"blockNumber", block.Number(),
			"validator", slashingOp.ValidatorAddr.String(),
			"slashRate", slashingOp.SlashRate,
			"基点")
	}

	p.logger.Info("✅✅✅ ========== 所有故障消减执行完成 ========== ✅✅✅",
		"blockNumber", block.Number(),
		"slashingsCount", len(slashingInfo.Slashings))

	return nil
}

// GetStateProviderForBlock is an implementation of blockchainBackend interface
func (p *blockchainWrapper) GetStateProviderForBlock(header *types.Header) (contract.Provider, error) {
	transition, err := p.executor.BeginTxn(header.StateRoot, header, types.ZeroAddress)
	if err != nil {
		return nil, err
	}

	return NewStateProvider(transition), nil
}

// GetStateProvider returns a reference to make queries to the provided state
func (p *blockchainWrapper) GetStateProvider(transition *state.Transition) contract.Provider {
	return NewStateProvider(transition)
}

// GetHeaderByNumber is an implementation of blockchainBackend interface
func (p *blockchainWrapper) GetHeaderByNumber(number uint64) (*types.Header, bool) {
	return p.blockchain.GetHeaderByNumber(number)
}

// GetHeaderByHash is an implementation of blockchainBackend interface
func (p *blockchainWrapper) GetHeaderByHash(hash types.Hash) (*types.Header, bool) {
	return p.blockchain.GetHeaderByHash(hash)
}

// GetBlockByHash is an implementation of blockchainBackend interface
func (p *blockchainWrapper) GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool) {
	return p.blockchain.GetBlockByHash(hash, full)
}

// NewBlockBuilder is an implementation of blockchainBackend interface
func (p *blockchainWrapper) NewBlockBuilder(
	parent *types.Header, coinbase types.Address,
	txPool txPoolInterface, blockTime time.Duration, logger hclog.Logger) (blockBuilder, error) {
	gasLimit, err := p.blockchain.CalculateGasLimit(parent.Number + 1)
	if err != nil {
		return nil, err
	}

	return NewBlockBuilder(&BlockBuilderParams{
		BlockTime: blockTime,
		Parent:    parent,
		Coinbase:  coinbase,
		Executor:  p.executor,
		GasLimit:  gasLimit,
		BaseFee:   p.blockchain.CalculateBaseFee(parent),
		TxPool:    txPool,
		Logger:    logger,
	}), nil
}

// GetSystemState is an implementation of blockchainBackend interface
func (p *blockchainWrapper) GetSystemState(provider contract.Provider) SystemState {
	return NewSystemState(contracts.ValidatorSetContract, contracts.StateReceiverContract, provider)
}

func (p *blockchainWrapper) SubscribeEvents() blockchain.Subscription {
	return p.blockchain.SubscribeEvents()
}

func (p *blockchainWrapper) UnubscribeEvents(subscription blockchain.Subscription) {
	p.blockchain.UnsubscribeEvents(subscription)
}

func (p *blockchainWrapper) GetChainID() uint64 {
	return uint64(p.blockchain.Config().ChainID)
}

func (p *blockchainWrapper) GetReceiptsByHash(hash types.Hash) ([]*types.Receipt, error) {
	return p.blockchain.GetReceiptsByHash(hash)
}

// ReadTxLookup returns the block hash using the transaction hash
func (p *blockchainWrapper) ReadTxLookup(hash types.Hash) (types.Hash, bool) {
	return p.blockchain.ReadTxLookup(hash)
}

var _ contract.Provider = &stateProvider{}

type stateProvider struct {
	transition *state.Transition
}

// NewStateProvider initializes EVM against given state and chain config and returns stateProvider instance
// which is an abstraction for smart contract calls
func NewStateProvider(transition *state.Transition) contract.Provider {
	return &stateProvider{transition: transition}
}

// Call implements the contract.Provider interface to make contract calls directly to the state
func (s *stateProvider) Call(addr ethgo.Address, input []byte, opts *contract.CallOpts) ([]byte, error) {
	result := s.transition.Call2(contracts.SystemCaller, types.Address(addr), input, big.NewInt(0), 10000000)
	if result.Failed() {
		return nil, result.Err
	}

	return result.ReturnValue, nil
}

// Txn is part of the contract.Provider interface to make Ethereum transactions. We disable this function
// since the system state does not make any transaction
func (s *stateProvider) Txn(ethgo.Address, ethgo.Key, []byte) (contract.Txn, error) {
	return nil, errSendTxnUnsupported
}
