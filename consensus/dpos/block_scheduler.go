package dpos

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// orderValidatorAddressesForLeaderElection 将验证者地址按字节升序排列，
// 与 ShouldProduceBlockNow 的规则一致，保证 slot % N 在所有节点映射到同一领导者。
func orderValidatorAddressesForLeaderElection(addrs []types.Address) []types.Address {
	ordered := append([]types.Address(nil), addrs...)
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(ordered[i][:], ordered[j][:]) < 0
	})
	return ordered
}

// BlockchainInterface 是区块链接口，用于获取当前区块头
type BlockchainInterface interface {
	Header() *types.Header
	GetHeaderByNumber(number uint64) (*types.Header, bool)
}

type BlockScheduler struct {
	blockWindow           time.Duration
	genesisTime           time.Time
	delegateCount         int
	blockchain            BlockchainInterface
	consensusSwitchHeight uint64
	logger                hclog.Logger
	lastLogTime           map[string]time.Time
	logMutex              sync.Mutex
}

func NewBlockScheduler(
	blockWindow time.Duration,
	delegateCount int,
	blockchain BlockchainInterface,
	consensusSwitchHeight uint64,
	logger hclog.Logger,
) (*BlockScheduler, error) {
	var genesisTime time.Time
	if blockchain == nil {
		logger.Error("❌ 区块链实例不可用，无法初始化BlockScheduler")
		return nil, fmt.Errorf("blockchain instance is nil, cannot initialize BlockScheduler")
	}

	if consensusSwitchHeight <= 0 {
		logger.Error("❌ 共识切换高度未配置或为0，无法初始化BlockScheduler", "consensusSwitchHeight", consensusSwitchHeight)
		return nil, fmt.Errorf("consensus switch height is not configured or is 0 (got %d)", consensusSwitchHeight)
	}

	// 使用父区块（consensusSwitchHeight - 1）的时间戳作为genesisTime
	parentHeight := consensusSwitchHeight - 1
	parentHeader, ok := blockchain.GetHeaderByNumber(parentHeight)
	if !ok || parentHeader == nil {
		logger.Error("❌ 无法获取创世时间-共识切换高度父区块的区块头的时间戳作为genesisTime",
			"consensusSwitchHeight", consensusSwitchHeight,
			"parentHeight", parentHeight)
		return nil, fmt.Errorf("cannot get parent block header at height %d for genesis time (consensus switch height %d)", parentHeight, consensusSwitchHeight)
	} else {
		// 使用父区块的时间戳作为genesisTime
		genesisTime = time.Unix(int64(parentHeader.Timestamp), 0)
		logger.Info("✅ 成功获取创世时间-使用共识切换高度父区块时间戳作为genesisTime",
			"consensusSwitchHeight", consensusSwitchHeight,
			"parentHeight", parentHeight,
			"genesisTime", genesisTime.Format("2006-01-02 15:04:05.000"),
			"parentBlockTimestamp", parentHeader.Timestamp)
	}

	return &BlockScheduler{
		blockWindow:           blockWindow,
		genesisTime:           genesisTime,
		delegateCount:         delegateCount,
		blockchain:            blockchain,
		consensusSwitchHeight: consensusSwitchHeight,
		logger:                logger,
		lastLogTime:           make(map[string]time.Time),
	}, nil
}

// nextBlockSchedulingInfo 与 BlockBuilder.Reset / effectiveTimeForNextBlock 一致：max(父块时间+blockWindow, now.UTC())
type nextBlockSchedulingInfo struct {
	ParentNumber       uint64
	ParentTimestampUTC time.Time
	ParentPlusWindow   time.Time // 父块时间 + blockWindow，即 max 左分支候选
	NowUTC             time.Time
	EffectiveTime      time.Time // max 结果，与下一区块头 Timestamp 对齐
	// MaxUsesParentChain 为 true 表示 EffectiveTime==ParentPlusWindow（链上时间轴不早于 now）
	MaxUsesParentChain bool
}

func (bs *BlockScheduler) nextBlockSchedulingInfo() nextBlockSchedulingInfo {
	var info nextBlockSchedulingInfo
	info.NowUTC = time.Now().UTC()
	head := bs.blockchain.Header()
	if head == nil {
		info.EffectiveTime = info.NowUTC
		info.MaxUsesParentChain = false
		return info
	}
	info.ParentNumber = head.Number
	info.ParentTimestampUTC = time.Unix(int64(head.Timestamp), 0).UTC()
	info.ParentPlusWindow = info.ParentTimestampUTC.Add(bs.blockWindow)
	if info.ParentPlusWindow.Before(info.NowUTC) {
		info.EffectiveTime = info.NowUTC
		info.MaxUsesParentChain = false
	} else {
		info.EffectiveTime = info.ParentPlusWindow
		info.MaxUsesParentChain = true
	}
	return info
}

