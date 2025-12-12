package dpos

import (
	"fmt"
	"time"
)

// calculateRoundBySlot 基于Slot计算轮次
func (r *dposRuntime) calculateRoundBySlot() uint64 {
	if r.config == nil || r.config.blockScheduler == nil || len(r.delegates) == 0 {
		return 1 // 默认从第1轮开始
	}

	now := time.Now()
	genesisTime := r.config.blockScheduler.GetGenesisTime()
	blockWindow := r.config.blockScheduler.GetBlockWindow()
	timeSinceGenesis := now.Sub(genesisTime)
	currentSlot := int(timeSinceGenesis / blockWindow)

	// 基于slot计算轮次
	round := uint64((currentSlot / len(r.delegates)) + 1)

	r.logger.Debug("🔍 基于slot计算轮次",
		"currentSlot", currentSlot,
		"delegateCount", len(r.delegates),
		"calculatedRound", round,
		"formula", fmt.Sprintf("(%d/%d)+1=%d", currentSlot, len(r.delegates), round))

	return round
}

// calculateInitialRound 根据当前区块号计算初始轮次（保持兼容性）
func (r *dposRuntime) calculateInitialRound() uint64 {
	if r.config != nil && r.config.blockScheduler != nil {
		return r.calculateRoundBySlot()
	}

	// 从 dposBackend 获取 DPoSValidatorsCount
	dposValidatorsCount := uint64(0)
	if dposBackend, ok := r.backend.(*DPoS); ok && dposBackend != nil && dposBackend.config != nil {
		dposValidatorsCount = dposBackend.config.DPoSValidatorsCount
	}
	if dposValidatorsCount == 0 {
		return 1 // 默认从第1轮开始
	}

	// 获取当前区块号
	var currentBlockNumber uint64 = 0
	if r.config.blockchain != nil {
		if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
			currentBlockNumber = currentHeader.Number
		}
	}

	// 计算轮次：每完成一轮（DPoSValidatorsCount个区块）轮次+1
	// 轮次从1开始，所以公式是：1 + (blockNumber - 1) / dposValidatorsCount
	if currentBlockNumber > 0 {
		round := 1 + (currentBlockNumber-1)/dposValidatorsCount
		r.logger.Debug("🔍 根据区块号计算初始轮次",
			"currentBlockNumber", currentBlockNumber,
			"dposValidatorsCount", dposValidatorsCount,
			"calculatedRound", round,
			"formula", fmt.Sprintf("1 + (%d-1)/%d=%d", currentBlockNumber, dposValidatorsCount, round))
		return round
	}

	return 1 // 默认从第1轮开始
}

// updateRoundSilent 静默更新轮次（不打印日志）
func (r *dposRuntime) updateRoundSilent() {
	if r.config == nil || r.config.blockScheduler == nil {
		return
	}
	// 从 dposBackend 获取 DPoSValidatorsCount（这里不需要检查，因为只是获取验证者列表）

	// 使用公共函数获取排序和限制后的验证者
	dposBackend, ok := r.backend.(*DPoS)
	if !ok {
		return
	}
	validators, err := dposBackend.GetSortedValidatorsWithLimit()
	if err != nil {
		return
	}

	// 已删除 currentDelegateIndex 的计算和设置
	// 现在完全通过 getCurrentDelegate() 基于时间slot实时计算
	// 此函数保留用于保持代码结构完整性
	_ = validators // 避免unused variable警告
}

// updateRound 更新轮次（混合方案：区块号触发边界，slot计算轮次）
func (r *dposRuntime) updateRound(blockNumber ...uint64) {
	// 修复：统一使用区块号计算委托者索引，避免不一致
	var currentBlockNumber uint64

	if len(blockNumber) > 0 {
		// 优先使用传入的区块号
		currentBlockNumber = blockNumber[0]
		r.logger.Debug("🔄 使用传入区块号更新委托者索引",
			"blockNumber", currentBlockNumber,
			"currentRound", r.currentRound,
			"timestamp", time.Now().Format("15:04:05.000"))
	} else {
		// 如果没有传入区块号，从CurrentHeader获取
		if r.config != nil && r.config.blockchain != nil {
			if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
				currentBlockNumber = currentHeader.Number
				r.logger.Debug("🔄 使用CurrentHeader区块号更新委托者索引",
					"blockNumber", currentBlockNumber,
					"currentRound", r.currentRound,
					"timestamp", time.Now().Format("15:04:05.000"))
			} else {
				r.logger.Warn("⚠️ 无法获取当前区块头，使用默认值0")
				currentBlockNumber = 0
			}
		} else {
			r.logger.Warn("⚠️ 配置或区块链服务不可用，使用默认值0")
			currentBlockNumber = 0
		}
	}

	// 已删除 currentDelegateIndex 的计算和设置
	// 现在完全通过 getCurrentDelegate() 基于时间slot实时计算
	// 此函数现在只负责更新 currentRound

	// 混合方案：基于区块号触发轮次边界处理，基于slot计算轮次
	// 从 dposBackend 获取 DPoSValidatorsCount
	dposValidatorsCount := uint64(0)
	if dposBackend, ok := r.backend.(*DPoS); ok && dposBackend != nil && dposBackend.config != nil {
		dposValidatorsCount = dposBackend.config.DPoSValidatorsCount
	}
	if r.config != nil && dposValidatorsCount > 0 {
		// 1. 基于区块号触发轮次边界处理（保持原有逻辑）
		if currentBlockNumber > 0 && currentBlockNumber%dposValidatorsCount == 0 {
			r.logger.Debug("🔄 轮次边界触发（基于区块号）",
				"blockNumber", currentBlockNumber,
				"dposValidatorsCount", dposValidatorsCount)

			// 方案1+方案2：轮次边界时处理延迟的验证者集合更新
			if r.backend != nil {
				// 通过类型断言访问DPoS实例
				if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance.pendingValidatorUpdate {
					r.logger.Info("🔄 轮次边界：处理延迟的验证者集合更新")
					if err := dposInstance.updateDelegatesInternal(nil); err != nil {
						r.logger.Error("❌ 轮次边界更新验证者集合失败", "error", err)
					} else {
						r.logger.Info("✅ 轮次边界：验证者集合更新完成")

						// 轮次边界：同步新的验证者集合到runtime
						r.logger.Info("🔄 轮次边界：同步新验证者集合到runtime")
						go func() {
							dposInstance.syncRuntimeDelegatesWithRetry()
						}()
					}
					dposInstance.pendingValidatorUpdate = false
					// 修复：清空被投票的验证者集合
					dposInstance.lastVotedDelegates = nil
				}
			}
		}

		// 2. 🆕 基于slot计算当前轮次（确保准确性）
		if r.config.blockScheduler != nil {
			newRound := r.calculateRoundBySlot()
			if newRound != r.currentRound {
				r.logger.Debug("🔄 轮次更新（基于slot）",
					"oldRound", r.currentRound,
					"newRound", newRound,
					"blockNumber", currentBlockNumber)
				r.currentRound = newRound
			}
		}
	}
}
