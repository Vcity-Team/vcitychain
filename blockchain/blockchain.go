package blockchain

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain/storage"
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/types/buildroot"

	"github.com/hashicorp/go-hclog"
	lru "github.com/hashicorp/golang-lru"
)

const (
	// blockGasTargetDivisor is the bound divisor of the gas limit, used in update calculations
	blockGasTargetDivisor uint64 = 1024

	// defaultCacheSize is the default size for Blockchain LRU cache structures
	defaultCacheSize int = 100
)

var (
	ErrNoBlock              = errors.New("no block data passed in")
	ErrParentNotFound       = errors.New("parent block not found")
	ErrInvalidParentHash    = errors.New("parent block hash is invalid")
	ErrParentHashMismatch   = errors.New("invalid parent block hash")
	ErrInvalidBlockSequence = errors.New("invalid block sequence")
	ErrInvalidSha3Uncles    = errors.New("invalid block sha3 uncles root")
	ErrInvalidTxRoot        = errors.New("invalid block transactions root")
	ErrInvalidReceiptsSize  = errors.New("invalid number of receipts")
	ErrInvalidStateRoot     = errors.New("invalid block state root")
	ErrInvalidGasUsed       = errors.New("invalid block gas used")
	ErrInvalidReceiptsRoot  = errors.New("invalid block receipts root")
)

// Blockchain is a blockchain reference
type Blockchain struct {
	logger hclog.Logger // The logger object

	db        storage.Storage // The Storage object (database)
	consensus Verifier
	executor  Executor
	txSigner  TxSigner

	config  *chain.Chain // Config containing chain information
	genesis types.Hash   // The hash of the genesis block

	headersCache    *lru.Cache // LRU cache for the headers
	difficultyCache *lru.Cache // LRU cache for the difficulty

	// We need to keep track of block receipts between the verification phase
	// and the insertion phase of a new block coming in. To avoid having to
	// execute the transactions twice, we save the receipts from the initial execution
	// in a cache, so we can grab it later when inserting the block.
	// This is of course not an optimal solution - a better one would be to add
	// the receipts to the proposed block (like we do with Transactions and Uncles), but
	// that is currently not possible because it would break backwards compatibility due to
	// insane conditionals in the RLP unmarshal methods for the Block structure, which prevent
	// any new fields from being added
	receiptsCache *lru.Cache // LRU cache for the block receipts

	currentHeader     atomic.Pointer[types.Header] // The current header
	currentDifficulty atomic.Pointer[big.Int]      // The current difficulty of the chain (total difficulty)

	stream *eventStream // Event subscriptions

	// 🆕 记录上次本地生产的区块信息（用于统计）
	lastProducedBlockNumber uint64     // 上次生产的区块号
	lastProducedBlockTime   time.Time  // 上次生产区块的时间
	lastProducedBlockMutex  sync.Mutex // 保护上次生产区块信息的互斥锁

	// 🆕 记录区块生产开始时间（用于统计生产耗时）
	lastBlockProductionStartTime  time.Time  // 上次区块生产开始的时间
	lastBlockProductionStartMutex sync.Mutex // 保护生产开始时间的互斥锁

	gpAverage *gasPriceAverage // A reference to the average gas price

	writeLock sync.Mutex
}

// gasPriceAverage keeps track of the average gas price (rolling average)
type gasPriceAverage struct {
	sync.RWMutex

	price *big.Int // The average gas price that gets queried
	count *big.Int // Param used in the avg. gas price calculation
}

type Verifier interface {
	VerifyHeader(header *types.Header) error
	ProcessHeaders(headers []*types.Header) error
	GetBlockCreator(header *types.Header) (types.Address, error)
	PreCommitState(block *types.Block, txn *state.Transition) error
}

type Executor interface {
	ProcessBlock(parentRoot types.Hash, block *types.Block, blockCreator types.Address) (*state.Transition, error)
}

type TxSigner interface {
	// Sender returns the sender of the transaction
	Sender(tx *types.Transaction) (types.Address, error)
}

type BlockResult struct {
	Root     types.Hash
	Receipts []*types.Receipt
	TotalGas uint64
}

// updateGasPriceAvg updates the rolling average value of the gas price
func (b *Blockchain) updateGasPriceAvg(newValues []*big.Int) {
	b.gpAverage.Lock()
	defer b.gpAverage.Unlock()

	// Sum the values for quick reference
	sum := big.NewInt(0)
	for _, val := range newValues {
		sum = sum.Add(sum, val)
	}

	// There is no previous average data,
	// so this new value set will instantiate it
	if b.gpAverage.count.Uint64() == 0 {
		b.calcArithmeticAverage(newValues, sum)

		return
	}

	// There is existing average data,
	// use it to generate a new average
	b.calcRollingAverage(newValues, sum)
}

// calcArithmeticAverage calculates and sets the arithmetic average
// of the passed in data set
func (b *Blockchain) calcArithmeticAverage(newValues []*big.Int, sum *big.Int) {
	newAverageCount := big.NewInt(int64(len(newValues)))
	newAverage := sum.Div(sum, newAverageCount)

	b.gpAverage.price = newAverage
	b.gpAverage.count = newAverageCount
}

// calcRollingAverage calculates the new average based on the
// moving average formula:
// new average = old average * (n-len(M))/n + (sum of values in M)/n)
// where n is the old average data count, and M is the new data set
func (b *Blockchain) calcRollingAverage(newValues []*big.Int, sum *big.Int) {
	var (
		// Save references to old counts
		oldCount   = b.gpAverage.count
		oldAverage = b.gpAverage.price

		inputSetCount = big.NewInt(0).SetInt64(int64(len(newValues)))
	)

	// old average * (n-len(M))/n
	newAverage := big.NewInt(0).Div(
		big.NewInt(0).Mul(
			oldAverage,
			big.NewInt(0).Sub(oldCount, inputSetCount),
		),
		oldCount,
	)

	// + (sum of values in M)/n
	newAverage.Add(
		newAverage,
		big.NewInt(0).Div(
			sum,
			oldCount,
		),
	)

	// Update the references
	b.gpAverage.price = newAverage
	b.gpAverage.count = inputSetCount.Add(inputSetCount, b.gpAverage.count)
}

// GetAvgGasPrice returns the average gas price for the chain
func (b *Blockchain) GetAvgGasPrice() *big.Int {
	b.gpAverage.RLock()
	defer b.gpAverage.RUnlock()

	return b.gpAverage.price
}

// NewBlockchain creates a new blockchain object
func NewBlockchain(
	logger hclog.Logger,
	db storage.Storage,
	config *chain.Chain,
	consensus Verifier,
	executor Executor,
	txSigner TxSigner,
) (*Blockchain, error) {
	b := &Blockchain{
		logger:    logger.Named("blockchain"),
		config:    config,
		consensus: consensus,
		db:        db,
		executor:  executor,
		txSigner:  txSigner,
		stream:    newEventStream(),
		gpAverage: &gasPriceAverage{
			price: big.NewInt(0),
			count: big.NewInt(0),
		},
	}

	if err := b.initCaches(defaultCacheSize); err != nil {
		return nil, err
	}

	// Push the initial event to the stream
	b.stream.push(&Event{})

	return b, nil
}

