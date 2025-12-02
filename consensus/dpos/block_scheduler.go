package dpos

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// BlockchainInterface 是区块链接口，用于获取当前区块头
type BlockchainInterface interface {
	Header() *types.Header
	// GetHeaderByNumber 获取指定区块号的区块头（用于获取创世区块）
	GetHeaderByNumber(number uint64) (*types.Header, bool)
}

// BlockScheduler 是固定时间窗口调度器，用于TRON模式的区块生产调度
type BlockScheduler struct {
	blockWindow           time.Duration
	genesisTime           time.Time
	delegateCount         int
	blockchain            BlockchainInterface
	consensusSwitchHeight uint64
	logger                hclog.Logger
	// 🆕 日志间隔管理
	lastLogTime map[string]time.Time
	logMutex    sync.RWMutex
}

// NewBlockScheduler 创建新的区块调度器
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

	// 优先使用共识切换高度的区块时间戳
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

// logOnceWithInterval 防重复日志函数（自定义间隔）
func (bs *BlockScheduler) logOnceWithInterval(key string, interval time.Duration, level string, message string, args ...interface{}) {
	bs.logMutex.Lock()
	lastTime, exists := bs.lastLogTime[key]
	now := time.Now()

	if !exists || now.Sub(lastTime) >= interval {
		bs.lastLogTime[key] = now
		bs.logMutex.Unlock()

		// 根据级别输出日志
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
			bs.logger.Debug(message, args...)
		}
	} else {
		bs.logMutex.Unlock()
	}
}

// ShouldProduceBlockNow 检查指定地址在当前slot是否应该出块
// 完全基于时间的slot计算：所有节点都使用time.Now()计算slot，重启后虽然slot会跳跃，但所有节点同步跳跃，出块顺序仍然是轮流的
func (bs *BlockScheduler) ShouldProduceBlockNow(
	myAddress types.Address,
	validators []types.Address,
	blockNumber uint64,
	currentBlock *types.Header, // 当前区块头，用于获取时间戳进行时间间隔检查
	validatorsSource string, // 验证者列表来源（用于日志）
) bool {
	if len(validators) == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者列表为空")
		return false
	}

	// 检查是否在共识切换高度之后
	nextBlockNumber := blockNumber + 1
	if nextBlockNumber < bs.consensusSwitchHeight {
		return false
	}

	// 计算当前slot应该出块的验证者索引
	activeValidatorCount := len(validators)
	if activeValidatorCount == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 活跃验证者数量为0")
		return false
	}

	// 1. 完全基于时间计算slot（所有节点都使用time.Now()，NTP同步后所有节点计算的slot一致）
	now := time.Now()
	genesisTime := bs.genesisTime
	blockWindow := bs.blockWindow
	timeSinceGenesis := now.Sub(genesisTime)
	currentSlot := int(timeSinceGenesis / blockWindow)
	if currentSlot < 0 {
		// 如果slot为负数（在共识切换之前），返回false
		return false
	}

	// 2. 计算验证者索引
	currentValidatorIndex := currentSlot % activeValidatorCount

	// 4. 检查当前验证者是否是本节点
	if currentValidatorIndex >= len(validators) {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者索引超出范围",
			"currentValidatorIndex", currentValidatorIndex,
			"validatorsCount", len(validators))
		return false
	}

	expectedValidator := validators[currentValidatorIndex]
	isMatch := expectedValidator == myAddress

	// 🆕 只有当本地节点应该出块时才打印详细日志（每次出块都打印，因为频率已经很低）
	if isMatch {
		// ========== 🆕 详细日志：打印ShouldProduceBlockNow中的验证者列表和验证结果（每次出块都打印） ==========
		bs.logger.Info("🎯 [出块验证] ShouldProduceBlockNow返回true，本节点应该出块",
			"blockNumber", blockNumber,
			"nextBlockNumber", nextBlockNumber,
			"currentSlot", currentSlot,
			"timeSinceGenesis", timeSinceGenesis.String(),
			"currentTime", now.Format("2006-01-02 15:04:05.000"),
			"activeValidatorCount", activeValidatorCount,
			"activeValidatorCountSource", validatorsSource, // 🆕 验证者列表来源
			"myAddress", myAddress.String(),
			"expectedValidator", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
			"validatorIndex", currentValidatorIndex,
			"isMatch", isMatch,
			"consensusSwitchHeight", bs.consensusSwitchHeight,
			"validatorsList", func() []string {
				var vs []string
				for i, v := range validators {
					vs = append(vs, fmt.Sprintf("[%d]%s", i, v.String()))
				}
				return vs
			}(),
			"note", "完全基于时间计算slot，所有节点同步，重启后出块顺序仍然是轮流的")
	}

	return isMatch
}

// GetGenesisTime 返回创世时间
func (bs *BlockScheduler) GetGenesisTime() time.Time {
	return bs.genesisTime
}

// GetBlockWindow 返回区块时间窗口
func (bs *BlockScheduler) GetBlockWindow() time.Duration {
	return bs.blockWindow
}

// StartNewEpoch 启动新的epoch，更新调度器的状态（如果需要）
func (bs *BlockScheduler) StartNewEpoch(epochNumber uint64, currentTime time.Time) {
	// 目前不需要特殊处理，因为调度器完全基于时间slot计算
	// 如果将来需要epoch特定的调度逻辑，可以在这里添加
	bs.logger.Debug("启动新epoch",
		"epochNumber", epochNumber,
		"currentTime", currentTime.Format("2006-01-02 15:04:05"))
}

