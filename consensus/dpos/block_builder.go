package dpos

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/bitmap"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/txpool"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// globalNewStateRoot 全局变量，用于传递executeBatchStateUpdate计算出的新状态根
var (
	globalNewStateRoot types.Hash
	// globalNewStateRootMutex 保护globalNewStateRoot的读写锁
	globalNewStateRootMutex sync.RWMutex
)

// BlockBuilderParams are fields for the block that cannot be changed
type BlockBuilderParams struct {
	// Parent block
	Parent *types.Header

	// Executor
	Executor *state.Executor

	// Coinbase that is signing the block
	Coinbase types.Address

	// GasLimit is the gas limit for the block
	GasLimit uint64

	// duration for one block
	BlockTime time.Duration

	// Logger
	Logger hclog.Logger

	// txPoolInterface implementation
	TxPool txPoolInterface

	// BaseFee is the base fee
	BaseFee uint64
}

// NewBlockBuilder creates a new block builder
func NewBlockBuilder(params *BlockBuilderParams) blockBuilder {
	return &BlockBuilder{
		params: params,
	}
}

var _ blockBuilder = &BlockBuilder{}

// BlockBuilder implements the blockBuilder interface
type BlockBuilder struct {
	// input params for the block
	params *BlockBuilderParams

	// header is the header for the block being created
	header *types.Header

	// transactions are the data included in the block
	txns []*types.Transaction

	// block is a reference to the already built block
	block *types.Block

	// state is in memory state transition
	state *state.Transition
}

// Reset initializes block builder before adding transactions and actual block building
func (b *BlockBuilder) Reset() error {
	// set the timestamp
	parentTime := time.Unix(int64(b.params.Parent.Timestamp), 0)
	headerTime := parentTime.Add(b.params.BlockTime)

	if headerTime.Before(time.Now().UTC()) {
		headerTime = time.Now().UTC()
	}

	b.header = &types.Header{
		ParentHash:   b.params.Parent.Hash,
		Number:       b.params.Parent.Number + 1,
		Miner:        b.params.Coinbase[:],
		Difficulty:   1,
		StateRoot:    types.EmptyRootHash, // this avoids needing state for now
		TxRoot:       types.EmptyRootHash,
		ReceiptsRoot: types.EmptyRootHash, // this avoids needing state for now
		Sha3Uncles:   types.EmptyUncleHash,
		GasLimit:     b.params.GasLimit,
		BaseFee:      b.params.BaseFee,
		Timestamp:    uint64(headerTime.Unix()),
		MixHash:      PolyBFTMixDigest, // 🆕 设置 MixHash 以通过验证
	}

	transition, err := b.params.Executor.BeginTxn(b.params.Parent.StateRoot, b.header, b.params.Coinbase)
	if err != nil {
		return err
	}

	b.state = transition
	b.block = nil
	b.txns = []*types.Transaction{}

	return nil
}

// Build creates the state and the final block
func (b *BlockBuilder) Build(handler func(h *types.Header)) (*types.FullBlock, error) {
	if handler != nil {
		handler(b.header)
	}

	_, stateRoot, err := b.state.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to commit the state changes: %w", err)
	}

	b.header.StateRoot = stateRoot
	b.header.GasUsed = b.state.TotalGas()
	b.header.LogsBloom = types.CreateBloom(b.Receipts())

	// build the block
	b.block = consensus.BuildBlock(consensus.BuildBlockParams{
		Header:   b.header,
		Txns:     b.txns,
		Receipts: b.state.Receipts(),
	})

	b.block.Header.ComputeHash()

	return &types.FullBlock{
		Block:    b.block,
		Receipts: b.state.Receipts(),
	}, nil
}

// WriteTx applies given transaction to the state. If transaction apply fails, it reverts the saved snapshot.
func (b *BlockBuilder) WriteTx(tx *types.Transaction) error {
	if tx.Gas > b.params.GasLimit {
		b.params.Logger.Info("Transaction gas limit exceedes block gas limit", "hash", tx.Hash,
			"tx gas limit", tx.Gas, "block gas limt", b.params.GasLimit)

		return txpool.ErrBlockLimitExceeded
	}

	if err := b.state.Write(tx); err != nil {
		b.params.Logger.Error("💀 交易应用到状态失败，程序将立即退出",
			"txHash", tx.Hash.String(),
			"nonce", tx.Nonce,
			"from", tx.From.String(),
			"error", err)
		os.Exit(1)
		return err
	}

	b.txns = append(b.txns, tx)

	return nil
}

// Fill fills the block with transactions from the txpool
// 🆕 完全对标以太坊：批量打包多笔交易，使用当前区块状态检查nonce
// 修复：不要每次都调用Prepare()，而是使用当前构建区块的状态来检查nonce
// 这样可以在一个区块中打包多笔交易，类似以太坊
func (b *BlockBuilder) Fill() {
	// 只在开始时调用一次Prepare()，初始化executables队列
	b.params.TxPool.Prepare()

	txCount := 0
	skippedCount := 0
	blockNumber := b.params.Parent.Number + 1
	maxConsecutiveSkips := 10 // 最多连续跳过10笔交易后重新Prepare()

	b.params.Logger.Debug("🔵 [BlockBuilder.Fill] 开始填充区块",
		"blockNumber", blockNumber)

	consecutiveSkips := 0
	for {
		tx := b.params.TxPool.Peek()

		// 如果没有交易，尝试重新Prepare()（因为可能有新交易进入交易池）
		if tx == nil {
			// 如果连续跳过太多交易，重新Prepare()（最多重试3次）
			if consecutiveSkips < maxConsecutiveSkips {
				b.params.Logger.Debug("⚠️ [BlockBuilder.Fill] executables队列为空，重新Prepare()",
					"blockNumber", blockNumber,
					"txCount", txCount,
					"skippedCount", skippedCount,
					"consecutiveSkips", consecutiveSkips,
					"note", "尝试重新准备交易")
				b.params.TxPool.Prepare()
				tx = b.params.TxPool.Peek()
				consecutiveSkips = 0 // 重置连续跳过计数
			}

			// 如果还是没有交易，返回
			if tx == nil {
				b.params.Logger.Debug("🔵 [BlockBuilder.Fill] 填充完成，没有更多交易",
					"blockNumber", blockNumber,
					"txCount", txCount,
					"skippedCount", skippedCount)
				return
			}
		}

		txCount++

		// 🆕 关键修复：使用当前构建区块的状态来检查nonce（不是父区块状态）
		// 这样可以看到当前区块已打包交易对nonce的影响
		accountNonce := b.state.GetNonce(tx.From)
		if tx.Nonce != accountNonce {
			// nonce不匹配，跳过这个交易
			skippedCount++
			consecutiveSkips++
			b.params.Logger.Info("⚠️ [BlockBuilder.Fill] nonce不匹配，跳过交易",
				"blockNumber", blockNumber,
				"txHash", tx.Hash.String()[:16],
				"from", tx.From.String()[:16],
				"txNonce", tx.Nonce,
				"accountNonce", accountNonce,
				"skippedCount", skippedCount,
				"txCount", txCount,
				"consecutiveSkips", consecutiveSkips,
				"note", "当前区块状态nonce已更新，交易nonce不匹配")
			b.params.TxPool.Pop(tx) // 移除这个交易，Pop()会自动将下一笔交易添加到executables队列
			continue                // 继续处理下一个交易
		}

		// nonce匹配，重置连续跳过计数
		consecutiveSkips = 0

		// execute transactions one by one
		// 🆕 writeTxPoolTransaction内部会调用Pop()，所以这里不需要再次调用
		finished, err := b.writeTxPoolTransaction(tx)
		if err != nil {
			b.params.Logger.Error("💀 交易填充失败，程序将立即退出",
				"txHash", tx.Hash.String(),
				"nonce", tx.Nonce,
				"from", tx.From.String(),
				"error", err)
			os.Exit(1)
		}

		// 🆕 writeTxPoolTransaction内部已经调用了Pop()，会自动将同一账户的下一笔交易添加到executables队列（如果存在）
		// 这样就不需要每次都调用Prepare()了

		// 区块已满（GasLimit 达到），立即返回
		if finished {
			b.params.Logger.Info("🔵 [BlockBuilder.Fill] 区块已满（GasLimit），停止填充",
				"blockNumber", blockNumber,
				"txCount", txCount,
				"skippedCount", skippedCount)
			return
		}

		// 🆕 修复：不再每次都调用Prepare()
		// 因为：
		// 1. Pop()会自动将同一账户的下一笔交易添加到executables队列
		// 2. 我们使用b.state.GetNonce()来检查nonce，这是当前构建区块的状态
		// 3. 这样可以批量打包多笔交易，类似以太坊
	}
}

// Receipts returns the collection of transaction receipts for given block
func (b *BlockBuilder) Receipts() []*types.Receipt {
	return b.state.Receipts()
}

func (b *BlockBuilder) writeTxPoolTransaction(tx *types.Transaction) (bool, error) {
	if tx == nil {
		return true, nil
	}

	if err := b.WriteTx(tx); err != nil {
		if _, ok := err.(*state.GasLimitReachedTransitionApplicationError); ok { //nolint:errorlint
			// stop processing
			return true, err
		} else if appErr, ok := err.(*state.TransitionApplicationError); ok && appErr.IsRecoverable { //nolint:errorlint
			// 可恢复错误，记录日志但不退出
			b.params.TxPool.Demote(tx)

			return false, err
		} else {
			// 不可恢复错误，退出程序
			b.params.Logger.Error("💀 交易写入失败，程序将立即退出",
				"txHash", tx.Hash.String(),
				"nonce", tx.Nonce,
				"from", tx.From.String(),
				"error", err)
			os.Exit(1)
			b.params.TxPool.Drop(tx)

			return false, err
		}
	}

	// remove tx from the pool and add it to the list of all block transactions
	b.params.TxPool.Pop(tx)

	return false, nil
}

// GetState returns Transition reference
func (b *BlockBuilder) GetState() *state.Transition {
	return b.state
}

// SignatureListener 签名响应监听器
type SignatureListener struct {
	checkpointHash types.Hash
	signatureCh    chan<- *SignatureResponse
	receivedSigs   map[types.Address]bool
	receivedMutex  sync.RWMutex // 添加互斥锁保护 receivedSigs map
	topic          *network.Topic
	logger         hclog.Logger
}

// Close 关闭监听器
func (sl *SignatureListener) Close() {
	// 清理接收的签名记录
	sl.receivedMutex.Lock()
	sl.receivedSigs = make(map[types.Address]bool)
	sl.receivedMutex.Unlock()

	// 注意：这里不能直接关闭topic，因为topic可能被其他监听器使用
	// 我们只是清理引用，避免资源泄漏
	if sl.topic != nil {
		sl.topic = nil
	}

	// 清理日志引用
	sl.logger = nil
}

// SignatureRequest 签名请求消息
type SignatureRequest struct {
	BlockNumber    uint64        `json:"blockNumber"`
	BlockHash      types.Hash    `json:"blockHash"`
	CheckpointHash types.Hash    `json:"checkpointHash"`
	Round          uint64        `json:"round"`
	Proposer       types.Address `json:"proposer"`
	Timestamp      uint64        `json:"timestamp"`
}

// SignatureResponse 签名响应消息
type SignatureResponse struct {
	ValidatorAddr  types.Address `json:"validatorAddr"`
	Signature      []byte        `json:"signature"`
	CheckpointHash types.Hash    `json:"checkpointHash"`
	Timestamp      uint64        `json:"timestamp"`
}

