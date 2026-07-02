package txpool

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armon/go-metrics"
	"github.com/golang/protobuf/ptypes/any"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/grpc"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/forkmanager"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/txpool/proto"
	"github.com/Vcity-Team/vcitychain/types"
)

const (
	txSlotSize  = 32 * 1024  // 32kB
	txMaxSize   = 128 * 1024 // 128Kb
	topicNameV1 = "txpool/0.1"

	// maximum allowed number of times an account
	// was excluded from block building (ibft.writeTransactions)
	maxAccountDemotions uint64 = 10

	// maximum allowed number of consecutive blocks that don't have the account's transaction
	maxAccountSkips = uint64(10)

	pruningCooldown = 5000 * time.Millisecond

	// txPoolMetrics is a prefix used for txpool-related metrics
	txPoolMetrics = "txpool"
)

// errors
var (
	ErrIntrinsicGas            = errors.New("intrinsic gas too low")
	ErrBlockLimitExceeded      = errors.New("exceeds block gas limit")
	ErrNegativeValue           = errors.New("negative value")
	ErrExtractSignature        = errors.New("cannot extract signature")
	ErrInvalidSender           = errors.New("invalid sender")
	ErrTxPoolOverflow          = errors.New("txpool is full")
	ErrUnderpriced             = errors.New("transaction underpriced")
	ErrNonceTooLow             = errors.New("nonce too low")
	ErrInsufficientFunds       = errors.New("insufficient funds for gas * price + value")
	ErrInvalidAccountState     = errors.New("invalid account state")
	ErrAlreadyKnown            = errors.New("already known")
	ErrOversizedData           = errors.New("oversized data")
	ErrMaxEnqueuedLimitReached = errors.New("maximum number of enqueued transactions reached")
	ErrRejectFutureTx          = errors.New("rejected future tx due to low slots")
	ErrInvalidTxType           = errors.New("invalid tx type")
	ErrTxTypeNotSupported      = types.ErrTxTypeNotSupported
	ErrTipAboveFeeCap          = errors.New("max priority fee per gas higher than max fee per gas")
	ErrTipVeryHigh             = errors.New("max priority fee per gas higher than 2^256-1")
	ErrFeeCapVeryHigh          = errors.New("max fee per gas higher than 2^256-1")
	ErrNonceExistsInPool       = errors.New("tx with the same nonce is already present")
	ErrReplacementUnderpriced  = errors.New("replacement tx underpriced")
	ErrDynamicTxNotAllowed     = errors.New("dynamic tx not allowed currently")
)

// indicates origin of a transaction
type txOrigin int

const (
	local  txOrigin = iota // json-RPC/gRPC endpoints
	gossip                 // gossip protocol
	reorg                  // re-added after chain reorganization
)

func (o txOrigin) String() (s string) {
	switch o {
	case local:
		s = "local"
	case gossip:
		s = "gossip"
	case reorg:
		s = "reorg"
	}

	return
}

// store interface defines State helper methods the TxPool should have access to
type store interface {
	Header() *types.Header
	GetNonce(root types.Hash, addr types.Address) uint64
	GetBalance(root types.Hash, addr types.Address) (*big.Int, error)
	GetBlockByHash(types.Hash, bool) (*types.Block, bool)
	CalculateBaseFee(parent *types.Header) uint64
}

// lockedBalanceProvider optionally provides the amount of balance that is locked
// (not spendable) for the given address at the current chain head.
//
// If implemented by the store, TxPool will enforce:
// spendable = balance - locked
// and require spendable >= tx.Cost().
type lockedBalanceProvider interface {
	GetLockedBalance(root types.Hash, addr types.Address) (*big.Int, error)
}

type signer interface {
	Sender(tx *types.Transaction) (types.Address, error)
}

type Config struct {
	PriceLimit         uint64
	MaxSlots           uint64
	MaxAccountEnqueued uint64
	ChainID            *big.Int
}

/* All requests are passed to the main loop
through their designated channels. */

// An enqueueRequest is created for any transaction
// meant to be enqueued onto some account.
// This request is created for (new) transactions
// that passed validation in addTx.
type enqueueRequest struct {
	tx *types.Transaction
}

// A promoteRequest is created each time some account
// is eligible for promotion. This request is signaled
// on 2 occasions:
//
// 1. When an enqueued transaction's nonce is
// not greater than the expected (account's nextNonce).
// == nextNonce - transaction is expected (addTx)
// < nextNonce - transaction was demoted (Demote)
//
// 2. When an account's nextNonce is updated (during ResetWithHeader)
// and the first enqueued transaction matches the new nonce.
type promoteRequest struct {
	account types.Address
}

// addTxRequest represents a request to add a transaction asynchronously
// Performance optimization: Event-driven architecture (Geth-style)
type addTxRequest struct {
	tx     *types.Transaction
	origin txOrigin
	errCh  chan error // Channel to return error (nil if success)
}

// TxPool is a module that handles pending transactions.
// All transactions are handled within their respective accounts.
// An account contains 2 queues a transaction needs to go through:
// - 1. Enqueued (entry point)
// - 2. Promoted (exit point)
// (both queues are min nonce ordered)
//
// When consensus needs to process promoted transactions,
// the pool generates a queue of "executable" transactions. These
// transactions are the first-in-line of some promoted queue,
// ready to be written to the state (primaries).
type TxPool struct {
	logger hclog.Logger
	signer signer
	forks  *chain.Forks
	store  store

	// map of all accounts registered by the pool
	accounts accountsMap

	// all the primaries sorted by max gas price
	executables   *pricedQueue
	executablesMu sync.RWMutex // Protects executables queue from concurrent access

	// lookup map keeping track of all
	// transactions present in the pool
	index lookupMap

	// networking stack
	topic *network.Topic

	// gauge for measuring pool capacity
	gauge slotGauge

	// priceLimit is a lower threshold for gas price
	priceLimit uint64

	// channels on which the pool's event loop
	// does dispatching/handling requests.
	promoteReqCh chan promoteRequest
	pruneCh      chan struct{}

	// Performance optimization: Event-driven architecture (Geth-style)
	// Main path is lock-free, all operations go through channels
	addTxCh chan *addTxRequest // Channel for async transaction addition

	// shutdown channel
	shutdownCh chan struct{}

	// flag indicating if the current node is a sealer,
	// and should therefore gossip transactions
	sealing atomic.Bool

	// baseFee is the base fee of the current head.
	// This is needed to sort transactions by price
	baseFee uint64

	// Event manager for txpool events
	eventManager *eventManager

	// indicates which txpool operator commands should be implemented
	proto.UnimplementedTxnPoolOperatorServer

	// pending is the list of pending and ready transactions. This variable
	// is accessed with atomics
	pending int64

	// chain id
	chainID *big.Int

	// maxAccountEnqueued is the maximum number of enqueued transactions per account
	maxAccountEnqueued uint64

	// Performance optimization: Caches for validated transactions and balances
	validatedCache *validatedTxCache // Cache for validated transaction signatures
	balanceCache   *balanceCache     // Cache for account balances (with TTL)

	// Performance optimization: Cache for chain nonces in Prepare() to reduce state queries
	// Key: account address, Value: chain nonce (from parent block state)
	// This cache is cleared when a new block is mined (in processEvent)
	prepareNonceCache   map[types.Address]uint64
	prepareNonceCacheMu sync.RWMutex // RWMutex for concurrent access to prepareNonceCache
}

