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
	currentEpoch       uint64
	epochStartTime     time.Time
	epochStartBlock    uint64
	mutex              sync.RWMutex
	logger             hclog.Logger
}

// NewBlockProductionTracker 创建出块统计管理器
func NewBlockProductionTracker(logger hclog.Logger) *BlockProductionTracker {
	return &BlockProductionTracker{
		currentEpochBlocks: make(map[types.Address]uint64),
		epochBlocksHistory: make(map[uint64]map[types.Address]uint64),
		logger:             logger,
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
			bpt.epochBlocksHistory[bpt.currentEpoch] = make(map[types.Address]uint64)
			for addr, count := range bpt.currentEpochBlocks {
				bpt.epochBlocksHistory[bpt.currentEpoch][addr] = count
			}
		}

		// 开始新epoch
		bpt.currentEpoch = epochNumber
		bpt.epochStartTime = blockTime
		bpt.epochStartBlock = blockNumber
		bpt.currentEpochBlocks = make(map[types.Address]uint64)

		bpt.logger.Info("🔄 开始新Epoch出块统计",
			"epoch", epochNumber,
			"startTime", blockTime.Format("2006-01-02 15:04:05"),
			"startBlock", blockNumber)
	}

	// 记录出块
	bpt.currentEpochBlocks[producer]++

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
