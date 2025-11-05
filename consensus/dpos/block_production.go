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

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("❌ Key不可用，无法启动区块生产",
			"configIsNil", r.config == nil,
			"keyIsNil", r.config != nil && r.config.Key == nil)
		return fmt.Errorf("key not available, cannot start block production")
	}

	blockTime := 2 * time.Second // 默认2秒
	if r.config.PolyBFTConfig != nil {
		blockTime = r.config.PolyBFTConfig.BlockTime.Duration
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
	// 🆕 关键修复：在主循环中周期性更新currentSlot
	// 每隔500ms检查一次，确保时间驱动的slot切换正常工作
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.closeCh:
			r.logger.Info("🛑 停止区块监测")
			return
		case <-ticker.C:
			// 🆕 关键：定期更新currentDelegateIndex，确保轮流出块
			// 使用静默模式，不打印日志（出块时会调用并打印日志）
			r.updateRoundSilent()
		default:
			// 持续监测出块时机
			shouldProduce := r.shouldProduceBlockNow()

			if shouldProduce {
				// 🆕 优化：立即调用produceBlock，不阻塞出块流程
				if err := r.produceBlock(); err != nil {
					r.logger.Error("出块失败", "error", err)
				} else {
					// 🆕 异步收集日志信息（不阻塞出块流程）
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
						r.logOnceWithInterval("should_produce_start", 2*time.Second, "debug",
							"✅ shouldProduceBlockNow返回true，开始出块",
							"timestamp", time.Now().Format("15:04:05.000"),
							"genesisTime", genesisStr,
							"validatorsOrdered", validators)
					}()
				}
			} else {
				// 🆕 添加为什么不应该出块的详细日志
				r.logOnceWithInterval("should_not_produce_debug", 2*time.Second, "info",
					"⏭️ 不应该出块的原因分析",
					"shouldProduceBlockNow", shouldProduce,
					"actualDelegatesCount", len(r.delegates),
					"configDelegateCount", func() uint64 {
						if r.config != nil {
							return r.config.DelegateCount
						}
						return 0
					}(),
					"timestamp", time.Now().Format("15:04:05.000"))
			}

			// 短暂休眠，避免CPU占用过高
			time.Sleep(10 * time.Millisecond) // 10毫秒监测一次
		}
	}
}

