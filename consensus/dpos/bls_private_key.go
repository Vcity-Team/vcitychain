package dpos

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/secrets"
)

// getBLSPrivateKey 获取BLS私钥（从运行时缓存或密钥管理器）
func (r *dposRuntime) getBLSPrivateKey() (*bls.PrivateKey, error) {
	// 首先检查缓存
	r.blsPrivateKeyCacheMutex.RLock()
	if r.blsPrivateKeyCache != nil {
		defer r.blsPrivateKeyCacheMutex.RUnlock()
		return r.blsPrivateKeyCache, nil
	}
	r.blsPrivateKeyCacheMutex.RUnlock()

	// 缓存未命中，需要从文件读取
	r.blsPrivateKeyCacheMutex.Lock()
	defer r.blsPrivateKeyCacheMutex.Unlock()

	// 双重检查，防止在获取写锁期间其他goroutine已经加载了缓存
	if r.blsPrivateKeyCache != nil {
		return r.blsPrivateKeyCache, nil
	}

	// 这里需要从密钥管理器获取BLS私钥
	secretsManager := r.backend.(*DPoS).config.SecretsManager
	if secretsManager == nil {
		return nil, fmt.Errorf("secrets manager not available")
	}

	// 获取BLS私钥
	blsKey, err := secretsManager.GetSecret(secrets.ValidatorBLSKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get BLS key from secrets manager: %w", err)
	}

	// 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey(blsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}

	// 缓存私钥
	r.blsPrivateKeyCache = privateKey

	return privateKey, nil
}




