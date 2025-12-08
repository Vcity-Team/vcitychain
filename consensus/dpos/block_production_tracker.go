package dpos

import (
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
)

// BlockProductionTracker 出块统计管理器
type BlockProductionTracker struct {
	currentEpochBlocks map[types.Address]uint64
	epochBlocksHistory map[uint64]map[types.Address]uint64

	// 新增：记录区块时间信息
	currentEpochBlockTimes map[types.Address][]time.Time
	epochBlockTimesHistory map[uint64]map[types.Address][]time.Time

	// 新增：记录已处理的区块号（用于去重）
	processedBlocks map[uint64]bool

	currentEpoch    uint64
	epochStartTime  time.Time
	epochStartBlock uint64
	mutex           sync.RWMutex
	logger          hclog.Logger

	// 新增：数据库存储
	store *BlockTrackerStore

	// 新增：区块时间配置
	blockTime time.Duration
}

// NewBlockProductionTracker 创建出块统计管理器
func NewBlockProductionTracker(logger hclog.Logger, store *BlockTrackerStore, blockTime time.Duration) *BlockProductionTracker {
	tracker := &BlockProductionTracker{
		currentEpochBlocks: make(map[types.Address]uint64),
		epochBlocksHistory: make(map[uint64]map[types.Address]uint64),

		// 新增：初始化区块时间记录
		currentEpochBlockTimes: make(map[types.Address][]time.Time),
		epochBlockTimesHistory: make(map[uint64]map[types.Address][]time.Time),

		// 新增：初始化已处理区块记录（用于去重）
		processedBlocks: make(map[uint64]bool),

		logger:    logger,
		store:     store,     // 新增：设置数据库存储
		blockTime: blockTime, // 新增：设置区块时间配置
	}

	// 从数据库加载历史数据
	tracker.loadFromDB()

	return tracker
}

// loadFromDB 从数据库加载历史数据
func (bpt *BlockProductionTracker) loadFromDB() {
	if bpt.store == nil {
		bpt.logger.Warn("BlockTrackerStore is nil, skipping database load")
		return
	}

	allBlocks, err := bpt.store.LoadAllEpochBlocks()
	if err != nil {
		bpt.logger.Error("Failed to load block data from database", "error", err)
		return
	}

	bpt.mutex.Lock()
	defer bpt.mutex.Unlock()

	if len(allBlocks) == 0 {
		return
	}

	var latestEpoch uint64
	for epochNumber := range allBlocks {
		if epochNumber > latestEpoch {
			latestEpoch = epochNumber
		}
	}

	// 将最新的epoch视为当前epoch，其余作为历史数据
	if latestEpoch > 0 {
		bpt.currentEpoch = latestEpoch
		bpt.currentEpochBlocks = make(map[types.Address]uint64)
		for addr, count := range allBlocks[latestEpoch] {
			bpt.currentEpochBlocks[addr] = count
		}
		delete(allBlocks, latestEpoch)
		bpt.logger.Info("恢复当前Epoch出块统计",
			"epoch", latestEpoch,
			"validatorCount", len(bpt.currentEpochBlocks))
	}

	for epochNumber, blockCounts := range allBlocks {
		bpt.epochBlocksHistory[epochNumber] = blockCounts
	}

	bpt.logger.Info("从数据库加载出块统计",
		"historicalEpochs", len(allBlocks),
		"currentEpochRestored", latestEpoch)
}

// saveEpochToDB 保存epoch数据到数据库
func (bpt *BlockProductionTracker) saveEpochToDB(epochNumber uint64, blockCounts map[types.Address]uint64) {
	if bpt.store == nil {
		bpt.logger.Warn("BlockTrackerStore is nil, skipping database save")
		return
	}

	err := bpt.store.SaveEpochBlocks(epochNumber, blockCounts)
	if err != nil {
		bpt.logger.Error("Failed to save block data to database", "epoch", epochNumber, "error", err)
	} else {
		bpt.logger.Debug("Saved block data to database", "epoch", epochNumber)
	}
}