// ShouldProduceBlockNow 检查指定地址在当前slot是否应该出块
func (bs *BlockScheduler) ShouldProduceBlockNow(
	myAddress types.Address,
	validators []types.Address,
	blockNumber uint64, // 这是已同步的当前区块号，来自 blockchain.CurrentHeader().Number
	validatorsSource string, // 验证者列表来源（用于日志）
) bool {
	if len(validators) == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者列表为空")
		return false
	}

	// 出块调度必须确定性一致：
	// 即使validators成员集合一致，不同节点也可能因为本地数据库/缓存/过滤差异导致validators顺序不一致，
	// 从而 currentSlot % N 映射到不同地址，触发分叉。
	// 这里强制按地址字节升序规范化顺序，保证slot->leader映射在所有节点一致。
	orderedValidators := orderValidatorAddressesForLeaderElection(validators)

	nextBlockNumber := blockNumber + 1
	if nextBlockNumber < bs.consensusSwitchHeight {
		return false
	}

	now := time.Now()
	schedInfo := bs.nextBlockSchedulingInfo()
	schedulingTime := schedInfo.EffectiveTime
	timeSinceGenesis := schedulingTime.Sub(bs.genesisTime)
	currentSlot := int(timeSinceGenesis / bs.blockWindow)

	// 计算当前slot应该出块的验证者索引
	activeValidatorCount := len(orderedValidators)
	if activeValidatorCount == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 活跃验证者数量为0")
		return false
	}

	currentValidatorIndex := currentSlot % activeValidatorCount

	if currentValidatorIndex >= len(orderedValidators) {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者索引超出范围",
			"currentValidatorIndex", currentValidatorIndex,
			"validatorsCount", len(orderedValidators))
		return false
	}

	expectedValidator := orderedValidators[currentValidatorIndex]
	isMatch := expectedValidator == myAddress

	// 构建验证者集合完整列表（带索引）
	validatorsList := make([]string, len(orderedValidators))
	for i, v := range orderedValidators {
		marker := ""
		if i == currentValidatorIndex {
			marker = " ← 计算出的索引"
		}
		if v == myAddress {
			marker += " ← 本节点"
		}
		validatorsList[i] = fmt.Sprintf("[%d]%s%s", i, v.String(), marker)
	}

	// 只有当本地节点应该出块时才打印详细日志（每次出块都打印，因为频率已经很低）
	if isMatch {
		maxBranch := "utc_now"
		if schedInfo.MaxUsesParentChain {
			maxBranch = "parent_timestamp_plus_blockWindow"
		}
		bs.logger.Info("📐 [出块调度] max(父块时间+blockWindow, now.UTC) 用于下一区块 slot",
			"parentBlockNumber", schedInfo.ParentNumber,
			"parentTimestampUTC", schedInfo.ParentTimestampUTC.Format("2006-01-02 15:04:05.000"),
			"parentPlusWindowUTC", schedInfo.ParentPlusWindow.Format("2006-01-02 15:04:05.000"),
			"nowUTC", schedInfo.NowUTC.Format("2006-01-02 15:04:05.000"),
			"effectiveSchedulingTimeUTC", schedInfo.EffectiveTime.Format("2006-01-02 15:04:05.000"),
			"maxBranch", maxBranch,
			"blockWindow", bs.blockWindow.String(),
			"note", "与 BlockBuilder.Reset 一致；maxBranch=parent_* 表示链上时间轴不早于本机 UTC")
		bs.logger.Info("🎯 [出块验证] ShouldProduceBlockNow返回true，本节点应该出块",
			"blockNumber", blockNumber, // 这个 blockNumber 来自 currentBlock.Number（已同步的区块号）
			"nextBlockNumber", nextBlockNumber, // 下一个应生产的区块号（blockNumber + 1）
			"currentSlot", currentSlot, // 与下一区块头 timestamp 一致的 slot（effectiveTimeForNextBlock）
			"activeValidatorCount", activeValidatorCount,
			"activeValidatorCountSource", validatorsSource, // 验证者列表来源
			"myAddress", myAddress.String(),
			"expectedValidator", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
			"validatorIndex", currentValidatorIndex, // 基于slot计算的验证者索引（第113行）
			"isMatch", isMatch,
			"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
			"schedulingTime", schedulingTime.Format("2006-01-02 15:04:05.000"),
			"timestamp", now.Format("15:04:05.000000"),
			"timeSinceGenesis", timeSinceGenesis.String(),
			"blockWindow", bs.blockWindow.String(),
			"validatorsList", validatorsList,
			"note", "用于验证同一时刻只有一个节点出块")
	}

	return isMatch
}

