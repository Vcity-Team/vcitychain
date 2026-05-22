package syncer

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network/event"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	syncerName        = "syncer"
	syncerProto       = "/syncer/0.2"
	fillGapRetryDelay = 300 * time.Millisecond // 关流后退避再对同一 peer 重开流，避免 stream reset
	// getBlocksInflightRetries：与广告探测 GetBlocks / KickSync 关流并发时短暂冲突，退避重试。
	getBlocksInflightRetries   = 25
	getBlocksInflightBackoff   = 40 * time.Millisecond
	kickSyncPostCloseGraceWait = 100 * time.Millisecond // 让 CloseStream 触发的 producer goroutine 摘掉 inflight 后再 notify
	// syncBestNotAheadWakeInterval：best peer 宣称不高于本地时，仍定时唤醒 Sync。
	// putToPeerMap 对「同区块高度」的状态更新不 notify，若无更高高度事件，否则会永久阻塞在 newStatusCh，
	// 与资源监控观测到的网络领先脱节（同步表现为停住）。
	// 宜 ≤ 出块间隔（如 3s），且须非阻塞 wake（见 wakeSyncAfter）；过长会导致「追一块睡一整段」。
	syncBestNotAheadWakeInterval = 3 * time.Second
	// best_peer_not_ahead 已追平时状态日志节流（避免异步 wake 后 tight loop 刷屏）。
	bestPeerNotAheadLogInterval = 20 * time.Second

	// 反复 bulk 拉取失败（开流失败、超时、验证失败、未完成）则短期不信任该 peer，避免死盯「虚高宣称」节点。
	pullFailStreakThreshold = 4
	pullDistrustDuration    = 3 * time.Minute
)

var (
	errTimeout = errors.New("timeout awaiting block from peer")

	// ErrForkRetryOtherPeer 表示已在本地回滚分叉高度，当前 peer 不宜继续补拉，应交给外层换源。
	ErrForkRetryOtherPeer = errors.New("syncer: rolled back after fork, retry with another peer")
)

// ErrNeedFillFrom 表示需从更早高度重试补拉（共同祖先在前方），调用方用 From 重试；同一时刻只开一个 stream。
type ErrNeedFillFrom struct {
	From uint64
}

func (e *ErrNeedFillFrom) Error() string {
	return fmt.Sprintf("fill gap should start from block %d", e.From)
}

// XXX: Don't use this syncer for the consensus that may cause fork.
// This syncer doesn't assume forks
type syncer struct {
	logger          hclog.Logger
	blockchain      Blockchain
	syncProgression Progression

	peerMap         *PeerMap
	syncPeerService SyncPeerService
	syncPeerClient  SyncPeerClient

	// Timeout for syncing a block
	blockTimeout time.Duration

	// Channel to notify Sync that a new status arrived
	newStatusCh chan struct{}

	// closeCh is closed when syncer shuts down (used to unblock Sync)
	closeCh chan struct{}
	closed  atomic.Bool

	// 新增：共识切换高度
	consensusSwitchHeight uint64

	// 交易去重机制
	processedTxs map[types.Hash]bool // 已处理的交易哈希
	txMutex      sync.RWMutex        // 保护交易哈希映射的锁

	// trustedPeers 记录“近期已验证并写入成功”的 peer 高度，用于替代不可信的 bestPeer 声称高度
	trustedPeersMu sync.RWMutex
	trustedPeers   map[peer.ID]trustedPeerStat

	// 宣称高度交付验证（共识门禁）：对 Best peer 拉取 local+1，失败则短期忽略其宣称。
	advertMu          sync.Mutex
	advertIgnoreUntil map[peer.ID]time.Time
	advertVerified    map[peer.ID]advertVerifiedEntry

	// bulk 拉取反复失败 → 短期不信任（仍保留连接，仅不参与同步源与门禁 verified-best）
	pullDistrustMu    sync.Mutex
	pullDistrustUntil map[peer.ID]time.Time
	pullFailStreak    map[peer.ID]int

	// trustedBootnodeIDs：创世 bootnodes 的 peer.ID，用于 bootnode 共识链尖。
	trustedBootnodeIDs map[peer.ID]struct{}
	// trustedBootJSONRPC：peer.ID -> HTTP JSON-RPC（boot multiaddr IP + 本节点 jsonrpc_addr 端口）。
	trustedBootJSONRPC map[peer.ID]string
	lastLoggedTrustedTip uint64
	trustedTipLogMu      sync.Mutex
	lastTrustedTipLogAt  time.Time
	lastTrustedTipLogKey string

	bestNotAheadLogMu           sync.Mutex
	lastBestNotAheadStatusLogAt time.Time
	bestNotAheadWakePending     atomic.Bool

	trustedQuorumLogMu        sync.Mutex
	trustedQuorumLogLocalHeight uint64
	trustedQuorumLogLocalSince  time.Time

	// trustedTip 高度缓存：boot eth_blockNumber
	trustedBootHeightMu       sync.Mutex
	trustedBootHeightCachedAt time.Time
	trustedBootHeightReports  []trustedBootPeerReport
	trustedBootHeightHeights  []uint64

	trustedBootRPCHeight   map[peer.ID]uint64
	trustedBootRPCHeightMu sync.RWMutex
	lastBootP2PRefreshMu   sync.Mutex
	lastBootP2PRefreshAt   time.Time
}

type trustedPeerStat struct {
	lastSuccess time.Time
	lastNumber  uint64
}

const trustedPeerWindow = 30 * time.Second