// buildBlock 构建区块并收集验证者签名
func (r *dposRuntime) buildBlock() (*types.FullBlock, error) {
	buildStartTime := time.Now()
	r.logger.Info("🏗️ buildBlock函数被调用", "timestamp", buildStartTime.Format("15:04:05.000"))

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot build block")
		return nil, fmt.Errorf("key not available, cannot build block")
	}

	// 获取父区块
	parent := r.config.blockchain.CurrentHeader()

	// 🆕 检查并应用延迟状态更新（只在非epoch结束区块时应用）
	nextBlockNumber := parent.Number + 1
	isEpochEndBlock := r.isEpochEndBlock(nextBlockNumber)

	if isEpochEndBlock {
		if r.config != nil && r.config.dposBackend != nil {
			if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
				dposInstance.processEpochBoundary(r, parent, nextBlockNumber)
			}
		}
	} else {
		r.nextEpochValidators = nil
	}

	// 创建区块构建器
	keyAddr := types.Address(r.config.Key.Address())

	// 从配置读取blockTime，如果没有配置则使用默认值3秒
	blockTime := r.config.BlockTime.Duration
	if blockTime == 0 {
		r.logger.Warn("⚠️ blockTime为0，使用默认值3秒")
		blockTime = 3 * time.Second
	}

	builder, err := r.config.blockchain.NewBlockBuilder(
		parent,
		keyAddr,
		r.config.txPool,
		blockTime, // 从配置读取的区块时间
		r.logger,
	)
	if err != nil {
		r.logger.Error("❌ buildBlock: 创建区块构建器失败", "error", err)
		return nil, fmt.Errorf("failed to create block builder: %w", err)
	}

	// 重置构建器
	if err := builder.Reset(); err != nil {
		r.logger.Error("❌ buildBlock: 重置构建器失败", "error", err)
		return nil, fmt.Errorf("failed to reset block builder: %w", err)
	}

	// 尝试获取更详细的交易池信息
	if txPool, ok := r.config.txPool.(interface {
		GetTxs(inclQueued bool) (map[types.Address][]*types.Transaction, map[types.Address][]*types.Transaction)
	}); ok {
		allPromoted, allEnqueued := txPool.GetTxs(true)

		// 检查每个账户的状态
		for addr, promotedTxs := range allPromoted {
			r.logger.Debug("账户已提升交易", "address", addr.String(), "count", len(promotedTxs))

			// 获取账户在区块链中的当前 nonce
			if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
				// 记录当前区块信息，帮助诊断 nonce 问题
				r.logger.Debug("当前区块状态", "blockNumber", currentHeader.Number, "stateRoot", currentHeader.StateRoot.String())
			}

			for i, tx := range promotedTxs {
				if tx != nil {
					r.logger.Debug("已提升交易", "index", i, "hash", tx.Hash.String(), "nonce", tx.Nonce)
				} else {
					r.logger.Warn("发现空交易", "index", i, "address", addr.String())
				}
			}
		}

		for addr, enqueuedTxs := range allEnqueued {
			r.logger.Debug("账户待提升交易", "address", addr.String(), "count", len(enqueuedTxs))
			for i, tx := range enqueuedTxs {
				if tx != nil {
					r.logger.Debug("待提升交易", "index", i, "hash", tx.Hash.String(), "nonce", tx.Nonce)
				} else {
					r.logger.Warn("发现空交易", "index", i, "address", addr.String())
				}
			}
		}
	}

	builder.Fill()

	// 检查填充后的交易数量
	if blockBuilder, ok := builder.(interface {
		GetTransactions() []*types.Transaction
	}); ok {
		txs := blockBuilder.GetTransactions()
		if len(txs) > 0 {
			for i, tx := range txs {
				if tx != nil {
					r.logger.Info("区块中的交易", "index", i, "hash", tx.Hash.String(), "nonce", tx.Nonce)
				} else {
					r.logger.Warn("发现空交易", "index", i)
				}
			}
		} else {
			r.logOnce("no_transactions", "debug", "区块中没有包含任何交易！")
		}
	}

	// 先计算验证者哈希，用于CheckpointData
	currentBlock := r.config.blockchain.CurrentHeader()

	// 获取父区块信息，与验证时保持一致
	var parents []*types.Header
	if currentBlock.Number > 0 {
		parentHeader, exists := r.config.blockchain.GetHeaderByNumber(currentBlock.Number - 1)
		if exists && parentHeader != nil {
			parents = append(parents, parentHeader)
		}
	}

	// 用与验证时完全相同的验证者集合获取方法
	// 验证时：getValidatorsFromExtraData(header, parent, parents, consensusBackend, logger)
	// 生产时：从当前区块的ExtraData解析验证者集合
	productionValidators, err := r.getValidatorsFromExtraDataForProduction(currentBlock, parents)
	if err != nil {
		r.logger.Error("❌ 生产时无法获取验证者集合", "blockNumber", currentBlock.Number, "error", err)
		return nil, fmt.Errorf("failed to get validators for production: %w", err)
	}

	// 缓存第一次成功获取的验证者集合，确保整个区块生产过程中使用相同的验证者
	r.cachedProductionValidators = productionValidators.Copy()
	r.logger.Debug("💾 已缓存生产时验证者集合", "validatorsCount", len(productionValidators))

	r.logger.Debug("🔍 生产时开始计算验证者哈希", "validatorsCount", len(productionValidators))
	currentValidatorsHash, err := productionValidators.HashAddressOnly()
	if err != nil {
		r.logger.Error("failed to calculate current validators hash", "error", err)
		return nil, fmt.Errorf("failed to calculate current validators hash: %w", err)
	}
	r.logger.Debug("🔍 生产时验证者哈希计算结果", "currentValidatorsHash", currentValidatorsHash.String())

	// 创建正确的Extra对象，包含必要的字段
	extra := &Extra{
		Committed: &Signature{}, // 添加空的Committed签名
		Checkpoint: &CheckpointData{
			BlockRound:            r.currentRound,
			EpochNumber:           1,
			CurrentValidatorsHash: currentValidatorsHash,
			NextValidatorsHash:    currentValidatorsHash, // 暂时使用相同的哈希
			EventRoot:             types.Hash{},          // 暂时为空
		},
		// 🆕 初始化CheckpointBlockHash为空，稍后会设置
		CheckpointBlockHash: types.Hash{},
	}
	// 延迟状态更新机制已移除，奖励分发在epoch结束区块直接执行
	r.logger.Debug("🔍 检查是否需要计算奖励分发",
		"nextBlockNumber", nextBlockNumber,
		"parentNumber", parent.Number,
		"delegate", keyAddr.String()[:16])

	isEpochEndBlock = r.isEpochEndBlock(nextBlockNumber)
	if isEpochEndBlock {
		r.logger.Info("🎯 EPOCH最后一个区块 + 当前出块者",
			"blockNumber", nextBlockNumber,
			"delegate", keyAddr.String()[:16],
			"isEpochEndBlock", isEpochEndBlock,
			"isCurrentProducer", true,
			"timestamp", time.Now().Format("2006-01-02 15:04:05"))

		// 执行奖励分发，传递当前轮次和出块者地址
		if err := r.executeRewardDistributionForEpochEnd(nextBlockNumber, r.currentRound, keyAddr); err != nil {
			r.logger.Error("❌ ========== 计算奖励信息失败 ========== ❌",
				"blockNumber", nextBlockNumber,
				"delegate", keyAddr.String()[:16],
				"error", err)
			// 不返回错误，继续构建区块，但记录错误
		} else {
			r.logger.Info("✅ ========== 计算奖励信息成功 ========== ✅",
				"blockNumber", nextBlockNumber,
				"delegate", keyAddr.String()[:16],
				"action", "REWARD_DISTRIBUTION_SUCCESS")
		}

		// 🆕 注意：故障检测已在前面执行（在计算验证者集合之前），这里不再重复执行
	} else {
		r.logger.Debug("ℹ️ 不是epoch最后一个区块，跳过奖励分发",
			"blockNumber", nextBlockNumber,
			"isEpochEndBlock", isEpochEndBlock)
	}

	// 如果是epoch结束区块，在生产节点也执行奖励分配
	if isEpochEndBlock {
		state := builder.GetState()
		if state != nil {
			// 执行奖励分配
			if err := r.processRewardDistributionInBlockForBuilder(builder, nextBlockNumber); err != nil {
				r.logger.Error("❌ 生产节点奖励分配失败", "blockNumber", nextBlockNumber, "error", err)
				return nil, fmt.Errorf("failed to process reward distribution in buildBlock: %w", err)
			}
		} else {
			r.logger.Error("❌ 生产节点无法获取状态", "blockNumber", nextBlockNumber)
		}
	}

	// 构建区块
	buildStart := time.Now()

	block, err := builder.Build(func(h *types.Header) {
		// 设置DPoS相关的区块头信息
		h.Miner = keyAddr[:]
		h.Difficulty = 1
		h.ExtraData = extra.MarshalRLPTo(nil)

		// 🆕 如果是epoch结束区块，不预先计算状态根，而是像交易一样在区块执行时处理
		if isEpochEndBlock {

			// 通过全局注册表获取DPoS实例，添加奖励信息到ExtraData
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {

				if dposInstance.pendingRewardDistribution != nil {
					// 复制RewardDistribution，避免引用被清空
					extra.RewardDistribution = &RewardDistributionInfo{
						EpochNumber: dposInstance.pendingRewardDistribution.EpochNumber,
						Rewards:     make(map[string]*big.Int),
						TotalReward: new(big.Int).Set(dposInstance.pendingRewardDistribution.TotalReward),
						Timestamp:   dposInstance.pendingRewardDistribution.Timestamp,
					}
					// 复制Rewards map
					for k, v := range dposInstance.pendingRewardDistribution.Rewards {
						extra.RewardDistribution.Rewards[k] = new(big.Int).Set(v)
					}

					r.logger.Info("🔧 buildBlock: epoch结束区块，奖励分配信息已添加到ExtraData",
						"blockNumber", h.Number,
						"rewardCount", len(extra.RewardDistribution.Rewards),
						"totalReward", extra.RewardDistribution.TotalReward.String())

					// 清空pending奖励分配信息
					dposInstance.pendingRewardDistribution = nil
					r.logger.Info("✅ buildBlock: 已清空pending奖励分配信息")
				} else {
					r.logger.Info("ℹ️ buildBlock: DPoS实例存在但无待处理的奖励分配信息",
						"blockNumber", h.Number,
						"isEpochEndBlock", isEpochEndBlock)
				}

				// 🆕 添加故障检测信息
				if len(dposInstance.pendingFaultFlags) > 0 {
					// 复制FaultFlags，避免引用被清空
					extra.FaultFlags = make([]FaultFlagInfo, len(dposInstance.pendingFaultFlags))
					copy(extra.FaultFlags, dposInstance.pendingFaultFlags)

					r.logger.Info("🔧 buildBlock: epoch结束区块，故障检测结果已添加到ExtraData",
						"blockNumber", h.Number,
						"faultFlagsCount", len(extra.FaultFlags))

					// 清空pending故障标志
					dposInstance.pendingFaultFlags = nil
					r.logger.Info("✅ buildBlock: 已清空pending故障标志")
				} else {
					r.logger.Info("ℹ️ buildBlock: DPoS实例存在但无待处理的故障检测信息",
						"blockNumber", h.Number,
						"isEpochEndBlock", isEpochEndBlock)
				}

				// 🆕 添加故障消减信息
				if dposInstance.pendingSlashingInfo != nil {
					// 复制SlashingInfo，避免引用被清空
					extra.SlashingInfo = &SlashingInfo{
						EpochNumber: dposInstance.pendingSlashingInfo.EpochNumber,
						Slashings:   make([]*SlashingOperation, len(dposInstance.pendingSlashingInfo.Slashings)),
						Timestamp:   dposInstance.pendingSlashingInfo.Timestamp,
					}
					for i, op := range dposInstance.pendingSlashingInfo.Slashings {
						extra.SlashingInfo.Slashings[i] = &SlashingOperation{
							ValidatorAddr:          op.ValidatorAddr,
							SlashRate:              op.SlashRate,
							MissedBlocks:           op.MissedBlocks,
							MissedBlocksPercentage: op.MissedBlocksPercentage,
							Reason:                 op.Reason,
						}
					}

					r.logger.Info("🔧 buildBlock: epoch结束区块，故障消减信息已添加到ExtraData",
						"blockNumber", h.Number,
						"slashingsCount", len(extra.SlashingInfo.Slashings))

					// 清空pending消减信息
					dposInstance.pendingSlashingInfo = nil
					r.logger.Info("✅ buildBlock: 已清空pending消减信息")
				} else {
					r.logger.Info("ℹ️ buildBlock: DPoS实例存在但无待处理的消减信息",
						"blockNumber", h.Number,
						"isEpochEndBlock", isEpochEndBlock)
				}

				// 🆕 在epoch结束区块中也设置CheckpointBlockHash
				extra.CheckpointBlockHash = h.Hash
				r.logger.Info("🔍 ===== epoch结束区块保存CheckpointBlockHash =====",
					"blockNumber", h.Number,
					"checkpointBlockHash", h.Hash.String(),
					"说明", "epoch结束区块保存用于CheckpointHash计算的初始区块哈希")

				// 重新设置ExtraData
				h.ExtraData = extra.MarshalRLPTo(nil)

				// 重新计算区块哈希（用于后续处理）
				h.ComputeHash()
			} else {
				r.logger.Info("❌ buildBlock: 无法从全局注册表获取DPoS实例",
					"blockNumber", h.Number,
					"isEpochEndBlock", isEpochEndBlock,
					"key", "vcity_dpos")
			}
		}

		// 添加调试日志
		r.logger.Info("🏗️ buildBlock: DPoS区块构建完成",
			"number", h.Number,
			"difficulty", h.Difficulty,
			"gasLimit", h.GasLimit,
			"timestamp", h.Timestamp,
			"extraDataLength", len(h.ExtraData),
			"delegate", keyAddr.String()[:16],
			"isEpochEndBlock", isEpochEndBlock)

		// 🆕 检查区块头中的状态根
		r.logger.Info("🔍 检查区块头状态根",
			"blockNumber", h.Number,
			"stateRoot", h.StateRoot.String(),
			"isEpochEndBlock", isEpochEndBlock)

	})

	if err != nil {
		r.logger.Error("❌ buildBlock: 区块构建失败", "error", err)
		return nil, fmt.Errorf("failed to build block: %w", err)
	}

	buildDuration := time.Since(buildStart)
	r.logger.Info("✅ buildBlock: 区块构建成功",
		"blockNumber", block.Block.Number(),
		"blockHash", block.Block.Hash().String()[:16],
		"buildDuration", buildDuration.String(),
		"timestamp", time.Now().Format("15:04:05.000"))

	// 等待收集其他验证者的签名
	r.logger.Debug("waiting for validator signatures", "blockNumber", block.Block.Number())

	// 计算checkpoint哈希用于签名
	// 使用与区块头相同的CheckpointData对象
	checkpoint := extra.Checkpoint

	// 计算checkpoint哈希，使用真实的区块哈希
	// 确保区块哈希已经计算完成
	block.Block.Header.ComputeHash()
	realBlockHash := block.Block.Hash()

	// 🆕 保存初始区块哈希到extra中，用于CheckpointHash计算
	extra.CheckpointBlockHash = realBlockHash

	// 🆕 重新设置ExtraData，确保CheckpointBlockHash被包含
	block.Block.Header.ExtraData = extra.MarshalRLPTo(nil)

	r.logger.Debug("🔍 生产时开始计算checkpoint哈希",
		"blockNumber", block.Block.Number(),
		"chainID", r.config.blockchain.GetChainID(),
		"realBlockHash", realBlockHash.String(),
		"currentValidatorsHash", checkpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", checkpoint.NextValidatorsHash.String(),
		"blockRound", checkpoint.BlockRound,
		"epochNumber", checkpoint.EpochNumber)

	// 🆕 添加详细的CheckpointData内容对比日志
	r.logger.Debug("🔍 生产时CheckpointData详细信息",
		"blockNumber", block.Block.Number(),
		"chainID", r.config.blockchain.GetChainID(),
		"blockHash", realBlockHash.String(),
		"blockRound", checkpoint.BlockRound,
		"epochNumber", checkpoint.EpochNumber,
		"eventRoot", checkpoint.EventRoot.String(),
		"currentValidatorsHash", checkpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", checkpoint.NextValidatorsHash.String(),
		"productionValidatorsCount", len(productionValidators))

	checkpointHash, err := checkpoint.Hash(r.config.blockchain.GetChainID(), block.Block.Number(), realBlockHash)
	if err != nil {
		r.logger.Error("failed to calculate checkpoint hash", "error", err)
		return nil, fmt.Errorf("failed to calculate checkpoint hash: %w", err)
	}

	// 确保checkpointHash不为全零
	if checkpointHash == (types.Hash{}) {
		r.logger.Error("checkpointHash计算结果为全零，使用备用哈希")
		// 使用备用哈希：区块哈希 + 当前轮次
		backupHash := types.BytesToHash(append(block.Block.Header.Hash.Bytes(), []byte(fmt.Sprintf("_%d", r.currentRound))...))
		checkpointHash = backupHash
	}

	// 最终验证：确保checkpointHash不为空
	if checkpointHash == (types.Hash{}) {
		r.logger.Error("checkpointHash 仍然为空，无法发送签名请求")
		return nil, fmt.Errorf("invalid checkpointHash: cannot be zero")
	}

	// 添加调试日志
	r.logger.Debug("计算checkpointHash完成",
		"blockNumber", block.Block.Number(),
		"currentRound", r.currentRound,
		"checkpointHash", checkpointHash.String(),
		"realBlockHash", realBlockHash.String())

	// 实现真实的签名收集机制，支持重试
	var signatures [][]byte
	var signatureBitmap bitmap.Bitmap
	var collectErr error

	// 重试机制：最多重试3次
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		signatures, signatureBitmap, collectErr = r.collectValidatorSignatures(block, checkpointHash, keyAddr)
		if collectErr == nil {
			break // 成功收集签名
		}

		// 检查是否是网络增长检测错误
		if strings.Contains(collectErr.Error(), "network growth detected") {
			r.logger.Debug("检测到网络增长，重试签名收集", "attempt", attempt+1)
			time.Sleep(2 * time.Second) // 等待2秒后重试
			continue
		}

		// 检查是否是验证者数量不足错误
		if strings.Contains(collectErr.Error(), "insufficient validators") {
			r.logger.Debug("验证者数量不足，等待网络改善后重试", "attempt", attempt+1)
			// 等待网络状态改善
			_, _, waitErr := r.waitForNetworkGrowth(checkpointHash, keyAddr)
			if waitErr != nil {
				r.logger.Debug("等待网络增长失败", "error", waitErr)
			}
			time.Sleep(2 * time.Second) // 等待2秒后重试
			continue
		}

		// 其他错误，记录并返回
		r.logger.Error("failed to collect validator signatures", "error", collectErr, "attempt", attempt+1)
		if attempt == maxRetries-1 {
			return nil, fmt.Errorf("failed to collect validator signatures after %d attempts: %w", maxRetries, collectErr)
		}

		// 等待后重试
		time.Sleep(1 * time.Second)
	}

	r.logger.Debug("签名收集完成",
		"totalSignatures", len(signatures),
		"bitmapLength", len(signatureBitmap),
		"bitmapBytes", fmt.Sprintf("%x", signatureBitmap))

	// 🆕 关键修复：检查签名收集结果
	if len(signatures) == 0 || len(signatureBitmap) == 0 {
		r.logger.Error("签名收集失败，无法提交区块",
			"signaturesCount", len(signatures),
			"bitmapLength", len(signatureBitmap),
			"error", collectErr)
		return nil, fmt.Errorf("cannot commit block without valid signatures: signatures=%d, bitmap=%d", len(signatures), len(signatureBitmap))
	}

	// 更新区块的签名
	if len(signatures) > 0 {
		r.logger.Debug("开始聚合签名",
			"signatureCount", len(signatures))

		// 🆕 修复：按位图索引顺序聚合签名，确保与验证时公钥顺序一致
		blsSignatures := make(bls.Signatures, 0, len(signatures))

		// 🆕 关键修复：使用与验证时完全相同的验证者集合获取方法
		// 验证时使用：consensusBackend.GetDelegates(blockNumber-1, parents)
		// 生产时也应该使用相同的逻辑：获取父区块信息并传递

		// 获取父区块信息，与验证时保持一致
		var parents []*types.Header
		if block.Block.Number() > 1 {
			// 获取父区块
			parentHeader, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number() - 1)
			if exists && parentHeader != nil {
				parents = append(parents, parentHeader)
				r.logger.Debug("🔍 生产时获取父区块信息",
					"blockNumber", block.Block.Number(),
					"parentBlockNumber", parentHeader.Number,
					"parentBlockHash", parentHeader.Hash.String())
			}
		}

		// 🆕 关键修复：使用缓存的验证者集合，确保与第一次获取完全一致
		var productionValidators validator.AccountSet
		if r.cachedProductionValidators != nil && len(r.cachedProductionValidators) > 0 {
			// 使用缓存的4个验证者
			productionValidators = r.cachedProductionValidators.Copy()
			r.logger.Debug("💾 使用缓存的验证者集合", "validatorsCount", len(productionValidators))
		} else {
			// 如果缓存为空，则重新获取（备用方案）
			r.logger.Warn("⚠️ 缓存为空，重新获取验证者集合")
			var err error
			productionValidators, err = r.getValidatorsFromExtraDataForProduction(block.Block.Header, parents)
			if err != nil {
				r.logger.Error("❌ 无法获取当前验证者集合", "blockNumber", block.Block.Number(), "error", err)
				return nil, fmt.Errorf("failed to get current validators for block %d: %w", block.Block.Number(), err)
			}
			if productionValidators == nil || len(productionValidators) == 0 {
				r.logger.Error("❌ 当前验证者集合为空", "blockNumber", block.Block.Number())
				return nil, fmt.Errorf("current validators set is empty for block %d", block.Block.Number())
			}
		}

		// 🆕 关键修复：实时同步r.delegates为productionValidators
		// 确保位图索引和保存的验证者集合完全匹配
		r.delegates = productionValidators
		r.logger.Debug("🔄 已同步r.delegates为productionValidators",
			"blockNumber", block.Block.Number(),
			"delegatesCount", len(r.delegates),
			"productionValidatorsCount", len(productionValidators),
			"note", "确保位图索引与验证者集合完全匹配")

		// 🆕 验证：确保验证者集合与 r.delegates 一致
		if len(productionValidators) != len(r.delegates) {
			r.logger.Warn("⚠️ 验证者集合数量不一致",
				"productionValidatorsCount", len(productionValidators),
				"delegatesCount", len(r.delegates),
				"blockNumber", block.Block.Number())
		}

		// 创建位图索引到签名的映射
		bitmapToSignature := make(map[uint64][]byte)
		signatureIndex := 0

		// 🆕 方案1：先找出实际参与签名的验证者索引（基于位图设置）
		participatingIndices := make([]uint64, 0)
		for i := uint64(0); i < uint64(len(productionValidators)); i++ {
			if signatureBitmap.IsSet(i) {
				participatingIndices = append(participatingIndices, i)
			}
		}

		// 按实际参与签名的验证者索引收集签名
		for _, validatorIndex := range participatingIndices {
			if signatureIndex < len(signatures) {
				bitmapToSignature[validatorIndex] = signatures[signatureIndex]
				signatureIndex++
			}
		}

		// 按位图索引顺序聚合签名
		for i := uint64(0); i < uint64(len(productionValidators)); i++ {
			if signatureBitmap.IsSet(i) {
				if sigBytes, exists := bitmapToSignature[i]; exists {
					sig, err := bls.UnmarshalSignature(sigBytes)
					if err != nil {
						r.logger.Error("❌ BLS签名解析失败", "error", err, "bitmapIndex", i, "signatureLength", len(sigBytes))
						continue
					}
					blsSignatures = append(blsSignatures, sig)
				}
			}
		}

		// 聚合所有签名
		aggregatedSignature, err := blsSignatures.Aggregate().Marshal()
		if err != nil {
			r.logger.Error("❌ 签名聚合失败", "error", err)
			return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
		}

		// 检查聚合签名长度
		if len(aggregatedSignature) != 64 {
			r.logger.Error("❌ 聚合签名长度不正确",
				"expectedLength", 64,
				"actualLength", len(aggregatedSignature),
				"aggregatedSignatureBytes", fmt.Sprintf("%x", aggregatedSignature))
			return nil, fmt.Errorf("aggregated signature length is %d, expected 64", len(aggregatedSignature))
		}

		// 🆕 测试：验证聚合签名是否可以正确解析
		r.logger.Debug("🔍 测试聚合签名解析...")
		_, err = bls.UnmarshalSignature(aggregatedSignature)
		if err != nil {
			r.logger.Error("❌ 聚合签名解析测试失败", "error", err,
				"aggregatedSignatureLength", len(aggregatedSignature),
				"aggregatedSignatureBytes", fmt.Sprintf("%x", aggregatedSignature))
			return nil, fmt.Errorf("aggregated signature verification failed: %w", err)
		}
		r.logger.Debug("✅ 聚合签名解析测试通过")

		// 获取父区块的签名作为Parent签名
		var parentSignature *Signature
		if block.Block.Number() > 1 {
			// 获取父区块头部
			parentHeader, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number() - 1)
			if !exists {
				r.logger.Error("failed to get parent header", "parentNumber", block.Block.Number()-1)
				return nil, fmt.Errorf("failed to get parent header for block %d", block.Block.Number()-1)
			}

			// 解析父区块的ExtraData
			parentExtra, err := GetIbftExtra(parentHeader.ExtraData)
			if err != nil {
				r.logger.Error("failed to parse parent extra data", "error", err)
				return nil, fmt.Errorf("failed to parse parent extra data: %w", err)
			}

			// 使用父区块的Committed签名作为当前区块的Parent签名
			if parentExtra.Committed != nil {
				parentSignature = parentExtra.Committed
				r.logger.Debug("设置父区块签名",
					"blockNumber", block.Block.Number(),
					"parentNumber", parentHeader.Number,
					"parentSignatureLength", len(parentSignature.AggregatedSignature))
			} else {
				r.logger.Debug("父区块没有Committed签名",
					"blockNumber", block.Block.Number(),
					"parentNumber", parentHeader.Number)
			}
		}

		// 🆕 计算参与签名的验证者数量（位图设置 AND 有签名）
		participatingCount := 0
		for i := uint64(0); i < uint64(len(productionValidators)); i++ {
			if signatureBitmap.IsSet(i) {
				// 检查该验证者是否真的有签名
				if sigBytes, exists := bitmapToSignature[i]; exists && len(sigBytes) > 0 {
					participatingCount++
				}
			}
		}

		// 🆕 修复：保存全部验证者，确保与生产时使用的验证者集合完全一致
		validatorAddresses := make(validator.AccountSet, 0, len(productionValidators))
		for _, v := range productionValidators {
			// 保存全部验证者，不仅仅是签名者，确保位图索引与验证者集合匹配
			validatorAddresses = append(validatorAddresses, &validator.ValidatorMetadata{
				Address:     v.Address,
				BlsKey:      nil, // 🆕 不保存BLS公钥，验证时从创世文件获取
				VotingPower: v.VotingPower,
				IsActive:    v.IsActive,
			})

		}

		signingValidatorDelta := &validator.ValidatorSetDelta{
			Added:   validatorAddresses, // 🆕 保存全部验证者，确保位图索引匹配
			Updated: make(validator.AccountSet, 0),
			Removed: bitmap.Bitmap{},
		}

		// 🆕 记录参与签名的验证者信息（用于调试）

		// 🆕 显著日志：生产时保存到ExtraData的验证者集合和索引
		r.logger.Debug("🏭 ===== 生产时保存到ExtraData的验证者集合 =====",
			"blockNumber", block.Block.Number(),
			"totalValidators", len(validatorAddresses),
			"bitmapHex", fmt.Sprintf("%x", signatureBitmap),
			"note", "这些验证者将被保存到区块ExtraData中")

		for i, validator := range validatorAddresses {
			r.logger.Debug("🏭 生产时ExtraData验证者",
				"blockNumber", block.Block.Number(),
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil,
				"bitmapSet", signatureBitmap.IsSet(uint64(i)),
				"note", "验证者索引与位图索引对应")
		}

		// 🆕 显著日志：位图索引详情
		r.logger.Debug("🏭 ===== 生产时位图索引详情 =====",
			"blockNumber", block.Block.Number(),
			"bitmapHex", fmt.Sprintf("%x", signatureBitmap),
			"bitmapLength", len(signatureBitmap),
			"totalValidators", len(validatorAddresses))

		for i := uint64(0); i < uint64(len(validatorAddresses)); i++ {
			r.logger.Debug("🏭 位图索引状态",
				"blockNumber", block.Block.Number(),
				"bitmapIndex", i,
				"isSet", signatureBitmap.IsSet(i),
				"validatorAddress", validatorAddresses[i].Address.String(),
				"note", "位图索引与验证者集合一一对应")
		}

		// 静默处理，不打印日志

		// 🆕 关键修复：从当前区块的ExtraData中获取奖励分配信息、故障标志和消减信息
		var rewardDistribution *RewardDistributionInfo
		var faultFlags []FaultFlagInfo
		var slashingInfo *SlashingInfo
		if currentBlockExtra, err := GetIbftExtra(block.Block.Header.ExtraData); err == nil {
			rewardDistribution = currentBlockExtra.RewardDistribution
			faultFlags = currentBlockExtra.FaultFlags
			slashingInfo = currentBlockExtra.SlashingInfo
			r.logger.Debug("🔍 从currentBlockExtra获取信息",
				"hasRewardDistribution", rewardDistribution != nil,
				"faultFlagsCount", len(faultFlags),
				"hasSlashingInfo", slashingInfo != nil)
		} else {
			r.logger.Warn("⚠️ 无法解析当前区块ExtraData，奖励分配信息可能丢失",
				"blockNumber", block.Block.Number(),
				"error", err)
		}

		// 计算写入ExtraData的下一个epoch验证者集合：
		// 直接使用最新的 r.nextEpochValidators 写入 ExtraData（已被故障过滤覆盖）
		nextEpochValidatorsForExtra := r.nextEpochValidators

		// 更新区块的ExtraData，包含聚合签名、父区块签名和验证者集合
		finalExtra := &Extra{
			Validators: signingValidatorDelta, // 🆕 保存全部验证者集合，确保位图索引匹配
			Parent:     parentSignature,       // 父区块签名
			Committed: &Signature{
				AggregatedSignature: aggregatedSignature,
				Bitmap:              signatureBitmap,
			},
			Checkpoint:          checkpoint,
			RewardDistribution:  rewardDistribution,          // 🆕 从当前区块ExtraData获取的奖励分配信息
			CheckpointBlockHash: extra.CheckpointBlockHash,   // 🆕 保持CheckpointBlockHash
			FaultFlags:          faultFlags,                  // 🆕 保持FaultFlags
			NextEpochValidators: nextEpochValidatorsForExtra, // 🆕 下一个epoch的验证者集合（只在epoch边界区块时设置）
			SlashingInfo:        slashingInfo,                // 🆕 保持SlashingInfo
		}

		if len(nextEpochValidatorsForExtra) > 0 {
			r.logger.Info("NextEpochValidators写入ExtraData",
				"blockNumber", block.Block.Number(),
				"nextEpochValidatorsCount", len(nextEpochValidatorsForExtra))
			for idx, acc := range nextEpochValidatorsForExtra {
				r.logger.Info("📝 NextEpochValidator写入详情",
					"blockNumber", block.Block.Number(),
					"index", idx,
					"address", acc.Address.String(),
					"votingPower", acc.VotingPower.String())
			}
		} else {
			r.logger.Debug("🆕 NextEpochValidators写入ExtraData: 当前为空",
				"blockNumber", block.Block.Number())
		}

		block.Block.Header.ExtraData = finalExtra.MarshalRLPTo(nil)

		// 🆕 关键修复：在重新计算区块哈希前，确保状态根正确
		// 从全局变量获取正确的状态根（针对epoch结束区块）
		globalNewStateRootMutex.RLock()
		correctStateRoot := globalNewStateRoot
		globalNewStateRootMutex.RUnlock()

		if correctStateRoot != (types.Hash{}) {
			oldStateRoot := block.Block.Header.StateRoot
			block.Block.Header.StateRoot = correctStateRoot
			r.logger.Info("🔧 签名聚合后修复状态根: 确保状态根正确",
				"blockNumber", block.Block.Number(),
				"oldStateRoot", oldStateRoot.String(),
				"correctStateRoot", correctStateRoot.String(),
				"note", "在重新计算区块哈希前修复状态根")
		}

		// 🆕 记录重新计算区块哈希前的状态根
		r.logger.Debug("🔍 重新计算区块哈希前的状态根",
			"blockNumber", block.Block.Number(),
			"stateRoot", block.Block.Header.StateRoot.String(),
			"说明", "在ComputeHash()之前记录状态根")

		// 重新计算区块哈希，因为ExtraData已经更新
		block.Block.Header.ComputeHash()

		// 🆕 记录重新计算区块哈希后的状态根
		r.logger.Debug("🔍 重新计算区块哈希后的状态根",
			"blockNumber", block.Block.Number(),
			"stateRoot", block.Block.Header.StateRoot.String(),
			"说明", "在ComputeHash()之后记录状态根")

		// 🆕 现在清理全局状态根，签名聚合已完成
		globalNewStateRootMutex.Lock()
		globalNewStateRoot = types.Hash{}
		globalNewStateRootMutex.Unlock()
		r.logger.Debug("🧹 签名聚合完成后清理全局状态根", "blockNumber", block.Block.Number())

		r.logger.Debug("区块签名更新完成",
			"blockNumber", block.Block.Number(),
			"extraDataLength", len(block.Block.Header.ExtraData),
			"newBlockHash", block.Block.Header.Hash.String())
	}

	// 🆕 清理缓存，为下一个区块做准备
	r.cachedProductionValidators = nil

	return block, nil
}

