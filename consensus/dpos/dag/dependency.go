package dag

import (
	"github.com/Vcity-Team/vcitychain/types"
)

// DependencyAnalyzer analyzes transaction dependencies
type DependencyAnalyzer struct {
	logger interface {
		Debug(msg string, args ...interface{})
		Info(msg string, args ...interface{})
	}
}

// NewDependencyAnalyzer creates a new dependency analyzer
func NewDependencyAnalyzer(logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
}) *DependencyAnalyzer {
	return &DependencyAnalyzer{
		logger: logger,
	}
}

// DependencyMap represents transaction dependencies
// Key: transaction, Value: list of transactions this transaction depends on
type DependencyMap map[*types.Transaction][]*types.Transaction

// DetectDependencies detects dependencies between transactions using static analysis
// Stage 3: DAG dependency detection
func (d *DependencyAnalyzer) DetectDependencies(txs []*types.Transaction) DependencyMap {
	deps := make(DependencyMap)
	
	// Initialize: no dependencies for now
	for _, tx := range txs {
		deps[tx] = []*types.Transaction{}
	}
	
	// Static analysis: detect dependencies based on transaction data
	for i, tx1 := range txs {
		for j, tx2 := range txs {
			if i == j {
				continue
			}
			
			// Rule 1: Nonce dependency (same account, nonce ordering)
			// tx2 depends on tx1 if they're from the same account and tx1 has lower nonce
			if tx1.From == tx2.From && tx1.Nonce < tx2.Nonce {
				deps[tx2] = append(deps[tx2], tx1)
				continue
			}
			
			// Rule 2: Balance dependency (tx2 receives from tx1)
			// If tx1 sends to tx2's account, tx2 depends on tx1
			if tx1.To != nil && *tx1.To == tx2.From {
				deps[tx2] = append(deps[tx2], tx1)
				continue
			}
			
			// Rule 3: Contract storage dependency (conservative approach)
			// If both transactions interact with the same contract, assume dependency
			// This is conservative - we assume dependency to ensure correctness
			if tx1.To != nil && tx2.To != nil && *tx1.To == *tx2.To {
				// Only add dependency if tx2 comes after tx1 in the original order
				// This prevents circular dependencies
				if j > i {
					deps[tx2] = append(deps[tx2], tx1)
				}
			}
		}
	}
	
	// Remove duplicates from dependency lists
	for tx, depList := range deps {
		uniqueDeps := make(map[*types.Transaction]bool)
		var filteredDeps []*types.Transaction
		for _, dep := range depList {
			if !uniqueDeps[dep] {
				uniqueDeps[dep] = true
				filteredDeps = append(filteredDeps, dep)
			}
		}
		deps[tx] = filteredDeps
	}
	
	return deps
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

