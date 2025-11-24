package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
)

// NewDPoS 创建DPoS实例（支持依赖注入）
// 注意：这是一个辅助函数，主要用于测试和依赖注入场景
// 生产环境仍使用Factory函数
func NewDPoS(opts *core.DPoSOptions) (*DPoS, error) {
	if opts == nil {
		return nil, fmt.Errorf("options cannot be nil")
	}
	if opts.Logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	dpos := &DPoS{
		closeCh:     make(chan struct{}),
		logger:      opts.Logger,
		lastLogTime: make(map[string]time.Time),
	}

	// 设置配置（如果提供）
	if opts.Config != nil {
		if config, ok := opts.Config.(*DPoSConfig); ok {
			dpos.config = config
		} else {
			// 如果类型不匹配，创建默认配置
			dpos.config = &DPoSConfig{}
		}
	} else {
		dpos.config = &DPoSConfig{}
	}

	// 设置密钥（如果提供）
	if opts.Key != nil {
		if key, ok := opts.Key.(*wallet.Key); ok {
			dpos.key = key
		}
	}

	// 设置依赖注入的模块（如果提供）
	// 这些模块将在Initialize()中初始化，如果没有提供则使用默认适配器
	if opts.ConsensusManager != nil {
		dpos.consensus = opts.ConsensusManager
	}
	if opts.ValidatorManager != nil {
		dpos.validator = opts.ValidatorManager
	}
	if opts.EpochManager != nil {
		dpos.epoch = opts.EpochManager
	}
	if opts.RewardManager != nil {
		dpos.reward = opts.RewardManager
	}
	if opts.FaultManager != nil {
		dpos.fault = opts.FaultManager
	}
	if opts.QueryManager != nil {
		dpos.query = opts.QueryManager
	}

	return dpos, nil
}