// collectValidatorSignatures 收集验证者签名
func (r *dposRuntime) collectValidatorSignatures(block *types.FullBlock, checkpointHash types.Hash, keyAddr types.Address) ([][]byte, bitmap.Bitmap, error) {
	signatures := make([][]byte, 0)
	signatureBitmap := bitmap.Bitmap{}

	// 🆕 确保受托人按票数排序，与验证时保持一致

	myAddress := types.Address(r.config.Key.Address())

	for _, delegate := range r.delegates {
		if delegate.BlsKey == nil {

			// 主动请求BLS公钥
			if r.networkIntegration != nil {
				if err := r.networkIntegration.RequestBLSKey(delegate.Address, myAddress); err != nil {
					r.logger.Warn("⚠️ 请求BLS公钥失败",
						"address", delegate.Address.String(),
						"error", err)
				} else {
					r.logger.Debug("📨 已发送BLS公钥请求",
						"address", delegate.Address.String())
				}
			}
		} else {
			// BLS公钥已存在
		}
	}

	// 等待一小段时间让BLS公钥请求完成
	time.Sleep(100 * time.Millisecond)

	// 🆕 尝试从缓存中恢复BLS公钥
	r.logger.Debug("🔄 尝试从缓存恢复BLS公钥")
	if r.networkIntegration != nil {
		// 使用批量恢复函数
		if err := r.networkIntegration.RestoreBLSKeysForDelegates(r.delegates); err != nil {
			r.logger.Warn("⚠️ 批量恢复BLS公钥失败", "error", err)
		}
	}

	// 🆕 验证BLS公钥可用性
	missingBlsKeys := 0
	for i, delegate := range r.delegates {
		if delegate.BlsKey == nil {
			missingBlsKeys++
			r.logger.Warn("⚠️ 受托人缺少BLS公钥",
				"index", i,
				"address", delegate.Address.String())
		}
	}

	if missingBlsKeys > 0 {
		r.logger.Warn("⚠️ 部分受托人缺少BLS公钥，将在验证时按需获取",
			"missingCount", missingBlsKeys,
			"totalDelegates", len(r.delegates))
	}

	r.logger.Debug("开始收集验证者签名",
		"checkpointHash", checkpointHash.String(),
		"delegatesCount", len(r.delegates),
		"proposerAddress", keyAddr.String())

	// 统计活跃验证者数量
	activeCount := 0
	for _, delegate := range r.delegates {
		if delegate.IsActive && isPositive(delegate.VotingPower) {
			activeCount++
		}
	}

	// 检查网络中的活跃验证者数量
	activeValidators := r.getActiveValidatorsCount()

	r.logger.Debug("📊 活跃验证者统计",
		"activeCount", activeCount,
		"totalDelegates", len(r.delegates),
		"activeValidators", activeValidators,
		"note", "activeCount是本地统计，activeValidators是网络方法返回")

	// 计算真正活跃的验证者数量（有足够stake且IsActive=true）
	expectedSignatures := activeValidators // 只计算活跃的验证者

	r.logger.Debug("🌐 出块时网络状态检查",
		"activeValidators", activeValidators,
		"totalDelegates", len(r.delegates),
		"expectedSignatures", expectedSignatures,
		"requiredForQuorum", r.calculateMinRequiredSignatures())

	// 检查是否有足够的验证者
	minRequired := r.calculateMinRequiredSignatures()
	if activeValidators < minRequired { // 现在包括提议者自己
		r.logger.Error("验证者数量不足，无法进行签名收集",
			"activeValidators", activeValidators,
			"minRequired", minRequired,
			"totalDelegates", len(r.delegates))

		// 返回错误，让上层重试机制处理
		return nil, nil, fmt.Errorf("insufficient validators: got %d, need %d", activeValidators, minRequired)
	}

	// 1. 广播签名请求给其他验证者
	// 创建protobuf签名请求消息
	protoRequest := &dposProto.SignatureRequest{}
	protoRequest.BlockNumber = block.Block.Number()
	protoRequest.BlockHash = block.Block.Header.Hash.Bytes()
	protoRequest.CheckpointHash = checkpointHash.Bytes()
	protoRequest.Round = r.currentRound
	protoRequest.Proposer = types.Address(r.config.Key.Address()).Bytes()
	protoRequest.Timestamp = uint64(time.Now().Unix())

	if err := r.broadcastSignatureRequest(protoRequest); err != nil {
		r.logger.Error("failed to broadcast signature request", "error", err)
		return nil, nil, fmt.Errorf("failed to broadcast signature request: %w", err)
	}

	// 2. 创建签名收集通道和收集器
	signatureCh := make(chan *SignatureResponse, len(r.delegates))

	// 如果有网络集成，使用网络集成层进行签名收集
	if r.networkIntegration != nil {
		r.logger.Debug("使用网络集成层进行签名收集", "checkpointHash", checkpointHash.String())

		// 注册签名收集器到网络集成层
		timeout := 30 * time.Second
		requiredCount := r.calculateMinRequiredSignatures()
		r.networkIntegration.RegisterSignatureCollector(checkpointHash, signatureCh, timeout, requiredCount)

		// 签名收集器已注册到网络集成层
	} else {
		// 网络集成不可用，使用原有的签名收集机制
	}

	// 启动签名收集协程
	// 签名收集调试信息已移除
	go r.collectSignaturesAsync(checkpointHash, signatureCh)

	// 等待一小段时间让协程启动
	time.Sleep(100 * time.Millisecond)

	// 3. 智能等待签名收集完成
	collectedSignatures := make(map[types.Address][]byte)
	minRequiredSignatures := r.calculateMinRequiredSignatures()

	// 使用合理的超时时间
	baseTimeout := 2 * time.Minute    // 减少基础超时时间
	networkTimeout := 5 * time.Minute // 减少网络等待超时

	// 如果验证者数量不足，使用更长的超时等待更多节点加入
	if r.getActiveValidatorsCount() < r.calculateMinRequiredSignatures()+1 {
		baseTimeout = networkTimeout
	}

	// 签名收集超时配置（静默处理）

	timeoutCh := time.After(baseTimeout)
	checkInterval := time.NewTicker(15 * time.Second) // 每15秒检查一次网络状态
	defer checkInterval.Stop()

	// 开始智能签名收集（静默处理）

	// 添加调试日志，监控通道状态
	debugTicker := time.NewTicker(5 * time.Second)
	defer debugTicker.Stop()

	for {
		select {
		case sigResp := <-signatureCh:
			if sigResp != nil && sigResp.Signature != nil {
				collectedSignatures[sigResp.ValidatorAddr] = sigResp.Signature
				// 收到验证者签名（静默处理）

				// 检查是否收集到足够的签名
				if len(collectedSignatures) >= minRequiredSignatures {
					r.logger.Debug("收集到足够的签名",
						"count", len(collectedSignatures),
						"checkpointHash", checkpointHash.String())
					goto processSignatures
				}
			}

		case <-checkInterval.C:
			// 定期检查网络状态
			activeValidators := r.getActiveValidatorsCount()
			r.logger.Debug("定期检查网络状态",
				"activeValidators", activeValidators,
				"totalDelegates", len(r.delegates),
				"collectedSignatures", len(collectedSignatures),
				"requiredSignatures", minRequiredSignatures)

			// 检查是否有足够的验证者进行签名收集
			if activeValidators < minRequiredSignatures {
				r.logger.Warn("验证者数量不足，等待更多节点加入",
					"activeValidators", activeValidators,
					"minRequired", minRequiredSignatures)
				// 重置超时，给更多节点加入的时间
				timeoutCh = time.After(1 * time.Minute)
			} else if len(collectedSignatures) == 0 {
				r.logger.Warn("验证者数量足够但未收到签名，检查网络连接")

				// 🆕 进行网络健康检查
				if !r.checkNetworkHealth() {
					r.logger.Warn("网络健康检查失败，尝试自动恢复")
					go r.recoverNetworkConnection()
					// 给恢复更多时间
					timeoutCh = time.After(1 * time.Minute)
				} else {
					// 网络健康但未收到签名，可能是其他问题，给更多时间
					timeoutCh = time.After(30 * time.Second)
				}
			}

		case <-debugTicker.C:
			// 调试日志：监控通道状态和收集器状态
			// 检查网络集成层的收集器状态
			if r.networkIntegration != nil {
				// 网络集成层状态检查
			}

		case <-timeoutCh:
			r.logger.Warn("签名收集超时",
				"collected", len(collectedSignatures),
				"required", minRequiredSignatures,
				"checkpointHash", checkpointHash.String(),
				"activeValidators", r.getActiveValidatorsCount(),
				"totalDelegates", len(r.delegates))

			// 记录当前收集到的签名详情
			for addr, sig := range collectedSignatures {
				r.logger.Debug("已收集签名",
					"validator", addr.String(),
					"signatureLength", len(sig))
			}

			// 记录缺失的验证者
			missingValidators := make([]string, 0)
			for _, delegate := range r.delegates {
				if _, exists := collectedSignatures[delegate.Address]; !exists {
					missingValidators = append(missingValidators, delegate.Address.String())
				}
			}
			r.logger.Warn("缺失签名的验证者",
				"missingCount", len(missingValidators),
				"missingValidators", missingValidators)

			// 如果收集到的签名不足，返回错误
			if len(collectedSignatures) < minRequiredSignatures {
				return nil, nil, fmt.Errorf("insufficient signatures collected: got %d, need at least %d", len(collectedSignatures), minRequiredSignatures)
			}
			goto processSignatures
		}
	}

processSignatures:
	// 4. 处理收集到的签名
	r.logger.Debug("🎯 出块时签名收集开始",
		"totalDelegates", len(r.delegates),
		"checkpointHash", checkpointHash.String())

	for i, delegate := range r.delegates {
		// 静默处理，不打印日志

		if delegate.Address == keyAddr {
			// 提议者自己生成签名
			if r.config != nil && r.config.Key != nil {
				blsKey, err := r.getBLSPrivateKey()
				if err != nil {
					r.logger.Warn("提议者无法获取BLS私钥", "error", err)
					continue
				}

				signature, err := blsKey.Sign(checkpointHash[:], signer.DomainValidatorSet)
				if err != nil {
					r.logger.Warn("提议者签名失败", "error", err)
					continue
				}

				signatureBytes, err := signature.Marshal()
				if err != nil {
					r.logger.Warn("提议者签名序列化失败", "error", err)
					continue
				}

				signatures = append(signatures, signatureBytes)
				signatureBitmap.Set(uint64(i))
				r.logger.Debug("✅ 提议者签名已添加",
					"address", delegate.Address.String(),
					"bitmapIndex", i,
					"signatureCount", len(signatures),
					"checkpointHash", checkpointHash.String())
			}
			continue
		}

		if signature, exists := collectedSignatures[delegate.Address]; exists {

			// 验证签名
			if err := r.verifyValidatorSignature(delegate, signature, checkpointHash); err != nil {
				r.logger.Warn("❌ 验证者签名验证失败",
					"validator", delegate.Address.String(),
					"error", err)
				continue
			}

			// 静默处理，不打印日志

			signatures = append(signatures, signature)
			signatureBitmap.Set(uint64(i))
		} else {
			r.logger.Debug("未收到验证者签名",
				"validator", delegate.Address.String())
		}
	}

	r.logger.Debug("🎉 出块签名收集完成",
		"totalSignatures", len(signatures),
		"bitmapLength", len(signatureBitmap),
		"collectedCount", len(collectedSignatures),
		"expectedCount", expectedSignatures,
		"checkpointHash", checkpointHash.String())

	// 显示最终的签名详情
	r.logger.Debug("📋 出块时最终签名详情:")
	for i, sig := range signatures {
		r.logger.Debug("📝 收集到的签名",
			"signatureIndex", i,
			"signatureLength", len(sig),
			"signatureHex", fmt.Sprintf("%x", sig))
	}

	// 🆕 新增：出块前数据一致性验证
	r.logger.Debug("🔍 出块前验证数据一致性...")
	if err := r.verifyBlockDataConsistency(); err != nil {
		r.logger.Error("❌ 出块前数据一致性验证失败", "error", err)
		// 继续出块，但记录错误
	} else {
		r.logger.Debug("✅ 出块前数据一致性验证通过")
	}

	// 显示位图详情
	if len(signatures) > 0 {
		r.logger.Debug("📊 区块签名位图详情",
			"bitmapHex", fmt.Sprintf("%x", signatureBitmap),
			"bitmapLength", len(signatureBitmap),
			"signedDelegatesCount", len(signatures))

		// 位图验证完成
	}

	// 资源清理：清理签名收集相关的临时数据
	r.cleanupSignatureCollectionResources(checkpointHash)

	return signatures, signatureBitmap, nil
}

