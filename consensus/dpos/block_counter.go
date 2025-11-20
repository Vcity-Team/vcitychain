package dpos

import (
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// BlockCounter 负责计算验证者的出块统计
type BlockCounter struct {
	dposInstance *DPoS
	logger       hclog.Logger
}

// NewBlockCounter 创建出块计数器
func NewBlockCounter(dposInstance *DPoS, logger hclog.Logger) *BlockCounter {
	return &BlockCounter{
		dposInstance: dposInstance,
		logger:       logger.Named("block-counter"),
	}
}

// BlockStats 出块统计信息
type BlockStats struct {
	ExpectedBlocks uint64
	ActualBlocks   uint64
	MissedBlocks   uint64
}

// CalculateBlockStats 计算验证者的出块统计
func (bc *BlockCounter) CalculateBlockStats(
	validatorAddr types.Address,
	startEpoch, endEpoch uint64,
) BlockStats {
	// 调用 dposInstance.calculateMissedBlocksWithActual
	missedBlocks, actualBlocks, expectedBlocks := bc.dposInstance.calculateMissedBlocksWithActual(validatorAddr, startEpoch, endEpoch)

	return BlockStats{
		ExpectedBlocks: expectedBlocks,
		ActualBlocks:   actualBlocks,
		MissedBlocks:   missedBlocks,
	}
}
