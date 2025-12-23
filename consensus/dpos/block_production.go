package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// startBlockProduction 启动区块生产
func (r *dposRuntime) startBlockProduction() error {
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("❌ Key不可用，无法启动区块生产",
			"configIsNil", r.config == nil,
			"keyIsNil", r.config != nil && r.config.Key == nil)
		return fmt.Errorf("key not available, cannot start block production")
	}
	blockTime := r.config.BlockTime.Duration
	if blockTime == 0 {
		r.logger.Error("❌ blockTime 配置为0，无法启动区块生产")
		return fmt.Errorf("blockTime is not configured or is zero")
	}

	r.logger.Debug("🏭 区块生产配置检查",
		"blockTime", blockTime.String(),
		"resourceMonitor", r.resourceMonitor != nil,
		"goroutineManager", func() bool {
			if r.resourceMonitor != nil {
				return r.resourceMonitor.goroutineManager != nil
			}
			return false
		}())

	r.logger.Info("✅ 启动持续区块监测", "blockTime", blockTime.String())

	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("block-production", func() {
			r.continuousBlockMonitoring() // 新增持续监测方法
		})
	} else {
		r.logger.Error("❌ 无法启动区块生产：resourceMonitor或goroutineManager为nil",
			"resourceMonitor", r.resourceMonitor != nil,
			"goroutineManager", func() bool {
				if r.resourceMonitor != nil {
					return r.resourceMonitor.goroutineManager != nil
				}
				return false
			}())
		return fmt.Errorf("resourceMonitor or goroutineManager is nil")
	}
	return nil
}

// continuousBlockMonitoring 持续区块监测
func (r *dposRuntime) continuousBlockMonitoring() {
	// 在主循环中周期性更新currentSlot
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.closeCh:
			r.logger.Info("🛑 停止区块监测")
			return
		case <-ticker.C:
			// 定期更新currentDelegateIndex，确保轮流出块
			r.updateRoundSilent()
		default:
			// 持续监测出块时机
			shouldProduce := r.shouldProduceBlockNow()

			if shouldProduce {
				// 立即调用produceBlock，不阻塞出块流程
				if err := r.produceBlock(); err != nil {
					r.logger.Error("出块失败", "error", err)
				} else {
					// 异步收集日志信息（不阻塞出块流程）
					go func() {
						var genesisStr string
						var validators []string
						if r.config != nil && r.config.blockScheduler != nil {
							genesisStr = r.config.blockScheduler.GetGenesisTime().Format("2006-01-02 15:04:05.000")
							if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
								for _, d := range r.delegates {
									info := dposInstance.getValidatorFaultInfo(d.Address)
									isFaulty := false
									if v, ok := info["isFaulty"].(bool); ok {
										isFaulty = v
									}
									if !isFaulty {
										validators = append(validators, d.Address.String())
									}
								}
							} else {
								for _, d := range r.delegates {
									validators = append(validators, d.Address.String())
								}
							}
						}
						r.logOnceWithInterval("should_produce_start", 1*time.Second, "info",
							"✅ shouldProduceBlockNow返回true，开始出块",
							"timestamp", time.Now().Format("15:04:05.000"),
							"genesisTime", genesisStr,
							"validatorsOrdered", validators)
					}()
				}
			} else {
				// 获取从数据库读取的验证者集合
				validatorsFromExtra := validator.AccountSet{}
				validatorsSource := "unknown"
				if r.config != nil {
					// 直接从数据库读取验证者集合，不再从 ExtraData 读取
					if r.config.dposBackend != nil {
						if dposInstance, ok := r.config.dposBackend.(*DPoS); ok && dposInstance != nil {
							if validators, err := dposInstance.GetSortedValidatorsWithLimitFilterFaulty(); err == nil && len(validators) > 0 {
								validatorsFromExtra = validators
								validatorsSource = "database_query_filter_faulty"
							}
						}
					}
				}

				// 添加为什么不应该出块的详细日志
				r.logOnceWithInterval("should_not_produce_debug", 2*time.Second, "debug",
					"⏭️ 不应该出块的原因分析",
					"shouldProduceBlockNow", shouldProduce,
					"actualDelegatesCount", len(r.delegates),
					"configDPoSValidatorsCount", func() uint64 {
						if r.config != nil && r.config.dposBackend != nil {
							if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
								return dposInstance.config.DPoSValidatorsCount
							}
						}
						return 0
					}(),
					"timestamp", time.Now().Format("15:04:05.000"),
					"slot", func() int {
						if r.config != nil && r.config.blockScheduler != nil {
							now := time.Now()
							genesisTime := r.config.blockScheduler.GetGenesisTime()
							blockWindow := r.config.blockScheduler.GetBlockWindow()
							return int(now.Sub(genesisTime) / blockWindow)
						}
						return -1
					}(),
					"lastProducedSlot", func() int {
						r.lock.RLock()
						defer r.lock.RUnlock()
						return r.lastProducedSlot
					}(),
					"currentDelegate", func() string {
						if r.config != nil && r.config.Key != nil {
							return types.Address(r.config.Key.Address()).String()
						}
						return ""
					}(),
					"expectedDelegate", func() string {
						if r.config != nil {
							if del := r.getCurrentDelegate(); del != (types.Address{}) {
								return del.String()
							}
						}
						return ""
					}(),
					"validatorsFromExtra", func() []string {
						var vs []string
						for i, v := range validatorsFromExtra {
							vs = append(vs, fmt.Sprintf("[%d]%s(vp:%s)", i, v.Address.String(), v.VotingPower.String()))
						}
						return vs
					}(),
					"validatorsFromExtraCount", len(validatorsFromExtra),
					"validatorsSource", validatorsSource)
			}
			time.Sleep(10 * time.Millisecond) // 10毫秒监测一次
		}
	}
}