// verifyBlockDataConsistency 出块前验证数据一致性
func (r *dposRuntime) verifyBlockDataConsistency() error {
	// 1. 获取出块时使用的验证者集合
	blockValidators := r.delegates.Copy()

	// 2. 获取通过GetDelegates方法获得的验证者集合
	currentBlockNumber := r.config.blockchain.CurrentHeader().Number
	getValidators, err := r.config.dposBackend.GetDelegates(currentBlockNumber, nil)
	if err != nil {
		r.logger.Error("❌ Failed to get validators via GetDelegates", "error", err)
		return fmt.Errorf("failed to get validators via GetDelegates: %w", err)
	}

	// 3. 比较两个验证者集合
	blockMap := make(map[types.Address]*validator.ValidatorMetadata)
	getMap := make(map[types.Address]*validator.ValidatorMetadata)

	// 构建出块验证者映射
	for _, del := range blockValidators {
		blockMap[del.Address] = del
	}

	// 构建GetDelegates验证者映射
	for _, del := range getValidators {
		getMap[del.Address] = del
	}

	// 4. 检查验证者集合一致性
	inconsistencies := 0

	// 检查出块验证者集合中的每个验证者
	for addr, blockDel := range blockMap {
		getDel, exists := getMap[addr]
		if !exists {
			r.logger.Error("❌ Validator exists in block delegates but not in GetDelegates",
				"address", addr.String(),
				"blockVotingPower", blockDel.VotingPower.String(),
				"blockIsActive", blockDel.IsActive)
			inconsistencies++
			continue
		}

		// 检查关键字段一致性
		if blockDel.VotingPower.Cmp(getDel.VotingPower) != 0 {
			r.logger.Error("❌ Voting power inconsistency in block validation",
				"address", addr.String(),
				"blockVotingPower", blockDel.VotingPower.String(),
				"getVotingPower", getDel.VotingPower.String(),
				"difference", new(big.Int).Sub(getDel.VotingPower, blockDel.VotingPower).String(),
				"blockIsActive", blockDel.IsActive,
				"getIsActive", getDel.IsActive)

			// 🆕 添加详细分析日志
			r.logger.Error("🔍 Voting power inconsistency analysis:",
				"address", addr.String(),
				"memoryValue", blockDel.VotingPower.String(),
				"databaseValue", getDel.VotingPower.String(),
				"memoryIsActive", blockDel.IsActive,
				"databaseIsActive", getDel.IsActive,
				"inconsistencyType", "voting_power_mismatch")
			inconsistencies++
		}

		if blockDel.IsActive != getDel.IsActive {
			r.logger.Error("❌ IsActive inconsistency in block validation",
				"address", addr.String(),
				"blockIsActive", blockDel.IsActive,
				"getIsActive", getDel.IsActive)
			inconsistencies++
		}

		// 检查BLS公钥一致性
		blockBlsKey := ""
		getBlsKey := ""
		if blockDel.BlsKey != nil {
			blockBlsKey = fmt.Sprintf("%x", blockDel.BlsKey.Marshal())
		}
		if getDel.BlsKey != nil {
			getBlsKey = fmt.Sprintf("%x", getDel.BlsKey.Marshal())
		}

		if blockBlsKey != getBlsKey {
			r.logger.Error("❌ BLS key inconsistency in block validation",
				"address", addr.String(),
				"blockBlsKey", blockBlsKey,
				"getBlsKey", getBlsKey)
			inconsistencies++
		}
	}

	// 5. 检查GetDelegates中是否有出块验证者集合中没有的验证者
	for addr, getDel := range getMap {
		if _, exists := blockMap[addr]; !exists {
			r.logger.Warn("⚠️ Validator exists in GetDelegates but not in block delegates",
				"address", addr.String(),
				"getVotingPower", getDel.VotingPower.String(),
				"getIsActive", getDel.IsActive)
		}
	}

	if inconsistencies > 0 {
		r.logger.Error("❌ Block data consistency verification failed",
			"inconsistencies", inconsistencies,
			"blockCount", len(blockValidators),
			"getCount", len(getValidators))
		return fmt.Errorf("block data consistency verification failed: %d inconsistencies found", inconsistencies)
	}

	// 数据一致性验证通过
	return nil
}