// NewTxPool returns a new pool for processing incoming transactions.
func NewTxPool(
	logger hclog.Logger,
	forks *chain.Forks,
	store store,
	grpcServer *grpc.Server,
	network *network.Server,
	config *Config,
) (*TxPool, error) {
	pool := &TxPool{
		logger:      logger.Named("txpool"),
		forks:       forks,
		store:       store,
		executables: newPricesQueue(0, nil),
		accounts: accountsMap{
			maxEnqueuedLimit: config.MaxAccountEnqueued,
			// Performance optimization: accountLastAccess is now sync.Map (no initialization needed)
			maxAccountCount: 10000, // 最大10000个账户
		},
		index:              lookupMap{all: make(map[types.Hash]*types.Transaction)},
		gauge:              slotGauge{height: 0, max: config.MaxSlots},
		priceLimit:         config.PriceLimit,
		chainID:            config.ChainID,
		maxAccountEnqueued: config.MaxAccountEnqueued, // 保存配置值，用于RPC查询

		// Performance optimization: Initialize caches
		validatedCache:    newValidatedTxCache(10000),       // Cache up to 10000 validated transactions
		balanceCache:      newBalanceCache(2 * time.Second), // Balance cache with 2s TTL
		prepareNonceCache: make(map[types.Address]uint64),   // Cache for chain nonces in Prepare()

		//	main loop channels
		promoteReqCh: make(chan promoteRequest),
		pruneCh:      make(chan struct{}),
		shutdownCh:   make(chan struct{}),

		// Performance optimization: Event-driven architecture (Geth-style)
		// Buffer size: 1000 transactions (non-blocking for most cases)
		addTxCh: make(chan *addTxRequest, 1000),
	}

	// Attach the event manager
	pool.eventManager = newEventManager(pool.logger)

	if network != nil {
		// subscribe to the gossip protocol
		topic, err := network.NewTopic(topicNameV1, &proto.Txn{})
		if err != nil {
			return nil, err
		}

		if subscribeErr := topic.Subscribe(pool.addGossipTx); subscribeErr != nil {
			return nil, fmt.Errorf("unable to subscribe to gossip topic, %w", subscribeErr)
		}

		pool.topic = topic
	}

	if grpcServer != nil {
		proto.RegisterTxnPoolOperatorServer(grpcServer, pool)
	}

	return pool, nil
}

func (p *TxPool) updatePending(i int64) {
	newPending := atomic.AddInt64(&p.pending, i)
	metrics.SetGauge([]string{txPoolMetrics, "pending_transactions"}, float32(newPending))
}

// Start runs the pool's main loop in the background.
// On each request received, the appropriate handler
// is invoked in a separate goroutine.
func (p *TxPool) Start() {
	// set default value of txpool pending transactions gauge
	p.updatePending(0)

	//	run the handler for high gauge level pruning
	go func() {
		for {
			select {
			case <-p.shutdownCh:
				return
			case <-p.pruneCh:
				p.pruneAccountsWithNonceHoles()
			}

			//	handler is in cooldown to avoid successive calls
			//	which could be just no-ops
			time.Sleep(pruningCooldown)
		}
	}()

	// Performance optimization: Event-driven architecture (Geth-style)
	// Main event loop: serial processing of all transactions (lock-free main path)
	go p.eventLoop()

	//	run the handler for the tx pipeline (legacy promotion handler)
	go func() {
		for {
			select {
			case <-p.shutdownCh:
				return
			case req := <-p.promoteReqCh:
				go p.handlePromoteRequest(req)
			}
		}
	}()

	//	run the account cleanup handler
	go func() {
		ticker := time.NewTicker(5 * time.Minute) // 每5分钟清理一次
		defer ticker.Stop()

		for {
			select {
			case <-p.shutdownCh:
				return
			case <-ticker.C:
				p.cleanupAccounts()
			}
		}
	}()
}

// eventLoop is the main event loop for processing transactions
// Performance optimization: Event-driven architecture (Geth-style)
// Serial processing eliminates lock contention on the main path
func (p *TxPool) eventLoop() {
	for {
		select {
		case <-p.shutdownCh:
			return
		case req := <-p.addTxCh:
			// Process transaction serially (no lock needed, single goroutine)
			err := p.processTx(req.tx, req.origin)
			// Send result back (non-blocking)
			select {
			case req.errCh <- err:
			default:
				// Error channel is full, skip (shouldn't happen with buffered channel)
			}
		}
	}
}

// processTx processes a transaction serially (called only from eventLoop)
// Performance optimization: No lock needed because only one goroutine processes transactions
func (p *TxPool) processTx(tx *types.Transaction, origin txOrigin) error {
	if p.logger.IsDebug() {
		p.logger.Debug("processTx", "origin", origin.String(), "hash", tx.Hash.String())
	}

	// Full validation (now we can do state queries safely)
	if err := p.validateTx(tx); err != nil {
		p.logger.Error("交易验证失败", "err", err, "txHash", tx.Hash.String())
		return err
	}

	// add chainID to the tx - only dynamic fee tx
	if tx.Type == types.DynamicFeeTx {
		tx.ChainID = p.chainID
	}

	// Performance optimization: Skip hash calculation if already computed
	if tx.Hash == (types.Hash{}) {
		tx.ComputeHash(p.store.Header().Number)
	}

	// 🚨 检测哈希计算后的结果
	if tx.Hash == (types.Hash{}) {
		p.logger.Error("🚨 CRITICAL: transaction hash is zero after ComputeHash",
			"origin", origin.String(),
			"txType", tx.Type,
			"nonce", tx.Nonce)
		return fmt.Errorf("zero hash after ComputeHash")
	}

	// initialize account for this address once or retrieve existing one
	account := p.getOrCreateAccount(tx.From)

	// Lock is still needed here because getOrCreateAccount might create new account
	// But lock contention is much lower because only one goroutine processes transactions
	account.mu.Lock()
	defer account.mu.Unlock()

	accountNonce := account.getNonce()

	//	only accept transactions with expected nonce
	if p.gauge.highPressure() {
		p.signalPruning()

		if tx.Nonce > accountNonce {
			metrics.IncrCounter([]string{txPoolMetrics, "rejected_future_tx"}, 1)
			return ErrRejectFutureTx
		}
	}

	// try to find if there is transaction with same nonce for this account
	oldTxWithSameNonce := account.nonceToTx.get(tx.Nonce)
	if oldTxWithSameNonce != nil {
		if oldTxWithSameNonce.Hash == tx.Hash {
			metrics.IncrCounter([]string{txPoolMetrics, "already_known_tx"}, 1)
			return ErrAlreadyKnown
		} else if oldTxWithSameNonce.GetGasPrice(p.baseFee).Cmp(
			tx.GetGasPrice(p.baseFee)) >= 0 {
			metrics.IncrCounter([]string{txPoolMetrics, "underpriced_tx"}, 1)
			return ErrReplacementUnderpriced
		}
	} else {
		if account.enqueued.length() == account.maxEnqueued && tx.Nonce != accountNonce {
			return ErrMaxEnqueuedLimitReached
		}

		// reject low nonce tx
		if tx.Nonce < accountNonce {
			metrics.IncrCounter([]string{txPoolMetrics, "nonce_too_low_tx"}, 1)
			return ErrNonceTooLow
		}
	}

	slotsAllocated := slotsRequired(tx)

	var slotsFreed uint64
	if oldTxWithSameNonce != nil {
		slotsFreed = slotsRequired(oldTxWithSameNonce)
	}

	var slotsIncreased uint64
	if slotsAllocated > slotsFreed {
		slotsIncreased = slotsAllocated - slotsFreed
		if !p.gauge.increaseWithinLimit(slotsIncreased) {
			return ErrTxPoolOverflow
		}
	}

	// add to index
	if ok := p.index.add(tx); !ok {
		metrics.IncrCounter([]string{txPoolMetrics, "already_known_tx"}, 1)

		if slotsIncreased > 0 {
			p.gauge.decrease(slotsIncreased)
		}

		return ErrAlreadyKnown
	}

	if slotsFreed > slotsAllocated {
		p.gauge.decrease(slotsFreed - slotsAllocated)
	}

	if oldTxWithSameNonce != nil {
		p.index.remove(oldTxWithSameNonce)
		p.removeFromExecutables(oldTxWithSameNonce.Hash)
	} else {
		metrics.SetGauge([]string{txPoolMetrics, "added_tx"}, 1)
	}

	account.enqueue(tx, oldTxWithSameNonce != nil) // add or replace tx into account

	// Signal events (ADDED and ENQUEUED) - same as original invokePromotion()
	p.eventManager.signalEvent(proto.EventType_ADDED, tx.Hash)
	p.eventManager.signalEvent(proto.EventType_ENQUEUED, tx.Hash)

	// Performance optimization: Geth-style active promotion
	// Immediately promote eligible transactions after enqueue (no async delay)
	// This aligns with Geth's enqueueTx() behavior: promote immediately after adding to enqueued
	promoted, pruned := account.promoteInternal()

	// Handle promoted transactions: update executables queue
	if len(promoted) > 0 {
		// Add the first promoted transaction to executables queue
		// (each account has only one primary transaction)
		// Other transactions will be added automatically when Pop() is called
		if firstPromoted := promoted[0]; firstPromoted != nil {
			p.executablesMu.Lock()
			p.executables.push(firstPromoted)
			p.executablesMu.Unlock()
		}

		// Update metrics
		p.updatePending(int64(len(promoted)))

		// Signal promotion event
		p.eventManager.signalEvent(proto.EventType_PROMOTED, toHash(promoted...)...)

		if p.logger.IsDebug() {
			p.logger.Debug("🔵 [processTx] 主动promote完成",
				"from", tx.From.String()[:16],
				"promotedCount", len(promoted),
				"txNonce", tx.Nonce,
				"accountNonce", accountNonce)
		}
	}

	// Handle pruned transactions: cleanup index and gauge
	if len(pruned) > 0 {
		p.index.remove(pruned...)
		p.gauge.decrease(slotsRequired(pruned...))
	}

	return nil
}

