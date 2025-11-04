package dpos

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/types"
)

// 🆕 新增：BLS响应处理器管理
var (
	blsResponseHandlers = make(map[string]chan *bls.PublicKey)
	blsErrorHandlers    = make(map[string]chan error)
	blsHandlerMutex     sync.RWMutex
)

// requestBLSPublicKeyFromNetwork 从网络请求BLS公钥
func (d *DPoS) requestBLSPublicKeyFromNetwork(address types.Address) (*bls.PublicKey, error) {
	// 1. 检查网络是否可用
	if d.config.Network == nil {
		return nil, fmt.Errorf("network not available")
	}

	// 2. 创建BLS公钥请求消息
	requestMsg := &BLSPublicKeyRequest{
		RequesterAddress: types.Address(d.key.Address()),
		TargetAddress:    address,
		Timestamp:        uint64(time.Now().Unix()),
	}

	// 3. 序列化请求消息
	requestData, err := json.Marshal(requestMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal BLS request: %w", err)
	}

	// 4. 发送网络广播请求
	// 创建响应通道
	responseCh := make(chan *bls.PublicKey, 1)
	errorCh := make(chan error, 1)

	// 注册响应处理器
	requestID := fmt.Sprintf("bls_request_%s_%d", address.String(), requestMsg.Timestamp)
	d.registerBLSResponseHandler(requestID, responseCh, errorCh)

	// 发送广播请求
	if err := d.broadcastBLSRequest(requestData, requestID); err != nil {
		d.unregisterBLSResponseHandler(requestID)
		return nil, fmt.Errorf("failed to broadcast BLS request: %w", err)
	}

	// 等待响应（设置超时，增加到30秒）
	timeout := time.After(30 * time.Second)
	select {
	case blsKey := <-responseCh:
		d.unregisterBLSResponseHandler(requestID)
		//d.logger.Info("✅ 收到BLS公钥响应", "address", address.String())
		return blsKey, nil
	case err := <-errorCh:
		d.unregisterBLSResponseHandler(requestID)
		return nil, fmt.Errorf("BLS request failed: %w", err)
	case <-timeout:
		d.unregisterBLSResponseHandler(requestID)
		return nil, fmt.Errorf("BLS request timeout for address %s", address.String())
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
	// 解析请求数据
	var request BLSPublicKeyRequest
	if err := json.Unmarshal(requestData, &request); err != nil {
		return fmt.Errorf("failed to unmarshal BLS request: %w", err)
	}

	// 通过P2P网络广播BLS公钥请求
	// 静默处理，不打印日志

	// 使用网络集成层进行真实的P2P广播
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

// handleBLSResponse 处理BLS响应
func (d *DPoS) handleBLSResponse(requestID string, blsKey *bls.PublicKey) error {
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