func NewSyncer(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
	blockTimeout time.Duration,
	consensusSwitchHeight uint64,
	trustedBootnodeIDs []peer.ID,
	trustedBootJSONRPC map[peer.ID]string,
) Syncer {
	bootSet := make(map[peer.ID]struct{}, len(trustedBootnodeIDs))
	for _, id := range trustedBootnodeIDs {
		bootSet[id] = struct{}{}
	}
	if trustedBootJSONRPC == nil {
		trustedBootJSONRPC = make(map[peer.ID]string)
	}
	s := &syncer{
		logger:          logger.Named(syncerName),
		blockchain:      blockchain,
		syncProgression: progress.NewProgressionWrapper(progress.ChainSyncBulk),
		syncPeerService: NewSyncPeerService(logger, network, blockchain),
		syncPeerClient:  NewSyncPeerClient(logger, network, blockchain),
		blockTimeout:    blockTimeout,
		// 缓冲 1：避免 notify 的非阻塞发送在关键时刻被丢弃，导致 Sync 长期卡在 <-newStatusCh
		newStatusCh:           make(chan struct{}, 1),
		closeCh:               make(chan struct{}),
		peerMap:               new(PeerMap),
		consensusSwitchHeight: consensusSwitchHeight,

		// 初始化交易去重机制
		processedTxs: make(map[types.Hash]bool),

		trustedPeers: make(map[peer.ID]trustedPeerStat),

		advertIgnoreUntil: make(map[peer.ID]time.Time),
		advertVerified:    make(map[peer.ID]advertVerifiedEntry),

		pullDistrustUntil: make(map[peer.ID]time.Time),
		pullFailStreak:    make(map[peer.ID]int),

		trustedBootnodeIDs: bootSet,
		trustedBootJSONRPC: trustedBootJSONRPC,
	}
	if len(bootSet) > 0 {
		rpcBoots := 0
		for id := range bootSet {
			if trustedBootJSONRPC[id] != "" {
				rpcBoots++
			}
		}
		logger.Named(syncerName).Info("syncer: trusted canonical tip = boot multiaddr IP + jsonrpc_addr port, eth_blockNumber only",
			"bootnodeCount", len(bootSet),
			"bootJsonRpcEndpoints", rpcBoots)
	}
	return s
}

// Start starts goroutine processes
func (s *syncer) Start() error {
	if err := s.syncPeerClient.Start(); err != nil {
		return err
	}

	s.syncPeerService.Start()

	s.initializePeerMap()
	// 启动补唤醒：initializePeerMap 仅 Put，不会触发 notify；这里主动唤醒一次避免 Sync 初始“等不到事件”
	s.notifyNewStatusEvent()

	go s.startPeerStatusUpdateProcess()
	go s.startPeerConnectionEventProcess()

	return nil
}

// Close terminates goroutine processes
func (s *syncer) Close() error {
	// Make Close idempotent and unblock Sync
	if s.closed.Swap(true) {
		return nil
	}
	close(s.closeCh)

	if err := s.syncPeerService.Close(); err != nil {
		return err
	}

	s.syncPeerClient.Close()

	return nil
}

// initializePeerMap fetches peer statuses and initializes map
func (s *syncer) initializePeerMap() {
	peerStatuses := s.syncPeerClient.GetConnectedPeerStatuses()
	s.logger.Debug("初始化对等节点映射", "peerCount", len(peerStatuses))

	if len(peerStatuses) == 0 {
		s.logger.Warn("⚠️ 没有找到任何对等节点，同步器将等待对等节点连接")
	} else {
		s.logger.Debug("已获取对等节点状态", "数量", len(peerStatuses))
		for i, peer := range peerStatuses {
			s.logger.Debug("对等节点信息",
				"索引", i,
				"ID", peer.ID.String()[:16],
				"区块高度", peer.Number,
				"距离", peer.Distance.String())
		}
	}

	s.peerMap.Put(peerStatuses...)
}

// startPeerStatusUpdateProcess subscribes peer status change event and updates peer map
func (s *syncer) startPeerStatusUpdateProcess() {
	processedCount := 0
	lastLogTime := time.Now()

	for peerStatus := range s.syncPeerClient.GetPeerStatusUpdateCh() {
		s.putToPeerMap(peerStatus)

		// 监控处理速度
		processedCount++
		if time.Since(lastLogTime) > 10*time.Second {
			processedCount = 0
			lastLogTime = time.Now()
		}
	}
}

// startPeerConnectionEventProcess processes peer connection change events
func (s *syncer) startPeerConnectionEventProcess() {
	for e := range s.syncPeerClient.GetPeerConnectionUpdateEventCh() {
		peerID := e.PeerID

		switch e.Type {
		case event.PeerConnected:
			go s.initNewPeerStatus(peerID)
		case event.PeerDisconnected:
			s.logger.Debug("节点断开", "peer", peerID.String())
			s.removeFromPeerMap(peerID)
		}
	}
}

// initNewPeerStatus fetches status of the peer and put to peer map
func (s *syncer) initNewPeerStatus(peerID peer.ID) {
	status, err := s.syncPeerClient.GetPeerStatus(peerID)
	if err != nil {
		if isRPCDeadlineExceeded(err) {
			s.logger.Debug("failed to get peer status, skip", "id", peerID, "err", err)
		} else {
			s.logger.Warn("failed to get peer status, skip", "id", peerID, "err", err)
		}
		return
	}

	s.putToPeerMap(status)
}

// putToPeerMap puts given status to peer map
func (s *syncer) putToPeerMap(status *NoForkPeer) {
	// 检查是否是重复的状态更新
	if existingPeer, exists := s.peerMap.Load(status.ID.String()); exists {
		if existingPeer.(*NoForkPeer).Number == status.Number {
			// 相同区块高度，跳过通知
			s.peerMap.Put(status) // 仍然更新距离等信息
			return
		}
	}

	s.peerMap.Put(status)
	s.notifyNewStatusEvent()
}

// removeFromPeerMap removes the peer from peer map
func (s *syncer) removeFromPeerMap(peerID peer.ID) {
	s.peerMap.Remove(peerID)
}

// notifyNewStatusEvent emits signal to newStatusCh
func (s *syncer) notifyNewStatusEvent() {
	// If shutting down, don't attempt to notify
	if s.closed.Load() {
		return
	}

	// 使用 recover 捕获 panic，避免向已关闭的 channel 发送数据
	defer func() {
		if r := recover(); r != nil {
			// 如果发生 panic（通常是向已关闭的 channel 发送数据），忽略
			s.logger.Debug("notifyNewStatusEvent recovered from panic", "error", r)
		}
	}()

	// 使用非阻塞发送，避免频繁触发
	select {
	case s.newStatusCh <- struct{}{}:
		// 成功发送
	default:
		// channel 已满或已关闭，忽略
	}
}

