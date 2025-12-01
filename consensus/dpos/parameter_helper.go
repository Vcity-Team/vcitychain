package dpos

import (
	"strconv"
)

// getParameterUint64 从参数系统获取 uint64 类型参数值
// 优先级：参数系统 > 配置值 > 默认值
func (d *DPoS) getParameterUint64(paramName string, configValue uint64, defaultValue uint64) uint64 {
	// 优先从参数系统读取（经过治理流程修改的值是权威数据源）
	if paramValue, err := d.getCurrentParameterValue(paramName); err == nil {
		switch v := paramValue.(type) {
		case uint64:
			if v > 0 {
				return v
			}
		case int64:
			if v > 0 {
				return uint64(v)
			}
		case float64:
			if v > 0 {
				return uint64(v)
			}
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil && parsed > 0 {
				return parsed
			}
		}
	}

	// 如果参数系统没有值，使用配置值
	if configValue > 0 {
		return configValue
	}

	// 使用默认值
	return defaultValue
}
