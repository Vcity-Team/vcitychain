package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/txpool"
	"github.com/Vcity-Team/vcitychain/types"
	hcf "github.com/hashicorp/go-hclog"
)

//nolint:godox
// TODO: Add opentracing (to be fixed in EVM-540)

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
	Logger hcf.Logger

	// txPoolInterface implementation
	TxPool txPoolInterface

	// BaseFee is the base fee
	BaseFee uint64
}

func NewBlockBuilder(params *BlockBuilderParams) *BlockBuilder {
	return &BlockBuilder{
		params: params,
	}
}

var _ blockBuilder = &BlockBuilder{}

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

// Init initializes block builder before adding transactions and actual block building
func (b *BlockBuilder) Reset() error {
	// set the timestamp
	parentTime := time.Unix(int64(b.params.Parent.Timestamp), 0)
	headerTime := parentTime.Add(b.params.BlockTime)

	if headerTime.Before(time.Now().UTC()) {
		headerTime = time.Now().UTC()
	}

	// 创建默认的extraData - 使用Extra对象的MarshalRLPTo方法
	extra := &Extra{} // 所有字段都是nil，会被编码为空数组
	defaultExtraData := extra.MarshalRLPTo(nil)

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
		ExtraData:    defaultExtraData, // 设置默认的extraData
		MixHash:      PolyBFTMixDigest, // 设置正确的MixHash
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

// Block returns the built block if nil, it is not built yet
func (b *BlockBuilder) Block() *types.Block {
	return b.block
}

// GetState returns the state transition object
func (b *BlockBuilder) GetState() *state.Transition {
	return b.state
}

// Build creates the state and the final block
func (b *BlockBuilder) Build(handler func(h *types.Header)) (*types.FullBlock, error) {
	if handler != nil {
		handler(b.header)
	}

	// 添加调试日志：记录状态提交开始时间
	commitStart := time.Now()
	b.params.Logger.Debug("🔍 开始状态提交", "timestamp", commitStart.Format("15:04:05.000"))

	_, stateRoot, err := b.state.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to commit the state changes: %w", err)
	}

	// 添加调试日志：记录状态提交耗时
	commitDuration := time.Since(commitStart)
	b.params.Logger.Debug("✅ 状态提交完成",
		"duration", commitDuration.String(),
		"timestamp", time.Now().Format("15:04:05.000"))

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
		return err
	}

	b.txns = append(b.txns, tx)

	return nil
}

// Fill fills the block with transactions from the txpool
func (b *BlockBuilder) Fill() {
	// 🚀 优化：移除2秒定时器，立即处理所有可用交易
	b.params.TxPool.Prepare()

	// 立即处理所有可用交易，不等待定时器
	for {
		tx := b.params.TxPool.Peek()
		if tx == nil {
			// 没有更多交易，立即返回
			return
		}

		// execute transactions one by one
		finished, err := b.writeTxPoolTransaction(tx)
		if err != nil {
			b.params.Logger.Debug("Fill transaction error", "hash", tx.Hash, "err", err)
		}

		if finished {
			return
		}
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
			b.params.TxPool.Demote(tx)

			return false, err
		} else {
			b.params.TxPool.Drop(tx)

			return false, err
		}
	}

	// remove tx from the pool and add it to the list of all block transactions
	b.params.TxPool.Pop(tx)

	return false, nil
}

// GetTransactions returns the transactions that have been added to the block
func (b *BlockBuilder) GetTransactions() []*types.Transaction {
	return b.txns
}
