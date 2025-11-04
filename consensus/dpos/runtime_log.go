package dpos

import (
	"time"
)

// logOnce 防重复日志函数（支持自定义间隔）
func (r *dposRuntime) logOnce(key string, level string, message string, args ...interface{}) {
	if r.resourceMonitor != nil {
		r.resourceMonitor.logOnceWithInterval(key, 10*time.Second, level, message, args...)
	} else {
		// 如果resourceMonitor未初始化，直接使用logger
		switch level {
		case "debug":
			r.logger.Debug(message, args...)
		case "info":
			r.logger.Info(message, args...)
		case "warn":
			r.logger.Warn(message, args...)
		case "error":
			r.logger.Error(message, args...)
		default:
			r.logger.Info(message, args...)
		}
	}
}

// logOnceWithInterval 防重复日志函数（自定义间隔）
func (r *dposRuntime) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	if r.resourceMonitor != nil {
		r.resourceMonitor.logOnceWithInterval(key, interval, level, message, args...)
	} else {
		// 如果resourceMonitor未初始化，直接使用logger
		switch level {
		case "debug":
			r.logger.Debug(message, args...)
		case "info":
			r.logger.Info(message, args...)
		case "warn":
			r.logger.Warn(message, args...)
		case "error":
			r.logger.Error(message, args...)
		default:
			r.logger.Info(message, args...)
		}
	}
}