// getValidatorsFromCurrentBlockExtraData 从当前区块（父区块）的ExtraData获取验证者列表
// 用于 shouldProduceBlockNow，确保所有节点基于同一区块的验证者列表计算
func (r *dposRuntime) getValidatorsFromCurrentBlockExtraData(currentBlock *types.Header) (validator.AccountSet, error) {
	// 获取当前区块的ExtraData
	currentExtra, err := GetIbftExtra(currentBlock.ExtraData)
	if err != nil {
		r.logger.Error("❌ 无法解析当前区块ExtraData",
			"blockNumber", currentBlock.Number,
			"error", err)
		return nil, fmt.Errorf("failed to parse ExtraData for block %d: %w", currentBlock.Number, err)
	}

	// 获取父区块（当前区块的父区块）
	var parent *types.Header
	if currentBlock.Number > 0 {
		parentHeader, exists := r.config.blockchain.GetHeaderByNumber(currentBlock.Number - 1)
		if exists {
			parent = parentHeader
		}
	}

	// 从当前区块的ExtraData获取验证者列表
	// 这会应用当前区块中的验证者变化（包括投票交易）
	validators, err := currentExtra.getValidatorsFromExtraData(
		currentBlock, // 当前区块（区块N）
		parent,       // 父区块（区块N-1）
		nil,          // parents array
		r.config.dposBackend,
		r.logger,
	)

	if err != nil {
		r.logger.Error("❌ 从ExtraData获取验证者列表失败",
			"blockNumber", currentBlock.Number,
			"error", err)
		return nil, fmt.Errorf("failed to get validators from ExtraData for block %d: %w", currentBlock.Number, err)
	}

	if len(validators) == 0 {
		r.logger.Error("❌ ExtraData中的验证者列表为空",
			"blockNumber", currentBlock.Number)
		return nil, fmt.Errorf("validators list is empty in ExtraData for block %d", currentBlock.Number)
	}

	return validators, nil
}

// getValidatorsFromDatabase 从数据库读取验证者列表（备用方案）
func (r *dposRuntime) getValidatorsFromDatabase() (validator.AccountSet, error) {
	if r.config.dposBackend == nil {
		return nil, fmt.Errorf("dpos backend not available")
	}

	dposInstance, ok := r.config.dposBackend.(*DPoS)
	if !ok {
		return nil, fmt.Errorf("invalid dpos backend type")
	}

	// 使用 GetSortedValidatorsWithLimit 获取排序和限制后的验证者列表
	validators, err := dposInstance.GetSortedValidatorsWithLimit()
	if err != nil {
		return nil, fmt.Errorf("failed to get validators from database: %w", err)
	}

	return validators, nil
}

// applyValidatorLimitAndSort 应用配置限制和排序（从 validator.AccountSet 转换为 []types.Address）
func (r *dposRuntime) applyValidatorLimitAndSort(validators validator.AccountSet) []types.Address {
	if len(validators) == 0 {
		return []types.Address{}
	}

	// 转换为地址列表
	addresses := make([]types.Address, 0, len(validators))
	for _, v := range validators {
		addresses = append(addresses, v.Address)
	}

	return addresses
}

// shouldProduceBlockNow 检查当前节点是否应该现在出块
// 🆕 方案2：使用读锁，不阻塞其他检查
func (r *dposRuntime) shouldProduceBlockNow() bool {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		// 🆕 添加调试日志
		r.logOnceWithInterval("should_produce_block_now_no_header", 10*time.Second, "warn",
			"❌ shouldProduceBlockNow: 无法获取当前区块头",
			"timestamp", time.Now().Format("15:04:05.000"))
		return false
	}

	// 🆕 新增：检查是否落后，如果落后则先同步再出块
	if r.config.dposBackend != nil {
		networkLatest := r.getNetworkLatestBlockNumber()
		if networkLatest > currentBlock.Number {
			return false
		}
	}

	// 🆕 添加详细的调试日志（使用Debug级别）
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

		// 优先从数据库获取预先计算的epoch验证者集合
		validatorsFromExtra, err := dposInstance.getEpochValidatorsFromDatabase()
		validatorsSource := "database" // 🆕 记录验证者列表来源
		if err != nil || len(validatorsFromExtra) == 0 {
			// 如果数据库中没有预先计算的验证者集合，回退到实时查询（兼容性）
			// 这种情况可能发生在：1. 第一次启动 2. 数据库被清空 3. 之前的epoch没有保存
			validatorsFromExtra, err = dposInstance.GetSortedValidatorsWithLimit()
			validatorsSource = "realtime_query" // 🆕 更新来源为实时查询
			if err != nil {
				r.logger.Error("❌ 实时查询验证者集合失败",
					"blockNumber", currentBlock.Number,
					"error", err)
				return false
			}
			if len(validatorsFromExtra) == 0 {
				r.logger.Error("❌ 实时查询的验证者集合为空",
					"blockNumber", currentBlock.Number)
				return false
			}
		}

		// 🆕 直接使用数据库中的验证者集合（已在epoch边界过滤）
		validators := make([]types.Address, 0, len(validatorsFromExtra))
		for _, validator := range validatorsFromExtra {
			validators = append(validators, validator.Address)
		}
		if len(validators) == 0 {
			r.logger.Error("❌ shouldProduceBlockNow: 数据库中的验证者集合为空，无法确定出块者",
				"dataSource", validatorsSource,
				"blockNumber", currentBlock.Number)
			return false
		}

		// 🆕 调用改进后的方法（直接比较地址），传递当前区块头用于时间戳计算
		result := r.config.blockScheduler.ShouldProduceBlockNow(myAddress, validators, currentBlock.Number, currentBlock, validatorsSource)

		return result
	}

	return false
}
