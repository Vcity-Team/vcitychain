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
			peerIDStr := grpcCtx.PeerID.String()
			if len(peerIDStr) > 8 {
				peerInfo = peerIDStr[:8]
			} else {
				peerInfo = peerIDStr
			}
		}
	}

	// 确保 logger 不为 nil
	if s.logger == nil {
		s.logger = hclog.NewNullLogger()
	}

	// 检查 blockchain 是否为 nil
	if s.blockchain == nil {
		s.logger.Error("blockchain 未初始化", "peer", peerInfo)
		return ErrBlockNotFound
	}

	var blockCount int
	// from to latest
	header := s.blockchain.Header()
	if header == nil {
		s.logger.Error("无法获取链头", "peer", peerInfo)
		return ErrBlockNotFound
	}
	for i := req.From; i <= header.Number; i++ {
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
	if s.blockchain != nil {
		if header := s.blockchain.Header(); header != nil {
			number = header.Number
		}
	}

	return &proto.SyncPeerStatus{
		Number: number,
	}, nil
}

// GetBlockByHash 按 hash 返回单个区块（服务端实现）
func (s *syncPeerService) GetBlockByHash(
	ctx context.Context,
	req *proto.GetBlockByHashRequest,
) (*proto.Block, error) {
	// 确保 logger 不为 nil
	if s.logger == nil {
		s.logger = hclog.NewNullLogger()
	}

	// 检查 blockchain 是否为 nil
	if s.blockchain == nil {
		s.logger.Error("blockchain 未初始化")
		return nil, ErrBlockNotFound
	}

	hash := types.BytesToHash(req.Hash)

	// 从本地数据库获取区块
	block, ok := s.blockchain.GetBlockByHash(hash, true)
	if !ok {
		s.logger.Error("区块未找到", "hash", hash.String())
		return nil, ErrBlockNotFound
	}

	resp := toProtoBlock(block)
	metrics.SetGauge([]string{syncerMetrics, "egress_bytes"}, float32(len(resp.Block)))

	s.logger.Debug("✅ 按hash返回区块成功",
		"hash", hash.String(),
		"blockNumber", block.Number())

	return resp, nil
}

// toProtoBlock converts type.Block -> proto.Block
func toProtoBlock(block *types.Block) *proto.Block {
	return &proto.Block{
		Block: block.MarshalRLP(),
	}
}
