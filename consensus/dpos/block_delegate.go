package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// getCurrentDelegate 获取当前受托人（基于时间slot实时计算）
func (r *dposRuntime) getCurrentDelegate() types.Address {
	// 🆕 使用公共函数获取排序和限制后的验证者
	dposBackend, ok := r.backend.(*DPoS)
	if !ok {
		r.logger.Error("❌ 无法访问数据库，backend类型错误")
		return types.ZeroAddress
	}
	validators, err := dposBackend.GetSortedValidatorsWithLimit()
	if err != nil {
		r.logger.Error("❌ 从数据库读取验证者失败", "error", err)
		return types.ZeroAddress
	}

	actualDelegateCount := len(validators)
	if actualDelegateCount == 0 {
		r.logger.Warn("🔍 getCurrentDelegate: 数据库中没有验证者")
		return types.ZeroAddress
	}

	// 🆕 基于时间slot实时计算当前委托者
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

		// 🆕 添加详细的调试日志
		r.logOnceWithInterval("get_current_delegate_start_check", 10*time.Second, "info", "🔍 getCurrentDelegate 开始检查",
			"delegatesCount", actualDelegateCount,
			"currentSlot", currentSlot,
			"validatorIndex", currentValidatorIndex,
			"delegates_array_detail", func() string {
				if len(validators) == 0 {
					return "delegates数组为空"
				}
				result := "delegates数组: "
				for i, delegate := range validators {
					if i < 5 { // 只显示前5个
						result += fmt.Sprintf("[%d]=%s(vp=%s,active=%v) ", i, delegate.Address.String()[:10], delegate.VotingPower.String(), delegate.IsActive)
					}
				}
				return result
			}(),
			"timestamp", time.Now().Format("15:04:05.000"))

		// 检查受托人是否活跃且有足够的stake
		if !delegate.IsActive || delegate.VotingPower.Cmp(big.NewInt(0)) <= 0 {
			r.logOnceWithInterval("inactive_delegate", 10*time.Second, "warn",
				"❌ 当前委托者不活跃或票数不足",
				"validatorIndex", currentValidatorIndex,
				"address", delegate.Address.String(),
				"isActive", delegate.IsActive,
				"votingPower", delegate.VotingPower.String(),
				"timestamp", time.Now().Format("15:04:05.000"))
			return types.ZeroAddress
		}

		return delegate.Address
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

