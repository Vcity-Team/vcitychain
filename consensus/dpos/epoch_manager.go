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

	// 🆕 新增：独立时间管理
	genesisTime time.Time          // 创世时间
	timer       *time.Timer        // 定时器
	stopCh      chan struct{}      // 停止信号
	callback    func(uint64) error // epoch切换回调
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
		stopCh:        make(chan struct{}),
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

// 🆕 新增：设置创世时间和回调
func (tem *TimeBasedEpochManager) SetGenesisTimeAndCallback(genesisTime time.Time, callback func(uint64) error) {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()

	tem.genesisTime = genesisTime
	tem.callback = callback

	tem.logger.Info("🔧 设置创世时间和回调",
		"genesisTime", genesisTime.Format("2006-01-02 15:04:05"),
		"epochDuration", tem.epochDuration.String())
}

// 🆕 新增：启动独立时间检查器
func (tem *TimeBasedEpochManager) StartIndependentTimer() {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()

	if tem.timer != nil {
		tem.timer.Stop()
	}

	// 计算到下一个epoch的时间
	now := time.Now()
	nextEpochTime := tem.calculateNextEpochTime(now)
	timeUntilNext := nextEpochTime.Sub(now)

	if timeUntilNext <= 0 {
		// 如果已经过了时间，立即触发
		go tem.triggerEpochSwitch()
		timeUntilNext = tem.epochDuration
	}

	tem.timer = time.AfterFunc(timeUntilNext, func() {
		tem.triggerEpochSwitch()
		// 重新设置下一个定时器
		tem.StartIndependentTimer()
	})

	tem.logger.Info("⏰ 启动独立epoch定时器",
		"nextEpochTime", nextEpochTime.Format("2006-01-02 15:04:05"),
		"timeUntilNext", timeUntilNext.String())
}

// 🆕 新增：停止独立时间检查器
func (tem *TimeBasedEpochManager) StopIndependentTimer() {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()

	if tem.timer != nil {
		tem.timer.Stop()
		tem.timer = nil
	}

	close(tem.stopCh)
	tem.logger.Info("🛑 停止独立epoch定时器")
}

// 🆕 新增：计算下一个epoch时间
func (tem *TimeBasedEpochManager) calculateNextEpochTime(now time.Time) time.Time {
	if tem.genesisTime.IsZero() {
		// 如果没有创世时间，使用当前时间作为基准
		return now.Add(tem.epochDuration)
	}

	// 基于创世时间计算下一个epoch时间
	timeSinceGenesis := now.Sub(tem.genesisTime)
	epochsPassed := timeSinceGenesis / tem.epochDuration
	nextEpochNumber := epochsPassed + 1
	nextEpochTime := tem.genesisTime.Add(time.Duration(nextEpochNumber) * tem.epochDuration)

	return nextEpochTime
}

// 🆕 新增：触发epoch切换
func (tem *TimeBasedEpochManager) triggerEpochSwitch() {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()

	now := time.Now()
	previousEpochTime := tem.lastEpochTime
	tem.currentEpoch++
	tem.lastEpochTime = now

	// 计算时间间隔
	var timeInterval time.Duration
	if !previousEpochTime.IsZero() {
		timeInterval = now.Sub(previousEpochTime)
	}

	tem.logger.Info("⏰ ========== 独立定时器触发新Epoch ==========",
		"epoch", tem.currentEpoch,
		"time", now.Format("2006-01-02 15:04:05"),
		"duration", tem.epochDuration.String(),
		"previousEpochTime", previousEpochTime.Format("2006-01-02 15:04:05"),
		"actualInterval", timeInterval.String())

	// 调用回调函数
	if tem.callback != nil {
		go func() {
			if err := tem.callback(tem.currentEpoch); err != nil {
				tem.logger.Error("❌ epoch切换回调失败", "epoch", tem.currentEpoch, "error", err)
			}
		}()
	}
}
