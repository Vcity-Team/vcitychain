package dpos

import (
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// getCurrentDelegate 获取当前受托人（基于时间slot实时计算）
func (r *dposRuntime) getCurrentDelegate() types.Address {
	// 修复：使用与shouldProduceBlockNow()完全相同的数据源策略，确保一致性
	dposBackend, ok := r.backend.(*DPoS)
	if !ok {
		r.logger.Error("❌ 无法访问数据库，backend类型错误")
		return types.ZeroAddress
	}

	// 优先从数据库获取预先计算的epoch验证者集合（与shouldProduceBlockNow()保持一致）
	allValidators, err := dposBackend.getEpochValidatorsFromDatabase()
	validatorsSource := "database" // 记录验证者列表来源
	if err != nil || len(allValidators) == 0 {
		// 如果数据库中没有预先计算的验证者集合，回退到实时查询（兼容性）
		// 这种情况可能发生在：1. 第一次启动 2. 数据库被清空 3. 之前的epoch没有保存
		r.logOnceWithInterval("get_current_delegate_fallback", 10*time.Second, "debug",
			"⚠️ getCurrentDelegate: 数据库中没有预先计算的epoch验证者集合，回退到实时查询",
			"error", err)
		allValidators, err = dposBackend.GetSortedValidatorsWithLimit()
		validatorsSource = "realtime_query" // 更新来源为实时查询
		if err != nil {
			r.logger.Error("❌ getCurrentDelegate: 实时查询验证者集合失败", "error", err)
			return types.ZeroAddress
		}
		if len(allValidators) == 0 {
			r.logger.Error("❌ getCurrentDelegate: 实时查询的验证者集合为空")
			return types.ZeroAddress
		}
	} else {
		// 检查从数据库读取的验证者数量是否与配置一致
		// 如果数量超过配置，说明数据库里保存的是旧的验证者集合，需要使用配置截取后的集合
		configLimitedValidators, err2 := dposBackend.GetSortedValidatorsWithLimit()
		if err2 == nil && len(configLimitedValidators) > 0 {
			expectedCount := len(configLimitedValidators)
			if len(allValidators) != expectedCount {
				r.logger.Warn("⚠️ getCurrentDelegate: 数据库中的epoch验证者数量与配置不一致，使用配置截取后的集合",
					"databaseCount", len(allValidators),
					"configCount", expectedCount,
					"validatorsSource", "config_limited")
				allValidators = configLimitedValidators
				validatorsSource = "config_limited" // 更新来源为配置截取
			}
		}
	}

	// 直接使用数据库中的验证者集合（已在epoch边界完成过滤）
	validators := allValidators
	if len(validators) == 0 {
		r.logger.Error("❌ getCurrentDelegate: 验证者集合为空，无法确定当前委托者",
			"dataSource", validatorsSource)
		return types.ZeroAddress
	}

	actualDelegateCount := len(validators)
	if actualDelegateCount == 0 {
		r.logger.Warn("🔍 getCurrentDelegate: 验证者集合为空")
		return types.ZeroAddress
	}

	// 基于时间slot实时计算当前委托者
	if r.config.blockScheduler != nil {
		// 获取当前时间
		now := time.Now()
		genesisTime := r.config.blockScheduler.GetGenesisTime()
		blockWindow := r.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot := int(timeSinceGenesis / blockWindow)

		// 计算当前应该出块的验证者索引
		currentValidatorIndex := currentSlot % actualDelegateCount
		delegate := validators[currentValidatorIndex]

		// 使用最新的数据库信息校验活跃状态和投票权重
		latestMeta := delegate
		if validatorsSource != "realtime_query" {
			if freshValidators, err := dposBackend.GetSortedValidatorsWithLimit(); err == nil {
				for _, meta := range freshValidators {
					if meta.Address == delegate.Address {
						latestMeta = meta
						break
				 }
				}
			} else {
				r.logger.Warn("⚠️ getCurrentDelegate: 获取最新验证者权重失败",
					"address", delegate.Address.String(),
					"error", err)
			}
		}

		if latestMeta == nil {
			r.logger.Error("❌ getCurrentDelegate: 无法获取当前委托者元数据",
				"address", delegate.Address.String())
			return types.ZeroAddress
		}

		// 检查受托人是否活跃且有足够的stake
		if !latestMeta.IsActive || latestMeta.VotingPower.Cmp(big.NewInt(0)) <= 0 {
			r.logOnceWithInterval("inactive_delegate", 10*time.Second, "warn",
				"❌ 当前委托者不活跃或票数不足",
				"validatorIndex", currentValidatorIndex,
				"address", latestMeta.Address.String(),
				"isActive", latestMeta.IsActive,
				"votingPower", latestMeta.VotingPower.String(),
				"dataSource", validatorsSource,
				"timestamp", time.Now().Format("15:04:05.000"))
			return types.ZeroAddress
		}

		return latestMeta.Address
	}

	r.logger.Error("❌ blockScheduler不可用")
	return types.ZeroAddress
}

// getNetworkLatestBlockNumber 获取网络最新区块号
func (r *dposRuntime) getNetworkLatestBlockNumber() uint64 {
	if r.config.dposBackend != nil {
		if dpos, ok := r.config.dposBackend.(*DPoS); ok && dpos.syncer != nil {
			// 通过 syncer 获取 bestPeer 的最新区块号
			return dpos.syncer.GetBestPeerNumber()
		}
	}
	return 0
}
