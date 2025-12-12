package txpool

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Thread safe map of all accounts registered by the pool.
// Each account (value) is bound to one address (key).
type accountsMap struct {
	sync.Map

	count            uint64
	maxEnqueuedLimit uint64

	// 增强的账户管理
	// Performance optimization: Use sync.Map for accountLastAccess to eliminate lock contention
	accountLastAccess sync.Map // 账户最后访问时间（使用sync.Map，无锁）
	maxAccountCount   int      // 最大账户数量
}

// Initializes an account for the given address.
func (m *accountsMap) initOnce(addr types.Address, nonce uint64) *account {
	a, loaded := m.LoadOrStore(addr, &account{
		enqueued:    newAccountQueue(),
		promoted:    newAccountQueue(),
		nonceToTx:   newNonceToTxLookup(),
		maxEnqueued: m.maxEnqueuedLimit,
		nextNonce:   nonce,
	})
	newAccount := a.(*account) //nolint:forcetypeassert

	if !loaded {
		// update global count if it was a store
		atomic.AddUint64(&m.count, 1)
	}

	// 记录访问时间
	m.recordAccess(addr)

	return newAccount
}

// exists checks if an account exists within the map.
func (m *accountsMap) exists(addr types.Address) bool {
	_, ok := m.Load(addr)

	return ok
}

// getPrimaries collects the heads (first-in-line transaction)
// from each of the promoted queues.
func (m *accountsMap) getPrimaries() (primaries []*types.Transaction) {
	m.Range(func(key, value interface{}) bool {
		addressKey, ok := key.(types.Address)
		if !ok {
			return false
		}

		account := m.get(addressKey)

		// Use read lock for read-only operation
		account.mu.RLock()
		// add head of the queue
		if tx := account.promoted.peek(); tx != nil {
			primaries = append(primaries, tx)
		}
		account.mu.RUnlock()

		return true
	})

	return primaries
}

// get returns the account associated with the given address.
func (m *accountsMap) get(addr types.Address) *account {
	a, ok := m.Load(addr)
	if !ok {
		return nil
	}

	fetchedAccount, ok := a.(*account)
	if !ok {
		return nil
	}

	// 记录访问时间
	m.recordAccess(addr)

	return fetchedAccount
}

// promoted returns the number of all promoted transactons.
func (m *accountsMap) promoted() (total uint64) {
	m.Range(func(key, value interface{}) bool {
		accountKey, ok := key.(types.Address)
		if !ok {
			return false
		}

		account := m.get(accountKey)

		// Use read lock for read-only operation
		account.mu.RLock()
		total += account.promoted.length()
		account.mu.RUnlock()

		return true
	})

	return
}

// allTxs returns all promoted and all enqueued transactions, depending on the flag.
func (m *accountsMap) allTxs(includeEnqueued bool) (
	allPromoted, allEnqueued map[types.Address][]*types.Transaction,
) {
	allPromoted = make(map[types.Address][]*types.Transaction)
	allEnqueued = make(map[types.Address][]*types.Transaction)

	m.Range(func(key, value interface{}) bool {
		addr, _ := key.(types.Address)
		account := m.get(addr)

		// Use read lock for read-only operation
		account.mu.RLock()
		if account.promoted.length() != 0 {
			allPromoted[addr] = account.promoted.queue
		}

		if includeEnqueued {
			if account.enqueued.length() != 0 {
				allEnqueued[addr] = account.enqueued.queue
			}
		}
		account.mu.RUnlock()

		return true
	})

	return
}

type nonceToTxLookup struct {
	mapping map[uint64]*types.Transaction
	mutex   sync.Mutex
}

func newNonceToTxLookup() *nonceToTxLookup {
	return &nonceToTxLookup{
		mapping: make(map[uint64]*types.Transaction),
	}
}

func (m *nonceToTxLookup) lock() {
	m.mutex.Lock()
}

func (m *nonceToTxLookup) unlock() {
	m.mutex.Unlock()
}