// getActiveValidatorsCount 获取网络中活跃验证者的数量
func (r *dposRuntime) getActiveValidatorsCount() int {
	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("网络服务不可用，无法进行多节点签名收集")
		return 0
	}

	var validators validator.AccountSet
	if r.config != nil && r.config.dposBackend != nil {
		dposInstance, ok := r.config.dposBackend.(*DPoS)
		if ok && dposInstance != nil {
			dbValidators, err := dposInstance.GetSortedValidatorsWithLimit()
			if err != nil {
				r.logger.Error("⚠️ 从数据库读取验证者失败", "error", err)
				return 0
			} else {
				validators = dbValidators
			}
		} else {
			r.logger.Error("⚠️ 获取dpos实例错误")
			return 0
		}
	} else {
		r.logger.Error("⚠️ 获取dposBackend错误")
		return 0
	}

	// 计算真正活跃的验证者数量（有足够stake且IsActive=true）
	activeValidators := 0
	for _, delegate := range validators {
		if delegate.IsActive && isPositive(delegate.VotingPower) {
			activeValidators++
		}
	}

	// 如果只有一个活跃验证者，返回0（表示无法进行多节点签名收集）
	if activeValidators <= 1 {
		r.logger.Debug("活跃验证者数量不足，无法进行多节点签名收集",
			"activeValidators", activeValidators,
			"totalDelegates", len(r.delegates))
		return 0
	}

	// 🆕 检查实际网络连接状态
	connectedPeers := r.getConnectedPeersCount()
	// 网络连接状态检查（静默处理）

	if connectedPeers < activeValidators {
		// 🆕 修复：如果完全没有网络连接，返回0触发等待网络改善
		if connectedPeers == 0 {
			r.logger.Warn("完全没有网络连接，返回0触发等待网络改善",
				"activeValidators", activeValidators,
				"connectedPeers", connectedPeers)
			return 0
		}

		// 如果有部分连接，返回活跃验证者数量（当前节点可参与签名）
		return activeValidators
	}

	return activeValidators
}

