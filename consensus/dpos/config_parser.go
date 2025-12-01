package dpos

import (
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// ConfigParser 配置解析器，提供统一的配置解析工具
type ConfigParser struct {
	config map[string]interface{}
	logger hclog.Logger
}

// NewConfigParser 创建配置解析器
func NewConfigParser(config map[string]interface{}, logger hclog.Logger) *ConfigParser {
	return &ConfigParser{
		config: config,
		logger: logger,
	}
}

// GetUint64 获取 uint64 类型配置值，支持多个键名（驼峰和下划线）
func (p *ConfigParser) GetUint64(keys ...string) (uint64, bool) {
	val, exists := p.getValue(keys...)
	if !exists {
		return 0, false
	}

	switch v := val.(type) {
	case uint64:
		return v, true
	case int:
		if v < 0 {
			p.logger.Warn("配置值不能为负数", "keys", keys, "value", v)
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			p.logger.Warn("配置值不能为负数", "keys", keys, "value", v)
			return 0, false
		}
		return uint64(v), true
	case float64:
		if v < 0 {
			p.logger.Warn("配置值不能为负数", "keys", keys, "value", v)
			return 0, false
		}
		return uint64(v), true
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			p.logger.Warn("配置值解析失败", "keys", keys, "value", v, "error", err)
			return 0, false
		}
		return parsed, true
	default:
		p.logger.Warn("配置值类型不支持", "keys", keys, "type", fmt.Sprintf("%T", val))
		return 0, false
	}
}

// GetDuration 获取 time.Duration 类型配置值，支持多个键名
func (p *ConfigParser) GetDuration(keys ...string) (time.Duration, bool) {
	val, exists := p.getValue(keys...)
	if !exists {
		return 0, false
	}

	switch v := val.(type) {
	case time.Duration:
		return v, true
	case string:
		duration, err := parseDurationAllowDays(v)
		if err != nil {
			p.logger.Warn("Duration 字符串解析失败", "keys", keys, "value", v, "error", err)
			return 0, false
		}
		return duration, true
	case float64:
		if v > 0 {
			return time.Duration(v) * time.Second, true
		}
		return 0, false
	case int64:
		if v > 0 {
			return time.Duration(v) * time.Second, true
		}
		return 0, false
	default:
		p.logger.Warn("Duration 配置值类型不支持", "keys", keys, "type", fmt.Sprintf("%T", val))
		return 0, false
	}
}

// GetAddress 获取 types.Address 类型配置值
func (p *ConfigParser) GetAddress(keys ...string) (types.Address, bool) {
	val, exists := p.getValue(keys...)
	if !exists {
		return types.Address{}, false
	}

	switch v := val.(type) {
	case types.Address:
		return v, true
	case string:
		addr := types.StringToAddress(v)
		return addr, true
	default:
		p.logger.Warn("Address 配置值类型不支持", "keys", keys, "type", fmt.Sprintf("%T", val))
		return types.Address{}, false
	}
}

// GetBigInt 获取 *big.Int 类型配置值
func (p *ConfigParser) GetBigInt(keys ...string) (*big.Int, bool) {
	val, exists := p.getValue(keys...)
	if !exists {
		return nil, false
	}

	switch v := val.(type) {
	case *big.Int:
		return v, true
	default:
		p.logger.Warn("BigInt 配置值类型不支持", "keys", keys, "type", fmt.Sprintf("%T", val))
		return nil, false
	}
}

// GetValue 获取原始配置值
func (p *ConfigParser) GetValue(keys ...string) (interface{}, bool) {
	return p.getValue(keys...)
}

// getValue 内部方法，支持多个键名查找
func (p *ConfigParser) getValue(keys ...string) (interface{}, bool) {
	for _, key := range keys {
		if val, ok := p.config[key]; ok {
			return val, true
		}
	}
	return nil, false
}

// SetUint64 设置 uint64 配置值（用于测试或动态配置）
func (p *ConfigParser) SetUint64(key string, value uint64) {
	p.config[key] = value
}

// SetDuration 设置 Duration 配置值（用于测试或动态配置）
func (p *ConfigParser) SetDuration(key string, value time.Duration) {
	p.config[key] = value
}