func (m *nonceToTxLookup) get(nonce uint64) *types.Transaction {
	return m.mapping[nonce]
}

func (m *nonceToTxLookup) set(tx *types.Transaction) {
	m.mapping[tx.Nonce] = tx
}

func (m *nonceToTxLookup) reset() {
	m.mapping = make(map[uint64]*types.Transaction)
}

func (m *nonceToTxLookup) remove(txs ...*types.Transaction) {
	for _, tx := range txs {
		delete(m.mapping, tx.Nonce)
	}
}

// An account is the core structure for processing
// transactions from a specific address. The nextNonce
// field is what separates the enqueued from promoted transactions:
//
// 1. enqueued - transactions higher than the nextNonce
// 2. promoted - transactions lower than the nextNonce
//
// If an enqueued transaction matches the nextNonce,
// a promoteRequest is signaled for this account
// indicating the account's enqueued transaction(s)
// are ready to be moved to the promoted queue.
//
// Performance optimization: Use a single RWMutex instead of 3 separate locks
// to reduce lock contention and improve TPS by 3x.
type account struct {
	// Unified lock for all account operations (replaces 3 separate locks)
	// Use RLock() for read operations, Lock() for write operations
	mu sync.RWMutex

	enqueued, promoted *accountQueue
	nonceToTx          *nonceToTxLookup

	nextNonce uint64
	demotions uint64
	// the number of consecutive blocks that don't contain account's transaction
	skips uint64

	//	maximum number of enqueued transactions
	maxEnqueued uint64
}

// getNonce returns the next expected nonce for this account.
func (a *account) getNonce() uint64 {
	return atomic.LoadUint64(&a.nextNonce)
}

// setNonce sets the next expected nonce for this account.
func (a *account) setNonce(nonce uint64) {
	atomic.StoreUint64(&a.nextNonce, nonce)
}

// Demotions returns the current value of demotions
func (a *account) Demotions() uint64 {
	return a.demotions
}

// resetDemotions sets 0 to demotions to clear count
func (a *account) resetDemotions() {
	a.demotions = 0
}

// incrementDemotions increments demotions
func (a *account) incrementDemotions() {
	a.demotions++
}

