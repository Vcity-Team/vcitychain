package dpos

import (
	"errors"
	"time"
)

// NetworkConfig DPoS网络配置
type NetworkConfig struct {
	// 签名收集超时时间
	SignatureCollectionTimeout time.Duration `json:"signatureCollectionTimeout"`

	// 备用签名收集超时时间
	FallbackSignatureTimeout time.Duration `json:"fallbackSignatureTimeout"`

	// 网络消息重试次数
	MaxRetryAttempts int `json:"maxRetryAttempts"`

	// 重试间隔
	RetryInterval time.Duration `json:"retryInterval"`

	// 网络主题清理间隔
	TopicCleanupInterval time.Duration `json:"topicCleanupInterval"`

	// 签名收集器清理间隔
	CollectorCleanupInterval time.Duration `json:"collectorCleanupInterval"`

	// 网络连接检查间隔
	ConnectionCheckInterval time.Duration `json:"connectionCheckInterval"`

	// 最大并发签名处理数
	MaxConcurrentSignatures int `json:"maxConcurrentSignatures"`

	// 网络缓冲区大小
	NetworkBufferSize int `json:"networkBufferSize"`

	// 启用网络监控
	EnableNetworkMonitoring bool `json:"enableNetworkMonitoring"`

	// 启用签名去重
	EnableSignatureDeduplication bool `json:"enableSignatureDeduplication"`

	// 启用备用传播机制
	EnableFallbackPropagation bool `json:"enableFallbackPropagation"`
}

// DefaultNetworkConfig 返回默认网络配置
func DefaultNetworkConfig() *NetworkConfig {
	return &NetworkConfig{
		SignatureCollectionTimeout:   30 * time.Second,
		FallbackSignatureTimeout:     15 * time.Second,
		MaxRetryAttempts:             3,
		RetryInterval:                2 * time.Second,
		TopicCleanupInterval:         5 * time.Second,
		CollectorCleanupInterval:     10 * time.Second,
		ConnectionCheckInterval:      30 * time.Second,
		MaxConcurrentSignatures:      21,
		NetworkBufferSize:            5000, // 增加缓冲区大小，避免通道满
		EnableNetworkMonitoring:      true,
		EnableSignatureDeduplication: true,
		EnableFallbackPropagation:    true,
	}
}

// OptimizedNetworkConfig 返回优化的网络配置（用于生产环境）
func OptimizedNetworkConfig() *NetworkConfig {
	return &NetworkConfig{
		SignatureCollectionTimeout:   45 * time.Second,
		FallbackSignatureTimeout:     20 * time.Second,
		MaxRetryAttempts:             5,
		RetryInterval:                1 * time.Second,
		TopicCleanupInterval:         3 * time.Second,
		CollectorCleanupInterval:     5 * time.Second,
		ConnectionCheckInterval:      20 * time.Second,
		MaxConcurrentSignatures:      21,
		NetworkBufferSize:            10000, // 生产环境使用更大的缓冲区
		EnableNetworkMonitoring:      true,
		EnableSignatureDeduplication: true,
		EnableFallbackPropagation:    true,
	}
}

// TestNetworkConfig 返回测试网络配置（用于测试环境）
func TestNetworkConfig() *NetworkConfig {
	return &NetworkConfig{
		SignatureCollectionTimeout:   15 * time.Second,
		FallbackSignatureTimeout:     8 * time.Second,
		MaxRetryAttempts:             2,
		RetryInterval:                500 * time.Millisecond,
		TopicCleanupInterval:         2 * time.Second,
		CollectorCleanupInterval:     3 * time.Second,
		ConnectionCheckInterval:      10 * time.Second,
		MaxConcurrentSignatures:      5,
		NetworkBufferSize:            100,
		EnableNetworkMonitoring:      false,
		EnableSignatureDeduplication: true,
		EnableFallbackPropagation:    true,
	}
}

// Validate 验证网络配置
func (nc *NetworkConfig) Validate() error {
	if nc.SignatureCollectionTimeout <= 0 {
		return errors.New("签名收集超时时间必须大于0")
	}

	if nc.FallbackSignatureTimeout <= 0 {
		return errors.New("备用签名超时时间必须大于0")
	}

	if nc.MaxRetryAttempts < 0 {
		return errors.New("最大重试次数不能为负数")
	}

	if nc.RetryInterval <= 0 {
		return errors.New("重试间隔必须大于0")
	}

	if nc.MaxConcurrentSignatures <= 0 {
		return errors.New("最大并发签名数必须大于0")
	}

	if nc.NetworkBufferSize <= 0 {
		return errors.New("网络缓冲区大小必须大于0")
	}

	return nil
}

// GetConfigSummary 获取配置摘要
func (nc *NetworkConfig) GetConfigSummary() map[string]interface{} {
	return map[string]interface{}{
		"signatureCollectionTimeout":   nc.SignatureCollectionTimeout.String(),
		"fallbackSignatureTimeout":     nc.FallbackSignatureTimeout.String(),
		"maxRetryAttempts":             nc.MaxRetryAttempts,
		"retryInterval":                nc.RetryInterval.String(),
		"topicCleanupInterval":         nc.TopicCleanupInterval.String(),
		"collectorCleanupInterval":     nc.CollectorCleanupInterval.String(),
		"connectionCheckInterval":      nc.ConnectionCheckInterval.String(),
		"maxConcurrentSignatures":      nc.MaxConcurrentSignatures,
		"networkBufferSize":            nc.NetworkBufferSize,
		"enableNetworkMonitoring":      nc.EnableNetworkMonitoring,
		"enableSignatureDeduplication": nc.EnableSignatureDeduplication,
		"enableFallbackPropagation":    nc.EnableFallbackPropagation,
	}
}
