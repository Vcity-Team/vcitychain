package dpos

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// votingService 投票服务实现
type votingService struct {
	dpos        *DPoS
	stateManager StateManager
	mutex       sync.RWMutex
}

// NewVotingService 创建新的投票服务
func NewVotingService(dpos *DPoS, stateManager StateManager) VotingService {
	return &votingService{
		dpos:         dpos,
		stateManager: stateManager,
	}
}

// ProcessVote 处理投票
func (vs *votingService) ProcessVote(vote *VoteMessage) error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()
	
	// 使用现有的投票处理逻辑
	// 这里需要调用voting_processor.go中的processVote
	// 暂时返回nil，后续迁移
	return nil
}

// GetVotingPower 获取投票权重
func (vs *votingService) GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error) {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetVotingPower方法
	return vs.dpos.GetVotingPower(blockNumber, delegate)
}

// GetVotingPowerWithTx 在事务中获取投票权重
func (vs *votingService) GetVotingPowerWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetVotingPowerWithTx方法
	return vs.dpos.GetVotingPowerWithTx(blockNumber, delegate, dbTx)
}

// UpdateVotingPower 更新投票权重
func (vs *votingService) UpdateVotingPower(delegate types.Address, power *big.Int) error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()
	
	// 使用状态管理器更新投票权重
	if vs.stateManager != nil {
		return vs.stateManager.UpdateVotingPower(delegate, power)
	}
	
	return fmt.Errorf("state manager is nil")
}

// GetVoters 获取投票者信息
func (vs *votingService) GetVoters() map[types.Address]*VoterInfo {
	vs.mutex.RLock()
	defer vs.mutex.RUnlock()
	
	// 使用现有的GetVoters方法
	return vs.dpos.GetVoters()
}



