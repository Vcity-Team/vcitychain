package dpos

import (
	"errors"
	"fmt"
	"math/big"
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

	// 🆕 添加验证者更新回调函数
	onValidatorsUpdated func(validators validator.AccountSet) error
}

// CurrentHeader returns the header of blockchain block head
func (p *blockchainWrapper) CurrentHeader() *types.Header {
	return p.blockchain.Header()
}

// CommitBlock commits a block to the chain
func (p *blockchainWrapper) CommitBlock(block *types.FullBlock) error {
	// 注意：WriteFullBlock 内部已经有写锁跟踪日志
	return p.blockchain.WriteFullBlock(block, consensusSource)
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
		// 🆕 确保从区块读取的交易补齐 From（RLP不含From，需要本地恢复）
		if tx.From == (types.Address{}) {
			// 🆕 根据当前区块的 forks 状态创建正确的 signer
			// 这样可以正确处理 EIP-1559 (DynamicFeeTx) 交易
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

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 🆕 添加详细的奖励分配跟踪日志
	p.logger.Debug("🔍🔍🔍 ========== blockchain_wrapper.ProcessBlockExecutor 奖励分配检查 ========== 🔍🔍🔍",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:16],
		"blockCreator", blockCreator.String(),
		"isEpochEnd", isEpochEnd)

	// 🆕 如果是epoch结束区块，处理奖励分发
	if isEpochEnd {
		p.logger.Debug("🎯🎯🎯 ========== 开始执行奖励分配 ========== 🎯🎯🎯",
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String()[:16],
			"blockCreator", blockCreator.String())

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

		// 奖励分配与故障统计完成后，再在边界应用已登记的待生效提案，避免被同区块统计覆盖
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			// 计算当前 epoch 编号
			currentEpochMeta := dposInstance.getEpochForBlock(block.Number())
			var currentEpoch uint64
			if currentEpochMeta != nil {
				currentEpoch = currentEpochMeta.Number
			}

			// 优先：从持久化存储扫描当前epoch待生效提案（若实现了该接口）
			var scheduledProps []*ParameterProposal
			if dposInstance.state != nil && dposInstance.state.ProposalStore != nil {
				type schedLister interface {
					ListScheduledByEpoch(epoch uint64) ([]*ParameterProposal, error)
				}
				if l, ok := interface{}(dposInstance.state.ProposalStore).(schedLister); ok {
					if ps, err := l.ListScheduledByEpoch(currentEpoch); err == nil && len(ps) > 0 {
						scheduledProps = append(scheduledProps, ps...)
					}
				}
			}
			// 兜底：遍历内存中的提案
			for _, prop := range dposInstance.parameterProposals {
				if prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch == currentEpoch {
					scheduledProps = append(scheduledProps, prop)
				}
			}

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

			for _, prop := range uniq {
				if prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch == currentEpoch {
					switch prop.ProposalType {
					case "validator_recovery":
					case "parameter":
						// 应用参数更新
						if err := dposInstance.updateParameterValue(prop.Parameter, prop.NewValue, fmt.Sprintf("proposal_%s", prop.ID)); err != nil {
							p.logger.Error("边界应用参数更新失败", "error", err, "proposalID", prop.ID)
						} else {
							prop.Schedule.Applied = true
							prop.Schedule.AppliedAtBlock = block.Number()
							if dposInstance.state != nil && dposInstance.state.ProposalStore != nil {
								_ = dposInstance.state.ProposalStore.SaveProposal(prop)
							}
							p.logger.Info("✅ =========================================边界应用参数更新成功", "proposalID", prop.ID, "parameter", prop.Parameter)
						}
					}
				}
			}
		}
	} else {
		p.logger.Debug("ℹ️ 不是epoch结束区块，跳过奖励分配",
			"blockNumber", block.Number(),
			"isEpochEnd", isEpochEnd)
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
		if err = transition.Write(tx); err != nil {
			return nil, fmt.Errorf("process block tx error, tx = %v, err = %w", tx.Hash, err)
		}
	}

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 🆕 如果是epoch结束区块且不是生产节点自己生产的区块，处理奖励分发和下一个epoch验证者集合
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

		// 🆕 从ExtraData读取下一个epoch的验证者集合并保存到数据库
		if err := p.processNextEpochValidatorsFromExtraData(block); err != nil {
			p.logger.Error("❌ 处理下一个epoch验证者集合失败",
				"blockNumber", block.Number(),
				"blockHash", block.Hash().String()[:16],
				"error", err)
			// 不返回错误，继续处理其他逻辑
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
		// 如果配置为空，使用默认值：86400秒 / 3秒 = 28800个区块
		epochDuration := 86400 * time.Second
		blockTime := 3 * time.Second

		epochSize := uint64(epochDuration / blockTime)
		if epochSize == 0 {
			epochSize = 1 // 至少1个区块
		}
		return epochSize
	}

	// 使用配置文件中的值
	epochDuration := p.config.EpochDuration
	blockTime := p.config.BlockTime.Duration

	if blockTime == 0 {
		blockTime = 3 * time.Second // 默认区块时间
	}

	epochSize := uint64(epochDuration / blockTime)
	if epochSize == 0 {
		epochSize = 1 // 至少1个区块
	}

	return epochSize
}

