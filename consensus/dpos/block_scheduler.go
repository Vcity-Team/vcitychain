package dpos

import (
	"fmt"
	"os"
	"time"

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
	// 日志间隔管理
	lastLogTime map[string]time.Time
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

	// 检查是否在共识切换高度之后
	// 修复：blockNumber是当前区块高度，下一个要生产的区块是blockNumber+1
	// 所以应该检查下一个区块是否达到共识切换高度
	nextBlockNumber := blockNumber + 1
	if nextBlockNumber < bs.consensusSwitchHeight {
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
			"validatorsList", func() []string {
				var vs []string
				for i, v := range validators {
					vs = append(vs, fmt.Sprintf("[%d]%s", i, v.String()))
				}
				return vs
			}(),
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

// shouldProduceBlockNow 检查当前节点是否应该现在出块
// 方案2：使用读锁，不阻塞其他检查
func (r *dposRuntime) shouldProduceBlockNow() bool {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		// 添加调试日志
		r.logOnceWithInterval("should_produce_block_now_no_header", 10*time.Second, "warn",
			"❌ shouldProduceBlockNow: 无法获取当前区块头",
			"timestamp", time.Now().Format("15:04:05.000"))
		return false
	}

	// 新增：检查是否落后，如果落后则先同步再出块
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

		// 优先从数据库获取预先计算的epoch验证者集合
		validatorsFromExtra, err := dposInstance.getEpochValidatorsFromDatabase()
		validatorsSource := "database" // 记录验证者列表来源
		if err != nil || len(validatorsFromExtra) == 0 {
			// 如果数据库中没有预先计算的验证者集合，回退到实时查询（兼容性）
			// 这种情况可能发生在：1. 第一次启动 2. 数据库被清空 3. 之前的epoch没有保存
			validatorsFromExtra, err = dposInstance.GetSortedValidatorsWithLimit()
			validatorsSource = "realtime_query" // 更新来源为实时查询
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
		} else {
			// 检查从数据库读取的验证者数量是否与配置一致
			// 如果数量超过配置，说明数据库里保存的是旧的验证者集合，需要使用配置截取后的集合
			configLimitedValidators, err2 := dposInstance.GetSortedValidatorsWithLimit()
			if err2 == nil && len(configLimitedValidators) > 0 {
				expectedCount := len(configLimitedValidators)
				if len(validatorsFromExtra) != expectedCount {
					r.logger.Warn("⚠️ 数据库中的epoch验证者数量与配置不一致，使用配置截取后的集合",
						"blockNumber", currentBlock.Number,
						"databaseCount", len(validatorsFromExtra),
						"configCount", expectedCount,
						"validatorsSource", "config_limited")
					validatorsFromExtra = configLimitedValidators
					validatorsSource = "config_limited" // 更新来源为配置截取
				}
			}
		}

		// 直接使用数据库中的验证者集合（已在epoch边界过滤）
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

		// 调用改进后的方法（直接比较地址）
		result := r.config.blockScheduler.ShouldProduceBlockNow(myAddress, validators, currentBlock.Number, validatorsSource)

		return result
	}

	return false
}
