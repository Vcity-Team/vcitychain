package dpos

import (
	"fmt"
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
	}
}

// ShouldProduceBlockNow 检查指定地址在当前slot是否应该出块
func (bs *BlockScheduler) ShouldProduceBlockNow(
	myAddress types.Address,
	validators []types.Address,
	blockNumber uint64,
) bool {
	if len(validators) == 0 {
		return false
	}

	// 检查是否在共识切换高度之后
	if blockNumber < bs.consensusSwitchHeight {
		return false
	}

	// 获取当前时间
	now := time.Now()
	timeSinceGenesis := now.Sub(bs.genesisTime)
	currentSlot := int(timeSinceGenesis / bs.blockWindow)

	// 计算当前slot应该出块的验证者索引
	activeValidatorCount := len(validators)
	if activeValidatorCount == 0 {
		return false
	}

	currentValidatorIndex := currentSlot % activeValidatorCount

	// 检查当前验证者是否是本节点
	if currentValidatorIndex >= len(validators) {
		return false
	}

	expectedValidator := validators[currentValidatorIndex]
	return expectedValidator == myAddress
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
			r.logger.Debug("⏸️ 节点落后，先同步再出块",
				"localNumber", currentBlock.Number,
				"networkLatest", networkLatest)
			return false
		}
	}

	// 🆕 添加详细的调试日志
	r.logOnceWithInterval("should_produce_block_now_debug", 5*time.Second, "debug",
		"🔍 shouldProduceBlockNow 开始检查",
		"currentBlockNumber", currentBlock.Number,
		"hasBlockScheduler", r.config.blockScheduler != nil,
		"timestamp", time.Now().Format("15:04:05.000"))

	// 使用TRON式调度器（完全基于时间，比较地址）
	if r.config.blockScheduler != nil {
		// 获取本节点地址
		myAddress := types.Address(r.config.Key.Address())

		// 🆕 获取验证者列表并过滤故障验证者
		activeValidators := make([]types.Address, 0, len(r.delegates))
		r.logOnceWithInterval("memory_validators_before_filter", 10*time.Second, "info",
			"🔍 内存中的验证者列表（过滤前）:", "count", len(r.delegates))

		for i, d := range r.delegates {
			// 获取验证者的故障标志信息
			var faultInfo map[string]interface{}
			var isFaulty bool

			// 通过DPoS实例获取故障信息
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				faultInfo = dposInstance.getValidatorFaultInfo(d.Address)
				if faultInfo["isFaulty"] != nil {
					isFaulty = faultInfo["isFaulty"].(bool)
				}
			} else {
				// 如果DPoS实例不存在，使用默认值
				faultInfo = map[string]interface{}{
					"isFaulty":     false,
					"missedBlocks": uint64(0),
					"reason":       "DPoS instance not available",
				}
				isFaulty = false
			}

			// 打印所有验证者的故障标志信息
			r.logOnceWithInterval(fmt.Sprintf("memory_validator_%d", i), 10*time.Second, "info",
				"👤 内存验证者",
				"index", i+1,
				"address", d.Address.String(),
				"votingPower", d.VotingPower.String(),
				"isFaulty", isFaulty,
				"missedBlocks", faultInfo["missedBlocks"],
				"reason", faultInfo["reason"])

			// 只保留非故障验证者
			if !isFaulty {
				activeValidators = append(activeValidators, d.Address)
			} else {
				r.logOnceWithInterval(fmt.Sprintf("memory_validator_filtered_%s", d.Address.String()), 10*time.Second, "info",
					"🚫 内存中验证者列表过滤掉故障验证者",
					"address", d.Address.String(),
					"missedBlocks", faultInfo["missedBlocks"],
					"reason", faultInfo["reason"])
			}
		}

		// 使用过滤后的验证者列表
		validators := activeValidators
		r.logOnceWithInterval("memory_validators_after_filter", 10*time.Second, "info",
			"✅ 过滤后的内存验证者列表:", "count", len(validators))

		// 🆕 调用改进后的方法（直接比较地址）
		result := r.config.blockScheduler.ShouldProduceBlockNow(myAddress, validators, currentBlock.Number)

		// 🆕 添加调度器结果日志
		r.logOnceWithInterval("block_scheduler_result", 5*time.Second, "debug",
			"🔍 区块调度器结果",
			"shouldProduce", result,
			"myAddress", myAddress.String(),
			"currentBlockNumber", currentBlock.Number,
			"timestamp", time.Now().Format("15:04:05.000"))

		return result
	}

	// 回退到原有逻辑（不使用blockScheduler时）
	result := r.shouldProduceBlock()

	// 🆕 添加回退逻辑结果日志
	r.logOnceWithInterval("fallback_should_produce_result", 5*time.Second, "debug",
		"🔍 回退逻辑结果",
		"shouldProduce", result,
		"currentBlockNumber", currentBlock.Number,
		"timestamp", time.Now().Format("15:04:05.000"))

	return result
}

// shouldProduceBlock 检查当前节点是否应该出块（基于固定时间窗口）
func (r *dposRuntime) shouldProduceBlock() bool {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		r.logger.Warn("无法获取当前区块头，跳过出块")
		return false
	}

	// 🆕 使用固定时间窗口调度器（TRON模式：改用新方法）
	if r.config.blockScheduler != nil {
		myAddress := types.Address(r.config.Key.Address())
		validators := make([]types.Address, len(r.delegates))
		for i, d := range r.delegates {
			validators[i] = d.Address
		}
		// 使用新的 ShouldProduceBlockNow 方法
		return r.config.blockScheduler.ShouldProduceBlockNow(myAddress, validators, currentBlock.Number)
	}

	// 回退到原有的顺序检查（兼容性）
	r.logger.Debug("🔍 使用回退模式检查出块资格",
		"currentBlock", currentBlock.Number,
		"delegateCount", r.config.DelegateCount)

	// 🆕 已删除 currentDelegateIndex 相关的判断
	// 现在完全依赖 shouldProduceBlockNow() 的时间slot计算
	r.logger.Info("🔍 回退模式：使用时间slot计算",
		"currentBlock", currentBlock.Number)
	return false // 回退模式下不依赖索引判断
}
