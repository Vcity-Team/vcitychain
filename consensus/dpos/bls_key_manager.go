package dpos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// BLSKeyManager 管理BLS公钥的缓存、持久化、网络请求
type BLSKeyManager struct {
	// 缓存
	cache     map[types.Address][]byte
	cacheTime map[types.Address]time.Time
	mutex     sync.RWMutex

	// 持久化回调
	persistCallback func(address types.Address, blsKeyBytes []byte) error
	lookupCallback  func(address types.Address) ([]byte, error)

	// 网络相关
	broadcastTopic *network.Topic
	requestTopic   *network.Topic
	responseTopic  *network.Topic
	ackTopic       *network.Topic

	// DPoS实例引用
	dposInstance interface{}

	logger hclog.Logger
}

// NewBLSKeyManager 创建BLS公钥管理器
func NewBLSKeyManager(
	broadcastTopic, requestTopic, responseTopic, ackTopic *network.Topic,
	logger hclog.Logger,
) *BLSKeyManager {
	return &BLSKeyManager{
		cache:          make(map[types.Address][]byte),
		cacheTime:      make(map[types.Address]time.Time),
		broadcastTopic: broadcastTopic,
		requestTopic:   requestTopic,
		responseTopic:  responseTopic,
		ackTopic:       ackTopic,
		logger:         logger,
	}
}

// SetPersistCallback 设置持久化回调
func (bkm *BLSKeyManager) SetPersistCallback(callback func(address types.Address, blsKeyBytes []byte) error) {
	bkm.persistCallback = callback
}

// SetLookupCallback 设置查找回调
func (bkm *BLSKeyManager) SetLookupCallback(callback func(address types.Address) ([]byte, error)) {
	bkm.lookupCallback = callback
}

// SetDPoSInstance 设置DPoS实例
func (bkm *BLSKeyManager) SetDPoSInstance(dposInstance interface{}) {
	bkm.dposInstance = dposInstance
}

// SetTopics 设置网络主题（用于延迟初始化）
func (bkm *BLSKeyManager) SetTopics(broadcastTopic, requestTopic, responseTopic, ackTopic *network.Topic) {
	if broadcastTopic != nil {
		bkm.broadcastTopic = broadcastTopic
	}
	if requestTopic != nil {
		bkm.requestTopic = requestTopic
	}
	if responseTopic != nil {
		bkm.responseTopic = responseTopic
	}
	if ackTopic != nil {
		bkm.ackTopic = ackTopic
	}
}

// GetBLSKey 从缓存获取BLS公钥
func (bkm *BLSKeyManager) GetBLSKey(address types.Address) ([]byte, bool) {
	bkm.mutex.RLock()
	defer bkm.mutex.RUnlock()
	blsKey, exists := bkm.cache[address]
	return blsKey, exists
}

// SaveBLSKey 保存BLS公钥到缓存和数据库
func (bkm *BLSKeyManager) SaveBLSKey(address types.Address, blsKeyBytes []byte) error {
	bkm.mutex.Lock()
	defer bkm.mutex.Unlock()

	// 检查是否已经存在相同的BLS公钥，避免重复保存
	if existingKey, exists := bkm.cache[address]; exists {
		if bytes.Equal(existingKey, blsKeyBytes) {
			return nil
		}
	}

	// 保存到内存缓存
	bkm.cache[address] = blsKeyBytes
	bkm.cacheTime[address] = time.Now()

	// 尝试持久化到数据库
	if err := bkm.persistBLSKeyToDatabase(address, blsKeyBytes); err != nil {
		bkm.logger.Debug("BLS公钥持久化到数据库失败，但缓存已保存",
			"address", address.String(),
			"error", err)
		// 不返回错误，因为缓存已经保存成功
	}

	return nil
}

// LoadBLSKeyToCache 只加载BLS公钥到缓存，不写入数据库
func (bkm *BLSKeyManager) LoadBLSKeyToCache(address types.Address, blsKeyBytes []byte) error {
	bkm.mutex.Lock()
	defer bkm.mutex.Unlock()

	// 检查是否已经存在相同的BLS公钥，避免重复保存
	if existingKey, exists := bkm.cache[address]; exists {
		if bytes.Equal(existingKey, blsKeyBytes) {
			return nil
		}
	}

	bkm.cache[address] = blsKeyBytes
	bkm.cacheTime[address] = time.Now()

	return nil
}