func (bs *BlockScheduler) GetGenesisTime() time.Time {
	return bs.genesisTime
}

// effectiveTimeForNextBlock matches BlockBuilder.Reset: the next block header time is
// parentTime+blockWindow, unless that is still before wall clock — then now.UTC().
// Scheduling / decisionSlot must use this so leader slot matches the built header timestamp.
func (bs *BlockScheduler) effectiveTimeForNextBlock() time.Time {
	return bs.nextBlockSchedulingInfo().EffectiveTime
}

// CurrentSlotForNextBlock returns the slot index for the next produced block (same basis as header Timestamp).
func (bs *BlockScheduler) CurrentSlotForNextBlock() int {
	t := bs.effectiveTimeForNextBlock()
	d := t.Sub(bs.genesisTime)
	if d < 0 {
		return 0
	}
	return int(d / bs.blockWindow)
}

// GetBlockWindow 返回区块时间窗口
func (bs *BlockScheduler) GetBlockWindow() time.Duration {
	return bs.blockWindow
}

// StartNewEpoch 启动新的epoch，更新调度器的状态（如果需要）
func (bs *BlockScheduler) StartNewEpoch(epochNumber uint64, currentTime time.Time) {
	bs.logger.Debug("启动新epoch",
		"epochNumber", epochNumber,
		"currentTime", currentTime.Format("2006-01-02 15:04:05"))
}

// logOnceWithInterval 防重复日志函数（自定义间隔）
func (bs *BlockScheduler) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	bs.logMutex.Lock()
	defer bs.logMutex.Unlock()

	now := time.Now()
	if lastTime, exists := bs.lastLogTime[key]; exists {
		// 如果指定间隔内已经记录过相同key的日志，则跳过
		if now.Sub(lastTime) < interval {
			return
		}
	}
	bs.lastLogTime[key] = now

	switch level {
	case "debug":
		bs.logger.Debug(message, args...)
	case "info":
		bs.logger.Info(message, args...)
	case "warn":
		bs.logger.Warn(message, args...)
	case "error":
		bs.logger.Error(message, args...)
	default:
		bs.logger.Info(message, args...)
	}
}

