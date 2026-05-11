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

// defaultMaxPeerAdvertisedLeadOverTrusted：peer 宣称相对 trusted 写入高度的默认最大可信超前（可通过 runtimeConfig 覆盖）。
const defaultMaxPeerAdvertisedLeadOverTrusted = uint64(8192)

// defaultMaxGossipLeadOverBootstrapRPC：gossip 相对 eth_blockNumber 允许的最大超前（块）；可与创世配置 dpos_max_gossip_lead_over_bootstrap_rpc 覆盖。
const defaultMaxGossipLeadOverBootstrapRPC = uint64(5)

func (r *dposRuntime) maxGossipLeadOverBootstrapRPC() uint64 {
	if r.config != nil && r.config.MaxGossipLeadOverBootstrapRPC > 0 {
		return r.config.MaxGossipLeadOverBootstrapRPC
	}
	return defaultMaxGossipLeadOverBootstrapRPC
}

// gateWaterlineBlendBootstrapRPCGossip 将 bootstrap RPC 链尖与 gossip 宣称合成门禁水位：
// gossip<=rpcTip 时用 rpcTip；gossip>rpcTip 时最高取 min(gossip, rpcTip+lead)，抑制虚报超高。
func gateWaterlineBlendBootstrapRPCGossip(rpcTip, gossipMax, lead uint64) uint64 {
	if gossipMax <= rpcTip {
		return rpcTip
	}
	capAt := rpcTip + lead
	if gossipMax > capAt {
		return capAt
	}
	return gossipMax
}

// gossipPeakForBootstrapGate 与 getNetworkLatestBlockNumber 中门禁合成一致：max(原始 gossip，TTL 内 lastPeerAdvertisedHead)。
func (r *dposRuntime) gossipPeakForBootstrapGate() uint64 {
	if r.config == nil || r.config.dposBackend == nil {
		return 0
	}
	dpos, ok := r.config.dposBackend.(*DPoS)
	if !ok || dpos.syncer == nil {
		return 0
	}
	rawGossipBest := dpos.syncer.GetBestPeerNumber()
	now := time.Now()
	r.networkHeadHintMu.Lock()
	defer r.networkHeadHintMu.Unlock()
	gossipPeak := rawGossipBest
	if r.lastPeerAdvertisedHead > 0 && now.Sub(r.lastPeerAdvertisedHeadAt) < peerAdvertisedHeadTTL && r.lastPeerAdvertisedHead > gossipPeak {
		gossipPeak = r.lastPeerAdvertisedHead
	}
	return gossipPeak
}

func (r *dposRuntime) maxPeerAdvertisedLeadOverTrusted() uint64 {
	if r.config != nil && r.config.MaxPeerAdvertisedLeadOverTrusted > 0 {
		return r.config.MaxPeerAdvertisedLeadOverTrusted
	}
	return defaultMaxPeerAdvertisedLeadOverTrusted
}

// peerAdvertisedHeadTTL 在 best 从有到无的短窗口内，仍用最近观测到的 peer 宣称高度参与「是否落后」门禁，避免仅退回 trusted≈本地 而误产出重复高度。
const peerAdvertisedHeadTTL = 2 * time.Minute