// GetSyncProgression returns progression
func (s *syncer) GetSyncProgression() *progress.Progression {
	return s.syncProgression.GetProgression()
}

// getPeerMapSize returns the current size of peerMap
func (s *syncer) getPeerMapSize() int {
	count := 0
	s.peerMap.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// EnablePublishingPeerStatus enables publishing own status via gossip
func (s *syncer) EnablePublishingPeerStatus() {
	if s.syncPeerClient != nil {
		s.syncPeerClient.EnablePublishingPeerStatus()
		s.logger.Debug("启用状态广播")
	} else {
		s.logger.Warn("⚠️ syncPeerClient为空，无法启用状态广播")
	}
}

// DisablePublishingPeerStatus disables publishing own status via gossip
func (s *syncer) DisablePublishingPeerStatus() {
	if s.syncPeerClient != nil {
		s.syncPeerClient.DisablePublishingPeerStatus()
		s.logger.Debug("禁用状态广播")
	} else {
		s.logger.Warn("⚠️ syncPeerClient为空，无法禁用状态广播")
	}
}

// HasSyncPeer returns whether syncer has the peer to syncs blocks
// return false if syncer has no peer whose latest block height doesn't exceed local height
func (s *syncer) HasSyncPeer() bool {
	bestPeer := s.peerMap.BestPeer(nil)
	header := s.blockchain.Header()

	return bestPeer != nil && bestPeer.Number > header.Number
}

// GetBestPeerNumber returns the effective peer waterline: max(P2P gossip, boot 共识链尖, 近期验证写入高度)。
// 不含 GetVerifiedBestPeerNumber（避免与 getNetworkLatestBlockNumber 重复探测）。
func (s *syncer) GetBestPeerNumber() uint64 {
	var max uint64
	if bestPeer := s.peerMap.BestPeer(nil); bestPeer != nil {
		max = bestPeer.Number
	}
	if tip := s.GetTrustedCanonicalTip(); tip > max {
		max = tip
	}
	if trusted := s.GetTrustedPeerNumber(); trusted > max {
		max = trusted
	}
	return max
}

// GetTrustedPeerNumber returns the highest block number among peers that successfully
// delivered blocks which were verified and written recently. Returns 0 if none.
func (s *syncer) GetTrustedPeerNumber() uint64 {
	cutoff := time.Now().Add(-trustedPeerWindow)

	s.trustedPeersMu.RLock()
	defer s.trustedPeersMu.RUnlock()

	var max uint64
	for _, st := range s.trustedPeers {
		if st.lastSuccess.Before(cutoff) {
			continue
		}
		if st.lastNumber > max {
			max = st.lastNumber
		}
	}
	return max
}

func (s *syncer) recordTrustedPeerSuccess(peerID peer.ID, blockNumber uint64) {
	now := time.Now()
	s.trustedPeersMu.Lock()
	s.trustedPeers[peerID] = trustedPeerStat{
		lastSuccess: now,
		lastNumber:  blockNumber,
	}
	s.trustedPeersMu.Unlock()
}

func (s *syncer) logBestPeerNotAheadStatusThrottled(fn func()) {
	s.bestNotAheadLogMu.Lock()
	defer s.bestNotAheadLogMu.Unlock()
	if time.Since(s.lastBestNotAheadStatusLogAt) < bestPeerNotAheadLogInterval {
		return
	}
	s.lastBestNotAheadStatusLogAt = time.Now()
	fn()
}

// scheduleBestNotAheadWake 仅保留一个在途的异步 wake，避免 notify 风暴下每秒 spawn 大量 goroutine/日志。
func (s *syncer) scheduleBestNotAheadWake(backoff time.Duration, args ...interface{}) {
	if s.closed.Load() {
		return
	}
	if !s.bestNotAheadWakePending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.bestNotAheadWakePending.Store(false)
		time.Sleep(backoff)
		if s.closed.Load() {
			return
		}
		selfWakeArgs := append([]interface{}{"reason", "best_peer_not_ahead", "backoff", backoff.String()}, args...)
		s.logBestPeerNotAheadStatusThrottled(func() {
			s.logger.Info("syncer self-wake", selfWakeArgs...)
		})
		s.notifyNewStatusEvent()
	}()
}

func (s *syncer) wakeSyncAfter(backoff time.Duration, reason string, args ...interface{}) {
	if s.closed.Load() {
		return
	}
	if reason == "best_peer_not_ahead" {
		s.scheduleBestNotAheadWake(backoff, args...)
		return
	}
	fire := func() {
		if s.closed.Load() {
			return
		}
		selfWakeArgs := append([]interface{}{"reason", reason, "backoff", backoff.String()}, args...)
		switch reason {
		case "trusted_ahead_no_serving_peer":
			s.logger.Debug("syncer self-wake", selfWakeArgs...)
		case "local_ahead_trusted_tip":
			s.logger.Info("syncer self-wake", selfWakeArgs...)
		default:
			s.logger.Debug("syncer self-wake", selfWakeArgs...)
		}
		s.notifyNewStatusEvent()
	}
	// 长退避必须异步：若在 Sync 循环内 Sleep，会挡住 newStatusCh，peer 高度更新也要等睡完。
	if backoff >= time.Second {
		go func() {
			time.Sleep(backoff)
			fire()
		}()
		return
	}
	if backoff > 0 {
		time.Sleep(backoff)
	}
	fire()
}

