package dpos

import (
	"bytes"
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
		if err = transition.Write(tx); err != nil {
			return nil, fmt.Errorf("process block tx error, tx = %v, err = %w", tx.Hash, err)
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

	// 🆕 添加详细的epoch检查日志
	epochSize := p.getEpochSize()
	consensusSwitchHeight := uint64(0)
	if p.config != nil {
		consensusSwitchHeight = p.config.ConsensusSwitchHeight
	}

	p.logger.Debug("🔍🔍🔍 ========== blockchain_wrapper.ProcessBlock epoch检查 ========== 🔍🔍🔍",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:16],
		"epochSize", epochSize,
		"consensusSwitchHeight", consensusSwitchHeight)

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 🆕 添加详细的奖励分配跟踪日志
	p.logger.Debug("🔍🔍🔍 ========== blockchain_wrapper.ProcessBlock 奖励分配检查 ========== 🔍🔍🔍",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:16],
		"isEpochEnd", isEpochEnd)

	// 🆕 如果是epoch结束区块且不是生产节点自己生产的区块，处理奖励分发
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

	p.logger.Debug("🔍 isEpochEndBlock: 详细计算",
		"blockNumber", blockNumber,
		"consensusSwitchHeight", consensusSwitchHeight,
		"dposBlockNumber", dposBlockNumber,
		"epochSize", epochSize,
		"currentEpoch", currentEpoch,
		"firstBlockInEpoch", firstBlockInEpoch,
		"lastBlockInEpoch", firstBlockInEpoch+epochSize-1,
		"isEpochEnd", isEpochEnd)

	return isEpochEnd
}

