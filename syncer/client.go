package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	defaultTimeoutForStatus  = 5 * time.Second // 对端短暂抖动更宽容，减少误判为不可用peer
)

type syncPeerClient struct {
	logger     hclog.Logger // logger used for console logging
	network    Network      // reference to the network module
	blockchain Blockchain   // reference to the blockchain module

	subscription           blockchain.Subscription // reference to the blockchain subscription
	topic                  *network.Topic          // reference to the network topic
	id                     string                  // node id
	statusTopicName        string                  // unique topic name for this node
	peerStatusUpdateCh     chan *NoForkPeer        // peer status update channel
	peerConnectionUpdateCh chan *event.PeerEvent   // peer connection update channel

	shouldEmitBlocks bool // flag for emitting blocks in the topic
	closeCh          chan struct{}
	closed           atomic.Bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	activeGetBlocksStreams atomic.Int64

	// 方案C：限制并发与单peer单流，避免抖动时重试风暴导致goroutine爆炸
	ioSem chan struct{} // global semaphore for RPC/stream operations

	inflightMu        sync.Mutex
	inflightGetBlocks map[peer.ID]struct{}

	peerStatusUpdateChLock   sync.Mutex
	peerStatusUpdateChClosed bool

	// 智能日志控制字段
	lastStatusLogTime time.Time
	statusUpdateCount int
	statusLogMutex    sync.Mutex
}

func NewSyncPeerClient(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
) SyncPeerClient {
	nodeID := network.AddrInfo().ID.String()

	// 记录节点ID信息
	logger.Debug("创建同步客户端", "节点ID", nodeID)

	ctx, cancel := context.WithCancel(context.Background())

	return &syncPeerClient{
		logger:                 logger.Named(SyncPeerClientLoggerName),
		network:                network,
		blockchain:             blockchain,
		id:                     nodeID,
		statusTopicName:        "syncer/status/0.1",             // 所有节点使用相同的topic名称进行状态广播
		peerStatusUpdateCh:     make(chan *NoForkPeer, 500),     // 缓冲足够多 peer 的状态推送，减轻积压
		peerConnectionUpdateCh: make(chan *event.PeerEvent, 50), // 增加缓冲区大小避免阻塞
		shouldEmitBlocks:       true,
		closeCh:                make(chan struct{}),
		ctx:                    ctx,
		cancel:                 cancel,
		ioSem:                  make(chan struct{}, 16),
		inflightGetBlocks:      make(map[peer.ID]struct{}),

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

	// 先启动gossip，确保topic初始化完成
	if err := m.startGossip(); err != nil {
		// 检查是否是topic冲突错误，如果是则直接创建新的后缀topic
		if strings.Contains(err.Error(), "topic already exists") {
			m.logger.Debug("⚠️ topic冲突，直接创建带后缀的新topic", "节点ID", m.id, "error", err)

			if !m.createAlternativeTopic() {
				m.logger.Error("❌ 无法创建可用topic，状态广播将不可用", "节点ID", m.id)
				// 继续启动其他功能
			}
		} else {
			return err
		}
	}

	// 然后启动其他goroutine
	m.wg.Add(2)
	go m.startNewBlockProcess()
	go m.startPeerEventProcess()

	return nil
}

// Close terminates running processes for SyncPeerClient
func (m *syncPeerClient) Close() {
	if m.closed.Swap(true) {
		// Already closed.
		return
	}

	m.logger.Debug("开始关闭同步客户端", "节点ID", m.id)

	// 先发出取消信号，确保所有使用 ctx 的逻辑尽快退出
	if m.cancel != nil {
		m.cancel()
	}

	// 关闭所有订阅和topic
	if m.topic != nil {
		m.topic.Close()
		m.topic = nil
	}

	if m.subscription != nil {
		m.blockchain.UnsubscribeEvents(m.subscription)
		m.subscription = nil
	}

	// 发送关闭信号
	if m.closeCh != nil {
		select {
		case <-m.closeCh:
			// already closed
		default:
			close(m.closeCh)
		}
	}

	// 等待goroutine退出（最多等待5秒）
	timeout := time.After(5 * time.Second)
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Debug("同步客户端goroutine已退出", "节点ID", m.id)
	case <-timeout:
		m.logger.Warn("同步客户端关闭超时", "节点ID", m.id, "activeGetBlocksStreams", m.activeGetBlocksStreams.Load(), "goroutines", runtime.NumGoroutine())
	}

	// 关闭状态更新通道
	m.peerStatusUpdateChLock.Lock()
	if !m.peerStatusUpdateChClosed {
		m.peerStatusUpdateChClosed = true
		close(m.peerStatusUpdateCh)
	}
	m.peerStatusUpdateChLock.Unlock()

	// 关闭连接更新通道
	select {
	case <-m.peerConnectionUpdateCh:
		// 通道已经关闭
	default:
		close(m.peerConnectionUpdateCh)
	}

	m.logger.Debug("同步客户端已关闭", "节点ID", m.id)
}