// initCaches initializes the blockchain caches with the specified size
func (b *Blockchain) initCaches(size int) error {
	var err error

	b.headersCache, err = lru.New(size)
	if err != nil {
		return fmt.Errorf("unable to create headers cache, %w", err)
	}

	b.difficultyCache, err = lru.New(size)
	if err != nil {
		return fmt.Errorf("unable to create difficulty cache, %w", err)
	}

	b.receiptsCache, err = lru.New(size)
	if err != nil {
		return fmt.Errorf("unable to create receipts cache, %w", err)
	}

	return nil
}

// ComputeGenesis computes the genesis hash, and updates the blockchain reference
func (b *Blockchain) ComputeGenesis() error {
	// try to write the genesis block
	head, ok := b.db.ReadHeadHash()

	if ok {
		// initialized storage
		b.genesis, ok = b.db.ReadCanonicalHash(0)
		if !ok {
			return fmt.Errorf("failed to load genesis hash")
		}

		// validate that the genesis file in storage matches the chain.Genesis
		if b.genesis != b.config.Genesis.Hash() {
			return fmt.Errorf("genesis file does not match current genesis")
		}

		header, ok := b.GetHeaderByHash(head)
		if !ok {
			return fmt.Errorf("failed to get header with hash %s", head.String())
		}

		diff, ok := b.GetTD(head)
		if !ok {
			return fmt.Errorf("failed to read difficulty")
		}

		b.logger.Info(
			"Current header",
			"hash",
			header.Hash.String(),
			"number",
			header.Number,
		)

		b.setCurrentHeader(header, diff)
	} else {
		// empty storage, write the genesis
		if err := b.writeGenesis(b.config.Genesis); err != nil {
			return err
		}
	}

	b.logger.Info("genesis", "hash", b.config.Genesis.Hash())

	return nil
}

func (b *Blockchain) GetConsensus() Verifier {
	return b.consensus
}

// SetConsensus sets the consensus
func (b *Blockchain) SetConsensus(c Verifier) {
	b.consensus = c
}

// SetExecutor sets the executor
func (b *Blockchain) SetExecutor(e Executor) {
	b.executor = e
}

// SetBlockProductionStartTime 设置区块生产开始时间（用于统计生产耗时）
func (b *Blockchain) SetBlockProductionStartTime() {
	b.lastBlockProductionStartMutex.Lock()
	defer b.lastBlockProductionStartMutex.Unlock()
	b.lastBlockProductionStartTime = time.Now()
}

// setCurrentHeader sets the current header
func (b *Blockchain) setCurrentHeader(h *types.Header, diff *big.Int) {
	// Update the header (atomic)
	header := h.Copy()
	b.currentHeader.Store(header)

	// Update the difficulty (atomic)
	difficulty := new(big.Int).Set(diff)
	b.currentDifficulty.Store(difficulty)
}

// Header returns the current header (atomic)
func (b *Blockchain) Header() *types.Header {
	return b.currentHeader.Load()
}

// CurrentTD returns the current total difficulty (atomic)
func (b *Blockchain) CurrentTD() *big.Int {
	return b.currentDifficulty.Load()
}

// Config returns the blockchain configuration
func (b *Blockchain) Config() *chain.Params {
	return b.config.Params
}

// GetHeader returns the block header using the hash
func (b *Blockchain) GetHeader(hash types.Hash, number uint64) (*types.Header, bool) {
	return b.GetHeaderByHash(hash)
}

// GetBlock returns the block using the hash
func (b *Blockchain) GetBlock(hash types.Hash, number uint64, full bool) (*types.Block, bool) {
	return b.GetBlockByHash(hash, full)
}

// GetParent returns the parent header
func (b *Blockchain) GetParent(header *types.Header) (*types.Header, bool) {
	return b.readHeader(header.ParentHash)
}

// Genesis returns the genesis block
func (b *Blockchain) Genesis() types.Hash {
	return b.genesis
}

// CalculateGasLimit returns the gas limit of the next block after parent
func (b *Blockchain) CalculateGasLimit(number uint64) (uint64, error) {
	parent, ok := b.GetHeaderByNumber(number - 1)
	if !ok {
		return 0, fmt.Errorf("parent of block %d not found", number)
	}

	return b.calculateGasLimit(parent.GasLimit), nil
}

// calculateGasLimit calculates gas limit in reference to the block gas target
func (b *Blockchain) calculateGasLimit(parentGasLimit uint64) uint64 {
	// The gas limit cannot move more than 1/1024 * parentGasLimit
	// in either direction per block
	blockGasTarget := b.Config().BlockGasTarget

	// Check if the gas limit target has been set
	if blockGasTarget == 0 {
		// The gas limit target has not been set,
		// so it should use the parent gas limit
		return parentGasLimit
	}

	// Check if the gas limit is already at the target
	if parentGasLimit == blockGasTarget {
		// The gas limit is already at the target, no need to move it
		return blockGasTarget
	}

	delta := parentGasLimit * 1 / blockGasTargetDivisor
	if parentGasLimit < blockGasTarget {
		// The gas limit is lower than the gas target, so it should
		// increase towards the target
		return common.Min(blockGasTarget, parentGasLimit+delta)
	}

	// The gas limit is higher than the gas target, so it should
	// decrease towards the target
	return common.Max(blockGasTarget, common.Max(parentGasLimit-delta, 0))
}

// writeGenesis wrapper for the genesis write function
func (b *Blockchain) writeGenesis(genesis *chain.Genesis) error {
	header := genesis.GenesisHeader()
	header.ComputeHash()

	if err := b.writeGenesisImpl(header); err != nil {
		return err
	}

	return nil
}

// writeGenesisImpl writes the genesis file to the DB + blockchain reference
func (b *Blockchain) writeGenesisImpl(header *types.Header) error {
	batchWriter := storage.NewBatchWriter(b.db)

	newTD := new(big.Int).SetUint64(header.Difficulty)

	batchWriter.PutCanonicalHeader(header, newTD)

	if err := b.writeBatchAndUpdate(batchWriter, header, newTD, true); err != nil {
		return err
	}

	// Update the reference
	b.genesis = header.Hash

	// Create an event and send it to the stream
	event := &Event{}
	event.AddNewHeader(header)
	b.stream.push(event)

	return nil
}

// Empty checks if the blockchain is empty
func (b *Blockchain) Empty() bool {
	_, ok := b.db.ReadHeadHash()

	return !ok
}

// GetChainTD returns the latest difficulty
func (b *Blockchain) GetChainTD() (*big.Int, bool) {
	header := b.Header()

	return b.GetTD(header.Hash)
}

// GetTD returns the difficulty for the header hash
func (b *Blockchain) GetTD(hash types.Hash) (*big.Int, bool) {
	return b.readTotalDifficulty(hash)
}

// GetReceiptsByHash returns the receipts by their hash
func (b *Blockchain) GetReceiptsByHash(hash types.Hash) ([]*types.Receipt, error) {
	return b.db.ReadReceipts(hash)
}

