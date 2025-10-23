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

	// 🆕 新增：记录区块时间信息
	currentEpochBlockTimes map[types.Address][]time.Time
	epochBlockTimesHistory map[uint64]map[types.Address][]time.Time

	currentEpoch    uint64
	epochStartTime  time.Time
	epochStartBlock uint64
	mutex           sync.RWMutex
	logger          hclog.Logger

	// 🆕 新增：数据库存储
	store *BlockTrackerStore
}

// NewBlockProductionTracker 创建出块统计管理器
func NewBlockProductionTracker(logger hclog.Logger, store *BlockTrackerStore) *BlockProductionTracker {
	tracker := &BlockProductionTracker{
		currentEpochBlocks: make(map[types.Address]uint64),
		epochBlocksHistory: make(map[uint64]map[types.Address]uint64),

		// 🆕 新增：初始化区块时间记录
		currentEpochBlockTimes: make(map[types.Address][]time.Time),
		epochBlockTimesHistory: make(map[uint64]map[types.Address][]time.Time),

		logger: logger,
		store:  store, // 🆕 新增：设置数据库存储
	}

	// 🆕 从数据库加载历史数据
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

	// 加载历史数据
	for epochNumber, blockCounts := range allBlocks {
		bpt.epochBlocksHistory[epochNumber] = blockCounts
	}

	bpt.logger.Info("Loaded block data from database", "epochs", len(allBlocks))
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
	defer bpt.mutex.Unlock()

	// 如果epoch变化，保存历史数据
	if epochNumber != bpt.currentEpoch {
		if bpt.currentEpoch > 0 {
			// 保存出块数量历史
			bpt.epochBlocksHistory[bpt.currentEpoch] = make(map[types.Address]uint64)
			for addr, count := range bpt.currentEpochBlocks {
				bpt.epochBlocksHistory[bpt.currentEpoch][addr] = count
			}

			// 🆕 保存区块时间历史
			bpt.epochBlockTimesHistory[bpt.currentEpoch] = make(map[types.Address][]time.Time)
			for addr, times := range bpt.currentEpochBlockTimes {
				bpt.epochBlockTimesHistory[bpt.currentEpoch][addr] = times
			}

			// 🆕 保存到数据库
			bpt.saveEpochToDB(bpt.currentEpoch, bpt.epochBlocksHistory[bpt.currentEpoch])
		}

		// 开始新epoch
		bpt.currentEpoch = epochNumber
		bpt.epochStartTime = blockTime
		bpt.epochStartBlock = blockNumber
		bpt.currentEpochBlocks = make(map[types.Address]uint64)

		// 🆕 重置区块时间记录
		bpt.currentEpochBlockTimes = make(map[types.Address][]time.Time)

		bpt.logger.Debug("🔄 开始新Epoch出块统计",
			"epoch", epochNumber,
			"startTime", blockTime.Format("2006-01-02 15:04:05"),
			"startBlock", blockNumber)
	}

	// 记录出块
	bpt.currentEpochBlocks[producer]++

	// 🆕 记录区块时间
	bpt.currentEpochBlockTimes[producer] = append(bpt.currentEpochBlockTimes[producer], blockTime)

	bpt.logger.Debug("📊 记录出块",
		"epoch", epochNumber,
		"block", blockNumber,
		"producer", producer.String(),
		"totalBlocks", bpt.currentEpochBlocks[producer])
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

	// 🆕 如果内存中没有，尝试从数据库加载
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

// 🆕 新增：获取指定epoch的平均出块时间
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

// 🆕 新增：获取指定epoch的配置区块时间
func (bpt *BlockProductionTracker) GetEpochExpectedBlockTime(epochNumber uint64) time.Duration {
	// 这里应该从配置中获取，暂时返回2秒
	return 2 * time.Second
}
