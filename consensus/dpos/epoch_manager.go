package dpos

import (
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
)

// TimeBasedEpochManager 基于区块高度的Epoch管理器
type TimeBasedEpochManager struct {
	rewardAccount types.Address
	rewardAmount  *big.Int
	epochDuration time.Duration // 添加epochDuration字段
	mutex         sync.RWMutex
	logger        hclog.Logger
	callback      func(uint64) error

	// 🆕 新增：区块链引用，用于基于区块高度计算Epoch
	blockchain interface{} // 区块链接口，用于获取当前区块号
}

// NewTimeBasedEpochManager 创建基于区块高度的Epoch管理器
func NewTimeBasedEpochManager(
	epochDuration time.Duration,
	rewardAccount types.Address,
	rewardAmount *big.Int,
	logger hclog.Logger,
) *TimeBasedEpochManager {
	return &TimeBasedEpochManager{
		rewardAccount: rewardAccount,
		rewardAmount:  rewardAmount,
		epochDuration: epochDuration,
		logger:        logger,
	}
}

// GetCurrentEpoch 获取当前epoch
func (tem *TimeBasedEpochManager) GetCurrentEpoch(blockNumber uint64) uint64 {
	tem.mutex.RLock()
	defer tem.mutex.RUnlock()

	// 🆕 修改：基于传入的区块高度计算Epoch
	epochSize := tem.getEpochSize()
	blockBasedEpoch := (blockNumber / epochSize) + 1

	tem.logger.Debug("🔍 基于区块高度计算Epoch",
		"blockNumber", blockNumber,
		"epochSize", epochSize,
		"blockBasedEpoch", blockBasedEpoch)

	return blockBasedEpoch
}

// getEpochSize 根据配置计算epoch大小（区块数）
func (tem *TimeBasedEpochManager) getEpochSize() uint64 {
	// 使用配置的epochDuration和默认的blockTime
	epochDuration := tem.epochDuration
	blockTime := 2 * time.Second // 默认区块时间，应该从配置中获取

	epochSize := uint64(epochDuration / blockTime)
	if epochSize == 0 {
		epochSize = 1 // 至少1个区块
	}

	return epochSize
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
	epochSize := tem.getEpochSize()
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

	// 🆕 修改：基于传入的区块高度获取Epoch信息
	epochSize := tem.getEpochSize()
	currentEpoch := (blockNumber / epochSize) + 1
	firstBlockInEpoch := (currentEpoch - 1) * epochSize

	// 计算epoch开始时间（基于第一个区块的时间）
	// 如果无法获取区块信息，使用当前时间
	epochStartTime := time.Now()
	if tem.blockchain != nil {
		if blockchain, ok := tem.blockchain.(interface {
			GetHeaderByNumber(uint64) (*types.Header, bool)
		}); ok {
			if header, exists := blockchain.GetHeaderByNumber(firstBlockInEpoch); exists {
				epochStartTime = time.Unix(int64(header.Timestamp), 0)
			}
		}
	}

	tem.logger.Debug("🔍 获取Epoch信息（区块基础）",
		"blockNumber", blockNumber,
		"currentEpoch", currentEpoch,
		"firstBlockInEpoch", firstBlockInEpoch,
		"epochStartTime", epochStartTime.Format("2006-01-02 15:04:05"))

	return currentEpoch, epochStartTime, time.Duration(10) * time.Second
}