// calculateMinRequiredSignatures 计算最少需要的签名数量
func (r *dposRuntime) calculateMinRequiredSignatures() int {
	// 改为使用实际验证者数量
	activeValidatorsCount := r.getActiveValidatorsCount()
	if activeValidatorsCount == 0 {
		return 1
	}

	// 计算：实际验证者数量的一半（半数）
	minRequired := activeValidatorsCount / 2
	if minRequired < 1 {
		minRequired = 1
	}

	return minRequired
}

func (r *dposRuntime) getValidatorsFromExtraDataForProduction(header *types.Header, parents []*types.Header) (validator.AccountSet, error) {
	if r.config != nil && r.config.dposBackend != nil {
		dposInstance, ok := r.config.dposBackend.(*DPoS)
		if ok && dposInstance != nil {
			dbValidators, err := dposInstance.GetSortedValidatorsWithLimit()
			if err != nil {
				r.logger.Error("⚠️ 从数据库读取验证者失败", "error", err)
				return nil, err
			}

			if len(dbValidators) > 0 {
				r.logger.Debug("🔍 生产时使用数据库验证者集合", "validatorsCount", len(dbValidators))
				r.lock.Lock()
				r.delegates = dbValidators
				r.lock.Unlock()
				return dbValidators, nil
			}
		}
	}

	return nil, fmt.Errorf("no validators available for production")
}

// waitForNetworkGrowth 等待网络增长到足够的验证者
func (r *dposRuntime) waitForNetworkGrowth(checkpointHash types.Hash, proposerAddr types.Address) ([][]byte, bitmap.Bitmap, error) {
	r.logger.Info("开始等待网络增长", "checkpointHash", checkpointHash.String())

	// 设置等待超时（10分钟）
	waitTimeout := 10 * time.Minute
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	timeoutCh := time.After(waitTimeout)

	for {
		select {
		case <-ticker.C:
			activeValidators := r.getActiveValidatorsCount()
			minRequired := r.calculateMinRequiredSignatures()

			r.logger.Info("检查网络状态",
				"activeValidators", activeValidators,
				"minRequired", minRequired,
				"totalDelegates", len(r.delegates))

			// 如果网络中有足够的验证者，重新尝试收集签名
			if activeValidators >= minRequired {
				r.logger.Info("检测到足够的验证者，重新尝试收集签名",
					"activeValidators", activeValidators,
					"minRequired", minRequired,
					"checkpointHash", checkpointHash.String())

				// 重新启动签名收集流程
				// 使用更短的超时时间，避免长时间等待
				retryTimeout := 30 * time.Second
				retryCtx, cancel := context.WithTimeout(context.Background(), retryTimeout)
				defer cancel()

				// 创建新的签名收集通道
				signatureCh := make(chan *SignatureResponse, len(r.delegates))

				// 重新广播签名请求
				currentBlockNumber := r.config.blockchain.CurrentHeader().Number + 1
				protoRequest := &dposProto.SignatureRequest{
					BlockNumber:    currentBlockNumber,
					CheckpointHash: checkpointHash.Bytes(),
					Round:          r.currentRound,
					Proposer:       proposerAddr.Bytes(),
					Timestamp:      uint64(time.Now().Unix()),
				}

				if err := r.broadcastSignatureRequest(protoRequest); err != nil {
					r.logger.Error("重新广播签名请求失败", "error", err)
					return nil, nil, fmt.Errorf("failed to rebroadcast signature request: %w", err)
				}

				// 等待签名收集
				signatures, bitmap, err := r.waitForSignaturesWithContext(retryCtx, signatureCh, minRequired)
				if err != nil {
					r.logger.Error("重新收集签名失败", "error", err)
					return nil, nil, fmt.Errorf("failed to collect signatures after network growth: %w", err)
				}

				r.logger.Info("网络增长后签名收集成功",
					"signaturesCount", len(signatures),
					"bitmapLength", len(bitmap))

				return signatures, bitmap, nil
			}

		case <-timeoutCh:
			r.logger.Error("等待网络增长超时，需要更多验证者节点")
			// 超时后，返回错误
			return nil, nil, fmt.Errorf("network growth timeout: need more validator nodes")
		}
	}
}

// waitForSignaturesWithContext 使用上下文控制的签名收集
func (r *dposRuntime) waitForSignaturesWithContext(ctx context.Context, signatureCh chan *SignatureResponse, minRequired int) ([][]byte, bitmap.Bitmap, error) {
	collectedSignatures := make(map[types.Address][]byte)
	signatureBitmap := bitmap.Bitmap{}

	// 设置收集超时
	collectTimeout := 20 * time.Second
	timeoutCh := time.After(collectTimeout)

	for {
		select {
		case response := <-signatureCh:
			if response == nil {
				continue
			}

			// 验证签名响应
			if err := r.validateSignatureResponse(response); err != nil {
				r.logger.Warn("签名响应验证失败", "validator", response.ValidatorAddr.String(), "error", err)
				continue
			}

			// 收集签名
			collectedSignatures[response.ValidatorAddr] = response.Signature

			// 设置位图
			for i, delegate := range r.delegates {
				if delegate.Address == response.ValidatorAddr {
					signatureBitmap.Set(uint64(i))
					break
				}
			}

			r.logger.Info("收集到签名",
				"validator", response.ValidatorAddr.String(),
				"collectedCount", len(collectedSignatures),
				"requiredCount", minRequired)

			// 检查是否收集到足够的签名
			if len(collectedSignatures) >= minRequired {
				// 按位图顺序排列签名
				signatures := make([][]byte, 0, len(collectedSignatures))
				for i := uint64(0); i < uint64(len(r.delegates)); i++ {
					if signatureBitmap.IsSet(i) {
						if sig, exists := collectedSignatures[r.delegates[i].Address]; exists {
							signatures = append(signatures, sig)
						}
					}
				}

				return signatures, signatureBitmap, nil
			}

		case <-timeoutCh:
			r.logger.Warn("签名收集超时",
				"collected", len(collectedSignatures),
				"required", minRequired)

			if len(collectedSignatures) >= minRequired {
				// 即使超时，如果收集到足够的签名就返回
				signatures := make([][]byte, 0, len(collectedSignatures))
				for i := uint64(0); i < uint64(len(r.delegates)); i++ {
					if signatureBitmap.IsSet(i) {
						if sig, exists := collectedSignatures[r.delegates[i].Address]; exists {
							signatures = append(signatures, sig)
						}
					}
				}
				return signatures, signatureBitmap, nil
			}

			return nil, nil, fmt.Errorf("signature collection timeout: got %d, need %d", len(collectedSignatures), minRequired)

		case <-ctx.Done():
			r.logger.Warn("签名收集被取消", "error", ctx.Err())
			return nil, nil, fmt.Errorf("signature collection cancelled: %w", ctx.Err())
		}
	}
}

