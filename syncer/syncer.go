package syncer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
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

	// 🆕 新增：共识切换高度
	consensusSwitchHeight uint64
	
	// 🆕 新增：BLS公钥加载状态缓存
	blsKeysLoaded bool
	blsKeysMutex  sync.RWMutex
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
	s.logger.Info("开始区块同步", "peer", peerID.String(), "目标高度", peerLatestBlock)

	localLatest := s.blockchain.Header().Number
	shouldTerminate := false
	
	s.logger.Debug("同步参数", 
		"peer", peerID.String(), 
		"peerLatestBlock", peerLatestBlock, 
		"localLatest", localLatest, 
		"startFrom", localLatest+1)

	s.logger.Info("🔍 准备获取区块流", "peer", peerID.String(), "从高度", localLatest+1, "到高度", peerLatestBlock)
	blockCh, err := s.syncPeerClient.GetBlocks(peerID, localLatest+1, s.blockTimeout)
	if err != nil {
		s.logger.Error("获取区块流失败", "peer", peerID.String(), "error", err)
		return 0, false, err
	}
	s.logger.Info("✅ 区块流获取成功", "peer", peerID.String(), "从高度", localLatest+1)

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
				s.logger.Info("区块同步完成", "peer", peerID.String(), "同步区块数", blockCount)
				return lastReceivedNumber, shouldTerminate, nil
			}
			
			s.logger.Info("🔍 从区块流接收到区块", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

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
				s.logger.Info("✅ DPoS区块同步成功", "peer", peerID.String(), "区块号", block.Number(), "哈希", block.Hash().String()[:16])
				shouldTerminate = newBlockCallback(fullBlock)
				lastReceivedNumber = block.Number()
				continue
			}

			s.logger.Info("🔍 开始验证区块", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))
			
			// 🆕 在验证前检查BLS公钥是否加载完成
			if err := s.waitForBLSKeysLoaded(); err != nil {
				s.logger.Warn("⚠️ 等待BLS公钥加载失败，继续验证区块", "error", err)
			}
			
			fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				s.logger.Error("区块验证失败", "peer", peerID.String(), "区块号", block.Number(), "error", err)
				return lastReceivedNumber, false, fmt.Errorf("unable to verify block, %w", err)
			}
			s.logger.Info("✅ 区块验证完成", "peer", peerID.String()[:8], "区块号", block.Number(), "时间戳", time.Now().Format("15:04:05.000"))

			s.logger.Info("🔒 同步器调用WriteFullBlock", "blockNumber", block.Number(), "peer", peerID.String())
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

// GetSyncPeerClient returns the sync peer client for controlling status broadcasting
func (s *syncer) GetSyncPeerClient() SyncPeerClient {
	return s.syncPeerClient
}

