package dpos

import (
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

	// 基于时间 slot 计算当前委托者（与 ShouldProduceBlockNow / 块头时间戳同一坐标系）
	if r.config.blockScheduler != nil {
		leaderSlot := r.config.blockScheduler.LeaderElectionSlot()

		// 与 ShouldProduceBlockNow 一致：先按地址字节升序再取模（数据库返回顺序≠选举顺序）
		addresses := make([]types.Address, 0, len(validators))
		for _, v := range validators {
			addresses = append(addresses, v.Address)
		}
		orderedAddrs := orderValidatorAddressesForLeaderElection(addresses)
		if len(orderedAddrs) == 0 {
			return types.ZeroAddress
		}
		idx := leaderSlot % len(orderedAddrs)
		return orderedAddrs[idx]
	}

	r.logger.Error("❌ blockScheduler不可用")
	return types.ZeroAddress
}

// defaultMaxPeerAdvertisedLeadOverTrusted：peer 宣称相对 trusted 写入高度的默认最大可信超前（可通过 runtimeConfig 覆盖）。
const defaultMaxPeerAdvertisedLeadOverTrusted = uint64(8192)

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
// 优先使用 syncer 创世 bootnode median+K=2 共识链尖（不读 dpos_bootstrap_rpc eth_blockNumber）。
// 不可用时回退为下述 gossip 合成逻辑。
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

	if tip := dpos.syncer.GetTrustedCanonicalTip(); tip > 0 {
		return tip
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

	if hdr != nil && r.lastPeerAdvertisedHead > 0 && hdr.Number >= r.lastPeerAdvertisedHead {
		r.lastPeerAdvertisedHead = 0
	}

	return candidate
}

// networkLatestHeaderForScheduling 返回用于出块 slot/时间戳的「网络最新区块」头。
// 高度取自 getNetworkLatestBlockNumber；本地 DB 已有该高度块头则返回，否则回退本地链尖（无 peer 时即单机链）。
func (r *dposRuntime) networkLatestHeaderForScheduling() *types.Header {
	if r.config == nil || r.config.blockchain == nil {
		return nil
	}
	local := r.config.blockchain.CurrentHeader()
	if local == nil {
		return nil
	}
	netNum := r.getNetworkLatestBlockNumber()
	if netNum == 0 {
		return local
	}
	if h, ok := r.config.blockchain.GetHeaderByNumber(netNum); ok && h != nil {
		return h
	}
	if netNum <= local.Number {
		return local
	}
	return local
}
