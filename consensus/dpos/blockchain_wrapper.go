package dpos

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/consensus"
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

	// 🆕 检查是否是生产节点自己生产的区块
	blockMiner := types.BytesToAddress(header.Miner)
	keyAddr := p.keyAddr
	isOurBlock := blockMiner == keyAddr

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 🆕 如果是epoch结束区块且不是生产节点自己生产的区块，处理奖励分发
	if isEpochEnd && !isOurBlock {

		if err := p.processRewardDistributionInBlock(block, transition); err != nil {
			return nil, fmt.Errorf("failed to process reward distribution: %w", err)
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
		if err = transition.Write(tx); err != nil {
			return nil, fmt.Errorf("process block tx error, tx = %v, err = %w", tx.Hash, err)
		}
	}

	// 🆕 检查是否是生产节点自己生产的区块
	blockMiner := types.BytesToAddress(header.Miner)
	keyAddr := p.keyAddr
	isOurBlock := blockMiner == keyAddr

	isEpochEnd := p.isEpochEndBlock(block.Number())

	// 🆕 如果是epoch结束区块且不是生产节点自己生产的区块，处理奖励分发
	if isEpochEnd && !isOurBlock {

		if err := p.processRewardDistributionInBlock(block, transition); err != nil {
			return nil, fmt.Errorf("failed to process reward distribution: %w", err)
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

	// 计算当前epoch的第一个区块号
	// 例如：区块7379属于epoch 738，第一个区块是7370
	currentEpoch := (blockNumber / epochSize) + 1
	firstBlockInEpoch := (currentEpoch - 1) * epochSize

	// 检查是否是epoch的最后一个区块
	isEpochEnd := firstBlockInEpoch+epochSize-1 == blockNumber

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

	// 解析ExtraData获取奖励分发信息
	extra := &Extra{}
	if err := extra.UnmarshalRLP(block.Header.ExtraData); err != nil {
		return fmt.Errorf("failed to unmarshal extra data: %w", err)
	}

	if extra.RewardDistribution == nil {
		// 没有奖励分发信息，跳过
		return nil
	}

	rewardInfo := extra.RewardDistribution

	// 处理奖励分发（直接修改状态，符合TRON主流做法）

	// 获取奖励账户地址（从配置中获取）
	rewardAccount := types.StringToAddress("0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe")

	// 计算总奖励金额
	totalReward := new(big.Int)
	for _, amount := range rewardInfo.Rewards {
		totalReward.Add(totalReward, amount)
	}

	// 检查奖励账户余额是否足够
	currentBalance := transition.GetBalance(rewardAccount)

	if currentBalance.Cmp(totalReward) < 0 {
		return fmt.Errorf("insufficient balance in reward account: current=%s, required=%s",
			currentBalance.String(), totalReward.String())
	}

	// 从奖励账户扣除总奖励
	transition.Txn().SubBalance(rewardAccount, totalReward)

	// 直接给每个验证者增加余额（不消耗gas，符合TRON做法）
	for addrStr, amount := range rewardInfo.Rewards {
		addr := types.StringToAddress(addrStr)

		// 直接修改状态，不通过Transfer
		transition.Txn().AddBalance(addr, amount)
	}

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
