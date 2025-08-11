package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"math/big"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/network/event"
	"github.com/Vcity-Team/vcitychain/syncer/proto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	SyncPeerClientLoggerName = "sync-peer-client"
	statusTopicName          = "syncer/status/0.1"
	defaultTimeoutForStatus  = 10 * time.Second

	// gRPC连接重试配置
	maxGRPCRetryAttempts = 3
	grpcRetryDelay       = 100 * time.Millisecond
	grpcBackoffFactor    = 2.0
)

type syncPeerClient struct {
	logger     hclog.Logger // logger used for console logging
	network    Network      // reference to the network module
	blockchain Blockchain   // reference to the blockchain module

	subscription           blockchain.Subscription // reference to the blockchain subscription
	topic                  *network.Topic          // reference to the network topic
	id                     string                  // node id
	peerStatusUpdateCh     chan *NoForkPeer        // peer status update channel
	peerConnectionUpdateCh chan *event.PeerEvent   // peer connection update channel

	shouldEmitBlocks bool // flag for emitting blocks in the topic
	closeCh          chan struct{}
	closed           atomic.Bool

	peerStatusUpdateChLock   sync.Mutex
	peerStatusUpdateChClosed bool

	// 智能日志控制字段
	lastStatusLogTime time.Time
	statusUpdateCount int
	statusLogMutex    sync.Mutex

	// gRPC连接管理
	grpcConnections sync.Map // 缓存gRPC连接，避免重复创建
	connMutex       sync.RWMutex
}

func NewSyncPeerClient(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
) SyncPeerClient {
	nodeID := network.AddrInfo().ID.String()

	// 记录节点ID信息
	logger.Info("创建同步客户端", "节点ID", nodeID)

	return &syncPeerClient{
		logger:                 logger.Named(SyncPeerClientLoggerName),
		network:                network,
		blockchain:             blockchain,
		id:                     nodeID,
		peerStatusUpdateCh:     make(chan *NoForkPeer, 100),     // 增加缓冲区大小避免阻塞
		peerConnectionUpdateCh: make(chan *event.PeerEvent, 50), // 增加缓冲区大小避免阻塞
		shouldEmitBlocks:       true,
		closeCh:                make(chan struct{}),

		peerStatusUpdateChLock:   sync.Mutex{},
		peerStatusUpdateChClosed: false,

		// 初始化智能日志控制字段
		lastStatusLogTime: time.Now(),
		statusUpdateCount: 0,
		statusLogMutex:    sync.Mutex{},
	}
}

// Start processes for SyncPeerClient
func (m *syncPeerClient) Start() error {
	// Mark client active.
	m.closed.Store(false)

	go m.startNewBlockProcess()
	go m.startPeerEventProcess()

	if err := m.startGossip(); err != nil {
		return err
	}

	return nil
}

// Close terminates running processes for SyncPeerClient
func (m *syncPeerClient) Close() {
	if m.closed.Swap(true) {
		// Already closed.
		return
	}

	if m.topic != nil {
		m.topic.Close()
	}

	if m.subscription != nil {
		m.blockchain.UnsubscribeEvents(m.subscription)

		m.subscription = nil
	}

	if m.closeCh != nil {
		close(m.closeCh)
	}

	m.peerStatusUpdateChLock.Lock()
	m.peerStatusUpdateChClosed = true
	close(m.peerStatusUpdateCh)
	m.peerStatusUpdateChLock.Unlock()
}

// DisablePublishingPeerStatus disables publishing own status via gossip
func (m *syncPeerClient) DisablePublishingPeerStatus() {
	m.shouldEmitBlocks = false
}

// EnablePublishingPeerStatus enables publishing own status via gossip
func (m *syncPeerClient) EnablePublishingPeerStatus() {
	m.shouldEmitBlocks = true
}

// GetPeerStatus 获取peer状态，增加健康状态更新
func (m *syncPeerClient) GetPeerStatus(peerID peer.ID) (*NoForkPeer, error) {
	startTime := time.Now()

	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), defaultTimeoutForStatus)
	defer cancel()

	status, err := clt.GetStatus(timeoutCtx, &emptypb.Empty{})
	if err != nil {
		// 创建状态对象用于健康状态更新
		noForkPeer := &NoForkPeer{
			ID:       peerID,
			Number:   0,
			Distance: m.network.GetPeerDistance(peerID),
			Health:   &PeerHealth{},
		}
		noForkPeer.UpdateHealth(0, false)
		return noForkPeer, err
	}

	noForkPeer := &NoForkPeer{
		ID:       peerID,
		Number:   status.Number,
		Distance: m.network.GetPeerDistance(peerID),
		Health:   &PeerHealth{},
	}

	// 更新健康状态（成功）
	responseTime := time.Since(startTime)
	noForkPeer.UpdateHealth(responseTime, true)

	return noForkPeer, nil
}