// addTx is the main entry point to the pool
// for all new transactions.
// Performance optimization: Event-driven architecture (Geth-style)
// Main path is lock-free: fast validation + async processing via channel
func (p *TxPool) addTx(origin txOrigin, tx *types.Transaction) error {
	// 🚨 全零哈希检测 - 显著日志标志
	if tx == nil {
		p.logger.Error("🚨 CRITICAL: addTx called with nil transaction", "origin", origin.String())
		return fmt.Errorf("nil transaction")
	}

	// Performance optimization: Fast validation (lock-free checks)
	// Only basic checks here, full validation happens in eventLoop
	if err := p.validateTxFast(tx); err != nil {
		return err
	}

	// Create error channel for async result
	errCh := make(chan error, 1)

	// Send to channel (non-blocking if buffer is full)
	select {
	case p.addTxCh <- &addTxRequest{
		tx:     tx,
		origin: origin,
		errCh:  errCh,
	}:
		// Successfully sent, wait for result
		select {
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Second):
			// Timeout: transaction is being processed but result not ready
			// This is acceptable for event-driven model
			return nil // Return success, transaction will be processed asynchronously
		}
	default:
		// Channel buffer is full, pool is overloaded
		metrics.IncrCounter([]string{txPoolMetrics, "txpool_full"}, 1)
		return ErrTxPoolOverflow
	}
}

// validateTxFast performs fast, lock-free validation checks
// Full validation happens in eventLoop
func (p *TxPool) validateTxFast(tx *types.Transaction) error {
	// Basic checks only (no state queries, no locks)
	if tx.Type == types.StateTx {
		return fmt.Errorf("%w: type %d rejected", ErrInvalidTxType, tx.Type)
	}

	// Check transaction size
	if uint64(len(tx.MarshalRLP())) > txMaxSize {
		return ErrOversizedData
	}

	// Check if the transaction has a strictly positive value
	if tx.Value.Sign() < 0 {
		return ErrNegativeValue
	}

	return nil
}

// Close shuts down the pool's main loop.
func (p *TxPool) Close() {
	p.eventManager.Close()
	close(p.shutdownCh)
}

// GetTopic returns the network topic for transaction broadcasting
func (p *TxPool) GetTopic() interface{} {
	return p.topic
}

// GetExecutablesCount returns the number of transactions in the executables queue
func (p *TxPool) GetExecutablesCount() int {
	p.executablesMu.RLock()
	defer p.executablesMu.RUnlock()
	if p.executables == nil {
		return 0
	}
	return p.executables.length()
}

// GetPendingCount returns the number of pending transactions
func (p *TxPool) GetPendingCount() int64 {
	return atomic.LoadInt64(&p.pending)
}

// GetAccountsCount returns the number of accounts with transactions
func (p *TxPool) GetAccountsCount() int {
	return int(p.accounts.promoted())
}

// DebugInfo returns debug information about the transaction pool
func (p *TxPool) DebugInfo() map[string]interface{} {
	return map[string]interface{}{
		"executablesCount": p.GetExecutablesCount(),
		"pendingCount":     p.GetPendingCount(),
		"accountsCount":    p.GetAccountsCount(),
		"hasTopic":         p.topic != nil,
		"isSealing":        p.sealing.Load(),
		"baseFee":          p.baseFee,
		"priceLimit":       p.priceLimit,
	}
}

// SetSigner sets the signer the pool will use
// to validate a transaction's signature.
func (p *TxPool) SetSigner(s signer) {
	p.signer = s
}

// SetSealing sets the sealing flag
func (p *TxPool) SetSealing(sealing bool) {
	p.sealing.CompareAndSwap(p.sealing.Load(), sealing)
}

// AddTx adds a new transaction to the pool (sent from json-RPC/gRPC endpoints)
// and broadcasts it to the network (if enabled).
func (p *TxPool) AddTx(tx *types.Transaction) error {
	if err := p.addTx(local, tx); err != nil {
		logFields := []interface{}{
			"err", err,
			"txHash", tx.Hash.String(),
			"from", tx.From.String(),
		}

		// 如果是 enqueued 限制错误，检查是否已达到最大限制
		if errors.Is(err, ErrMaxEnqueuedLimitReached) {
			if account := p.accounts.get(tx.From); account != nil {
				// Use read lock for read-only operation
				account.mu.RLock()
				enqueuedCount := account.enqueued.length()
				maxEnqueued := account.maxEnqueued
				accountNonce := account.getNonce()
				account.mu.RUnlock()

				// 添加账户信息到日志字段
				logFields = append(logFields,
					"enqueuedCount", enqueuedCount,
					"maxEnqueued", maxEnqueued,
					"accountNonce", accountNonce,
					"txNonce", tx.Nonce,
				)

				// 如果 enqueuedCount 等于 maxEnqueued，只打印日志不退出
				if enqueuedCount == maxEnqueued {
					logFields = append(logFields, "status", "REJECTED_NO_EXIT")
					p.logger.Error("⚠️⚠️⚠️ 账户 enqueued 队列已满，交易被拒绝（程序继续运行）", logFields...)
					return err
				}
			}
		}

		// 其他错误情况，保持原有退出逻辑
		p.logger.Error(" 交易加入交易池失败", logFields...)
		return err
	}

	// broadcast the transaction only if a topic
	// subscription is present
	if p.topic != nil {
		tx := &proto.Txn{
			Raw: &any.Any{
				Value: tx.MarshalRLP(),
			},
		}

		if err := p.topic.Publish(tx); err != nil {
			p.logger.Error("failed to topic tx", "err", err)
		}
	}

	return nil
}

// SyncPrepareNonces overlays nonce hints used by Prepare().
// Block builder passes in-flight block nonces so re-Prepare during Fill
// does not re-queue transactions already executed in the current block.
func (p *TxPool) SyncPrepareNonces(nonces map[types.Address]uint64) {
	if len(nonces) == 0 {
		return
	}

	p.prepareNonceCacheMu.Lock()
	for addr, nonce := range nonces {
		p.prepareNonceCache[addr] = nonce
	}
	p.prepareNonceCacheMu.Unlock()
}

