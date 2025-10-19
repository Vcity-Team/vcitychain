package dpos

import (
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
)

// BlockScheduler 固定时间窗口区块调度器（TRON模式）
type BlockScheduler struct {
	blockWindow    time.Duration       // 区块时间窗口（如2秒）
	validatorCount int                 // 验证者数量
	epochStartTime time.Time           // Epoch开始时间
	currentEpoch   uint64              // 当前Epoch
	blockchain     BlockchainInterface // 区块链接口
	genesisTime    time.Time           // 创世时间（缓存）
	lastLogTime    time.Time           // 上次打印日志的时间（防刷屏）

	mutex  sync.RWMutex
	logger hclog.Logger
}

// BlockchainInterface 区块链接口，用于获取区块信息
type BlockchainInterface interface {
	GetHeaderByNumber(blockNumber uint64) (*types.Header, bool)
}

// NewBlockScheduler 创建区块调度器
func NewBlockScheduler(blockWindow time.Duration, validatorCount int, blockchain BlockchainInterface, logger hclog.Logger) *BlockScheduler {
	return &BlockScheduler{
		blockWindow:    blockWindow,
		validatorCount: validatorCount,
		blockchain:     blockchain,
		logger:         logger,
	}
}

// StartNewEpoch 开始新的Epoch
func (bs *BlockScheduler) StartNewEpoch(epochNumber uint64, epochStartTime time.Time) {
	bs.mutex.Lock()
	defer bs.mutex.Unlock()

	bs.currentEpoch = epochNumber
	bs.epochStartTime = epochStartTime

	bs.logger.Info("🕐 开始新Epoch时间调度",
		"epoch", epochNumber,
		"startTime", epochStartTime.Format("2006-01-02 15:04:05"),
		"blockWindow", bs.blockWindow.String(),
		"validatorCount", bs.validatorCount)
}

