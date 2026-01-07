package dpos

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

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
) *BlockScheduler {
	var genesisTime time.Time
	if blockchain == nil {
		logger.Error("❌ 区块链实例不可用，无法初始化BlockScheduler")
		os.Exit(1)
	}

	if consensusSwitchHeight <= 0 {
		logger.Error("❌ 共识切换高度未配置或为0，无法初始化BlockScheduler", "consensusSwitchHeight", consensusSwitchHeight)
		os.Exit(1)
	}

	// 使用父区块（consensusSwitchHeight - 1）的时间戳作为genesisTime
	parentHeight := consensusSwitchHeight - 1
	parentHeader, ok := blockchain.GetHeaderByNumber(parentHeight)
	if !ok || parentHeader == nil {
		logger.Error("❌ 无法获取创世时间-共识切换高度父区块的区块头的时间戳作为genesisTime",
			"consensusSwitchHeight", consensusSwitchHeight,
			"parentHeight", parentHeight)
		os.Exit(1)
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
	}
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

	nextBlockNumber := blockNumber + 1
	if nextBlockNumber < bs.consensusSwitchHeight {
		return false
	}

	now := time.Now()
	timeSinceGenesis := now.Sub(bs.genesisTime)
	currentSlot := int(timeSinceGenesis / bs.blockWindow)

	// 计算当前slot应该出块的验证者索引
	activeValidatorCount := len(validators)
	if activeValidatorCount == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 活跃验证者数量为0")
		return false
	}

	currentValidatorIndex := currentSlot % activeValidatorCount

	if currentValidatorIndex >= len(validators) {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者索引超出范围",
			"currentValidatorIndex", currentValidatorIndex,
			"validatorsCount", len(validators))
		return false
	}

	expectedValidator := validators[currentValidatorIndex]
	isMatch := expectedValidator == myAddress

	// 构建验证者集合完整列表（带索引）
	validatorsList := make([]string, len(validators))
	for i, v := range validators {
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
		bs.logger.Info("🎯 [出块验证] ShouldProduceBlockNow返回true，本节点应该出块",
			"blockNumber", blockNumber, // 这个 blockNumber 来自 currentBlock.Number（已同步的区块号）
			"nextBlockNumber", nextBlockNumber, // 下一个应生产的区块号（blockNumber + 1）
			"currentSlot", currentSlot, // 基于时间计算的当前slot（第103行）
			"activeValidatorCount", activeValidatorCount,
			"activeValidatorCountSource", validatorsSource, // 验证者列表来源
			"myAddress", myAddress.String(),
			"expectedValidator", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
			"validatorIndex", currentValidatorIndex, // 基于slot计算的验证者索引（第113行）
			"isMatch", isMatch,
			"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
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
	// 检查是否落后，如果落后则先同步再出块
	if r.config.dposBackend != nil {
		networkLatest := r.getNetworkLatestBlockNumber()
		if networkLatest > currentBlock.Number {
			return false
		}
	}

	// 方案2：使用读锁快速检查lastProducedSlot（如果使用blockScheduler）
	if r.config.blockScheduler != nil {
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot := int(timeSinceGenesis / blockWindow)

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
			r.logger.Error("❌ 从数据库查询的验证者集合为空",
				"blockNumber", currentBlock.Number)
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

		// 在调用 ShouldProduceBlockNow 之前，计算并保存 decisionSlot
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		decisionSlot := int(timeSinceGenesis / blockWindow)

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