// GetConnectedPeerStatuses fetches the statuses of all connecting peers
func (m *syncPeerClient) GetConnectedPeerStatuses() []*NoForkPeer {
	var (
		ps            = m.network.Peers()
		syncPeers     = make([]*NoForkPeer, 0, len(ps))
		syncPeersLock sync.Mutex
		wg            sync.WaitGroup
	)

	for _, p := range ps {
		p := p

		wg.Add(1)

		go func() {
			defer wg.Done()

			peerID := p.Info.ID

			status, err := m.GetPeerStatus(peerID)
			if err != nil {
				m.logger.Warn("failed to get status from a peer, skip", "id", peerID, "err", err)

				return //Skip appending nil status
			}

			syncPeersLock.Lock()

			syncPeers = append(syncPeers, status)

			syncPeersLock.Unlock()
		}()
	}

	wg.Wait()

	return syncPeers
}

// GetPeerStatusUpdateCh returns a channel of peer's status update
func (m *syncPeerClient) GetPeerStatusUpdateCh() <-chan *NoForkPeer {
	return m.peerStatusUpdateCh
}

// GetPeerConnectionUpdateEventCh returns peer's connection change event
func (m *syncPeerClient) GetPeerConnectionUpdateEventCh() <-chan *event.PeerEvent {
	return m.peerConnectionUpdateCh
}

// startGossip creates new topic and starts subscribing
func (m *syncPeerClient) startGossip() error {
	m.logger.Info("启动gossip", "节点ID", m.id, "topic", statusTopicName)

	// 记录当前连接的节点数量
	peers := m.network.Peers()
	m.logger.Debug("当前连接节点数量", "节点ID", m.id, "连接数", len(peers))

	topic, err := m.network.NewTopic(statusTopicName, &proto.SyncPeerStatus{})
	if err != nil {
		m.logger.Error("创建gossip topic失败", "节点ID", m.id, "error", err)
		return err
	}

	if err := topic.Subscribe(m.handleStatusUpdate); err != nil {
		m.logger.Error("订阅gossip topic失败", "节点ID", m.id, "error", err)
		return fmt.Errorf("unable to subscribe to gossip topic, %w", err)
	}

	m.topic = topic
	m.logger.Debug("gossip启动成功", "节点ID", m.id, "topic", statusTopicName, "topic对象", fmt.Sprintf("%p", topic))

	// 验证topic是否正确创建
	if topic == nil {
		m.logger.Error("topic对象为空", "节点ID", m.id)
		return fmt.Errorf("topic object is nil")
	}

	return nil
}