// ShouldProduceBlock 检查指定验证者是否应该出块（TRON模式：基于创世时间的绝对计算）
func (bs *BlockScheduler) ShouldProduceBlock(validatorIndex int, currentBlockNumber uint64) bool {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()

	bs.logger.Info("🚀 开始检查出块资格（TRON模式）",
		"validatorIndex", validatorIndex,
		"currentBlockNumber", currentBlockNumber,
		"validatorCount", bs.validatorCount,
		"blockWindow", bs.blockWindow.String())

	// 1. 获取创世时间（缓存，只获取一次）
	if bs.genesisTime.IsZero() {
		genesisTime, err := bs.getGenesisTime()
		if err != nil {
			bs.logger.Error("无法获取创世时间", "error", err)
			return false
		}
		bs.genesisTime = genesisTime
		bs.logger.Info("🔧 获取创世时间", "genesisTime", genesisTime.Format("2006-01-02 15:04:05.000"))
	}

	// 2. 检查是否是该验证者的轮次
	expectedIndex := int(currentBlockNumber) % bs.validatorCount
	bs.logger.Debug("🔍 检查验证者轮次",
		"validatorIndex", validatorIndex,
		"expectedIndex", expectedIndex,
		"blockNumber", currentBlockNumber,
		"validatorCount", bs.validatorCount,
		"formula", fmt.Sprintf("%d%%%d=%d", currentBlockNumber, bs.validatorCount, expectedIndex))

	if validatorIndex != expectedIndex {
		// 🆕 防刷屏：每10秒打印一次日志
		now := time.Now()
		if now.Sub(bs.lastLogTime) >= 10*time.Second {
			bs.logger.Debug("⏭️ 不是当前轮次的验证者",
				"validatorIndex", validatorIndex,
				"expectedIndex", expectedIndex,
				"blockNumber", currentBlockNumber,
				"action", "跳过出块")
			bs.lastLogTime = now
		}
		return false
	}

	bs.logger.Debug("✅ 验证者轮次检查通过",
		"validatorIndex", validatorIndex,
		"expectedIndex", expectedIndex,
		"action", "继续时间窗口检查")

	// 3. 计算下一个区块的期望时间（TRON模式）
	nextBlockNumber := currentBlockNumber + 1
	expectedTime := bs.genesisTime.Add(time.Duration(nextBlockNumber) * bs.blockWindow)

	// 4. 获取当前时间
	now := time.Now()

	bs.logger.Debug("🕐 TRON模式时间计算",
		"validatorIndex", validatorIndex,
		"currentBlockNumber", currentBlockNumber,
		"nextBlockNumber", nextBlockNumber,
		"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
		"expectedTime", expectedTime.Format("2006-01-02 15:04:05.000"),
		"now", now.Format("2006-01-02 15:04:05.000"),
		"timeDiff", now.Sub(expectedTime).String(),
		"formula", fmt.Sprintf("genesisTime + nextBlockNumber * blockWindow = %s + %d * %s",
			bs.genesisTime.Format("15:04:05.000"), nextBlockNumber, bs.blockWindow.String()))

	// 5. 如果当前时间已经过了期望时间，计算下一个时间窗口
	if now.After(expectedTime) {
		// 计算从创世时间开始，当前时间应该对应的区块号
		timeSinceGenesis := now.Sub(bs.genesisTime)
		currentExpectedBlock := int(timeSinceGenesis / bs.blockWindow)

		// 计算下一个应该出块的时间
		nextExpectedTime := bs.genesisTime.Add(time.Duration(currentExpectedBlock+1) * bs.blockWindow)

		slotStart := nextExpectedTime
		slotEnd := nextExpectedTime.Add(bs.blockWindow)

		bs.logger.Debug("🔄 基于创世时间重新计算时间窗口",
			"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
			"timeSinceGenesis", timeSinceGenesis.String(),
			"currentExpectedBlock", currentExpectedBlock,
			"nextExpectedTime", nextExpectedTime.Format("2006-01-02 15:04:05.000"),
			"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
			"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"))

		// 检查是否在时间窗口内
		if now.Before(slotStart) {
			// 还没到时间，等待
			waitTime := time.Until(slotStart)
			bs.logger.Debug("⏳ 等待到出块时间",
				"validatorIndex", validatorIndex,
				"waitTime", waitTime.String(),
				"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
				"now", now.Format("2006-01-02 15:04:05.000"),
				"action", "等待后出块")

			// 等待到预定时间
			time.Sleep(waitTime)
			bs.logger.Debug("✅ 等待完成，准备出块",
				"validatorIndex", validatorIndex,
				"finalTime", time.Now().Format("2006-01-02 15:04:05.000"))
			return true
		} else if now.After(slotEnd) {
			// 时间窗口已过，不能出块
			bs.logger.Debug("⏰ 时间窗口已过，跳过出块",
				"validatorIndex", validatorIndex,
				"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
				"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"),
				"now", now.Format("2006-01-02 15:04:05.000"),
				"timeSinceSlotEnd", time.Since(slotEnd).String(),
				"action", "跳过出块")
			return false
		} else {
			// 在时间窗口内，可以出块
			bs.logger.Debug("✅ 在时间窗口内，可以出块",
				"validatorIndex", validatorIndex,
				"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
				"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"),
				"currentTime", now.Format("2006-01-02 15:04:05.000"),
				"action", "立即出块")
			return true
		}
	}

	// 6. 正常情况：检查是否在时间窗口内
	slotStart := expectedTime
	slotEnd := expectedTime.Add(bs.blockWindow)

	bs.logger.Info("🔍 检查时间窗口",
		"validatorIndex", validatorIndex,
		"blockNumber", currentBlockNumber,
		"now", now.Format("2006-01-02 15:04:05.000"),
		"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
		"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"))

	if now.Before(slotStart) {
		// 还没到时间，等待
		waitTime := time.Until(slotStart)
		bs.logger.Debug("⏳ 等待到出块时间",
			"validatorIndex", validatorIndex,
			"waitTime", waitTime.String(),
			"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
			"action", "等待后出块")

		// 等待到预定时间
		time.Sleep(waitTime)
		bs.logger.Debug("✅ 等待完成，准备出块",
			"validatorIndex", validatorIndex,
			"finalTime", time.Now().Format("2006-01-02 15:04:05.000"))
		return true
	} else if now.After(slotEnd) {
		// 时间窗口已过，不能出块
		bs.logger.Debug("⏰ 时间窗口已过，跳过出块",
			"validatorIndex", validatorIndex,
			"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
			"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
			"timeSinceSlotEnd", time.Since(slotEnd).String(),
			"action", "跳过出块")
		return false
	} else {
		// 在时间窗口内，可以出块
		bs.logger.Debug("✅ 在时间窗口内，可以出块",
			"validatorIndex", validatorIndex,
			"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
			"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"),
			"currentTime", now.Format("2006-01-02 15:04:05.000"),
			"action", "立即出块")
		return true
	}
}

