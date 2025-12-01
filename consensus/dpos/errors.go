package dpos

import "fmt"

// 定义常见的错误类型
var (
	// ErrModuleNotInitialized 模块未初始化错误
	ErrModuleNotInitialized = fmt.Errorf("module not initialized")
	
	// ErrRuntimeNotInitialized runtime未初始化错误
	ErrRuntimeNotInitialized = fmt.Errorf("runtime not initialized")
	
	// ErrBlockchainNotAvailable 区块链不可用错误
	ErrBlockchainNotAvailable = fmt.Errorf("blockchain not available")
	
	// ErrBlockchainConfigNotInitialized 区块链配置未初始化错误
	ErrBlockchainConfigNotInitialized = fmt.Errorf("blockchain config not initialized")
	
	// ErrParentHeaderNotFound 父区块头未找到错误
	ErrParentHeaderNotFound = fmt.Errorf("parent header not found")
	
	// ErrStakeStoreNotAvailable 质押存储不可用错误
	ErrStakeStoreNotAvailable = fmt.Errorf("stake store not available")
	
	// ErrDatabaseTransactionRequired 需要数据库事务错误
	ErrDatabaseTransactionRequired = fmt.Errorf("database transaction is required")
	
	// ErrDelegatesSetEmpty 受托人集合为空错误
	ErrDelegatesSetEmpty = fmt.Errorf("delegates set is empty")
	
	// ErrConfigNotInitialized 配置未初始化错误
	ErrConfigNotInitialized = fmt.Errorf("config not initialized")
	
	// ErrStateNotInitialized 状态未初始化错误
	ErrStateNotInitialized = fmt.Errorf("state not initialized")
)

// WrapError 统一错误包装，添加操作上下文
func WrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// WrapErrorf 统一错误包装，支持格式化消息
func WrapErrorf(operation string, format string, args ...interface{}) error {
	return fmt.Errorf("%s: %s", operation, fmt.Sprintf(format, args...))
}