// waitForBLSKeysLoaded 等待BLS公钥加载完成
func (s *syncer) waitForBLSKeysLoaded() error {
	// 先检查BLS公钥是否已经加载完成（缓存检查）
	s.blsKeysMutex.RLock()
	if s.blsKeysLoaded {
		s.blsKeysMutex.RUnlock()
		fmt.Printf("✅ syncer: BLS公钥已加载完成（缓存），跳过等待 blockNumber=%d\n", s.blockchain.Header().Number)
		return nil
	}
	s.blsKeysMutex.RUnlock()
	
	// 通过检查节点目录下的dpos.db文件是否存在来判断是否是DPoS共识
	// 从命令行参数中解析节点目录前缀
	nodeDirPrefix := s.extractNodeDirPrefix()
	fmt.Printf("🔍 syncer: 解析的节点目录前缀: '%s' blockNumber=%d\n", nodeDirPrefix, s.blockchain.Header().Number)
	
	// 检查 dpos.db 文件
	dposFilePath := filepath.Join(nodeDirPrefix, "consensus", "dpos", "dpos.db")
	fmt.Printf("🔍 syncer: 检查DPoS文件路径: %s blockNumber=%d\n", dposFilePath, s.blockchain.Header().Number)
	
	if _, err := os.Stat(dposFilePath); err == nil {
		fmt.Printf("✅ syncer: 检测到DPoS共识，开始等待BLS公钥加载完成 blockNumber=%d\n", s.blockchain.Header().Number)
		
		// 真正检查BLS公钥是否加载完成
		if err := s.waitForBLSKeysActuallyLoaded(); err != nil {
			fmt.Printf("⚠️ syncer: 等待BLS公钥加载失败 blockNumber=%d error=%v\n", s.blockchain.Header().Number, err)
			return err
		}
		
		// 设置BLS公钥加载完成标志
		s.blsKeysMutex.Lock()
		s.blsKeysLoaded = true
		s.blsKeysMutex.Unlock()
		
		fmt.Printf("✅ syncer: DPoS BLS公钥加载完成 blockNumber=%d\n", s.blockchain.Header().Number)
		return nil
	} else {
		fmt.Printf("❌ syncer: DPoS文件不存在: %s error=%v blockNumber=%d\n", dposFilePath, err, s.blockchain.Header().Number)
	}
	
	// 如果不是DPoS共识，直接返回成功
	fmt.Printf("ℹ️ syncer: 非DPoS共识，跳过BLS公钥等待 blockNumber=%d\n", s.blockchain.Header().Number)
	return nil
}

// extractNodeDirPrefix 从命令行参数中提取节点目录前缀
func (s *syncer) extractNodeDirPrefix() string {
	// 从命令行参数中获取配置文件路径
	// 例如: ./main server --config ./node1/node-config-validator.yaml
	// 需要提取 ./node1 作为节点目录前缀
	
	// 尝试从环境变量获取（如果设置了的话）
	if nodeDirPrefix := os.Getenv("NODE_DIR_PREFIX"); nodeDirPrefix != "" {
		return nodeDirPrefix
	}
	
	// 从命令行参数中解析配置文件路径
	// 通过os.Args获取命令行参数
	args := os.Args
	fmt.Printf("🔍 syncer: 命令行参数: %v\n", args)
	
	for i, arg := range args {
		if arg == "--config" && i+1 < len(args) {
			configPath := args[i+1]
			fmt.Printf("🔍 syncer: 配置文件路径: %s\n", configPath)
			
			// 从配置文件路径中提取目录前缀
			// 例如: ./node1/node-config-validator.yaml -> ./node1
			dir := filepath.Dir(configPath)
			fmt.Printf("🔍 syncer: 提取的目录前缀: %s\n", dir)
			
			// 检查这个目录下是否有dpos.db文件
			dposFilePath := filepath.Join(dir, "consensus", "dpos", "dpos.db")
			if _, err := os.Stat(dposFilePath); err == nil {
				fmt.Printf("🔍 syncer: 找到DPoS文件在目录: %s\n", dir)
				return dir
			} else {
				fmt.Printf("🔍 syncer: DPoS文件不存在: %s error=%v\n", dposFilePath, err)
			}
			break
		}
	}
	
	// 如果都没找到，返回当前目录
	return "."
}