// shouldProduceBlockNow 检查当前节点是否应该现在出块
func (r *dposRuntime) shouldProduceBlockNow() bool {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		// 添加调试日志
		r.logOnceWithInterval("should_produce_block_now_no_header", 10*time.Second, "warn",
			"❌ shouldProduceBlockNow: 无法获取当前区块头",
			"timestamp", time.Now().Format("15:04:05.000"))
		return false
	}
	local := currentBlock.Number
	if r.mustWaitForBootstrapCanonicalSync(local) {
		return false
	}
	// 落后门禁：须与 gossip 水位对齐。仅用 getNetworkLatestBlockNumber 会在 verifiedBest 回落时误判「已追平」；
	// 追平锁用水位 = max(门禁候选, 原始 GetBestPeerNumber)，直到本地高度达到历史最大值。
	if r.config.dposBackend != nil {
		networkLatest := r.getNetworkLatestBlockNumber()
		waterline := networkLatest
		if dpos, ok := r.config.dposBackend.(*DPoS); ok && dpos.syncer != nil {
			rawGossip := dpos.syncer.GetBestPeerNumber()
			if rawGossip > waterline {
				waterline = rawGossip
			}
		}
		waterline = r.capGateWaterlineWithBootstrapRPC(waterline)
		blocked, catchUpTarget := r.updateProductionCatchUpLatch(local, waterline)
		if blocked {
			r.logOnceWithInterval("should_produce_catchup_latch", 5*time.Second, "info",
				"⏸️ 落后追平锁定期，暂不出块（本地须达到曾观测到的门禁/gossip 高度）",
				"localBlockNumber", local,
				"catchUpTargetBlockNumber", catchUpTarget,
				"gateWaterline", waterline,
				"candidateNetworkLatest", networkLatest,
				"lagBlocksToTarget", catchUpTarget-local)
			return false
		}
	}

	// 方案2：使用读锁快速检查lastProducedSlot（如果使用blockScheduler）
	if r.config.blockScheduler != nil {
		currentSlot := r.config.blockScheduler.CurrentSlotForNextBlock()

		// 使用读锁快速检查
		r.lock.RLock()
		lastSlot := r.lastProducedSlot
		r.lock.RUnlock()

		// 如果当前 slot 已经出过块，跳过
		if lastSlot >= 0 && lastSlot == currentSlot {
			return false
		}
	}

	// 添加详细的调试日志（使用Debug级别）
	r.logOnceWithInterval("should_produce_block_now_debug", 5*time.Second, "debug",
		"🔍 shouldProduceBlockNow 开始检查",
		"currentBlockNumber", currentBlock.Number,
		"hasBlockScheduler", r.config.blockScheduler != nil,
		"delegatesCount", len(r.delegates),
		"timestamp", time.Now().Format("15:04:05.000"))

	if r.config.blockScheduler != nil {
		// 获取本节点地址
		myAddress := types.Address(r.config.Key.Address())

		if r.config.dposBackend == nil {
			r.logger.Error("❌ dposBackend为nil，无法获取验证者集合",
				"blockNumber", currentBlock.Number)
			return false
		}

		dposInstance, ok := r.config.dposBackend.(*DPoS)
		if !ok {
			r.logger.Error("❌ dposBackend类型转换失败，无法获取验证者集合",
				"blockNumber", currentBlock.Number)
			return false
		}

		// 🔧 修改：直接从数据库读取验证者集合，不再从 ExtraData 读取
		validatorsFromExtra, err := dposInstance.GetSortedValidatorsWithLimitFilterFaulty()
		validatorsSource := "database_query_filter_faulty" // 记录验证者列表来源
		if err != nil {
			r.logger.Error("❌ 从数据库查询验证者集合失败",
				"blockNumber", currentBlock.Number,
				"error", err)
			return false
		}
		if len(validatorsFromExtra) == 0 {
			// 检查是否在共识切换高度之前
			isBeforeConsensusSwitch := false
			if dposInstance != nil && dposInstance.config != nil && dposInstance.config.ConsensusSwitchHeight > 0 {
				isBeforeConsensusSwitch = currentBlock.Number < dposInstance.config.ConsensusSwitchHeight
			}

			if isBeforeConsensusSwitch {
				// 在共识切换高度之前，验证者集合为空是正常的
				r.logOnceWithInterval("should_produce_validators_empty_before_switch", 30*time.Second, "debug",
					"ℹ️ 共识切换前验证者集合为空（正常）",
					"blockNumber", currentBlock.Number,
					"consensusSwitchHeight", func() uint64 {
						if dposInstance != nil && dposInstance.config != nil {
							return dposInstance.config.ConsensusSwitchHeight
						}
						return 0
					}())
			} else {
				// 在共识切换高度之后，验证者集合为空是异常情况
				r.logOnceWithInterval("should_produce_validators_empty_after_switch", 10*time.Second, "error",
					"❌ 从数据库查询的验证者集合为空",
					"blockNumber", currentBlock.Number)
			}
			return false
		}

		// 使用从 ExtraData 或实时查询获取的验证者集合
		validators := make([]types.Address, 0, len(validatorsFromExtra))
		for _, v := range validatorsFromExtra {
			validators = append(validators, v.Address)
		}
		if len(validators) == 0 {
			r.logger.Error("❌ shouldProduceBlockNow: 数据库中的验证者集合为空，无法确定出块者",
				"dataSource", validatorsSource,
				"blockNumber", currentBlock.Number)
			return false
		}

		// 与 BlockBuilder 下一区块头时间一致，保存 decisionSlot
		decisionSlot := r.config.blockScheduler.CurrentSlotForNextBlock()

		// 调用改进后的方法（直接比较地址）
		result := r.config.blockScheduler.ShouldProduceBlockNow(myAddress, validators, currentBlock.Number, validatorsSource)

		// 如果返回 true，保存 decisionSlot；如果返回 false，清除 decisionSlot
		r.lock.Lock()
		if result {
			r.decisionSlot = decisionSlot
		} else {
			r.decisionSlot = -1
		}
		r.lock.Unlock()

		return result
	}

	return false
}
