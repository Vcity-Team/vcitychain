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
	// networkHeadResolver 返回用于出块调度的「网络最新区块」头（无 peer 时通常为本地链尖）。
	networkHeadResolver func() *types.Header
	// wallClockSlotAlignment 为 true 时，下一区块时间戳 additionally 对齐墙钟 slot（见 effectiveTimeForNextBlock）。
	wallClockSlotAlignment bool
}

// SetNetworkHeadResolver 设置网络最新区块头解析器（由 dposRuntime 注入）。
func (bs *BlockScheduler) SetNetworkHeadResolver(resolver func() *types.Header) {
	bs.networkHeadResolver = resolver
}

// SetWallClockSlotAlignment 启用/关闭墙钟 slot 对齐（由节点配置 dpos_wall_clock_slot_alignment 注入）。
func (bs *BlockScheduler) SetWallClockSlotAlignment(enabled bool) {
	bs.wallClockSlotAlignment = enabled
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
		blockWindow:            blockWindow,
		genesisTime:            genesisTime,
		delegateCount:          delegateCount,
		blockchain:             blockchain,
		consensusSwitchHeight:  consensusSwitchHeight,
		logger:                 logger,
		lastLogTime:            make(map[string]time.Time),
		wallClockSlotAlignment: true,
	}, nil
}

// SlotAt returns the slot index for a timestamp (same basis as block header / leader election).
func (bs *BlockScheduler) SlotAt(t time.Time) int {
	return bs.slotAt(t)
}

func (bs *BlockScheduler) slotAt(t time.Time) int {
	d := t.Sub(bs.genesisTime)
	if d < 0 {
		return 0
	}
	return int(d / bs.blockWindow)
}

// schedulingReferenceHeader 用于下一区块 slot/时间戳的参照块（网络最新；无网络视图时为本地链尖）。
func (bs *BlockScheduler) schedulingReferenceHeader() *types.Header {
	if bs.networkHeadResolver != nil {
		if h := bs.networkHeadResolver(); h != nil {
			return h
		}
	}
	if bs.blockchain != nil {
		return bs.blockchain.Header()
	}
	return nil
}

// DesignatedProposerForNext 本节点是否为 localTip+1 的轮值 proposer（无日志，供 sync 豁免等热路径）。
func (bs *BlockScheduler) DesignatedProposerForNext(
	localTip uint64,
	myAddress types.Address,
	validators []types.Address,
	isEligible func(types.Address) bool,
) bool {
	orderedValidators := orderValidatorAddressesForLeaderElection(validators)
	n := len(orderedValidators)
	if n == 0 {
		return false
	}
	if localTip+1 < bs.consensusSwitchHeight {
		return false
	}
	idx := bs.ProposerSlotForNextBlock() % n
	expected := orderedValidators[idx]
	if expected != myAddress {
		return false
	}
	if isEligible != nil && !isEligible(expected) {
		return false
	}
	return true
}

