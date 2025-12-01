package dpos

import (
	"fmt"

	"github.com/hashicorp/go-hclog"
)

// loggerWrapper 日志包装器，统一处理 logger 可能为 nil 的情况
type loggerWrapper struct {
	logger hclog.Logger
}

// newLoggerWrapper 创建日志包装器
func newLoggerWrapper(logger hclog.Logger) *loggerWrapper {
	return &loggerWrapper{logger: logger}
}

// Debug 记录调试日志
func (lw *loggerWrapper) Debug(msg string, args ...interface{}) {
	if lw.logger != nil {
		lw.logger.Debug(msg, args...)
	} else {
		lw.printLog("DEBUG", msg, args...)
	}
}

// Info 记录信息日志
func (lw *loggerWrapper) Info(msg string, args ...interface{}) {
	if lw.logger != nil {
		lw.logger.Info(msg, args...)
	} else {
		lw.printLog("INFO", msg, args...)
	}
}

// Warn 记录警告日志
func (lw *loggerWrapper) Warn(msg string, args ...interface{}) {
	if lw.logger != nil {
		lw.logger.Warn(msg, args...)
	} else {
		lw.printLog("WARN", msg, args...)
	}
}

// Error 记录错误日志
func (lw *loggerWrapper) Error(msg string, args ...interface{}) {
	if lw.logger != nil {
		lw.logger.Error(msg, args...)
	} else {
		lw.printLog("ERROR", msg, args...)
	}
}

// Trace 记录跟踪日志
func (lw *loggerWrapper) Trace(msg string, args ...interface{}) {
	if lw.logger != nil {
		lw.logger.Trace(msg, args...)
	} else {
		lw.printLog("TRACE", msg, args...)
	}
}

// printLog 当 logger 为 nil 时使用 fmt.Printf 输出日志
func (lw *loggerWrapper) printLog(level, msg string, args ...interface{}) {
	fmt.Printf("[%s] %s", level, msg)
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			fmt.Printf(" %v=%v", args[i], args[i+1])
		} else {
			fmt.Printf(" %v", args[i])
		}
	}
	fmt.Println()
}

// Named 创建子 logger
func (lw *loggerWrapper) Named(name string) *loggerWrapper {
	if lw.logger != nil {
		return newLoggerWrapper(lw.logger.Named(name))
	}
	return lw
}