// GetBodyByHash returns the body by their hash
func (b *Blockchain) GetBodyByHash(hash types.Hash) (*types.Body, bool) {
	return b.readBody(hash)
}

// GetHeaderByHash returns the header by his hash
func (b *Blockchain) GetHeaderByHash(hash types.Hash) (*types.Header, bool) {
	return b.readHeader(hash)
}

// readHeader Returns the header using the hash
func (b *Blockchain) readHeader(hash types.Hash) (*types.Header, bool) {
	// Try to find a hit in the headers cache
	h, ok := b.headersCache.Get(hash)
	if ok {
		// Hit, return the3 header
		header, ok := h.(*types.Header)
		if !ok {
			return nil, false
		}

		return header, true
	}

	// Cache miss, load it from the DB
	hh, err := b.db.ReadHeader(hash)
	if err != nil {
		return nil, false
	}

	// Compute the header hash and update the cache
	hh.ComputeHash()
	b.headersCache.Add(hash, hh)

	return hh, true
}

// readBody reads the block's body, using the block hash
func (b *Blockchain) readBody(hash types.Hash) (*types.Body, bool) {
	bb, err := b.db.ReadBody(hash)
	if err != nil {
		b.logger.Error("failed to read body", "err", err)

		return nil, false
	}

	// To return from field in the transactions of the past blocks
	if updated := b.recoverFromFieldsInTransactions(bb.Transactions); updated {
		batchWriter := storage.NewBatchWriter(b.db)

		batchWriter.PutBody(hash, bb)

		if err := batchWriter.WriteBatch(); err != nil {
			b.logger.Warn("failed to write body into storage", "hash", hash, "err", err)
		}
	}

	return bb, true
}

// readTotalDifficulty reads the total difficulty associated with the hash
func (b *Blockchain) readTotalDifficulty(headerHash types.Hash) (*big.Int, bool) {
	// Try to find the difficulty in the cache
	foundDifficulty, ok := b.difficultyCache.Get(headerHash)
	if ok {
		// Hit, return the difficulty
		fd, ok := foundDifficulty.(*big.Int)
		if !ok {
			return nil, false
		}

		return fd, true
	}

	// Miss, read the difficulty from the DB
	dbDifficulty, ok := b.db.ReadTotalDifficulty(headerHash)
	if !ok {
		return nil, false
	}

	// Update the difficulty cache
	b.difficultyCache.Add(headerHash, dbDifficulty)

	return dbDifficulty, true
}

// GetHeaderByNumber returns the header using the block number
func (b *Blockchain) GetHeaderByNumber(n uint64) (*types.Header, bool) {
	hash, ok := b.db.ReadCanonicalHash(n)
	if !ok {
		return nil, false
	}

	h, ok := b.readHeader(hash)
	if !ok {
		return nil, false
	}

	return h, true
}

// WriteHeadersWithBodies writes a batch of headers
func (b *Blockchain) WriteHeadersWithBodies(headers []*types.Header) error {
	// Check the size
	if len(headers) == 0 {
		return fmt.Errorf("passed in headers array is empty")
	}

	// Validate the chain
	for i := 1; i < len(headers); i++ {
		// Check the sequence
		if headers[i].Number-1 != headers[i-1].Number {
			return fmt.Errorf(
				"number sequence not correct at %d, %d and %d",
				i,
				headers[i].Number,
				headers[i-1].Number,
			)
		}

		// Check if the parent hashes match
		if headers[i].ParentHash != headers[i-1].Hash {
			return fmt.Errorf("parent hash not correct")
		}
	}

	// Write the actual headers in separate batches for now
	for _, header := range headers {
		event := &Event{}

		batchWriter := storage.NewBatchWriter(b.db)

		isCanonical, newTD, err := b.writeHeaderImpl(batchWriter, event, header)
		if err != nil {
			return err
		}

		if err := b.writeBatchAndUpdate(batchWriter, header, newTD, isCanonical); err != nil {
			return err
		}

		// Notify the event stream
		b.dispatchEvent(event)
	}

	return nil
}

// VerifyPotentialBlock does the minimal block verification without consulting the
// consensus layer. Should only be used if consensus checks are done
// outside the method call
func (b *Blockchain) VerifyPotentialBlock(block *types.Block) error {
	// Do just the initial block verification
	_, err := b.verifyBlock(block)

	return err
}

// VerifyFinalizedBlock verifies that the block is valid by performing a series of checks.
// It is assumed that the block status is sealed (committed)
func (b *Blockchain) VerifyFinalizedBlock(block *types.Block) (*types.FullBlock, error) {
	// Make sure the consensus layer verifies this block header
	if err := b.consensus.VerifyHeader(block.Header); err != nil {
		return nil, fmt.Errorf("failed to verify the header: %w", err)
	}

	// Do the initial block verification
	receipts, err := b.verifyBlock(block)
	if err != nil {
		return nil, err
	}

	return &types.FullBlock{Block: block, Receipts: receipts}, nil
}

// verifyBlock does the base (common) block verification steps by
// verifying the block body as well as the parent information
func (b *Blockchain) verifyBlock(block *types.Block) ([]*types.Receipt, error) {
	// Make sure the block is present
	if block == nil {
		return nil, ErrNoBlock
	}

	// Make sure the block is in line with the parent block
	if err := b.verifyBlockParent(block); err != nil {
		return nil, err
	}

	// Make sure the block body data is valid
	receipts, err := b.verifyBlockBody(block)
	if err != nil {
		return nil, err
	}

	return receipts, nil
}

// verifyBlockParent makes sure that the child block is in line
// with the locally saved parent block. This means checking:
// - The parent exists
// - The hashes match up
// - The block numbers match up
// - The block gas limit / used matches up
func (b *Blockchain) verifyBlockParent(childBlock *types.Block) error {
	// Grab the parent block
	parentHash := childBlock.ParentHash()
	parent, ok := b.readHeader(parentHash)

	if !ok {
		b.logger.Error(fmt.Sprintf(
			"parent of %s (%d) not found: %s",
			childBlock.Hash().String(),
			childBlock.Number(),
			parentHash,
		))

		return ErrParentNotFound
	}

	// Make sure the hash is valid
	if parent.Hash == types.ZeroHash {
		return ErrInvalidParentHash
	}

	// Make sure the hashes match up
	if parentHash != parent.Hash {
		return ErrParentHashMismatch
	}

	// Make sure the block numbers are correct
	if childBlock.Number()-1 != parent.Number {
		b.logger.Error(fmt.Sprintf(
			"number sequence not correct at %d and %d",
			childBlock.Number(),
			parent.Number,
		))

		return ErrInvalidBlockSequence
	}

	// Make sure the gas limit is within correct bounds
	if gasLimitErr := b.verifyGasLimit(childBlock.Header, parent); gasLimitErr != nil {
		return fmt.Errorf("invalid gas limit, %w", gasLimitErr)
	}

	return nil
}