// Prepare generates all the transactions
// ready for execution. (primaries)
func (p *TxPool) Prepare() {
	// fetch primary from each account
	primaries := p.accounts.getPrimaries()

	// 方案2：使用链上nonce过滤primaries，只添加nonce匹配的交易
	// 这样可以减少Fill()循环中的nonce检查失败，提高打包效率
	stateRoot := p.store.Header().StateRoot
	validPrimaries := make([]*types.Transaction, 0, len(primaries))
	skippedCount := 0

	// Performance optimization: Use cached nonce if available, otherwise query and cache
	for _, tx := range primaries {
		var currentNonce uint64

		// Try to get from cache first
		p.prepareNonceCacheMu.RLock()
		cachedNonce, cacheHit := p.prepareNonceCache[tx.From]
		p.prepareNonceCacheMu.RUnlock()

		if cacheHit {
			currentNonce = cachedNonce
		} else {
			// Cache miss: query from state and cache
			currentNonce = p.store.GetNonce(stateRoot, tx.From)
			p.prepareNonceCacheMu.Lock()
			p.prepareNonceCache[tx.From] = currentNonce
			p.prepareNonceCacheMu.Unlock()
		}

		if tx.Nonce == currentNonce {
			// nonce匹配，添加到executables队列
			validPrimaries = append(validPrimaries, tx)
		} else {
			// ️ nonce不匹配，不添加到executables队列
			// 交易仍然在promoted队列中，等待下次Prepare()时检查
			skippedCount++
		}
	}

	// 添加警告日志：如果所有交易都被过滤了
	if len(primaries) > 0 && len(validPrimaries) == 0 {
		p.logger.Warn("⚠️ [txpool.Prepare] 过滤了所有交易，可能导致空块",
			"blockNumber", p.store.Header().Number,
			"primariesCount", len(primaries),
			"validPrimariesCount", len(validPrimaries),
			"skippedCount", skippedCount,
			"stateRoot", stateRoot.String()[:16],
			"note", "所有交易的nonce都不匹配链上nonce，executables队列为空")
	}

	// create new executables queue with valid transactions only (nonce matched)
	p.executablesMu.Lock()
	p.executables = newPricesQueue(p.GetBaseFee(), validPrimaries)
	p.executablesMu.Unlock()
}

// Peek returns the best-price selected transaction ready for execution
// without removing it from the executables queue.
func (p *TxPool) Peek() *types.Transaction {
	p.executablesMu.RLock()
	defer p.executablesMu.RUnlock()
	if p.executables == nil {
		return nil
	}

	return p.executables.peek()
}

func (p *TxPool) removeFromExecutables(hash types.Hash) {
	p.executablesMu.Lock()
	defer p.executablesMu.Unlock()
	if p.executables != nil {
		p.executables.removeByHash(hash)
	}
}

// DiscardExecutable removes a stale transaction from the executables queue only.
// Promoted/enqueued queues are left intact so the correct nonce head remains.
func (p *TxPool) DiscardExecutable(tx *types.Transaction) {
	if tx == nil {
		return
	}

	p.removeFromExecutables(tx.Hash)
}

// Pop removes the given transaction from the
// associated promoted queue (account).
// Will update executables with the next primary
// from that account (if any).
func (p *TxPool) Pop(tx *types.Transaction) {
	// fetch the associated account
	account := p.accounts.get(tx.From)

	// Use unified lock instead of 2 separate locks
	account.mu.Lock()
	defer account.mu.Unlock()

	// pop the top most promoted tx
	account.promoted.pop()

	// update the account nonce -> *tx map
	account.nonceToTx.remove(tx)

	// successfully popping an account resets its demotions count to 0
	account.resetDemotions()

	// update state
	p.gauge.decrease(slotsRequired(tx))

	// update metrics
	p.updatePending(-1)

	p.removeFromExecutables(tx.Hash)

	// update executables
	// 参考以太坊：Pop()只负责移除交易，不清理过期交易
	// 清理过期交易由processEvent()/resetAccounts()负责（基于nonce批量清理）
	if nextTx := account.promoted.peek(); nextTx != nil {
		p.executablesMu.Lock()
		p.executables.push(nextTx)
		p.executablesMu.Unlock()
	}
}

// Drop clears the entire account associated with the given transaction
// and reverts its next (expected) nonce.
func (p *TxPool) Drop(tx *types.Transaction) {
	account := p.accounts.get(tx.From)
	p.dropAccount(account, tx.Nonce, tx)
}

// dropAccount clears all promoted and enqueued tx from the account
// signals EventType_DROPPED for provided hash, clears all the slots and metrics
// and sets nonce to provided nonce
func (p *TxPool) dropAccount(account *account, nextNonce uint64, tx *types.Transaction) {
	// Use unified lock instead of 3 separate locks
	account.mu.Lock()
	defer account.mu.Unlock()

	// num of all txs dropped
	droppedCount := 0

	// pool resource cleanup
	clearAccountQueue := func(txs []*types.Transaction) {
		p.index.remove(txs...)
		p.gauge.decrease(slotsRequired(txs...))

		// increase counter
		droppedCount += len(txs)
	}

	// rollback nonce
	account.setNonce(nextNonce)

	// reset accounts nonce map
	account.nonceToTx.reset()

	// drop promoted
	dropped := account.promoted.clear()
	clearAccountQueue(dropped)

	// update metrics
	p.updatePending(-1 * int64(len(dropped)))

	// drop enqueued
	dropped = account.enqueued.clear()
	clearAccountQueue(dropped)

	p.eventManager.signalEvent(proto.EventType_DROPPED, tx.Hash)

	if p.logger.IsDebug() {
		p.logger.Debug("dropped account txs",
			"num", droppedCount,
			"next_nonce", nextNonce,
			"address", tx.From.String(),
		)
	}
}

// Demote excludes an account from being further processed during block building
// due to a recoverable error. If an account has been demoted too many times (maxAccountDemotions),
// it is Dropped instead.
func (p *TxPool) Demote(tx *types.Transaction) {
	account := p.accounts.get(tx.From)
	if account.Demotions() >= maxAccountDemotions {
		if p.logger.IsDebug() {
			p.logger.Debug(
				"Demote: threshold reached - dropping account",
				"addr", tx.From.String(),
			)
		}

		p.Drop(tx)

		// reset the demotions counter
		account.resetDemotions()

		return
	}

	account.incrementDemotions()

	p.eventManager.signalEvent(proto.EventType_DEMOTED, tx.Hash)
}

// ResetWithEvent syncs the pool with a blockchain insert/reorg event.
func (p *TxPool) ResetWithEvent(event *blockchain.Event) {
	if event == nil {
		p.processEvent(&blockchain.Event{})

		return
	}

	p.logger.Debug("🔵 [ResetWithEvent] 开始处理区块链事件",
		"newChainCount", len(event.NewChain),
		"oldChainCount", len(event.OldChain),
		"eventType", event.Type,
		"source", event.Source)

	p.processEvent(event)
}

// ResetWithHeaders processes the transactions from the new
// headers to sync the pool with the new state.
func (p *TxPool) ResetWithHeaders(headers ...*types.Header) {
	p.ResetWithEvent(&blockchain.Event{
		NewChain: headers,
	})
}

