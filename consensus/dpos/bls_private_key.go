package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/secrets"
)

// blsKeyCacheTTL controls how long the BLS private key stays in memory
// before it is reloaded from the secrets manager.
const blsKeyCacheTTL = 30 * time.Minute

// getBLSPrivateKey 获取BLS私钥（从运行时缓存或密钥管理器）
func (r *dposRuntime) getBLSPrivateKey() (*bls.PrivateKey, error) {
	// 首先检查缓存
	r.blsPrivateKeyCacheMutex.RLock()
	if r.blsPrivateKeyCache != nil && !r.blsPrivateKeyCacheTime.IsZero() &&
		time.Since(r.blsPrivateKeyCacheTime) <= blsKeyCacheTTL {
		defer r.blsPrivateKeyCacheMutex.RUnlock()
		return r.blsPrivateKeyCache, nil
	}
	r.blsPrivateKeyCacheMutex.RUnlock()

	// 缓存未命中，需要从文件读取
	r.blsPrivateKeyCacheMutex.Lock()
	defer r.blsPrivateKeyCacheMutex.Unlock()

	// 双重检查，防止在获取写锁期间其他goroutine已经加载了缓存
	if r.blsPrivateKeyCache != nil && !r.blsPrivateKeyCacheTime.IsZero() &&
		time.Since(r.blsPrivateKeyCacheTime) <= blsKeyCacheTTL {
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
	r.blsPrivateKeyCacheTime = time.Now()

	return privateKey, nil
}