// ShouldProduceBlockNow TRON式即时出块检查
func (bs *BlockScheduler) ShouldProduceBlockNow(validatorIndex int, currentBlockNumber uint64) bool {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()

	// 1. 获取创世时间
	if bs.genesisTime.IsZero() {
		genesisTime, err := bs.getGenesisTime()
		if err != nil {
			bs.logger.Error("无法获取创世时间", "error", err)
			return false
		}
		bs.genesisTime = genesisTime
	}

	now := time.Now()

	// 2. 计算当前slot（TRON方式）
	timeSinceGenesis := now.Sub(bs.genesisTime)
	currentSlot := int(timeSinceGenesis / bs.blockWindow)

	// 3. 计算当前应该出块的验证者
	expectedValidatorIndex := currentSlot % bs.validatorCount

	// 4. 检查是否轮到自己
	if validatorIndex != expectedValidatorIndex {
		// 🆕 防刷屏：每10秒打印一次日志
		now := time.Now()
		if now.Sub(bs.lastLogTime) >= 10*time.Second {
			bs.logger.Debug("⏭️ 不是当前轮次的验证者",
				"validatorIndex", validatorIndex,
				"expectedValidatorIndex", expectedValidatorIndex,
				"currentSlot", currentSlot,
				"action", "跳过出块")
			bs.lastLogTime = now
		}
		return false
	}

	// 5. 计算当前slot的时间窗口
	slotStart := bs.genesisTime.Add(time.Duration(currentSlot) * bs.blockWindow)
	slotEnd := slotStart.Add(bs.blockWindow)

	// 6. 检查是否在时间窗口内
	if now.Before(slotStart) {
		// 还没到时间
		bs.logger.Debug("⏳ 还没到出块时间",
			"validatorIndex", validatorIndex,
			"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
			"waitTime", slotStart.Sub(now).String())
		return false
	} else if now.After(slotEnd) {
		// 时间窗口已过，跳过
		bs.logger.Debug("⏰ 时间窗口已过，跳过出块",
			"validatorIndex", validatorIndex,
			"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
			"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
			"timeSinceSlotEnd", now.Sub(slotEnd).String())
		return false
	} else {
		// 在时间窗口内，可以出块
		// 防刷屏：5秒内只打印一次日志
		if now.Sub(bs.lastLogTime) >= 5*time.Second {
			bs.logger.Debug("✅ TRON式出块时机到达",
				"validatorIndex", validatorIndex,
				"currentSlot", currentSlot,
				"slotStart", slotStart.Format("2006-01-02 15:04:05.000"),
				"slotEnd", slotEnd.Format("2006-01-02 15:04:05.000"),
				"now", now.Format("2006-01-02 15:04:05.000"),
				"timeInSlot", now.Sub(slotStart).String())
			bs.lastLogTime = now
		}
		return true
	}
}

// GetNextBlockTime 获取下一个区块的预定时间
func (bs *BlockScheduler) GetNextBlockTime(currentBlockNumber uint64) time.Time {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()

	// 计算下一个区块的期望时间（TRON模式）
	nextBlockNumber := currentBlockNumber + 1
	nextSlotStartTime := bs.genesisTime.Add(time.Duration(nextBlockNumber) * bs.blockWindow)

	return nextSlotStartTime
}

// GetCurrentSlotInfo 获取当前时间槽信息
func (bs *BlockScheduler) GetCurrentSlotInfo() map[string]interface{} {
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()

	now := time.Now()
	currentSlot := int(now.Sub(bs.genesisTime) / bs.blockWindow)

	return map[string]interface{}{
		"currentEpoch":   bs.currentEpoch,
		"genesisTime":    bs.genesisTime.Format("2006-01-02 15:04:05.000"),
		"currentTime":    now.Format("2006-01-02 15:04:05.000"),
		"currentSlot":    currentSlot,
		"blockWindow":    bs.blockWindow.String(),
		"validatorCount": bs.validatorCount,
	}
}

