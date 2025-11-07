package dpos

import (
	"sync"
)

// instanceManager DPoS实例管理器实现
type instanceManager struct {
	instances map[string]*DPoS
	mutex     sync.RWMutex
}

// NewInstanceManager 创建新的实例管理器
func NewInstanceManager() InstanceManager {
	return &instanceManager{
		instances: make(map[string]*DPoS),
	}
}

// Register 注册DPoS实例
func (im *instanceManager) Register(key string, dpos *DPoS) {
	im.mutex.Lock()
	defer im.mutex.Unlock()
	im.instances[key] = dpos
}

// Get 获取DPoS实例
func (im *instanceManager) Get(key string) (*DPoS, bool) {
	im.mutex.RLock()
	defer im.mutex.RUnlock()
	dpos, exists := im.instances[key]
	return dpos, exists
}

// GetAll 获取所有DPoS实例
func (im *instanceManager) GetAll() map[string]*DPoS {
	im.mutex.RLock()
	defer im.mutex.RUnlock()
	
	// 创建副本以避免外部修改
	result := make(map[string]*DPoS)
	for key, instance := range im.instances {
		result[key] = instance
	}
	return result
}

// Unregister 注销DPoS实例
func (im *instanceManager) Unregister(key string) {
	im.mutex.Lock()
	defer im.mutex.Unlock()
	delete(im.instances, key)
}



