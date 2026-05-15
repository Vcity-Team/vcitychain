package blockchain

// ExecutionMode controls whether block execution persists trie commits and consensus side effects.
type ExecutionMode uint8

const (
	// ExecutionVerify runs execution for header/body checks only (A-lite): no trie Commit, no Bolt side effects.
	ExecutionVerify ExecutionMode = iota
	// ExecutionCommit persists trie and consensus side effects (canonical write path).
	ExecutionCommit
)

func (m ExecutionMode) PersistState() bool {
	return m == ExecutionCommit
}

func (m ExecutionMode) PersistSideEffects() bool {
	return m == ExecutionCommit
}
