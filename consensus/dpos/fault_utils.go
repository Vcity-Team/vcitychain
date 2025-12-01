package dpos

import (
	"time"
)

// getConfigUint64 从 rawConfig 读取 uint64 配置值
func (d *DPoS) getConfigUint64(keys ...string) uint64 {
	if d.rawConfig == nil {
		return 0
	}
	for _, key := range keys {
		if val, ok := d.rawConfig[key]; ok {
			switch v := val.(type) {
			case uint64:
				return v
			case int:
				if v >= 0 {
					return uint64(v)
				}
			case int64:
				if v >= 0 {
					return uint64(v)
				}
			case float64:
				if v >= 0 {
					return uint64(v)
				}
			}
		}
	}
	return 0
}

// getMissedBlocksPercentage 获取漏块率阈值（基点）
func (d *DPoS) getMissedBlocksPercentage() uint64 {
	return d.getParameterUint64WithLog(
		"dpos_missed_blocks_percentage",
		[]string{"dpos_missed_blocks_percentage", "missed_blocks_percentage"},
		1000, // 默认值 10%
		"missed blocks percentage",
	)
}

// getMinorOffenseSlashRate 获取轻度违规削减率（基点）
func (d *DPoS) getMinorOffenseSlashRate() uint64 {
	return d.getParameterUint64WithLog(
		"dpos_minor_offense_slash_rate",
		[]string{"dpos_minor_offense_slash_rate", "minor_offense_slash_rate"},
		50, // 默认值 0.5%
		"minor offense slash rate",
	)
}

// getSevereOffenseSlashRate 获取严重违规削减率（基点）
func (d *DPoS) getSevereOffenseSlashRate() uint64 {
	return d.getParameterUint64WithLog(
		"dpos_severe_offense_slash_rate",
		[]string{"dpos_severe_offense_slash_rate", "severe_offense_slash_rate"},
		1000, // 默认值 10%
		"severe offense slash rate",
	)
}

// logOnceWithInterval 按时间间隔限制日志输出频率
func (d *DPoS) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	d.logMutex.Lock()
	defer d.logMutex.Unlock()

	now := time.Now()
	if lastTime, exists := d.lastLogTime[key]; exists {
		if now.Sub(lastTime) < interval {
			return
		}
	}
	d.lastLogTime[key] = now
	switch level {
	case "debug":
		d.logger.Debug(message, args...)
	case "info":
		d.logger.Info(message, args...)
	case "warn":
		d.logger.Warn(message, args...)
	case "error":
		d.logger.Error(message, args...)
	default:
		d.logger.Info(message, args...)
	}
}

