package dpos

import (
	"os"
)

// getEpochForBlock 获取指定区块号的epoch信息
func (d *DPoS) getEpochForBlock(blockNumber uint64) *epochMetadata {
	// 基于指定区块号计算epoch
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

	d.logger.Debug(" 计算epoch信息",
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
		return 5 // 默认值：10秒 / 2秒 = 5个区块
	}

	// 根据EpochDuration和BlockTime计算epoch大小
	epochDuration := d.config.EpochDuration
	blockTime := d.config.BlockTime.Duration

	if blockTime == 0 {
		d.logger.Error("💀 blockTime大小为0，这是严重配置错误，程序将立即退出",
			"blockTime", blockTime)
		os.Exit(1)
	}

	if epochDuration == 0 {
		d.logger.Error("💀 epochDuration大小为0，这是严重配置错误，程序将立即退出",
			"epochDuration", epochDuration)
		os.Exit(1)
	}

	epochSize := uint64(epochDuration / blockTime)

	if epochSize == 0 {
		d.logger.Error("💀 计算出的epoch大小为0，这是严重配置错误，程序将立即退出",
			"epochDuration", epochDuration,
			"blockTime", blockTime)
		os.Exit(1)
	}

	return epochSize
}
