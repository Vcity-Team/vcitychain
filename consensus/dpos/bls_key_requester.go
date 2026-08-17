package dpos

import (
	"context"
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// BLSKeyRequester 负责从网络请求BLS公钥
type BLSKeyRequester struct {
	dposInstance       *DPoS
	networkIntegration *NetworkIntegration
	blsKeyManager      *BLSKeyManager
	logger             hclog.Logger

	// 响应处理器管理
	responseManager *BLSResponseManager

	// 请求构建器
	requestBuilder *BLSRequestBuilder

	// 请求发送器
	requestSender *BLSRequestSender

	// 配置
	requestTimeout time.Duration
}

// NewBLSKeyRequester 创建BLS公钥请求器
func NewBLSKeyRequester(
	dposInstance *DPoS,
	networkIntegration *NetworkIntegration,
	blsKeyManager *BLSKeyManager,
	logger hclog.Logger,
) *BLSKeyRequester {
	return &BLSKeyRequester{
		dposInstance:       dposInstance,
		networkIntegration: networkIntegration,
		blsKeyManager:      blsKeyManager,
		logger:             logger.Named("bls-key-requester"),
		responseManager:    NewBLSResponseManager(logger),
		requestBuilder:     NewBLSRequestBuilder(dposInstance),
		requestSender:      NewBLSRequestSender(networkIntegration, blsKeyManager, logger),
		requestTimeout:     30 * time.Second,
	}
}

// RequestBLSKey 请求BLS公钥（主入口）
func (bkr *BLSKeyRequester) RequestBLSKey(
	ctx context.Context,
	address types.Address,
) (*bls.PublicKey, error) {
	// 1. 验证网络可用性
	if err := bkr.validateNetwork(); err != nil {
		return nil, err
	}

	// 2. 检查peer连接状态
	connectivity := bkr.requestSender.CheckConnectivity(address)
	if connectivity.HasPeerMapping && !connectivity.IsPeerConnected {
		bkr.logger.Warn("⏭️ 跳过BLS请求：目标节点离线",
			"target", address.String(),
			"peerID", connectivity.PeerID.String(),
			"note", "已知peer但当前未连接，直接返回")
		return nil, fmt.Errorf("validator %s offline (peer %s not connected)", address.String(), connectivity.PeerID.String())
	}

	// 3. 构建请求
	request, requestID := bkr.requestBuilder.BuildRequest(address)

	// 4. 注册响应处理器
	handler, err := bkr.responseManager.RegisterHandler(requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to register response handler: %w", err)
	}

	// 确保在函数退出时注销处理器
	defer bkr.responseManager.UnregisterHandler(requestID)

	// 5. 发送请求
	if err := bkr.requestSender.SendRequest(request); err != nil {
		return nil, fmt.Errorf("failed to send BLS request: %w", err)
	}

	// 6. 记录日志
	if connectivity.HasPeerMapping {
		bkr.logger.Info("🎯 BLS请求命中在线节点",
			"target", address.String(),
			"peerID", connectivity.PeerID.String(),
			"note", "直接尝试获取BLS公钥")
	} else {
		bkr.logger.Info("📡 BLS请求使用广播路径（peer映射未知）",
			"target", address.String(),
			"note", "未注册peer映射，使用广播方式请求，响应时将自动注册peer映射")
	}

	// 7. 等待响应
	return bkr.requestSender.WaitForResponse(ctx, handler, bkr.requestTimeout)
}

// validateNetwork 验证网络可用性
func (bkr *BLSKeyRequester) validateNetwork() error {
	if bkr.dposInstance == nil || bkr.dposInstance.config == nil || bkr.dposInstance.config.Network == nil {
		return fmt.Errorf("network not available")
	}
	return nil
}

// HandleBLSResponse 处理BLS响应（供外部调用）
func (bkr *BLSKeyRequester) HandleBLSResponse(requestID string, blsKey *bls.PublicKey) error {
	return bkr.responseManager.HandleResponse(requestID, blsKey)
}

// HandleBLSError 处理BLS错误（供外部调用）
func (bkr *BLSKeyRequester) HandleBLSError(requestID string, err error) error {
	return bkr.responseManager.HandleError(requestID, err)
}