// produceBlock 生产区块
func (r *dposRuntime) produceBlock() error {
	var currentSlot int = -1
	if r.config.blockScheduler != nil {
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot = int(timeSinceGenesis / blockWindow)

		r.lock.RLock()
		lastSlot := r.lastProducedSlot
		r.lock.RUnlock()

		// 如果当前 slot 已经出过块，跳过
		if lastSlot >= 0 && lastSlot == currentSlot {
			return nil
		}
	}

	// 获取当前区块
	currentBlock := r.config.blockchain.CurrentHeader()

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("❌ key not available, cannot produce block",
			"config", r.config != nil,
			"key", func() bool {
				if r.config != nil {
					return r.config.Key != nil
				}
				return false
			}())
		return fmt.Errorf("key not available, cannot produce block")
	}

	// 检查当前节点是否为出块者
	currentDelegate := r.getCurrentDelegate()
	keyAddr := types.Address(r.config.Key.Address())

	var currentDelegateInfo *validator.ValidatorMetadata

	if r.config != nil && r.config.dposBackend != nil {
		dposInstance, ok := r.config.dposBackend.(*DPoS)
		if ok && dposInstance != nil {
			// 使用与验证时相同的验证者集合获取方法（过滤故障的）-确保生产区块和验证区块时使用相同的验证者集合，避免位图索引不匹配
			dbValidators, err := dposInstance.GetSortedValidatorsWithLimitFilterFaulty()
			if err != nil {
				r.logger.Error("❌ 从数据库读取验证者失败", "error", err)
				return fmt.Errorf("failed to get validators from database: %w", err)
			}

			// 在数据库验证者集合中查找当前节点
			for _, delegate := range dbValidators {
				if delegate.Address == keyAddr {
					currentDelegateInfo = delegate
					// 确保IsActive为true（验证者应该都是活跃的）
					currentDelegateInfo.IsActive = true
					break
				}
			}

			// 如果从数据库找到验证者，更新内存缓存（用于其他非关键逻辑）
			if len(dbValidators) > 0 {
				r.lock.Lock()
				r.delegates = dbValidators
				r.lock.Unlock()
			}
		} else {
			r.logger.Error("❌ dposBackend类型转换失败，无法获取验证者信息")
			return fmt.Errorf("invalid dpos backend type")
		}
	} else {
		r.logger.Error("❌ dposBackend为nil，无法获取验证者信息")
		return fmt.Errorf("dpos backend not available")
	}

	// 如果当前节点stake为0或不活跃，跳过出块
	if currentDelegateInfo == nil || !currentDelegateInfo.IsActive || currentDelegateInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
		var isActiveStr string
		var votingPowerStr string
		if currentDelegateInfo != nil {
			isActiveStr = fmt.Sprintf("%v", currentDelegateInfo.IsActive)
			votingPowerStr = currentDelegateInfo.VotingPower.String()
		} else {
			isActiveStr = "N/A"
			votingPowerStr = "N/A"
		}

		r.logger.Info("❌ 当前节点stake不足或不活跃，跳过出块",
			"keyAddr", keyAddr.String(),
			"isActive", isActiveStr,
			"votingPower", votingPowerStr,
			"currentDelegateInfo", currentDelegateInfo != nil,
			"reason", func() string {
				if currentDelegateInfo == nil {
					return "当前节点不在验证者集合中"
				}
				if !currentDelegateInfo.IsActive {
					return "当前节点不活跃"
				}
				if currentDelegateInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
					return "当前节点投票权重为0"
				}
				return "未知原因"
			}())
		return nil
	}

	if currentDelegate == keyAddr {
		// 获取验证者数量（从内存或数据库）
		delegatesCount := 0
		r.lock.RLock()
		if r.delegates != nil {
			delegatesCount = len(r.delegates)
		}
		r.lock.RUnlock()

		r.logger.Debug("🏭 检查区块生产资格",
			"currentDelegate", currentDelegate,
			"keyAddr", keyAddr,
			"currentRound", r.currentRound,
			"delegatesCount", delegatesCount,
			"votingPower", currentDelegateInfo.VotingPower.String())
	}

	// 计算下一个要生产的区块号
	nextBlockNumber := currentBlock.Number + 1

	// 保存构建开始时的slot和时间（用于超时检查，TRON机制）
	var buildStartSlot int = -1
	var buildStartTime time.Time
	if r.config.blockScheduler != nil {
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		buildStartSlot = int(timeSinceGenesis / blockWindow)
		buildStartTime = now
	}

	// 如果当前区块号大于等于要生产的区块号，说明区块已存在
	if currentBlock.Number >= nextBlockNumber {
		r.logger.Info("⏰ 区块生产被跳过：区块已存在",
			"currentBlockNumber", currentBlock.Number,
			"nextBlockNumber", nextBlockNumber)
		return nil
	}

	// 🔧 修复：设置当前构建的 buildStartSlot，供 buildBlock 内部使用
	r.lock.Lock()
	r.currentBuildStartSlot = buildStartSlot
	r.lock.Unlock()
	defer func() {
		// 构建完成后清除
		r.lock.Lock()
		r.currentBuildStartSlot = -1
		r.lock.Unlock()
	}()

	r.config.blockchain.SetBlockProductionStartTime()
	r.logger.Info("🏗️ [produceBlock] 开始构建新区块", "blockNumber", nextBlockNumber, "buildStartSlot", buildStartSlot)
	block, err := r.buildBlock()
	if err != nil {
		r.logger.Error("❌ [produceBlock] buildBlock失败", "blockNumber", nextBlockNumber, "error", err)
		return fmt.Errorf("failed to build block: %w", err)
	}
	r.logger.Info("✅ [produceBlock] buildBlock完成", "blockNumber", nextBlockNumber, "blockHash", block.Block.Hash().String()[:16], "txs", len(block.Block.Transactions))

	// 记录 buildBlock 返回后的实际耗时（包括等待签名的时间）
	buildEndTime := time.Now()
	totalBuildDuration := buildEndTime.Sub(buildStartTime)
	r.logger.Info("📊 [produceBlock] buildBlock返回后的总耗时统计",
		"blockNumber", nextBlockNumber,
		"totalBuildDuration", totalBuildDuration.String(),
		"buildStartTime", buildStartTime.Format("15:04:05.000000"),
		"buildEndTime", buildEndTime.Format("15:04:05.000000"),
		"note", "包括区块构建和签名收集的总耗时")

	// 检查是否超过slot时间
	if r.config.blockScheduler != nil && buildStartSlot >= 0 {
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlotAfterBuild := int(timeSinceGenesis / blockWindow)
		buildDuration := now.Sub(buildStartTime)

		// 检查构建耗时是否超过slot时间
		if buildDuration > blockWindow {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：构建耗时超过slot时间",
				"blockNumber", nextBlockNumber,
				"buildDuration", buildDuration.String(),
				"blockWindow", blockWindow.String(),
				"buildStartSlot", buildStartSlot,
				"currentSlotAfterBuild", currentSlotAfterBuild,
				"reason", fmt.Sprintf("构建耗时 %v 超过slot时间窗口 %v", buildDuration, blockWindow))
			return nil
		}

		if currentSlotAfterBuild != buildStartSlot {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：slot已变化",
				"blockNumber", nextBlockNumber,
				"buildStartSlot", buildStartSlot,
				"currentSlotAfterBuild", currentSlotAfterBuild,
				"buildDuration", buildDuration.String(),
				"reason", fmt.Sprintf("构建开始时slot=%d，构建完成后slot=%d，slot已变化", buildStartSlot, currentSlotAfterBuild))
			return nil
		}

		r.logger.Info("✅ [produceBlock] slot检查通过，slot未变化且未超时",
			"blockNumber", nextBlockNumber,
			"buildStartSlot", buildStartSlot,
			"currentSlotAfterBuild", currentSlotAfterBuild,
			"buildDuration", buildDuration.String())
	} else {
		reason := "未知原因"
		if r.config.blockScheduler == nil {
			reason = "blockScheduler为nil"
		} else if buildStartSlot < 0 {
			reason = "buildStartSlot < 0（未初始化）"
		}
		r.logger.Info("⚠️ [produceBlock] 跳过slot检查",
			"blockNumber", nextBlockNumber,
			"blockSchedulerIsNil", r.config.blockScheduler == nil,
			"buildStartSlot", buildStartSlot,
			"reason", reason)
	}

	// 检查父区块是否已变化（防止其他节点已出块导致分叉）
	if r.config.blockScheduler != nil {
		currentBlock := r.config.blockchain.CurrentHeader()

		if currentBlock.Hash != block.Block.Header.ParentHash {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：父区块已变化，其他节点已出块（防止分叉）",
				"blockNumber", block.Block.Number(),
				"expectedParent", block.Block.Header.ParentHash.String(),
				"actualParent", currentBlock.Hash.String(),
				"expectedParentNumber", block.Block.Header.Number-1,
				"actualParentNumber", currentBlock.Number,
				"reason", fmt.Sprintf("构建时父区块hash=%s，构建完成后父区块hash=%s，其他节点已出块", block.Block.Header.ParentHash.String()[:16], currentBlock.Hash.String()[:16]))
			return nil // 不提交，避免分叉
		}
		r.logger.Info("✅ [produceBlock] 父区块检查通过，父区块未变化", "blockNumber", block.Block.Number())
	} else {
		r.logger.Info("⚠️ [produceBlock] 跳过父区块检查（blockScheduler为nil）", "blockNumber", block.Block.Number())
	}

	// 提交区块（可能需要锁，取决于blockchain的实现）
	// 在提交前再做一次基于区块 timestamp 的 slot 校验，防止“构建完成后长时间滞后”仍被写入
	if r.config.blockScheduler != nil {
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()

		// 依据区块头时间戳推算它所属的 slot
		blockTimestamp := time.Unix(int64(block.Block.Header.Timestamp), 0)
		timeSinceGenesisForBlock := blockTimestamp.Sub(genesisTime)
		blockSlot := int(timeSinceGenesisForBlock / blockWindow)

		// 以当前时间计算本地 slot
		now := time.Now()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot := int(timeSinceGenesis / blockWindow)

		// 容忍 1 个 slot 漂移（可后续做成配置）
		const slotTolerance = 1

		if currentSlot > blockSlot+slotTolerance {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：提交时已超出允许的slot窗口",
				"blockNumber", block.Block.Number(),
				"blockSlot", blockSlot,
				"currentSlot", currentSlot,
				"slotTolerance", slotTolerance,
				"blockTimestamp", blockTimestamp.Format("15:04:05.000"),
				"now", now.Format("15:04:05.000"),
				"reason", "提交时已落后超过允许的slot漂移")
			return nil
		}

		r.logger.Info("✅ [produceBlock] 提交前slot检查通过",
			"blockNumber", block.Block.Number(),
			"blockSlot", blockSlot,
			"currentSlot", currentSlot,
			"slotTolerance", slotTolerance)
	}

	if err := r.config.blockchain.CommitBlock(block); err != nil {
		r.logger.Error("❌ [produceBlock] 区块提交失败", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String(), "error", err)
		return fmt.Errorf("failed to commit block: %w", err)
	}

	// 只在更新状态时使用写锁（时间很短）
	if r.config.blockScheduler != nil && currentSlot >= 0 {
		r.lock.Lock()
		// 再次检查（防止并发问题）
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		actualSlot := int(timeSinceGenesis / blockWindow)

		// 如果slot已经变化，不更新（避免覆盖新的slot）
		if actualSlot == currentSlot {
			r.lastProducedSlot = currentSlot
		}
		r.lock.Unlock()
	}
	r.logger.Debug("🔔 区块提交完成，等待区块链事件触发状态广播", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String())

	// 验证区块是否真正写入区块链
	if writtenBlock, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number()); exists {
		r.logger.Debug("区块验证成功", "blockNumber", writtenBlock.Number, "blockHash", writtenBlock.Hash.String(), "stateRoot", writtenBlock.StateRoot.String())

		// 记录所有交易的详细信息，帮助诊断
		for i, tx := range block.Block.Transactions {
			r.logger.Debug("区块交易详情",
				"index", i,
				"txHash", tx.Hash.String(),
				"nonce", tx.Nonce,
				"from", tx.From.String(),
				"blockNumber", block.Block.Number())
		}

		// 验证交易查找表是否正确写入（关键验证）
		for i, tx := range block.Block.Transactions {
			// 尝试通过交易哈希查找区块
			if blockHash, found := r.config.blockchain.ReadTxLookup(tx.Hash); found {
				r.logger.Debug("✅ 交易查找表验证成功",
					"index", i,
					"txHash", tx.Hash.String(),
					"blockHash", blockHash.String(),
					"expectedBlockHash", block.Block.Hash().String())
			} else {
				r.logger.Error("❌ 交易查找表验证失败",
					"index", i,
					"txHash", tx.Hash.String(),
					"blockNumber", block.Block.Number(),
					"expectedBlockHash", block.Block.Hash().String())
				return fmt.Errorf("transaction lookup table verification failed for tx %s", tx.Hash.String())
			}
		}
		r.logger.Debug("🎉 所有交易查找表验证完成")
	} else {
		r.logger.Warn("⚠️ 区块写入验证失败，无法获取区块头", "blockNumber", block.Block.Number())
	}

	return nil
}
