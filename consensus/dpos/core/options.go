package core

import (
	"github.com/hashicorp/go-hclog"
)

// DPoSOptions DPoS构造选项（支持依赖注入）
// 注意：DPoSConfig和wallet.Key类型在dpos包中定义，这里使用interface{}占位
// 实际使用时需要从dpos包导入具体类型
type DPoSOptions struct {
	// 必需配置
	Config interface{} // *dpos.DPoSConfig - 使用interface{}避免循环依赖
	Logger hclog.Logger
	Key    interface{} // *wallet.Key - 使用interface{}避免循环依赖

	// 可选依赖（如果为nil，将使用默认实现）
	ConsensusManager ConsensusManager
	ValidatorManager ValidatorManager
	EpochManager     EpochManager
	RewardManager    RewardManager
	FaultManager     FaultManager
	NetworkManager   NetworkManager
	BLSManager       BLSManager
	StateManager     StateManager
	QueryManager     QueryManager
}

