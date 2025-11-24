package query

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies 查询管理器依赖
type Dependencies struct {
	// 配置相关
	ConsensusSwitchHeight uint64
	EpochSize             uint64
	BlockTime             time.Duration

	// 区块链接口
	GetCurrentBlockNumber func() uint64

	// Epoch管理器
	EpochManager core.EpochManager

	// 验证者相关
	GetSortedValidatorsWithLimit func() ([]ValidatorInfo, error)
	GetValidatorFaultInfo        func(address types.Address) map[string]interface{}

	// 区块追踪
	GetEpochBlockCounts func(epochNumber uint64) map[types.Address]uint64
	GetTotalEpochBlocks func(epochNumber uint64) uint64

	Logger hclog.Logger
}

// ValidatorInfo 验证者信息（简化版，避免循环依赖）
type ValidatorInfo struct {
	Address     types.Address
	VotingPower interface{} // 可以是*big.Int或string
	IsActive    bool
}

// Manager 查询管理器实现
type Manager struct {
	deps Dependencies
}

// NewManager 创建查询管理器
func NewManager(deps Dependencies) core.QueryManager {
	return &Manager{
		deps: deps,
	}
}

// GetCurrentEpochInfo 获取当前Epoch信息
func (m *Manager) GetCurrentEpochInfo() map[string]interface{} {
	if m.deps.EpochManager == nil {
		return map[string]interface{}{
			"error": "epoch manager not initialized",
		}
	}

	// 获取当前区块高度
	currentBlockNumber := m.deps.GetCurrentBlockNumber()

	// 计算epoch信息
	consensusSwitchHeight := m.deps.ConsensusSwitchHeight
	epochSize := m.deps.EpochSize

	var epochNumber uint64
	var firstBlockInEpoch uint64

	if currentBlockNumber < consensusSwitchHeight {
		epochNumber = 0
		firstBlockInEpoch = 0
	} else {
		dposBlockNumber := currentBlockNumber - consensusSwitchHeight
		epochNumber = (dposBlockNumber / epochSize) + 1
		firstBlockInEpoch = consensusSwitchHeight + (epochNumber-1)*epochSize
	}

	lastBlockInEpoch := firstBlockInEpoch + epochSize - 1

	// 获取epoch时间信息
	_, lastEpochTime, epochDuration := m.deps.EpochManager.GetEpochInfo(currentBlockNumber)

	// 计算剩余区块数和时间
	remainingBlocks := int64(0)
	if currentBlockNumber < lastBlockInEpoch {
		remainingBlocks = int64(lastBlockInEpoch) - int64(currentBlockNumber)
	}

	blockTime := m.deps.BlockTime
	if blockTime == 0 {
		blockTime = 3 * time.Second
	}

	timeRemaining := time.Duration(remainingBlocks) * blockTime
	nextEpochTimeEstimated := time.Now().Add(timeRemaining)

	// 获取验证者信息
	validators := make([]map[string]interface{}, 0)
	var blockCounts map[types.Address]uint64

	if m.deps.GetEpochBlockCounts != nil {
		blockCounts = m.deps.GetEpochBlockCounts(epochNumber)
	} else {
		blockCounts = make(map[types.Address]uint64)
	}

	if m.deps.GetSortedValidatorsWithLimit != nil {
		dbValidators, err := m.deps.GetSortedValidatorsWithLimit()
		if err == nil && len(dbValidators) > 0 {
			for i, validator := range dbValidators {
				faultInfo := make(map[string]interface{})
				if m.deps.GetValidatorFaultInfo != nil {
					faultInfo = m.deps.GetValidatorFaultInfo(validator.Address)
				}

				blocksProduced := blockCounts[validator.Address]

				validators = append(validators, map[string]interface{}{
					"index":          i,
					"address":        validator.Address.String(),
					"votingPower":    validator.VotingPower,
					"isActive":       validator.IsActive,
					"faultFlag":      faultInfo,
					"blocksProduced": blocksProduced,
				})
			}
		}
	}

	// 判断epoch状态
	epochStatus := "active"
	if currentBlockNumber >= lastBlockInEpoch {
		epochStatus = "completed"
	} else if currentBlockNumber < firstBlockInEpoch {
		epochStatus = "pending"
	}

	// 计算已出块数
	blocksProduced := uint64(0)
	if currentBlockNumber >= firstBlockInEpoch {
		blocksProduced = currentBlockNumber - firstBlockInEpoch + 1
		if blocksProduced > epochSize {
			blocksProduced = epochSize
		}
	}

	return map[string]interface{}{
		"epochNumber":            epochNumber,
		"epochStatus":            epochStatus,
		"epochStartTime":         lastEpochTime.Format(time.RFC3339),
		"epochDuration":          epochDuration.String(),
		"firstBlockInEpoch":      firstBlockInEpoch,
		"lastBlockInEpoch":       lastBlockInEpoch,
		"currentBlockNumber":     currentBlockNumber,
		"epochSize":              epochSize,
		"remainingBlocks":        remainingBlocks,
		"estimatedTimeRemaining": timeRemaining.String(),
		"nextEpochTimeEstimated": nextEpochTimeEstimated.Format(time.RFC3339),
		"blocksProduced":         blocksProduced,
		"validators":             validators,
		"validatorCount":         len(validators),
		"consensusSwitchHeight":  consensusSwitchHeight,
	}
}

