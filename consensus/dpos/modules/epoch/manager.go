package epoch

import (
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/hashicorp/go-hclog"
)

// EpochManagerInterface Epoch管理器接口（用于依赖注入）
type EpochManagerInterface interface {
	GetCurrentEpoch(blockNumber uint64) uint64
	GetEpochInfo(blockNumber uint64) (uint64, time.Time, time.Duration)
	getEpochSize() uint64
	getConsensusSwitchHeight() uint64
}

// Manager Epoch管理器的默认实现
type Manager struct {
	epochManager EpochManagerInterface
	logger       hclog.Logger
}

// NewManager 创建Epoch管理器
func NewManager(epochManager EpochManagerInterface, logger hclog.Logger) core.EpochManager {
	return &Manager{
		epochManager: epochManager,
		logger:       logger,
	}
}

// GetCurrentEpoch 获取当前epoch
func (m *Manager) GetCurrentEpoch(blockNumber uint64) uint64 {
	if m.epochManager == nil {
		return 0
	}
	return m.epochManager.GetCurrentEpoch(blockNumber)
}

// GetEpochInfo 获取epoch信息
func (m *Manager) GetEpochInfo(blockNumber uint64) (uint64, time.Time, time.Duration) {
	if m.epochManager == nil {
		return 0, time.Now(), 0
	}
	return m.epochManager.GetEpochInfo(blockNumber)
}

// IsEpochEnd 判断是否是epoch结束
func (m *Manager) IsEpochEnd(blockNumber uint64) bool {
	if m.epochManager == nil {
		return false
	}
	epochSize := m.epochManager.getEpochSize()
	consensusSwitchHeight := m.epochManager.getConsensusSwitchHeight()

	if blockNumber < consensusSwitchHeight {
		return false
	}

	dposBlockNumber := blockNumber - consensusSwitchHeight
	currentEpoch := (dposBlockNumber / epochSize) + 1
	firstBlockInEpoch := consensusSwitchHeight + (currentEpoch-1)*epochSize
	lastBlockInEpoch := firstBlockInEpoch + epochSize - 1
	return blockNumber >= lastBlockInEpoch
}

// GetEpochSize 获取epoch大小
func (m *Manager) GetEpochSize() uint64 {
	if m.epochManager == nil {
		return 0
	}
	return m.epochManager.getEpochSize()
}