// ShouldProduceBlockNow 检查指定地址在当前slot是否应该出块
func (bs *BlockScheduler) ShouldProduceBlockNow(
	myAddress types.Address,
	validators []types.Address,
	blockNumber uint64, // 这是已同步的当前区块号，来自 blockchain.CurrentHeader().Number
	validatorsSource string, // 验证者列表来源（用于日志）
	isEligible func(types.Address) bool, // nil 表示 validators 中地址均可参与轮值
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
	refHeader := bs.schedulingReferenceHeader()
	schedulingTime := bs.effectiveTimeForNextBlock()
	timeSinceGenesis := schedulingTime.Sub(bs.genesisTime)

	activeValidatorCount := len(orderedValidators)
	if activeValidatorCount == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 活跃验证者数量为0")
		return false
	}

	chainSlot := bs.slotAt(schedulingTime)
	wallSlot := bs.slotAt(now.UTC())
	leaderSlot := bs.LeaderElectionSlot()
	// 下一块 miner 由块头 schedulingTime 对应 slot 决定（与 sync 落块、CurrentSlotForNextBlock 一致），
	// 不用 LeaderElectionSlot（墙钟 max）——否则 chainDue 前墙钟已推进时会永久轮空 chain proposer。
	proposerSlot := chainSlot
	currentValidatorIndex := proposerSlot % activeValidatorCount
	expectedValidator := orderedValidators[currentValidatorIndex]

	isMatch := expectedValidator == myAddress
	if isMatch && isEligible != nil && !isEligible(expectedValidator) {
		isMatch = false
	}

	if wallSlot > chainSlot {
		wallIdx := wallSlot % activeValidatorCount
		bs.logOnceWithInterval("leader_slot_wall_ahead", 30*time.Second, "info",
			"⏭️ [出块调度] 墙钟 slot 领先链上调度 slot，空耗已过轮值窗口",
			"leaderSlot", leaderSlot,
			"chainSlot", chainSlot,
			"wallSlot", wallSlot,
			"chainProposer", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
			"wallProposer", fmt.Sprintf("[%d]%s", wallIdx, orderedValidators[wallIdx].String()),
			"nextBlockNumber", nextBlockNumber)
	}

	if !isMatch {
		return false
	}

	// 构建验证者集合完整列表（带索引）
	validatorsList := make([]string, len(orderedValidators))
	for i, v := range orderedValidators {
		marker := ""
		if i == currentValidatorIndex {
			marker = " ← 轮值索引"
		}
		if v == myAddress {
			marker += " ← 本节点"
		}
		validatorsList[i] = fmt.Sprintf("[%d]%s%s", i, v.String(), marker)
	}

	refNum := uint64(0)
	refTS := ""
	if refHeader != nil {
		refNum = refHeader.Number
		refTS = time.Unix(int64(refHeader.Timestamp), 0).UTC().Format("2006-01-02 15:04:05.000")
	}
	bs.logger.Info("📐 [出块调度] 网络最新块时间+blockWindow 确定下一区块 slot",
		"networkRefBlockNumber", refNum,
		"networkRefTimestampUTC", refTS,
		"leaderElectionTimeUTC", schedulingTime.Format("2006-01-02 15:04:05.000"),
		"blockWindow", bs.blockWindow.String(),
		"note", "与 BlockBuilder.Reset 一致；无 peer 时 networkRef=本地链尖")
	bs.logger.Info("🎯 [出块验证] ShouldProduceBlockNow返回true，本节点应该出块",
		"blockNumber", blockNumber,
		"nextBlockNumber", nextBlockNumber,
		"proposerSlot", proposerSlot,
		"leaderSlot", leaderSlot,
		"chainSlot", chainSlot,
		"wallSlot", wallSlot,
		"activeValidatorCount", activeValidatorCount,
		"activeValidatorCountSource", validatorsSource,
		"myAddress", myAddress.String(),
		"expectedValidator", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
		"validatorIndex", currentValidatorIndex,
		"isMatch", isMatch,
		"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
		"now", now.Format("2006-01-02 15:04:05.000"),
		"schedulingTime", schedulingTime.Format("2006-01-02 15:04:05.000"),
		"timestamp", now.Format("15:04:05.000000"),
		"timeSinceGenesis", timeSinceGenesis.String(),
		"blockWindow", bs.blockWindow.String(),
		"validatorsList", validatorsList,
		"note", "用于验证同一时刻只有一个节点出块")

	return true
}

func (bs *BlockScheduler) GetGenesisTime() time.Time {
	return bs.genesisTime
}

// schedulingBaseTimestamp 调度参照时间戳：max(网络/解析参照块, 本地链尖)，避免参照落后于实际父块。
func (bs *BlockScheduler) schedulingBaseTimestamp() uint64 {
	var base uint64
	if ref := bs.schedulingReferenceHeader(); ref != nil {
		base = ref.Timestamp
	}
	if bs.blockchain != nil {
		if local := bs.blockchain.Header(); local != nil && local.Timestamp > base {
			base = local.Timestamp
		}
	}
	return base
}

// wallDueTime 墙钟对齐的下一 slot 起始时刻：genesis + (slotAt(now)+1)*blockWindow。
func (bs *BlockScheduler) wallDueTime() time.Time {
	now := time.Now().UTC()
	slot := bs.slotAt(now)
	return bs.genesisTime.UTC().Add(time.Duration(slot+1) * bs.blockWindow)
}

// chainSchedulingDue 纯链上调度：参照块 timestamp + blockWindow（不含墙钟 wallDue）。
// 用于 EarliestProduceTime；块头时间戳仍用 effectiveTimeForNextBlock。
func (bs *BlockScheduler) chainSchedulingDue() time.Time {
	base := bs.schedulingBaseTimestamp()
	if base == 0 {
		return time.Now().UTC()
	}
	return time.Unix(int64(base), 0).UTC().Add(bs.blockWindow)
}

// effectiveTimeForNextBlock 下一区块头时间戳：max(参照+blockWindow[, 墙钟 slot 对齐])。
// 未启用墙钟时仅为链上调度；启用时为 max(chainDue, wallDue)，与 BlockBuilder.Reset 一致。
func (bs *BlockScheduler) effectiveTimeForNextBlock() time.Time {
	base := bs.schedulingBaseTimestamp()
	if base == 0 {
		if bs.wallClockSlotAlignment {
			return bs.wallDueTime()
		}
		return time.Now().UTC()
	}
	chainDue := time.Unix(int64(base), 0).UTC().Add(bs.blockWindow)
	if !bs.wallClockSlotAlignment {
		return chainDue
	}
	wallDue := bs.wallDueTime()
	if wallDue.After(chainDue) {
		return wallDue
	}
	return chainDue
}

