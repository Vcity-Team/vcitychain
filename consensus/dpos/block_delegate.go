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

	// 🔧 修改：直接从数据库读取验证者集合，不再从 ExtraData 读取
	allValidators, err := dposBackend.GetSortedValidatorsWithLimitFilterFaulty()
	validatorsSource := "database_query_filter_faulty" // 记录验证者列表来源
	if err != nil {
		r.logger.Error("❌ getCurrentDelegate: 从数据库查询验证者集合失败", "error", err)
		return types.ZeroAddress
	}
	if len(allValidators) == 0 {
		r.logger.Error("❌ getCurrentDelegate: 从数据库查询的验证者集合为空")
		return types.ZeroAddress
	}

	// 使用从 ExtraData 或实时查询获取的验证者集合
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
