package syncer

import (
	"errors"
	"fmt"
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
		newStatusCh:     make(chan struct{}),
		peerMap:         new(PeerMap),
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

	return nil
}

// Close terminates goroutine processes
func (s *syncer) Close() error {
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
			s.logger.Debug("状态更新处理统计",
				"处理数量", processedCount,
				"时间间隔", time.Since(lastLogTime),
				"处理速率", float64(processedCount)/time.Since(lastLogTime).Seconds())
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
	select {
	case s.newStatusCh <- struct{}{}:
	default:
	}
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

// Sync syncs block with the best peer until callback returns true
func (s *syncer) Sync(callback func(*types.FullBlock) bool) error {
	localLatest := s.blockchain.Header().Number
	skipList := make(map[peer.ID]bool)

	for {
		// Wait for a new event to arrive
		<-s.newStatusCh

		// fetch local latest block
		if header := s.blockchain.Header(); header != nil {
			localLatest = header.Number
			s.logger.Debug("同步器状态更新", "localLatest", localLatest)
		}

		// pick one best peer
		bestPeer := s.peerMap.BestPeer(skipList)
		if bestPeer == nil {
			s.logger.Debug("没有可用的对等节点", "skipListSize", len(skipList))
			// Empty skipList map if there are no best peers
			skipList = make(map[peer.ID]bool)

			continue
		}
		
		s.logger.Debug("找到最佳对等节点", 
			"peer", bestPeer.ID.String(), 
			"peerNumber", bestPeer.Number, 
			"localLatest", localLatest)

		// if the bestPeer does not have a new block continue
		if bestPeer.Number <= localLatest {
			s.logger.Debug("跳过同步：对等节点没有新区块", 
				"peer", bestPeer.ID.String(), 
				"peerNumber", bestPeer.Number, 
				"localLatest", localLatest)
			continue
		}
		
		s.logger.Debug("选择最佳对等节点进行同步", 
			"peer", bestPeer.ID.String(), 
			"peerNumber", bestPeer.Number, 
			"localLatest", localLatest)

		// 检查是否在DPoS切换高度，如果是则跳过同步
		if s.isDPoSTransitionHeight(bestPeer.Number) {
			s.logger.Info("跳过DPoS切换高度区块同步", "peer", bestPeer.ID.String(), "目标高度", bestPeer.Number)
			continue
		}

		// fetch block from the peer
		lastNumber, shouldTerminate, err := s.bulkSyncWithPeer(bestPeer.ID, bestPeer.Number, callback)
		if err != nil {
			s.logger.Warn("failed to complete bulk sync with peer, try to next one", "peer ID", "error", bestPeer.ID, err)
		}

		if lastNumber < bestPeer.Number {
			skipList[bestPeer.ID] = true

			// continue to next peer
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
	s.logger.Debug("开始区块同步", "peer", peerID.String(), "目标高度", peerLatestBlock)

	localLatest := s.blockchain.Header().Number
	shouldTerminate := false
	
	s.logger.Debug("同步参数", 
		"peer", peerID.String(), 
		"peerLatestBlock", peerLatestBlock, 
		"localLatest", localLatest, 
		"startFrom", localLatest+1)

	blockCh, err := s.syncPeerClient.GetBlocks(peerID, localLatest+1, s.blockTimeout)
	if err != nil {
		s.logger.Error("获取区块流失败", "peer", peerID.String(), "error", err)
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

	for {
		select {
		case block, ok := <-blockCh:
			if !ok {
				s.logger.Debug("区块同步完成", "peer", peerID.String(), "同步区块数", blockCount)
				return lastReceivedNumber, shouldTerminate, nil
			}

			// 打印详细的区块接收日志
			s.logger.Info("🔄 同步接收到区块", 
				"peer", peerID.String()[:8], 
				"区块号", block.Number(), 
				"难度", block.Header.Difficulty, 
				"哈希", block.Hash().String()[:16],
				"时间戳", block.Header.Timestamp,
				"交易数", len(block.Transactions),
				"Gas限制", block.Header.GasLimit,
				"Gas使用", block.Header.GasUsed)

			// safe check
			if block.Number() == 0 {
				continue
			}

			blockCount++
			// 只在每10个区块或关键节点记录日志
			if blockCount%10 == 0 || block.Number()%100 == 0 {
				s.logger.Info("区块同步进度", "peer", peerID.String(), "当前区块", block.Number(), "已同步", blockCount)
			}

			// 检查是否是DPoS区块（难度为1且区块号较高），如果是则完全跳过IBFT验证
			if s.isDPoSBlock(block) {
				s.logger.Info("🚀 DPoS区块直接写入，跳过IBFT验证", "peer", peerID.String(), "区块号", block.Number(), "难度", block.Header.Difficulty)
				
				// 对于DPoS区块，使用WriteBlockWithoutConsensus完全绕过共识验证
				// 这样可以避免所有IBFT相关的验证和交易执行
				s.logger.Debug("🔒 同步器调用WriteBlockWithoutConsensus", "blockNumber", block.Number(), "peer", peerID.String())
				if err := s.blockchain.WriteBlockWithoutConsensus(block, syncerName); err != nil {
					metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
					s.logger.Error("DPoS区块写入失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
					return lastReceivedNumber, false, fmt.Errorf("failed to write DPoS block: %w", err)
				}
				
				// 创建一个简化的FullBlock用于回调
				fullBlock := &types.FullBlock{
					Block:    block,
					Receipts: []*types.Receipt{}, // 空的receipts
				}
				
				updateMetrics(fullBlock)
				s.logger.Info("✅ DPoS区块同步成功", "peer", peerID.String(), "区块号", block.Number(), "哈希", block.Hash().String()[:16])
				shouldTerminate = newBlockCallback(fullBlock)
				lastReceivedNumber = block.Number()
				continue
			}

			fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块验证失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("unable to verify block, %w", err)
			}

			s.logger.Debug("🔒 同步器调用WriteFullBlock", "blockNumber", block.Number(), "peer", peerID.String())
			if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块写入失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("failed to write block while bulk syncing: %w", err)
			}

			updateMetrics(fullBlock)
			s.logger.Info("✅ 区块同步成功", "peer", peerID.String(), "区块号", block.Number(), "哈希", block.Hash().String()[:16], "交易数", len(block.Transactions))
			shouldTerminate = newBlockCallback(fullBlock)

			lastReceivedNumber = block.Number()
		case <-time.After(s.blockTimeout):
			return lastReceivedNumber, shouldTerminate, errTimeout
		}
	}
}

func updateMetrics(fullBlock *types.FullBlock) {
	metrics.SetGauge([]string{syncerMetrics, "tx_num"}, float32(len(fullBlock.Block.Transactions)))
	metrics.SetGauge([]string{syncerMetrics, "receipts_num"}, float32(len(fullBlock.Receipts)))
	metrics.SetGauge([]string{syncerMetrics, "blocks_num"}, 1)
}

// isDPoSBlock 检查是否是DPoS区块
func (s *syncer) isDPoSBlock(block *types.Block) bool {
	// DPoS区块的特征：
	// 1. 难度为1（IBFT区块的难度等于区块号）
	// 2. 区块号在切换高度之后
	// 3. MixHash可能不同
	
	header := block.Header
	if header == nil {
		return false
	}
	
	// 检查难度：DPoS区块的难度为1，IBFT区块的难度等于区块号
	// 同时检查区块号是否在切换高度之后
	if header.Difficulty == 1 && header.Number >= 7390 {
		s.logger.Debug("检测到DPoS区块", 
			"区块号", header.Number, 
			"难度", header.Difficulty,
			"切换高度", 7390)
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

// GetSyncPeerClient returns the sync peer client for controlling status broadcasting
func (s *syncer) GetSyncPeerClient() SyncPeerClient {
	return s.syncPeerClient
}