// EarliestProduceTime 本节点最早可开始下一轮出块的 UTC 时刻：
// max(链上调度 chainSchedulingDue, 本地上次成功提交+blockWindow)。
// 不使用 effectiveTimeForNextBlock/wallDue：墙钟下一 slot 起点恒在未来，会导致 now<earliest 永真。
func (bs *BlockScheduler) EarliestProduceTime(lastLocalProduce time.Time) time.Time {
	chainDue := bs.chainSchedulingDue()
	if lastLocalProduce.IsZero() {
		return chainDue
	}
	intervalDue := lastLocalProduce.UTC().Add(bs.blockWindow)
	if intervalDue.After(chainDue) {
		return intervalDue
	}
	return chainDue
}

// CurrentSlotForNextBlock returns the slot index for the next produced block (same basis as header Timestamp).
func (bs *BlockScheduler) CurrentSlotForNextBlock() int {
	return bs.slotAt(bs.effectiveTimeForNextBlock())
}

// ProposerSlotForNextBlock 下一块合法 proposer 的 slot（= 块头 schedulingTime 对应 slot，与 miner 一致）。
func (bs *BlockScheduler) ProposerSlotForNextBlock() int {
	return bs.CurrentSlotForNextBlock()
}

// LeaderElectionSlot 决定「谁可以出下一块」的 slot：max(链上调度 slot, 墙钟 slot)。
// 链时间落后墙钟时，离线 SR 的 slot 在墙钟上被空耗过去，下一在线 SR 获得出块权（不顺延、不 pickLeader 扫描）。
// 块头时间戳用 effectiveTimeForNextBlock（链上或墙钟对齐），与 CurrentSlotForNextBlock 一致。
func (bs *BlockScheduler) LeaderElectionSlot() int {
	chainSlot := bs.CurrentSlotForNextBlock()
	wallSlot := bs.slotAt(time.Now().UTC())
	if wallSlot > chainSlot {
		return wallSlot
	}
	return chainSlot
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
	if !r.designatedProduceSyncExempt(local) {
		r.nudgeSyncIfBehind(local)
	}

	// 方案2：使用读锁快速检查lastProducedSlot（如果使用blockScheduler）
	if r.config.blockScheduler != nil {
		leaderSlot := r.config.blockScheduler.LeaderElectionSlot()

		// 使用读锁快速检查
		r.lock.RLock()
		lastSlot := r.lastProducedSlot
		r.lock.RUnlock()

		// 如果当前 leader 轮值 slot 已经出过块，跳过
		if lastSlot >= 0 && lastSlot == leaderSlot {
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

		// 严格轮流：仅 leaderSlot%N 对应地址可出块；墙钟领先链上 slot 时空耗已过窗口。
		eligible := dposInstance.productionEligibilityCheckerBase(validatorsFromExtra)
		// sync 可能在查验证者期间写入下一块；出块判定前刷新链尖，避免 blockNumber 与 schedulingReference 不一致。
		if hdr := r.config.blockchain.CurrentHeader(); hdr != nil {
			currentBlock = hdr
			local = hdr.Number
		}
		// 与 ShouldProduceBlockNow / produceBlock 使用同一链尖估算 EarliestProduceTime。
		r.lock.RLock()
		lastWallProduce := r.lastBlockProductionTime
		r.lock.RUnlock()
		if earliest := r.config.blockScheduler.EarliestProduceTime(lastWallProduce); time.Now().UTC().Before(earliest) {
			r.logOnceWithInterval("should_produce_before_earliest", 5*time.Second, "debug",
				"⏳ [出块监测] 未到 EarliestProduceTime，暂不出块",
				"localTip", currentBlock.Number,
				"plannedNextBlockNumber", currentBlock.Number+1,
				"earliestProduceUTC", earliest.Format("2006-01-02 15:04:05.000"),
				"nowUTC", time.Now().UTC().Format("2006-01-02 15:04:05.000"),
				"waitRemaining", earliest.Sub(time.Now().UTC()).String())
			return false
		}
		decisionSlot := r.config.blockScheduler.ProposerSlotForNextBlock()
		decisionLocalTip := currentBlock.Number
		result := r.config.blockScheduler.ShouldProduceBlockNow(
			myAddress, validators, decisionLocalTip, validatorsSource, eligible)

		// 如果返回 true，保存 decisionSlot；如果返回 false，清除 decisionSlot
		r.lock.Lock()
		if result {
			r.decisionSlot = decisionSlot
			r.decisionLocalTip = decisionLocalTip
		} else {
			r.decisionSlot = -1
			r.decisionLocalTip = 0
		}
		r.lock.Unlock()

		if result && r.produceDecisionObsoletedByChainAdvance(decisionLocalTip) {
			r.lock.Lock()
			r.decisionSlot = -1
			r.decisionLocalTip = 0
			r.lock.Unlock()
			return false
		}
		if hdr := r.config.blockchain.CurrentHeader(); hdr != nil {
			local = hdr.Number
		}
		if result && r.blockProductionIfBehindTrustedCanonical(local) {
			return false
		}
		return result
	}

	return false
}
