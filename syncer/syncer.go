package syncer

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network/event"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	syncerName  = "syncer"
	syncerProto = "/syncer/0.2"

	// 同步相关常量
	maxSyncRetries      = 3
	syncRetryDelay      = 2 * time.Second
	syncRetryBackoff    = 1.5
	maxConcurrentSync   = 2
	healthCheckInterval = 30 * time.Second
)

var (
	errTimeout = errors.New("timeout awaiting block from peer")
)

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

	// 状态更新统计
	statusUpdateStats struct {
		sync.Mutex
		totalUpdates   int64
		droppedUpdates int64
		lastUpdateTime time.Time
	}

	// 同步重试统计
	syncRetryStats struct {
		sync.Mutex
		totalRetries      int64
		successfulRetries int64
		failedRetries     int64
		lastRetryTime     time.Time
	}

	// 并发同步控制
	syncSemaphore chan struct{}

	// 关闭信号
	closeCh chan struct{}
}

func NewSyncer(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
	blockTimeout time.Duration,
) Syncer {
	return &syncer{
		logger:          logger.Named(syncerName),
		blockchain:      blockchain,
		syncProgression: progress.NewProgressionWrapper(progress.ChainSyncBulk),
		syncPeerService: NewSyncPeerService(logger, network, blockchain),
		syncPeerClient:  NewSyncPeerClient(logger, network, blockchain),
		blockTimeout:    blockTimeout,
		newStatusCh:     make(chan struct{}, 100), // 缓冲channel
		peerMap:         new(PeerMap),
		syncSemaphore:   make(chan struct{}, maxConcurrentSync),
		closeCh:         make(chan struct{}),
	}
}

// Start starts goroutine processes
func (s *syncer) Start() error {
	if err := s.syncPeerClient.Start(); err != nil {
		return err
	}

	s.syncPeerService.Start()

	s.initializePeerMap()

	go s.startPeerStatusUpdateProcess()
	go s.startPeerConnectionEventProcess()
	go s.startHealthCheckProcess()

	return nil
}

// Close terminates goroutine processes
func (s *syncer) Close() error {
	close(s.closeCh)
	close(s.newStatusCh)

	if err := s.syncPeerService.Close(); err != nil {
		return err
	}

	s.syncPeerClient.Close()

	return nil
}

// initializePeerMap fetches peer statuses and initializes map
func (s *syncer) initializePeerMap() {
	peerStatuses := s.syncPeerClient.GetConnectedPeerStatuses()
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
			// 获取状态更新统计
			s.statusUpdateStats.Lock()
			totalUpdates := s.statusUpdateStats.totalUpdates
			droppedUpdates := s.statusUpdateStats.droppedUpdates
			lastUpdateTime := s.statusUpdateStats.lastUpdateTime
			s.statusUpdateStats.Unlock()

			s.logger.Info("状态更新处理统计",
				"处理数量", processedCount,
				"时间间隔", time.Since(lastLogTime),
				"处理速率", float64(processedCount)/time.Since(lastLogTime).Seconds(),
				"总状态更新", totalUpdates,
				"丢弃状态更新", droppedUpdates,
				"最后更新时间", lastUpdateTime.Format("15:04:05.000"))
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
			s.logger.Info("节点连接", "peer", peerID.String())
			go s.initNewPeerStatus(peerID)
		case event.PeerDisconnected:
			s.logger.Info("节点断开", "peer", peerID.String())
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
	s.peerMap.Put(status)
	s.notifyNewStatusEvent()
}

// removeFromPeerMap removes the peer from peer map
func (s *syncer) removeFromPeerMap(peerID peer.ID) {
	s.peerMap.Remove(peerID)
}

// notifyNewStatusEvent emits signal to newStatusCh
func (s *syncer) notifyNewStatusEvent() {
	// 更新统计信息
	s.statusUpdateStats.Lock()
	s.statusUpdateStats.totalUpdates++
	s.statusUpdateStats.lastUpdateTime = time.Now()
	s.statusUpdateStats.Unlock()

	select {
	case s.newStatusCh <- struct{}{}:
		// 发送成功
	default:
		// 缓冲区满，记录丢弃统计
		s.statusUpdateStats.Lock()
		s.statusUpdateStats.droppedUpdates++
		droppedCount := s.statusUpdateStats.droppedUpdates
		totalCount := s.statusUpdateStats.totalUpdates
		s.statusUpdateStats.Unlock()

		// 记录消息丢弃日志
		s.logger.Warn("状态更新消息被丢弃",
			"丢弃数", droppedCount,
			"总更新数", totalCount,
			"丢弃率", float64(droppedCount)/float64(totalCount),
			"时间", time.Now().Format("15:04:05.000"))
	}
}