// produceBlock 生产区块
// 🆕 方案1+2：缩小锁的粒度，使用读写锁
func (r *dposRuntime) produceBlock() error {
	// 🆕 方案1：将slot检查移到锁外，使用读锁快速检查
	var currentSlot int = -1
	if r.config.blockScheduler != nil {
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot = int(timeSinceGenesis / blockWindow)

		// 🆕 使用读锁快速检查
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

	// 检查当前节点是否为出块者

	// 检查当前节点是否有足够的stake参与出块
	var currentDelegateInfo *validator.ValidatorMetadata

	// 🆕 如果delegates为空，尝试重新加载（使用读锁检查，写锁更新）
	r.lock.RLock()
	delegatesEmpty := r.delegates == nil || len(r.delegates) == 0
	r.lock.RUnlock()

	if delegatesEmpty {
		r.logger.Warn("⚠️ delegates为空，尝试重新加载验证者信息")
		if r.config != nil && r.config.dposBackend != nil {
			currentBlockNumber := uint64(0)
			if r.config.blockchain != nil {
				if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
					currentBlockNumber = currentHeader.Number
				}
			}

			if delegates, err := r.config.dposBackend.GetDelegates(currentBlockNumber, nil); err == nil && len(delegates) > 0 {
				// 🆕 使用写锁更新delegates（快速操作）
				r.lock.Lock()
				r.delegates = delegates
				r.lock.Unlock()
				r.logger.Info("✅ 成功重新加载验证者信息", "count", len(delegates))
			} else {
				r.logger.Error("❌ 无法重新加载验证者信息", "error", err)
			}
		}
	}

	// 🆕 使用读锁读取delegates（避免在构建区块时被阻塞）
	r.lock.RLock()
	delegates := r.delegates
	r.lock.RUnlock()

	for _, delegate := range delegates {
		if delegate.Address == keyAddr {
			currentDelegateInfo = delegate
			// 🆕 修复：确保IsActive为true（验证者应该都是活跃的）
			currentDelegateInfo.IsActive = true
			break
		}
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

	// 当前节点stake检查通过

	// 添加调试日志 - 只有当本节点是当前受托人时才打印
	if currentDelegate == keyAddr {
		r.logger.Debug("🏭 检查区块生产资格",
			"currentDelegate", currentDelegate,
			"keyAddr", keyAddr,
			"currentRound", r.currentRound,
			"delegatesCount", len(delegates),
			"votingPower", currentDelegateInfo.VotingPower.String())
	}

	// 🆕 优化：移除重复的shouldProduceBlockNow检查
	// 原因：shouldProduceBlockNow()已经在continuousBlockMonitoring()中调用（第76行）
	// produceBlock()只在shouldProduceBlockNow()返回true时才会被调用
	// 重复检查浪费时间和资源

	// 检查是否已经有更新的区块
	// currentBlock 已在上面声明过了

	// 计算下一个要生产的区块号
	nextBlockNumber := currentBlock.Number + 1

	// 只对区块1进行特殊检查，防止分叉
	if nextBlockNumber == 1 {
		// 如果我们要生产区块1，检查是否已经有区块1了
		if currentBlock.Number >= 1 {
			// 检查当前区块的矿工地址是否是我们自己
			blockMiner := types.BytesToAddress(currentBlock.Miner)
			keyAddr := types.Address(r.config.Key.Address())

			if blockMiner != keyAddr {
				r.logger.Debug("block 1 was produced by another node, skipping block production",
					"blockMiner", blockMiner, "keyAddr", keyAddr)
				return nil
			} else {
				r.logger.Debug("block 1 was produced by us, continuing with next block")
			}
		}
	} else if currentBlock.Number >= nextBlockNumber {
		// 如果当前区块号大于等于我们要生产的区块号，说明已经有更新的区块了
		r.logger.Debug("block already exists, skipping block production",
			"currentBlockNumber", currentBlock.Number, "nextBlockNumber", nextBlockNumber)
		return nil
	}

	// 🆕 方案1：构建区块和签名收集不在锁内（避免阻塞）
	// 构建新区块（无锁，不阻塞）
	r.logger.Debug("🏗️ DPoS开始构建新区块", "blockNumber", nextBlockNumber)
	block, err := r.buildBlock()
	if err != nil {
		return fmt.Errorf("failed to build block: %w", err)
	}

	// 检查父区块是否已变化（防止其他节点已出块导致分叉）
	if r.config.blockScheduler != nil {
		currentBlock := r.config.blockchain.CurrentHeader()
		if currentBlock.Hash != block.Block.Header.ParentHash {
			r.logger.Warn("⏰ 父区块已变化，其他节点已出块，丢弃当前区块（防止分叉）",
				"blockNumber", block.Block.Number(),
				"expectedParent", block.Block.Header.ParentHash.String(),
				"actualParent", currentBlock.Hash.String())
			return nil // 不提交，避免分叉
		}
	}

	// 提交区块（可能需要锁，取决于blockchain的实现）
	if err := r.config.blockchain.CommitBlock(block); err != nil {
		r.logger.Error("区块提交失败", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String(), "error", err)
		return fmt.Errorf("failed to commit block: %w", err)
	}

	r.logger.Debug("✅ DPoS区块提交成功",
		"blockNumber", block.Block.Number(),
		"blockHash", block.Block.Hash().String()[:16],
		"txs", len(block.Block.Transactions),
		"difficulty", block.Block.Header.Difficulty,
		"gasUsed", block.Block.Header.GasUsed,
		"timestamp", block.Block.Header.Timestamp,
		"delegate", r.config.Key.Address().String()[:16])

	// 🆕 方案1：只在更新状态时使用写锁（时间很短）
	if r.config.blockScheduler != nil && currentSlot >= 0 {
		r.lock.Lock()
		// 🆕 再次检查（防止并发问题）
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

	// 添加事件触发日志跟踪
	r.logger.Debug("🔔 区块提交完成，等待区块链事件触发状态广播", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String())

	// 验证区块是否真正写入区块链
	if writtenBlock, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number()); exists {
		r.logger.Debug("区块验证成功", "blockNumber", writtenBlock.Number, "blockHash", writtenBlock.Hash.String(), "stateRoot", writtenBlock.StateRoot.String())

		// 记录所有交易的详细信息，帮助诊断
		for i, tx := range block.Block.Transactions {
			r.logger.Info("区块交易详情",
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