// processRewardDistributionInBlock 在区块执行时处理奖励分发
func (p *blockchainWrapper) processRewardDistributionInBlock(block *types.Block, transition *state.Transition) error {
	p.logger.Info("🔍🔍🔍 ==========验证中processRewardDistributionInBlock 开始 ========== 🔍🔍🔍",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:16],
		"extraDataLength", len(block.Header.ExtraData))

	// 解析ExtraData获取奖励分发信息
	extra := &Extra{}
	if err := extra.UnmarshalRLP(block.Header.ExtraData); err != nil {
		p.logger.Error("❌ 解析ExtraData失败",
			"blockNumber", block.Number(),
			"error", err,
			"extraDataLength", len(block.Header.ExtraData))
		return fmt.Errorf("failed to unmarshal extra data: %w", err)
	}

	p.logger.Info("✅ ExtraData解析成功",
		"blockNumber", block.Number(),
		"hasRewardDistribution", extra.RewardDistribution != nil,
		"hasFaultFlags", len(extra.FaultFlags) > 0)

	// 🔍 添加详细的奖励信息日志
	if extra.RewardDistribution != nil {
		p.logger.Info("💰 ExtraData包含奖励信息",
			"blockNumber", block.Number(),
			"epoch", extra.RewardDistribution.EpochNumber,
			"rewardCount", len(extra.RewardDistribution.Rewards),
			"totalReward", extra.RewardDistribution.TotalReward.String())

		rewardInfo := extra.RewardDistribution

		p.logger.Info("1.🎯🎯🎯🎯🎯🎯🎯🎯验证节点开始处理奖励分发",
			"blockNumber", block.Number(),
			"rewardCount", len(rewardInfo.Rewards),
			"epoch", rewardInfo.EpochNumber,
			"totalReward", rewardInfo.TotalReward.String())

		// 获取奖励账户地址（从配置中获取）
		rewardAccount := types.StringToAddress("0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe")

		// 计算总奖励金额
		totalReward := new(big.Int)
		for addrStr, amount := range rewardInfo.Rewards {
			totalReward.Add(totalReward, amount)
			p.logger.Info("💰 奖励详情",
				"blockNumber", block.Number(),
				"validator", addrStr,
				"amount", amount.String())
		}

		p.logger.Info("💰 总奖励计算完成",
			"blockNumber", block.Number(),
			"totalReward", totalReward.String(),
			"rewardAccount", rewardAccount.String())

		// 检查奖励账户余额是否足够
		currentBalance := transition.GetBalance(rewardAccount)

		p.logger.Info("💳 检查奖励账户余额",
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
		p.logger.Info("✅ 从奖励账户扣除总奖励",
			"blockNumber", block.Number(),
			"amount", totalReward.String())

		cnt := 0
		// 直接给每个验证者增加余额（不消耗gas，符合TRON做法）
		for addrStr, amount := range rewardInfo.Rewards {
			addr := types.StringToAddress(addrStr)

			// 直接修改状态，不通过Transfer
			transition.Txn().AddBalance(addr, amount)
			p.logger.Info("✅ 给验证者增加余额",
				"blockNumber", block.Number(),
				"validator", addrStr,
				"amount", amount.String())
			cnt++
		}
		if cnt == len(rewardInfo.Rewards) {
			p.logger.Info("✅ 所有验证者处理完毕",
				"cnt", cnt)
		}
	} else {
		p.logger.Warn("⚠️ ExtraData解析后RewardDistribution为nil",
			"blockNumber", block.Number(),
			"extraDataLength", len(block.Header.ExtraData))
	}

	// 预先收集：需要在本epoch边界应用的恢复提案
	recoveredValidators := make(map[types.Address]*ParameterProposal)

	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		if dposInstance.state != nil && dposInstance.state.ProposalStore != nil {
			// 计算当前epoch用于日志与筛选
			epochForLog := uint64(0)
			if meta := dposInstance.getEpochForBlock(block.Number()); meta != nil {
				epochForLog = meta.Number
			}
			allProposals, err := dposInstance.state.ProposalStore.GetAllProposals()
			if err == nil {
				p.logger.Info("[DEBUG_ProposalStore_FullDump] 当前所有提案总数", "count", len(allProposals), "currentEpoch", epochForLog)
				for pid, prop := range allProposals {
					p.logger.Info("[DEBUG_ProposalStore_FullDump]", "id", pid, "Type", prop.ProposalType, "Epoch", prop.Schedule.EffectiveEpoch, "Scheduled", prop.Schedule.Scheduled, "Validator", prop.ValidatorAddress.String(), "Status", prop.Status, "Start", prop.StartBlock, "End", prop.EndBlock, "Description", prop.Description, "currentEpoch", epochForLog)
					// 直接基于全量数据筛选“本epoch需要生效”的恢复提案
					if prop != nil && prop.ProposalType == "validator_recovery" && prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch == epochForLog {
						vaddr := prop.ValidatorAddress
						if vaddr == (types.Address{}) {
							vaddr = types.StringToAddress(prop.Parameter)
						}
						recoveredValidators[vaddr] = prop
						p.logger.Info("++++++++[Proposal][边界恢复提案调试] 收集需要apply的恢复提案", "ID", prop.ID, "Type", prop.ProposalType, "Scheduled", prop.Schedule.Scheduled, "EffEpoch", prop.Schedule.EffectiveEpoch, "Validator", vaddr.String(), "Parameter", prop.Parameter, "currentEpoch", epochForLog)
					}
				}
			} else {
				p.logger.Error("[DEBUG_ProposalStore_FullDump] GetAllProposals error", "error", err, "currentEpoch", epochForLog)
			}
		}
	}

	// 🆕 处理故障标志
	if len(extra.FaultFlags) > 0 {
		p.logger.Info("🔍 开始处理故障标志", "count", len(extra.FaultFlags))
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			for i := range extra.FaultFlags {
				ff := &extra.FaultFlags[i]
				if pprop, ok := recoveredValidators[ff.NodeAddress]; ok {
					p.logger.Info("✅【EXTRA修正】本epoch恢复提案清零", "address", ff.NodeAddress.String())
					ff.MissedBlocks = 0
					ff.IsFaulty = false
					ff.Reason = "本epoch恢复提案生效，统计清零"

					// 立刻持久化到与读取口径一致的数据库，确保后续读取不再视为故障
					if dposInstance.state != nil && dposInstance.state.StakeStore != nil {
						if err := dposInstance.state.StakeStore.ClearValidatorFaultStatus(ff.NodeAddress, pprop.ID); err != nil {
							p.logger.Error("❌ ClearValidatorFaultStatus 持久化失败", "address", ff.NodeAddress.String(), "error", err)
						} else {
							p.logger.Info("✅ ClearValidatorFaultStatus 持久化成功", "address", ff.NodeAddress.String())
						}
					}
					// 同步内存：从故障集合中剔除
					if dposInstance.faultyValidators != nil {
						delete(dposInstance.faultyValidators, ff.NodeAddress)
					}

					// 在修正后设置提案applied并保存
					pprop.Schedule.Applied = true
					pprop.Schedule.AppliedAtBlock = block.Number()
					if dposInstance.state != nil && dposInstance.state.ProposalStore != nil {
						_ = dposInstance.state.ProposalStore.SaveProposal(pprop)
					}
					// 计算当前epoch用于日志
					epochForLog := uint64(0)
					if meta := dposInstance.getEpochForBlock(block.Number()); meta != nil {
						epochForLog = meta.Number
					}
					p.logger.Info("✅ ===============================================边界应用恢复提案成功", "proposalID", pprop.ID, "validator", ff.NodeAddress.String(), "currentEpoch", epochForLog)
				}

				if ff.IsFaulty {
					p.logger.Info("📝 处理故障标志",
						"address", ff.NodeAddress.String(),
						"isFaulty", ff.IsFaulty,
						"missedBlocks", ff.MissedBlocks,
						"reason", ff.Reason)
				}
				// 更新验证者故障状态到数据库
				if err := p.updateValidatorFaultStatus(*ff); err != nil {
					p.logger.Error("❌ 更新验证者故障状态失败", "error", err)
				}
				// 更新内存故障状态
				dposInstance.updateMemoryFaultStatus(*ff)
			}

			// 🆕 调用前打印最终的 FaultFlags 快照
			for i := range extra.FaultFlags {
				ff := &extra.FaultFlags[i]
				if !ff.IsFaulty {
					continue
				}
				p.logger.Info("📸 FaultFlags 最终快照",
					"index", i,
					"address", ff.NodeAddress.String(),
					"isFaulty", ff.IsFaulty,
					"missedBlocks", ff.MissedBlocks,
					"reason", ff.Reason)
			}

			// 重新计算并更新出块者列表（只调用一次）
			if err := dposInstance.updateBlockProducersFromFaultFlags(extra.FaultFlags); err != nil {
				p.logger.Error("❌ 更新出块者列表失败", "error", err)
			}
		}
	} else {
		p.logger.Info("🔍🔍🔍没有故障标志，跳过处理", "blockNumber", block.Number())
	}

	// 🔍 添加详细的判断前检查日志
	p.logger.Info("🔍 准备检查RewardDistribution",
		"blockNumber", block.Number(),
		"RewardDistribution==nil", extra.RewardDistribution == nil,
		"transition==nil", transition == nil,
		"extraDataLength", len(block.Header.ExtraData))

	if extra.RewardDistribution == nil {
		// 没有奖励分发信息，跳过
		p.logger.Info("ℹ️ 没有奖励分发信息，跳过",
			"blockNumber", block.Number(),
			"extraDataLength", len(block.Header.ExtraData))
		return nil
	}

	return nil
}