// KickSync 在落后于网络且拉块链路疑似僵死时由上层调用：不退出进程，通过关流 / 换人 / 唤醒打破卡死。
func (s *syncer) KickSync(reason string) {
	if s.closed.Load() {
		return
	}

	local := uint64(0)
	if h := s.blockchain.Header(); h != nil {
		local = h.Number
	}

	s.logger.Warn("syncer KickSync: soft-recovery reset",
		"reason", reason,
		"localLatest", local,
		"peerMapSizeBefore", s.getPeerMapSize())

	for _, st := range s.syncPeerClient.GetConnectedPeerStatuses() {
		if st != nil {
			s.putToPeerMap(st)
		}
	}

	streamClosed := 0
	s.peerMap.Range(func(_ interface{}, value interface{}) bool {
		p, ok := value.(*NoForkPeer)
		if !ok || p == nil {
			return true
		}
		if err := s.syncPeerClient.CloseStream(p.ID); err != nil {
			s.logger.Debug("KickSync CloseStream", "peer", p.ID.String(), "err", err)
		} else {
			streamClosed++
		}
		return true
	})

	if bp := s.peerMap.BestPeer(nil); streamClosed == 0 && bp != nil && bp.Number > local {
		s.syncPeerClient.DisconnectPeer(bp.ID)
		s.logger.Warn("KickSync: disconnected tallest peer to force reconnect",
			"peer", bp.ID.String(), "theirNumber", bp.Number,
			"localLatest", local)
	}

	// 避免与仍在收尾的 GetBlocks goroutine 竞态：notify 太快会立刻再次 GetBlocks，仍报 inflight。
	time.Sleep(kickSyncPostCloseGraceWait)

	s.notifyNewStatusEvent()
	time.AfterFunc(300*time.Millisecond, func() {
		if s.closed.Load() {
			return
		}
		s.notifyNewStatusEvent()
	})
}

// pullDistrustSkipMap 返回当前仍在「不信任冷却」内的 peer（用于 Sync 选 Best）。
func (s *syncer) pullDistrustSkipMap() map[peer.ID]bool {
	s.pullDistrustMu.Lock()
	defer s.pullDistrustMu.Unlock()
	now := time.Now()
	m := make(map[peer.ID]bool)
	for id, until := range s.pullDistrustUntil {
		if until.After(now) {
			m[id] = true
		} else {
			delete(s.pullDistrustUntil, id)
			delete(s.pullFailStreak, id)
			s.logger.Info("syncer: pull-distrust cooldown ended; peer eligible again for BestPeer/bulk selection",
				"peer", id.String())
		}
	}
	return m
}

// appendPullDistrustSkips 将不信任 peer 并入已有 skip 集合（与 advertSkipMap 共用逻辑，需在 advertMu 外单独持 pullDistrustMu）。
func (s *syncer) appendPullDistrustSkips(m map[peer.ID]bool, now time.Time) {
	s.pullDistrustMu.Lock()
	defer s.pullDistrustMu.Unlock()
	for id, until := range s.pullDistrustUntil {
		if until.After(now) {
			m[id] = true
		} else {
			delete(s.pullDistrustUntil, id)
			delete(s.pullFailStreak, id)
			s.logger.Info("syncer: pull-distrust cooldown ended; peer eligible again for BestPeer/bulk selection",
				"peer", id.String())
		}
	}
}

func (s *syncer) resetPullFailureStreak(id peer.ID) {
	s.pullDistrustMu.Lock()
	defer s.pullDistrustMu.Unlock()
	prev, had := s.pullFailStreak[id]
	delete(s.pullFailStreak, id)
	if had && prev > 0 {
		s.logger.Info("syncer: bulk sync advanced local tip; cleared pull failure streak for peer",
			"peer", id.String(),
			"previousStreak", prev)
	}
}

func (s *syncer) mergeSkipsForBestPeer(manual map[peer.ID]bool) map[peer.ID]bool {
	out := make(map[peer.ID]bool)
	for id, v := range manual {
		if v {
			out[id] = true
		}
	}
	for id := range s.pullDistrustSkipMap() {
		out[id] = true
	}
	return out
}

func (s *syncer) recordBulkPullFailure(peerID peer.ID) {
	s.pullDistrustMu.Lock()
	defer s.pullDistrustMu.Unlock()
	s.pullFailStreak[peerID]++
	n := s.pullFailStreak[peerID]
	s.logger.Info("syncer: bulk pull did not advance local tip; incrementing peer failure streak (next BestPeer selection ignores this peer after threshold)",
		"peer", peerID.String(),
		"streak", n,
		"threshold", pullFailStreakThreshold)
	if n >= pullFailStreakThreshold {
		until := time.Now().Add(pullDistrustDuration)
		s.pullDistrustUntil[peerID] = until
		s.pullFailStreak[peerID] = 0
		s.logger.Info("syncer: peer enters pull-distrust cooldown — will not be chosen as BestPeer until expiry",
			"peer", peerID.String(),
			"cooldown", pullDistrustDuration.String(),
			"until", until.Format(time.RFC3339Nano),
			"failuresThreshold", pullFailStreakThreshold)
		go func() {
			if s.closed.Load() {
				return
			}
			s.notifyNewStatusEvent()
		}()
	}
}

