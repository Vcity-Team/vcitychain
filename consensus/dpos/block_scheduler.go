package dpos

import (
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// BlockchainInterface 是区块链接口，用于获取当前区块头
type BlockchainInterface interface {
	Header() *types.Header
}

// BlockScheduler 是固定时间窗口调度器，用于TRON模式的区块生产调度
type BlockScheduler struct {
	blockWindow          time.Duration
	genesisTime          time.Time
	delegateCount        int
	blockchain           BlockchainInterface
	consensusSwitchHeight uint64
	logger               hclog.Logger
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
	// 获取创世区块时间
	genesisTime := time.Now()
	if blockchain != nil {
		genesisHeader := blockchain.Header()
		if genesisHeader != nil {
			genesisTime = time.Unix(int64(genesisHeader.Timestamp), 0)
		}
	}

	return &BlockScheduler{
		blockWindow:          blockWindow,
		genesisTime:          genesisTime,
		delegateCount:        delegateCount,
		blockchain:            blockchain,
		consensusSwitchHeight: consensusSwitchHeight,
		logger:                logger,
		lastLogTime:          make(map[string]time.Time),
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

	// 🆕 添加详细的调试日志（10秒间隔，避免刷屏）
	bs.logOnceWithInterval("should_produce_block_now_detail", 10*time.Second, "debug",
		"🔍 ShouldProduceBlockNow 详细检查",
		"myAddress", myAddress.String(),
		"expectedValidator", expectedValidator.String(),
		"isMatch", isMatch,
		"currentSlot", currentSlot,
		"currentValidatorIndex", currentValidatorIndex,
		"activeValidatorCount", activeValidatorCount,
		"blockNumber", blockNumber,
		"timeSinceGenesis", timeSinceGenesis.String(),
		"blockWindow", bs.blockWindow.String(),
			"genesisTime", bs.genesisTime.Format("2006-01-02 15:04:05.000"),
			"now", now.Format("2006-01-02 15:04:05.000"),
		"validators", func() []string {
			var vs []string
			for i, v := range validators {
				vs = append(vs, fmt.Sprintf("[%d]%s", i, v.String()))
			}
			return vs
		}())

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

// shouldProduceBlockNow 检查当前节点是否应该现在出块
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

		// 🆕 获取验证者列表并过滤故障验证者
		activeValidators := make([]types.Address, 0, len(r.delegates))

		// 🆕 检查DPoS实例是否存在
		dposInstance, dposExists := GetDPoSInstance("vcity_dpos")
		if !dposExists {
			r.logOnceWithInterval("dpos_instance_not_found", 10*time.Second, "warn",
				"⚠️ DPoS实例不存在，跳过故障过滤，使用所有验证者")
		}

		for _, d := range r.delegates {
			// 获取验证者的故障标志信息
			var faultInfo map[string]interface{}
			var isFaulty bool

			// 通过DPoS实例获取故障信息
			if dposExists && dposInstance != nil {
				faultInfo = dposInstance.getValidatorFaultInfo(d.Address)
				if faultInfo != nil && faultInfo["isFaulty"] != nil {
					if v, ok := faultInfo["isFaulty"].(bool); ok {
						isFaulty = v
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
				activeValidators = append(activeValidators, d.Address)
			}
		}

		// 🆕 如果过滤后没有验证者，使用原始列表（避免所有验证者被过滤导致不出块）
		validators := activeValidators
		if len(validators) == 0 {
			r.logOnceWithInterval("all_validators_filtered_fallback", 10*time.Second, "warn",
				"⚠️ 所有验证者被过滤，回退到原始验证者列表",
				"originalCount", len(r.delegates))
			// 回退到原始验证者列表
			validators = make([]types.Address, len(r.delegates))
			for i, d := range r.delegates {
				validators[i] = d.Address
			}
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
			"timestamp", time.Now().Format("15:04:05.000"))

		return result
	}

	// 回退到原有逻辑（不使用blockScheduler时）
	result := r.shouldProduceBlock()

	// 🆕 添加回退逻辑结果日志（使用Debug级别）
	r.logOnceWithInterval("fallback_should_produce_result", 5*time.Second, "debug",
		"🔍 回退逻辑结果",
		"shouldProduce", result,
		"currentBlockNumber", currentBlock.Number,
		"timestamp", time.Now().Format("15:04:05.000"))

	return result
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