// processNextEpochValidatorsFromExtraData 从ExtraData读取下一个epoch的验证者集合并保存到数据库
func (p *blockchainWrapper) processNextEpochValidatorsFromExtraData(block *types.Block) error {
	p.logger.Info("🔄 ===== 开始处理下一个epoch验证者集合 =====",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:16],
		"extraDataLength", len(block.Header.ExtraData))

	// 解析ExtraData获取下一个epoch的验证者集合
	extra := &Extra{}
	if err := extra.UnmarshalRLP(block.Header.ExtraData); err != nil {
		p.logger.Error("❌ 解析ExtraData失败",
			"blockNumber", block.Number(),
			"error", err,
			"extraDataLength", len(block.Header.ExtraData))
		return fmt.Errorf("failed to unmarshal extra data: %w", err)
	}

	// 检查是否有下一个epoch的验证者集合
	if len(extra.NextEpochValidators) == 0 {
		p.logger.Warn("⚠️ ExtraData中没有下一个epoch的验证者集合",
			"blockNumber", block.Number())
		return nil // 不是错误，可能不是epoch边界区块或出块节点没有设置
	}

	nextEpochValidators := extra.NextEpochValidators
	source := "extra_data"

	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		if localValidators, err := dposInstance.calculateNextEpochValidators(block.Number()); err != nil {
			p.logger.Error("❌ 本地计算下一个epoch验证者失败，回退到ExtraData",
				"blockNumber", block.Number(),
				"error", err)
		} else if len(localValidators) > 0 {
			nextEpochValidators = localValidators
			source = "local_calculation"
		}
	}

	p.logger.Info("✅ 确定下一个epoch的验证者集合",
		"blockNumber", block.Number(),
		"source", source,
		"nextEpochValidatorsCount", len(nextEpochValidators))

	// 打印下一个epoch的验证者列表
	for i, validator := range nextEpochValidators {
		p.logger.Info("📋 下一个epoch验证者",
			"blockNumber", block.Number(),
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String())
	}

	// 获取DPoS实例并保存到数据库
	if p.state == nil || p.state.StakeStore == nil {
		p.logger.Error("❌ StakeStore不可用",
			"blockNumber", block.Number())
		return fmt.Errorf("stake store not available")
	}

	// 保存下一个epoch的验证者集合到数据库
	if err := p.state.StakeStore.SaveEpochValidators(nextEpochValidators); err != nil {
		p.logger.Error("❌ 保存下一个epoch验证者集合失败",
			"blockNumber", block.Number(),
			"error", err)
		return fmt.Errorf("failed to save next epoch validators: %w", err)
	}

	p.logger.Info("✅✅✅ ========== 下一个epoch验证者集合已保存到数据库 ========== ✅✅✅",
		"blockNumber", block.Number(),
		"nextEpochValidatorsCount", len(nextEpochValidators),
		"note", "所有节点（包括出块节点自己）都会从ExtraData读取并保存到本地数据库")

	return nil
}