// BroadcastBLSKey 广播BLS公钥
func (bkm *BLSKeyManager) BroadcastBLSKey(address types.Address, blsKeyBytes []byte, nodeType string) error {
	if bkm.broadcastTopic == nil {
		return fmt.Errorf("BLS公钥广播主题不可用")
	}

	blsKeyMsg := &BLSKeyBroadcastMessage{
		Address:      address,
		BLSPublicKey: blsKeyBytes,
		Timestamp:    uint64(time.Now().Unix()),
		NodeType:     nodeType,
	}

	data, err := json.Marshal(blsKeyMsg)
	if err != nil {
		return fmt.Errorf("序列化BLS公钥广播消息失败: %w", err)
	}

	dposMsg := &DPOSMessage{
		Data: data,
	}

	if err := bkm.broadcastTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("发布BLS公钥广播消息失败: %w", err)
	}

	bkm.logger.Info("BLS公钥广播消息已发送",
		"address", address.String(),
		"nodeType", nodeType,
		"blsKeyLength", len(blsKeyBytes))

	return nil
}

// RequestBLSKey 请求BLS公钥（fire-and-forget；内部生成 RequestID）
func (bkm *BLSKeyManager) RequestBLSKey(requestedAddress types.Address, requester types.Address) error {
	return bkm.RequestBLSKeyWithID(requestedAddress, requester, "")
}

// RequestBLSKeyWithID 请求BLS公钥并携带关联 ID（空则自动生成）
func (bkm *BLSKeyManager) RequestBLSKeyWithID(requestedAddress types.Address, requester types.Address, requestID string) error {
	if bkm.requestTopic == nil {
		return fmt.Errorf("BLS公钥请求主题不可用")
	}

	now := time.Now()
	if requestID == "" {
		requestID = fmt.Sprintf("bls_request_%s_%d", requestedAddress.String(), now.UnixNano())
	}

	requestMsg := &BLSKeyRequestMessage{
		RequestID:        requestID,
		RequestedAddress: requestedAddress,
		Requester:        requester,
		Timestamp:        uint64(now.Unix()),
	}

	data, err := json.Marshal(requestMsg)
	if err != nil {
		return fmt.Errorf("序列化BLS公钥请求消息失败: %w", err)
	}

	dposMsg := &DPOSMessage{
		Data: data,
	}

	if err := bkm.requestTopic.Publish(dposMsg); err != nil {
		return fmt.Errorf("发布BLS公钥请求消息失败: %w", err)
	}

	bkm.logger.Debug("📨 已广播BLS公钥请求",
		"requestedAddress", requestedAddress.String(),
		"requester", requester.String(),
		"requestID", requestID)

	return nil
}

// persistBLSKeyToDatabase 将BLS公钥持久化到数据库
func (bkm *BLSKeyManager) persistBLSKeyToDatabase(address types.Address, blsKeyBytes []byte) error {
	// 优先使用DPoS实例直接调用
	if bkm.dposInstance != nil {
		if dpos, ok := bkm.dposInstance.(*DPoS); ok {
			if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
				return fmt.Errorf("通过DPoS实例持久化BLS公钥失败: %w", err)
			}
			return nil
		}
	}

	// 通过全局注册表查找DPoS实例
	if dpos, exists := GetDPoSInstance("vcity_dpos"); exists && dpos != nil {
		if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
			return fmt.Errorf("通过全局注册表持久化BLS公钥失败: %w", err)
		}
		return nil
	}

	// 使用回调函数进行持久化
	if bkm.persistCallback != nil {
		if err := bkm.persistCallback(address, blsKeyBytes); err != nil {
			return fmt.Errorf("BLS公钥持久化回调失败: %w", err)
		}
		return nil
	}

	// 如果都没有设置，记录警告但不返回错误
	bkm.logger.Warn("BLS公钥持久化DPoS实例和回调函数都未设置，跳过数据库持久化",
		"address", address.String(),
		"blsKeyLength", len(blsKeyBytes))

	return nil
}

// cleanupExpiredBLSKeys 清理过期的BLS公钥缓存
func (bkm *BLSKeyManager) cleanupExpiredBLSKeys() {
	bkm.mutex.Lock()
	defer bkm.mutex.Unlock()

	expiredKeys := make([]types.Address, 0)
	expirationTime := time.Now().Add(-24 * time.Hour) // 24小时过期

	for address, cacheTime := range bkm.cacheTime {
		if cacheTime.Before(expirationTime) {
			expiredKeys = append(expiredKeys, address)
		}
	}

	for _, address := range expiredKeys {
		delete(bkm.cache, address)
		delete(bkm.cacheTime, address)
	}

	if len(expiredKeys) > 0 {
		bkm.logger.Debug("cleaned up expired BLS key cache", "count", len(expiredKeys))
	}
}

// Clear 清空所有缓存
func (bkm *BLSKeyManager) Clear() {
	bkm.mutex.Lock()
	defer bkm.mutex.Unlock()
	bkm.cache = make(map[types.Address][]byte)
	bkm.cacheTime = make(map[types.Address]time.Time)
}