// getNetworkLatestBlockNumber 返回用于「是否已落后于网络」的门禁高度。
//
// 若配置了 dpos_bootstrap_rpc 且 eth_blockNumber 可读：水位 = gateWaterlineBlendBootstrapRPCGossip(rpcTip, gossipPeak, lead)。
// gossipPeak 为 GetBestPeerNumber 与 TTL 内 lastPeerAdvertisedHead 的较大者；lead 默认 5（可配置 dpos_max_gossip_lead_over_bootstrap_rpc）。
// 未配置或 RPC 失败时回退为下述 syncer 合成逻辑；capGateWaterlineWithBootstrapRPC 将水位限制在 [rpcTip, rpcTip+lead]（错链 RPC 远低于本地时不收窄）。
//
// GetTrustedPeerNumber：近期从某 peer 成功验证并写入本地的最高块号，大体接近本地链头，不等价于「全网链尖」。
// GetVerifiedBestPeerNumber：对 Best peer 尝试拉取 local+1 验证后再采信其宣称；失败则返回 0（仍可用 gossip hint）。
// GetBestPeerNumber：原始 gossip 链尖，用于 hint 水印与非门禁逻辑。
//
// 旧逻辑在 trusted>0 时只用 trusted、忽略 best，会把门禁高度钉在本地同步水位上，无法发现网络上已有 nextHeight，
// 从而在他人已产出该高度时仍本地出块，造成分叉。
//
// 另一常见问题：GetBestPeerNumber 短暂为 0（RPC 超时、peer 图抖动）时若仅返回 trusted，会把门禁钉在≈本地高度，误判已追平而出块。
// 因此在 TTL 内保留最近一次可信的 peer 宣称高度，与 candidate 取 max；本地链尖追上该高度后清除。
//
// 若仍把「历史 hint」在 peer 已回落或 best==0 时继续抬高 candidate，会出现：日志一直报落后、不出块，但 syncer 侧 BestPeer<=本地 根本不会 bulk sync（拉取进度不动）。
func (r *dposRuntime) getNetworkLatestBlockNumber() uint64 {
	if r.config == nil || r.config.dposBackend == nil {
		return 0
	}
	dpos, ok := r.config.dposBackend.(*DPoS)
	if !ok || dpos.syncer == nil {
		return 0
	}

	rawGossipBest := dpos.syncer.GetBestPeerNumber()
	verifiedBest := dpos.syncer.GetVerifiedBestPeerNumber()
	trusted := dpos.syncer.GetTrustedPeerNumber()

	maxLead := r.maxPeerAdvertisedLeadOverTrusted()

	var candidate uint64
	switch {
	case verifiedBest == 0:
		candidate = trusted
	case trusted == 0:
		candidate = verifiedBest
	case verifiedBest > trusted+maxLead:
		candidate = trusted
	default:
		if verifiedBest > trusted {
			candidate = verifiedBest
		} else {
			candidate = trusted
		}
	}

	now := time.Now()

	r.networkHeadHintMu.Lock()
	defer r.networkHeadHintMu.Unlock()

	// 仅在非「异常超高宣称」分支更新记忆（仍用原始 gossip，便于 best==0 时 hint）；门禁 candidate 已用 verifiedBest。
	if rawGossipBest > 0 && !(trusted > 0 && rawGossipBest > trusted+maxLead) {
		if rawGossipBest > r.lastPeerAdvertisedHead {
			r.lastPeerAdvertisedHead = rawGossipBest
		}
		r.lastPeerAdvertisedHeadAt = now
	}

	if r.lastPeerAdvertisedHead > 0 && now.Sub(r.lastPeerAdvertisedHeadAt) >= peerAdvertisedHeadTTL {
		r.lastPeerAdvertisedHead = 0
	}

	hdr := r.config.blockchain.CurrentHeader()
	var localNum uint64
	if hdr != nil {
		localNum = hdr.Number
	}

	// 历史 hint 仅在「当前仍能从 gossip 推出更高链尖」或至少「best 仍宣称高于本地」时参与抬高；
	// rawGossipBest==0：无可用宣称链尖，hint 只会制造「门禁落后但无人可拉」的假掉队。
	// rawGossipBest<=local：peer 视图已不高于本地，不应再用过期 hint 压住出块与误导运维日志。
	applyPeerAdvertisedHint := rawGossipBest > 0 && (localNum == 0 || rawGossipBest > localNum)
	if applyPeerAdvertisedHint && r.lastPeerAdvertisedHead > 0 && now.Sub(r.lastPeerAdvertisedHeadAt) < peerAdvertisedHeadTTL {
		if r.lastPeerAdvertisedHead > candidate {
			candidate = r.lastPeerAdvertisedHead
		}
	}

	gossipPeak := rawGossipBest
	if r.lastPeerAdvertisedHead > 0 && now.Sub(r.lastPeerAdvertisedHeadAt) < peerAdvertisedHeadTTL && r.lastPeerAdvertisedHead > gossipPeak {
		gossipPeak = r.lastPeerAdvertisedHead
	}

	if hdr != nil && r.lastPeerAdvertisedHead > 0 && hdr.Number >= r.lastPeerAdvertisedHead {
		r.lastPeerAdvertisedHead = 0
	}

	if tip, rpcGate := r.bootstrapRPCGateTip(); rpcGate {
		lead := r.maxGossipLeadOverBootstrapRPC()
		w := gateWaterlineBlendBootstrapRPCGossip(tip, gossipPeak, lead)
		wFinal := r.capGateWaterlineWithBootstrapRPC(w)
		if hdr != nil {
			r.updateProductionCatchUpLatch(hdr.Number, wFinal)
		}
		return wFinal
	}

	// 追平锁水位：须包含 TTL 内的 lastPeerAdvertisedHead。若仅依赖瞬时 candidate，在 rawGossipBest==0 时
	// applyPeerAdvertisedHint 不会抬高 candidate，但内存水印仍高于本地 → 否则会误判「已追平」而出块分叉。
	if hdr != nil {
		w := candidate
		if rawGossipBest > w {
			w = rawGossipBest
		}
		if r.lastPeerAdvertisedHead > 0 && now.Sub(r.lastPeerAdvertisedHeadAt) < peerAdvertisedHeadTTL && r.lastPeerAdvertisedHead > w {
			w = r.lastPeerAdvertisedHead
		}
		w = r.capGateWaterlineWithBootstrapRPC(w)
		r.updateProductionCatchUpLatch(hdr.Number, w)
	}

	return r.capGateWaterlineWithBootstrapRPC(candidate)
}

// updateProductionCatchUpLatch 在观测到「门禁水位或原始 gossip」高于本地时抬高追平目标；本地达到目标后清除。
// 当 bootstrap RPC 等权威水位低于先前 gossip 虚高形成的追平目标时，同步下调目标，否则会出现「RPC 已是链尖仍卡在旧高度」。
func (r *dposRuntime) updateProductionCatchUpLatch(localHeight, waterline uint64) (blocked bool, catchUpTarget uint64) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.productionCatchUpTarget > 0 && waterline > 0 && waterline < r.productionCatchUpTarget {
		if waterline > localHeight {
			r.productionCatchUpTarget = waterline
		} else {
			r.productionCatchUpTarget = 0
		}
	}

	if waterline > localHeight {
		if waterline > r.productionCatchUpTarget {
			r.productionCatchUpTarget = waterline
		}
	}
	if r.productionCatchUpTarget > 0 && localHeight >= r.productionCatchUpTarget {
		r.productionCatchUpTarget = 0
	}
	catchUpTarget = r.productionCatchUpTarget
	blocked = r.productionCatchUpTarget > 0 && localHeight < r.productionCatchUpTarget
	return blocked, catchUpTarget
}
