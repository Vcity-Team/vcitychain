package dpos

// getSlotForBlock 根据区块号获取对应的slot
func (r *dposRuntime) getSlotForBlock(blockNumber uint64) int {
	if r.config == nil || r.config.blockScheduler == nil {
		return int(blockNumber) // 回退方案
	}

	// 计算区块对应的slot
	consensusSwitchHeight := uint64(0)
	if r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			consensusSwitchHeight = dposInstance.config.ConsensusSwitchHeight
		}
	}

	dposBlockNumber := blockNumber - consensusSwitchHeight
	return int(dposBlockNumber)
}

// getBlockForSlot Slot到区块号的映射函数
func (r *dposRuntime) getBlockForSlot(slot int) uint64 {
	if r.config == nil || r.config.dposBackend == nil {
		return uint64(slot) // 回退方案
	}

	consensusSwitchHeight := uint64(0)
	if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
		consensusSwitchHeight = dposInstance.config.ConsensusSwitchHeight
	}

	return consensusSwitchHeight + uint64(slot)
}

// isEpochEndBlock 检查是否是epoch的最后一个区块
func (r *dposRuntime) isEpochEndBlock(blockNumber uint64) bool {
	// 🆕 修改：基于指定区块号获取epoch信息
	currentEpoch := r.getEpochForBlock(blockNumber)
	if currentEpoch == nil {
		r.logger.Warn("⚠️ 无法获取当前epoch信息", "blockNumber", blockNumber)
		return false
	}

	// 检查是否是epoch的最后一个区块
	// 使用配置计算epoch大小
	epochSize := r.getEpochSize()

	isEpochEnd := currentEpoch.FirstBlockInEpoch+epochSize-1 == blockNumber

	r.logger.Debug("🔍 检查是否是epoch最后一个区块",
		"blockNumber", blockNumber,
		"epochNumber", currentEpoch.Number,
		"firstBlockInEpoch", currentEpoch.FirstBlockInEpoch,
		"epochSize", epochSize,
		"isEpochEnd", isEpochEnd)

	return isEpochEnd
}

// getCurrentEpoch 获取当前epoch信息
func (r *dposRuntime) getCurrentEpoch() *epochMetadata {
	return r.getEpochForBlock(0) // 0表示使用当前区块号
}

// getEpochForSlot 基于Slot计算Epoch
func (r *dposRuntime) getEpochForSlot(slot int) *epochMetadata {
	if r.config == nil || r.config.dposBackend == nil {
		r.logger.Warn("⚠️ 无法获取DPoS实例，使用默认epoch信息")
		return &epochMetadata{
			Number:            1,
			FirstBlockInEpoch: 0,
		}
	}

	dposInstance, ok := r.config.dposBackend.(*DPoS)
	if !ok {
		r.logger.Warn("⚠️ 无法转换为DPoS实例，使用默认epoch信息")
		return &epochMetadata{
			Number:            1,
			FirstBlockInEpoch: 0,
		}
	}

	// 计算epoch信息
	epochSize := r.getEpochSize()
	consensusSwitchHeight := dposInstance.config.ConsensusSwitchHeight

	// 从slot 0开始计算epoch
	if slot < 0 {
		// 在共识切换之前，epoch为0
		currentEpochNumber := uint64(0)
		firstBlockInEpoch := uint64(0)

		r.logger.Debug("🔍 基于slot计算epoch信息（共识切换前）",
			"slot", slot,
			"epochSize", epochSize,
			"currentEpochNumber", currentEpochNumber,
			"firstBlockInEpoch", firstBlockInEpoch)

		return &epochMetadata{
			Number:            currentEpochNumber,
			FirstBlockInEpoch: firstBlockInEpoch,
		}
	}

	// 计算DPoS epoch：从slot 0开始
	currentEpochNumber := (uint64(slot) / epochSize) + 1
	firstBlockInEpoch := consensusSwitchHeight + (currentEpochNumber-1)*epochSize

	r.logger.Debug("🔍 基于slot计算epoch信息",
		"slot", slot,
		"epochSize", epochSize,
		"currentEpochNumber", currentEpochNumber,
		"firstBlockInEpoch", firstBlockInEpoch)

	return &epochMetadata{
		Number:            currentEpochNumber,
		FirstBlockInEpoch: firstBlockInEpoch,
	}
}

// getEpochForBlock 获取指定区块号的epoch信息（保持API兼容性）
func (r *dposRuntime) getEpochForBlock(blockNumber uint64) *epochMetadata {
	// 🆕 内部转换为slot计算
	slot := r.getSlotForBlock(blockNumber)
	epochMetadata := r.getEpochForSlot(slot)

	// 🆕 添加区块号信息以保持兼容性
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			consensusSwitchHeight := dposInstance.config.ConsensusSwitchHeight
			epochSize := r.getEpochSize()

			// 计算第一个区块号
			firstBlockInEpoch := consensusSwitchHeight + (epochMetadata.Number-1)*epochSize
			epochMetadata.FirstBlockInEpoch = firstBlockInEpoch
		}
	}

	r.logger.Debug("🔍 基于区块号查询epoch（内部转换slot）",
		"blockNumber", blockNumber,
		"slot", slot,
		"epochNumber", epochMetadata.Number,
		"firstBlockInEpoch", epochMetadata.FirstBlockInEpoch)

	return epochMetadata
}

