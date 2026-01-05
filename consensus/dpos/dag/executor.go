package dag

import (
	"fmt"
	"sync"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// DAGExecutor executes transactions based on DAG dependencies
type DAGExecutor struct {
	transition *state.Transition
	logger     hclog.Logger
}

// NewDAGExecutor creates a new DAG executor
func NewDAGExecutor(transition *state.Transition, logger hclog.Logger) *DAGExecutor {
	return &DAGExecutor{
		transition: transition,
		logger:     logger,
	}
}

// ExecuteDAG executes transactions in parallel based on DAG levels
// Transactions at the same level can be executed in parallel
// Transactions at different levels must be executed sequentially
func (e *DAGExecutor) ExecuteDAG(dag *DAG) error {
	maxLevel := dag.GetMaxLevel()
	
	if maxLevel < 0 {
		return fmt.Errorf("invalid DAG: no levels found")
	}
	
	e.logger.Debug("🚀 [DAGExecutor] 开始执行 DAG",
		"maxLevel", maxLevel,
		"totalNodes", len(dag.Nodes))
	
	// Execute level by level
	for level := 0; level <= maxLevel; level++ {
		nodesAtLevel := dag.GetNodesAtLevel(level)
		
		if len(nodesAtLevel) == 0 {
			continue
		}
		
		e.logger.Debug("📊 [DAGExecutor] 执行层级",
			"level", level,
			"nodeCount", len(nodesAtLevel))
		
		// Execute all transactions at this level in parallel
		var wg sync.WaitGroup
		errCh := make(chan error, len(nodesAtLevel))
		
		for _, node := range nodesAtLevel {
			wg.Add(1)
			go func(n *TransactionNode) {
				defer wg.Done()
				
				// Check if all dependencies are executed
				allDepsExecuted := true
				for _, dep := range n.Dependencies {
					if !dep.IsExecuted() {
						allDepsExecuted = false
						break
					}
				}
				
				if !allDepsExecuted {
					errCh <- fmt.Errorf("transaction %s dependencies not all executed", n.Tx.Hash.String())
					return
				}
				
				// Execute the transaction
				if err := e.transition.Write(n.Tx); err != nil {
					errCh <- fmt.Errorf("transaction %s execution failed: %w", n.Tx.Hash.String(), err)
					return
				}
				
				// Mark as executed
				n.MarkExecuted()
				
				e.logger.Debug("✅ [DAGExecutor] 交易执行成功",
					"level", level,
					"txHash", n.Tx.Hash.String()[:16],
					"from", n.Tx.From.String()[:16],
					"nonce", n.Tx.Nonce)
			}(node)
		}
		
		// Wait for all transactions at this level to complete
		wg.Wait()
		close(errCh)
		
		// Check for errors
		for err := range errCh {
			if err != nil {
				e.logger.Error("❌ [DAGExecutor] 层级执行失败",
					"level", level,
					"error", err)
				return err
			}
		}
		
		e.logger.Debug("✅ [DAGExecutor] 层级执行完成",
			"level", level,
			"nodeCount", len(nodesAtLevel))
	}
	
	e.logger.Debug("🎉 [DAGExecutor] DAG 执行完成",
		"totalLevels", maxLevel+1,
		"totalNodes", len(dag.Nodes))
	
	return nil
}

// ExecuteDAGWithAccountGrouping combines DAG execution with account grouping
// This is a hybrid approach: use DAG for dependency detection, but group by account for execution
func (e *DAGExecutor) ExecuteDAGWithAccountGrouping(dag *DAG) error {
	// Group nodes by account within each level
	maxLevel := dag.GetMaxLevel()
	
	for level := 0; level <= maxLevel; level++ {
		nodesAtLevel := dag.GetNodesAtLevel(level)
		
		if len(nodesAtLevel) == 0 {
			continue
		}
		
		// Group by account
		accountGroups := groupNodesByAccount(nodesAtLevel)
		
		// Execute different accounts in parallel
		var wg sync.WaitGroup
		errCh := make(chan error, len(accountGroups))
		
		for account, nodes := range accountGroups {
			wg.Add(1)
			go func(addr types.Address, accountNodes []*TransactionNode) {
				defer wg.Done()
				
				// Execute transactions from this account serially (by nonce order)
				for _, node := range accountNodes {
					// Check dependencies
					allDepsExecuted := true
					for _, dep := range node.Dependencies {
						if !dep.IsExecuted() {
							allDepsExecuted = false
							break
						}
					}
					
					if !allDepsExecuted {
						errCh <- fmt.Errorf("transaction %s dependencies not executed", node.Tx.Hash.String())
						return
					}
					
					// Execute
					if err := e.transition.WriteForAccount(node.Tx, addr); err != nil {
						errCh <- fmt.Errorf("transaction %s execution failed: %w", node.Tx.Hash.String(), err)
						return
					}
					
					node.MarkExecuted()
				}
			}(account, nodes)
		}
		
		wg.Wait()
		close(errCh)
		
		for err := range errCh {
			if err != nil {
				return err
			}
		}
	}
	
	return nil
}

// groupNodesByAccount groups transaction nodes by sender address
func groupNodesByAccount(nodes []*TransactionNode) map[types.Address][]*TransactionNode {
	groups := make(map[types.Address][]*TransactionNode)
	
	for _, node := range nodes {
		groups[node.Tx.From] = append(groups[node.Tx.From], node)
	}
	
	return groups
}