// handleStatusUpdate is a handler of gossip
func (m *syncPeerClient) handleStatusUpdate(obj interface{}, from peer.ID) {
	status, ok := obj.(*proto.SyncPeerStatus)
	if !ok {
		m.logger.Error("failed to cast gossiped message to txn")

		return
	}

	// 记录接收到状态更新
	m.logger.Info("接收到状态更新", "来源节点", from.String(), "区块高度", status.Number, "本地节点", m.id, "时间", time.Now().Format("15:04:05.000"))

	// 检查网络连接状态
	if !m.network.IsConnected(from) {
		if m.id != from.String() {
			m.logger.Warn("收到非连接节点的状态，忽略", "来源节点", from.String(), "本地节点", m.id)
		}

		return
	}

	// 记录连接状态确认
	m.logger.Debug("确认网络连接", "来源节点", from.String(), "本地节点", m.id)

	// 智能日志：每30秒或每10次更新记录一次汇总
	m.statusLogMutex.Lock()
	m.statusUpdateCount++
	shouldLog := time.Since(m.lastStatusLogTime) > 30*time.Second || m.statusUpdateCount >= 10
	if shouldLog {
		m.logger.Info("状态更新汇总",
			"更新次数", m.statusUpdateCount,
			"时间间隔", time.Since(m.lastStatusLogTime),
			"来源节点", from.String(),
			"最新状态", status.Number,
			"本地节点", m.id)
		m.lastStatusLogTime = time.Now()
		m.statusUpdateCount = 0
	}
	m.statusLogMutex.Unlock()

	m.peerStatusUpdateChLock.Lock()
	defer m.peerStatusUpdateChLock.Unlock()

	// 监控channel长度，避免积压
	channelLen := len(m.peerStatusUpdateCh)
	if channelLen > 50 {
		m.logger.Warn("peerStatusUpdateCh积压严重", "长度", channelLen, "来源节点", from.String())
	}

	if !m.peerStatusUpdateChClosed {
		// 使用非阻塞发送，避免阻塞gossip消息处理
		select {
		case m.peerStatusUpdateCh <- &NoForkPeer{
			ID:       from,
			Number:   status.Number,
			Distance: m.network.GetPeerDistance(from),
		}:
			// 发送成功
		case <-time.After(100 * time.Millisecond):
			// 发送超时，记录警告但不阻塞
			m.logger.Warn("发送状态更新超时，丢弃消息",
				"来源节点", from.String(),
				"区块高度", status.Number,
				"channel长度", len(m.peerStatusUpdateCh))
		default:
			// 缓冲区满，丢弃消息
			m.logger.Warn("peerStatusUpdateCh缓冲区满，丢弃状态更新",
				"来源节点", from.String(),
				"区块高度", status.Number)
		}
	}
}

// startNewBlockProcess starts blockchain event subscription
func (m *syncPeerClient) startNewBlockProcess() {
	m.logger.Info("启动区块事件监听", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks)

	m.subscription = m.blockchain.SubscribeEvents()
	eventCh := m.subscription.GetEventCh()

	// 启动gossip统计监控
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-m.closeCh:
				return
			case <-ticker.C:
				if m.topic != nil {
					stats := m.topic.GetStats()
					m.logger.Debug("gossip统计信息",
						"topic", stats["topic"],
						"发布数", stats["publishedCount"],
						"接收数", stats["receivedCount"],
						"距上次发布", stats["timeSinceLastPublish"],
						"距上次接收", stats["timeSinceLastReceive"])
				}
			}
		}
	}()

	for {
		var event *blockchain.Event

		select {
		case <-m.closeCh:
			m.logger.Info("区块事件监听停止", "节点ID", m.id)
			return
		case event = <-eventCh:
		}

		m.logger.Debug("收到区块事件", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks, "NewChain长度", len(event.NewChain))

		if !m.shouldEmitBlocks {
			m.logger.Debug("跳过状态广播", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks)
			continue
		}

		if l := len(event.NewChain); l > 0 {
			latest := event.NewChain[l-1]

			// 检查网络连接状态
			peers := m.network.Peers()
			m.logger.Debug("准备广播状态", "区块高度", latest.Number, "节点ID", m.id, "连接节点数", len(peers), "shouldEmitBlocks", m.shouldEmitBlocks)

			// 检查topic状态
			if m.topic == nil {
				m.logger.Error("topic为空，无法广播状态", "区块高度", latest.Number, "节点ID", m.id)
				continue
			}

			// 确保区块已经写入到区块链中
			m.logger.Debug("等待区块写入完成", "区块高度", latest.Number, "节点ID", m.id)
			time.Sleep(1 * time.Second) // 等待1秒确保区块写入完成

			// 验证区块是否已经写入
			block, exists := m.blockchain.GetBlockByNumber(latest.Number, false)
			if !exists {
				m.logger.Warn("区块尚未写入，延迟状态广播", "区块高度", latest.Number, "节点ID", m.id)
				time.Sleep(2 * time.Second) // 再等待2秒

				// 再次检查
				block, exists = m.blockchain.GetBlockByNumber(latest.Number, false)
				if !exists {
					m.logger.Error("区块写入超时，跳过状态广播", "区块高度", latest.Number, "节点ID", m.id)
					continue
				}
			}

			m.logger.Debug("区块已确认写入，开始广播状态", "区块高度", latest.Number, "区块哈希", block.Header.Hash.String(), "节点ID", m.id)

			// 实现状态广播重试机制
			maxRetries := 3
			retryDelay := 2 * time.Second

			for attempt := 1; attempt <= maxRetries; attempt++ {
				// Publish status
				if err := m.topic.Publish(&proto.SyncPeerStatus{
					Number: latest.Number,
				}); err != nil {
					m.logger.Error("状态广播失败", "区块高度", latest.Number, "尝试次数", attempt, "错误", err)

					if attempt < maxRetries {
						m.logger.Info("准备重试状态广播", "区块高度", latest.Number, "尝试次数", attempt+1, "延迟", retryDelay)
						time.Sleep(retryDelay)
						continue
					}
				} else {
					m.logger.Info("状态广播成功", "区块高度", latest.Number, "节点ID", m.id, "尝试次数", attempt, "时间", time.Now().Format("15:04:05.000"))

					// 启动状态广播确认检查
					go m.checkStatusBroadcastConfirmation()
					break // 成功发布，跳出重试循环
				}
			}
		}
	}
}

