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
	accountLastAccess  map[types.Address]time.Time // 账户最后访问时间
	accountAccessMutex sync.RWMutex                // 访问时间锁
	maxAccountCount    int                         // 最大账户数量
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

		account.promoted.lock(false)
		defer account.promoted.unlock()

		// add head of the queue
		if tx := account.promoted.peek(); tx != nil {
			primaries = append(primaries, tx)
		}

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

		account.promoted.lock(false)
		defer account.promoted.unlock()

		total += account.promoted.length()

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

		account.promoted.lock(false)
		defer account.promoted.unlock()

		if account.promoted.length() != 0 {
			allPromoted[addr] = account.promoted.queue
		}

		if includeEnqueued {
			account.enqueued.lock(false)
			defer account.enqueued.unlock()

			if account.enqueued.length() != 0 {
				allEnqueued[addr] = account.enqueued.queue
			}
		}

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
// lock order is important! promoted.lock(true), enqueued.lock(true), nonceToTx.lock()
type account struct {
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
// After pruning, a promotion may be signaled if the first
// enqueued transaction matches the new nonce.
func (a *account) reset(nonce uint64, promoteCh chan<- promoteRequest, addr types.Address, logger hclog.Logger) (
	prunedPromoted,
	prunedEnqueued []*types.Transaction,
) {
	oldNonce := a.getNonce()
	if logger != nil {
		logger.Debug("🔵 [account.reset] 开始重置账户",
			"addr", addr.String()[:16],
			"oldNonce", oldNonce,
			"newNonce", nonce)
	}

	a.promoted.lock(true)
	a.enqueued.lock(true)
	a.nonceToTx.lock()

	defer func() {
		a.nonceToTx.unlock()
		a.enqueued.unlock()
		a.promoted.unlock()
	}()

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

	// 当 newNonce <= oldNonce 时（例如：reorg 导致链上 nonce 回退，或初始化时状态不一致）
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

		return
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

	// it is important to signal promotion while
	// the locks are held to ensure no other
	// handler will mutate the account
	if first := a.enqueued.peek(); first != nil && first.Nonce == nonce {
		// first enqueued tx is expected -> signal promotion
		if logger != nil {
			logger.Info("🔵 [account.reset] 触发promotion信号",
				"addr", addr.String()[:16],
				"firstTxNonce", first.Nonce,
				"newNonce", nonce)
		}
		promoteCh <- promoteRequest{account: first.From}
	}

	if logger != nil {
		logger.Info("🔵 [account.reset] 重置完成",
			"addr", addr.String()[:16],
			"oldNonce", oldNonce,
			"newNonce", nonce,
			"prunedPromoted", len(prunedPromoted),
			"prunedEnqueued", len(prunedEnqueued))
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

// Promote moves eligible transactions from enqueued to promoted.
//
// Eligible transactions are all sequential in order of nonce
// and the first one has to have nonce less (or equal) to the account's
// nextNonce.
func (a *account) promote() (promoted []*types.Transaction, pruned []*types.Transaction) {
	a.promoted.lock(true)
	a.enqueued.lock(true)

	defer func() {
		a.enqueued.unlock()
		a.promoted.unlock()
	}()

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

	a.nonceToTx.lock()
	a.nonceToTx.remove(pruned...)
	a.nonceToTx.unlock()

	return
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
	a.promoted.lock(true)
	defer a.promoted.unlock()

	if firstPromoted := a.promoted.peek(); firstPromoted != nil {
		return firstPromoted
	}

	a.enqueued.lock(true)
	defer a.enqueued.unlock()

	if firstEnqueued := a.enqueued.peek(); firstEnqueued != nil {
		return firstEnqueued
	}

	return nil
}

// recordAccess 记录账户访问时间
func (m *accountsMap) recordAccess(addr types.Address) {
	m.accountAccessMutex.Lock()
	defer m.accountAccessMutex.Unlock()

	if m.accountLastAccess == nil {
		m.accountLastAccess = make(map[types.Address]time.Time)
	}
	m.accountLastAccess[addr] = time.Now()
}

// cleanupInactiveAccounts 清理不活跃的账户
func (m *accountsMap) cleanupInactiveAccounts(maxAge time.Duration) int {
	m.accountAccessMutex.Lock()
	defer m.accountAccessMutex.Unlock()

	if m.accountLastAccess == nil {
		return 0
	}

	now := time.Now()
	cleanedCount := 0
	inactiveAccounts := make([]types.Address, 0)

	// 收集不活跃的账户
	for addr, lastAccess := range m.accountLastAccess {
		if now.Sub(lastAccess) > maxAge {
			// 检查账户是否为空（没有待处理或已提升的交易）
			if account := m.getWithoutAccess(addr); account != nil {
				if m.isAccountEmpty(account) {
					inactiveAccounts = append(inactiveAccounts, addr)
				}
			}
		}
	}

	// 删除不活跃的空账户
	for _, addr := range inactiveAccounts {
		m.Delete(addr)
		delete(m.accountLastAccess, addr)
		atomic.AddUint64(&m.count, ^uint64(0)) // 减1
		cleanedCount++
	}

	return cleanedCount
}

// cleanupOversizedAccounts 清理过多的账户
func (m *accountsMap) cleanupOversizedAccounts() int {
	currentCount := int(atomic.LoadUint64(&m.count))

	if currentCount <= m.maxAccountCount {
		return 0
	}

	// 计算需要清理的数量（保留80%的账户）
	targetCount := int(float64(m.maxAccountCount) * 0.8)
	needToClean := currentCount - targetCount

	if needToClean <= 0 {
		return 0
	}

	m.accountAccessMutex.RLock()
	// 按访问时间排序，找出最久未访问的账户
	accountEntries := make([]accountAccessEntry, 0, len(m.accountLastAccess))
	for addr, lastAccess := range m.accountLastAccess {
		accountEntries = append(accountEntries, accountAccessEntry{
			addr:       addr,
			lastAccess: lastAccess,
		})
	}
	m.accountAccessMutex.RUnlock()

	// 按访问时间排序（最旧的在前）
	sort.Slice(accountEntries, func(i, j int) bool {
		return accountEntries[i].lastAccess.Before(accountEntries[j].lastAccess)
	})

	// 清理最久未访问的空账户
	cleanedCount := 0
	for _, entry := range accountEntries {
		if cleanedCount >= needToClean {
			break
		}

		if account := m.getWithoutAccess(entry.addr); account != nil {
			if m.isAccountEmpty(account) {
				m.Delete(entry.addr)
				m.accountAccessMutex.Lock()
				delete(m.accountLastAccess, entry.addr)
				m.accountAccessMutex.Unlock()
				atomic.AddUint64(&m.count, ^uint64(0)) // 减1
				cleanedCount++
			}
		}
	}

	return cleanedCount
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
	// 检查待处理队列
	account.enqueued.lock(false)
	enqueuedEmpty := account.enqueued.length() == 0
	account.enqueued.unlock()

	// 检查已提升队列
	account.promoted.lock(false)
	promotedEmpty := account.promoted.length() == 0
	account.promoted.unlock()

	// 检查nonce映射
	account.nonceToTx.lock()
	nonceEmpty := len(account.nonceToTx.mapping) == 0
	account.nonceToTx.unlock()

	return enqueuedEmpty && promotedEmpty && nonceEmpty
}

// accountAccessEntry 账户访问条目，用于排序
type accountAccessEntry struct {
	addr       types.Address
	lastAccess time.Time
}