// collectSignaturesAsync 异步收集签名
func (r *dposRuntime) collectSignaturesAsync(checkpointHash types.Hash, signatureCh chan<- *SignatureResponse) {
	// 计算最小所需签名数量
	minRequiredSignatures := r.calculateMinRequiredSignatures()

	// 启动异步签名收集（静默处理）

	// 修复：创建一个双向通道作为桥梁，确保类型兼容性
	// 同时保持签名响应能够正确传递到collectValidatorSignatures等待的通道
	bridgeCh := make(chan *SignatureResponse, 1000) // 使用合适的缓冲区大小

	// 启动转发协程，将bridgeCh的消息转发到signatureCh
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("signature-bridge", func() {
			defer close(signatureCh)
			defer close(bridgeCh)

			// 添加超时控制，避免无限运行
			timeout := time.After(5 * time.Minute) // 5分钟后自动退出

			for {
				select {
				case response, ok := <-bridgeCh:
					if !ok {
						r.logger.Debug("桥接通道关闭，停止转发",
							"checkpointHash", checkpointHash.String())
						return
					}

					// 发送到signatureCh
					select {
					case signatureCh <- response:
					case <-time.After(5 * time.Second):
						r.logger.Error("签名响应桥接转发超时，丢弃响应",
							"validator", response.ValidatorAddr.String(),
							"checkpointHash", checkpointHash.String())
					}
				case <-timeout:
					r.logger.Debug("签名桥接协程超时，自动退出",
						"checkpointHash", checkpointHash.String())
					return
				}
			}
		})
	} else {
		r.logger.Error("资源监控器不可用，无法启动签名桥接协程")
	}

	// 注册签名收集器，使用桥接通道
	if r.networkIntegration != nil {

		r.networkIntegration.RegisterSignatureCollector(
			checkpointHash,
			bridgeCh, // 使用桥接通道
			30*time.Second,
			minRequiredSignatures,
		)
	} else {
		r.logger.Error("网络集成层不可用，无法注册签名收集器",
			"checkpointHash", checkpointHash.String())
	}

	// 启动一个监控协程，定期检查收集状态
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("signature-monitor", func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()

			// 添加超时控制，避免无限运行
			timeout := time.After(5 * time.Minute) // 5分钟后自动退出

			for {
				select {
				case <-ticker.C:
					// 检查收集器状态
					if r.networkIntegration != nil {
						// 静默监控，不打印日志
					}
				case <-timeout:
					r.logger.Debug("签名收集监控超时，自动退出",
						"checkpointHash", checkpointHash.String())
					return
				}
			}
		})
	}

}

// fallbackSignatureCollection 备用签名收集机制
func (r *dposRuntime) fallbackSignatureCollection(ctx context.Context, listener *SignatureListener, checkpointHash types.Hash) {
	r.logger.Info("启动备用签名收集机制", "checkpointHash", checkpointHash.String())

	// 定期检查是否有新的签名响应
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// 记录启动时间，用于超时控制
	startTime := time.Now()
	maxDuration := 5 * time.Minute // 最大运行时间

	for {
		select {
		case <-ctx.Done():
			r.logger.Debug("备用签名收集机制停止", "checkpointHash", checkpointHash.String())
			return
		case <-ticker.C:
			// 检查是否超时
			if time.Since(startTime) > maxDuration {
				r.logger.Warn("备用签名收集机制超时，停止运行", "checkpointHash", checkpointHash.String())
				return
			}

			// 尝试查询待处理的签名请求
			r.queryPendingSignatureRequests()

			// 尝试广播签名查询
			r.broadcastSignatureQuery()
		}
	}
}

// createSignatureListener 创建签名响应监听器
func (r *dposRuntime) createSignatureListener(checkpointHash types.Hash, signatureCh chan<- *SignatureResponse) *SignatureListener {
	return &SignatureListener{
		checkpointHash: checkpointHash,
		signatureCh:    signatureCh,
		receivedSigs:   make(map[types.Address]bool),
		logger:         r.logger,
	}
}

// isValidator 检查当前节点是否是验证者
func (r *dposRuntime) isValidator() bool {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot check if validator")
		return false
	}

	currentAddr := types.Address(r.config.Key.Address())

	// 🆕 直接从数据库读取验证者信息，确保数据一致性
	if r.backend == nil {
		r.logger.Error("❌ backend为nil，无法检查验证者状态")
		return false
	}

	// 通过类型断言访问DPoS的state字段
	dposBackend, ok := r.backend.(*DPoS)
	if !ok {
		r.logger.Error("❌ 无法访问数据库，backend类型错误")
		return false
	}

	if dposBackend.state == nil || dposBackend.state.StakeStore == nil {
		r.logger.Error("❌ state或StakeStore不可用，无法检查验证者状态")
		return false
	}

	// 🆕 使用公共函数获取排序和限制后的验证者
	dbValidators, err := dposBackend.GetSortedValidatorsWithLimit()
	if err != nil {
		r.logger.Error("❌ 从数据库读取验证者失败", "error", err)
		return false
	}

	if len(dbValidators) == 0 {
		r.logger.Error("❌ 数据库中没有验证者")
		return false
	}

	// 🆕 在排序截取后的验证者集合中查找当前节点
	for idx, delegate := range dbValidators {
		if delegate.Address == currentAddr {
			// 关键：检查stake是否足够且是否活跃
			if delegate.IsActive && isPositive(delegate.VotingPower) {
				// 额外查询故障标志状态
				isFaulty := false
				if dposBackend != nil {
					if faultInfo := dposBackend.getValidatorFaultInfo(currentAddr); faultInfo != nil {
						if flag, ok := faultInfo["isFaulty"].(bool); ok {
							isFaulty = flag
						}
					}
				}

				msg := "🎯 当前节点是活跃验证者（从数据库）"
				if isFaulty {
					msg = "🎯 当前节点是活跃验证者但故障标志为true（从数据库）"
				} else {
					msg = "🎯 当前节点是活跃验证者且故障标志为false（从数据库）"
				}

				// 🆕 使用统一日志间隔（10秒）
				r.logOnceWithInterval("active_validator_from_db", 10*time.Second, "info", msg,
					"address", currentAddr.String(),
					"votingPower", delegate.VotingPower.String(),
					"isActive", delegate.IsActive,
					"isFaulty", isFaulty,
					"rank", idx+1,
					"totalValidators", len(dbValidators))
				return true
			} else {
				r.logger.Info("❌ 当前节点不是活跃验证者（stake不足或不活跃，从数据库）",
					"address", currentAddr.String(),
					"votingPower", delegate.VotingPower.String(),
					"isActive", delegate.IsActive)
				return false
			}
		}
	}

	// 当前节点不在截取后的验证者集合中
	maxValidators := int(r.config.DelegateCount)
	if dposBackend.config != nil && dposBackend.config.DPoSValidatorsCount > 0 {
		maxValidators = int(dposBackend.config.DPoSValidatorsCount)
	}

	// 🆕 详细调试信息
	r.logger.Info("❌ 当前节点不在数据库验证者集合中（可能权重不足被截取）",
		"address", currentAddr.String(),
		"maxValidators", maxValidators,
		"dbValidatorsCount", len(dbValidators),
		"configDelegateCount", r.config.DelegateCount,
		"configDPoSValidatorsCount", dposBackend.config.DPoSValidatorsCount)

	// 🆕 显示所有验证者的详细信息
	r.logger.Info("🔍 截取后的验证者详细信息:")
	for i, validator := range dbValidators {
		r.logger.Info("👤 截取后验证者",
			"index", i+1,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"isCurrentNode", validator.Address == currentAddr)
	}
	return false
}

// generateSignatureResponse 生成签名响应
func (r *dposRuntime) generateSignatureResponse(request *SignatureRequest) error {
	validatorAddr := types.Address(r.config.Key.Address())

	r.logger.Debug("🎯 收到签名请求，开始生成签名响应",
		"myAddress", validatorAddr.String(),
		"requestBlockNumber", request.BlockNumber,
		"requestCheckpointHash", request.CheckpointHash.String(),
		"proposer", request.Proposer.String())

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot generate signature response")
		return fmt.Errorf("key not available, cannot generate signature response")
	}

	// 获取自己的BLS私钥
	blsKey, err := r.getBLSPrivateKey()
	if err != nil {
		r.logger.Error("❌ 获取BLS私钥失败", "address", validatorAddr.String(), "error", err)
		return fmt.Errorf("failed to get BLS private key: %w", err)
	}

	r.logger.Debug("✅ 获取BLS私钥成功", "address", validatorAddr.String())

	// 生成签名
	signature, err := blsKey.Sign(request.CheckpointHash[:], signer.DomainValidatorSet)
	if err != nil {
		r.logger.Error("❌ 签名生成失败", "address", validatorAddr.String(), "error", err)
		return fmt.Errorf("failed to sign checkpoint hash: %w", err)
	}

	// 序列化BLS签名
	signatureBytes, err := signature.Marshal()
	if err != nil {
		r.logger.Error("❌ 签名序列化失败", "address", validatorAddr.String(), "error", err)
		return fmt.Errorf("failed to marshal signature: %w", err)
	}

	// 创建内部签名响应结构
	internalResponse := &SignatureResponse{
		ValidatorAddr:  types.Address(r.config.Key.Address()),
		Signature:      signatureBytes,
		CheckpointHash: request.CheckpointHash,
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 优先使用网络集成层发送消息
	if r.networkIntegration != nil {
		if err := r.networkIntegration.BroadcastSignatureResponse(internalResponse); err != nil {
			r.logger.Warn("通过网络集成层发送签名响应失败，使用回退模式", "error", err)
			// 回退到日志记录
			r.logger.Debug("生成签名响应（回退模式）",
				"validator", types.Address(r.config.Key.Address()).String(),
				"checkpointHash", request.CheckpointHash.String(),
				"signatureLength", len(signatureBytes))
			return nil
		}

		// 签名响应已广播
		return nil
	}

	// 如果没有网络集成层，记录错误并返回
	r.logger.Error("网络集成层不可用，无法发送签名响应",
		"validator", types.Address(r.config.Key.Address()).String(),
		"checkpointHash", request.CheckpointHash.String())
	return fmt.Errorf("网络集成层不可用，无法发送签名响应")
}

// verifyValidatorSignatureByAddress 根据地址验证验证者签名
func (r *dposRuntime) verifyValidatorSignatureByAddress(validatorAddr types.Address, signature []byte, checkpointHash types.Hash) error {
	// 查找验证者
	var validator *validator.ValidatorMetadata
	for _, delegate := range r.delegates {
		if delegate.Address == validatorAddr {
			validator = delegate
			break
		}
	}

	if validator == nil {
		return fmt.Errorf("validator %s not found", validatorAddr)
	}

	return r.verifyValidatorSignature(validator, signature, checkpointHash)
}