// RecordBlockProduction 记录出块
func (bpt *BlockProductionTracker) RecordBlockProduction(
	blockNumber uint64,
	blockTime time.Time,
	producer types.Address,
	epochNumber uint64,
) {
	bpt.mutex.Lock()

	var persistSnapshots []struct {
		epoch  uint64
		counts map[types.Address]uint64
	}

	// 去重检查：如果该区块已经处理过，跳过
	if bpt.processedBlocks[blockNumber] {
		bpt.logger.Debug("⚠️ 区块已记录，跳过重复记录",
			"blockNumber", blockNumber,
			"producer", producer.String(),
			"epoch", epochNumber)
		bpt.mutex.Unlock()
		return
	}

	// 如果epoch变化，保存历史数据
	if epochNumber != bpt.currentEpoch {
		if bpt.currentEpoch > 0 {
			// 保存出块数量历史
			historyCounts := make(map[types.Address]uint64)
			for addr, count := range bpt.currentEpochBlocks {
				historyCounts[addr] = count
			}
			bpt.epochBlocksHistory[bpt.currentEpoch] = historyCounts

			// 保存区块时间历史
			bpt.epochBlockTimesHistory[bpt.currentEpoch] = make(map[types.Address][]time.Time)
			for addr, times := range bpt.currentEpochBlockTimes {
				bpt.epochBlockTimesHistory[bpt.currentEpoch][addr] = times
			}

			// 保存到数据库（上一epoch）
			if bpt.store != nil {
				persistSnapshots = append(persistSnapshots, struct {
					epoch  uint64
					counts map[types.Address]uint64
				}{
					epoch:  bpt.currentEpoch,
					counts: historyCounts,
				})
			}
		}

		// 开始新epoch
		bpt.currentEpoch = epochNumber
		bpt.epochStartTime = blockTime
		bpt.epochStartBlock = blockNumber
		bpt.currentEpochBlocks = make(map[types.Address]uint64)

		// 重置区块时间记录
		bpt.currentEpochBlockTimes = make(map[types.Address][]time.Time)

		// 清理已处理区块记录（只保留当前epoch的区块，避免内存泄漏）
		// 清理策略：只保留最近2个epoch的区块记录
		bpt.cleanupProcessedBlocks(epochNumber)

		bpt.logger.Debug("🔄 开始新Epoch出块统计",
			"epoch", epochNumber,
			"startTime", blockTime.Format("2006-01-02 15:04:05"),
			"startBlock", blockNumber)
	}

	// 记录出块
	bpt.currentEpochBlocks[producer]++

	// 标记该区块已处理
	bpt.processedBlocks[blockNumber] = true

	// 记录区块时间
	bpt.currentEpochBlockTimes[producer] = append(bpt.currentEpochBlockTimes[producer], blockTime)

	bpt.logger.Debug("📊 记录出块",
		"epoch", epochNumber,
		"block", blockNumber,
		"producer", producer.String(),
		"totalBlocks", bpt.currentEpochBlocks[producer])

	// 持久化当前epoch快照
	if bpt.store != nil && bpt.currentEpoch > 0 {
		currentSnapshot := make(map[types.Address]uint64)
		for addr, count := range bpt.currentEpochBlocks {
			currentSnapshot[addr] = count
		}
		persistSnapshots = append(persistSnapshots, struct {
			epoch  uint64
			counts map[types.Address]uint64
		}{
			epoch:  bpt.currentEpoch,
			counts: currentSnapshot,
		})
	}

	bpt.mutex.Unlock()

	// 执行持久化（锁外进行，避免阻塞）
	for _, snapshot := range persistSnapshots {
		bpt.saveEpochToDB(snapshot.epoch, snapshot.counts)
	}
}