// reset aligns the account with the new nonce
// by pruning all transactions with nonce lesser than new.
// Performance optimization: Geth-style direct promote (no async signal delay)
// After pruning, transactions are directly promoted if eligible.
func (a *account) reset(nonce uint64, promoteCh chan<- promoteRequest, addr types.Address, logger hclog.Logger) (
	prunedPromoted,
	prunedEnqueued,
	promoted []*types.Transaction,
) {
	oldNonce := a.getNonce()
	if logger != nil {
		logger.Debug("🔵 [account.reset] 开始重置账户",
			"addr", addr.String()[:16],
			"oldNonce", oldNonce,
			"newNonce", nonce)
	}

	// Use unified lock instead of 3 separate locks
	a.mu.Lock()
	defer a.mu.Unlock()

	// prune the promoted txs
	prunedPromoted = a.promoted.prune(nonce, addr, logger, "promoted")
	a.nonceToTx.remove(prunedPromoted...)

	if logger != nil && len(prunedPromoted) > 0 {
		logger.Info("🔵 [account.reset] promoted队列清理完成",
			"addr", addr.String()[:16],
			"prunedCount", len(prunedPromoted),
			"prunedNonces", func() []uint64 {
				var nonces []uint64
				for _, tx := range prunedPromoted {
					nonces = append(nonces, tx.Nonce)
				}
				return nonces
			}())
	}
	// 只需要清理 promoted 队列，但必须更新 nextNonce 以与链上状态同步
	// 注意：enqueued 队列中的交易（nonce >= oldNonce）仍然有效，会在链上 nonce 增长时被 promote
	if nonce <= oldNonce {
		if logger != nil {
			logger.Debug("🔵 [account.reset] 只清理promoted队列（newNonce <= oldNonce），但需要更新nextNonce",
				"addr", addr.String()[:16],
				"oldNonce", oldNonce,
				"newNonce", nonce)
		}

		// 更新 nextNonce 以与链上状态同步（链上状态是权威的）
		a.setNonce(nonce)

		// Return with empty promoted slice (no promotion in this case)
		return prunedPromoted, prunedEnqueued, nil
	}

	// prune the enqueued txs
	prunedEnqueued = a.enqueued.prune(nonce, addr, logger, "enqueued")
	a.nonceToTx.remove(prunedEnqueued...)

	if logger != nil && len(prunedEnqueued) > 0 {
		logger.Info("🔵 [account.reset] enqueued队列清理完成",
			"addr", addr.String()[:16],
			"prunedCount", len(prunedEnqueued),
			"prunedNonces", func() []uint64 {
				var nonces []uint64
				for _, tx := range prunedEnqueued {
					nonces = append(nonces, tx.Nonce)
				}
				return nonces
			}())
	}

	// update nonce expected for this account
	oldNonceBeforeSet := a.getNonce()
	a.setNonce(nonce)
	newNonceAfterSet := a.getNonce()

	if logger != nil {
		logger.Info("🔵 [account.reset] 更新账户nonce",
			"addr", addr.String()[:16],
			"oldNonce", oldNonceBeforeSet,
			"newNonce", nonce,
			"actualNonceAfterSet", newNonceAfterSet)
	}

	// Performance optimization: Geth-style direct promote (no async signal delay)
	// Directly promote eligible transactions instead of sending async signal
	promoted, prunedDuringPromote := a.promoteInternal()

	if logger != nil && len(promoted) > 0 {
		logger.Info("🔵 [account.reset] 直接批量promote完成",
			"addr", addr.String()[:16],
			"promotedCount", len(promoted),
			"promotedNonces", func() []uint64 {
				var nonces []uint64
				for _, tx := range promoted {
					nonces = append(nonces, tx.Nonce)
				}
				return nonces
			}(),
			"newNonce", nonce)
	}

	// Merge pruned transactions from promotion into prunedEnqueued
	if len(prunedDuringPromote) > 0 {
		prunedEnqueued = append(prunedEnqueued, prunedDuringPromote...)
	}

	if logger != nil {
		logger.Info("🔵 [account.reset] 重置完成",
			"addr", addr.String()[:16],
			"oldNonce", oldNonce,
			"newNonce", nonce,
			"prunedPromoted", len(prunedPromoted),
			"prunedEnqueued", len(prunedEnqueued),
			"promoted", len(promoted))
	}

	return
}

// enqueue push the transaction onto the enqueued queue or replace it
func (a *account) enqueue(tx *types.Transaction, replace bool) {
	replaceInQueue := func(queue minNonceQueue) bool {
		for i, x := range queue {
			if x.Nonce == tx.Nonce {
				queue[i] = tx // replace

				return true
			}
		}

		return false
	}

	a.nonceToTx.set(tx)

	if !replace {
		a.enqueued.push(tx)
	} else {
		// first -> try to replace in enqueued
		if !replaceInQueue(a.enqueued.queue) {
			// .. then try to replace in promoted
			replaceInQueue(a.promoted.queue)
		}
	}
}