// processEvent collects the latest nonces for each account contained
// in the received event. Resets all known accounts with the new nonce.
func (p *TxPool) processEvent(event *blockchain.Event) {
	if event == nil {
		event = &blockchain.Event{}
	}

	p.logger.Debug("🔵 [processEvent] 开始处理区块链事件",
		"newChainCount", len(event.NewChain),
		"oldChainCount", len(event.OldChain),
		"source", event.Source)

	reorgIndex := buildReorgChainIndex(p, event.NewChain)
	p.reinjectDiscarded(event.OldChain, reorgIndex)

	// Grab the latest state root now that the block has been inserted
	stateRoot := p.store.Header().StateRoot
	stateNonces := make(map[types.Address]uint64)

	// discover latest (next) nonces for all accounts
	for _, header := range event.NewChain {
		p.logger.Debug("🔵 [processEvent] 处理区块",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String()[:16])
		block, ok := p.store.GetBlockByHash(header.Hash, true)
		if !ok {
			p.logger.Error("could not find block in store", "hash", header.Hash.String())

			continue
		}

		// remove mined txs from the lookup map
		p.index.remove(block.Transactions...)

		// 方案1：直接从promoted队列中移除已打包的交易（必须）
		// 这样可以确保pending计数准确，避免已打包的交易还在promoted队列中
		// Extract latest nonces and remove mined transactions from promoted queue
		for _, tx := range block.Transactions {
			var err error

			addr := tx.From
			if addr == types.ZeroAddress {
				// From field is not set, extract the signer
				if addr, err = p.signer.Sender(tx); err != nil {
					p.logger.Error(
						fmt.Sprintf("unable to extract signer for transaction, %v", err),
					)

					continue
				}
			}

			// 方案1改进：基于nonce清理，不要求hash匹配（参考以太坊的两层清理机制）
			// 第一层：如果其他节点打包了某个nonce的交易，本节点应该清理该nonce的所有交易
			account := p.accounts.get(addr)
			if account != nil {
				p.logger.Debug("🔵 [processEvent-第一层清理] 检查账户交易",
					"from", addr.String(),
					"minedTxNonce", tx.Nonce,
					"minedTxHash", tx.Hash.String()[:16],
					"blockNumber", header.Number)

				// 尝试从promoted队列中移除
				// Use unified lock instead of 2 separate locks
				account.mu.Lock()

				// 检查是否有该nonce的交易（不管hash是否匹配）
				txInPool := account.nonceToTx.get(tx.Nonce)
				if txInPool != nil {
					p.logger.Debug("🔵 [processEvent-第一层清理] 找到相同nonce的交易",
						"from", addr.String(),
						"nonce", tx.Nonce,
						"poolTxHash", txInPool.Hash.String()[:16],
						"minedTxHash", tx.Hash.String()[:16],
						"hashMatch", txInPool.Hash == tx.Hash)

					// 尝试从promoted队列中移除
					removedFromPromoted := account.promoted.remove(txInPool.Hash)
					// 如果不在promoted队列，尝试从enqueued队列中移除
					removedFromEnqueued := false
					if !removedFromPromoted {
						removedFromEnqueued = account.enqueued.remove(txInPool.Hash)
					}

					// 如果从任一队列中移除了交易，需要释放slots
					if removedFromPromoted || removedFromEnqueued {
						account.nonceToTx.remove(txInPool)
						p.index.remove(txInPool)
						p.gauge.decrease(slotsRequired(txInPool))

						// 只有从promoted队列移除时才减少pending计数
						// enqueued队列中的交易不计入pending
						if removedFromPromoted {
							p.updatePending(-1)
						}

						// Performance optimization: Clear caches for this transaction
						p.validatedCache.remove(txInPool.Hash)
						p.balanceCache.remove(tx.From) // Balance may have changed

						queueType := "promoted"
						if removedFromEnqueued {
							queueType = "enqueued"
						}

						if txInPool.Hash == tx.Hash {
							p.logger.Debug("✅ [processEvent-第一层清理] 从"+queueType+"队列移除已打包的交易（hash匹配）",
								"txHash", txInPool.Hash.String()[:16],
								"nonce", tx.Nonce,
								"from", addr.String(),
								"blockNumber", header.Number)
						} else {
							p.logger.Debug("✅ [processEvent-第一层清理] 从"+queueType+"队列移除过期交易（nonce已被其他节点使用）",
								"txHash", txInPool.Hash.String()[:16],
								"nonce", tx.Nonce,
								"minedTxHash", tx.Hash.String()[:16],
								"from", addr.String(),
								"blockNumber", header.Number,
								"note", "其他节点打包了不同hash的同nonce交易")
						}
					} else {
						p.logger.Warn("⚠️ [processEvent-第一层清理] 交易在nonceToTx中但不在promoted或enqueued队列中（异常情况）",
							"txHash", txInPool.Hash.String()[:16],
							"nonce", tx.Nonce,
							"from", addr.String(),
							"blockNumber", header.Number)

						// 即使不在队列中，也要清理nonceToTx和index，并释放slots
						account.nonceToTx.remove(txInPool)
						p.index.remove(txInPool)
						p.gauge.decrease(slotsRequired(txInPool))
					}
				} else {
					p.logger.Debug("🔵 [processEvent-第一层清理] 交易池中未找到相同nonce的交易",
						"from", addr.String(),
						"nonce", tx.Nonce,
						"minedTxHash", tx.Hash.String()[:16])
				}

				account.mu.Unlock()
			} else {
				p.logger.Debug("🔵 [processEvent-第一层清理] 账户不存在于交易池",
					"from", addr.String(),
					"nonce", tx.Nonce)
			}

			// skip already processed accounts
			if _, processed := stateNonces[addr]; processed {
				continue
			}

			// fetch latest nonce from the state
			latestNonce := p.store.GetNonce(stateRoot, addr)

			p.logger.Debug("🔵 [processEvent] 从state获取账户nonce",
				"addr", addr.String()[:16],
				"latestNonce", latestNonce,
				"blockNumber", header.Number,
				"txNonce", tx.Nonce)

			// update the result map
			stateNonces[addr] = latestNonce
		}
	}

	// update base fee
	if ln := len(event.NewChain); ln > 0 {
		p.SetBaseFee(event.NewChain[ln-1])
	}

	// 修复：确保交易池中所有账户的 nonce 都从 state 中更新
	// 因为某些账户可能在链上的 nonce 已经增加（之前的交易被打包），
	// 但在当前区块中没有新交易，所以不会被添加到 stateNonces 中
	// 这会导致交易池中的 account nonce 与链上的 nonce 不一致
	p.accounts.Range(func(key, value interface{}) bool {
		addr, _ := key.(types.Address)
		account, _ := value.(*account)

		// 如果已经在 stateNonces 中，跳过（已经在区块中处理过）
		if _, processed := stateNonces[addr]; processed {
			return true
		}

		// 如果账户在交易池中有交易，需要从 state 获取最新 nonce 并更新
		// Use read lock for read-only operation
		account.mu.RLock()
		hasTxs := account.promoted.length() > 0 || account.enqueued.length() > 0
		account.mu.RUnlock()

		if hasTxs {
			// 从 state 获取最新的 nonce
			latestNonce := p.store.GetNonce(stateRoot, addr)
			currentNonce := account.getNonce()

			// 修复：即使 latestNonce == currentNonce，也要更新以确保状态一致
			// 因为链上的状态是权威的，即使值相同，也要通过resetAccounts确保清理过期交易
			if latestNonce != currentNonce {
				if latestNonce > currentNonce {
					p.logger.Info("🔵 [processEvent] 发现交易池账户nonce需要更新（state > txpool）",
						"addr", addr.String()[:16],
						"currentNonce", currentNonce,
						"latestNonce", latestNonce)
				} else {
					// 如果state的nonce小于交易池的nonce，说明state的nonce可能过时了
					// 但这种情况不应该发生，因为state是权威的
					p.logger.Warn("⚠️ [processEvent] state的nonce小于交易池的nonce（异常情况）",
						"addr", addr.String()[:16],
						"currentNonce", currentNonce,
						"latestNonce", latestNonce)
				}
				stateNonces[addr] = latestNonce
			} else {
				// 即使值相同，也要添加到stateNonces中，确保通过resetAccounts清理过期交易
				// 这样可以确保promoted队列中的过期交易（nonce < latestNonce）被清理
				stateNonces[addr] = latestNonce
			}
		}

		return true
	})

	// reset accounts with the new state
	p.resetAccounts(stateNonces)

	if !p.sealing.Load() {
		// only non-validator cleanup inactive accounts
		p.updateAccountSkipsCounts(stateNonces)
	}

	// Performance optimization: Clear prepareNonceCache when new blocks are mined
	// This ensures the cache reflects the latest chain state
	p.prepareNonceCacheMu.Lock()
	p.prepareNonceCache = make(map[types.Address]uint64)
	p.prepareNonceCacheMu.Unlock()
}

