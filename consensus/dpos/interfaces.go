package dpos

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// ==================== 服务层接口定义 ====================

// BlockService 区块服务接口
type BlockService interface {
	// BuildBlock 构建新区块
	BuildBlock(parent *types.Header, coinbase types.Address) (*types.FullBlock, error)
	
	// ValidateBlock 验证区块
	ValidateBlock(block *types.Block) error
	
	// ShouldProduceBlock 判断是否应该出块
	ShouldProduceBlock() bool
	
	// GetBlockCreator 获取区块创建者
	GetBlockCreator(blockNumber uint64) (types.Address, error)
}

// ValidatorService 验证者服务接口
type ValidatorService interface {
	// GetValidators 获取验证者集合
	GetValidators(blockNumber uint64) (validator.AccountSet, error)
	
	// GetValidatorsWithTx 在事务中获取验证者集合
	GetValidatorsWithTx(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error)
	
	// GetCurrentValidators 获取当前验证者集合
	GetCurrentValidators() validator.AccountSet
	
	// UpdateValidators 更新验证者集合
	UpdateValidators(validators validator.AccountSet) error
	
	// IsValidator 判断是否是验证者
	IsValidator(address types.Address) bool
	
	// GetStakingInfo 获取质押信息
	GetStakingInfo(blockNumber uint64, staker types.Address) (*StakeInfo, error)
	
	// GetStakingInfoWithTx 在事务中获取质押信息
	GetStakingInfoWithTx(blockNumber uint64, staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error)
	
	// DetectFaults 检测验证者故障
	DetectFaults() error
	
	// GetValidatorFaultInfo 获取验证者故障信息
	GetValidatorFaultInfo(address types.Address) *FaultFlagInfo
}

// VotingService 投票服务接口
type VotingService interface {
	// ProcessVote 处理投票
	ProcessVote(vote *VoteMessage) error
	
	// GetVotingPower 获取投票权重
	GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error)
	
	// GetVotingPowerWithTx 在事务中获取投票权重
	GetVotingPowerWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error)
	
	// UpdateVotingPower 更新投票权重
	UpdateVotingPower(delegate types.Address, power *big.Int) error
	
	// GetVoters 获取投票者信息
	GetVoters() map[types.Address]*VoterInfo
}

// GovernanceService 治理服务接口
type GovernanceService interface {
	// CreateProposal 创建提案
	CreateProposal(proposal *ParameterProposal) error
	
	// VoteOnProposal 对提案投票
	VoteOnProposal(proposalID string, vote *ParameterVote) error
	
	// CheckProposalResult 检查提案结果
	CheckProposalResult(proposalID string) (ProposalStatus, error)
	
	// ExecuteProposal 执行提案
	ExecuteProposal(proposalID string) error
	
	// GetProposal 获取提案
	GetProposal(proposalID string) (*ParameterProposal, error)
	
	// GetActiveProposals 获取活跃提案
	GetActiveProposals() ([]*ParameterProposal, error)
	
	// GetCurrentParameterValues 获取当前参数值
	GetCurrentParameterValues() map[string]interface{}
}

// RewardService 奖励服务接口
type RewardService interface {
	// DistributeRewards 分发奖励
	DistributeRewards(rewards map[types.Address]*big.Int, rewardAccount types.Address) error
	
	// CalculateRewards 计算奖励
	CalculateRewards(epochNumber uint64) (map[types.Address]*big.Int, error)
	
	// ProcessBlockRewards 处理区块奖励
	ProcessBlockRewards(block *types.FullBlock) error
}

// StateManager 状态管理接口
type StateManager interface {
	// GetValidators 获取验证者集合
	GetValidators() (validator.AccountSet, error)
	
	// GetStakingInfo 获取质押信息
	GetStakingInfo(address types.Address) (*StakeInfo, error)
	
	// GetVotingPower 获取投票权重
	GetVotingPower(delegate types.Address) (*big.Int, error)
	
	// SaveValidators 保存验证者集合
	SaveValidators(validators validator.AccountSet) error
	
	// SaveStakingInfo 保存质押信息
	SaveStakingInfo(info *StakeInfo) error
	
	// UpdateVotingPower 更新投票权重
	UpdateVotingPower(delegate types.Address, power *big.Int) error
}

// InstanceManager DPoS实例管理器接口
type InstanceManager interface {
	// Register 注册DPoS实例
	Register(key string, dpos *DPoS)
	
	// Get 获取DPoS实例
	Get(key string) (*DPoS, bool)
	
	// GetAll 获取所有DPoS实例
	GetAll() map[string]*DPoS
	
	// Unregister 注销DPoS实例
	Unregister(key string)
}