// 🆕 新增：更新验证者故障状态函数
func (p *blockchainWrapper) updateValidatorFaultStatus(faultFlag FaultFlagInfo) error {
	if faultFlag.IsFaulty {
		p.logger.Info("🔄 更新验证者故障状态",
			"address", faultFlag.NodeAddress.String(),
			"isFaulty", faultFlag.IsFaulty,
			"missedBlocks", faultFlag.MissedBlocks)
	}

	// 检查stake store是否可用
	if p.state == nil || p.state.StakeStore == nil {
		return fmt.Errorf("stake store not available")
	}

	// 更新验证者故障状态到数据库
	err := p.state.StakeStore.UpdateValidatorFaultStatus(
		faultFlag.NodeAddress,
		faultFlag.IsFaulty,
		faultFlag.MissedBlocks,
		faultFlag.LastUpdateTime,
		faultFlag.LastFaultyEpoch,
		faultFlag.Reason,
	)
	if err != nil {
		p.logger.Error("❌ 更新验证者故障状态失败", "error", err)
		return err
	}
	return nil
}

// 🆕 新增：获取父区块验证者集合函数
func (p *blockchainWrapper) getParentValidators(blockNumber uint64) (validator.AccountSet, error) {
	if blockNumber == 0 {
		return nil, fmt.Errorf("genesis block has no parent")
	}

	// 获取父区块
	parentBlock, exists := p.config.Blockchain.GetHeaderByNumber(blockNumber - 1)
	if !exists {
		return nil, fmt.Errorf("parent block not found: %d", blockNumber-1)
	}

	// 解析父区块的Extra字段
	var extra Extra
	if err := extra.UnmarshalRLP(parentBlock.ExtraData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal parent block extra: %v", err)
	}

	// 获取父区块的验证者集合
	if extra.Validators == nil {
		return nil, fmt.Errorf("parent block has no validators")
	}

	// 应用验证者集合变化
	parentValidators := validator.AccountSet{}
	for _, v := range extra.Validators.Added {
		parentValidators = append(parentValidators, v)
	}

	return parentValidators, nil
}

// 🆕 新增：更新数据库中的验证者集合函数
func (p *blockchainWrapper) updateValidatorsInDatabase(validators validator.AccountSet) error {
	p.logger.Info("💾 更新数据库中的验证者集合", "count", len(validators))

	// 检查stake store是否可用
	if p.state == nil || p.state.StakeStore == nil {
		return fmt.Errorf("stake store not available")
	}

	// 保存验证者集合到数据库
	err := p.state.StakeStore.SaveEpochValidators(validators)
	if err != nil {
		p.logger.Error("❌ 保存验证者集合失败", "error", err)
		return err
	}

	p.logger.Info("✅ 验证者集合更新成功")
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
