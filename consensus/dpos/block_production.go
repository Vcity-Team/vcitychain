package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
)

// preCommitPeerProbeTimeout 提交前 P2P 探测下一高度的超时（改法 C）。
const preCommitPeerProbeTimeout = 300 * time.Millisecond

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

const (
	blockProductionPollInterval   = 500 * time.Millisecond
	blockProductionRecheckDelay   = 10 * time.Millisecond
	blockProductionFallbackDelay  = 50 * time.Millisecond
	blockProductionMaxDesignatedWait = 30 * time.Second
)

// continuousBlockMonitoring 持续区块监测：ticker 与 produceTimer 任意唤醒均做出块判定，避免 Sleep+default 丢窗口。
func (r *dposRuntime) continuousBlockMonitoring() {
	ticker := time.NewTicker(blockProductionPollInterval)
	defer ticker.Stop()

	produceTimer := time.NewTimer(0)
	defer produceTimer.Stop()

	for {
		select {
		case <-r.closeCh:
			r.logger.Info("🛑 停止区块监测")
			return
		case <-ticker.C:
			r.updateRoundSilent()
		case <-produceTimer.C:
		}
		r.runBlockProductionOnce()
		r.rearmProduceTimer(produceTimer)
	}
}

// runBlockProductionOnce 单次出块监测：判定 + 生产或 debug 日志。
func (r *dposRuntime) runBlockProductionOnce() {
	r.logDesignatedProposerEarliestReached()

	shouldProduce := r.shouldProduceBlockNow()
	if shouldProduce {
		if err := r.produceBlock(); err != nil {
			r.logger.Error("出块失败", "error", err)
		}
		return
	}

	validatorsFromExtra := validator.AccountSet{}
	validatorsSource := "unknown"
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok && dposInstance != nil {
			if validators, err := dposInstance.GetSortedValidatorsWithLimitFilterFaulty(); err == nil && len(validators) > 0 {
				validatorsFromExtra = validators
				validatorsSource = "database_query_filter_faulty"
			}
		}
	}

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

// logDesignatedProposerEarliestReached 轮值 proposer 到达 EarliestProduceTime 时打一条 Info，便于区分「未判定」与「判定 false」。
func (r *dposRuntime) logDesignatedProposerEarliestReached() {
	if r.config == nil || r.config.blockScheduler == nil || r.config.blockchain == nil {
		return
	}
	if !r.isDesignatedProposerForNext() {
		return
	}
	hdr := r.config.blockchain.CurrentHeader()
	if hdr == nil {
		return
	}
	r.lock.RLock()
	lastWall := r.lastBlockProductionTime
	r.lock.RUnlock()
	nowUTC := time.Now().UTC()
	earliest := r.config.blockScheduler.EarliestProduceTime(lastWall)
	if nowUTC.Before(earliest) {
		return
	}
	plannedNext := hdr.Number + 1
	r.logOnceWithInterval(
		fmt.Sprintf("proposer_earliest_reached_%d", plannedNext),
		24*time.Hour,
		"info",
		"🔔 [出块追踪] earliest 已到，开始出块判定",
		"plannedNext", plannedNext,
		"localTip", hdr.Number,
		"earliestProduceWallUTC", earliest.Format("2006-01-02 15:04:05.000"),
		"nowUTC", nowUTC.Format("2006-01-02 15:04:05.000"),
	)
}

// produceWakeDuration 下次 produceTimer 唤醒间隔（不 blocking sleep）。
func (r *dposRuntime) produceWakeDuration() time.Duration {
	if r.config == nil || r.config.blockScheduler == nil {
		return blockProductionFallbackDelay
	}
	r.lock.RLock()
	lastWall := r.lastBlockProductionTime
	r.lock.RUnlock()
	wait := time.Until(r.config.blockScheduler.EarliestProduceTime(lastWall))
	if wait <= 0 {
		return blockProductionRecheckDelay
	}
	if r.isDesignatedProposerForNext() {
		if wait > blockProductionMaxDesignatedWait {
			return blockProductionPollInterval
		}
		return wait
	}
	if wait > blockProductionPollInterval {
		return blockProductionPollInterval
	}
	return wait
}