// DisablePublishingPeerStatus disables publishing own status via gossip
func (m *syncPeerClient) DisablePublishingPeerStatus() {
	m.shouldEmitBlocks = false
}

// EnablePublishingPeerStatus enables publishing own status via gossip
func (m *syncPeerClient) EnablePublishingPeerStatus() {
	m.shouldEmitBlocks = true
	m.logger.Info("✅ 状态广播已启用", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks)
}

// GetPeerStatus fetches peer status
func (m *syncPeerClient) GetPeerStatus(peerID peer.ID) (*NoForkPeer, error) {
	// 方案C：限制并发，避免对大量peer并发GetStatus导致goroutine激增
	select {
	case m.ioSem <- struct{}{}:
		defer func() { <-m.ioSem }()
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}

	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(context.Background(), defaultTimeoutForStatus)
	defer cancel()

	status, err := clt.GetStatus(timeoutCtx, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}

	return &NoForkPeer{
		ID:       peerID,
		Number:   status.Number,
		Distance: m.network.GetPeerDistance(peerID),
	}, nil
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

// createAlternativeTopic 创建带后缀的独立topic，避免与IBFT冲突
func (m *syncPeerClient) createAlternativeTopic() bool {
	for i := 1; i <= 5; i++ {
		alternativeTopicName := fmt.Sprintf("syncer/status/0.1_%d", i)
		m.logger.Debug("🔄 尝试替代topic名称", "节点ID", m.id, "尝试次数", i, "topic名称", alternativeTopicName)

		topic, err := m.network.NewTopic(alternativeTopicName, &proto.SyncPeerStatus{})
		if err != nil {
			m.logger.Debug("❌ 替代topic创建失败", "节点ID", m.id, "尝试次数", i, "error", err)
			continue
		}

		if err := topic.Subscribe(m.handleStatusUpdate); err != nil {
			m.logger.Error("❌ 订阅替代topic失败", "节点ID", m.id, "error", err)
			continue
		}

		m.topic = topic
		m.statusTopicName = alternativeTopicName
		m.logger.Debug("✅ 成功创建替代topic", "节点ID", m.id, "topic名称", alternativeTopicName)
		return true
	}

	return false
}

// startGossip creates new topic and starts subscribing
func (m *syncPeerClient) startGossip() error {
	m.logger.Info("启动gossip", "节点ID", m.id, "topic", m.statusTopicName)
	m.logger.Info("🔍 调试信息", "节点ID", m.id, "完整topic名称", m.statusTopicName, "节点ID长度", len(m.id))

	// 记录当前连接的节点数量
	peers := m.network.Peers()
	m.logger.Info("当前连接节点数量", "节点ID", m.id, "连接数", len(peers))

	topic, err := m.network.NewTopic(m.statusTopicName, &proto.SyncPeerStatus{})
	if err != nil {
		// 如果NewTopic失败，记录错误并返回
		m.logger.Debug("创建gossip topic失败", "节点ID", m.id, "error", err)
		return err
	} else {
		// 成功创建新topic
		if err := topic.Subscribe(m.handleStatusUpdate); err != nil {
			m.logger.Error("订阅gossip topic失败", "节点ID", m.id, "error", err)
			return fmt.Errorf("unable to subscribe to gossip topic, %w", err)
		}
		m.topic = topic
		m.logger.Info("🚨🚨🚨 m.topic成功设置为真实topic 🚨🚨🚨",
			"节点ID", m.id,
			"topic", m.statusTopicName,
			"m.topic", m.topic)
		m.logger.Info("gossip启动成功", "节点ID", m.id, "topic", m.statusTopicName)
	}

	// 启动网络健康检查协程
	go m.networkHealthCheck()

	return nil
}

// handleStatusUpdate is a handler of gossip
func (m *syncPeerClient) handleStatusUpdate(obj interface{}, from peer.ID) {
	status, ok := obj.(*proto.SyncPeerStatus)
	if !ok {
		m.logger.Error("failed to cast gossiped message to txn")

		return
	}


	// 检查网络连接状态
	if !m.network.IsConnected(from) {
		return
	}

	m.peerStatusUpdateChLock.Lock()
	defer m.peerStatusUpdateChLock.Unlock()

	channelLen := len(m.peerStatusUpdateCh)
	if channelLen > 400 {
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
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("startNewBlockProcess panic", "节点ID", m.id, "error", r)
		}
		m.logger.Debug("startNewBlockProcess goroutine已退出", "节点ID", m.id)
		m.wg.Done()
	}()

	m.logger.Info("🚀 启动区块事件监听", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks)

	// 移除超时保护，让同步器持续运行
	m.subscription = m.blockchain.SubscribeEvents()
	eventCh := m.subscription.GetEventCh()

	for {
		var event *blockchain.Event

		select {
		case <-m.closeCh:
			m.logger.Debug("区块事件监听停止", "节点ID", m.id)
			return
		case event = <-eventCh:
		}


		if !m.shouldEmitBlocks {
			m.logger.Info("❌ 跳过状态广播", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks, "原因", "shouldEmitBlocks为false")
			continue
		}

		if l := len(event.NewChain); l > 0 {
			latest := event.NewChain[l-1]

			// 检查topic是否已初始化
			if m.topic == nil {
				m.logger.Error("❌ topic未初始化，无法进行状态广播", "区块高度", latest.Number, "节点ID", m.id, "原因", "topic冲突导致无法初始化")
				m.logger.Error("❌ 这将导致其他节点无法同步此区块", "区块高度", latest.Number, "节点ID", m.id)
				continue
			}

			// 检查网络连接状态
			peers := m.network.Peers()
			if len(peers) == 0 {
				m.logger.Info("没有连接的节点，跳过状态广播", "区块高度", latest.Number, "节点ID", m.id)
				continue
			}

			// Publish status with retry mechanism
			var publishErr error
			maxRetries := 3

			for retry := 0; retry < maxRetries; retry++ {
				if err := m.topic.Publish(&proto.SyncPeerStatus{
					Number: latest.Number,
				}); err != nil {
					publishErr = err
					m.logger.Info("状态广播失败，准备重试",
						"区块高度", latest.Number,
						"重试次数", retry+1,
						"最大重试次数", maxRetries,
						"topic名称", m.statusTopicName,
						"错误", err)

					// 短暂等待后重试
					time.Sleep(100 * time.Millisecond)
				} else {
					publishErr = nil
					break
				}
			}

			if publishErr != nil {
				m.logger.Error("❌ 状态广播最终失败", "区块高度", latest.Number, "节点ID", m.id, "topic名称", m.statusTopicName, "错误", publishErr)
			}
		}
	}
}