// GetEpochBlockCounts 获取指定epoch的出块统计
func (bpt *BlockProductionTracker) GetEpochBlockCounts(epochNumber uint64) map[types.Address]uint64 {
	bpt.mutex.RLock()
	defer bpt.mutex.RUnlock()

	if epochNumber == bpt.currentEpoch {
		result := make(map[types.Address]uint64)
		for addr, count := range bpt.currentEpochBlocks {
			result[addr] = count
		}
		return result
	}

	if history, exists := bpt.epochBlocksHistory[epochNumber]; exists {
		result := make(map[types.Address]uint64)
		for addr, count := range history {
			result[addr] = count
		}
		return result
	}

	// 如果内存中没有，尝试从数据库加载
	if bpt.store != nil {
		dbBlocks, err := bpt.store.LoadEpochBlocks(epochNumber)
		if err == nil && len(dbBlocks) > 0 {
			// 将数据库数据加载到内存
			bpt.epochBlocksHistory[epochNumber] = dbBlocks
			bpt.logger.Debug("Loaded epoch blocks from database", "epoch", epochNumber)
			return dbBlocks
		}
	}

	return make(map[types.Address]uint64)
}

// GetTotalEpochBlocks 获取指定epoch的总出块数
func (bpt *BlockProductionTracker) GetTotalEpochBlocks(epochNumber uint64) uint64 {
	blockCounts := bpt.GetEpochBlockCounts(epochNumber)
	total := uint64(0)
	for _, count := range blockCounts {
		total += count
	}
	return total
}

// 新增：获取指定epoch的平均出块时间
func (bpt *BlockProductionTracker) GetEpochAverageBlockTime(epochNumber uint64) time.Duration {
	bpt.mutex.RLock()
	defer bpt.mutex.RUnlock()

	var allBlockTimes []time.Time

	if epochNumber == bpt.currentEpoch {
		// 当前epoch
		for _, times := range bpt.currentEpochBlockTimes {
			allBlockTimes = append(allBlockTimes, times...)
		}
	} else if history, exists := bpt.epochBlockTimesHistory[epochNumber]; exists {
		// 历史epoch
		for _, times := range history {
			allBlockTimes = append(allBlockTimes, times...)
		}
	}

	if len(allBlockTimes) < 2 {
		return 0 // 需要至少2个区块才能计算间隔
	}

	// 按时间排序
	for i := 0; i < len(allBlockTimes)-1; i++ {
		for j := i + 1; j < len(allBlockTimes); j++ {
			if allBlockTimes[i].After(allBlockTimes[j]) {
				allBlockTimes[i], allBlockTimes[j] = allBlockTimes[j], allBlockTimes[i]
			}
		}
	}

	// 计算平均出块间隔
	var totalInterval time.Duration
	for i := 1; i < len(allBlockTimes); i++ {
		interval := allBlockTimes[i].Sub(allBlockTimes[i-1])
		totalInterval += interval
	}

	return totalInterval / time.Duration(len(allBlockTimes)-1)
}

// 获取指定epoch的配置区块时间
func (bpt *BlockProductionTracker) GetEpochExpectedBlockTime(epochNumber uint64) time.Duration {
	// 从配置中获取blockTime
	blockTime := bpt.blockTime

	// 如果blockTime为0，使用默认值3秒（兜底保护）
	if blockTime == 0 {
		bpt.logger.Warn("⚠️ blockTime为0，使用默认值3秒")
		blockTime = 3 * time.Second
	}

	return blockTime
}

// cleanupProcessedBlocks 清理已处理区块记录（避免内存泄漏）
// 只保留最近2个epoch的区块记录
func (bpt *BlockProductionTracker) cleanupProcessedBlocks(currentEpoch uint64) {
	// 计算要保留的最小区块号（假设每个epoch最多200个区块，保留2个epoch）
	// 这是一个保守的估计，实际应该根据epochSize来计算
	epochSize := uint64(100) // 默认值，实际应该从配置获取
	minBlockNumber := uint64(0)
	if currentEpoch > 2 {
		// 只保留最近2个epoch的区块记录
		minBlockNumber = (currentEpoch - 2) * epochSize
	}

	// 清理过期的区块记录
	for blockNum := range bpt.processedBlocks {
		if blockNum < minBlockNumber {
			delete(bpt.processedBlocks, blockNum)
		}
	}

	bpt.logger.Debug("🧹 清理已处理区块记录",
		"currentEpoch", currentEpoch,
		"minBlockNumber", minBlockNumber,
		"remainingRecords", len(bpt.processedBlocks))
}