// verifyBlockBody verifies that the block body is valid. This means checking:
// - The trie roots match up (state, transactions, receipts, uncles)
// - The receipts match up
// - The execution result matches up
func (b *Blockchain) verifyBlockBody(block *types.Block) ([]*types.Receipt, error) {
	// Make sure the Uncles root matches up
	if hash := buildroot.CalculateUncleRoot(block.Uncles); hash != block.Header.Sha3Uncles {
		b.logger.Error(fmt.Sprintf(
			"uncle root hash mismatch: have %s, want %s",
			hash,
			block.Header.Sha3Uncles,
		))

		return nil, ErrInvalidSha3Uncles
	}

	// Make sure the transactions root matches up
	if hash := buildroot.CalculateTransactionsRoot(block.Transactions, block.Number()); hash != block.Header.TxRoot {
		b.logger.Error(fmt.Sprintf(
			"incorrect tx root (expected: %s, actual: %s)",
			hash,
			block.Header.TxRoot,
		))

		return nil, ErrInvalidTxRoot
	}

	// Execute the transactions in the block and grab the result
	blockResult, executeErr := b.executeBlockTransactions(block)
	if executeErr != nil {
		return nil, fmt.Errorf("unable to execute block transactions, %w", executeErr)
	}

	// 🆕 奖励分配已在 executor.ProcessBlock 中处理，无需重复执行
	b.logger.Debug("ℹ️ 奖励分配已在 executor.ProcessBlock 中处理",
		"blockNumber", block.Number(),
		"说明", "奖励分配逻辑在 consensus/dpos/blockchain_wrapper.go 的 ProcessBlock 方法中执行")

	// Verify the local execution result with the proposed block data
	if err := blockResult.verifyBlockResult(block); err != nil {
		return nil, fmt.Errorf("unable to verify block execution result, %w", err)
	}

	return blockResult.Receipts, nil
}

// verifyBlockResult verifies that the block transaction execution result
// matches up to the expected values
func (br *BlockResult) verifyBlockResult(referenceBlock *types.Block) error {
	// Make sure the number of receipts matches the number of transactions
	if len(br.Receipts) != len(referenceBlock.Transactions) {
		return ErrInvalidReceiptsSize
	}

	// Make sure the world state root matches up
	if br.Root != referenceBlock.Header.StateRoot {
		// 🆕 详细的状态根对比日志
		fmt.Printf("🚨🚨🚨 ========== 状态根验证失败 ========== 🚨🚨🚨\n")
		fmt.Printf("🚨 区块号: %d\n", referenceBlock.Number())
		fmt.Printf("🚨 区块哈希: %s\n", referenceBlock.Hash().String())
		fmt.Printf("🚨 本地计算状态根: %s\n", br.Root.String())
		fmt.Printf("🚨 区块头状态根: %s\n", referenceBlock.Header.StateRoot.String())
		fmt.Printf("🚨 状态根是否一致: %t\n", br.Root == referenceBlock.Header.StateRoot)
		fmt.Printf("🚨 说明: 验证节点本地计算的状态根与区块头中的状态根不一致\n")
		fmt.Printf("🚨🚨🚨 ========================================== 🚨🚨🚨\n")
		return ErrInvalidStateRoot
	}

	// Make sure the gas used is valid
	if br.TotalGas != referenceBlock.Header.GasUsed {
		return ErrInvalidGasUsed
	}

	// Make sure the receipts root matches up
	receiptsRoot := buildroot.CalculateReceiptsRoot(br.Receipts)
	if receiptsRoot != referenceBlock.Header.ReceiptsRoot {
		// 🆕 详细的收据根对比日志
		fmt.Printf("🚨🚨🚨 ========== 收据根验证失败 ========== 🚨🚨🚨\n")
		fmt.Printf("🚨 区块号: %d\n", referenceBlock.Number())
		fmt.Printf("🚨 区块哈希: %s\n", referenceBlock.Hash().String())
		fmt.Printf("🚨 本地计算收据根: %s\n", receiptsRoot.String())
		fmt.Printf("🚨 区块头收据根: %s\n", referenceBlock.Header.ReceiptsRoot.String())
		fmt.Printf("🚨 收据数量: %d, 交易数量: %d\n", len(br.Receipts), len(referenceBlock.Transactions))
		if len(br.Receipts) > 0 {
			r0 := br.Receipts[0]
			statusVal := uint64(0)
			if r0.Status != nil {
				statusVal = uint64(*r0.Status)
			}
			fmt.Printf("🚨 首条收据简要: status=%d gasUsed=%d cumulative=%d logs=%d\n", statusVal, r0.GasUsed, r0.CumulativeGasUsed, len(r0.Logs))
		}
		fmt.Printf("🚨🚨🚨 ========================================== 🚨🚨🚨\n")
		return ErrInvalidReceiptsRoot
	}

	return nil
}

// executeBlockTransactions executes the transactions in the block locally,
// and reports back the block execution result
func (b *Blockchain) executeBlockTransactions(block *types.Block) (*BlockResult, error) {
	header := block.Header

	parent, ok := b.readHeader(header.ParentHash)
	if !ok {
		return nil, ErrParentNotFound
	}

	blockCreator, err := b.consensus.GetBlockCreator(header)
	if err != nil {
		return nil, err
	}

	txn, err := b.executor.ProcessBlock(parent.StateRoot, block, blockCreator)
	if err != nil {
		b.logger.Error("❌ executor.ProcessBlock调用失败", "blockNumber", block.Number(), "error", err)
		return nil, err
	}

	if err := b.consensus.PreCommitState(block, txn); err != nil {
		return nil, err
	}

	_, root, err := txn.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to commit the state changes: %w", err)
	}

	// Append the receipts to the receipts cache
	b.receiptsCache.Add(header.Hash, txn.Receipts())

	return &BlockResult{
		Root:     root,
		Receipts: txn.Receipts(),
		TotalGas: txn.TotalGas(),
	}, nil
}

