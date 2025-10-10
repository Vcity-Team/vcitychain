package dpos

import (
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
)

// TimeBasedEpochManager 基于时间的Epoch管理器
type TimeBasedEpochManager struct {
	epochDuration time.Duration
	lastEpochTime time.Time
	currentEpoch  uint64
	rewardAccount types.Address
	rewardAmount  *big.Int
	mutex         sync.RWMutex
	logger        hclog.Logger
}

// NewTimeBasedEpochManager 创建时间基础Epoch管理器
func NewTimeBasedEpochManager(
	epochDuration time.Duration,
	rewardAccount types.Address,
	rewardAmount *big.Int,
	logger hclog.Logger,
) *TimeBasedEpochManager {
	return &TimeBasedEpochManager{
		epochDuration: epochDuration,
		rewardAccount: rewardAccount,
		currentEpoch:  0,
		rewardAmount:  rewardAmount,
		logger:        logger,
	}
}

// ShouldStartNewEpoch 检查是否应该开始新epoch
func (tem *TimeBasedEpochManager) ShouldStartNewEpoch(blockTime time.Time) bool {
	tem.mutex.RLock()
	defer tem.mutex.RUnlock()

	if tem.lastEpochTime.IsZero() {
		// 第一次，记录时间但不开始epoch
		tem.lastEpochTime = blockTime
		return false
	}

	return blockTime.Sub(tem.lastEpochTime) >= tem.epochDuration
}

// StartNewEpoch 开始新epoch
func (tem *TimeBasedEpochManager) StartNewEpoch(blockTime time.Time) uint64 {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()

	tem.currentEpoch++
	tem.lastEpochTime = blockTime

	tem.logger.Info("⏰ ========== 开始新Epoch ==========",
		"epoch", tem.currentEpoch,
		"time", blockTime.Format("2006-01-02 15:04:05"),
		"duration", tem.epochDuration.String())

	return tem.currentEpoch
}

// GetCurrentEpoch 获取当前epoch
func (tem *TimeBasedEpochManager) GetCurrentEpoch() uint64 {
	tem.mutex.RLock()
	defer tem.mutex.RUnlock()
	return tem.currentEpoch
}

// GetEpochInfo 获取epoch信息
func (tem *TimeBasedEpochManager) GetEpochInfo() (uint64, time.Time, time.Duration) {
	tem.mutex.RLock()
	defer tem.mutex.RUnlock()
	return tem.currentEpoch, tem.lastEpochTime, tem.epochDuration
}