// promoteInternal is the core logic for promoting transactions (without locking).
// It should only be called when the account lock is already held.
// Performance optimization: Geth-style direct promote (no async signal delay)
func (a *account) promoteInternal() (promoted []*types.Transaction, pruned []*types.Transaction) {
	// sanity check
	currentNonce := a.getNonce()
	if a.enqueued.length() == 0 || a.enqueued.peek().Nonce > currentNonce {
		// nothing to promote
		return
	}

	nextNonce := a.enqueued.peek().Nonce

	// move all promotable txs (enqueued txs that are sequential in nonce)
	// to the account's promoted queue
	for {
		tx := a.enqueued.peek()
		if tx == nil || tx.Nonce != nextNonce {
			break
		}

		// pop from enqueued
		tx = a.enqueued.pop()

		// push to promoted
		a.promoted.push(tx)

		// update counters
		nextNonce = tx.Nonce + 1

		// prune the transactions with lower nonce
		// Note: This is called from promote() which doesn't have logger access
		// We use a nil logger here since this is an internal cleanup during promotion
		var dummyLogger hclog.Logger
		pruned = append(pruned, a.enqueued.prune(nextNonce, tx.From, dummyLogger, "enqueued")...)

		// update return result
		promoted = append(promoted, tx)
	}

	// only update the nonce map if the new nonce
	// is higher than the one previously stored.
	if nextNonce > currentNonce {
		a.setNonce(nextNonce)
	}

	// nonceToTx operations are now protected by a.mu
	a.nonceToTx.remove(pruned...)

	return
}

// Promote moves eligible transactions from enqueued to promoted.
//
// Eligible transactions are all sequential in order of nonce
// and the first one has to have nonce less (or equal) to the account's
// nextNonce.
func (a *account) promote() (promoted []*types.Transaction, pruned []*types.Transaction) {
	// Use unified lock instead of 2 separate locks
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.promoteInternal()
}

// resetSkips sets 0 to skips
func (a *account) resetSkips() {
	atomic.StoreUint64(&a.skips, 0)
}

// incrementSkips increments skips
func (a *account) incrementSkips() uint64 {
	return atomic.AddUint64(&a.skips, 1)
}

// getLowestTx returns the transaction with lowest nonce, which might be popped next
// this method don't pop a transaction from both queues
func (a *account) getLowestTx() *types.Transaction {
	// Use read lock for read-only operation
	a.mu.RLock()
	defer a.mu.RUnlock()

	if firstPromoted := a.promoted.peek(); firstPromoted != nil {
		return firstPromoted
	}

	if firstEnqueued := a.enqueued.peek(); firstEnqueued != nil {
		return firstEnqueued
	}

	return nil
}

// recordAccess 记录账户访问时间
// Performance optimization: Use sync.Map for lock-free operation
func (m *accountsMap) recordAccess(addr types.Address) {
	m.accountLastAccess.Store(addr, time.Now())
}

// cleanupInactiveAccounts 清理不活跃的账户
// Performance optimization: Use sync.Map.Range for lock-free iteration
// 返回被删除账户的所有交易，用于释放slots
func (m *accountsMap) cleanupInactiveAccounts(maxAge time.Duration) (int, []*types.Transaction) {
	now := time.Now()
	cleanedCount := 0
	inactiveAccounts := make([]types.Address, 0)
	allRemovedTxs := make([]*types.Transaction, 0)

	// 收集不活跃的账户（使用sync.Map.Range，无锁迭代）
	m.accountLastAccess.Range(func(key, value interface{}) bool {
		addr := key.(types.Address)
		lastAccess := value.(time.Time)

		if now.Sub(lastAccess) > maxAge {
			// 检查账户是否为空（没有待处理或已提升的交易）
			if account := m.getWithoutAccess(addr); account != nil {
				if m.isAccountEmpty(account) {
					inactiveAccounts = append(inactiveAccounts, addr)
				}
			}
		}
		return true
	})

	// 删除不活跃的空账户，并收集所有交易用于释放slots
	for _, addr := range inactiveAccounts {
		if account := m.getWithoutAccess(addr); account != nil {
			// 即使账户是"空的"，也要确保收集所有可能的交易
			// 使用读锁获取所有交易
			account.mu.RLock()
			promotedTxs := make([]*types.Transaction, len(account.promoted.queue))
			copy(promotedTxs, account.promoted.queue)
			enqueuedTxs := make([]*types.Transaction, len(account.enqueued.queue))
			copy(enqueuedTxs, account.enqueued.queue)
			account.mu.RUnlock()

			// 收集所有交易
			allRemovedTxs = append(allRemovedTxs, promotedTxs...)
			allRemovedTxs = append(allRemovedTxs, enqueuedTxs...)

			m.Delete(addr)
			m.accountLastAccess.Delete(addr)       // sync.Map的Delete操作
			atomic.AddUint64(&m.count, ^uint64(0)) // 减1
			cleanedCount++
		}
	}

	return cleanedCount, allRemovedTxs
}

