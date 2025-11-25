package fault

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies 故障管理器依赖
type Dependencies struct {
	// 检测故障（返回的FaultFlagInfo需要包含NodeAddress和Reason字段）
	DetectFaults func(blockNumber uint64) ([]LocalFaultFlagInfo, error)

	// 判断验证者是否故障
	IsValidatorFaulty func(validatorAddress types.Address) (bool, error)

	// 获取故障信息
	GetFaultInfo func(validatorAddress types.Address) map[string]interface{}

	Logger hclog.Logger
}

// LocalFaultFlagInfo 故障标志信息（本地定义，映射到core.FaultFlagInfo）
type LocalFaultFlagInfo struct {
	NodeAddress            types.Address
	EpochNumber            uint64
	Reason                 string
	IsFaulty               bool
	MissedBlocks           uint64
	ActualBlocks           uint64
	ExpectedBlocks         uint64
	MissedBlocksPercentage uint64
	LastUpdateTime         uint64
	LastFaultyEpoch        uint64
	DoubleSigningHeight    uint64
}

// Manager 故障管理器实现
type Manager struct {
	deps Dependencies
}

// NewManager 创建故障管理器
func NewManager(deps Dependencies) core.FaultManager {
	return &Manager{
		deps: deps,
	}
}

// DetectFaults 检测故障
func (m *Manager) DetectFaults(blockNumber uint64) ([]core.FaultFlagInfo, error) {
	if m.deps.DetectFaults == nil {
		return nil, fmt.Errorf("detect faults function not provided")
	}

	faults, err := m.deps.DetectFaults(blockNumber)
	if err != nil {
		return nil, err
	}

	// 转换为core.FaultFlagInfo
	result := make([]core.FaultFlagInfo, len(faults))
	for i, fault := range faults {
		result[i] = core.FaultFlagInfo{
			ValidatorAddress:       fault.NodeAddress,
			EpochNumber:            fault.EpochNumber,
			FaultType:              fault.Reason,
			Reason:                 fault.Reason,
			IsFaulty:               fault.IsFaulty,
			MissedBlocks:           fault.MissedBlocks,
			ActualBlocks:           fault.ActualBlocks,
			ExpectedBlocks:         fault.ExpectedBlocks,
			MissedBlocksPercentage: fault.MissedBlocksPercentage,
			LastUpdateTime:         fault.LastUpdateTime,
			LastFaultyEpoch:        fault.LastFaultyEpoch,
			DoubleSigningHeight:    fault.DoubleSigningHeight,
		}
	}

	return result, nil
}

// IsValidatorFaulty 判断验证者是否故障
func (m *Manager) IsValidatorFaulty(validatorAddress types.Address) (bool, error) {
	if m.deps.IsValidatorFaulty == nil {
		return false, fmt.Errorf("is validator faulty function not provided")
	}
	return m.deps.IsValidatorFaulty(validatorAddress)
}

// GetFaultInfo 获取故障信息
func (m *Manager) GetFaultInfo(validatorAddress types.Address) map[string]interface{} {
	if m.deps.GetFaultInfo == nil {
		return make(map[string]interface{})
	}
	return m.deps.GetFaultInfo(validatorAddress)
}