// GetEpochInfoByNumber 获取指定Epoch信息
func (m *Manager) GetEpochInfoByNumber(epochNumber uint64) map[string]interface{} {
	if m.deps.EpochManager == nil {
		return map[string]interface{}{
			"error": "epoch manager not initialized",
		}
	}

	currentBlockNumber := m.deps.GetCurrentBlockNumber()
	consensusSwitchHeight := m.deps.ConsensusSwitchHeight
	epochSize := m.deps.EpochSize

	// 计算当前epoch
	var currentEpoch uint64
	if currentBlockNumber < consensusSwitchHeight {
		currentEpoch = 0
	} else {
		dposBlockNumber := currentBlockNumber - consensusSwitchHeight
		currentEpoch = (dposBlockNumber / epochSize) + 1
	}

	// 如果请求的是当前Epoch，返回当前信息
	if epochNumber == currentEpoch {
		return m.GetCurrentEpochInfo()
	}

	// 计算指定epoch的区块范围
	var firstBlockInEpoch uint64
	if epochNumber == 0 {
		firstBlockInEpoch = 0
	} else {
		firstBlockInEpoch = consensusSwitchHeight + (epochNumber-1)*epochSize
	}
	lastBlockInEpoch := firstBlockInEpoch + epochSize - 1

	// 确定epoch状态
	epochStatus := "unknown"
	if epochNumber < currentEpoch {
		epochStatus = "completed"
	} else if epochNumber == currentEpoch {
		epochStatus = "active"
	} else {
		epochStatus = "future"
	}

	// 计算剩余区块数和已出块数
	remainingBlocks := int64(0)
	blocksProduced := uint64(0)

	if epochNumber == currentEpoch {
		if currentBlockNumber < lastBlockInEpoch {
			remainingBlocks = int64(lastBlockInEpoch) - int64(currentBlockNumber)
		}
		if currentBlockNumber >= firstBlockInEpoch {
			blocksProduced = currentBlockNumber - firstBlockInEpoch + 1
			if blocksProduced > epochSize {
				blocksProduced = epochSize
			}
		}
	} else if epochNumber < currentEpoch {
		remainingBlocks = 0
		blocksProduced = epochSize
	} else {
		remainingBlocks = int64(epochSize)
		blocksProduced = 0
	}

	// 获取epoch时间信息
	_, _, epochDuration := m.deps.EpochManager.GetEpochInfo(currentBlockNumber)

	// 计算剩余时间
	blockTime := m.deps.BlockTime
	if blockTime == 0 {
		blockTime = 3 * time.Second
	}
	timeRemaining := time.Duration(remainingBlocks) * blockTime

	// 获取出块统计
	var blockCounts map[types.Address]uint64
	if m.deps.GetEpochBlockCounts != nil {
		blockCounts = m.deps.GetEpochBlockCounts(epochNumber)
	} else {
		blockCounts = make(map[types.Address]uint64)
	}

	// 获取验证者信息
	validators := make([]map[string]interface{}, 0)
	if m.deps.GetSortedValidatorsWithLimit != nil {
		dbValidators, err := m.deps.GetSortedValidatorsWithLimit()
		if err == nil && len(dbValidators) > 0 {
			for i, validator := range dbValidators {
				faultInfo := make(map[string]interface{})
				if m.deps.GetValidatorFaultInfo != nil {
					faultInfo = m.deps.GetValidatorFaultInfo(validator.Address)
				}

				blocksProducedByValidator := blockCounts[validator.Address]

				validators = append(validators, map[string]interface{}{
					"index":          i,
					"address":        validator.Address.String(),
					"votingPower":    validator.VotingPower,
					"isActive":       validator.IsActive,
					"faultFlag":      faultInfo,
					"blocksProduced": blocksProducedByValidator,
				})
			}
		}
	}

	return map[string]interface{}{
		"epochNumber":            epochNumber,
		"epochStatus":            epochStatus,
		"epochDuration":          epochDuration.String(),
		"firstBlockInEpoch":      firstBlockInEpoch,
		"lastBlockInEpoch":       lastBlockInEpoch,
		"currentBlockNumber":     currentBlockNumber,
		"epochSize":              epochSize,
		"remainingBlocks":        remainingBlocks,
		"estimatedTimeRemaining": timeRemaining.String(),
		"blocksProduced":         blocksProduced,
		"validators":             validators,
		"validatorCount":         len(validators),
		"consensusSwitchHeight":  consensusSwitchHeight,
	}
}

// GetValidatorStats 获取验证者统计
func (m *Manager) GetValidatorStats(validatorAddress types.Address, epochNumber uint64) map[string]interface{} {
	stats := make(map[string]interface{})

	if m.deps.GetEpochBlockCounts != nil {
		blockCounts := m.deps.GetEpochBlockCounts(epochNumber)
		stats["blocksProduced"] = blockCounts[validatorAddress]
	}

	if m.deps.GetTotalEpochBlocks != nil {
		totalBlocks := m.deps.GetTotalEpochBlocks(epochNumber)
		stats["totalBlocks"] = totalBlocks
	}

	return stats
}

