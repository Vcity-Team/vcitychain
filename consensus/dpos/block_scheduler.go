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
func (bs *BlockScheduler) ShouldProduceBlockNow(
	myAddress types.Address,
	validators []types.Address,
	blockNumber uint64,
) bool {
	// 🆕 在函数开始就输出所有关键参数（10秒间隔）
	bs.logOnceWithInterval("should_produce_block_now_start", 10*time.Second, "debug",
		"🔍 ShouldProduceBlockNow 函数开始",
		"myAddress", myAddress.String(),
		"blockNumber", blockNumber,
		"consensusSwitchHeight", bs.consensusSwitchHeight,
		"validatorsCount", len(validators),
		"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
		"blockWindow", bs.blockWindow.String())

	if len(validators) == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者列表为空")
		return false
	}

	// 检查是否在共识切换高度之后
	if blockNumber < bs.consensusSwitchHeight {
		bs.logOnceWithInterval("should_produce_block_now_before_switch", 10*time.Second, "debug",
			"❌ ShouldProduceBlockNow: 区块高度未达到共识切换高度",
			"blockNumber", blockNumber,
			"consensusSwitchHeight", bs.consensusSwitchHeight,
			"difference", bs.consensusSwitchHeight-blockNumber)
		return false
	}

	// 获取当前时间
	now := time.Now()
	timeSinceGenesis := now.Sub(bs.genesisTime)
	currentSlot := int(timeSinceGenesis / bs.blockWindow)

	// 计算当前slot应该出块的验证者索引
	activeValidatorCount := len(validators)
	if activeValidatorCount == 0 {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 活跃验证者数量为0")
		return false
	}

	// 计算当前slot应该出块的验证者索引（TRON方式：完全基于时间slot）
	currentValidatorIndex := currentSlot % activeValidatorCount

	// 检查当前验证者是否是本节点
	if currentValidatorIndex >= len(validators) {
		bs.logger.Debug("❌ ShouldProduceBlockNow: 验证者索引超出范围",
			"currentValidatorIndex", currentValidatorIndex,
			"validatorsCount", len(validators))
		return false
	}

	expectedValidator := validators[currentValidatorIndex]
	isMatch := expectedValidator == myAddress

	// ========== 🆕 详细日志：打印ShouldProduceBlockNow中的验证者列表和验证结果（2000ms间隔，便于追踪分叉问题） ==========
	bs.logOnceWithInterval("should_produce_block_now_validators_detail", 2000*time.Millisecond, "info",
		"🔍 ShouldProduceBlockNow 中的验证者列表和验证详情",
		"blockNumber", blockNumber,
		"currentSlot", currentSlot,
		"activeValidatorCount", activeValidatorCount,
		"myAddress", myAddress.String(),
		"expectedValidator", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
		"isMatch", isMatch,
		"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
		"now", now.Format("2006-01-02 15:04:05.000"),
		"timeSinceGenesis", timeSinceGenesis.String(),
		"blockWindow", bs.blockWindow.String(),
		"validatorsList", func() []string {
			var vs []string
			for i, v := range validators {
				vs = append(vs, fmt.Sprintf("[%d]%s", i, v.String()))
			}
			return vs
		}())

	// ========== 🆕 关键验证点：ShouldProduceBlockNow返回true时（用于验证同一时刻只有一个节点出块） ==========
	if isMatch {
		// 🆕 使用200ms间隔，便于追踪分叉问题
		bs.logOnceWithInterval("should_produce_block_now_true", 200*time.Millisecond, "info",
			"🎯 [出块验证] ShouldProduceBlockNow返回true，本节点应该出块",
			"timestamp", now.Format("15:04:05.000000"),
			"myAddress", myAddress.String(),
			"blockNumber", blockNumber,
			"currentSlot", currentSlot,
			"expectedValidator", fmt.Sprintf("[%d]%s", currentValidatorIndex, expectedValidator.String()),
			"validatorIndex", currentValidatorIndex,
			"activeValidatorCount", activeValidatorCount,
			"note", "用于验证同一时刻只有一个节点出块")
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
		r.logger.Warn("⚠️ 无法解析当前区块ExtraData，尝试从数据库读取",
			"blockNumber", currentBlock.Number,
			"error", err)
		// 备用方案：从数据库读取
		return r.getValidatorsFromDatabase()
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
		r.logger.Warn("⚠️ 从ExtraData获取验证者列表失败，尝试从数据库读取",
			"blockNumber", currentBlock.Number,
			"error", err)
		// 备用方案：从数据库读取
		return r.getValidatorsFromDatabase()
	}

	if len(validators) == 0 {
		r.logger.Warn("⚠️ ExtraData中的验证者列表为空，尝试从数据库读取",
			"blockNumber", currentBlock.Number)
		// 备用方案：从数据库读取
		return r.getValidatorsFromDatabase()
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

	// 🆕 方案2：使用读锁快速检查lastProducedSlot（如果使用blockScheduler）
	if r.config.blockScheduler != nil {
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot := int(timeSinceGenesis / blockWindow)

		// 🆕 使用读锁快速检查
		r.lock.RLock()
		lastSlot := r.lastProducedSlot
		r.lock.RUnlock()

		// 如果当前 slot 已经出过块，跳过
		if lastSlot >= 0 && lastSlot == currentSlot {
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

	// 🆕 关键修复：在共识切换高度之前，使用IBFT逻辑，不使用DPoS调度器
	// 必须在调用 blockScheduler.ShouldProduceBlockNow 之前检查
	var consensusSwitchHeight uint64 = 0
	hasDPoSBackend := r.config.dposBackend != nil
	if hasDPoSBackend {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			if dposInstance.config != nil {
				consensusSwitchHeight = dposInstance.config.ConsensusSwitchHeight
			}
		}
	}

	// 🆕 关键：在共识切换高度之前（blockNumber < consensusSwitchHeight），使用IBFT逻辑
	// 注意：consensusSwitchHeight 为 0 时，表示还没有设置共识切换高度，应该使用IBFT
	if consensusSwitchHeight > 0 && currentBlock.Number < consensusSwitchHeight {
		r.logOnceWithInterval("ibft_mode_before_switch", 5*time.Second, "debug",
			"🔍 共识切换高度前，使用IBFT逻辑（不调用DPoS调度器）",
			"currentBlockNumber", currentBlock.Number,
			"consensusSwitchHeight", consensusSwitchHeight,
			"difference", consensusSwitchHeight-currentBlock.Number)
		result := r.shouldProduceBlock()
		r.logOnceWithInterval("ibft_should_produce_result", 5*time.Second, "debug",
			"🔍 IBFT逻辑结果",
			"shouldProduce", result,
			"currentBlockNumber", currentBlock.Number,
			"timestamp", time.Now().Format("15:04:05.000"))
		return result
	}

	// 使用TRON式调度器（完全基于时间，比较地址）
	if r.config.blockScheduler != nil {
		// 获取本节点地址
		myAddress := types.Address(r.config.Key.Address())

		// 🆕 方案1：优先使用预先计算的epoch验证者集合，而不是实时查询
		// 这样新投票的节点不会立即生效，而是等到下一个epoch开始
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
		if err != nil || len(validatorsFromExtra) == 0 {
			// 如果数据库中没有预先计算的验证者集合，回退到实时查询（兼容性）
			// 这种情况可能发生在：1. 第一次启动 2. 数据库被清空 3. 之前的epoch没有保存
			// 🆕 使用日志频率限制，10秒一次
			r.logOnceWithInterval("fallback_to_realtime_query", 10*time.Second, "warn",
				"⚠️ 数据库中没有预先计算的epoch验证者集合，回退到实时查询",
				"blockNumber", currentBlock.Number,
				"error", err)
			validatorsFromExtra, err = dposInstance.GetSortedValidatorsWithLimit()
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

		// 🆕 获取验证者列表并过滤故障验证者
		activeValidators := make([]types.Address, 0, len(validatorsFromExtra))

		// 🆕 检查DPoS实例是否存在
		dposInstance, dposExists := GetDPoSInstance("vcity_dpos")
		if !dposExists {
			r.logOnceWithInterval("dpos_instance_not_found", 10*time.Second, "warn",
				"⚠️ DPoS实例不存在，跳过故障过滤，使用所有验证者")
		}

		for _, validator := range validatorsFromExtra {
			// 获取验证者的故障标志信息
			var faultInfo map[string]interface{}
			var isFaulty bool

			// 通过DPoS实例获取故障信息
			if dposExists && dposInstance != nil {
				faultInfo = dposInstance.getValidatorFaultInfo(validator.Address)
				if faultInfo != nil && faultInfo["isFaulty"] != nil {
					if faultValue, ok := faultInfo["isFaulty"].(bool); ok {
						isFaulty = faultValue
					}
				}
			} else {
				// 如果DPoS实例不存在，使用默认值（不标记为故障）
				faultInfo = map[string]interface{}{
					"isFaulty":     false,
					"missedBlocks": uint64(0),
					"reason":       "DPoS instance not available",
				}
				isFaulty = false
			}

			// 只保留非故障验证者
			if !isFaulty {
				activeValidators = append(activeValidators, validator.Address)
			}
		}

		// 🆕 如果过滤后没有验证者，使用原始列表（避免所有验证者被过滤导致不出块）
		validators := activeValidators
		if len(validators) == 0 {
			r.logOnceWithInterval("all_validators_filtered_fallback", 10*time.Second, "warn",
				"⚠️ 所有验证者被过滤，回退到原始验证者列表",
				"originalCount", len(validatorsFromExtra))
			// 回退到原始验证者列表
			validators = r.applyValidatorLimitAndSort(validatorsFromExtra)
		}

		// 🆕 调用改进后的方法（直接比较地址）
		result := r.config.blockScheduler.ShouldProduceBlockNow(myAddress, validators, currentBlock.Number)

		// 🆕 添加调度器结果日志（使用Debug级别）
		r.logOnceWithInterval("block_scheduler_result", 5*time.Second, "debug",
			"🔍 区块调度器结果",
			"shouldProduce", result,
			"myAddress", myAddress.String(),
			"currentBlockNumber", currentBlock.Number,
			"validatorsCount", len(validators),
			"validatorsSource", "PrecomputedEpoch", // 🆕 标记数据来源：预先计算的epoch验证者集合
			"timestamp", time.Now().Format("15:04:05.000"))

		return result
	}

	return false
}

// shouldProduceBlock 检查当前节点是否应该出块（IBFT模式：基于区块号和验证者索引）
// 🆕 关键：这个函数专门用于IBFT模式，不应该调用DPoS调度器
func (r *dposRuntime) shouldProduceBlock() bool {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		r.logger.Warn("无法获取当前区块头，跳过出块")
		return false
	}

	// 🆕 关键修复：shouldProduceBlock 是IBFT模式的出块检查，不应该调用DPoS调度器
	// 这个函数专门用于共识切换高度之前的IBFT逻辑
	r.logOnceWithInterval("ibft_check_qualification", 10*time.Second, "debug",
		"🔍 使用IBFT模式检查出块资格",
		"currentBlock", currentBlock.Number,
		"delegateCount", r.config.DelegateCount,
		"delegatesCount", len(r.delegates))

	// IBFT逻辑：基于区块号计算当前应该出块的验证者索引
	if len(r.delegates) == 0 {
		r.logger.Warn("⚠️ IBFT模式：验证者列表为空")
		return false
	}

	// 计算当前应该出块的验证者索引（基于区块号）
	validatorIndex := int(currentBlock.Number) % len(r.delegates)
	if validatorIndex < 0 || validatorIndex >= len(r.delegates) {
		r.logger.Warn("⚠️ IBFT模式：验证者索引超出范围",
			"validatorIndex", validatorIndex,
			"delegatesCount", len(r.delegates))
		return false
	}

	// 获取当前应该出块的验证者地址
	expectedValidator := r.delegates[validatorIndex].Address
	myAddress := types.Address(r.config.Key.Address())

	// 检查本节点是否是当前应该出块的验证者
	isMatch := expectedValidator == myAddress

	r.logOnceWithInterval("ibft_check_result", 10*time.Second, "debug",
		"🔍 IBFT模式检查结果",
		"blockNumber", currentBlock.Number,
		"validatorIndex", validatorIndex,
		"expectedValidator", expectedValidator.String(),
		"myAddress", myAddress.String(),
		"isMatch", isMatch)

	return isMatch
}