// validateTx ensures the transaction conforms to specific
// constraints before entering the pool.
func (p *TxPool) validateTx(tx *types.Transaction) error {
	// Check the transaction type. State transactions are not expected to be added to the pool
	if tx.Type == types.StateTx {
		metrics.IncrCounter([]string{txPoolMetrics, "invalid_tx_type"}, 1)

		return fmt.Errorf("%w: type %d rejected, state transactions are not expected to be added to the pool",
			ErrInvalidTxType, tx.Type)
	}

	// Check the transaction size to overcome DOS Attacks
	if uint64(len(tx.MarshalRLP())) > txMaxSize {
		metrics.IncrCounter([]string{txPoolMetrics, "oversized_data_txs"}, 1)

		return ErrOversizedData
	}

	// Check if the transaction has a strictly positive value
	if tx.Value.Sign() < 0 {
		metrics.IncrCounter([]string{txPoolMetrics, "negative_value_tx"}, 1)

		return ErrNegativeValue
	}

	// Check if the transaction is signed properly

	// Performance optimization: Check cache first to avoid repeated signature verification
	var from types.Address
	var signerErr error

	// Check if transaction signature is already validated
	if p.validatedCache.has(tx.Hash) {
		// Already validated, use cached From address or recover if not set
		if tx.From != types.ZeroAddress {
			from = tx.From
		} else {
			// Still need to recover From address
			from, signerErr = p.signer.Sender(tx)
			if signerErr != nil {
				metrics.IncrCounter([]string{txPoolMetrics, "invalid_signature_txs"}, 1)
				p.logger.Error("🚨 CRITICAL: Failed to extract sender from cached transaction",
					"error", signerErr, "txHash", tx.Hash.String())
				return ErrExtractSignature
			}
		}
	} else {
		// Not cached: Extract the sender (ECDSA recovery)
		from, signerErr = p.signer.Sender(tx)
		if signerErr != nil {
			metrics.IncrCounter([]string{txPoolMetrics, "invalid_signature_txs"}, 1)
			p.logger.Error("🚨 CRITICAL: Failed to extract sender from transaction",
				"error", signerErr, "txHash", tx.Hash.String())
			return ErrExtractSignature
		}

		// Verify From matches if already set
		if tx.From != types.ZeroAddress && tx.From != from {
			metrics.IncrCounter([]string{txPoolMetrics, "invalid_sender_txs"}, 1)
			return ErrInvalidSender
		}

		// Cache validated transaction (only after successful validation)
		p.validatedCache.add(tx.Hash)
	}

	// If no address was set, update it
	if tx.From == types.ZeroAddress {
		tx.From = from
	}

	// Grab current block number
	currentHeader := p.store.Header()
	currentBlockNumber := currentHeader.Number

	// Get forks state for the current block
	forks := p.forks.At(currentBlockNumber)

	// Check if transaction can deploy smart contract
	if tx.IsContractCreation() && forks.EIP158 && len(tx.Input) > state.TxPoolMaxInitCodeSize {
		metrics.IncrCounter([]string{txPoolMetrics, "contract_deploy_too_large_txs"}, 1)

		return runtime.ErrMaxCodeSizeExceeded
	}

	// Grab the state root, and block gas limit for the latest block
	stateRoot := currentHeader.StateRoot
	latestBlockGasLimit := currentHeader.GasLimit
	baseFee := p.GetBaseFee() // base fee is calculated for the next block

	if tx.Type == types.DynamicFeeTx {
		// Reject dynamic fee tx if london hardfork is not enabled
		if !forks.London {
			metrics.IncrCounter([]string{txPoolMetrics, "tx_type"}, 1)

			return fmt.Errorf("%w: type %d rejected, london hardfork is not enabled", ErrTxTypeNotSupported, tx.Type)
		}

		// DynamicFeeTx should be rejected if TxHashWithType fork is registered but not enabled for current block
		blockNumber, err := forkmanager.GetInstance().GetForkBlock(chain.TxHashWithType)
		if err == nil && blockNumber > currentBlockNumber {
			metrics.IncrCounter([]string{txPoolMetrics, "dynamic_tx_not_allowed"}, 1)

			return ErrDynamicTxNotAllowed
		}

		// Check EIP-1559-related fields and make sure they are correct
		if tx.GasFeeCap == nil || tx.GasTipCap == nil {
			metrics.IncrCounter([]string{txPoolMetrics, "underpriced_tx"}, 1)

			return ErrUnderpriced
		}

		if tx.GasFeeCap.BitLen() > 256 {
			metrics.IncrCounter([]string{txPoolMetrics, "fee_cap_too_high_dynamic_tx"}, 1)

			return ErrFeeCapVeryHigh
		}

		if tx.GasTipCap.BitLen() > 256 {
			metrics.IncrCounter([]string{txPoolMetrics, "tip_too_high_dynamic_tx"}, 1)

			return ErrTipVeryHigh
		}

		if tx.GasFeeCap.Cmp(tx.GasTipCap) < 0 {
			metrics.IncrCounter([]string{txPoolMetrics, "tip_above_fee_cap_dynamic_tx"}, 1)

			return ErrTipAboveFeeCap
		}

		// Reject underpriced transactions
		if tx.GasFeeCap.Cmp(new(big.Int).SetUint64(baseFee)) < 0 {
			metrics.IncrCounter([]string{txPoolMetrics, "underpriced_tx"}, 1)

			return ErrUnderpriced
		}
	} else {
		// Legacy approach to check if the given tx is not underpriced when london hardfork is enabled
		if forks.London && tx.GasPrice.Cmp(new(big.Int).SetUint64(baseFee)) < 0 {
			metrics.IncrCounter([]string{txPoolMetrics, "underpriced_tx"}, 1)

			return ErrUnderpriced
		}
	}

	// Check if the given tx is not underpriced
	if tx.GetGasPrice(baseFee).Cmp(new(big.Int).SetUint64(p.priceLimit)) < 0 {
		metrics.IncrCounter([]string{txPoolMetrics, "underpriced_tx"}, 1)

		return ErrUnderpriced
	}

	// Check nonce ordering
	if p.store.GetNonce(stateRoot, tx.From) > tx.Nonce {
		metrics.IncrCounter([]string{txPoolMetrics, "nonce_too_low_tx"}, 1)

		return ErrNonceTooLow
	}

	// Performance optimization: Check balance cache first
	var accountBalance *big.Int
	var balanceErr error

	if cached, ok := p.balanceCache.get(tx.From); ok {
		accountBalance = cached
	} else {
		// Cache miss: query from state
		accountBalance, balanceErr = p.store.GetBalance(stateRoot, tx.From)
		if balanceErr != nil {
			metrics.IncrCounter([]string{txPoolMetrics, "invalid_account_state_tx"}, 1)
			return ErrInvalidAccountState
		}
		// Cache the balance
		p.balanceCache.set(tx.From, accountBalance)
	}

	// Check if the sender has enough funds to execute the transaction
	spendable := accountBalance
	if lbp, ok := p.store.(lockedBalanceProvider); ok {
		if locked, lerr := lbp.GetLockedBalance(stateRoot, tx.From); lerr == nil && locked != nil && locked.Sign() > 0 {
			if spendable.Cmp(locked) > 0 {
				spendable = new(big.Int).Sub(spendable, locked)
			} else {
				spendable = big.NewInt(0)
			}
		}
	}

	if spendable.Cmp(tx.Cost()) < 0 {
		metrics.IncrCounter([]string{txPoolMetrics, "insufficient_funds_tx"}, 1)

		return ErrInsufficientFunds
	}

	// Make sure the transaction has more gas than the basic transaction fee
	intrinsicGas, err := state.TransactionGasCost(tx, forks.Homestead, forks.Istanbul)
	if err != nil {
		metrics.IncrCounter([]string{txPoolMetrics, "invalid_intrinsic_gas_tx"}, 1)

		return err
	}

	if tx.Gas < intrinsicGas {
		metrics.IncrCounter([]string{txPoolMetrics, "intrinsic_gas_low_tx"}, 1)

		return ErrIntrinsicGas
	}

	if tx.Gas > latestBlockGasLimit {
		metrics.IncrCounter([]string{txPoolMetrics, "block_gas_limit_exceeded_tx"}, 1)

		return ErrBlockLimitExceeded
	}

	return nil
}

