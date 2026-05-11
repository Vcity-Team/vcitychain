package syncer

import (
	"context"
	"math/big"
	"time"

	rawGrpc "google.golang.org/grpc"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/network/event"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

const syncerMetrics = "syncer"

type Blockchain interface {
	// SubscribeEvents subscribes new blockchain event
	SubscribeEvents() blockchain.Subscription
	// UnsubscribeEvents unsubscribes from new blockchain event
	UnsubscribeEvents(blockchain.Subscription)
	// Header returns get latest header
	Header() *types.Header
	// GetBlockByNumber returns block by number
	GetBlockByNumber(uint64, bool) (*types.Block, bool)
	// VerifyFinalizedBlock verifies finalized block
	VerifyFinalizedBlock(block *types.Block) (*types.FullBlock, error)
	// WriteBlock writes a given block to chain
	WriteBlock(*types.Block, string) error
	// WriteFullBlock writes a given block to chain and saves its receipts to cache
	WriteFullBlock(*types.FullBlock, string) error
	// WriteBlockWithoutConsensus writes a block without consensus verification
	WriteBlockWithoutConsensus(*types.Block, string) error
	// GetConsensus returns the consensus verifier
	GetConsensus() blockchain.Verifier
}

type Network interface {
	// AddrInfo returns Network Info
	AddrInfo() *peer.AddrInfo
	// RegisterProtocol registers gRPC service
	RegisterProtocol(string, network.Protocol)
	// Peers returns current connected peers
	Peers() []*network.PeerConnInfo
	// SubscribeCh returns a channel of peer event
	SubscribeCh(context.Context) (<-chan *event.PeerEvent, error)
	// GetPeerDistance returns the distance between the node and given peer
	GetPeerDistance(peer.ID) *big.Int
	// NewProtoConnection opens up a new stream on the set protocol to the peer,
	// and returns a reference to the connection
	NewProtoConnection(protocol string, peerID peer.ID) (*rawGrpc.ClientConn, error)
	// GetProtocolStream returns a previously saved gRPC ClientConn for peer/protocol, or nil.
	GetProtocolStream(protocol string, peerID peer.ID) *rawGrpc.ClientConn
	// NewTopic Creates New Topic for gossip
	NewTopic(protoID string, obj proto.Message) (*network.Topic, error)
	// IsConnected returns the node is connecting to the peer associated with the given ID
	IsConnected(peerID peer.ID) bool
	// SaveProtocolStream saves stream
	SaveProtocolStream(protocol string, stream *rawGrpc.ClientConn, peerID peer.ID)
	// CloseProtocolStream closes stream
	CloseProtocolStream(protocol string, peerID peer.ID) error
	// DisconnectFromPeer 主动断开与指定 peer 的连接，便于重连后恢复（如 syncer 开流反复失败时）
	DisconnectFromPeer(peerID peer.ID, reason string)
}

type Syncer interface {
	// Start starts syncer processes
	Start() error
	// Close terminates syncer process
	Close() error
	// GetSyncProgression returns sync progression
	GetSyncProgression() *progress.Progression
	// HasSyncPeer returns whether syncer has the peer syncer can sync with
	HasSyncPeer() bool
	// Sync starts routine to sync blocks
	Sync(func(*types.FullBlock) bool) error
	// EnablePublishingPeerStatus enables publishing own status via gossip
	EnablePublishingPeerStatus()
	// DisablePublishingPeerStatus disables publishing own status via gossip
	DisablePublishingPeerStatus()
	// GetBestPeerNumber returns the latest block number from the best peer
	GetBestPeerNumber() uint64
	// GetVerifiedBestPeerNumber 对宣称高于本地的 Best peer 尝试拉取 local+1 验证父哈希；失败则短期忽略该 peer 宣称并换 peer。
	// 无法验证（含同步占用流）时返回 0，由调用方结合 GetTrustedPeerNumber 使用。
	GetVerifiedBestPeerNumber() uint64
	// GetTrustedPeerNumber returns the latest block number from a recently verified peer.
	// If no peer has been verified recently, it returns 0.
	GetTrustedPeerNumber() uint64
	// KickSync 进程内软性重启同步：刷新 peer 图、关闭同步链路上的流并多次唤醒 Sync 循环。
	KickSync(reason string)
	// TryProbeCanonicalNextBeforeProduce 出块前 P1：从 peer 拉取 localTip+1，若父哈希与本地链尖一致则返回 true（应放弃本轮本地出块，由同步落块）。
	TryProbeCanonicalNextBeforeProduce(probeTimeout time.Duration) bool
}

type Progression interface {
	// StartProgression starts progression
	StartProgression(startingBlock uint64, subscription blockchain.Subscription)
	// UpdateHighestProgression updates highest block number
	UpdateHighestProgression(highestBlock uint64)
	// GetProgression returns Progression
	GetProgression() *progress.Progression
	// StopProgression finishes progression
	StopProgression()
}

type SyncPeerService interface {
	// Start starts server
	Start()
	// Close terminates running processes for SyncPeerService
	Close() error
}

type SyncPeerClient interface {
	// Start processes for SyncPeerClient
	Start() error
	// Close terminates running processes for SyncPeerClient
	Close()
	// GetPeerStatus fetches peer status
	GetPeerStatus(id peer.ID) (*NoForkPeer, error)
	// GetConnectedPeerStatuses fetches the statuses of all connecting peers
	GetConnectedPeerStatuses() []*NoForkPeer
	// GetBlocks returns a stream of blocks from given height to peer's latest.
	// Callers must invoke the returned CancelFunc when done (e.g. defer cancel()) so the
	// producer can exit if the consumer stops reading.
	GetBlocks(peer.ID, uint64, time.Duration) (<-chan *types.Block, context.CancelFunc, error)
	// GetPeerStatusUpdateCh returns a channel of peer's status update
	GetPeerStatusUpdateCh() <-chan *NoForkPeer
	// GetPeerConnectionUpdateEventCh returns peer's connection change event
	GetPeerConnectionUpdateEventCh() <-chan *event.PeerEvent
	// CloseStream close a stream
	CloseStream(peerID peer.ID) error
	// DisconnectPeer 断开与指定 peer 的连接（用于开流反复失败时促其重连）
	DisconnectPeer(peerID peer.ID)
	// DisablePublishingPeerStatus disables publishing status in syncer topic
	DisablePublishingPeerStatus()
	// EnablePublishingPeerStatus enables publishing status in syncer topic
	EnablePublishingPeerStatus()
}
