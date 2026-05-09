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
) Syncer {
	return &syncer{
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
	}
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
	s.logger.Info("🔍 初始化对等节点映射", "获取到的对等节点数量", len(peerStatuses))

	if len(peerStatuses) == 0 {
		s.logger.Warn("⚠️ 没有找到任何对等节点，同步器将等待对等节点连接")
	} else {
		s.logger.Info("✅ 成功获取对等节点状态", "数量", len(peerStatuses))
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
		s.logger.Warn("failed to get peer status, skip", "id", peerID, "err", err)

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
		s.logger.Info("✅ 启用状态广播")
	} else {
		s.logger.Warn("⚠️ syncPeerClient为空，无法启用状态广播")
	}
}

// DisablePublishingPeerStatus disables publishing own status via gossip
func (s *syncer) DisablePublishingPeerStatus() {
	if s.syncPeerClient != nil {
		s.syncPeerClient.DisablePublishingPeerStatus()
		s.logger.Info("✅ 禁用状态广播")
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

// GetBestPeerNumber returns the latest block number from the best peer
func (s *syncer) GetBestPeerNumber() uint64 {
	bestPeer := s.peerMap.BestPeer(nil)
	if bestPeer != nil {
		return bestPeer.Number
	}
	return 0
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

func (s *syncer) wakeSyncAfter(backoff time.Duration, reason string, args ...interface{}) {
	// 仅用于“仍落后/刚失败/无可用 peer”等需要尽快重试的路径
	if backoff > 0 {
		time.Sleep(backoff)
	}
	if s.closed.Load() {
		return
	}
	// 这里用 Debug，避免正常波动时刷屏；关键路径另有 Warn 说明
	s.logger.Debug("syncer self-wake", append([]interface{}{"reason", reason, "backoff", backoff.String()}, args...)...)
	s.notifyNewStatusEvent()
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

	s.notifyNewStatusEvent()
	time.AfterFunc(300*time.Millisecond, func() {
		if s.closed.Load() {
			return
		}
		s.notifyNewStatusEvent()
	})
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

		// pick one best peer
		bestPeer := s.peerMap.BestPeer(skipList)
		if bestPeer == nil {
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

		// if the bestPeer does not have a new block continue
		if bestPeer.Number <= localLatest {
			continue
		}

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
		lastNumber, shouldTerminate, err := s.bulkSyncWithPeer(bestPeer.ID, bestPeer.Number, callback)
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

		if lastNumber < bestPeer.Number {
			skipList[bestPeer.ID] = true

			// continue to next peer
			// 治本点：同步未完成（或中途失败）时，若 peer 高度不再变化，等待 newStatusCh 可能永久睡死
			s.logger.Warn("syncer: bulk sync incomplete, will self-wake and retry with another peer",
				"peer", bestPeer.ID.String(),
				"peerNumber", bestPeer.Number,
				"localLatest", localLatest,
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
			// 只在每10个区块或关键节点记录日志
			if blockCount%10 == 0 || block.Number()%100 == 0 {
				s.logger.Info("区块同步进度", "peer", peerID.String(), "当前区块", block.Number(), "已同步", blockCount)
			}

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
				s.logger.Error("区块验证失败，返回错误供上层重试（可换 peer 或稍后重试）", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("block verification failed (retry or try another peer): %w", err)
			}
			s.logger.Debug("✅ 区块验证完成", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

			if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块写入失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("failed to write block while bulk syncing: %w", err)
			}

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
			return lastReceivedNumber, fmt.Errorf("verify block %d: %w", block.Number(), err)
		}
		if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
			return lastReceivedNumber, fmt.Errorf("write block %d: %w", block.Number(), err)
		}
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

		// 标记交易为已处理
		s.markTransactionProcessed(tx.Hash)

		// 添加到过滤后的列表
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
