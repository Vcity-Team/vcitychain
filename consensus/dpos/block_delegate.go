package dpos

import (
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
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
	
	// 检查当前区块号，判断是否在共识切换高度之前
	currentBlockNumber := uint64(0)
	if r.config != nil && r.config.blockchain != nil {
		if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
			currentBlockNumber = currentHeader.Number
		}
	}
	isBeforeConsensusSwitch := false
	if dposBackend.config != nil && dposBackend.config.ConsensusSwitchHeight > 0 {
		isBeforeConsensusSwitch = currentBlockNumber < dposBackend.config.ConsensusSwitchHeight
	}
	
	if err != nil {
		// 使用频率限制日志
		r.logOnceWithInterval("get_current_delegate_query_error", 10*time.Second, "error",
			"❌ getCurrentDelegate: 从数据库查询验证者集合失败",
			"error", err,
			"blockNumber", currentBlockNumber,
			"isBeforeConsensusSwitch", isBeforeConsensusSwitch)
		return types.ZeroAddress
	}
	if len(allValidators) == 0 {
		// 在共识切换高度之前，验证者集合为空是正常的（还没有投票记录）
		if isBeforeConsensusSwitch {
			// 使用DEBUG级别，并限制频率
			r.logOnceWithInterval("get_current_delegate_empty_before_switch", 30*time.Second, "debug",
				"ℹ️ getCurrentDelegate: 共识切换前验证者集合为空（正常）",
				"blockNumber", currentBlockNumber,
				"consensusSwitchHeight", dposBackend.config.ConsensusSwitchHeight,
				"note", "在7370高度之前，验证者权重为0，这是正常的")
		} else {
			// 在共识切换高度之后，验证者集合为空是异常情况
			r.logOnceWithInterval("get_current_delegate_empty_after_switch", 10*time.Second, "error",
				"❌ getCurrentDelegate: 从数据库查询的验证者集合为空",
				"blockNumber", currentBlockNumber,
				"consensusSwitchHeight", dposBackend.config.ConsensusSwitchHeight)
		}
		return types.ZeroAddress
	}

	// 使用从 ExtraData 或实时查询获取的验证者集合
	validators := allValidators
	if len(validators) == 0 {
		// 这种情况理论上不会发生（上面已经检查过了），但为了安全还是记录
		if isBeforeConsensusSwitch {
			r.logOnceWithInterval("get_current_delegate_validators_empty_before_switch", 30*time.Second, "debug",
				"ℹ️ getCurrentDelegate: 验证者集合为空（共识切换前，正常）",
				"dataSource", validatorsSource,
				"blockNumber", currentBlockNumber)
		} else {
			r.logOnceWithInterval("get_current_delegate_validators_empty_after_switch", 10*time.Second, "error",
				"❌ getCurrentDelegate: 验证者集合为空，无法确定当前委托者",
				"dataSource", validatorsSource,
				"blockNumber", currentBlockNumber)
		}
		return types.ZeroAddress
	}

	actualDelegateCount := len(validators)
	if actualDelegateCount == 0 {
		// 这种情况理论上不会发生，但为了安全还是记录
		if isBeforeConsensusSwitch {
			r.logOnceWithInterval("get_current_delegate_count_zero_before_switch", 30*time.Second, "debug",
				"ℹ️ getCurrentDelegate: 验证者集合为空（共识切换前，正常）",
				"blockNumber", currentBlockNumber)
		} else {
			r.logOnceWithInterval("get_current_delegate_count_zero_after_switch", 10*time.Second, "warn",
				"🔍 getCurrentDelegate: 验证者集合为空",
				"blockNumber", currentBlockNumber)
		}
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

		// 与 ShouldProduceBlockNow 一致：先按地址字节升序再取模（数据库返回顺序≠选举顺序）
		addresses := make([]types.Address, 0, len(validators))
		for _, v := range validators {
			addresses = append(addresses, v.Address)
		}
		orderedAddrs := orderValidatorAddressesForLeaderElection(addresses)
		if len(orderedAddrs) == 0 {
			return types.ZeroAddress
		}
		currentValidatorIndex := currentSlot % len(orderedAddrs)
		delegateAddr := orderedAddrs[currentValidatorIndex]

		var delegate *validator.ValidatorMetadata
		for _, v := range validators {
			if v.Address == delegateAddr {
				delegate = v
				break
			}
		}
		if delegate == nil {
			r.logOnceWithInterval("get_current_delegate_sorted_miss", 30*time.Second, "error",
				"❌ getCurrentDelegate: 排序后的槽位地址不在活跃集合中",
				"delegateAddr", delegateAddr.String(),
				"validatorIndex", currentValidatorIndex,
				"currentSlot", currentSlot)
			return types.ZeroAddress
		}

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

// maxPeerAdvertisedLeadOverTrusted 若 peer 宣称高度比「近期已成功写入」的最高块还高这么多，
// 视为异常（恶意 Status / 错误 gossip），门禁退回 trusted，避免长期误判落后。
const maxPeerAdvertisedLeadOverTrusted = uint64(500_000)

// getNetworkLatestBlockNumber 返回用于「是否已落后于网络」的门禁高度。
//
// GetTrustedPeerNumber：近期从某 peer 成功验证并写入本地的最高块号，大体接近本地链头，不等价于「全网链尖」。
// GetBestPeerNumber：peer 宣称的链尖（gossip），用于发现「别人已有更高块」从而避免重复高度出块。
//
// 旧逻辑在 trusted>0 时只用 trusted、忽略 best，会把门禁高度钉在本地同步水位上，无法发现网络上已有 nextHeight，
// 从而在他人已产出该高度时仍本地出块，造成分叉。
func (r *dposRuntime) getNetworkLatestBlockNumber() uint64 {
	if r.config.dposBackend != nil {
		if dpos, ok := r.config.dposBackend.(*DPoS); ok && dpos.syncer != nil {
			best := dpos.syncer.GetBestPeerNumber()
			trusted := dpos.syncer.GetTrustedPeerNumber()

			switch {
			case best == 0:
				return trusted
			case trusted == 0:
				return best
			case best > trusted+maxPeerAdvertisedLeadOverTrusted:
				return trusted
			default:
				if best > trusted {
					return best
				}
				return trusted
			}
		}
	}
	return 0
}
