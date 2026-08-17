package dpos

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/types"
)

// 保留旧的全局响应处理器（向后兼容，用于旧的handleBLSResponse）
var (
	blsResponseHandlers = make(map[string]chan *bls.PublicKey)
	blsErrorHandlers    = make(map[string]chan error)
	blsHandlerMutex     sync.RWMutex
)

// requestBLSPublicKeyFromNetwork 从网络请求BLS公钥（重构后）
func (d *DPoS) requestBLSPublicKeyFromNetwork(address types.Address) (*bls.PublicKey, error) {
	// 获取或创建BLSKeyRequester
	requester := d.getBLSKeyRequester()
	if requester == nil {
		return nil, fmt.Errorf("BLS key requester not available")
	}

	// 使用BLSKeyRequester请求BLS公钥
	ctx := context.Background()
	return requester.RequestBLSKey(ctx, address)
}

// getBLSKeyRequester 获取或创建BLSKeyRequester实例（进程内单例）
func (d *DPoS) getBLSKeyRequester() *BLSKeyRequester {
	if d.runtime == nil || d.runtime.networkIntegration == nil {
		return nil
	}

	d.blsKeyRequesterOnce.Do(func() {
		var blsKeyManager *BLSKeyManager
		if d.runtime.networkIntegration.blsKeyManager != nil {
			blsKeyManager = d.runtime.networkIntegration.blsKeyManager
		}
		d.blsKeyRequester = NewBLSKeyRequester(
			d,
			d.runtime.networkIntegration,
			blsKeyManager,
			d.logger,
		)
	})

	// runtime/network 可能在 Once 之后才补齐 blsKeyManager，延迟刷新引用
	if d.blsKeyRequester != nil && d.blsKeyRequester.blsKeyManager == nil &&
		d.runtime.networkIntegration.blsKeyManager != nil {
		d.blsKeyRequester.blsKeyManager = d.runtime.networkIntegration.blsKeyManager
		d.blsKeyRequester.requestSender.blsKeyManager = d.runtime.networkIntegration.blsKeyManager
	}

	return d.blsKeyRequester
}

// handleBLSError 将错误投递给等待中的 BLS 请求
func (d *DPoS) handleBLSError(requestID string, err error) error {
	requester := d.getBLSKeyRequester()
	if requester != nil {
		if herr := requester.HandleBLSError(requestID, err); herr == nil {
			return nil
		}
	}

	blsHandlerMutex.RLock()
	errorCh, exists := blsErrorHandlers[requestID]
	blsHandlerMutex.RUnlock()
	if !exists {
		return fmt.Errorf("BLS error handler not found for requestID: %s", requestID)
	}

	select {
	case errorCh <- err:
		return nil
	default:
		return fmt.Errorf("BLS error channel is full")
	}
}

// initializeBLSNetworking 初始化BLS网络通信
func (d *DPoS) initializeBLSNetworking() error {
	if d.config.Network == nil {
		return fmt.Errorf("network not available")
	}

	d.logger.Info("🌐 初始化BLS网络通信")

	// 由于BLS消息不是protobuf类型，我们使用简化的网络通信
	// 这里暂时跳过Topic创建，直接使用JSON序列化进行网络通信
	d.logger.Info("✅ BLS网络通信初始化完成（使用JSON序列化）")
	return nil
}

// broadcastBLSRequest 广播BLS请求
func (d *DPoS) broadcastBLSRequest(requestData []byte, requestID string) error {
	var request BLSPublicKeyRequest
	if err := json.Unmarshal(requestData, &request); err != nil {
		return fmt.Errorf("failed to unmarshal BLS request: %w", err)
	}

	if d.runtime != nil && d.runtime.networkIntegration != nil {
		// 通过网络集成层请求BLS公钥
		if err := d.runtime.networkIntegration.RequestBLSKey(request.TargetAddress, request.RequesterAddress); err != nil {
			d.logger.Error("❌ 请求BLS公钥失败", "error", err)
			return fmt.Errorf("failed to request BLS key: %w", err)
		}

		d.logger.Debug("✅ BLS公钥请求发送成功", "requestID", requestID)
	} else {
		d.logger.Warn("⚠️ 网络集成层不可用，无法请求BLS公钥")
		return fmt.Errorf("network integration not available")
	}

	return nil
}

// handleBLSResponse 处理BLS响应（重构后，优先使用BLSKeyRequester）
func (d *DPoS) handleBLSResponse(requestID string, blsKey *bls.PublicKey) error {
	// 优先使用BLSKeyRequester处理响应
	requester := d.getBLSKeyRequester()
	if requester != nil {
		if err := requester.HandleBLSResponse(requestID, blsKey); err == nil {
			return nil
		}
		// 如果BLSKeyRequester处理失败，继续使用旧的全局处理器（向后兼容）
	}

	// 备用方案：使用旧的全局处理器（向后兼容）
	blsHandlerMutex.RLock()
	responseCh, exists := blsResponseHandlers[requestID]
	blsHandlerMutex.RUnlock()

	if !exists {
		return fmt.Errorf("BLS response handler not found for requestID: %s", requestID)
	}

	// 发送响应，使用recover防止panic
	defer func() {
		if r := recover(); r != nil {
			d.logger.Debug("handleBLSResponse recovered from panic", "requestID", requestID, "error", r)
		}
	}()

	select {
	case responseCh <- blsKey:
		d.logger.Debug("✅ 发送真实BLS响应", "requestID", requestID)
	default:
		d.logger.Warn("⚠️ BLS响应通道已满", "requestID", requestID)
		return fmt.Errorf("BLS response channel is full")
	}

	return nil
}

// simulateBLSResponse 模拟BLS响应（用于测试）
func (d *DPoS) simulateBLSResponse(requestID string) {
	blsHandlerMutex.RLock()
	responseCh, exists := blsResponseHandlers[requestID]
	blsHandlerMutex.RUnlock()

	if !exists {
		return
	}

	// 生成模拟的BLS公钥
	blsKey, err := bls.GenerateBlsKey()
	if err != nil {
		d.logger.Error("❌ 生成模拟BLS公钥失败", "error", err)
		return
	}

	// 发送响应，使用recover防止panic
	defer func() {
		if r := recover(); r != nil {
			d.logger.Debug("simulateBLSResponse recovered from panic", "requestID", requestID, "error", r)
		}
	}()

	select {
	case responseCh <- blsKey.PublicKey():
		d.logger.Info("✅ 发送模拟BLS响应", "requestID", requestID)
	default:
		d.logger.Warn("⚠️ BLS响应通道已满", "requestID", requestID)
	}
}

// registerBLSResponseHandler 注册BLS响应处理器
func (d *DPoS) registerBLSResponseHandler(requestID string, responseCh chan *bls.PublicKey, errorCh chan error) {
	blsHandlerMutex.Lock()
	defer blsHandlerMutex.Unlock()

	blsResponseHandlers[requestID] = responseCh
	blsErrorHandlers[requestID] = errorCh

	// 静默处理，不打印日志
}

// unregisterBLSResponseHandler 注销BLS响应处理器
func (d *DPoS) unregisterBLSResponseHandler(requestID string) {
	blsHandlerMutex.Lock()
	defer blsHandlerMutex.Unlock()

	if responseCh, exists := blsResponseHandlers[requestID]; exists {
		close(responseCh)
		delete(blsResponseHandlers, requestID)
	}

	if errorCh, exists := blsErrorHandlers[requestID]; exists {
		close(errorCh)
		delete(blsErrorHandlers, requestID)
	}

	// 静默处理，不打印日志
}