// startHealthCheckProcess 定期检查peer健康状态
func (s *syncer) startHealthCheckProcess() {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.performHealthCheck()
		case <-s.closeCh:
			// 优雅关闭
			return
		}
	}
}

// performHealthCheck 执行peer健康检查
func (s *syncer) performHealthCheck() {
	healthyPeers := s.peerMap.GetHealthyPeers()
	totalPeers := 0

	s.peerMap.Range(func(key, value interface{}) bool {
		totalPeers++
		return true
	})

	if len(healthyPeers) < totalPeers/2 {
		s.logger.Warn("健康peer数量不足",
			"健康peer数", len(healthyPeers),
			"总peer数", totalPeers)
	}

	s.logger.Debug("peer健康检查完成",
		"健康peer数", len(healthyPeers),
		"总peer数", totalPeers)
}

// GetSyncProgression returns progression
func (s *syncer) GetSyncProgression() *progress.Progression {
	return s.syncProgression.GetProgression()
}

// HasSyncPeer returns whether syncer has the peer to syncs blocks
// return false if syncer has no peer whose latest block height doesn't exceed local height
func (s *syncer) HasSyncPeer() bool {
	bestPeer := s.peerMap.BestPeer(nil)
	header := s.blockchain.Header()

	return bestPeer != nil && bestPeer.Number > header.Number
}

// Sync 改进的同步策略，增加智能重试和并发控制
func (s *syncer) Sync(callback func(*types.FullBlock) bool) error {
	localLatest := s.blockchain.Header().Number
	skipList := make(map[peer.ID]bool)
	retryCount := 0

	for {
		// Wait for a new event to arrive
		<-s.newStatusCh

		// fetch local latest block
		if header := s.blockchain.Header(); header != nil {
			localLatest = header.Number
		}

		// pick one best peer
		bestPeer := s.peerMap.BestPeer(skipList)
		if bestPeer == nil {
			// 如果没有健康的peer，等待一段时间后重试
			if retryCount < maxSyncRetries {
				retryCount++
				s.logger.Warn("没有可用的健康peer，等待重试",
					"重试次数", retryCount,
					"等待时间", syncRetryDelay*time.Duration(retryCount))
				time.Sleep(syncRetryDelay * time.Duration(retryCount))
				continue
			}

			// 重置重试计数和跳过列表
			retryCount = 0
			skipList = make(map[peer.ID]bool)
			s.logger.Warn("重置同步状态，重新开始")
			continue
		}

		// if the bestPeer does not have a new block continue
		if bestPeer.Number <= localLatest {
			continue
		}

		// 获取同步信号量
		select {
		case s.syncSemaphore <- struct{}{}:
			// 成功获取信号量，开始同步
		default:
			// 信号量已满，等待
			s.logger.Debug("同步并发数已达上限，等待可用槽位")
			s.syncSemaphore <- struct{}{}
		}

		// fetch block from the peer with retry
		lastNumber, shouldTerminate, err := s.syncWithRetry(bestPeer, callback)

		// 释放信号量
		<-s.syncSemaphore

		if err != nil {
			s.logger.Warn("同步失败，标记peer为不健康",
				"peer", bestPeer.ID.String()[:8],
				"error", err)

			// 更新peer健康状态
			if bestPeer.Health != nil {
				bestPeer.UpdateHealth(0, false)
			}

			// 添加到跳过列表
			skipList[bestPeer.ID] = true
			continue
		}

		if lastNumber < bestPeer.Number {
			skipList[bestPeer.ID] = true
			continue
		}

		if shouldTerminate {
			break
		}

		// 同步成功，重置重试计数
		retryCount = 0
	}

	return nil
}

