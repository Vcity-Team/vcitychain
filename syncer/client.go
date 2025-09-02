package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	statusTopicName          = "syncer/status/0.1"
	defaultTimeoutForStatus  = 10 * time.Second
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

	m.logger.Info("开始关闭同步客户端", "节点ID", m.id)

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
		close(m.closeCh)
	}

	// 等待goroutine退出（最多等待5秒）
	timeout := time.After(5 * time.Second)
	done := make(chan struct{})
	go func() {
		// 等待所有goroutine退出
		time.Sleep(100 * time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		m.logger.Debug("同步客户端goroutine已退出", "节点ID", m.id)
	case <-timeout:
		m.logger.Warn("同步客户端关闭超时", "节点ID", m.id)
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

	m.logger.Info("同步客户端已关闭", "节点ID", m.id)
}

// DisablePublishingPeerStatus disables publishing own status via gossip
func (m *syncPeerClient) DisablePublishingPeerStatus() {
	m.shouldEmitBlocks = false
}

// EnablePublishingPeerStatus enables publishing own status via gossip
func (m *syncPeerClient) EnablePublishingPeerStatus() {
	m.shouldEmitBlocks = true
}

// GetPeerStatus fetches peer status
func (m *syncPeerClient) GetPeerStatus(peerID peer.ID) (*NoForkPeer, error) {
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

// startGossip creates new topic and starts subscribing
func (m *syncPeerClient) startGossip() error {
	m.logger.Info("启动gossip", "节点ID", m.id, "topic", statusTopicName)

	// 记录当前连接的节点数量
	peers := m.network.Peers()
	m.logger.Info("当前连接节点数量", "节点ID", m.id, "连接数", len(peers))

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
	m.logger.Info("gossip启动成功", "节点ID", m.id, "topic", statusTopicName)

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

	// 记录接收到状态更新
	m.logger.Debug("接收到状态更新", "来源节点", from.String(), "区块高度", status.Number, "本地节点", m.id)

	// 检查网络连接状态
	if !m.network.IsConnected(from) {
		if m.id != from.String() {
			m.logger.Warn("收到非连接节点的状态，忽略", "来源节点", from.String(), "本地节点", m.id)
		}

		return
	}

	// 记录连接状态确认
	m.logger.Debug("确认网络连接", "来源节点", from.String(), "本地节点", m.id)

	// 添加网络诊断信息
	peers := m.network.Peers()
	peerCount := len(peers)

	// 记录网络状态统计
	m.logger.Debug("网络状态统计",
		"来源节点", from.String(),
		"本地节点", m.id,
		"总连接节点数", peerCount,
		"接收消息大小", 2) // 状态消息固定为2字节

	// 智能日志：每30秒或每10次更新记录一次汇总
	m.statusLogMutex.Lock()
	m.statusUpdateCount++
	shouldLog := time.Since(m.lastStatusLogTime) > 30*time.Second || m.statusUpdateCount >= 10
	if shouldLog {
		m.logger.Debug("状态更新汇总",
			"更新次数", m.statusUpdateCount,
			"时间间隔", time.Since(m.lastStatusLogTime),
			"来源节点", from.String(),
			"最新状态", status.Number,
			"本地节点", m.id,
			"网络连接数", peerCount)
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
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("startNewBlockProcess panic", "节点ID", m.id, "error", r)
		}
		m.logger.Debug("startNewBlockProcess goroutine已退出", "节点ID", m.id)
	}()

	m.logger.Info("启动区块事件监听", "节点ID", m.id, "shouldEmitBlocks", m.shouldEmitBlocks)

	// 添加超时保护
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	m.subscription = m.blockchain.SubscribeEvents()
	eventCh := m.subscription.GetEventCh()

	for {
		var event *blockchain.Event

		select {
		case <-ctx.Done():
			m.logger.Info("区块事件监听超时退出", "节点ID", m.id)
			return
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
			m.logger.Debug("准备广播状态", "区块高度", latest.Number, "节点ID", m.id, "连接节点数", len(peers))

			// 记录状态广播开始
			m.logger.Debug("开始广播状态", "区块高度", latest.Number, "节点ID", m.id)

			// 添加网络状态检查
			if len(peers) == 0 {
				m.logger.Warn("没有连接的节点，跳过状态广播", "区块高度", latest.Number, "节点ID", m.id)
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
					m.logger.Warn("状态广播失败，准备重试",
						"区块高度", latest.Number,
						"重试次数", retry+1,
						"最大重试次数", maxRetries,
						"错误", err)

					// 短暂等待后重试
					time.Sleep(100 * time.Millisecond)
				} else {
					publishErr = nil
					break
				}
			}

			if publishErr != nil {
				m.logger.Error("状态广播最终失败", "区块高度", latest.Number, "错误", publishErr)
			} else {
				m.logger.Debug("状态广播成功", "区块高度", latest.Number, "节点ID", m.id)
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
	}()

	// 添加超时保护
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	peerEventCh, err := m.network.SubscribeCh(ctx)
	if err != nil {
		m.logger.Error("failed to subscribe", "err", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("peer事件监听超时退出", "节点ID", m.id)
			return
		case <-m.closeCh:
			m.logger.Info("peer事件监听停止", "节点ID", m.id)
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

// GetBlocks returns a stream of blocks from given height to peer's latest
func (m *syncPeerClient) GetBlocks(
	peerID peer.ID,
	from uint64,
	timeoutPerBlock time.Duration,
) (<-chan *types.Block, error) {
	m.logger.Debug("请求区块", "peer", peerID.String(), "起始高度", from)

	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		m.logger.Error("创建同步客户端失败", "peer", peerID.String()[:8], "error", err)
		return nil, fmt.Errorf("failed to create sync peer client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	stream, err := clt.GetBlocks(ctx, &proto.GetBlocksRequest{
		From: from,
	})
	if err != nil {
		cancel()
		m.logger.Error("打开区块流失败", "peer", peerID.String()[:8], "error", err)
		return nil, fmt.Errorf("failed to open GetBlocks stream: %w", err)
	}

	// input channel
	streamBlockCh, streamErrorCh := blockStreamToChannel(stream)

	// output channel
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
				m.logger.Error("failed to get block from gRPC stream", "peer", peerID, "err", err)

				return
			case <-time.After(timeoutPerBlock):
				m.logger.Warn("block doesn't reach within timeout", "timeout", timeoutPerBlock)

				return
			}
		}
	}()

	return blockCh, nil
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
				m.logger.Debug("Topic状态正常", "节点ID", m.id, "topic", statusTopicName)
			} else {
				m.logger.Error("Topic未初始化", "节点ID", m.id)
			}
		}
	}
}
