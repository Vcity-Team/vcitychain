package chain

// IsTransactionsBlockListActive reports whether the transactions block list
// should be enforced at the given block.
//
// Enablement rules:
//   - params.TransactionsBlockList must be configured
//   - TransactionsAllowList takes precedence (block list ignored when allow list is set)
//   - If forks.transactionsBlockList is present, it gates activation at that height
//   - If the fork key is absent, the list is active from genesis (config-only path)
func (p *Params) IsTransactionsBlockListActive(block uint64) bool {
	if p == nil || p.TransactionsBlockList == nil {
		return false
	}

	// Allow list takes precedence over block list (same as executor run path).
	if p.TransactionsAllowList != nil {
		return false
	}

	if p.Forks == nil {
		return true
	}

	if _, exists := (*p.Forks)[TransactionsBlockList]; !exists {
		return true
	}

	return p.Forks.IsActive(TransactionsBlockList, block)
}

// TransactionsBlockListForkBlock returns the activation block when the
// transactionsBlockList fork is registered. ok is false when the fork is absent.
func (p *Params) TransactionsBlockListForkBlock() (uint64, bool) {
	if p == nil || p.Forks == nil {
		return 0, false
	}

	fork, exists := (*p.Forks)[TransactionsBlockList]
	if !exists {
		return 0, false
	}

	return fork.Block, true
}

// ShouldApplyTransactionsBlockListGenesisAllocs reports whether Admin/Enabled
// roles should be baked into genesis allocs.
//
// Live IBFT→DPoS networks enable the list via forks.transactionsBlockList.block = H
// with H > 0. Writing allocs in that case changes the genesis state root and makes
// existing nodes fail with "genesis file does not match current genesis".
// For H > 0, roles are injected at (or after) H by the executor instead.
func (p *Params) ShouldApplyTransactionsBlockListGenesisAllocs() bool {
	if p == nil || p.TransactionsBlockList == nil {
		return false
	}

	forkBlock, ok := p.TransactionsBlockListForkBlock()
	if !ok {
		// No fork key → config-only path, active from genesis.
		return true
	}

	return forkBlock == 0
}