// getCurrentBlockTime 获取当前区块的实际生产时间
func (bs *BlockScheduler) getCurrentBlockTime(currentBlockNumber uint64) (time.Time, error) {
	if bs.blockchain == nil {
		return time.Time{}, fmt.Errorf("blockchain interface not available")
	}

	// 获取当前区块的区块头
	header, exists := bs.blockchain.GetHeaderByNumber(currentBlockNumber)
	if !exists {
		return time.Time{}, fmt.Errorf("current block %d not found", currentBlockNumber)
	}

	// 从区块头中获取时间戳
	blockTime := time.Unix(int64(header.Timestamp), 0)

	bs.logger.Debug("🔍 获取当前区块时间",
		"currentBlockNumber", currentBlockNumber,
		"blockTime", blockTime.Format("2006-01-02 15:04:05.000"),
		"timestamp", header.Timestamp)

	return blockTime, nil
}

// getLastBlockTime 获取上一个区块的实际生产时间
func (bs *BlockScheduler) getLastBlockTime(currentBlockNumber uint64) (time.Time, error) {
	if bs.blockchain == nil {
		return time.Time{}, fmt.Errorf("blockchain interface not available")
	}

	// 获取上一个区块的区块头
	lastBlockNumber := currentBlockNumber - 1
	header, exists := bs.blockchain.GetHeaderByNumber(lastBlockNumber)
	if !exists {
		return time.Time{}, fmt.Errorf("last block %d not found", lastBlockNumber)
	}

	// 从区块头中获取时间戳
	blockTime := time.Unix(int64(header.Timestamp), 0)

	bs.logger.Debug("🔍 获取上一个区块时间",
		"lastBlockNumber", lastBlockNumber,
		"blockTime", blockTime.Format("2006-01-02 15:04:05.000"),
		"timestamp", header.Timestamp)

	return blockTime, nil
}

// getGenesisTime 获取创世时间
func (bs *BlockScheduler) getGenesisTime() (time.Time, error) {
	if bs.blockchain == nil {
		return time.Time{}, fmt.Errorf("blockchain interface not available")
	}

	// 获取创世区块头（区块号0）
	genesisHeader, exists := bs.blockchain.GetHeaderByNumber(0)
	if !exists {
		return time.Time{}, fmt.Errorf("genesis header not found")
	}

	// 从创世区块头中获取时间戳
	genesisTime := time.Unix(int64(genesisHeader.Timestamp), 0)

	// 如果创世时间戳为0（Unix epoch），使用当前时间作为参考点
	if genesisHeader.Timestamp == 0 {
		now := time.Now()
		currentBlockNumber := bs.getCurrentBlockNumber()

		if currentBlockNumber > 0 {
			// 计算虚拟创世时间：当前时间 - 当前区块号 * 区块间隔
			virtualGenesisTime := now.Add(-time.Duration(currentBlockNumber) * bs.blockWindow)
			bs.logger.Info("🔧 使用虚拟创世时间",
				"virtualGenesisTime", virtualGenesisTime.Format("2006-01-02 15:04:05.000"),
				"currentBlockNumber", currentBlockNumber,
				"blockWindow", bs.blockWindow.String(),
				"now", now.Format("2006-01-02 15:04:05.000"))
			return virtualGenesisTime, nil
		} else {
			// 如果当前区块号也是0，使用当前时间
			bs.logger.Info("🔧 使用当前时间作为创世时间")
			return now, nil
		}
	}

	bs.logger.Info("🔍 获取创世时间",
		"genesisTime", genesisTime.Format("2006-01-02 15:04:05.000"),
		"timestamp", genesisHeader.Timestamp)

	return genesisTime, nil
}

// getCurrentBlockNumber 获取当前区块号
func (bs *BlockScheduler) getCurrentBlockNumber() uint64 {
	if bs.blockchain == nil {
		return 0
	}

	// 尝试获取最新的区块号
	// 这里我们使用一个简单的方法：从区块1开始查找，直到找不到为止
	for i := uint64(1); i < 1000000; i++ { // 设置一个合理的上限
		_, exists := bs.blockchain.GetHeaderByNumber(i)
		if !exists {
			return i - 1
		}
	}

	return 0
}
