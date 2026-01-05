package dag

import (
	"fmt"
	"sync"

	"github.com/Vcity-Team/vcitychain/types"
)

// TransactionNode represents a node in the DAG
type TransactionNode struct {
	Tx           *types.Transaction
	Dependencies []*TransactionNode // Transactions this node depends on
	Dependents   []*TransactionNode // Transactions that depend on this node
	Level        int                // Topological level (0 = no dependencies)
	Executed     bool               // Whether this transaction has been executed
	mu           sync.RWMutex      // Protects Executed flag
}

// DAG represents a directed acyclic graph of transactions
type DAG struct {
	Nodes      []*TransactionNode
	LevelGroups map[int][]*TransactionNode // Transactions grouped by level
	mu         sync.RWMutex                // Protects LevelGroups
}

// BuildDAG builds a DAG from transactions and their dependencies
func BuildDAG(txs []*types.Transaction, dependencies DependencyMap) (*DAG, error) {
	// Create nodes for all transactions
	nodes := make(map[*types.Transaction]*TransactionNode)
	for _, tx := range txs {
		nodes[tx] = &TransactionNode{
			Tx:           tx,
			Dependencies: []*TransactionNode{},
			Dependents:   []*TransactionNode{},
			Level:        -1, // Uninitialized
			Executed:     false,
		}
	}
	
	// Build edges based on dependencies
	for tx, deps := range dependencies {
		node := nodes[tx]
		for _, depTx := range deps {
			depNode := nodes[depTx]
			if depNode == nil {
				return nil, fmt.Errorf("dependency transaction not found in transaction set")
			}
			
			// Add dependency edge
			node.Dependencies = append(node.Dependencies, depNode)
			depNode.Dependents = append(depNode.Dependents, node)
		}
	}
	
	// Topological sort to assign levels
	if err := topologicalSort(nodes); err != nil {
		return nil, fmt.Errorf("topological sort failed: %w", err)
	}
	
	// Group nodes by level
	levelGroups := groupByLevel(nodes)
	
	// Convert map to slice for easier iteration
	nodeSlice := make([]*TransactionNode, 0, len(nodes))
	for _, node := range nodes {
		nodeSlice = append(nodeSlice, node)
	}
	
	return &DAG{
		Nodes:      nodeSlice,
		LevelGroups: levelGroups,
	}, nil
}

// topologicalSort performs topological sorting and assigns levels to nodes
func topologicalSort(nodes map[*types.Transaction]*TransactionNode) error {
	// Calculate in-degree for each node
	inDegree := make(map[*TransactionNode]int)
	for _, node := range nodes {
		inDegree[node] = len(node.Dependencies)
	}
	
	// BFS to assign levels
	level := 0
	queue := []*TransactionNode{}
	
	// Start with nodes that have no dependencies (in-degree = 0)
	for _, node := range nodes {
		if inDegree[node] == 0 {
			node.Level = level
			queue = append(queue, node)
		}
	}
	
	// Process nodes level by level
	for len(queue) > 0 {
		level++
		nextQueue := []*TransactionNode{}
		
		for _, node := range queue {
			// Process all dependents of this node
			for _, dependent := range node.Dependents {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					dependent.Level = level
					nextQueue = append(nextQueue, dependent)
				}
			}
		}
		
		queue = nextQueue
	}
	
	// Check for cycles (nodes with in-degree > 0)
	for _, node := range nodes {
		if inDegree[node] > 0 {
			return fmt.Errorf("cycle detected in transaction dependencies")
		}
	}
	
	return nil
}

// groupByLevel groups nodes by their topological level
func groupByLevel(nodes map[*types.Transaction]*TransactionNode) map[int][]*TransactionNode {
	levelGroups := make(map[int][]*TransactionNode)
	
	for _, node := range nodes {
		level := node.Level
		if level < 0 {
			// Should not happen after successful topological sort
			continue
		}
		levelGroups[level] = append(levelGroups[level], node)
	}
	
	return levelGroups
}

// GetMaxLevel returns the maximum level in the DAG
func (d *DAG) GetMaxLevel() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	
	maxLevel := -1
	for level := range d.LevelGroups {
		if level > maxLevel {
			maxLevel = level
		}
	}
	return maxLevel
}

// GetNodesAtLevel returns all nodes at a specific level
func (d *DAG) GetNodesAtLevel(level int) []*TransactionNode {
	d.mu.RLock()
	defer d.mu.RUnlock()
	
	return d.LevelGroups[level]
}

// MarkExecuted marks a transaction node as executed
func (n *TransactionNode) MarkExecuted() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Executed = true
}

// IsExecuted checks if a transaction node has been executed
func (n *TransactionNode) IsExecuted() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.Executed
}

