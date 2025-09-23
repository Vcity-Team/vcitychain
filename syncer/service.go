package syncer

import (
	"context"
	"errors"

	"github.com/Vcity-Team/vcitychain/network/grpc"
	"github.com/Vcity-Team/vcitychain/syncer/proto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/armon/go-metrics"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/hashicorp/go-hclog"
)

var (
	ErrBlockNotFound = errors.New("block not found")
)

type syncPeerService struct {
	proto.UnimplementedSyncPeerServer

	logger     hclog.Logger     // logger for logging
	blockchain Blockchain       // reference to the blockchain module
	network    Network          // reference to the network module
	stream     *grpc.GrpcStream // reference to the grpc stream
}

func NewSyncPeerService(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
) SyncPeerService {
	return &syncPeerService{
		logger:     logger.Named("sync-peer-service"),
		blockchain: blockchain,
		network:    network,
	}
}

// Start starts syncPeerService
func (s *syncPeerService) Start() {
	s.setupGRPCServer()
}

// Close closes syncPeerService
func (s *syncPeerService) Close() error {
	return s.stream.Close()
}

// setupGRPCServer setup GRPC server
func (s *syncPeerService) setupGRPCServer() {
	s.stream = grpc.NewGrpcStream()

	proto.RegisterSyncPeerServer(s.stream.GrpcServer(), s)
	s.stream.Serve()
	s.network.RegisterProtocol(syncerProto, s.stream)
}

// GetBlocks is a gRPC endpoint to return blocks from the specific height via stream
func (s *syncPeerService) GetBlocks(
	req *proto.GetBlocksRequest,
	stream proto.SyncPeer_GetBlocksServer,
) error {
	// 获取请求者信息
	peerInfo := "unknown"
	if ctx := stream.Context(); ctx != nil {
		// 尝试从grpc.Context中获取PeerID
		if grpcCtx, ok := ctx.(*grpc.Context); ok {
			peerInfo = grpcCtx.PeerID.String()[:8]
		}
	}


	var blockCount int
	// from to latest
	for i := req.From; i <= s.blockchain.Header().Number; i++ {
		block, ok := s.blockchain.GetBlockByNumber(i, true)
		if !ok {
			s.logger.Error("区块未找到", "peer", peerInfo, "区块号", i)
			return ErrBlockNotFound
		}

		resp := toProtoBlock(block)
		metrics.SetGauge([]string{syncerMetrics, "egress_bytes"}, float32(len(resp.Block)))

		// if client closes stream, context.Canceled is given
		if err := stream.Send(resp); err != nil {
			s.logger.Warn("发送区块失败", "peer", peerInfo, "区块号", i, "error", err)
			break
		}

		blockCount++
		// 只在每10个区块记录一次日志
		if blockCount%10 == 0 {
			s.logger.Debug("发送区块进度", "peer", peerInfo, "当前区块", i, "已发送", blockCount)
		}
	}

	return nil
}

// GetStatus is a gRPC endpoint to return the latest block number as a node status
func (s *syncPeerService) GetStatus(
	ctx context.Context,
	req *empty.Empty,
) (*proto.SyncPeerStatus, error) {
	var number uint64
	if header := s.blockchain.Header(); header != nil {
		number = header.Number
	}

	return &proto.SyncPeerStatus{
		Number: number,
	}, nil
}

// toProtoBlock converts type.Block -> proto.Block
func toProtoBlock(block *types.Block) *proto.Block {
	return &proto.Block{
		Block: block.MarshalRLP(),
	}
}