// WriteFullBlock writes a single block to the local blockchain.
// It doesn't do any kind of verification, only commits the block to the DB
// This function is a copy of WriteBlock but with a full block which does not
// require to compute again the Receipts.
func (b *Blockchain) WriteFullBlock(fblock *types.FullBlock, source string) error {
	b.writeLock.Lock()
	defer b.writeLock.Unlock()

	block := fblock.Block
	header := block.Header

	if block.Number() <= b.Header().Number {
		b.logger.Info("block already inserted", "block", block.Number(), "source", source)
		return nil
	}

	// header 已在上面获取（第850行）
	batchWriter := storage.NewBatchWriter(b.db)

	if err := b.writeBody(batchWriter, block); err != nil {
		return err
	}

	// Write the header to the chain
	evnt := &Event{Source: source}

	isCanonical, newTD, err := b.writeHeaderImpl(batchWriter, evnt, header)
	if err != nil {
		return err
	}

	// write the receipts, do it only after the header has been written.
	// Otherwise, a client might ask for a header once the receipt is valid,
	// but before it is written into the storage
	batchWriter.PutReceipts(block.Hash(), fblock.Receipts)

	// Update the average gas price
	b.updateGasPriceAvgWithBlock(block)

	if err := b.writeBatchAndUpdate(batchWriter, header, newTD, isCanonical); err != nil {
		return err
	}

	// update snapshot
	if err := b.consensus.ProcessHeaders([]*types.Header{header}); err != nil {
		return err
	}

	b.dispatchEvent(evnt)

	// 🆕 调用共识的 OnBlockInserted 来清理交易池
	// 注意：
	// 1. 同步区块时（source="syncer"）：必须调用 OnBlockInserted 来清理交易池
	// 2. 本地生产区块时（source="consensus"）：虽然 consensusRuntime.OnBlockInserted 已经清理了交易池，
	//    但为了保持一致性，也调用 DPoS.OnBlockInserted（它内部会调用 txPool.ResetWithHeaders）
	//    DPoS.OnBlockInserted 内部会调用 consensusRuntime.OnBlockInserted，后者有重复处理保护机制
	//    （lastBuiltBlock.Number >= fullBlock.Block.Number()），所以不会重复处理
	b.logger.Debug("🔵 [blockchain.WriteFullBlock] 检查是否需要调用 OnBlockInserted",
		"blockNumber", header.Number,
		"source", source,
		"consensusType", fmt.Sprintf("%T", b.consensus),
		"consensusIsNil", b.consensus == nil)

	// 使用接口类型断言来避免循环依赖
	// 定义本地接口来避免导入 dpos 包
	type onBlockInsertedInterface interface {
		OnBlockInserted(fullBlock *types.FullBlock)
	}

	b.logger.Debug("🔵 [blockchain.WriteFullBlock] 开始类型断言，检查共识是否支持 OnBlockInserted",
		"blockNumber", header.Number,
		"source", source,
		"consensusType", fmt.Sprintf("%T", b.consensus))

	if blockInsertedHandler, ok := b.consensus.(onBlockInsertedInterface); ok && blockInsertedHandler != nil {
		b.logger.Debug("🔵 [blockchain.WriteFullBlock] 类型断言成功，调用共识 OnBlockInserted",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String()[:16],
			"source", source,
			"consensusType", fmt.Sprintf("%T", b.consensus))
		blockInsertedHandler.OnBlockInserted(fblock)
		b.logger.Debug("🔵 [blockchain.WriteFullBlock] OnBlockInserted 调用完成",
			"blockNumber", header.Number,
			"source", source)
	} else {
		// 如果共识不支持 OnBlockInserted 接口，可能是其他共识类型
		// 这些共识类型有自己的处理方式，这里不处理
		b.logger.Info("⚠️ [blockchain.WriteFullBlock] 共识不支持 OnBlockInserted 接口，跳过调用",
			"blockNumber", header.Number,
			"source", source,
			"consensusType", fmt.Sprintf("%T", b.consensus),
			"typeAssertOk", ok,
			"handlerIsNil", blockInsertedHandler == nil,
			"note", "这可能导致交易池nonce未更新")
	}

	// 🆕 判断区块类型：空块、交易块、mix块
	txCount := len(block.Transactions)
	stateTxCount := 0
	normalTxCount := 0

	for _, tx := range block.Transactions {
		// 判断是否为状态交易（系统调用交易）
		// 状态交易通常 From 是零地址（系统调用），或者 To 是系统合约地址
		// 简化判断：如果 From 是零地址，认为是状态交易
		if tx.From == (types.Address{}) {
			stateTxCount++
		} else {
			normalTxCount++
		}
	}

	// 确定区块类型
	blockType := "空块"
	if txCount > 0 {
		if stateTxCount > 0 && normalTxCount > 0 {
			blockType = "mix块"
		} else if stateTxCount > 0 {
			blockType = "状态块"
		} else {
			blockType = "交易块"
		}
	}

	logArgs := []interface{}{
		"number", header.Number,
		"txs", txCount,
		"hash", header.Hash,
		"parent", header.ParentHash,
		"source", source,
		"blockType", blockType, // 🆕 区块类型：空块、交易块、mix块、状态块
		"normalTxs", normalTxCount, // 🆕 普通交易数量
		"stateTxs", stateTxCount, // 🆕 状态交易数量
	}

	if prevHeader, ok := b.GetHeaderByNumber(header.Number - 1); ok {
		diff := header.Timestamp - prevHeader.Timestamp
		logArgs = append(logArgs, "generation_time_in_seconds", diff)
	}

	// 🆕 根据 source 区分本地生产和同步区块的日志消息
	logMessage := "💎 新区块写入"
	if source == "consensus" {
		// 🆕 本地生产区块：计算与上次生产的区块间隔
		b.lastProducedBlockMutex.Lock()
		lastBlockNumber := b.lastProducedBlockNumber
		lastBlockTime := b.lastProducedBlockTime
		currentBlockTime := time.Unix(int64(header.Timestamp), 0)

		// 计算区块间隔和时间间隔
		var blockInterval uint64 = 0
		var timeInterval time.Duration = 0
		if lastBlockNumber > 0 {
			if header.Number > lastBlockNumber {
				blockInterval = header.Number - lastBlockNumber
			}
			if !lastBlockTime.IsZero() {
				timeInterval = currentBlockTime.Sub(lastBlockTime)
			}
		}

		// 更新上次生产的区块信息
		b.lastProducedBlockNumber = header.Number
		b.lastProducedBlockTime = currentBlockTime
		b.lastProducedBlockMutex.Unlock()

		// 🆕 计算生产耗时（从开始构建到写入完成）
		var productionDuration time.Duration = 0
		b.lastBlockProductionStartMutex.Lock()
		startTime := b.lastBlockProductionStartTime
		if !startTime.IsZero() {
			productionDuration = time.Since(startTime)
			// 清空开始时间，避免下次误用
			b.lastBlockProductionStartTime = time.Time{}
		}
		b.lastBlockProductionStartMutex.Unlock()

		// 添加到日志参数
		logArgs = append(logArgs,
			"lastProducedBlockNumber", lastBlockNumber, // 🆕 上次生产的区块号
			"blockInterval", blockInterval, // 🆕 区块间隔（当前区块号 - 上次区块号）
			"timeInterval", timeInterval.String(), // 🆕 时间间隔
			"timeIntervalSeconds", timeInterval.Seconds(), // 🆕 时间间隔（秒）
			"productionDuration", productionDuration.String(), // 🆕 生产耗时（从开始构建到写入完成）
			"productionDurationMs", productionDuration.Milliseconds(), // 🆕 生产耗时（毫秒）
		)

		// 🆕 尝试获取验证者详情并添加到日志中
		// 定义接口来避免循环依赖
		type validatorDetailGetter interface {
			GetValidatorDetail(blockNumber uint64) map[string]interface{}
		}

		if detailGetter, ok := b.consensus.(validatorDetailGetter); ok {
			// ShouldProduceBlockNow 保存的是下一个区块号的详情（blockNumber + 1）
			// 所以直接查找当前区块号的详情即可
			detail := detailGetter.GetValidatorDetail(header.Number)
			if detail != nil {
				// 将验证者详情添加到日志参数中
				if currentSlot, ok := detail["currentSlot"].(int); ok {
					logArgs = append(logArgs, "currentSlot", currentSlot)
				}
				if activeValidatorCount, ok := detail["activeValidatorCount"].(int); ok {
					logArgs = append(logArgs, "activeValidatorCount", activeValidatorCount)
				}
				if myAddress, ok := detail["myAddress"].(string); ok {
					logArgs = append(logArgs, "myAddress", myAddress)
				}
				if expectedValidator, ok := detail["expectedValidator"].(string); ok {
					logArgs = append(logArgs, "expectedValidator", expectedValidator)
				}
				if isMatch, ok := detail["isMatch"].(bool); ok {
					logArgs = append(logArgs, "isMatch", isMatch)
				}
				if genesisTime, ok := detail["genesisTime"].(string); ok {
					logArgs = append(logArgs, "genesisTime", genesisTime)
				}
				if now, ok := detail["now"].(string); ok {
					logArgs = append(logArgs, "now", now)
				}
				if timeSinceGenesis, ok := detail["timeSinceGenesis"].(string); ok {
					logArgs = append(logArgs, "timeSinceGenesis", timeSinceGenesis)
				}
				if blockWindow, ok := detail["blockWindow"].(string); ok {
					logArgs = append(logArgs, "blockWindow", blockWindow)
				}
				if validatorsList, ok := detail["validatorsList"].([]string); ok {
					logArgs = append(logArgs, "validatorsList", validatorsList)
				}
			} else {
				// 接口断言成功，但未找到验证者详情
				b.logger.Info("⚠️ 接口断言成功但未找到验证者详情",
					"blockNumber", header.Number,
					"source", source)
			}
		} else {
			// 接口类型断言失败
			b.logger.Info("⚠️ consensus不支持GetValidatorDetail接口，无法添加验证者详情字段",
				"blockNumber", header.Number,
				"consensusType", fmt.Sprintf("%T", b.consensus),
				"source", source)
		}

		// 🆕 根据是否有交易使用不同的表情符号
		if txCount > 0 {
			// 有交易：使用 🏭 表情符号
			logMessage = "🏭 🏭 🏭 🏭 🏭 🏭 🏭  本地生产新区块写入（有交易）"
		} else {
			// 无交易（空块）：使用 ⚪ 表情符号
			logMessage = "⚪⚪⚪⚪⚪⚪⚪⚪⚪⚪ 本地生产新区块写入（空块）"
		}
	} else if source == "syncer" {
		logMessage = "📥📥📥📥📥📥📥📥📥📥 同步新区块写入"
	}

	b.logger.Info(logMessage, logArgs...)

	// 🆕 检查写入的区块状态根
	b.logger.Debug("🔍 区块写入完成状态根检查",
		"blockNumber", header.Number,
		"stateRoot", header.StateRoot.String(),
		"blockHash", header.Hash.String(),
		"source", source)

	return nil
}