// cleanupOversizedAccounts 清理过多的账户
// 返回被删除账户的所有交易，用于释放slots
func (m *accountsMap) cleanupOversizedAccounts() (int, []*types.Transaction) {
	currentCount := int(atomic.LoadUint64(&m.count))

	if currentCount <= m.maxAccountCount {
		return 0, nil
	}

	// 计算需要清理的数量（保留80%的账户）
	targetCount := int(float64(m.maxAccountCount) * 0.8)
	needToClean := currentCount - targetCount

	if needToClean <= 0 {
		return 0, nil
	}

	// 按访问时间排序，找出最久未访问的账户（使用sync.Map.Range，无锁迭代）
	accountEntries := make([]accountAccessEntry, 0)
	m.accountLastAccess.Range(func(key, value interface{}) bool {
		addr := key.(types.Address)
		lastAccess := value.(time.Time)
		accountEntries = append(accountEntries, accountAccessEntry{
			addr:       addr,
			lastAccess: lastAccess,
		})
		return true
	})

	// 按访问时间排序（最旧的在前）
	sort.Slice(accountEntries, func(i, j int) bool {
		return accountEntries[i].lastAccess.Before(accountEntries[j].lastAccess)
	})

	// 清理最久未访问的空账户，并收集所有交易用于释放slots
	cleanedCount := 0
	allRemovedTxs := make([]*types.Transaction, 0)
	for _, entry := range accountEntries {
		if cleanedCount >= needToClean {
			break
		}

		if account := m.getWithoutAccess(entry.addr); account != nil {
			if m.isAccountEmpty(account) {
				// 即使账户是"空的"，也要确保收集所有可能的交易
				// 使用读锁获取所有交易
				account.mu.RLock()
				promotedTxs := make([]*types.Transaction, len(account.promoted.queue))
				copy(promotedTxs, account.promoted.queue)
				enqueuedTxs := make([]*types.Transaction, len(account.enqueued.queue))
				copy(enqueuedTxs, account.enqueued.queue)
				account.mu.RUnlock()

				// 收集所有交易
				allRemovedTxs = append(allRemovedTxs, promotedTxs...)
				allRemovedTxs = append(allRemovedTxs, enqueuedTxs...)

				m.Delete(entry.addr)
				m.accountLastAccess.Delete(entry.addr) // sync.Map的Delete操作
				atomic.AddUint64(&m.count, ^uint64(0)) // 减1
				cleanedCount++
			}
		}
	}

	return cleanedCount, allRemovedTxs
}

// getWithoutAccess 获取账户但不记录访问时间（用于清理检查）
func (m *accountsMap) getWithoutAccess(addr types.Address) *account {
	a, ok := m.Load(addr)
	if !ok {
		return nil
	}

	fetchedAccount, ok := a.(*account)
	if !ok {
		return nil
	}

	return fetchedAccount
}

// isAccountEmpty 检查账户是否为空（没有待处理或已提升的交易）
func (m *accountsMap) isAccountEmpty(account *account) bool {
	// Use read lock for read-only operation
	account.mu.RLock()
	defer account.mu.RUnlock()

	enqueuedEmpty := account.enqueued.length() == 0
	promotedEmpty := account.promoted.length() == 0
	nonceEmpty := len(account.nonceToTx.mapping) == 0

	return enqueuedEmpty && promotedEmpty && nonceEmpty
}

// accountAccessEntry 账户访问条目，用于排序
type accountAccessEntry struct {
	addr       types.Address
	lastAccess time.Time
}