func (p *TxPool) signalPruning() {
	select {
	case p.pruneCh <- struct{}{}:
	default: //	pruning handler is active or in cooldown
	}
}

func (p *TxPool) pruneAccountsWithNonceHoles() {
	p.accounts.Range(
		func(_, value interface{}) bool {
			account, _ := value.(*account)

			// Use unified lock instead of 2 separate locks
			account.mu.Lock()
			defer account.mu.Unlock()

			firstTx := account.enqueued.peek()

			if firstTx == nil {
				return true
			}

			if firstTx.Nonce == account.getNonce() {
				return true
			}

			removed := account.enqueued.clear()

			account.nonceToTx.remove(removed...)
			p.index.remove(removed...)
			p.gauge.decrease(slotsRequired(removed...))

			return true
		},
	)
}

// OLD addTx function removed - replaced by event-driven version above
// The old synchronous implementation has been moved to processTx() which runs in eventLoop()

func (p *TxPool) invokePromotion(tx *types.Transaction, callPromote bool) {
	p.eventManager.signalEvent(proto.EventType_ADDED, tx.Hash)

	if p.logger.IsDebug() {
		p.logger.Debug("enqueue request", "hash", tx.Hash.String())
	}

	p.eventManager.signalEvent(proto.EventType_ENQUEUED, tx.Hash)

	if callPromote {
		select {
		case <-p.shutdownCh:
		case p.promoteReqCh <- promoteRequest{account: tx.From}: // BLOCKING
		}
	}
}

// handlePromoteRequest handles moving promotable transactions
// of some account from enqueued to promoted. Can only be
// invoked by handleEnqueueRequest or resetAccount.
// Note: This is still used for async promotion triggered by addTx,
// but reset() now uses direct promote (Geth-style).
func (p *TxPool) handlePromoteRequest(req promoteRequest) {
	addr := req.account
	account := p.accounts.get(addr)

	// promote enqueued txs
	promoted, pruned := account.promote()
	if p.logger.IsDebug() {
		p.logger.Debug("promote request", "promoted", promoted, "addr", addr.String())
	}

	p.index.remove(pruned...)
	p.gauge.decrease(slotsRequired(pruned...))

	// Handle promoted transactions: update executables queue
	if len(promoted) > 0 {
		// Add the first promoted transaction to executables queue
		// (each account has only one primary transaction)
		// Other transactions will be added automatically when Pop() is called
		if firstPromoted := promoted[0]; firstPromoted != nil {
			p.executablesMu.Lock()
			p.executables.push(firstPromoted)
			p.executablesMu.Unlock()
		}

		// update metrics
		p.updatePending(int64(len(promoted)))

		p.eventManager.signalEvent(proto.EventType_PROMOTED, toHash(promoted...)...)
	}
}

// addGossipTx handles receiving transactions
// gossiped by the network.
func (p *TxPool) addGossipTx(obj interface{}, _ peer.ID) {
	if !p.sealing.Load() {
		return
	}

	raw, ok := obj.(*proto.Txn)
	if !ok {
		p.logger.Error("failed to cast gossiped message to txn")

		return
	}

	// Verify that the gossiped transaction message is not empty
	if raw == nil || raw.Raw == nil {
		p.logger.Error("🚨 CRITICAL: malformed gossip transaction message received - raw is nil")

		return
	}

	// 🚨 检测gossip消息中的原始数据
	if len(raw.Raw.Value) == 0 {
		p.logger.Error("🚨 CRITICAL: empty gossip transaction raw data received")
		return
	}

	tx := new(types.Transaction)

	// decode tx
	rawBytes := raw.Raw.Value

	// DEBUG: 打印接收到的gossip交易基本信息
	if p.logger.IsDebug() {
		p.logger.Debug("🔎 接收到Gossip交易原始数据",
			"rawLen", len(rawBytes),
			"firstByte", func() string {
				if len(rawBytes) > 0 {
					return fmt.Sprintf("0x%02x", rawBytes[0])
				}
				return "<nil>"
			}(),
			"rawHeadHex", func() string {
				if len(rawBytes) > 32 {
					return fmt.Sprintf("%x", rawBytes[:32])
				}
				return fmt.Sprintf("%x", rawBytes)
			}(),
		)
	}

	// 按纯RLP处理（标准交易）
	if err := tx.UnmarshalRLP(rawBytes); err != nil {
		p.logger.Error("🚨 CRITICAL: failed to decode broadcast tx",
			"err", err,
			"rawDataLength", len(rawBytes),
			"rawDataHex", func() string {
				if len(rawBytes) > 32 {
					return fmt.Sprintf("%x", rawBytes[:32])
				}
				return fmt.Sprintf("%x", rawBytes)
			}())

		return
	}

	// 解码后补齐哈希与发送者地址
	currentNumber := uint64(0)
	if hdr := p.store.Header(); hdr != nil {
		currentNumber = hdr.Number
	}
	tx.ComputeHash(currentNumber)
	if tx.From == types.ZeroAddress {
		if addr, err := p.signer.Sender(tx); err == nil {
			tx.From = addr
		} else {
			p.logger.Error("🚨 CRITICAL: failed to recover sender for gossip tx",
				"err", err,
				"txType", tx.Type,
				"nonce", tx.Nonce)
			return
		}
	}

	if p.logger.IsDebug() {
		p.logger.Debug("🧮 Gossip交易补齐完成",
			"hash", tx.Hash.String(),
			"from", tx.From.String(),
			"txType", tx.Type.String(),
			"nonce", tx.Nonce,
			"blockNumUsedForHash", currentNumber)
	}

	// 最终零哈希保护
	if tx.Hash == (types.Hash{}) {
		p.logger.Error("🚨 CRITICAL: decoded gossip tx has zero hash after ComputeHash",
			"txType", tx.Type,
			"nonce", tx.Nonce,
			"gasPrice", tx.GasPrice.String(),
			"value", tx.Value.String(),
			"from", tx.From.String(),
			"rawDataLength", len(raw.Raw.Value))
		return
	}

	// add tx
	if err := p.addTx(gossip, tx); err != nil {
		if errors.Is(err, ErrAlreadyKnown) {
			if p.logger.IsDebug() {
				p.logger.Debug("rejecting known tx (gossip)", "hash", tx.Hash.String())
			}

			return
		}

		p.logger.Error("failed to add broadcast tx", "err", err, "hash", tx.Hash.String())
	}
}

