package txpool

import (
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// validatedTxCache caches validated transaction signatures to avoid repeated ECDSA recovery
type validatedTxCache struct {
	cache   map[types.Hash]bool
	mu      sync.RWMutex
	maxSize int
}

func newValidatedTxCache(maxSize int) *validatedTxCache {
	return &validatedTxCache{
		cache:   make(map[types.Hash]bool),
		maxSize: maxSize,
	}
}

func (c *validatedTxCache) has(hash types.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache[hash]
}

func (c *validatedTxCache) add(hash types.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple eviction: if cache is full, clear half of it
	if len(c.cache) >= c.maxSize {
		// Clear half of the cache (simple strategy)
		cleared := 0
		target := len(c.cache) / 2
		for k := range c.cache {
			if cleared >= target {
				break
			}
			delete(c.cache, k)
			cleared++
		}
	}

	c.cache[hash] = true
}

func (c *validatedTxCache) remove(hash types.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, hash)
}

func (c *validatedTxCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[types.Hash]bool)
}

// balanceCache caches account balances with TTL to reduce state queries
type balanceCache struct {
	cache map[types.Address]*cachedBalance
	mu    sync.RWMutex
	ttl   time.Duration
}

type cachedBalance struct {
	balance *big.Int
	expires time.Time
}

func newBalanceCache(ttl time.Duration) *balanceCache {
	return &balanceCache{
		cache: make(map[types.Address]*cachedBalance),
		ttl:   ttl,
	}
}

func (c *balanceCache) get(addr types.Address) (*big.Int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cached, ok := c.cache[addr]
	if !ok {
		return nil, false
	}

	if time.Now().After(cached.expires) {
		return nil, false
	}

	return new(big.Int).Set(cached.balance), true
}

func (c *balanceCache) set(addr types.Address, balance *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache[addr] = &cachedBalance{
		balance: new(big.Int).Set(balance),
		expires: time.Now().Add(c.ttl),
	}
}

func (c *balanceCache) remove(addr types.Address) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, addr)
}

func (c *balanceCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[types.Address]*cachedBalance)
}

