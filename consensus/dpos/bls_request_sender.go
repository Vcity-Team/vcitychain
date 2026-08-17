package dpos

import (
	"context"
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ConnectivityInfo 连接信息
type ConnectivityInfo struct {
	HasPeerMapping  bool
	IsPeerConnected bool
	PeerID          peer.ID
}

// BLSRequestSender 发送BLS请求
type BLSRequestSender struct {
	networkIntegration *NetworkIntegration
	blsKeyManager      *BLSKeyManager
	logger             hclog.Logger
}

// NewBLSRequestSender 创建请求发送器
func NewBLSRequestSender(
	networkIntegration *NetworkIntegration,
	blsKeyManager *BLSKeyManager,
	logger hclog.Logger,
) *BLSRequestSender {
	return &BLSRequestSender{
		networkIntegration: networkIntegration,
		blsKeyManager:      blsKeyManager,
		logger:             logger.Named("bls-request-sender"),
	}
}

// CheckConnectivity 检查连接状态
func (brs *BLSRequestSender) CheckConnectivity(address types.Address) ConnectivityInfo {
	if brs.networkIntegration == nil {
		return ConnectivityInfo{HasPeerMapping: false}
	}

	peerID, hasMapping, isConnected := brs.networkIntegration.GetValidatorConnectivity(address)
	return ConnectivityInfo{
		HasPeerMapping:  hasMapping,
		IsPeerConnected: isConnected,
		PeerID:          peerID,
	}
}

// SendRequest 发送请求（保留 request.RequestID 以便响应匹配）
func (brs *BLSRequestSender) SendRequest(
	request *BLSPublicKeyRequest,
) error {
	if brs.blsKeyManager != nil {
		return brs.blsKeyManager.RequestBLSKeyWithID(
			request.TargetAddress,
			request.RequesterAddress,
			request.RequestID,
		)
	}

	if brs.networkIntegration != nil {
		return brs.networkIntegration.RequestBLSKeyWithID(
			request.TargetAddress,
			request.RequesterAddress,
			request.RequestID,
		)
	}

	return fmt.Errorf("both BLSKeyManager and NetworkIntegration are unavailable")
}

// WaitForResponse 等待响应
func (brs *BLSRequestSender) WaitForResponse(
	ctx context.Context,
	handler *ResponseHandler,
	timeout time.Duration,
) (*bls.PublicKey, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case blsKey := <-handler.ResponseCh:
		return blsKey, nil
	case err := <-handler.ErrorCh:
		return nil, fmt.Errorf("BLS request failed: %w", err)
	case <-timeoutCtx.Done():
		return nil, fmt.Errorf("BLS request timeout")
	}
}
