package dpos

import (
	"time"
)

// isEpochEndBlock 检查是否是epoch最后一个区块
func (d *DPoS) isEpochEndBlock(blockNumber uint64) bool {
	currentEpoch := d.getEpochForBlock(blockNumber)
	if currentEpoch == nil {
		d.logger.Warn("⚠️ 无法获取当前epoch信息", "blockNumber", blockNumber)
		return false
	}

	epochSize := d.getEpochSize()

	isEpochEnd := currentEpoch.FirstBlockInEpoch+epochSize-1 == blockNumber

	d.logger.Debug("🔍 检查是否是epoch最后一个区块",
		"blockNumber", blockNumber,
		"epochNumber", currentEpoch.Number,
		"firstBlockInEpoch", currentEpoch.FirstBlockInEpoch,
		"epochSize", epochSize,
		"isEpochEnd", isEpochEnd)

	return isEpochEnd
}

// getCurrentEpoch 获取当前epoch信息
func (d *DPoS) getCurrentEpoch() *epochMetadata {
	return d.getEpochForBlock(0) // 0表示使用当前区块号
}

// getEpochForBlock 获取指定区块号的epoch信息
func (d *DPoS) getEpochForBlock(blockNumber uint64) *epochMetadata {
	// 🆕 修改：基于指定区块号计算epoch
	targetBlockNumber := blockNumber
	if blockNumber == 0 {
		// 如果传入0，则使用当前区块号
		if d.config.Blockchain != nil {
			if header := d.config.Blockchain.Header(); header != nil {
				targetBlockNumber = header.Number
			}
		}
	}

	// 计算epoch信息
	epochSize := d.getEpochSize()

	// 获取共识切换高度
	consensusSwitchHeight := uint64(0)
	if d.config != nil {
		consensusSwitchHeight = d.config.ConsensusSwitchHeight
	}

	// 从共识切换高度开始计算epoch
	if targetBlockNumber < consensusSwitchHeight {
		// 在共识切换高度之前，epoch为0
		return &epochMetadata{
			Number:            0,
			FirstBlockInEpoch: 0,
		}
	}

	// 计算DPoS区块号（从共识切换高度开始）
	dposBlockNumber := targetBlockNumber - consensusSwitchHeight

	// 计算epoch编号（从1开始）
	epochNumber := (dposBlockNumber / epochSize) + 1

	// 计算该epoch的第一个区块号
	firstBlockInEpoch := consensusSwitchHeight + (epochNumber-1)*epochSize

	d.logger.Debug("🔍 计算epoch信息",
		"targetBlockNumber", targetBlockNumber,
		"consensusSwitchHeight", consensusSwitchHeight,
		"dposBlockNumber", dposBlockNumber,
		"epochSize", epochSize,
		"epochNumber", epochNumber,
		"firstBlockInEpoch", firstBlockInEpoch)

	return &epochMetadata{
		Number:            epochNumber,
		FirstBlockInEpoch: firstBlockInEpoch,
	}
}

// getEpochSize 获取epoch大小（区块数）
func (d *DPoS) getEpochSize() uint64 {
	if d.config == nil {
		d.logger.Warn("⚠️ DPoS配置为空，使用默认epoch大小")
		return 28800 // 默认值：86400秒 / 3秒 = 28800个区块
	}

	// 根据EpochDuration和BlockTime计算epoch大小
	epochDuration := d.config.EpochDuration
	blockTime := d.config.BlockTime.Duration

	if blockTime == 0 {
		d.logger.Warn("⚠️ BlockTime为0，使用默认值3秒")
		blockTime = 3 * time.Second
	}

	if epochDuration == 0 {
		d.logger.Warn("⚠️ EpochDuration为0，使用默认值86400秒")
		epochDuration = 86400 * time.Second
	}

	epochSize := uint64(epochDuration / blockTime)

	if epochSize == 0 {
		d.logger.Warn("⚠️ 计算出的epoch大小为0，使用默认值28800")
		epochSize = 28800
	}

	return epochSize
}
