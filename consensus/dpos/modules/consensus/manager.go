package consensus

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies 共识管理器依赖
type Dependencies struct {
	// 构建区块
	BuildBlock func() (*types.FullBlock, error)
	
	// 验证区块
	ValidateBlock func(block *types.Block) error
	
	// 判断是否应该出块
	ShouldProduceBlock func(blockNumber uint64, myAddress types.Address) bool
	
	// 判断是否是epoch结束区块
	IsEpochEndBlock func(blockNumber uint64) bool
	
	Logger hclog.Logger
}

// Manager 共识管理器实现
type Manager struct {
	deps Dependencies
}

// NewManager 创建共识管理器
func NewManager(deps Dependencies) core.ConsensusManager {
	return &Manager{
		deps: deps,
	}
}

// BuildBlock 构建区块
func (m *Manager) BuildBlock(parent *types.Header) (*types.FullBlock, error) {
	if m.deps.BuildBlock == nil {
		return nil, fmt.Errorf("build block function not provided")
	}
	return m.deps.BuildBlock()
}

// ValidateBlock 验证区块
func (m *Manager) ValidateBlock(block *types.Block) error {
	if m.deps.ValidateBlock == nil {
		return fmt.Errorf("validate block function not provided")
	}
	return m.deps.ValidateBlock(block)
}

// ShouldProduceBlock 判断是否应该出块
func (m *Manager) ShouldProduceBlock(blockNumber uint64, myAddress types.Address) bool {
	if m.deps.ShouldProduceBlock == nil {
		return false
	}
	return m.deps.ShouldProduceBlock(blockNumber, myAddress)
}

// IsEpochEndBlock 判断是否是epoch结束区块
func (m *Manager) IsEpochEndBlock(blockNumber uint64) bool {
	if m.deps.IsEpochEndBlock == nil {
		return false
	}
	return m.deps.IsEpochEndBlock(blockNumber)
}

