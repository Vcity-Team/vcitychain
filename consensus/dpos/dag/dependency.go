package dag

import (
	"strings"

	"github.com/Vcity-Team/vcitychain/types"
)

// SameToMode controls how strictly "same To address" creates a dependency edge.
type SameToMode string

const (
	// SameToModeStrict: any two txs with the same To are dependent (safest / legacy).
	SameToModeStrict SameToMode = "strict"
	// SameToModeRelaxed: same To only serializes when either tx looks like a contract
	// call/create (has calldata or is create). Pure value transfers to the same
	// recipient may run in parallel.
	SameToModeRelaxed SameToMode = "relaxed"
	// SameToModeAggressive: currently same as relaxed; reserved for storage-set deps.
	SameToModeAggressive SameToMode = "aggressive"
)

// Legacy aliases kept for older configs / code.
const (
	DependencyModeConservative = SameToModeStrict
	DependencyModeBalanced     = SameToModeRelaxed
	DependencyModeAggressive   = SameToModeAggressive
)

// DependencyMode is an alias of SameToMode (historical name).
type DependencyMode = SameToMode

// ParseSameToMode normalizes config strings; unknown values fall back to strict.
// Accepts new names (strict/relaxed) and legacy names (conservative/balanced).
func ParseSameToMode(s string) SameToMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(SameToModeStrict), "conservative", "legacy":
		return SameToModeStrict
	case string(SameToModeRelaxed), "balanced":
		return SameToModeRelaxed
	case string(SameToModeAggressive):
		return SameToModeAggressive
	default:
		return SameToModeStrict
	}
}

// ParseDependencyMode is the legacy name of ParseSameToMode.
func ParseDependencyMode(s string) DependencyMode {
	return ParseSameToMode(s)
}

// DependencyAnalyzer analyzes transaction dependencies
type DependencyAnalyzer struct {
	mode SameToMode
	logger interface {
		Debug(msg string, args ...interface{})
		Info(msg string, args ...interface{})
	}
}

// NewDependencyAnalyzer creates a new dependency analyzer (strict same-To mode).
func NewDependencyAnalyzer(logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
}) *DependencyAnalyzer {
	return NewDependencyAnalyzerWithMode(SameToModeStrict, logger)
}

// NewDependencyAnalyzerWithMode creates an analyzer with an explicit same-To mode.
func NewDependencyAnalyzerWithMode(mode SameToMode, logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
}) *DependencyAnalyzer {
	if mode == "" {
		mode = SameToModeStrict
	}
	return &DependencyAnalyzer{
		mode:   mode,
		logger: logger,
	}
}

// DependencyMap represents transaction dependencies
// Key: transaction, Value: list of transactions this transaction depends on
type DependencyMap map[*types.Transaction][]*types.Transaction

// DependencyStats summarizes edge sources for observability.
type DependencyStats struct {
	Mode           string
	TxCount        int
	NonceEdges     int
	TransferEdges  int
	SameToEdges    int
	TotalDepEdges  int
	DependentTxs   int
	IndependentTxs int
}

// DetectDependencies detects dependencies between transactions using static analysis.
func (d *DependencyAnalyzer) DetectDependencies(txs []*types.Transaction) DependencyMap {
	deps, _ := d.DetectDependenciesWithStats(txs)
	return deps
}

// DetectDependenciesWithStats is DetectDependencies plus edge counters.
func (d *DependencyAnalyzer) DetectDependenciesWithStats(txs []*types.Transaction) (DependencyMap, DependencyStats) {
	stats := DependencyStats{
		Mode:    string(d.mode),
		TxCount: len(txs),
	}
	deps := make(DependencyMap)

	for _, tx := range txs {
		deps[tx] = []*types.Transaction{}
	}

	for i, tx1 := range txs {
		for j, tx2 := range txs {
			if i == j {
				continue
			}

			// Rule 1: Nonce dependency (same account, nonce ordering)
			if tx1.From == tx2.From && tx1.Nonce < tx2.Nonce {
				deps[tx2] = append(deps[tx2], tx1)
				stats.NonceEdges++
				continue
			}

			// Rule 2: Balance / transfer chain (tx1 pays into tx2.From)
			if tx1.To != nil && *tx1.To == tx2.From {
				deps[tx2] = append(deps[tx2], tx1)
				stats.TransferEdges++
				continue
			}

			// Rule 3: same-To dependency (mode-dependent)
			if tx1.To != nil && tx2.To != nil && *tx1.To == *tx2.To && j > i {
				if d.shouldAddSameToEdge(tx1, tx2) {
					deps[tx2] = append(deps[tx2], tx1)
					stats.SameToEdges++
				}
			}
		}
	}

	// Remove duplicates from dependency lists
	for tx, depList := range deps {
		uniqueDeps := make(map[*types.Transaction]bool)
		filteredDeps := make([]*types.Transaction, 0, len(depList))
		for _, dep := range depList {
			if !uniqueDeps[dep] {
				uniqueDeps[dep] = true
				filteredDeps = append(filteredDeps, dep)
			}
		}
		deps[tx] = filteredDeps
		stats.TotalDepEdges += len(filteredDeps)
		if len(filteredDeps) > 0 {
			stats.DependentTxs++
		} else {
			stats.IndependentTxs++
		}
	}

	return deps, stats
}

func (d *DependencyAnalyzer) shouldAddSameToEdge(tx1, tx2 *types.Transaction) bool {
	switch d.mode {
	case SameToModeRelaxed, SameToModeAggressive:
		// Pure value transfers to the same recipient do not conflict with each other
		// (final balances commute). Contract calls/creates still serialize on same To.
		return looksLikeContractInteraction(tx1) || looksLikeContractInteraction(tx2)
	default: // strict
		return true
	}
}

func looksLikeContractInteraction(tx *types.Transaction) bool {
	if tx == nil {
		return false
	}
	// Contract create
	if tx.To == nil {
		return true
	}
	// Any calldata ⇒ treat as contract call (static; no state lookup)
	return len(tx.Input) > 0
}

// HasDependencies checks if there are any dependencies in the transaction set
func (d *DependencyAnalyzer) HasDependencies(deps DependencyMap) bool {
	for _, depList := range deps {
		if len(depList) > 0 {
			return true
		}
	}
	return false
}