// rearmProduceTimer 在每次监测 tick 后重置 produceTimer。
func (r *dposRuntime) rearmProduceTimer(t *time.Timer) {
	if t == nil {
		return
	}
	d := r.produceWakeDuration()
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// isDesignatedProposerForNext 本节点是否为 localTip+1 的轮值 proposer。
func (r *dposRuntime) isDesignatedProposerForNext() bool {
	if r.config == nil || r.config.blockScheduler == nil || r.config.Key == nil || r.config.blockchain == nil {
		return false
	}
	hdr := r.config.blockchain.CurrentHeader()
	if hdr == nil || r.localHasCanonicalBlock(hdr.Number+1) {
		return false
	}
	myAddress := types.Address(r.config.Key.Address())
	dpos, ok := r.config.dposBackend.(*DPoS)
	if !ok || dpos == nil {
		return false
	}
	set, err := dpos.GetSortedValidatorsWithLimitFilterFaulty()
	if err != nil || len(set) == 0 {
		return false
	}
	addrs := make([]types.Address, len(set))
	for i, v := range set {
		addrs[i] = v.Address
	}
	eligible := dpos.productionEligibilityCheckerBase(set)
	return r.config.blockScheduler.DesignatedProposerForNext(hdr.Number, myAddress, addrs, eligible)
}

// produceBlock 生产区块
func (r *dposRuntime) produceBlock() error {
	var currentSlot int = -1
	if r.config.blockScheduler != nil {
		currentSlot = r.config.blockScheduler.LeaderElectionSlot()

		r.lock.RLock()
		lastSlot := r.lastProducedSlot
		lastWallProduce := r.lastBlockProductionTime
		r.lock.RUnlock()

		// 如果当前 leader 轮值 slot 已经出过块，跳过
		if lastSlot >= 0 && lastSlot == currentSlot {
			return nil
		}
		earliest := r.config.blockScheduler.EarliestProduceTime(lastWallProduce)
		if nowUTC := time.Now().UTC(); nowUTC.Before(earliest) {
			r.logOnceWithInterval("produce_block_before_earliest", 2*time.Second, "info",
				"⏳ [produceBlock] 未到最早出块时刻，跳过",
				"nowUTC", nowUTC.Format("2006-01-02 15:04:05.000"),
				"earliestProduceUTC", earliest.Format("2006-01-02 15:04:05.000"),
				"waitRemaining", earliest.Sub(nowUTC).String())
			return nil
		}
	}

	// 获取当前区块
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock != nil {
		r.lock.RLock()
		decisionLocalTip := r.decisionLocalTip
		r.lock.RUnlock()
		if decisionLocalTip > 0 && r.produceDecisionObsoletedByChainAdvance(decisionLocalTip) {
			r.lock.Lock()
			r.decisionSlot = -1
			r.decisionLocalTip = 0
			r.lock.Unlock()
			return nil
		}
		designatedExempt := r.designatedProduceSyncExempt(currentBlock.Number)
		skipPreProduceProbe := designatedExempt && r.lagBehindCanonicalGate(currentBlock.Number) == 0
		if r.config != nil && r.config.dposBackend != nil {
			if dpos, ok := r.config.dposBackend.(*DPoS); ok && dpos.syncer != nil {
				probeTO := r.preProducePeerProbeTimeout()
				sawPeerFork := false
				if !skipPreProduceProbe && probeTO > 0 && dpos.syncer.TryProbeCanonicalNextBeforeProduce(probeTO) {
					r.logger.Info("🔭 【出块前 P2P 探测】已从 peer 写入或可衔接下一高度，放弃本轮本地出块",
						"localTip", currentBlock.Number,
						"plannedNextBlockNumber", currentBlock.Number+1,
						"probeTimeout", probeTO.String(),
						"designatedExempt", designatedExempt)
					return nil
				}
				if probeTO > 0 {
					sawPeerFork = dpos.syncer.PreProduceProbeSawFork()
				}
				if probeTO > 0 && !dpos.syncer.PreProduceAllowLocalBuild(sawPeerFork) {
					r.logger.Info("⏸️ 【出块前跳过出块】boot hash 多数表决不允许本地构建",
						"localTip", currentBlock.Number,
						"plannedNextBlockNumber", currentBlock.Number+1,
						"sawPeerFork", sawPeerFork)
					return nil
				}
				if probeTO > 0 {
					r.logger.Info("🔭 【出块前 P2P 探测】未从 peer 拉到可衔接下一高度，继续本地出块",
						"localTip", currentBlock.Number,
						"plannedNextBlockNumber", currentBlock.Number+1,
						"probeTimeout", probeTO.String(),
						"designatedExempt", designatedExempt,
						"skipPreProduceProbe", skipPreProduceProbe)
				}
			}
		}
		if hdr := r.config.blockchain.CurrentHeader(); hdr != nil {
			if decisionLocalTip > 0 && hdr.Number >= decisionLocalTip+1 {
				r.lock.Lock()
				r.decisionSlot = -1
				r.decisionLocalTip = 0
				r.lock.Unlock()
				r.logger.Info("⏭️ 【出块跳过】P2P 探测后链尖已推进，放弃本地出块",
					"decisionLocalTip", decisionLocalTip,
					"currentLocalTip", hdr.Number)
				return nil
			}
			currentBlock = hdr
		}
		if r.preProduceTrustedCanonicalCheck(currentBlock.Number) {
			return nil
		}
		if r.blockProductionIfBehindTrustedCanonical(currentBlock.Number) {
			return nil
		}
	}

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
		buildStartSlot = r.config.blockScheduler.LeaderElectionSlot()
		buildStartTime = time.Now()
	}

	// 🔧 严格比对：检查构建开始时slot是否与判断时slot一致
	if r.config.blockScheduler != nil && buildStartSlot >= 0 {
		r.lock.RLock()
		decisionSlot := r.decisionSlot
		r.lock.RUnlock()

		// 如果 decisionSlot 已设置，进行严格比对
		if decisionSlot >= 0 && buildStartSlot != decisionSlot {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：构建开始时slot已变化",
				"blockNumber", nextBlockNumber,
				"decisionSlot", decisionSlot,
				"buildStartSlot", buildStartSlot,
				"reason", fmt.Sprintf("判断时slot=%d，构建开始时slot=%d，slot已变化", decisionSlot, buildStartSlot))

			// 清除 decisionSlot
			r.lock.Lock()
			r.decisionSlot = -1
			r.decisionLocalTip = 0
			r.lock.Unlock()

			return nil
		}
	}

	// 如果当前区块号大于等于要生产的区块号，说明区块已存在
	if currentBlock.Number >= nextBlockNumber {
		r.logger.Info("⏰ 区块生产被跳过：区块已存在",
			"currentBlockNumber", currentBlock.Number,
			"nextBlockNumber", nextBlockNumber)
		return nil
	}

	// 检查网络上是否已经有这个区块号了（避免重复出块导致分叉）
	networkLatest := r.getNetworkLatestBlockNumber()
	if networkLatest > 0 && networkLatest >= nextBlockNumber {
		r.logger.Info("⏰ 区块生产被跳过：网络上已存在该区块，应先同步",
			"nextBlockNumber", nextBlockNumber,
			"networkLatestBlock", networkLatest,
			"localBlockNumber", currentBlock.Number,
			"reason", "网络上已有该区块号，应通过syncer同步而不是自己生产")
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

	// 检查是否超过 slot 时间；slot 一致性以区块头时间戳为准（勿用墙钟 LeaderElectionSlot，避免构建 <1s 仍被误判跨 slot）。
	if r.config.blockScheduler != nil && buildStartSlot >= 0 {
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		buildDuration := time.Since(buildStartTime)

		if buildDuration > blockWindow {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：构建耗时超过slot时间",
				"blockNumber", nextBlockNumber,
				"buildDuration", buildDuration.String(),
				"blockWindow", blockWindow.String(),
				"buildStartSlot", buildStartSlot,
				"reason", fmt.Sprintf("构建耗时 %v 超过slot时间窗口 %v", buildDuration, blockWindow))
			return nil
		}

		blockTimestamp := time.Unix(int64(block.Block.Header.Timestamp), 0).UTC()
		blockSlot := r.config.blockScheduler.SlotAt(blockTimestamp)
		r.lock.RLock()
		decisionSlot := r.decisionSlot
		r.lock.RUnlock()
		expectedSlot := buildStartSlot
		if decisionSlot >= 0 {
			expectedSlot = decisionSlot
		}
		if blockSlot != expectedSlot {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：区块时间戳 slot 与决策 slot 不一致",
				"blockNumber", nextBlockNumber,
				"buildStartSlot", buildStartSlot,
				"decisionSlot", decisionSlot,
				"blockSlot", blockSlot,
				"expectedSlot", expectedSlot,
				"buildDuration", buildDuration.String(),
				"reason", fmt.Sprintf("区块时间戳 slot=%d，期望 slot=%d", blockSlot, expectedSlot))
			return nil
		}

		r.logger.Info("✅ [produceBlock] slot检查通过，区块时间戳 slot 与决策 slot 一致且未超时",
			"blockNumber", nextBlockNumber,
			"buildStartSlot", buildStartSlot,
			"decisionSlot", decisionSlot,
			"blockSlot", blockSlot,
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
	var savedLeaderSlot int = -1
	// 在提交前再做一次 leader 轮值 slot 校验
	if r.config.blockScheduler != nil {
		// 依据区块头时间戳推算 slot（与 BlockScheduler 一致）
		blockTimestamp := time.Unix(int64(block.Block.Header.Timestamp), 0).UTC()
		blockSlot := r.config.blockScheduler.SlotAt(blockTimestamp)

		// 获取 decisionSlot 进行严格比对
		r.lock.RLock()
		decisionSlot := r.decisionSlot
		r.lock.RUnlock()

		// leader 轮值 slot 须在提交时未变；块头 slot（链上时间）可小于 decisionSlot（墙钟空耗窗口）。
		commitLeaderSlot := r.config.blockScheduler.LeaderElectionSlot()
		if decisionSlot >= 0 && commitLeaderSlot != decisionSlot {
			r.logger.Info("⏰ [produceBlock] 区块被丢弃：leader 轮值 slot 已变化",
				"blockNumber", block.Block.Number(),
				"decisionSlot", decisionSlot,
				"commitLeaderSlot", commitLeaderSlot,
				"blockSlot", blockSlot,
				"blockTimestamp", blockTimestamp.Format("15:04:05.000"),
				"reason", fmt.Sprintf("判断时 leaderSlot=%d，提交时 leaderSlot=%d", decisionSlot, commitLeaderSlot))

			r.lock.Lock()
			r.decisionSlot = -1
			r.decisionLocalTip = 0
			r.lock.Unlock()

			return nil
		}

		r.logger.Info("✅ [produceBlock] 提交前slot检查通过",
			"blockNumber", block.Block.Number(),
			"decisionSlot", decisionSlot,
			"commitLeaderSlot", commitLeaderSlot,
			"blockSlot", blockSlot)

		savedLeaderSlot = decisionSlot
		if savedLeaderSlot < 0 {
			savedLeaderSlot = commitLeaderSlot
		}
		r.lock.Lock()
		r.decisionSlot = -1
		r.decisionLocalTip = 0
		r.lock.Unlock()
	}

	// C：提交前再探测 localTip+1，若 peer 已有可衔接块则放弃提交（避免构建窗口内竞态）
	if r.config != nil && r.config.dposBackend != nil {
		if dpos, ok := r.config.dposBackend.(*DPoS); ok && dpos.syncer != nil {
			commitProbeTO := preCommitPeerProbeTimeout
			if custom := r.preProducePeerProbeTimeout(); custom > 0 && custom < commitProbeTO {
				commitProbeTO = custom
			}
			localTip := r.config.blockchain.CurrentHeader()
			if localTip != nil && dpos.syncer.TryProbeCanonicalNextBeforeProduce(commitProbeTO) {
				r.logger.Info("🔭 【提交前 P2P 探测】已从 peer 拉到可衔接下一高度，放弃提交",
					"localTip", localTip.Number,
					"plannedBlockNumber", block.Block.Number(),
					"plannedBlockHash", block.Block.Hash().String(),
					"probeTimeout", commitProbeTO.String())
				return nil
			}
			if localTip != nil {
				sawPeerFork := dpos.syncer.PreProduceProbeSawFork()
				if !dpos.syncer.PreProduceAllowLocalBuild(sawPeerFork) {
					r.logger.Info("⏸️ 【提交前跳过提交】boot hash 多数表决不允许提交",
						"localTip", localTip.Number,
						"plannedBlockNumber", block.Block.Number(),
						"sawPeerFork", sawPeerFork)
					return nil
				}
				r.logger.Info("🔭 【提交前 P2P 探测】未从 peer 拉到可衔接下一高度，继续提交",
					"localTip", localTip.Number,
					"plannedBlockNumber", block.Block.Number(),
					"probeTimeout", commitProbeTO.String())
			}
		}
	}

	if err := r.config.blockchain.CommitBlock(block); err != nil {
		// 提交失败时也要清除 decisionSlot（如果还没清除）
		if r.config.blockScheduler != nil {
			r.lock.Lock()
			r.decisionSlot = -1
			r.decisionLocalTip = 0
			r.lock.Unlock()
		}
		r.logger.Error("❌ [produceBlock] 区块提交失败", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String(), "error", err)
		return fmt.Errorf("failed to commit block: %w", err)
	}
	blockTS := time.Unix(int64(block.Block.Header.Timestamp), 0).UTC()
	wallNow := time.Now().UTC()
	r.logger.Info("✅ [produceBlock] 区块已提交",
		"blockNumber", block.Block.Number(),
		"blockHash", block.Block.Hash().String(),
		"blockTimestampUnix", block.Block.Header.Timestamp,
		"blockTimestampUTC", blockTS.Format("2006-01-02 15:04:05.000"),
		"commitWallClockUTC", wallNow.Format("2006-01-02 15:04:05.000"),
		"blockTimeLagFromWallClock", wallNow.Sub(blockTS).String())

	if r.config.blockScheduler != nil {
		r.lock.Lock()
		if savedLeaderSlot >= 0 {
			r.lastProducedSlot = savedLeaderSlot
		}
		r.lastBlockProductionTime = time.Now().UTC()
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

// preProducePeerProbeTimeout 出块前 P2P 探测超时；0 使用 syncer 默认（500ms）。
func (r *dposRuntime) preProducePeerProbeTimeout() time.Duration {
	if r.config != nil && r.config.PreProducePeerProbeTimeout > 0 {
		return r.config.PreProducePeerProbeTimeout
	}
	return syncer.DefaultPreProduceProbeTimeout
}

// noteCanonicalTipProductionPace 链尖前进时刷新墙钟出块节流；仅 miner 为本节点时刷新。
func (d *DPoS) noteCanonicalTipProductionPace(block *types.Block) {
	if d == nil || block == nil {
		return
	}
	height := block.Number()
	d.canonicalTipPaceMu.Lock()
	defer d.canonicalTipPaceMu.Unlock()
	if height <= d.lastCanonicalTipPaceAt {
		return
	}
	d.lastCanonicalTipPaceAt = height
	if d.runtime == nil || d.runtime.config == nil || d.runtime.config.Key == nil {
		return
	}
	if len(block.Header.Miner) == 0 {
		return
	}
	myAddr := types.Address(d.runtime.config.Key.Address())
	if types.BytesToAddress(block.Header.Miner) != myAddr {
		return
	}
	now := time.Now().UTC()
	d.runtime.lock.Lock()
	d.runtime.lastBlockProductionTime = now
	d.runtime.lock.Unlock()
}