// Sync syncs block with the best peer until callback returns true
func (s *syncer) Sync(callback func(*types.FullBlock) bool) error {
	localLatest := s.blockchain.Header().Number
	skipList := make(map[peer.ID]bool)
	// 失败/未完成时的退避（避免 tight loop，同时避免“等不到高度变化就永远不醒”）
	retryBackoff := 500 * time.Millisecond

	for {
		// Wait for a new event to arrive (or exit on shutdown)
		select {
		case <-s.newStatusCh:
		case <-s.closeCh:
			return nil
		}

		// fetch local latest block
		if header := s.blockchain.Header(); header != nil {
			localLatest = header.Number
		}

		headBeforeBulk := localLatest

		trustedMeta := s.computeTrustedBootnodeTip(localLatest)
		trustedTip := trustedMeta.Tip
		syncTarget := trustedTip
		if trustedMeta.MaxBootHeight > syncTarget {
			syncTarget = trustedMeta.MaxBootHeight
		}

		if trustedTip > 0 && localLatest > trustedTip+localAheadForceKickBlocks {
			s.logger.Info("syncer: local chain ahead of trusted bootnode tip; KickSync to avoid stall",
				"localLatest", localLatest,
				"trustedTip", trustedTip,
				"aheadBlocks", localLatest-trustedTip)
			s.KickSync("local ahead of trusted bootnode canonical tip")
			s.wakeSyncAfter(retryBackoff, "local_ahead_trusted_tip",
				"localLatest", localLatest,
				"trustedTip", trustedTip)
			continue
		}
		if trustedTip > 0 && localLatest > trustedTip+localAheadWarnBlocks {
			s.logger.Info("syncer: local chain ahead of trusted bootnode tip (warn)",
				"localLatest", localLatest,
				"trustedTip", trustedTip,
				"aheadBlocks", localLatest-trustedTip,
				"trustedTipBranch", trustedMeta.Branch,
				"heightsUsedInQuorum", trustedMeta.Heights,
				"maxBootHeight", trustedMeta.MaxBootHeight,
				"configuredBootnodes", len(s.trustedBootnodeIDs),
				"connectedBootsInPeerMap", trustedMeta.ConnectedBoots,
				"reportingBootsUsedInQuorum", trustedMeta.ReportingBoots,
				"maxClusterNearMax", trustedMeta.MaxClusterNearMax,
				"maxClusterFloor", trustedMeta.MaxClusterFloor)
		}

		forceBulk := syncTarget > localLatest
		if forceBulk {
			s.logSyncInfoOnLocalStall(localLatest, func() {
				s.logger.Info("syncer: behind trusted bootnode tip, will force bulk sync",
					"localLatest", localLatest,
					"trustedTip", trustedTip,
					"syncTarget", syncTarget,
					"maxBootHeight", trustedMeta.MaxBootHeight,
					"quorum", trustedMeta.Quorum,
					"connectedBootnodes", trustedMeta.ConnectedBoots,
					"reportingBootnodes", trustedMeta.ReportingBoots)
			})
		}

		// trusted 已知超前：lag<2 时 burst catch-up；lag>=2 走 bulk（不饿死 bulkSyncWithPeer）。
		if trustedAheadOfLocal(trustedMeta, localLatest) {
			if trustedCatchUpLag(trustedMeta, localLatest) < catchUpBulkMinLag {
				if s.tryCatchUpBurstFromBoot(localLatest, trustedMeta, callback) {
					continue
				}
			}
		}

		// pick one best peer（fork/未完成 的 skip 与「拉取反复失败」的不信任 合并，但不信任不触发「无 best 时全体 Disconnect」）
		pullDistrustActive := len(s.pullDistrustSkipMap())
		if forceBulk && trustedTip > localLatest {
			s.refreshTrustedBootP2PStatus(localLatest, false)
		}
		bestPeer := s.pickSyncPeerForTarget(localLatest, syncTarget, skipList, forceBulk)
		if bestPeer == nil && forceBulk && trustedTip > localLatest {
			if s.refreshTrustedBootP2PStatus(localLatest, true) > 0 {
				bestPeer = s.pickSyncPeerForTarget(localLatest, syncTarget, skipList, forceBulk)
			}
		}
		if bestPeer == nil {
			if trustedAheadOfLocal(trustedMeta, localLatest) {
				s.logSyncInfoOnLocalStall(localLatest, func() {
					s.logger.Info("syncer: trusted ahead but direct pull local+1 failed, will retry",
						"localLatest", localLatest,
						"trustedTip", trustedTip,
						"syncTarget", syncTarget,
						"nextHeight", localLatest+1)
				})
				s.wakeSyncAfter(s.catchUpRetryBackoff(localLatest, trustedMeta), "trusted_ahead_no_serving_peer",
					"localLatest", localLatest,
					"trustedTip", trustedTip)
				continue
			}
			// 所有候选 peer 均被跳过（开流失败等）：主动断开这些 peer 促其重连，退避后清空 skipList 再试
			if len(skipList) > 0 {
				for pid := range skipList {
					s.syncPeerClient.DisconnectPeer(pid)
					s.logger.Info("无可用 peer，主动断开促重连", "peer", pid.String())
				}
				time.Sleep(15 * time.Second)
			}
			skipList = make(map[peer.ID]bool)
			// 治本点：这里继续等待 newStatusCh 可能永远等不到（peer 高度不变时不会 notify）
			s.logger.Warn("syncer: no best peer, will self-wake and retry",
				"localLatest", localLatest,
				"skipListSize", len(skipList),
				"backoff", retryBackoff.String())
			s.wakeSyncAfter(retryBackoff, "no_best_peer", "localLatest", localLatest)
			continue
		}

		if bestPeer.Number <= localLatest {
			if trustedAheadOfLocal(trustedMeta, localLatest) {
				s.wakeSyncAfter(s.catchUpRetryBackoff(localLatest, trustedMeta), "trusted_ahead_no_serving_peer",
					"localLatest", localLatest,
					"trustedTip", trustedTip)
				continue
			}
			if s.tryAdvancePeerViewForNextBlock(localLatest) {
				continue
			}
			s.logSyncInfoOnLocalStall(localLatest, func() {
				s.logger.Info("syncer: best peer not ahead of local, self-wake to avoid stall on unchanged peer heights",
					"peer", bestPeer.ID.String(),
					"peerNumber", bestPeer.Number,
					"localLatest", localLatest,
					"trustedTip", trustedTip,
					"syncTarget", syncTarget)
			})
			s.scheduleBestNotAheadWake(s.catchUpRetryBackoff(localLatest, trustedMeta),
				"peer", bestPeer.ID.String(),
				"peerNumber", bestPeer.Number,
				"localLatest", localLatest)
			continue
		}

		// 只拉到 peer 实际宣称的高度；勿将 bulkTarget 抬到 syncTarget 若 peerNumber 更低（避免对 15911800 空拉 15911801）。
		peerTarget := bestPeer.Number
		if forceBulk && syncTarget < peerTarget {
			peerTarget = syncTarget
		}

		s.logSyncInfoOnLocalStall(localLatest, func() {
			s.logger.Info("syncer: selected peer for bulk sync (bootnode preferred when behind trusted tip)",
				"peer", bestPeer.ID.String(),
				"peerAdvertisedLatest", bestPeer.Number,
				"bulkTargetHeight", peerTarget,
				"localLatest", localLatest,
				"trustedTip", trustedTip,
				"syncTarget", syncTarget,
				"forceBulk", forceBulk,
				"pullDistrustCooldownPeerCount", pullDistrustActive,
				"forkOrIncompleteSkipCount", len(skipList))
		})

		// 添加真正开始同步的详细日志
		s.logger.Debug("🚀 开始同步区块",
			"peer", bestPeer.ID.String(),
			"peerNumber", bestPeer.Number,
			"localLatest", localLatest,
			"syncReason", "bestPeer.Number > localLatest",
			"timestamp", time.Now().Format("15:04:05.000"))

		// 检查是否在DPoS切换高度，如果是则跳过同步
		if s.isDPoSTransitionHeight(bestPeer.Number) {
			s.logger.Info("跳过DPoS切换高度区块同步", "peer", bestPeer.ID.String(), "目标高度", bestPeer.Number)
			continue
		}

		// fetch block from the peer
		lastNumber, shouldTerminate, err := s.bulkSyncWithPeer(bestPeer.ID, peerTarget, callback)

		if lastNumber > headBeforeBulk {
			s.resetPullFailureStreak(bestPeer.ID)
		} else if err != nil || lastNumber < peerTarget {
			s.recordBulkPullFailure(bestPeer.ID)
		}

		if err != nil {
			s.logger.Warn("failed to complete bulk sync with peer, try to next one", "peer", bestPeer.ID.String(), "error", err)
			if errors.Is(err, ErrForkRetryOtherPeer) {
				skipList[bestPeer.ID] = true
				// 需要立刻换 peer 重试，否则可能卡在 <-newStatusCh（而 peer 高度不变不再 notify）
				s.logger.Warn("syncer: fork retry requested, will self-wake to pick next peer",
					"peer", bestPeer.ID.String(),
					"localLatest", localLatest,
					"peerNumber", bestPeer.Number,
					"backoff", retryBackoff.String())
				s.wakeSyncAfter(retryBackoff, "fork_retry_other_peer",
					"peer", bestPeer.ID.String(),
					"localLatest", localLatest,
					"peerNumber", bestPeer.Number)
				continue
			}
		}

		if lastNumber < peerTarget {
			skipList[bestPeer.ID] = true

			// continue to next peer
			// 治本点：同步未完成（或中途失败）时，若 peer 高度不再变化，等待 newStatusCh 可能永久睡死
			s.logger.Warn("syncer: bulk sync incomplete, will self-wake and retry with another peer",
				"peer", bestPeer.ID.String(),
				"peerNumber", bestPeer.Number,
				"bulkTargetHeight", peerTarget,
				"localLatest", localLatest,
				"trustedTip", trustedTip,
				"lastReceivedNumber", lastNumber,
				"error", err,
				"backoff", retryBackoff.String())
			s.wakeSyncAfter(retryBackoff, "bulk_sync_incomplete",
				"peer", bestPeer.ID.String(),
				"peerNumber", bestPeer.Number,
				"localLatest", localLatest,
				"lastReceivedNumber", lastNumber)
			continue
		}

		if shouldTerminate {
			break
		}
	}

	return nil
}