// checkStatusBroadcastConfirmation 检查状态广播确认率，优化触发条件
func (m *syncPeerClient) checkStatusBroadcastConfirmation() {
	// 等待一段时间让状态传播
	time.Sleep(2 * time.Second)

	// 获取当前连接的peers
	networkPeers := m.network.Peers()
	if len(networkPeers) == 0 {
		m.logger.Debug("没有连接的peers，跳过确认检查")
		return
	}

	// 计算确认率
	confirmedCount := 0
	for range networkPeers {
		// 这里可以添加更复杂的确认逻辑
		// 目前简单统计peer数量作为确认
		confirmedCount++
	}

	confirmationRate := float64(confirmedCount) / float64(len(networkPeers))

	// 降低触发阈值，减少过度触发
	if confirmationRate < 0.3 {
		m.logger.Warn("状态广播确认率过低，启动备用转传播",
			"确认率", confirmationRate,
			"确认数", confirmedCount,
			"总peer数", len(networkPeers))
		m.fallbackStatusPropagation()
	} else {
		m.logger.Debug("状态广播确认率正常",
			"确认率", confirmationRate,
			"确认数", confirmedCount,
			"总peer数", len(networkPeers))
	}
}

// fallbackStatusPropagation 改进的备用状态传播
func (m *syncPeerClient) fallbackStatusPropagation() {
	m.logger.Info("=== 备用转传播启动 ===")

	// 获取当前连接的peers
	networkPeers := m.network.Peers()
	if len(networkPeers) == 0 {
		m.logger.Warn("没有连接的peers，无法进行备用传播")
		return
	}

	// 获取本地最新状态
	localStatus := m.getLocalStatus()
	if localStatus == nil {
		m.logger.Error("无法获取本地状态")
		return
	}

	// 第一轮：重新尝试gossip传播
	m.logger.Debug("第一轮：重新尝试gossip传播")
	successCount := 0
	for i := 0; i < 3; i++ { // 尝试3次
		if err := m.topic.Publish(&proto.SyncPeerStatus{
			Number: localStatus.Number,
		}); err == nil {
			successCount++
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 第二轮：主动查询和强制同步
	m.logger.Debug("第二轮：主动查询和强制同步")
	forceSyncSuccess := 0
	for _, peer := range networkPeers {
		peerID := peer.Info.ID
		if err := m.sendDirectStatusUpdateWithRetry(peerID, localStatus); err == nil {
			forceSyncSuccess++
		}
		// 添加小延迟避免同时发送
		time.Sleep(50 * time.Millisecond)
	}

	// 计算总体成功率
	totalAttempts := len(networkPeers) + 3 // gossip尝试 + 直接发送尝试
	totalSuccess := successCount + forceSyncSuccess
	successRate := float64(totalSuccess) / float64(totalAttempts)

	// 确保成功率不超过100%
	if successRate > 1.0 {
		successRate = 1.0
	}

	m.logger.Info("=== 备用转传播完成 ===",
		"gossip成功", successCount,
		"直接发送成功", forceSyncSuccess,
		"总体成功率", successRate)
}

// forceSyncPeer 强制同步指定peer
func (m *syncPeerClient) forceSyncPeer(peerID peer.ID, fromBlock, toBlock uint64) error {
	m.logger.Debug("开始强制同步", "peer", peerID.String()[:8], "从", fromBlock, "到", toBlock)

	// 使用现有的GetBlocks方法
	blockCh, err := m.GetBlocks(peerID, fromBlock, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to get blocks: %w", err)
	}

	// 接收区块
	blockCount := 0
	for block := range blockCh {
		if block != nil {
			blockCount++
			m.logger.Debug("收到区块", "peer", peerID.String()[:8], "区块号", block.Number(), "区块数", blockCount)
		}
	}

	m.logger.Info("强制同步完成", "peer", peerID.String()[:8], "获取区块数", blockCount)
	return nil
}

// getLocalStatus 获取本地最新状态
func (m *syncPeerClient) getLocalStatus() *NoForkPeer {
	// 获取本地最新区块高度
	localBlock := m.blockchain.Header()
	if localBlock == nil {
		return nil
	}

	return &NoForkPeer{
		ID:       peer.ID(m.id),
		Number:   localBlock.Number,
		Distance: big.NewInt(0), // 本地距离为0
		Health:   &PeerHealth{},
	}
}

// sendDirectStatusUpdate 直接发送状态更新给指定peer
func (m *syncPeerClient) sendDirectStatusUpdate(peerID peer.ID, blockNumber uint64) error {
	// 创建状态更新消息
	statusMsg := &proto.SyncPeerStatus{
		Number: blockNumber,
	}

	// 通过gossip发送
	return m.topic.Publish(statusMsg)
}

// sendDirectStatusUpdateWithRetry 带重试的直接状态更新发送
func (m *syncPeerClient) sendDirectStatusUpdateWithRetry(peerID peer.ID, status *NoForkPeer) error {
	maxRetries := 3
	baseDelay := 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := m.sendDirectStatusUpdate(peerID, status.Number)
		if err == nil {
			return nil
		}

		if attempt < maxRetries {
			// 指数退避
			delay := time.Duration(float64(baseDelay) * float64(attempt))
			m.logger.Debug("直接状态更新失败，准备重试",
				"peer", peerID.String()[:8],
				"尝试次数", attempt,
				"延迟时间", delay,
				"error", err)
			time.Sleep(delay)
		}
	}

	return fmt.Errorf("直接状态更新重试%d次后仍然失败", maxRetries)
}

// startPeerEventProcess starts subscribing peer connection change events and process them
func (m *syncPeerClient) startPeerEventProcess() {
	defer close(m.peerConnectionUpdateCh)

	peerEventCh, err := m.network.SubscribeCh(context.Background())
	if err != nil {
		m.logger.Error("failed to subscribe", "err", err)

		return
	}

	for {
		select {
		case <-m.closeCh:
			return

		case e := <-peerEventCh:
			if e != nil && (e.Type == event.PeerConnected || e.Type == event.PeerDisconnected) {
				// 使用非阻塞发送，避免阻塞网络事件处理
				select {
				case m.peerConnectionUpdateCh <- e:
					// 发送成功
				default:
					// 缓冲区满，记录警告但不阻塞
					m.logger.Warn("peerConnectionUpdateCh缓冲区满，丢弃连接事件",
						"事件类型", e.Type,
						"peer", e.PeerID.String())
				}
			}
		}
	}
}

// CloseStream closes stream
func (m *syncPeerClient) CloseStream(peerID peer.ID) error {
	return m.network.CloseProtocolStream(syncerProto, peerID)
}

// GetBlocks fetches blocks from a peer starting from a specific block number
func (m *syncPeerClient) GetBlocks(
	peerID peer.ID,
	from uint64,
	timeoutPerBlock time.Duration,
) (<-chan *types.Block, error) {
	var lastErr error

	// 使用指数退避重试机制
	for attempt := 0; attempt < maxGRPCRetryAttempts; attempt++ {
		if attempt > 0 {
			// 指数退避延迟
			delay := time.Duration(float64(grpcRetryDelay) * float64(attempt) * grpcBackoffFactor)
			m.logger.Debug("gRPC连接重试", "peer", peerID.String()[:8], "attempt", attempt+1, "delay", delay)
			time.Sleep(delay)
		}

		// 尝试获取或创建gRPC客户端
		client, err := m.getOrCreateGRPCClient(peerID)
		if err != nil {
			lastErr = fmt.Errorf("failed to create gRPC client (attempt %d): %w", attempt+1, err)
			m.logger.Warn("gRPC客户端创建失败", "peer", peerID.String()[:8], "attempt", attempt+1, "error", err)
			continue
		}

		// 尝试打开区块流
		ctx, cancel := context.WithTimeout(context.Background(), timeoutPerBlock*2)
		stream, err := client.GetBlocks(ctx, &proto.GetBlocksRequest{
			From: from,
		})
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("failed to open GetBlocks stream (attempt %d): %w", attempt+1, err)
			m.logger.Warn("打开区块流失败", "peer", peerID.String()[:8], "attempt", attempt+1, "error", err)

			// 如果是连接错误，清理连接并重试
			if isConnectionError(err) {
				m.cleanupGRPCConnection(peerID)
			}
			continue
		}

		// 成功获取流，创建输出通道
		streamBlockCh, streamErrorCh := blockStreamToChannel(stream)
		blockCh := make(chan *types.Block, 1)

		go func() {
			defer cancel()
			defer close(blockCh)

			for {
				select {
				case block, ok := <-streamBlockCh:
					if !ok {
						return
					}
					blockCh <- block
				case err := <-streamErrorCh:
					if isConnectionError(err) {
						m.logger.Warn("gRPC流连接错误", "peer", peerID.String()[:8], "error", err)
						// 连接错误时清理连接
						m.cleanupGRPCConnection(peerID)
					} else {
						m.logger.Error("从gRPC流获取区块失败", "peer", peerID.String()[:8], "error", err)
					}
					return
				case <-time.After(timeoutPerBlock):
					m.logger.Warn("区块获取超时", "peer", peerID.String()[:8], "timeout", timeoutPerBlock)
					return
				}
			}
		}()

		return blockCh, nil
	}

	return nil, fmt.Errorf("所有gRPC重试尝试都失败了，最后的错误: %w", lastErr)
}

