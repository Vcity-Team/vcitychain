package reward

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies 奖励管理器依赖
type Dependencies struct {
	// 计算奖励
	CalculateRewards func(validators validator.AccountSet, voters map[types.Address]interface{}, blockCounts map[types.Address]uint64, totalBlocks uint64) map[types.Address]*big.Int
	
	// 分发奖励
	DistributeRewards func(epochNumber uint64, validators validator.AccountSet, voters map[types.Address]interface{}) error
	
	// 获取验证者集合
	GetValidators func() validator.AccountSet
	
	// 获取投票者集合（使用interface{}避免循环依赖）
	GetVoters func() map[types.Address]interface{} // map[types.Address]*VoterInfo
	
	// 获取出块统计
	GetEpochBlockCounts func(epochNumber uint64) map[types.Address]uint64
	GetTotalEpochBlocks func(epochNumber uint64) uint64
	
	Logger hclog.Logger
}

// Manager 奖励管理器实现
type Manager struct {
	deps Dependencies
}

// NewManager 创建奖励管理器
func NewManager(deps Dependencies) core.RewardManager {
	return &Manager{
		deps: deps,
	}
}

// CalculateRewards 计算奖励
func (m *Manager) CalculateRewards(epochNumber uint64) (map[types.Address]*big.Int, error) {
	if m.deps.CalculateRewards == nil {
		return nil, fmt.Errorf("calculate rewards function not provided")
	}
	
	validators := m.deps.GetValidators()
	voters := m.deps.GetVoters()
	blockCounts := m.deps.GetEpochBlockCounts(epochNumber)
	totalBlocks := m.deps.GetTotalEpochBlocks(epochNumber)
	
	rewards := m.deps.CalculateRewards(validators, voters, blockCounts, totalBlocks)
	return rewards, nil
}

// DistributeRewards 分发奖励
func (m *Manager) DistributeRewards(epochNumber uint64, rewards map[types.Address]*big.Int) error {
	if m.deps.DistributeRewards == nil {
		return fmt.Errorf("distribute rewards function not provided")
	}
	
	validators := m.deps.GetValidators()
	voters := m.deps.GetVoters()
	
	return m.deps.DistributeRewards(epochNumber, validators, voters)
}

// GetRewardInfo 获取奖励信息
func (m *Manager) GetRewardInfo(validatorAddress types.Address, epochNumber uint64) map[string]interface{} {
	info := make(map[string]interface{})
	
	if m.deps.GetEpochBlockCounts != nil {
		blockCounts := m.deps.GetEpochBlockCounts(epochNumber)
		info["blocksProduced"] = blockCounts[validatorAddress]
	}
	
	if m.deps.GetTotalEpochBlocks != nil {
		totalBlocks := m.deps.GetTotalEpochBlocks(epochNumber)
		info["totalBlocks"] = totalBlocks
	}
	
	return info
}