// syncWithRetry 带重试的同步逻辑
func (s *syncer) syncWithRetry(peer *NoForkPeer, callback func(*types.FullBlock) bool) (uint64, bool, error) {
	var lastNumber uint64
	var shouldTerminate bool
	var err error

	startTime := time.Now()

	for attempt := 1; attempt <= maxSyncRetries; attempt++ {
		s.logger.Debug("开始同步尝试",
			"peer", peer.ID.String()[:8],
			"尝试次数", attempt,
			"目标高度", peer.Number)

		lastNumber, shouldTerminate, err = s.bulkSyncWithPeer(peer.ID, peer.Number, callback)

		if err == nil {
			// 同步成功，更新peer健康状态
			responseTime := time.Since(startTime)
			peer.UpdateHealth(responseTime, true)

			s.logger.Debug("同步成功",
				"peer", peer.ID.String()[:8],
				"响应时间", responseTime,
				"尝试次数", attempt)

			// 更新重试统计
			s.syncRetryStats.Lock()
			s.syncRetryStats.successfulRetries++
			s.syncRetryStats.lastRetryTime = time.Now()
			s.syncRetryStats.Unlock()

			return lastNumber, shouldTerminate, nil
		}

		// 同步失败，记录重试统计
		s.syncRetryStats.Lock()
		s.syncRetryStats.totalRetries++
		s.syncRetryStats.failedRetries++
		s.syncRetryStats.lastRetryTime = time.Now()
		s.syncRetryStats.Unlock()

		s.logger.Warn("同步尝试失败",
			"peer", peer.ID.String()[:8],
			"尝试次数", attempt,
			"error", err)

		if attempt < maxSyncRetries {
			// 计算退避延迟
			delay := time.Duration(float64(syncRetryDelay) *
				pow(syncRetryBackoff, float64(attempt-1)))

			s.logger.Debug("等待重试",
				"peer", peer.ID.String()[:8],
				"延迟时间", delay)

			time.Sleep(delay)
		}
	}

	// 所有重试都失败了
	s.logger.Error("同步重试次数已达上限",
		"peer", peer.ID.String()[:8],
		"最大重试次数", maxSyncRetries)

	return lastNumber, shouldTerminate, err
}

// pow 计算x的y次方
func pow(x, y float64) float64 {
	result := 1.0
	for i := 0; i < int(y); i++ {
		result *= x
	}
	return result
}

// bulkSyncWithPeer 改进的区块同步逻辑
func (s *syncer) bulkSyncWithPeer(peerID peer.ID, peerLatestBlock uint64,
	newBlockCallback func(*types.FullBlock) bool) (uint64, bool, error) {
	s.logger.Debug("开始区块同步", "peer", peerID.String(), "目标高度", peerLatestBlock)

	localLatest := s.blockchain.Header().Number
	shouldTerminate := false

	blockCh, err := s.syncPeerClient.GetBlocks(peerID, localLatest+1, s.blockTimeout)
	if err != nil {
		s.logger.Error("获取区块流失败", "peer", peerID.String()[:8], "error", err)
		return 0, false, err
	}

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
	var lastProgressTime time.Time

	for {
		select {
		case block, ok := <-blockCh:
			if !ok {
				s.logger.Debug("区块同步完成", "peer", peerID.String(), "同步区块数", blockCount)
				return lastReceivedNumber, shouldTerminate, nil
			}

			// safe check
			if block.Number() == 0 {
				continue
			}

			blockCount++
			// 只在每10个区块或关键节点记录日志
			if blockCount%10 == 0 || block.Number()%100 == 0 {
				s.logger.Debug("区块同步进度", "peer", peerID.String(), "当前区块", block.Number(), "已同步", blockCount)
			}

			// 记录进度时间
			lastProgressTime = time.Now()

			fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块验证失败", "peer", peerID.String()[:8], "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("unable to verify block, %w", err)
			}

			if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块写入失败", "peer", peerID.String()[:8], "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("failed to write block while bulk syncing: %w", err)
			}

			updateMetrics(fullBlock)
			shouldTerminate = newBlockCallback(fullBlock)

			lastReceivedNumber = block.Number()
		case <-time.After(s.blockTimeout):
			// 检查是否有进度
			if time.Since(lastProgressTime) > s.blockTimeout/2 {
				s.logger.Warn("同步超时，但检测到进度",
					"peer", peerID.String()[:8],
					"最后进度时间", lastProgressTime,
					"超时时间", s.blockTimeout)
				// 不返回超时错误，继续等待
				continue
			}
			return lastReceivedNumber, shouldTerminate, errTimeout
		}
	}
}

func updateMetrics(fullBlock *types.FullBlock) {
	metrics.SetGauge([]string{syncerMetrics, "tx_num"}, float32(len(fullBlock.Block.Transactions)))
	metrics.SetGauge([]string{syncerMetrics, "receipts_num"}, float32(len(fullBlock.Receipts)))
	metrics.SetGauge([]string{syncerMetrics, "blocks_num"}, 1)
}
