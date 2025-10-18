package dpos

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
)

// TimeBasedEpochManager 基于时间的Epoch管理器
type TimeBasedEpochManager struct {
	rewardAccount         types.Address
	rewardAmount          *big.Int
	epochDuration         time.Duration // 🆕 新增：Epoch持续时间
	consensusSwitchHeight uint64        // 🆕 新增：共识切换高度
	mutex                 sync.RWMutex
	logger                hclog.Logger
	callback              func(uint64) error
	blockchain            interface{} // 区块链接口，用于获取区块信息
}

// NewTimeBasedEpochManager 创建基于时间的Epoch管理器
func NewTimeBasedEpochManager(
	epochDuration time.Duration,
	rewardAccount types.Address,
	rewardAmount *big.Int,
	consensusSwitchHeight uint64, // 🆕 新增：共识切换高度参数
	logger hclog.Logger,
) *TimeBasedEpochManager {
	return &TimeBasedEpochManager{
		rewardAccount:         rewardAccount,
		rewardAmount:          rewardAmount,
		epochDuration:         epochDuration,         // 🆕 设置Epoch持续时间
		consensusSwitchHeight: consensusSwitchHeight, // 🆕 设置共识切换高度
		logger:                logger,
	}
}

// GetCurrentEpoch 获取当前epoch（基于时间）
func (tem *TimeBasedEpochManager) GetCurrentEpoch(blockNumber uint64) uint64 {
	tem.mutex.RLock()
	defer tem.mutex.RUnlock()

	// 如果区块号小于共识切换高度，返回0
	if blockNumber < tem.consensusSwitchHeight {
		return 0
	}

	// 获取共识切换高度区块的时间戳
	startTime, err := tem.getEpochStartTime()
	if err != nil {
		// 如果无法获取起始时间，说明还没有切换到DPoS共识
		tem.logger.Debug("🔍 无法获取Epoch起始时间，DPoS共识尚未激活",
			"blockNumber", blockNumber,
			"consensusSwitchHeight", tem.consensusSwitchHeight,
			"error", err,
			"说明", "此时使用其他共识机制，不是DPoS，无Epoch概念")
		return 0
	}

	// 计算当前时间
	currentTime := time.Now()

	// 计算从共识切换高度开始经过的时间
	elapsedTime := currentTime.Sub(startTime)

	// 计算当前Epoch（基于时间）
	currentEpoch := uint64(elapsedTime/tem.epochDuration) + 1

	tem.logger.Debug("🔍 基于时间计算Epoch",
		"blockNumber", blockNumber,
		"consensusSwitchHeight", tem.consensusSwitchHeight,
		"startTime", startTime.Format("2006-01-02 15:04:05"),
		"currentTime", currentTime.Format("2006-01-02 15:04:05"),
		"elapsedTime", elapsedTime.String(),
		"epochDuration", tem.epochDuration.String(),
		"currentEpoch", currentEpoch)

	return currentEpoch
}

// 🆕 新增：设置区块链引用
func (tem *TimeBasedEpochManager) SetBlockchain(blockchain interface{}) {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()
	tem.blockchain = blockchain
	tem.logger.Info("🔧 设置区块链引用，用于基于区块高度计算Epoch")
}

// 🆕 新增：设置回调函数
func (tem *TimeBasedEpochManager) SetCallback(callback func(uint64) error) {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()
	tem.callback = callback
	tem.logger.Info("🔧 设置Epoch切换回调函数")
}

// 🆕 新增：基于区块高度触发epoch切换
func (tem *TimeBasedEpochManager) TriggerEpochSwitch(blockNumber uint64) {
	tem.mutex.Lock()
	defer tem.mutex.Unlock()

	// 基于区块高度计算Epoch
	epochSize := uint64(10)
	currentEpoch := (blockNumber / epochSize) + 1

	tem.logger.Info("⏰ ========== 基于区块高度触发新Epoch ==========",
		"epoch", currentEpoch,
		"blockNumber", blockNumber)

	// 调用epoch切换回调
	if tem.callback != nil {
		go func() {
			if err := tem.callback(currentEpoch); err != nil {
				tem.logger.Error("❌ epoch切换回调失败", "epoch", currentEpoch, "error", err)
			}
		}()
	}
}

// GetEpochInfo 获取epoch信息
func (tem *TimeBasedEpochManager) GetEpochInfo(blockNumber uint64) (uint64, time.Time, time.Duration) {
	tem.mutex.RLock()
	defer tem.mutex.RUnlock()

	// 如果区块号小于共识切换高度，返回0
	if blockNumber < tem.consensusSwitchHeight {
		return 0, time.Now(), tem.epochDuration
	}

	// 获取共识切换高度区块的时间戳
	startTime, err := tem.getEpochStartTime()
	if err != nil {
		// 如果无法获取起始时间，说明还没有切换到DPoS共识
		tem.logger.Debug("🔍 无法获取Epoch起始时间，DPoS共识尚未激活",
			"blockNumber", blockNumber,
			"consensusSwitchHeight", tem.consensusSwitchHeight,
			"error", err,
			"说明", "此时使用其他共识机制，不是DPoS，无Epoch概念")
		return 0, time.Now(), tem.epochDuration
	}

	// 计算当前时间
	currentTime := time.Now()

	// 计算从共识切换高度开始经过的时间
	elapsedTime := currentTime.Sub(startTime)

	// 计算当前Epoch
	currentEpoch := uint64(elapsedTime/tem.epochDuration) + 1

	// 计算当前Epoch的开始时间
	epochStartTime := startTime.Add(time.Duration(currentEpoch-1) * tem.epochDuration)

	tem.logger.Debug("🔍 获取Epoch信息（基于时间）",
		"blockNumber", blockNumber,
		"consensusSwitchHeight", tem.consensusSwitchHeight,
		"currentEpoch", currentEpoch,
		"epochStartTime", epochStartTime.Format("2006-01-02 15:04:05"),
		"epochDuration", tem.epochDuration.String())

	return currentEpoch, epochStartTime, tem.epochDuration
}

// getEpochStartTime 获取Epoch起始时间（共识切换高度区块的时间戳）
func (tem *TimeBasedEpochManager) getEpochStartTime() (time.Time, error) {
	if tem.blockchain == nil {
		return time.Time{}, fmt.Errorf("blockchain not available")
	}

	// 获取共识切换高度区块
	if blockchain, ok := tem.blockchain.(interface {
		GetHeaderByNumber(uint64) (*types.Header, bool)
	}); ok {
		if header, exists := blockchain.GetHeaderByNumber(tem.consensusSwitchHeight); exists {
			startTime := time.Unix(int64(header.Timestamp), 0)
			tem.logger.Debug("🔍 获取Epoch起始时间",
				"consensusSwitchHeight", tem.consensusSwitchHeight,
				"startTime", startTime.Format("2006-01-02 15:04:05"))
			return startTime, nil
		} else {
			// 如果共识切换高度区块还不存在，返回错误
			// 因为此时还没有切换到DPoS共识，不应该计算Epoch
			return time.Time{}, fmt.Errorf("consensus switch height block %d not found, DPoS not active yet", tem.consensusSwitchHeight)
		}
	}

	return time.Time{}, fmt.Errorf("blockchain interface not available")
}