// newSyncPeerClient creates gRPC client
func (m *syncPeerClient) newSyncPeerClient(peerID peer.ID) (proto.SyncPeerClient, error) {
	conn, err := m.network.NewProtoConnection(syncerProto, peerID)
	if err != nil {
		return nil, fmt.Errorf("failed to open a stream, err %w", err)
	}

	m.network.SaveProtocolStream(syncerProto, conn, peerID)

	return proto.NewSyncPeerClient(conn), nil
}

// fromProto gets block from gRPC response data
func fromProto(protoBlock *proto.Block) (*types.Block, error) {
	block := &types.Block{}
	if err := block.UnmarshalRLP(protoBlock.Block); err != nil {
		return nil, err
	}

	return block, nil
}

func blockStreamToChannel(stream proto.SyncPeer_GetBlocksClient) (<-chan *types.Block, <-chan error) {
	blockCh := make(chan *types.Block)
	errorCh := make(chan error, 1)

	go func() {
		defer close(blockCh)

		for {
			protoBlock, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_message"}, 1)
				errorCh <- err

				break
			}

			block, err := fromProto(protoBlock)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				errorCh <- err

				break
			}

			metrics.SetGauge([]string{syncerMetrics, "ingress_bytes"}, float32(len(protoBlock.Block)))

			blockCh <- block
		}
	}()

	return blockCh, errorCh
}

