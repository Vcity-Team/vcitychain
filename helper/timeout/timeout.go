package timeout

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"
)

// TimeoutManager 超时管理器
type TimeoutManager struct {
	logger hclog.Logger
}

// NewTimeoutManager 创建超时管理器
func NewTimeoutManager(logger hclog.Logger) *TimeoutManager {
	return &TimeoutManager{
		logger: logger.Named("timeout-manager"),
	}
}

// WithTimeout 为操作添加超时控制
func (tm *TimeoutManager) WithTimeout(ctx context.Context, timeout time.Duration, operation string, fn func(context.Context) error) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- fn(timeoutCtx)
	}()

	select {
	case err := <-done:
		return err
	case <-timeoutCtx.Done():
		tm.logger.Warn("操作超时",
			"operation", operation,
			"timeout", timeout,
			"error", timeoutCtx.Err())
		return timeoutCtx.Err()
	}
}

// WithTimeoutAndRetry 为操作添加超时控制和重试机制
func (tm *TimeoutManager) WithTimeoutAndRetry(ctx context.Context, timeout time.Duration, maxRetries int, operation string, fn func(context.Context) error) error {
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := tm.WithTimeout(ctx, timeout, operation, fn)
		if err == nil {
			return nil
		}

		lastErr = err
		tm.logger.Debug("操作失败，准备重试",
			"operation", operation,
			"attempt", i+1,
			"maxRetries", maxRetries,
			"error", err)

		// 指数退避
		backoff := time.Duration(1<<uint(i)) * time.Second
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}

	tm.logger.Error("操作最终失败",
		"operation", operation,
		"maxRetries", maxRetries,
		"lastError", lastErr)

	return lastErr
}

// WithDeadline 为操作添加截止时间控制
func (tm *TimeoutManager) WithDeadline(ctx context.Context, deadline time.Time, operation string, fn func(context.Context) error) error {
	deadlineCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- fn(deadlineCtx)
	}()

	select {
	case err := <-done:
		return err
	case <-deadlineCtx.Done():
		tm.logger.Warn("操作超时（截止时间）",
			"operation", operation,
			"deadline", deadline,
			"error", deadlineCtx.Err())
		return deadlineCtx.Err()
	}
}

// SafeExecute 安全执行操作，包含panic恢复
func (tm *TimeoutManager) SafeExecute(ctx context.Context, timeout time.Duration, operation string, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			tm.logger.Error("操作发生panic",
				"operation", operation,
				"panic", r)
			err = context.DeadlineExceeded
		}
	}()

	return tm.WithTimeout(ctx, timeout, operation, fn)
}

// DefaultTimeouts 默认超时配置
var DefaultTimeouts = struct {
	Short  time.Duration // 短操作超时
	Medium time.Duration // 中等操作超时
	Long   time.Duration // 长操作超时
}{
	Short:  5 * time.Second,
	Medium: 30 * time.Second,
	Long:   5 * time.Minute,
}