// verifyValidatorSignature 验证验证者签名
func (r *dposRuntime) verifyValidatorSignature(delegate *validator.ValidatorMetadata, signature []byte, checkpointHash types.Hash) error {
	if delegate.BlsKey == nil {
		// 🆕 尝试从持久化存储中获取BLS公钥
		// 静默处理，不打印日志

		// 通过网络集成层获取BLS公钥
		if r.networkIntegration != nil {
			blsKeyBytes, exists := r.networkIntegration.GetBLSKey(delegate.Address)
			if exists && len(blsKeyBytes) > 0 {
				// 解析BLS公钥
				blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
				if err != nil {
					r.logger.Warn("解析持久化的BLS公钥失败", "validator", delegate.Address.String(), "error", err)
				} else {
					// 设置BLS公钥到delegate
					delegate.BlsKey = blsKey
					r.logger.Debug("成功从持久化存储恢复BLS公钥", "validator", delegate.Address.String())
				}
			} else {
				// 如果缓存中没有，尝试从数据库恢复
				// 静默处理，不打印日志
				if err := r.networkIntegration.restoreBLSKeysFromDatabase(); err != nil {
					r.logger.Debug("从数据库恢复BLS公钥失败", "validator", delegate.Address.String(), "error", err)
				} else {
					// 再次尝试从缓存获取
					blsKeyBytes, exists := r.networkIntegration.GetBLSKey(delegate.Address)
					if exists && len(blsKeyBytes) > 0 {
						blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
						if err == nil {
							delegate.BlsKey = blsKey
							r.logger.Debug("成功从数据库恢复BLS公钥", "validator", delegate.Address.String())
						}
					}
				}
			}
		}

		// 如果仍然没有BLS公钥，根据验证者类型选择恢复方法
		if delegate.BlsKey == nil {
			// 检查是否是本地验证者
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance.key != nil &&
				delegate.Address == types.Address(dposInstance.key.Address()) {
				// 本地验证者：从validator-bls.key文件恢复
				r.logger.Warn("⚠️ 本地验证者缺少BLS公钥，尝试从validator-bls.key文件恢复",
					"validator", delegate.Address.String())

				if r.config != nil && r.config.DataDir != "" {
					// 构建BLS私钥文件路径
					parentDir := filepath.Dir(r.config.DataDir)
					keyFilePath := filepath.Join(parentDir, "validator-bls.key")

					// 检查文件是否存在
					if _, err := os.Stat(keyFilePath); err == nil {
						// 读取私钥文件
						privateKeyData, err := os.ReadFile(keyFilePath)
						if err == nil {
							// 获取十六进制字符串（去除可能的换行符）
							privateKeyHex := strings.TrimSpace(string(privateKeyData))

							// 检查并修正私钥长度
							if len(privateKeyHex)%2 != 0 {
								privateKeyHex = "0" + privateKeyHex
							}

							// 解析BLS私钥
							privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
							if err == nil {
								// 从私钥生成公钥
								publicKey := privateKey.PublicKey()
								delegate.BlsKey = publicKey

								// 将BLS公钥保存到网络集成层缓存
								if r.networkIntegration != nil {
									publicKeyBytes := publicKey.Marshal()
									if err := r.networkIntegration.saveBLSKey(delegate.Address, publicKeyBytes); err != nil {
										r.logger.Warn("⚠️ 保存BLS公钥到缓存失败",
											"validator", delegate.Address.String(),
											"error", err)
									}
								}

								r.logger.Info("✅ 本地验证者BLS公钥恢复成功",
									"validator", delegate.Address.String(),
									"filePath", keyFilePath)
							} else {
								r.logger.Warn("⚠️ 解析validator-bls.key文件失败",
									"validator", delegate.Address.String(),
									"filePath", keyFilePath,
									"error", err)
							}
						} else {
							r.logger.Warn("⚠️ 读取validator-bls.key文件失败",
								"validator", delegate.Address.String(),
								"filePath", keyFilePath,
								"error", err)
						}
					} else {
						r.logger.Warn("⚠️ validator-bls.key文件不存在",
							"validator", delegate.Address.String(),
							"filePath", keyFilePath)
					}
				}
			} else {
				// 远程验证者：通过网络请求获取BLS公钥
				// 静默处理，不打印日志

				if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
					// 使用DPoS实例的网络请求功能
					blsKey, err := dposInstance.GetBLSKeyForValidator(delegate.Address)
					if err == nil {
						delegate.BlsKey = blsKey
						// 静默处理，不打印日志
					} else {
						r.logger.Warn("⚠️ 远程验证者BLS公钥获取失败",
							"validator", delegate.Address.String(),
							"error", err)
					}
				}
			}

			// 如果仍然没有BLS公钥，返回错误
			if delegate.BlsKey == nil {
				r.logger.Error("🚨 BLS公钥恢复失败，验证者无法验证签名",
					"validator", delegate.Address.String(),
					"checkpointHash", checkpointHash.String())
				return fmt.Errorf("validator has no BLS public key")
			}
		}
	}

	// 检查签名长度
	if len(signature) != 64 {
		return fmt.Errorf("signature length must be 64 bytes, got %d", len(signature))
	}

	// 解析签名
	blsSignature, err := bls.UnmarshalSignature(signature)
	if err != nil {
		return fmt.Errorf("failed to unmarshal signature: %w", err)
	}

	// 验证签名
	if !blsSignature.Verify(delegate.BlsKey, checkpointHash[:], signer.DomainValidatorSet) {
		return fmt.Errorf("signature verification failed")
	}

	// 静默处理，不打印日志

	return nil
}

// validateSignatureResponse 验证签名响应
func (r *dposRuntime) validateSignatureResponse(response *SignatureResponse) error {
	// 1. 检查基本字段
	if response.ValidatorAddr == types.ZeroAddress {
		return errors.New("invalid validator address")
	}
	if len(response.Signature) == 0 {
		return errors.New("empty signature")
	}
	if response.CheckpointHash == types.ZeroHash {
		return errors.New("invalid checkpoint hash")
	}

	// 2. 检查时间戳
	now := uint64(time.Now().Unix())
	if response.Timestamp < now-300 || response.Timestamp > now+60 {
		return errors.New("response timestamp out of range")
	}

	// 3. 检查验证者是否为有效验证者
	isValidValidator := false
	for _, delegate := range r.delegates {
		if delegate.Address == response.ValidatorAddr {
			isValidValidator = true
			break
		}
	}
	if !isValidValidator {
		return errors.New("validator is not a valid delegate")
	}

	return nil
}

// SignatureQueryRequest 签名查询请求
type SignatureQueryRequest struct {
	RequesterAddr types.Address `json:"requesterAddr"`
	Timestamp     uint64        `json:"timestamp"`
}

// isSignatureRequestProcessed 检查签名请求是否已经处理过（去重机制）
func (r *dposRuntime) isSignatureRequestProcessed(proposer types.Address, checkpointHash types.Hash) bool {
	r.signatureRequestDedupMutex.RLock()
	defer r.signatureRequestDedupMutex.RUnlock()

	key := fmt.Sprintf("%s-%s", proposer.String(), checkpointHash.String())
	lastProcessed, exists := r.processedSignatureRequests[key]

	if !exists {
		return false
	}

	// 如果超过5分钟，认为可以重新处理（避免内存泄漏）
	if time.Since(lastProcessed) > 5*time.Minute {
		return false
	}

	return true
}

// markSignatureRequestProcessed 标记签名请求已处理
func (r *dposRuntime) markSignatureRequestProcessed(proposer types.Address, checkpointHash types.Hash) {
	r.signatureRequestDedupMutex.Lock()
	defer r.signatureRequestDedupMutex.Unlock()

	key := fmt.Sprintf("%s-%s", proposer.String(), checkpointHash.String())
	r.processedSignatureRequests[key] = time.Now()
}

// isSignatureResponseBroadcasted 检查签名响应是否已经广播过（去重机制）
func (r *dposRuntime) isSignatureResponseBroadcasted(responseKey string) bool {
	r.signatureResponseDedupMutex.RLock()
	defer r.signatureResponseDedupMutex.RUnlock()

	lastProcessed, exists := r.processedSignatureResponses[responseKey]

	if !exists {
		return false
	}

	// 如果超过5分钟，认为可以重新广播
	if time.Since(lastProcessed) > 5*time.Minute {
		return false
	}

	return true
}

// markSignatureResponseBroadcasted 标记签名响应已广播
func (r *dposRuntime) markSignatureResponseBroadcasted(responseKey string) {
	r.signatureResponseDedupMutex.Lock()
	defer r.signatureResponseDedupMutex.Unlock()

	r.processedSignatureResponses[responseKey] = time.Now()
}

// isSignatureResponseGenerated 检查签名响应是否已经生成过（去重机制）
func (r *dposRuntime) isSignatureResponseGenerated(generateKey string) bool {
	r.signatureGenerationDedupMutex.RLock()
	defer r.signatureGenerationDedupMutex.RUnlock()

	lastProcessed, exists := r.processedSignatureGenerations[generateKey]

	if !exists {
		return false
	}

	// 如果超过5分钟，认为可以重新生成
	if time.Since(lastProcessed) > 5*time.Minute {
		return false
	}

	return true
}

// markSignatureResponseGenerated 标记签名响应已生成
func (r *dposRuntime) markSignatureResponseGenerated(generateKey string) {
	r.signatureGenerationDedupMutex.Lock()
	defer r.signatureGenerationDedupMutex.Unlock()

	r.processedSignatureGenerations[generateKey] = time.Now()
}

// getSignatureCollectionProgress 获取签名收集进度
func (r *dposRuntime) getSignatureCollectionProgress(checkpointHash types.Hash) float64 {
	if r.networkIntegration != nil {
		collector := r.networkIntegration.GetSignatureCollector(checkpointHash)
		if collector != nil {
			return float64(collector.GetCollectedCount()) / float64(collector.requiredCount)
		}
	}
	return 0.0
}

// fallbackSignatureRequestPropagation 备用签名请求传播机制
func (r *dposRuntime) fallbackSignatureRequestPropagation(protoRequest *dposProto.SignatureRequest, checkpointHash types.Hash) {
	r.logger.Debug("=== 备用转传播启动 ===", "区块高度", protoRequest.BlockNumber, "checkpointHash", checkpointHash.String())

	peers := r.network.Peers()
	successCount := 0
	totalPeers := len(peers)

	for _, peer := range peers {
		peerID := peer.Info.ID

		// 跳过自己
		if peerID.String() == r.network.AddrInfo().ID.String() {
			continue
		}

		// 尝试直接发送签名请求，使用指数退避重试
		if err := r.sendDirectSignatureRequestWithRetry(peerID, protoRequest); err != nil {
			r.logger.Warn("直接签名请求失败", "peer", peerID.String()[:8], "区块高度", protoRequest.BlockNumber, "错误", err)
		} else {
			successCount++
			r.logger.Debug("直接签名请求成功", "peer", peerID.String()[:8], "区块高度", protoRequest.BlockNumber)
		}
	}

	// 修复统计逻辑：确保成功率不会超过100%
	if totalPeers > 1 {
		effectiveTotal := totalPeers - 1 // 排除自己
		successRate := float64(successCount) / float64(effectiveTotal)

		// 限制成功率最大为100%
		if successRate > 1.0 {
			successRate = 1.0
		}

		r.logger.Debug("=== 备用转传播完成 ===",
			"区块高度", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"成功节点数", successCount,
			"总节点数", effectiveTotal,
			"成功率", fmt.Sprintf("%.2f%%", successRate*100))
	}
}

// simpleFallbackMonitoring 基于时间的简单备用传播监控
func (r *dposRuntime) simpleFallbackMonitoring(protoRequest *dposProto.SignatureRequest, checkpointHash types.Hash) {
	// 第一层：1秒后检查进度
	time.Sleep(1 * time.Second)
	progress := r.getSignatureCollectionProgress(checkpointHash)
	if progress < 0.2 { // 20%以下
		r.logger.Debug("签名收集进度极低，启动备用传播",
			"区块高度", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"progress", fmt.Sprintf("%.2f%%", progress*100))
		go r.fallbackSignatureRequestPropagation(protoRequest, checkpointHash)
		return
	}

	// 第二层：1.5秒后再次检查
	time.Sleep(500 * time.Millisecond) // 总共1.5秒
	progress = r.getSignatureCollectionProgress(checkpointHash)
	if progress < 0.5 { // 50%以下
		r.logger.Debug("签名收集进度不足，启动备用传播",
			"区块高度", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"progress", fmt.Sprintf("%.2f%%", progress*100))
		go r.fallbackSignatureRequestPropagation(protoRequest, checkpointHash)
	}
}

// debugPendingSignatureRequests 调试方法：检查待处理的签名请求
func (r *dposRuntime) debugPendingSignatureRequests() {
	r.signatureRequestMutex.RLock()
	defer r.signatureRequestMutex.RUnlock()

	if r.pendingSignatureRequests == nil {
		r.logger.Info("pendingSignatureRequests映射为nil")
		return
	}
}
