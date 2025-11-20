package dpos

import (
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/hashicorp/go-hclog"
)

// BLSResponseManager 管理BLS响应处理器
type BLSResponseManager struct {
	handlers map[string]*ResponseHandler
	mutex    sync.RWMutex
	logger   hclog.Logger
}

// ResponseHandler 响应处理器
type ResponseHandler struct {
	ResponseCh chan *bls.PublicKey
	ErrorCh    chan error
	CreatedAt  time.Time
}

// NewBLSResponseManager 创建BLS响应管理器
func NewBLSResponseManager(logger hclog.Logger) *BLSResponseManager {
	return &BLSResponseManager{
		handlers: make(map[string]*ResponseHandler),
		logger:   logger.Named("bls-response-manager"),
	}
}

// RegisterHandler 注册响应处理器
func (brm *BLSResponseManager) RegisterHandler(requestID string) (*ResponseHandler, error) {
	brm.mutex.Lock()
	defer brm.mutex.Unlock()

	if _, exists := brm.handlers[requestID]; exists {
		return nil, fmt.Errorf("handler already exists for requestID: %s", requestID)
	}

	handler := &ResponseHandler{
		ResponseCh: make(chan *bls.PublicKey, 1),
		ErrorCh:    make(chan error, 1),
		CreatedAt:  time.Now(),
	}

	brm.handlers[requestID] = handler
	return handler, nil
}

// UnregisterHandler 注销响应处理器
func (brm *BLSResponseManager) UnregisterHandler(requestID string) {
	brm.mutex.Lock()
	defer brm.mutex.Unlock()

	if handler, exists := brm.handlers[requestID]; exists {
		// 使用recover防止关闭已关闭的channel导致panic
		defer func() {
			if r := recover(); r != nil {
				brm.logger.Debug("unregisterHandler recovered from panic", "requestID", requestID, "error", r)
			}
		}()

		close(handler.ResponseCh)
		close(handler.ErrorCh)
		delete(brm.handlers, requestID)
	}
}

// HandleResponse 处理响应
func (brm *BLSResponseManager) HandleResponse(requestID string, blsKey *bls.PublicKey) error {
	brm.mutex.RLock()
	handler, exists := brm.handlers[requestID]
	brm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("handler not found for requestID: %s", requestID)
	}

	// 使用recover防止panic
	defer func() {
		if r := recover(); r != nil {
			brm.logger.Debug("handleResponse recovered from panic", "requestID", requestID, "error", r)
		}
	}()

	select {
	case handler.ResponseCh <- blsKey:
		return nil
	default:
		return fmt.Errorf("response channel is full")
	}
}

// HandleError 处理错误
func (brm *BLSResponseManager) HandleError(requestID string, err error) error {
	brm.mutex.RLock()
	handler, exists := brm.handlers[requestID]
	brm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("handler not found for requestID: %s", requestID)
	}

	// 使用recover防止panic
	defer func() {
		if r := recover(); r != nil {
			brm.logger.Debug("handleError recovered from panic", "requestID", requestID, "error", r)
		}
	}()

	select {
	case handler.ErrorCh <- err:
		return nil
	default:
		return fmt.Errorf("error channel is full")
	}
}

// GetHandler 获取响应处理器（用于测试）
func (brm *BLSResponseManager) GetHandler(requestID string) (*ResponseHandler, bool) {
	brm.mutex.RLock()
	defer brm.mutex.RUnlock()

	handler, exists := brm.handlers[requestID]
	return handler, exists
}