// WriteBlock writes a single block to the local blockchain.
// It doesn't do any kind of verification, only commits the block to the DB
func (b *Blockchain) WriteBlock(block *types.Block, source string) error {
	b.writeLock.Lock()
	defer b.writeLock.Unlock()

	if block.Number() <= b.Header().Number {
		b.logger.Info("block already inserted", "block", block.Number(), "source", source)

		return nil
	}

	header := block.Header

	batchWriter := storage.NewBatchWriter(b.db)

	if err := b.writeBody(batchWriter, block); err != nil {
		return err
	}

	// Write the header to the chain
	evnt := &Event{Source: source}

	isCanonical, newTD, err := b.writeHeaderImpl(batchWriter, evnt, header)
	if err != nil {
		return err
	}

	// Fetch the block receipts
	blockReceipts, receiptsErr := b.extractBlockReceipts(block)
	if receiptsErr != nil {
		return receiptsErr
	}

	// write the receipts, do it only after the header has been written.
	// Otherwise, a client might ask for a header once the receipt is valid,
	// but before it is written into the storage
	batchWriter.PutReceipts(block.Hash(), blockReceipts)

	// update snapshot
	if err := b.consensus.ProcessHeaders([]*types.Header{header}); err != nil {
		return err
	}

	// Update the average gas price
	b.updateGasPriceAvgWithBlock(block)

	if err := b.writeBatchAndUpdate(batchWriter, header, newTD, isCanonical); err != nil {
		return err
	}

	b.dispatchEvent(evnt)

	logArgs := []interface{}{
		"number", header.Number,
		"txs", len(block.Transactions),
		"hash", header.Hash,
		"parent", header.ParentHash,
		"source", source,
	}

	if prevHeader, ok := b.GetHeaderByNumber(header.Number - 1); ok {
		diff := header.Timestamp - prevHeader.Timestamp
		logArgs = append(logArgs, "generation_time_in_seconds", diff)
	}

	b.logger.Info("💎 新区块写入", logArgs...)

	return nil
}

// GetCachedReceipts retrieves cached receipts for given headerHash
func (b *Blockchain) GetCachedReceipts(headerHash types.Hash) ([]*types.Receipt, error) {
	receipts, found := b.receiptsCache.Get(headerHash)
	if !found {
		return nil, fmt.Errorf("failed to retrieve receipts for header hash: %s", headerHash)
	}

	extractedReceipts, ok := receipts.([]*types.Receipt)
	if !ok {
		return nil, errors.New("invalid type assertion for receipts")
	}

	return extractedReceipts, nil
}

// WriteBlockWithoutConsensus writes a block without consensus verification
// This is used for DPoS blocks that need to bypass IBFT validation
func (b *Blockchain) WriteBlockWithoutConsensus(block *types.Block, source string) error {
	b.writeLock.Lock()
	defer b.writeLock.Unlock()

	if block.Number() <= b.Header().Number {
		b.logger.Info("block already inserted", "block", block.Number(), "source", source)
		return nil
	}

	header := block.Header
	batchWriter := storage.NewBatchWriter(b.db)

	if err := b.writeBody(batchWriter, block); err != nil {
		return err
	}

	// Write the header to the chain without consensus verification
	evnt := &Event{Source: source}
	isCanonical, newTD, err := b.writeHeaderImpl(batchWriter, evnt, header)
	if err != nil {
		return err
	}

	// Write empty receipts for DPoS blocks
	batchWriter.PutReceipts(block.Hash(), []*types.Receipt{})

	// Update the average gas price
	b.updateGasPriceAvgWithBlock(block)

	if err := b.writeBatchAndUpdate(batchWriter, header, newTD, isCanonical); err != nil {
		return err
	}

	// update snapshot
	if err := b.consensus.ProcessHeaders([]*types.Header{header}); err != nil {
		return err
	}

	b.dispatchEvent(evnt)

	logArgs := []interface{}{
		"number", header.Number,
		"txs", len(block.Transactions),
		"hash", header.Hash,
		"parent", header.ParentHash,
		"source", source,
	}

	if prevHeader, ok := b.GetHeaderByNumber(header.Number - 1); ok {
		diff := header.Timestamp - prevHeader.Timestamp
		logArgs = append(logArgs, "generation_time_in_seconds", diff)
	}

	b.logger.Info("💎 新区块写入(跳过共识验证)", logArgs...)

	return nil
}