// startPeerEventProcess starts subscribing peer connection change events and process them
func (m *syncPeerClient) startPeerEventProcess() {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("startPeerEventProcess panic", "节点ID", m.id, "error", r)
		}
		close(m.peerConnectionUpdateCh)
		m.logger.Debug("startPeerEventProcess goroutine已退出", "节点ID", m.id)
		m.wg.Done()
	}()

	// 移除超时保护，让同步器持续运行
	peerEventCh, err := m.network.SubscribeCh(m.ctx)
	if err != nil {
		m.logger.Error("failed to subscribe", "err", err)
		return
	}

	for {
		select {
		case <-m.closeCh:
			m.logger.Debug("peer事件监听停止", "节点ID", m.id)
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

// CloseStream closes stream and releases the peer's inflight mark so the same peer
// can be used again immediately (e.g. for fill-gap after MissingParent).
func (m *syncPeerClient) CloseStream(peerID peer.ID) error {
	m.inflightMu.Lock()
	delete(m.inflightGetBlocks, peerID)
	m.inflightMu.Unlock()
	return m.network.CloseProtocolStream(syncerProto, peerID)
}

// DisconnectPeer 断开与指定 peer 的连接，促其重连（syncer 开流反复失败时调用）
func (m *syncPeerClient) DisconnectPeer(peerID peer.ID) {
	m.network.DisconnectFromPeer(peerID, "syncer stream failed, force reconnect")
}

// GetBlocks returns a stream of blocks from given height to peer's latest
func (m *syncPeerClient) GetBlocks(
	peerID peer.ID,
	from uint64,
	timeoutPerBlock time.Duration,
) (<-chan *types.Block, context.CancelFunc, error) {
	m.logger.Debug("请求区块", "peer", peerID.String(), "起始高度", from)

	// 方案C：同一peer同一时间只允许一个GetBlocks流，避免重连/补拉叠加导致goroutine爆炸
	m.inflightMu.Lock()
	if _, exists := m.inflightGetBlocks[peerID]; exists {
		m.inflightMu.Unlock()
		return nil, nil, fmt.Errorf("GetBlocks already in progress for peer %s", peerID.String())
	}
	m.inflightGetBlocks[peerID] = struct{}{}
	m.inflightMu.Unlock()

	// 方案C：限制开流并发，避免瞬时开太多stream导致资源耗尽
	select {
	case m.ioSem <- struct{}{}:
		// release after stream established (or error)
	case <-m.ctx.Done():
		m.inflightMu.Lock()
		delete(m.inflightGetBlocks, peerID)
		m.inflightMu.Unlock()
		return nil, nil, m.ctx.Err()
	}

	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		<-m.ioSem
		m.inflightMu.Lock()
		delete(m.inflightGetBlocks, peerID)
		m.inflightMu.Unlock()
		m.logger.Error("创建同步客户端失败", "peer", peerID.String(), "error", err)
		return nil, nil, fmt.Errorf("failed to create sync peer client: %w", err)
	}

	ctx, cancel := context.WithCancel(m.ctx)

	stream, err := clt.GetBlocks(ctx, &proto.GetBlocksRequest{
		From: from,
	})
	if err != nil {
		<-m.ioSem
		m.inflightMu.Lock()
		delete(m.inflightGetBlocks, peerID)
		m.inflightMu.Unlock()
		cancel()
		m.logger.Error("打开区块流失败", "peer", peerID.String(), "error", err)
		return nil, nil, fmt.Errorf("failed to open GetBlocks stream: %w", err)
	}

	// stream建立完成，释放并发信号量（后续收块在独立goroutine中进行）
	<-m.ioSem

	// input channel
	streamBlockCh, streamErrorCh := blockStreamToChannel(ctx, stream)

	// output channel
	blockCh := make(chan *types.Block, 1)

	m.activeGetBlocksStreams.Add(1)

	go func() {
		defer m.activeGetBlocksStreams.Add(-1)
		defer cancel()
		defer close(blockCh)
		defer func() { _ = m.CloseStream(peerID) }()
		defer func() {
			m.inflightMu.Lock()
			delete(m.inflightGetBlocks, peerID)
			m.inflightMu.Unlock()
		}()

		// 使用可重置的 timer，避免每次 select 都创建新的 time.After
		timer := time.NewTimer(timeoutPerBlock)
		defer timer.Stop()

		resetTimer := func() {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeoutPerBlock)
		}

		resetTimer()

		for {
			select {
			case block, ok := <-streamBlockCh:
				if !ok {
					return
				}

				select {
				case blockCh <- block:
					resetTimer()
				case <-ctx.Done():
					return
				}
			case err := <-streamErrorCh:
				if err != nil {
					m.logger.Error("failed to get block from gRPC stream", "peer", peerID, "err", err)
					// 对典型的连接类错误，主动断开连接促使重连，避免 stream 残留导致持续 reset
					errStr := err.Error()
					if strings.Contains(errStr, "stream reset") ||
						strings.Contains(errStr, "transport is closing") ||
						strings.Contains(errStr, "connection error") ||
						strings.Contains(errStr, "Unavailable") {
						m.DisconnectPeer(peerID)
					}
				}

				return
			case <-timer.C:
				m.logger.Warn("block doesn't reach within timeout", "peer", peerID, "timeout", timeoutPerBlock)
				// 超时也断开 peer，避免对端卡住导致一直占用资源
				m.DisconnectPeer(peerID)

				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return blockCh, cancel, nil
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

func blockStreamToChannel(ctx context.Context, stream proto.SyncPeer_GetBlocksClient) (<-chan *types.Block, <-chan error) {
	blockCh := make(chan *types.Block)
	errorCh := make(chan error, 1)

	go func() {
		defer close(blockCh)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			protoBlock, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}

			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_message"}, 1)
				errorCh <- err
				return
			}

			block, err := fromProto(protoBlock)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)
				errorCh <- err
				return
			}

			metrics.SetGauge([]string{syncerMetrics, "ingress_bytes"}, float32(len(protoBlock.Block)))
			select {
			case blockCh <- block:
			case <-ctx.Done():
				return
			}
		}
	}()

	return blockCh, errorCh
}

// networkHealthCheck 定期检查网络状态
func (m *syncPeerClient) networkHealthCheck() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.closeCh:
			return
		case <-ticker.C:
			peers := m.network.Peers()
			peerCount := len(peers)

			// // 记录网络状态
			// m.logger.Info("网络健康检查",
			// 	"节点ID", m.id,
			// 	"连接节点数", peerCount,
			// 	"shouldEmitBlocks", m.shouldEmitBlocks)

			// 如果连接节点数过少，发出警告
			if peerCount < 2 {
				m.logger.Warn("网络连接异常，连接节点数过少",
					"节点ID", m.id,
					"连接节点数", peerCount)
			}

			// 检查topic状态
			if m.topic != nil {
				m.logger.Info("Topic状态正常", "节点ID", m.id, "topic", m.statusTopicName)
			} else {
				m.logger.Error("Topic未初始化", "节点ID", m.id)
			}
		}
	}
}
