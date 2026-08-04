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
