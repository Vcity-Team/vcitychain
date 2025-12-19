package syncer

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network/event"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"

	"os"
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

	// 新增：共识切换高度
	consensusSwitchHeight uint64

	// 交易去重机制
	processedTxs map[types.Hash]bool // 已处理的交易哈希
	txMutex      sync.RWMutex        // 保护交易哈希映射的锁
}

func NewSyncer(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
	blockTimeout time.Duration,
	consensusSwitchHeight uint64,
) Syncer {
	return &syncer{
		logger:                logger.Named(syncerName),
		blockchain:            blockchain,
		syncProgression:       progress.NewProgressionWrapper(progress.ChainSyncBulk),
		syncPeerService:       NewSyncPeerService(logger, network, blockchain),
		syncPeerClient:        NewSyncPeerClient(logger, network, blockchain),
		blockTimeout:          blockTimeout,
		newStatusCh:           make(chan struct{}),
		peerMap:               new(PeerMap),
		consensusSwitchHeight: consensusSwitchHeight,

		// 初始化交易去重机制
		processedTxs: make(map[types.Hash]bool),
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

// Sync syncs block with the best peer until callback returns true
func (s *syncer) Sync(callback func(*types.FullBlock) bool) error {
	localLatest := s.blockchain.Header().Number
	skipList := make(map[peer.ID]bool)

	// 添加日志控制变量
	lastNoPeerLogTime := time.Time{}
	lastStatusUpdateLogTime := time.Time{}
	noPeerLogInterval := 30 * time.Second      // 30秒打印一次
	statusUpdateLogInterval := 5 * time.Second // 5秒打印一次

	for {
		// Wait for a new event to arrive
		<-s.newStatusCh

		// fetch local latest block
		if header := s.blockchain.Header(); header != nil {
			localLatest = header.Number
			// 减少"同步器状态更新"日志的打印频率
			now := time.Now()
			if now.Sub(lastStatusUpdateLogTime) > statusUpdateLogInterval {
				s.logger.Debug("同步器状态更新", "localLatest", localLatest)
				lastStatusUpdateLogTime = now
			}
		}

		// pick one best peer
		bestPeer := s.peerMap.BestPeer(skipList)
		if bestPeer == nil {
			// 控制日志频率，避免刷屏
			now := time.Now()
			if now.Sub(lastNoPeerLogTime) > noPeerLogInterval {
				// 显示 peerMap 的当前状态
				peerCount := s.getPeerMapSize()
				s.logger.Debug("没有可用的对等节点",
					"skipListSize", len(skipList),
					"peerMapSize", peerCount)
				lastNoPeerLogTime = now
			}
			// Empty skipList map if there are no best peers
			skipList = make(map[peer.ID]bool)

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
	localLatest := s.blockchain.Header().Number
	shouldTerminate := false

	s.logger.Debug("🔍 准备获取区块流", "peer", peerID.String(), "从高度", localLatest+1, "到高度", peerLatestBlock)

	blockCh, err := s.syncPeerClient.GetBlocks(peerID, localLatest+1, s.blockTimeout)
	if err != nil {
		s.logger.Error("获取区块流失败", "peer", peerID.String(), "error", err)
		return 0, false, err
	}
	s.logger.Debug("✅ 区块流获取成功", "peer", peerID.String(), "从高度", localLatest+1)

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

			s.logger.Debug("🔍 从区块流接收到区块",
				"peer", peerID.String()[:8],
				"区块号", block.Number(),
				"期望区块号", localLatest+1,
				"本地最新", localLatest,
				"blockNumber==localLatest+1", block.Number() == localLatest+1,
				"时间戳", time.Now().Format("15:04:05.000"))

			// 打印详细的区块接收日志
			s.logger.Debug("🔄 同步接收到区块",
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

			// 对区块中的交易进行去重检查
			if len(block.Transactions) > 0 {
				filteredTransactions := s.filterProcessedTransactions(block.Transactions)

				// 如果过滤后有交易，创建新的区块
				if len(filteredTransactions) < len(block.Transactions) {
					s.logger.Info("🔍 过滤重复交易",
						"peer", peerID.String()[:8],
						"blockNumber", block.Number(),
						"originalCount", len(block.Transactions),
						"filteredCount", len(filteredTransactions))

					// 创建新的区块，只包含未处理的交易
					newBlock := &types.Block{
						Header:       block.Header,
						Transactions: filteredTransactions,
					}
					block = newBlock
				}
			}

			s.logger.Debug("🔍 开始验证区块", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

			fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块验证失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)

				// 检查是否是 parent 不匹配错误（可能是分叉）
				if errors.Is(err, blockchain.ErrParentNotFound) || errors.Is(err, blockchain.ErrParentHashMismatch) {
					// parent 不匹配，触发分叉处理
					s.logger.Info("🔀 检测到分叉，开始分叉处理",
						"peer", peerID.String(),
						"blockNumber", block.Number(),
						"blockHash", block.Hash().String(),
						"error", err)

					// 尝试分叉恢复
					if forkErr := s.handleFork(peerID, block); forkErr != nil {
						s.logger.Error("分叉处理失败，断开该peer",
							"peer", peerID.String(),
							"error", forkErr)
						// 断开该 peer 连接
						if err := s.syncPeerClient.CloseStream(peerID); err != nil {
							s.logger.Debug("关闭peer流失败", "peer", peerID.String(), "error", err)
						}
						// 继续尝试其他 peer
						continue
					} else {
						// 分叉处理成功，重新验证并写入
						s.logger.Info("✅ 分叉处理成功，重新验证区块",
							"peer", peerID.String(),
							"blockNumber", block.Number())
						fullBlock, err = s.blockchain.VerifyFinalizedBlock(block)
						if err != nil {
							s.logger.Error("分叉处理后区块验证仍失败", "error", err)
							continue
						}
						// 继续写入流程
					}
				} else {
					// 其他错误才退出程序
					s.logger.Error("💀 区块验证失败（非分叉错误），程序将立即退出")
					os.Exit(1)
				}
			}
			s.logger.Debug("✅ 区块验证完成", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

			if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块写入失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("failed to write block while bulk syncing: %w", err)
			}

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

// handleFork 处理分叉：回溯找共同祖先，下载分叉链，触发 reorg
func (s *syncer) handleFork(peerID peer.ID, forkBlock *types.Block) error {
	s.logger.Info("🔀 [分叉处理] 开始处理分叉",
		"peer", peerID.String(),
		"forkBlockNumber", forkBlock.Number(),
		"forkBlockHash", forkBlock.Hash().String(),
		"forkBlockParent", forkBlock.ParentHash().String())

	// 步骤1：回溯找共同祖先
	commonAncestor, err := s.findCommonAncestor(peerID, forkBlock)
	if err != nil {
		return fmt.Errorf("failed to find common ancestor: %w", err)
	}

	s.logger.Info("🔀 [分叉处理] 找到共同祖先",
		"commonAncestorNumber", commonAncestor.Number,
		"commonAncestorHash", commonAncestor.Hash.String())

	// 步骤2：下载分叉链（从共同祖先的下一个区块到分叉区块）
	forkChain, err := s.downloadForkChain(peerID, commonAncestor.Hash, forkBlock.Number())
	if err != nil {
		return fmt.Errorf("failed to download fork chain: %w", err)
	}

	s.logger.Info("🔀 [分叉处理] 分叉链下载完成",
		"forkChainLength", len(forkChain),
		"fromBlock", commonAncestor.Number+1,
		"toBlock", forkBlock.Number())

	// 步骤3：验证并写入分叉链的所有区块
	for i, block := range forkChain {
		s.logger.Info("🔀 [分叉处理] 验证分叉链区块",
			"index", i+1,
			"total", len(forkChain),
			"blockNumber", block.Number(),
			"blockHash", block.Hash().String())

		fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
		if err != nil {
			return fmt.Errorf("failed to verify fork chain block %d: %w", block.Number(), err)
		}

		// 写入分叉链区块（这会触发 reorg）
		if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
			return fmt.Errorf("failed to write fork chain block %d: %w", block.Number(), err)
		}

		s.logger.Info("🔀 [分叉处理] 分叉链区块写入成功",
			"blockNumber", block.Number())
	}

	s.logger.Info("✅ [分叉处理] 分叉处理完成",
		"forkChainLength", len(forkChain),
		"newHeadNumber", forkBlock.Number())

	return nil
}

// findCommonAncestor 回溯找共同祖先
func (s *syncer) findCommonAncestor(peerID peer.ID, forkBlock *types.Block) (*types.Header, error) {
	s.logger.Info("🔍 [找共同祖先] 开始回溯",
		"forkBlockNumber", forkBlock.Number(),
		"forkBlockHash", forkBlock.Hash().String(),
		"forkBlockParent", forkBlock.ParentHash().String())

	localHeader := s.blockchain.Header()
	if localHeader == nil {
		return nil, fmt.Errorf("failed to get local header")
	}

	// 从分叉区块的 parent 开始，向上回溯
	currentHash := forkBlock.ParentHash()
	currentNumber := forkBlock.Number() - 1

	// 最多回溯 1000 个区块（防止无限循环）
	maxBacktrack := uint64(1000)
	backtrackCount := uint64(0)

	for backtrackCount < maxBacktrack && currentNumber > 0 {
		// 先检查本地是否有该区块
		localBlock, ok := s.blockchain.GetBlockByNumber(currentNumber, false)
		if ok {
			// 检查 hash 是否匹配
			if localBlock.Hash() == currentHash {
				// ✅ 找到共同祖先
				s.logger.Info("✅ [找共同祖先] 找到共同祖先（本地已有）",
					"blockNumber", currentNumber,
					"blockHash", currentHash.String())
				return localBlock.Header, nil
			}
			// hash 不匹配，说明是分叉点，继续向上回溯
			s.logger.Info("🔍 [找共同祖先] 本地区块hash不匹配，继续回溯",
				"blockNumber", currentNumber,
				"localHash", localBlock.Hash().String(),
				"remoteHash", currentHash.String())
		}

		// 本地没有或hash不匹配，从 peer 请求该区块
		block, err := s.requestBlockByHash(peerID, currentHash)
		if err != nil {
			// 如果请求失败，尝试通过高度请求（fallback）
			s.logger.Info("🔍 [找共同祖先] 按hash请求失败，尝试按高度请求",
				"blockNumber", currentNumber,
				"error", err)

			// 使用 GetBlocks 按高度请求（简化处理）
			blockCh, err := s.syncPeerClient.GetBlocks(peerID, currentNumber, s.blockTimeout)
			if err != nil {
				return nil, fmt.Errorf("failed to get block %d: %w", currentNumber, err)
			}

			select {
			case block, ok := <-blockCh:
				if !ok {
					return nil, fmt.Errorf("failed to receive block %d from peer", currentNumber)
				}
				if block.Number() != currentNumber {
					return nil, fmt.Errorf("received wrong block: expected %d, got %d", currentNumber, block.Number())
				}
				// 检查 hash 是否匹配
				if block.Hash() != currentHash {
					// hash 不匹配，继续向上回溯
					currentHash = block.ParentHash()
					currentNumber = block.Number() - 1
					backtrackCount++
					continue
				}
			case <-time.After(s.blockTimeout):
				return nil, fmt.Errorf("timeout waiting for block %d", currentNumber)
			}
		}

		// 检查本地是否有该区块（再次检查，因为可能已经写入）
		localBlock, ok = s.blockchain.GetBlockByNumber(currentNumber, false)
		if ok && localBlock.Hash() == currentHash {
			// ✅ 找到共同祖先
			s.logger.Info("✅ [找共同祖先] 找到共同祖先",
				"blockNumber", currentNumber,
				"blockHash", currentHash.String())
			return localBlock.Header, nil
		}

		// 继续向上回溯
		currentHash = block.ParentHash()
		currentNumber = block.Number() - 1
		backtrackCount++

		s.logger.Info("🔍 [找共同祖先] 继续回溯",
			"backtrackCount", backtrackCount,
			"currentNumber", currentNumber,
			"currentHash", currentHash.String())
	}

	return nil, fmt.Errorf("failed to find common ancestor within %d blocks", maxBacktrack)
}

// downloadForkChain 下载分叉链（从共同祖先的下一个区块到目标区块）
func (s *syncer) downloadForkChain(peerID peer.ID, commonAncestorHash types.Hash, toNumber uint64) ([]*types.Block, error) {
	s.logger.Info("📥 [下载分叉链] 开始下载",
		"commonAncestorHash", commonAncestorHash.String(),
		"toNumber", toNumber)

	// 获取共同祖先区块
	fromBlock, ok := s.blockchain.GetBlockByHash(commonAncestorHash, true)
	if !ok {
		return nil, fmt.Errorf("failed to get common ancestor block: %s", commonAncestorHash.String())
	}

	fromNumber := fromBlock.Number()
	forkChain := make([]*types.Block, 0)

	// 从共同祖先的下一个区块开始下载
	for blockNum := fromNumber + 1; blockNum <= toNumber; blockNum++ {
		// 先检查本地是否已有（可能已经下载过）
		_, ok := s.blockchain.GetBlockByNumber(blockNum, true)
		if ok {
			// 本地已有，检查是否是分叉链的区块
			// 这里简化：假设需要从 peer 重新获取（因为可能是不同分叉的区块）
			s.logger.Info("📥 [下载分叉链] 本地已有区块，但需要确认是否是分叉链区块",
				"blockNumber", blockNum)
		}

		// 从 peer 请求该区块（使用 GetBlocks 按高度请求，更简单）
		blockCh, err := s.syncPeerClient.GetBlocks(peerID, blockNum, s.blockTimeout)
		if err != nil {
			return nil, fmt.Errorf("failed to request block %d: %w", blockNum, err)
		}

		// 从 channel 读取第一个区块
		select {
		case block, ok := <-blockCh:
			if !ok {
				return nil, fmt.Errorf("failed to receive block %d from peer", blockNum)
			}
			if block.Number() != blockNum {
				return nil, fmt.Errorf("received wrong block: expected %d, got %d", blockNum, block.Number())
			}
			forkChain = append(forkChain, block)
			s.logger.Info("📥 [下载分叉链] 下载区块成功",
				"blockNumber", blockNum,
				"blockHash", block.Hash().String(),
				"progress", fmt.Sprintf("%d/%d", len(forkChain), toNumber-fromNumber))
		case <-time.After(s.blockTimeout):
			return nil, fmt.Errorf("timeout waiting for block %d", blockNum)
		}
	}

	s.logger.Info("✅ [下载分叉链] 分叉链下载完成",
		"forkChainLength", len(forkChain),
		"fromBlock", fromNumber+1,
		"toBlock", toNumber)

	return forkChain, nil
}

// requestBlockByHash 按 hash 从 peer 请求单个区块
func (s *syncer) requestBlockByHash(peerID peer.ID, hash types.Hash) (*types.Block, error) {
	s.logger.Debug("🔍 [请求区块] 按hash请求区块",
		"peer", peerID.String(),
		"hash", hash.String())

	// 先检查本地是否已有
	localBlock, ok := s.blockchain.GetBlockByHash(hash, true)
	if ok {
		s.logger.Debug("🔍 [请求区块] 本地已有区块，直接返回",
			"hash", hash.String(),
			"blockNumber", localBlock.Number())
		return localBlock, nil
	}

	// 本地没有，从 peer 请求
	block, err := s.syncPeerClient.GetBlockByHash(peerID, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get block by hash from peer: %w", err)
	}

	// 验证 hash 是否匹配
	if block.Hash() != hash {
		return nil, fmt.Errorf("block hash mismatch: expected %s, got %s", hash.String(), block.Hash().String())
	}

	s.logger.Debug("✅ [请求区块] 从peer获取区块成功",
		"peer", peerID.String(),
		"hash", hash.String(),
		"blockNumber", block.Number())

	return block, nil
}