// extractBlockReceipts extracts the receipts from the passed in block
func (b *Blockchain) extractBlockReceipts(block *types.Block) ([]*types.Receipt, error) {
	// Check the cache for the block receipts
	receipts, ok := b.receiptsCache.Get(block.Header.Hash)
	if !ok {
		// No receipts found in the cache, execute the transactions from the block
		// and fetch them
		blockResult, err := b.executeBlockTransactions(block)
		if err != nil {
			return nil, err
		}

		return blockResult.Receipts, nil
	}

	extractedReceipts, ok := receipts.([]*types.Receipt)
	if !ok {
		return nil, errors.New("invalid type assertion for receipts")
	}

	return extractedReceipts, nil
}

// updateGasPriceAvgWithBlock extracts the gas price information from the
// block, and updates the average gas price for the chain accordingly
func (b *Blockchain) updateGasPriceAvgWithBlock(block *types.Block) {
	if len(block.Transactions) < 1 {
		// 空块时，如果启用了 London fork，使用 BaseFee 作为参考价格
		if b.config.Params.Forks.IsActive(chain.London, block.Number()) {
			// 使用 BaseFee 作为新的价格样本，促进下降
			baseFee := block.Header.BaseFee
			if baseFee > 0 {
				gasPrices := []*big.Int{new(big.Int).SetUint64(baseFee)}
				b.updateGasPriceAvg(gasPrices)
			}
		}
		// 如果没有启用 London fork，或者 BaseFee 为 0，则不更新（保持原逻辑）
		return
	}

	gasPrices := make([]*big.Int, len(block.Transactions))
	for i, transaction := range block.Transactions {
		gasPrices[i] = transaction.GetGasPrice(block.Header.BaseFee)
	}

	b.updateGasPriceAvg(gasPrices)
}

// writeBody writes the block body to the DB.
// Additionally, it also updates the txn lookup, for txnHash -> block lookups
func (b *Blockchain) writeBody(batchWriter *storage.BatchWriter, block *types.Block) error {
	// Recover 'from' field in tx before saving
	// Because the block passed from the consensus layer doesn't have from field in tx,
	// due to missing encoding in RLP
	if err := b.recoverFromFieldsInBlock(block); err != nil {
		return err
	}

	// Write the full body (txns + receipts)
	batchWriter.PutBody(block.Header.Hash, block.Body())

	// Write txn lookups (txHash -> block)
	for _, txn := range block.Transactions {
		batchWriter.PutTxLookup(txn.Hash, block.Hash())
	}

	return nil
}

// ReadTxLookup returns the block hash using the transaction hash
func (b *Blockchain) ReadTxLookup(hash types.Hash) (types.Hash, bool) {
	v, ok := b.db.ReadTxLookup(hash)

	return v, ok
}

// recoverFromFieldsInBlock recovers 'from' fields in the transactions of the given block
// return error if the invalid signature found
func (b *Blockchain) recoverFromFieldsInBlock(block *types.Block) error {
	for _, tx := range block.Transactions {
		if tx.From != types.ZeroAddress || tx.Type == types.StateTx {
			continue
		}

		sender, err := b.txSigner.Sender(tx)
		if err != nil {
			return err
		}

		tx.From = sender
	}

	return nil
}

// recoverFromFieldsInTransactions recovers 'from' fields in the transactions
// log as warning if failing to recover one address
func (b *Blockchain) recoverFromFieldsInTransactions(transactions []*types.Transaction) bool {
	updated := false

	for _, tx := range transactions {
		if tx.From != types.ZeroAddress || tx.Type == types.StateTx {
			continue
		}

		sender, err := b.txSigner.Sender(tx)
		if err != nil {
			b.logger.Warn("failed to recover from address in Tx", "hash", tx.Hash, "err", err)

			continue
		}

		tx.From = sender
		updated = true
	}

	return updated
}

// verifyGasLimit is a helper function for validating a gas limit in a header
func (b *Blockchain) verifyGasLimit(header *types.Header, parentHeader *types.Header) error {
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf(
			"block gas used exceeds gas limit, limit = %d, used=%d",
			header.GasLimit,
			header.GasUsed,
		)
	}

	// Skip block limit difference check for genesis
	if header.Number == 0 {
		return nil
	}

	// Find the absolute delta between the limits
	diff := int64(parentHeader.GasLimit) - int64(header.GasLimit)
	if diff < 0 {
		diff *= -1
	}

	limit := parentHeader.GasLimit / blockGasTargetDivisor
	if uint64(diff) > limit {
		return fmt.Errorf(
			"invalid gas limit, limit = %d, want %d +- %d",
			header.GasLimit,
			parentHeader.GasLimit,
			limit-1,
		)
	}

	return nil
}

// GetHashHelper is used by the EVM, so that the SC can get the hash of the header number
func (b *Blockchain) GetHashHelper(header *types.Header) func(i uint64) (res types.Hash) {
	return func(i uint64) (res types.Hash) {
		num, hash := header.Number-1, header.ParentHash

		for {
			if num == i {
				res = hash

				return
			}

			h, ok := b.GetHeaderByHash(hash)
			if !ok {
				return
			}

			hash = h.ParentHash

			if num == 0 {
				return
			}

			num--
		}
	}
}

// GetHashByNumber returns the block hash using the block number
func (b *Blockchain) GetHashByNumber(blockNumber uint64) types.Hash {
	block, ok := b.GetBlockByNumber(blockNumber, false)
	if !ok {
		return types.Hash{}
	}

	return block.Hash()
}

// dispatchEvent pushes a new event to the stream
func (b *Blockchain) dispatchEvent(evnt *Event) {
	// 添加事件分发日志跟踪
	if evnt != nil && len(evnt.NewChain) > 0 {
		latest := evnt.NewChain[len(evnt.NewChain)-1]
		b.logger.Debug("🔔 区块链事件分发", "区块高度", latest.Number, "区块哈希", latest.Hash.String(), "来源", evnt.Source, "NewChain长度", len(evnt.NewChain))
	}
	b.stream.push(evnt)
}

// writeHeaderImpl writes a block and the data, assumes the genesis is already set
// Returning parameters (is canonical header, new total difficulty, error)
func (b *Blockchain) writeHeaderImpl(
	batchWriter *storage.BatchWriter, evnt *Event, header *types.Header) (bool, *big.Int, error) {
	// parent total difficulty of incoming header
	parentTD, ok := b.readTotalDifficulty(header.ParentHash)
	if !ok {
		return false, nil, fmt.Errorf(
			"parent of %s (%d) not found",
			header.Hash.String(),
			header.Number,
		)
	}

	currentHeader := b.Header()
	incomingTD := new(big.Int).Add(parentTD, new(big.Int).SetUint64(header.Difficulty))

	// if parent of new header is current header just put everything in batch and update event
	// new header will be canonical one
	if header.ParentHash == currentHeader.Hash {
		batchWriter.PutCanonicalHeader(header, incomingTD)

		evnt.Type = EventHead
		evnt.AddNewHeader(header)
		evnt.SetDifficulty(incomingTD)

		return true, incomingTD, nil
	}

	currentTD, ok := b.readTotalDifficulty(currentHeader.Hash)
	if !ok {
		return false, nil, errors.New("failed to get header difficulty")
	}

	if incomingTD.Cmp(currentTD) > 0 {
		// new block has higher difficulty, reorg the chain
		if err := b.handleReorg(batchWriter, evnt, currentHeader, header, incomingTD); err != nil {
			return false, nil, err
		}

		batchWriter.PutCanonicalHeader(header, incomingTD)

		return true, incomingTD, nil
	}

	forks, err := b.getForksToWrite(header)
	if err != nil {
		return false, nil, err
	}

	batchWriter.PutHeader(header)
	batchWriter.PutTotalDifficulty(header.Hash, incomingTD)
	batchWriter.PutForks(forks)

	// new block has lower difficulty, create a new fork
	evnt.AddOldHeader(header)
	evnt.Type = EventFork

	return false, nil, nil
}