// getEpochSize 根据配置计算epoch大小（区块数）
func (p *blockchainWrapper) getEpochSize() uint64 {
	if p.config == nil {
		// 如果配置为空，使用默认值：10秒 / 2秒 = 5个区块
		epochDuration := 10 * time.Second
		blockTime := 2 * time.Second

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
		blockTime = 2 * time.Second // 默认区块时间
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

		p.logger.Info("🎯🎯🎯🎯🎯🎯🎯🎯验证节点开始处理奖励分发",
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

	// 🆕 处理故障标志
	if len(extra.FaultFlags) > 0 {
		p.logger.Info("🔍 开始处理故障标志", "count", len(extra.FaultFlags))

		// 🆕 更新内存中的故障状态并重新计算出块者列表（只调用一次）
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			// 先更新所有故障状态
			for _, faultFlag := range extra.FaultFlags {
				p.logger.Info("📝 处理故障标志",
					"address", faultFlag.NodeAddress.String(),
					"isFaulty", faultFlag.IsFaulty,
					"missedBlocks", faultFlag.MissedBlocks,
					"reason", faultFlag.Reason)

				// 更新验证者故障状态到数据库
				if err := p.updateValidatorFaultStatus(faultFlag); err != nil {
					p.logger.Error("❌ 更新验证者故障状态失败", "error", err)
				}

				// 更新内存故障状态
				dposInstance.updateMemoryFaultStatus(faultFlag)
			}

			// 重新计算并更新出块者列表（只调用一次）
			if err := dposInstance.updateBlockProducersFromFaultFlags(extra.FaultFlags); err != nil {
				p.logger.Error("❌ 更新出块者列表失败", "error", err)
			}
		}
	} else {
		p.logger.Info("🔍🔍🔍没有故障标志，跳过处理", "blockNumber", block.Number())
	}

	// 🆕 动态计算下一个Epoch的出块者序列
	p.logger.Info("🔍 开始动态计算下一个Epoch验证者集合", "blockNumber", block.Number())
	newValidators, err := p.calculateNextEpochValidators(block.Number(), extra, p.state)
	if err != nil {
		p.logger.Error("❌ 计算下一个Epoch验证者失败", "error", err)
	} else if newValidators != nil {
		p.logger.Info("✅ 动态计算验证者集合完成", "newValidatorsCount", len(newValidators))

		// 🆕 通过回调函数更新验证者集合
		if p.onValidatorsUpdated != nil {
			if err := p.onValidatorsUpdated(newValidators); err != nil {
				p.logger.Error("❌ 更新验证者集合失败", "error", err)
			} else {
				p.logger.Info("✅ 验证者集合更新成功", "newCount", len(newValidators))
			}
		} else {
			p.logger.Warn("⚠️ 验证者更新回调函数未设置")
		}
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

// 🆕 新增：更新验证者故障状态函数
func (p *blockchainWrapper) updateValidatorFaultStatus(faultFlag FaultFlagInfo) error {
	p.logger.Info("🔄 更新验证者故障状态",
		"address", faultFlag.NodeAddress.String(),
		"isFaulty", faultFlag.IsFaulty,
		"missedBlocks", faultFlag.MissedBlocks)

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
		faultFlag.Reason,
	)
	if err != nil {
		p.logger.Error("❌ 更新验证者故障状态失败", "error", err)
		return err
	}

	p.logger.Info("✅ 验证者故障状态更新成功")
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

// 🆕 新增：计算下一个Epoch的验证者集合
func (p *blockchainWrapper) calculateNextEpochValidators(blockNumber uint64, extra *Extra, state *State) (validator.AccountSet, error) {
	p.logger.Info("🔄 ===== 开始计算下一个Epoch的验证者集合 =====", "blockNumber", blockNumber)

	// 检查stake store是否可用
	if state == nil || state.StakeStore == nil {
		p.logger.Error("❌ StakeStore不可用", "state", state != nil, "stakeStore", state != nil && state.StakeStore != nil)
		return nil, fmt.Errorf("stake store not available")
	}

	// 从数据库获取所有验证者
	p.logger.Info("🔍 从数据库获取所有验证者...")
	allValidators, err := state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		p.logger.Error("❌ 获取验证者失败", "error", err)
		return nil, err
	}
	p.logger.Info("✅ 成功获取所有验证者", "count", len(allValidators))

	// 过滤掉故障验证者
	p.logger.Info("🔍 开始过滤故障验证者...")
	activeValidators := make(validator.AccountSet, 0, len(allValidators))
	faultyCount := 0
	for _, validator := range allValidators {
		// 检查是否在故障标志中
		isFaulty := false
		for _, faultFlag := range extra.FaultFlags {
			if faultFlag.NodeAddress == validator.Address && faultFlag.IsFaulty {
				isFaulty = true
				break
			}
		}

		if !isFaulty {
			activeValidators = append(activeValidators, validator)
		} else {
			faultyCount++
			p.logger.Info("🚫 跳过故障验证者", "address", validator.Address.String(), "votingPower", validator.VotingPower.String())
		}
	}
	p.logger.Info("✅ 故障验证者过滤完成", "totalValidators", len(allValidators), "faultyValidators", faultyCount, "activeValidators", len(activeValidators))

	// 按权重排序
	p.logger.Info("🔍 开始按权重排序验证者...")
	sort.Slice(activeValidators, func(i, j int) bool {
		votingPowerCmp := activeValidators[i].VotingPower.Cmp(activeValidators[j].VotingPower)
		if votingPowerCmp != 0 {
			return votingPowerCmp > 0
		}
		return bytes.Compare(activeValidators[i].Address[:], activeValidators[j].Address[:]) < 0
	})

	// 显示排序后的验证者
	p.logger.Info("📊 排序后的验证者列表:")
	for i, validator := range activeValidators {
		p.logger.Info("🏆 排序后验证者", "rank", i+1, "address", validator.Address.String(), "votingPower", validator.VotingPower.String())
	}

	// 截取前N个（从配置读取）
	var maxValidators int
	if p.config != nil && p.config.DPoSValidatorsCount > 0 {
		maxValidators = int(p.config.DPoSValidatorsCount)
		p.logger.Info("✅ 使用配置文件中的DPoSValidatorsCount", "DPoSValidatorsCount", p.config.DPoSValidatorsCount, "maxValidators", maxValidators)
	} else {
		p.logger.Error("❌ 配置读取失败，无法获取验证者数量限制",
			"configIsNil", p.config == nil,
			"DPoSValidatorsCount", func() uint64 {
				if p.config != nil {
					return p.config.DPoSValidatorsCount
				}
				return 0
			}())
		return nil, fmt.Errorf("failed to get validator count limit from config: DPoSValidatorsCount is 0 or config is nil")
	}
	p.logger.Info("🎯 验证者截取逻辑", "maxValidators", maxValidators, "activeValidators", len(activeValidators))

	if len(activeValidators) > maxValidators {
		p.logger.Info("✂️ 截取前N个验证者", "截取前", maxValidators, "原始数量", len(activeValidators))
		activeValidators = activeValidators[:maxValidators]
	} else {
		p.logger.Info("✅ 验证者数量 <= 配置数量，取全部验证者", "取用数量", len(activeValidators), "配置数量", maxValidators)
	}

	// 保存到数据库
	p.logger.Info("💾 保存下一个Epoch验证者集合到数据库...")
	err = state.StakeStore.SaveEpochValidators(activeValidators)
	if err != nil {
		p.logger.Error("❌ 保存下一个Epoch验证者失败", "error", err)
		return nil, err
	}

	// 显示最终结果
	p.logger.Info("🏁 ===== 下一个Epoch验证者集合计算完成 =====")
	p.logger.Info("📋 最终验证者集合:")
	for i, validator := range activeValidators {
		p.logger.Info("🎖️ 最终验证者", "index", i+1, "address", validator.Address.String(), "votingPower", validator.VotingPower.String())
	}
	p.logger.Info("✅ 计算完成统计", "totalValidators", len(allValidators), "finalValidators", len(activeValidators), "maxValidators", maxValidators)

	return activeValidators, nil
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
