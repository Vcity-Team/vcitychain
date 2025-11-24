package core

import (
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// ==================== 核心接口定义 ====================

// ConsensusManager 共识管理器接口
// 负责区块构建、验证和出块调度
type ConsensusManager interface {
	// BuildBlock 构建区块
	BuildBlock(parent *types.Header) (*types.FullBlock, error)

	// ValidateBlock 验证区块
	ValidateBlock(block *types.Block) error

	// ShouldProduceBlock 判断是否应该出块
	ShouldProduceBlock(blockNumber uint64, myAddress types.Address) bool

	// IsEpochEndBlock 判断是否是epoch结束区块
	IsEpochEndBlock(blockNumber uint64) bool
}

// ValidatorManager 验证者管理器接口
// 负责验证者集合的管理和查询
type ValidatorManager interface {
	// GetValidators 获取指定区块的验证者集合
	GetValidators(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error)

	// GetCurrentValidators 获取当前验证者集合
	GetCurrentValidators() validator.AccountSet

	// UpdateValidators 更新验证者集合
	UpdateValidators(blockNumber uint64, validators validator.AccountSet) error

	// IsValidator 判断指定地址是否是验证者
	IsValidator(address types.Address, blockNumber uint64) bool

	// GetVotingPower 获取指定验证者的投票权重
	GetVotingPower(blockNumber uint64, validator types.Address) (*big.Int, error)
}

// EpochManager Epoch管理器接口
// 负责Epoch的计算和管理
type EpochManager interface {
	// GetCurrentEpoch 获取当前epoch编号
	GetCurrentEpoch(blockNumber uint64) uint64

	// GetEpochInfo 获取epoch信息
	GetEpochInfo(blockNumber uint64) (epochNumber uint64, startTime time.Time, duration time.Duration)

	// IsEpochEnd 判断是否是epoch结束
	IsEpochEnd(blockNumber uint64) bool

	// GetEpochSize 获取epoch大小（区块数）
	GetEpochSize() uint64
}

// RewardManager 奖励管理器接口
// 负责奖励的计算和分配
type RewardManager interface {
	// CalculateRewards 计算奖励
	CalculateRewards(epochNumber uint64) (map[types.Address]*big.Int, error)

	// DistributeRewards 分发奖励
	DistributeRewards(epochNumber uint64, rewards map[types.Address]*big.Int) error

	// GetRewardInfo 获取奖励信息
	GetRewardInfo(validatorAddress types.Address, epochNumber uint64) map[string]interface{}
}

// FaultManager 故障管理器接口
// 负责故障检测和处理
type FaultManager interface {
	// DetectFaults 检测故障
	DetectFaults(blockNumber uint64) ([]FaultFlagInfo, error)

	// IsValidatorFaulty 判断验证者是否故障
	IsValidatorFaulty(validatorAddress types.Address) (bool, error)

	// GetFaultInfo 获取故障信息
	GetFaultInfo(validatorAddress types.Address) map[string]interface{}
}

// NetworkManager 网络管理器接口
// 负责网络通信
type NetworkManager interface {
	// BroadcastMessage 广播消息
	BroadcastMessage(topic string, message []byte) error

	// RequestBLSKey 请求BLS公钥
	RequestBLSKey(targetAddress types.Address) error

	// GetValidatorConnectivity 获取验证者连接状态
	GetValidatorConnectivity(address types.Address) (peerID string, hasMapping bool, isConnected bool)
}

// BLSManager BLS管理器接口
// 负责BLS密钥管理
type BLSManager interface {
	// GetBLSKey 获取BLS公钥
	GetBLSKey(address types.Address) (*bls.PublicKey, error)

	// SaveBLSKey 保存BLS公钥
	SaveBLSKey(address types.Address, key *bls.PublicKey) error

	// BroadcastBLSKey 广播BLS公钥
	BroadcastBLSKey(address types.Address, key *bls.PublicKey) error
}

// StateManager 状态管理器接口
// 负责状态存储和查询
type StateManager interface {
	// GetAccount 获取账户信息
	GetAccount(address types.Address) (*AccountInfo, error)

	// GetBalance 获取余额
	GetBalance(address types.Address) (*big.Int, error)

	// SaveValidators 保存验证者集合
	SaveValidators(blockNumber uint64, validators validator.AccountSet) error
}

// QueryManager 查询管理器接口
// 负责查询接口
type QueryManager interface {
	// GetCurrentEpochInfo 获取当前Epoch信息
	GetCurrentEpochInfo() map[string]interface{}

	// GetEpochInfoByNumber 获取指定Epoch信息
	GetEpochInfoByNumber(epochNumber uint64) map[string]interface{}

	// GetValidatorStats 获取验证者统计
	GetValidatorStats(validatorAddress types.Address, epochNumber uint64) map[string]interface{}
}

// ==================== 辅助类型定义 ====================

// FaultFlagInfo 故障标志信息
type FaultFlagInfo struct {
	ValidatorAddress types.Address
	EpochNumber      uint64
	FaultType        string
	Reason           string
}

// AccountInfo 账户信息
type AccountInfo struct {
	Address  types.Address
	Balance  *big.Int
	Nonce    uint64
	CodeHash types.Hash
}