// getForksToWrite retrieves new header forks that should be written to the DB
func (b *Blockchain) getForksToWrite(header *types.Header) ([]types.Hash, error) {
	forks, err := b.db.ReadForks()
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			forks = []types.Hash{}
		} else {
			return nil, err
		}
	}

	newForks := []types.Hash{}

	for _, fork := range forks {
		if fork != header.ParentHash {
			newForks = append(newForks, fork)
		}
	}

	return append(newForks, header.Hash), nil
}

// handleReorg handles a reorganization event
func (b *Blockchain) handleReorg(
	batchWriter *storage.BatchWriter,
	evnt *Event,
	oldHeader *types.Header,
	newHeader *types.Header,
	newTD *big.Int,
) error {
	newChainHead := newHeader
	oldChainHead := oldHeader

	oldChain := []*types.Header{}
	newChain := []*types.Header{}

	var ok bool

	// Fill up the old headers array
	for oldHeader.Number > newHeader.Number {
		oldHeader, ok = b.readHeader(oldHeader.ParentHash)
		if !ok {
			return fmt.Errorf("header '%s' not found", oldHeader.ParentHash.String())
		}

		oldChain = append(oldChain, oldHeader)
	}

	// Fill up the new headers array
	for newHeader.Number > oldHeader.Number {
		newHeader, ok = b.readHeader(newHeader.ParentHash)
		if !ok {
			return fmt.Errorf("header '%s' not found", newHeader.ParentHash.String())
		}

		newChain = append(newChain, newHeader)
	}

	for oldHeader.Hash != newHeader.Hash {
		oldHeader, ok = b.readHeader(oldHeader.ParentHash)
		if !ok {
			return fmt.Errorf("header '%s' not found", oldHeader.ParentHash.String())
		}

		newHeader, ok = b.readHeader(newHeader.ParentHash)
		if !ok {
			return fmt.Errorf("header '%s' not found", newHeader.ParentHash.String())
		}

		oldChain = append(oldChain, oldHeader)
	}

	forks, err := b.getForksToWrite(oldChainHead)
	if err != nil {
		return fmt.Errorf("failed to write the old header as fork: %w", err)
	}

	batchWriter.PutForks(forks)

	// Update canonical chain numbers
	for _, h := range newChain {
		batchWriter.PutCanonicalHash(h.Number, h.Hash)
	}

	for _, b := range oldChain[:len(oldChain)-1] {
		evnt.AddOldHeader(b)
	}

	evnt.AddOldHeader(oldChainHead)
	evnt.AddNewHeader(newChainHead)

	for _, b := range newChain {
		evnt.AddNewHeader(b)
	}

	// Set the event type and difficulty
	evnt.Type = EventReorg
	evnt.SetDifficulty(newTD)

	return nil
}

// GetForks returns the forks
func (b *Blockchain) GetForks() ([]types.Hash, error) {
	return b.db.ReadForks()
}

// GetBlockByHash returns the block using the block hash
func (b *Blockchain) GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool) {
	header, ok := b.readHeader(hash)
	if !ok {
		return nil, false
	}

	block := &types.Block{
		Header: header,
	}

	if !full || header.Number == 0 {
		return block, true
	}

	// Load the entire block body
	body, ok := b.readBody(hash)
	if !ok {
		return block, false
	}

	// Set the transactions and uncles
	block.Transactions = body.Transactions
	block.Uncles = body.Uncles

	return block, true
}

// GetBlockByNumber returns the block using the block number
func (b *Blockchain) GetBlockByNumber(blockNumber uint64, full bool) (*types.Block, bool) {
	blockHash, ok := b.db.ReadCanonicalHash(blockNumber)
	if !ok {
		return nil, false
	}

	// if blockNumber 0 (genesis block), do not try and get the full block
	if blockNumber == uint64(0) {
		full = false
	}

	return b.GetBlockByHash(blockHash, full)
}

// Close closes the DB connection
func (b *Blockchain) Close() error {
	return b.db.Close()
}

// CalculateBaseFee calculates the basefee of the header.
func (b *Blockchain) CalculateBaseFee(parent *types.Header) uint64 {
	// Return zero base fee is a london hardfork is not enabled
	if !b.config.Params.Forks.IsActive(chain.London, parent.Number) {
		return 0
	}

	// Check if this is the first London hardfork block.
	// Should return chain.GenesisBaseFee ins this case.
	if parent.BaseFee == 0 {
		if b.config.Genesis.BaseFee > 0 {
			return b.config.Genesis.BaseFee
		}

		return chain.GenesisBaseFee
	}

	parentGasTarget := parent.GasLimit / b.config.Genesis.BaseFeeEM

	// If the parent gasUsed is the same as the target, the baseFee remains unchanged.
	if parent.GasUsed == parentGasTarget {
		return parent.BaseFee
	}

	// If the parent block used more gas than its target, the baseFee should increase.
	if parent.GasUsed > parentGasTarget {
		gasUsedDelta := parent.GasUsed - parentGasTarget
		baseFeeDelta := b.calcBaseFeeDelta(gasUsedDelta, parentGasTarget, parent.BaseFee)

		return parent.BaseFee + common.Max(baseFeeDelta, 1)
	}

	// Otherwise, if the parent block used less gas than its target, the baseFee should decrease.
	gasUsedDelta := parentGasTarget - parent.GasUsed
	baseFeeDelta := b.calcBaseFeeDelta(gasUsedDelta, parentGasTarget, parent.BaseFee)

	return common.Max(parent.BaseFee-baseFeeDelta, 0)
}

func (b *Blockchain) calcBaseFeeDelta(gasUsedDelta, parentGasTarget, baseFee uint64) uint64 {
	y := baseFee * gasUsedDelta / parentGasTarget

	return y / b.config.Genesis.BaseFeeChangeDenom
}

func (b *Blockchain) writeBatchAndUpdate(
	batchWriter *storage.BatchWriter,
	header *types.Header,
	newTD *big.Int,
	isCanonnical bool) error {
	if err := batchWriter.WriteBatch(); err != nil {
		return err
	}

	if isCanonnical {
		b.headersCache.Add(header.Hash, header)
		b.setCurrentHeader(header, newTD) // Update the blockchain reference
	}

	return nil
}
