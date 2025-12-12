package dpos

import (
	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/types"
)

// isEndOfPeriod checks if an end of a period (either it be sprint or epoch)
// is reached with the current block (the parent block of the current fsm iteration)
func isEndOfPeriod(blockNumber, periodSize uint64) bool {
	return blockNumber%periodSize == 0
}

// getBlockData returns block header and extra
func getBlockData(blockNumber uint64, blockchainBackend blockchainBackend) (*types.Header, *Extra, error) {
	blockHeader, found := blockchainBackend.GetHeaderByNumber(blockNumber)
	if !found {
		return nil, nil, blockchain.ErrNoBlock
	}

	blockExtra, err := GetDposExtra(blockHeader.ExtraData)
	if err != nil {
		return nil, nil, err
	}

	return blockHeader, blockExtra, nil
}

// isEpochEndingBlock checks if given block is an epoch ending block
func isEpochEndingBlock(blockNumber uint64, extra *Extra, blockchain blockchainBackend) (bool, error) {
	// 🔧 修改：不再通过 Validators 判断 epoch 结束，改为通过 Checkpoint 判断
	// 如果 Checkpoint 存在且 EpochNumber 与下一个区块不同，则为 epoch 结束区块
	if extra.Checkpoint == nil {
		return false, nil
	}

	// 检查下一个区块的 epoch 是否不同
	nextBlockNumber := blockNumber + 1
	if nextHeader, exists := blockchain.GetHeaderByNumber(nextBlockNumber); exists {
		nextExtra := &Extra{}
		if err := nextExtra.UnmarshalRLP(nextHeader.ExtraData); err == nil {
			if nextExtra.Checkpoint != nil && nextExtra.Checkpoint.EpochNumber != extra.Checkpoint.EpochNumber {
				return true, nil
			}
		}
	}

	// 如果没有下一个区块，或者无法确定，则通过计算 epoch 大小来判断
	// 这需要知道 epoch 大小，暂时返回 false（实际应该通过配置获取）
	return false, nil
}