// waitForBLSKeysActuallyLoaded 真正等待BLS公钥加载完成
func (s *syncer) waitForBLSKeysActuallyLoaded() error {
	fmt.Printf("⏳ syncer: 开始检查BLS公钥加载状态 blockNumber=%d\n", s.blockchain.Header().Number)
	
	// 检查DPoS数据库中的BLS公钥状态
	nodeDirPrefix := s.extractNodeDirPrefix()
	blsDbPath := filepath.Join(nodeDirPrefix, "consensus", "dpos", "dpos.db")
	
	// 轮询检查BLS公钥是否加载完成
	maxRetries := 30 // 最多检查30次
	retryInterval := 2 * time.Second // 每2秒检查一次
	
	for i := 0; i < maxRetries; i++ {
		fmt.Printf("🔍 syncer: 检查BLS公钥状态 第%d次 blockNumber=%d\n", i+1, s.blockchain.Header().Number)
		
		// 检查数据库文件是否存在且不为空
		if stat, err := os.Stat(blsDbPath); err == nil && stat.Size() > 0 {
			fmt.Printf("✅ syncer: BLS数据库文件存在且不为空 size=%d blockNumber=%d\n", stat.Size(), s.blockchain.Header().Number)
			
			// 检查数据库中是否有BLS公钥记录
			if s.checkBLSKeysInDatabase(blsDbPath) {
				fmt.Printf("✅ syncer: BLS公钥已加载到数据库 blockNumber=%d\n", s.blockchain.Header().Number)
				return nil
			} else {
				fmt.Printf("⏳ syncer: BLS数据库存在但公钥未加载完成，继续等待 blockNumber=%d\n", s.blockchain.Header().Number)
			}
		} else {
			fmt.Printf("⏳ syncer: BLS数据库文件不存在或为空，继续等待 blockNumber=%d\n", s.blockchain.Header().Number)
		}
		
		// 等待一段时间后再次检查
		time.Sleep(retryInterval)
	}
	
	return fmt.Errorf("BLS公钥加载超时，检查了%d次仍未完成", maxRetries)
}

// checkBLSKeysInDatabase 检查数据库中是否有BLS公钥记录
func (s *syncer) checkBLSKeysInDatabase(dbPath string) bool {
	// 真正检查BLS公钥是否加载完成
	// 通过检查DPoS共识是否真正启动并加载了BLS公钥
	
	// 检查共识对象是否有waitForBLSKeysLoaded方法
	consensus := s.blockchain.GetConsensus()
	fmt.Printf("🔍 syncer: 检查共识对象是否有BLS等待方法 blockNumber=%d\n", s.blockchain.Header().Number)
	
	// 尝试调用BLS等待方法
	if dposInstance, ok := consensus.(interface{ waitForBLSKeysLoaded() error }); ok {
		fmt.Printf("🔍 syncer: 找到BLS等待方法，调用等待 blockNumber=%d\n", s.blockchain.Header().Number)
		if err := dposInstance.waitForBLSKeysLoaded(); err != nil {
			fmt.Printf("⚠️ syncer: BLS等待方法调用失败 blockNumber=%d error=%v\n", s.blockchain.Header().Number, err)
			return false
		}
		fmt.Printf("✅ syncer: BLS等待方法调用成功 blockNumber=%d\n", s.blockchain.Header().Number)
		return true
	}
	
	// 如果共识对象没有BLS等待方法，检查是否有其他BLS相关方法
	if dposInstance, ok := consensus.(interface{ GetBLSKeyForValidator(types.Address) (*bls.PublicKey, error) }); ok {
		fmt.Printf("🔍 syncer: 找到BLS获取方法，检查BLS公钥 blockNumber=%d\n", s.blockchain.Header().Number)
		
		// 尝试获取一个测试地址的BLS公钥来验证BLS公钥是否真正可用
		// 这里使用一个已知的验证者地址进行测试
		testAddress := types.Address{0x5d, 0x1F, 0x45, 0xB8, 0xD5, 0xa5, 0xeC, 0x9c, 0x3B, 0xEb, 0x91, 0xcA, 0xEb, 0xA6, 0xa3, 0x18, 0x0D, 0xBe, 0xC7, 0xA9}
		if blsKey, err := dposInstance.GetBLSKeyForValidator(testAddress); err == nil && blsKey != nil {
			fmt.Printf("✅ syncer: BLS公钥验证成功，公钥存在 blockNumber=%d\n", s.blockchain.Header().Number)
			return true
		} else {
			fmt.Printf("⚠️ syncer: BLS公钥验证失败 blockNumber=%d error=%v\n", s.blockchain.Header().Number, err)
			return false
		}
	}
	
	fmt.Printf("⚠️ syncer: 共识对象没有BLS相关方法 blockNumber=%d\n", s.blockchain.Header().Number)
	return false
}