// bulkSyncWithPeer syncs block with a given peer
func (s *syncer) bulkSyncWithPeer(peerID peer.ID, peerLatestBlock uint64,
	newBlockCallback func(*types.FullBlock) bool) (uint64, bool, error) {
	localLatest := s.blockchain.Header().Number
	shouldTerminate := false

	s.logger.Debug("🔍 准备获取区块流", "peer", peerID.String(), "从高度", localLatest+1, "到高度", peerLatestBlock)

	blockCh, cancelGetBlocks, err := s.syncPeerClient.GetBlocks(peerID, localLatest+1, s.blockTimeout)
	if err != nil {
		s.logger.Error("获取区块流失败", "peer", peerID.String(), "error", err)
		return 0, false, err
	}
	defer cancelGetBlocks()

	// Create a blockchain subscription for the sync progression and start tracking
	subscription := s.blockchain.SubscribeEvents()
	s.syncProgression.StartProgression(localLatest+1, subscription)
	s.syncProgression.UpdateHighestProgression(peerLatestBlock)

	defer func() {
		err := s.syncPeerClient.CloseStream(peerID)
		if err != nil {
			s.logger.Error("Failed to close stream: ", err)
		}

		// Stop monitoring the sync progression upon exit
		s.syncProgression.StopProgression()
		s.blockchain.UnsubscribeEvents(subscription)
	}()

	var lastReceivedNumber uint64
	var blockCount int

	for {
		select {
		case block, ok := <-blockCh:
			if !ok {
				s.logger.Debug("区块同步完成",
					"peer", peerID.String(),
					"同步区块数", blockCount,
					"lastReceivedNumber", lastReceivedNumber,
					"timestamp", time.Now().Format("15:04:05.000"))
				return lastReceivedNumber, shouldTerminate, nil
			}

			// safe check
			if block.Number() == 0 {
				continue
			}

			blockCount++

			// 检查是否是共识切换高度，如果是则使用WriteBlockWithoutConsensus
			if s.isConsensusSwitchHeight(block) {
				s.logger.Info("🚀 共识切换高度区块，使用WriteBlockWithoutConsensus", "peer", peerID.String(), "区块号", block.Number(), "难度", block.Header.Difficulty)

				// 对于共识切换高度区块，使用WriteBlockWithoutConsensus完全绕过共识验证
				// 这样可以避免所有IBFT相关的验证和交易执行
				s.logger.Info("🔒 同步器调用WriteBlockWithoutConsensus", "blockNumber", block.Number(), "peer", peerID.String())
				if err := s.blockchain.WriteBlockWithoutConsensus(block, syncerName); err != nil {
					metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
					s.logger.Error("共识切换高度区块写入失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
					return lastReceivedNumber, false, fmt.Errorf("failed to write consensus switch height block: %w", err)
				}

				// 创建一个简化的FullBlock用于回调
				fullBlock := &types.FullBlock{
					Block:    block,
					Receipts: []*types.Receipt{}, // 空的receipts
				}

				updateMetrics(fullBlock)
				s.logger.Debug("✅ DPoS区块同步成功",
					"peer", peerID.String(),
					"区块号", block.Number(),
					"哈希", block.Hash().String()[:16],
					"timestamp", time.Now().Format("15:04:05.000"))
				shouldTerminate = newBlockCallback(fullBlock)
				lastReceivedNumber = block.Number()
				continue
			}

			// 对区块中的交易做去重记录（仅用于日志与已处理标记）；验证与写入必须使用原始 block，否则 TxRoot 校验会失败
			if len(block.Transactions) > 0 {
				filteredTransactions := s.filterProcessedTransactions(block.Transactions)
				if len(filteredTransactions) < len(block.Transactions) {
					s.logger.Info("🔍 过滤重复交易（仅记录，不修改区块）",
						"peer", peerID.String()[:8],
						"blockNumber", block.Number(),
						"originalCount", len(block.Transactions),
						"filteredCount", len(filteredTransactions))
				}
			}

			s.logger.Debug("🔍 开始验证区块", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

			fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
			if err != nil {
				var missingParent *blockchain.MissingParentError
				if errors.As(err, &missingParent) && missingParent.ParentNumber < block.Number() {
					s.logger.Warn("⚠️ 缺父块（可能分叉），尝试从 peer 补拉直到共同祖先", "peer", peerID.String()[:16], "blockNumber", block.Number(), "parentNumber", missingParent.ParentNumber, "parentHash", missingParent.ParentHash.String()[:18])
					// Stop the GetBlocks producer before opening fill-gap stream (consumer stops reading blockCh).
					cancelGetBlocks()
					_ = s.syncPeerClient.CloseStream(peerID)
					fillFrom := missingParent.ParentNumber
					var fillLast uint64
					for {
						var fillErr error
						fillLast, fillErr = s.fillGapFromPeer(peerID, fillFrom, peerLatestBlock, newBlockCallback)
						if fillErr == nil {
							break
						}
						var needFrom *ErrNeedFillFrom
						if errors.As(fillErr, &needFrom) && needFrom.From < fillFrom {
							time.Sleep(fillGapRetryDelay)
							fillFrom = needFrom.From
							continue
						}
						// 关键：needFrom.From == fillFrom 说明本地该高度区块已存在但 hash 不一致（分叉），必须回滚到 (fillFrom-1) 才能写入新分支
						if errors.As(fillErr, &needFrom) && needFrom.From == fillFrom && fillFrom > 0 {
							if rb, ok := s.blockchain.(interface{ RollbackToHeight(uint64) error }); ok {
								s.logger.Warn("⚠️ 检测到本地分叉：补拉起点与缺父块高度相同，回滚链头后切换到 peer 分支继续补拉",
									"peer", peerID.String()[:16],
									"forkHeight", fillFrom,
									"rollbackTo", fillFrom-1,
								)
								if err := rb.RollbackToHeight(fillFrom - 1); err == nil {
									time.Sleep(fillGapRetryDelay)
									var headN uint64
									if h := s.blockchain.Header(); h != nil {
										headN = h.Number
									}
									return headN, false, fmt.Errorf("%w: forkHeight=%d", ErrForkRetryOtherPeer, fillFrom)
								}
							}
						}
						s.logger.Warn("缺父块补拉失败，供上层换 peer 重试", "peer", peerID.String(), "parentNumber", missingParent.ParentNumber, "error", fillErr)
						return lastReceivedNumber, false, fmt.Errorf("fill gap failed (try another peer): %w", fillErr)
					}
					s.logger.Info("✅ 缺父块补拉完成（已到共同祖先并写回）", "peer", peerID.String()[:16], "fillLastNumber", fillLast)
					return fillLast, false, nil
				}
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				if healed, healErr := s.tryHealStateRootMismatch(block, err); healErr == nil {
					fullBlock = healed
					err = nil
				}
				if err != nil {
					s.logger.Error("区块验证失败，返回错误供上层重试（可换 peer 或稍后重试）", "peer", peerID.String(), "区块号", block.Number(), "error", err)
					return lastReceivedNumber, false, fmt.Errorf("block verification failed (retry or try another peer): %w", err)
				}
			}
			s.logger.Debug("✅ 区块验证完成", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

			if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块写入失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("failed to write block while bulk syncing: %w", err)
			}

			s.markBlockTransactionsProcessed(block.Transactions)

			// 标记该 peer 为近期可信：已成功验证并写入区块
			s.recordTrustedPeerSuccess(peerID, block.Number())

			updateMetrics(fullBlock)
			s.logger.Debug("✅ 区块同步成功", "peer", peerID.String(), "区块号", block.Number(), "哈希", block.Hash().String()[:16], "交易数", len(block.Transactions))
			shouldTerminate = newBlockCallback(fullBlock)

			// 关键：更新localLatest！
			lastReceivedNumber = block.Number()
			localLatest = block.Number() // 更新本地最新，确保期望值正确
		case <-time.After(s.blockTimeout):
			return lastReceivedNumber, shouldTerminate, errTimeout
		}
	}
}

// fillGapFromPeer 从指定高度 from 向 peer 拉取区块并验证写入（单次只开一个 stream）。
// 遇 MissingParent 时返回 ErrNeedFillFrom，由调用方从更早高度重试，直到从共同祖先开始顺序写回，避免递归开多流导致 stream reset 与协程暴涨。
func (s *syncer) fillGapFromPeer(peerID peer.ID, from uint64, peerLatestBlock uint64,
	newBlockCallback func(*types.FullBlock) bool) (uint64, error) {
	blockCh, cancelGetBlocks, err := s.syncPeerClient.GetBlocks(peerID, from, s.blockTimeout)
	if err != nil {
		return 0, fmt.Errorf("get blocks for fill gap: %w", err)
	}
	defer cancelGetBlocks()
	defer func() { _ = s.syncPeerClient.CloseStream(peerID) }()

	var lastReceivedNumber uint64
	for block := range blockCh {
		if block == nil || block.Number() == 0 {
			continue
		}
		if s.isConsensusSwitchHeight(block) {
			if err := s.blockchain.WriteBlockWithoutConsensus(block, syncerName); err != nil {
				return lastReceivedNumber, fmt.Errorf("write consensus switch block %d: %w", block.Number(), err)
			}
			fullBlock := &types.FullBlock{Block: block, Receipts: []*types.Receipt{}}
			updateMetrics(fullBlock)
			newBlockCallback(fullBlock)
			lastReceivedNumber = block.Number()
			continue
		}
		// 仅做去重记录与已处理标记；验证/写入必须用原始 block，否则 TxRoot 校验会失败
		if len(block.Transactions) > 0 {
			filtered := s.filterProcessedTransactions(block.Transactions)
			if len(filtered) < len(block.Transactions) {
				s.logger.Info("🔍 过滤重复交易（仅记录，不修改区块）",
					"peer", peerID.String()[:8],
					"blockNumber", block.Number(),
					"originalCount", len(block.Transactions),
					"filteredCount", len(filtered))
			}
		}
		fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
		if err != nil {
			var missingParent *blockchain.MissingParentError
			if errors.As(err, &missingParent) && missingParent.ParentNumber < block.Number() {
				return lastReceivedNumber, &ErrNeedFillFrom{From: missingParent.ParentNumber}
			}
			if healed, healErr := s.tryHealStateRootMismatch(block, err); healErr == nil {
				fullBlock = healed
				err = nil
			}
			if err != nil {
				return lastReceivedNumber, fmt.Errorf("verify block %d: %w", block.Number(), err)
			}
		}
		if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
			return lastReceivedNumber, fmt.Errorf("write block %d: %w", block.Number(), err)
		}
		s.markBlockTransactionsProcessed(block.Transactions)
		// 标记该 peer 为近期可信：已成功验证并写入区块
		s.recordTrustedPeerSuccess(peerID, block.Number())
		updateMetrics(fullBlock)
		newBlockCallback(fullBlock)
		lastReceivedNumber = block.Number()
	}
	return lastReceivedNumber, nil
}

func updateMetrics(fullBlock *types.FullBlock) {
	metrics.SetGauge([]string{syncerMetrics, "tx_num"}, float32(len(fullBlock.Block.Transactions)))
	metrics.SetGauge([]string{syncerMetrics, "receipts_num"}, float32(len(fullBlock.Receipts)))
	metrics.SetGauge([]string{syncerMetrics, "blocks_num"}, 1)
}

// isConsensusSwitchHeight 检查是否是共识切换高度
func (s *syncer) isConsensusSwitchHeight(block *types.Block) bool {
	// 只在指定的共识切换高度使用WriteBlockWithoutConsensus
	// 其他DPoS区块正常进行验证

	header := block.Header
	if header == nil {
		return false
	}

	// 检查是否是共识切换高度
	if s.consensusSwitchHeight > 0 && header.Number == s.consensusSwitchHeight {
		s.logger.Info("🔄 检测到共识切换高度，使用WriteBlockWithoutConsensus",
			"区块号", header.Number,
			"难度", header.Difficulty,
			"切换高度", s.consensusSwitchHeight)
		return true
	}

	return false
}

// isDPoSTransitionHeight 检查指定高度是否是DPoS切换高度
func (s *syncer) isDPoSTransitionHeight(blockNumber uint64) bool {
	// 使用与signer相同的逻辑，但这里我们返回false
	// 因为实际上在切换时，那些区块还没有生成，所以不应该跳过同步
	// 如果将来需要跳过同步，可以在这里添加切换高度检查
	return false
}

// 检查交易是否已处理
func (s *syncer) isTransactionProcessed(txHash types.Hash) bool {
	s.txMutex.RLock()
	defer s.txMutex.RUnlock()
	return s.processedTxs[txHash]
}

// 标记交易为已处理
func (s *syncer) markTransactionProcessed(txHash types.Hash) {
	s.txMutex.Lock()
	defer s.txMutex.Unlock()
	s.processedTxs[txHash] = true
	s.logger.Debug("🔍 标记交易为已处理", "txHash", txHash.String())
}

// 清理过期的交易哈希（防止内存泄漏）
func (s *syncer) cleanupProcessedTxs() {
	s.txMutex.Lock()
	defer s.txMutex.Unlock()

	// 清理超过1000个的交易哈希
	if len(s.processedTxs) > 1000 {
		s.processedTxs = make(map[types.Hash]bool)
		s.logger.Debug("🔍 清理交易哈希缓存")
	}
}

// 标记区块内交易为已处理（在成功写入后调用）
func (s *syncer) markBlockTransactionsProcessed(transactions []*types.Transaction) {
	for _, tx := range transactions {
		if tx != nil {
			s.markTransactionProcessed(tx.Hash)
		}
	}
}

// 过滤已处理的交易
func (s *syncer) filterProcessedTransactions(transactions []*types.Transaction) []*types.Transaction {
	var filtered []*types.Transaction
	duplicateCount := 0

	for _, tx := range transactions {
		// 检查交易是否已处理
		if s.isTransactionProcessed(tx.Hash) {
			s.logger.Debug("🔍 跳过已处理的交易",
				"txHash", tx.Hash.String(),
				"nonce", tx.Nonce)
			duplicateCount++
			continue
		}

		// 添加到过滤后的列表（写入成功后再 markTransactionProcessed）
		filtered = append(filtered, tx)
	}

	// 记录过滤结果
	if duplicateCount > 0 {
		s.logger.Info("🔍 交易去重过滤结果",
			"originalCount", len(transactions),
			"filteredCount", len(filtered),
			"duplicateCount", duplicateCount)
	}

	// 定期清理缓存
	if len(s.processedTxs) > 500 {
		s.cleanupProcessedTxs()
	}

	return filtered
}

// GetSyncPeerClient returns the sync peer client for controlling status broadcasting
func (s *syncer) GetSyncPeerClient() SyncPeerClient {
	return s.syncPeerClient
}