// resetAccounts updates existing accounts with the new nonce and prunes stale transactions.
func (p *TxPool) resetAccounts(stateNonces map[types.Address]uint64) {
	if len(stateNonces) == 0 {
		return
	}

	p.logger.Debug("🔵 [resetAccounts] 开始第二层清理（批量清理过期交易）",
		"accountCount", len(stateNonces))

	var (
		allPrunedPromoted []*types.Transaction
		allPrunedEnqueued []*types.Transaction
	)

	// clear all accounts of stale txs
	for addr, newNonce := range stateNonces {
		account := p.accounts.get(addr)

		if account == nil {
			// no updates for this account
			continue
		}

		oldNonce := account.getNonce()
		p.logger.Debug("🔵 [resetAccounts] 重置账户nonce",
			"addr", addr.String()[:16],
			"oldNonce", oldNonce,
			"newNonce", newNonce)

		prunedPromoted, prunedEnqueued, promoted := account.reset(newNonce, p.promoteReqCh, addr, p.logger)

		if len(prunedPromoted) > 0 || len(prunedEnqueued) > 0 || len(promoted) > 0 {
			p.logger.Debug("🔵 [resetAccounts] 账户清理结果",
				"addr", addr.String()[:16],
				"prunedPromotedCount", len(prunedPromoted),
				"prunedEnqueuedCount", len(prunedEnqueued),
				"promotedCount", len(promoted),
				"oldNonce", oldNonce,
				"newNonce", newNonce)
		}

		// Handle promoted transactions: update executables queue
		// Performance optimization: Geth-style direct promote
		if len(promoted) > 0 {
			// Add the first promoted transaction to executables queue
			// (each account has only one primary transaction)
			// Other transactions will be added automatically when Pop() is called
			if firstPromoted := promoted[0]; firstPromoted != nil {
				p.executablesMu.Lock()
				p.executables.push(firstPromoted)
				p.executablesMu.Unlock()
			}

			// Update metrics
			p.updatePending(int64(len(promoted)))

			// Signal promotion event
			p.eventManager.signalEvent(proto.EventType_PROMOTED, toHash(promoted...)...)

			p.logger.Debug("🔵 [resetAccounts] 账户promote完成",
				"addr", addr.String()[:16],
				"promotedCount", len(promoted),
				"oldNonce", oldNonce,
				"newNonce", newNonce)
		}

		// append pruned
		allPrunedPromoted = append(allPrunedPromoted, prunedPromoted...)
		allPrunedEnqueued = append(allPrunedEnqueued, prunedEnqueued...)

		// new state for account -> demotions are reset to 0
		account.resetDemotions()
	}

	p.logger.Debug("🔵 [resetAccounts] 第二层清理汇总",
		"totalPrunedPromoted", len(allPrunedPromoted),
		"totalPrunedEnqueued", len(allPrunedEnqueued))

	// pool cleanup callback
	cleanup := func(stale []*types.Transaction) {
		p.index.remove(stale...)
		p.gauge.decrease(slotsRequired(stale...))
	}

	// prune pool state
	if len(allPrunedPromoted) > 0 {
		p.logger.Debug("✅ [resetAccounts] 清理promoted交易",
			"count", len(allPrunedPromoted),
			"txHashes", func() []string {
				var hashes []string
				for i, tx := range allPrunedPromoted {
					if i < 5 { // 只显示前5个
						hashes = append(hashes, tx.Hash.String()[:16])
					}
				}
				return hashes
			}())

		cleanup(allPrunedPromoted)

		p.eventManager.signalEvent(
			proto.EventType_PRUNED_PROMOTED,
			toHash(allPrunedPromoted...)...,
		)

		p.updatePending(int64(-1 * len(allPrunedPromoted)))
	}

	if len(allPrunedEnqueued) > 0 {
		p.logger.Debug("✅ [resetAccounts] 清理enqueued交易",
			"count", len(allPrunedEnqueued),
			"txHashes", func() []string {
				var hashes []string
				for i, tx := range allPrunedEnqueued {
					if i < 5 { // 只显示前5个
						hashes = append(hashes, tx.Hash.String()[:16])
					}
				}
				return hashes
			}())

		cleanup(allPrunedEnqueued)

		p.eventManager.signalEvent(
			proto.EventType_PRUNED_ENQUEUED,
			toHash(allPrunedEnqueued...)...,
		)
	}

	p.logger.Debug("🔵 [resetAccounts] 第二层清理完成")
}

// updateAccountSkipsCounts update the accounts' skips,
// the number of the consecutive blocks that doesn't have the account's transactions
func (p *TxPool) updateAccountSkipsCounts(latestActiveAccounts map[types.Address]uint64) {
	stateRoot := p.store.Header().StateRoot
	p.accounts.Range(
		func(key, value interface{}) bool {
			address, _ := key.(types.Address)
			account, _ := value.(*account)

			if _, ok := latestActiveAccounts[address]; ok {
				account.resetSkips()

				return true
			}

			firstTx := account.getLowestTx()
			if firstTx == nil {
				// no need to increment anything,
				// account has no txs
				return true
			}

			if account.incrementSkips() < maxAccountSkips {
				return true
			}

			// account has been skipped too many times
			nextNonce := p.store.GetNonce(stateRoot, firstTx.From)
			p.dropAccount(account, nextNonce, firstTx)

			account.resetSkips()

			return true
		},
	)
}

// getOrCreateAccount creates an account and
// ensures it is only initialized once.
func (p *TxPool) getOrCreateAccount(newAddr types.Address) *account {
	if account := p.accounts.get(newAddr); account != nil {
		return account
	}

	// fetch nonce from state
	stateRoot := p.store.Header().StateRoot
	stateNonce := p.store.GetNonce(stateRoot, newAddr)

	// initialize the account
	return p.accounts.initOnce(newAddr, stateNonce)
}

// Length returns the total number of all promoted transactions.
func (p *TxPool) Length() uint64 {
	return p.accounts.promoted()
}

// toHash returns the hash(es) of given transaction(s)
func toHash(txs ...*types.Transaction) (hashes []types.Hash) {
	for _, tx := range txs {
		hashes = append(hashes, tx.Hash)
	}

	return
}

// cleanupAccounts 清理不活跃和过多的账户
func (p *TxPool) cleanupAccounts() {
	// 清理不活跃的账户（超过30分钟未访问且为空）
	inactiveCleaned, inactiveRemovedTxs := p.accounts.cleanupInactiveAccounts(30 * time.Minute)

	// 清理过多的账户
	oversizedCleaned, oversizedRemovedTxs := p.accounts.cleanupOversizedAccounts()

	totalCleaned := inactiveCleaned + oversizedCleaned

	// 合并所有被删除账户的交易
	allRemovedTxs := append(inactiveRemovedTxs, oversizedRemovedTxs...)

	// 释放被删除账户的所有交易的slots
	// 注意：只释放那些确实存在于index中的交易（避免重复释放）
	if len(allRemovedTxs) > 0 {
		// 过滤出那些确实存在于index中的交易（避免重复释放）
		validTxs := make([]*types.Transaction, 0, len(allRemovedTxs))
		for _, tx := range allRemovedTxs {
			// 检查交易是否在index中（通过尝试获取）
			if _, exists := p.index.get(tx.Hash); exists {
				validTxs = append(validTxs, tx)
			}
		}

		if len(validTxs) > 0 {
			slotsToRelease := slotsRequired(validTxs...)
			p.gauge.decrease(slotsToRelease)
			p.index.remove(validTxs...)

			// 只计算promoted队列中的交易数量（enqueued不计入pending）
			pendingCount := 0
			for _, tx := range validTxs {
				if account := p.accounts.getWithoutAccess(tx.From); account != nil {
					account.mu.RLock()
					// 检查交易是否在promoted队列中
					for _, promotedTx := range account.promoted.queue {
						if promotedTx.Hash == tx.Hash {
							pendingCount++
							break
						}
					}
					account.mu.RUnlock()
				}
			}
			if pendingCount > 0 {
				p.updatePending(-1 * int64(pendingCount))
			}

			p.logger.Info("✅ [cleanupAccounts] 释放被删除账户的slots",
				"账户数", totalCleaned,
				"总交易数", len(allRemovedTxs),
				"有效交易数", len(validTxs),
				"释放slots", slotsToRelease)
		} else {
			p.logger.Debug("🔵 [cleanupAccounts] 被删除账户的交易都已不在index中（可能已被其他清理流程释放）",
				"账户数", totalCleaned,
				"交易数", len(allRemovedTxs))
		}
	}

	if totalCleaned > 0 {
		p.logger.Debug("交易池账户清理完成",
			"不活跃账户清理", inactiveCleaned,
			"过多账户清理", oversizedCleaned,
			"总清理数量", totalCleaned,
			"当前账户数", p.GetAccountsCount(),
			"释放交易数", len(allRemovedTxs))
	}
}