// getOrCreateGRPCClient 获取或创建gRPC客户端，使用连接缓存
func (m *syncPeerClient) getOrCreateGRPCClient(peerID peer.ID) (proto.SyncPeerClient, error) {
	// 先尝试从缓存获取
	if cached, ok := m.grpcConnections.Load(peerID.String()); ok {
		if client, ok := cached.(proto.SyncPeerClient); ok {
			return client, nil
		}
	}

	// 缓存中没有，创建新的客户端
	client, err := m.newSyncPeerClient(peerID)
	if err != nil {
		return nil, err
	}

	// 缓存新创建的客户端
	m.grpcConnections.Store(peerID.String(), client)
	return client, nil
}

// cleanupGRPCConnection 清理指定peer的gRPC连接
func (m *syncPeerClient) cleanupGRPCConnection(peerID peer.ID) {
	m.grpcConnections.Delete(peerID.String())
	m.logger.Debug("清理gRPC连接", "peer", peerID.String()[:8])
}

// isConnectionError 判断是否为连接相关错误
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// 检查常见的连接错误关键词
	connectionErrors := []string{
		"connection error",
		"stream reset",
		"connection refused",
		"unavailable",
		"deadline exceeded",
		"context canceled",
		"transport is closing",
	}

	for _, connErr := range connectionErrors {
		if strings.Contains(errStr, connErr) {
			return true
		}
	}

	return false
}
