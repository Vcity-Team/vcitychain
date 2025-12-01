package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
)

// getValidatorsForEpoch 获取指定epoch的验证者集合
func (d *DPoS) getValidatorsForEpoch(epochNumber uint64) (validator.AccountSet, error) {
	// 先检查缓存
	if cached := d.getCachedEpochValidators(epochNumber); cached != nil {
		d.logger.Debug("✅ 从缓存获取epoch验证者集合",
			"epochNumber", epochNumber,
			"validatorsCount", len(cached))
		return cached.Copy(), nil
	}

	var validators validator.AccountSet
	var lastErr error

	// 优先从数据库按epoch号获取（最快）
	if store, err := d.getStateStore(); err == nil {
		if epochValidators, err := store.GetEpochValidatorsByEpoch(epochNumber); err == nil && len(epochValidators) > 0 {
			validators = epochValidators
			d.logger.Debug("✅ 从数据库（按epoch号）获取epoch验证者集合",
				"epochNumber", epochNumber,
				"validatorsCount", len(validators))
			// 缓存结果
			d.setCachedEpochValidators(epochNumber, validators)
			return validators, nil
		} else if err != nil {
			lastErr = fmt.Errorf("database: %w", err)
		} else {
			lastErr = fmt.Errorf("database: no validators found for epoch %d", epochNumber)
		}
	} else {
		lastErr = fmt.Errorf("failed to get state store: %w", err)
	}

	// 计算该epoch的开始区块号--主要是兼容老逻辑，数据库没取到，后续可以移除
	blocksPerEpoch := d.getEpochSize()
	consensusSwitchHeight := d.config.ConsensusSwitchHeight
	epochStartBlock := consensusSwitchHeight
	if epochNumber > 1 {
		epochStartBlock = consensusSwitchHeight + (epochNumber-1)*blocksPerEpoch
	}

	// 从该epoch开始区块的ExtraData获取验证者集合
	if d.blockchain == nil {
		lastErr = fmt.Errorf("blockchain not available")
	} else if header, exists := d.blockchain.GetHeaderByNumber(epochStartBlock); exists {
		extra := &Extra{}
		if err := extra.UnmarshalRLP(header.ExtraData); err == nil {
			// 从ExtraData获取验证者集合
			if extra.Validators != nil && !extra.Validators.IsEmpty() && len(extra.Validators.Added) > 0 {
				validators = make(validator.AccountSet, 0, len(extra.Validators.Added))
				for _, v := range extra.Validators.Added {
					validators = append(validators, &validator.ValidatorMetadata{
						Address:     v.Address,
						VotingPower: v.VotingPower,
						IsActive:    v.IsActive,
					})
				}
				d.logger.Debug("✅ 从ExtraData获取epoch验证者集合",
					"epochNumber", epochNumber,
					"epochStartBlock", epochStartBlock,
					"validatorsCount", len(validators))
				// 异步保存到数据库（不阻塞）
				go d.saveEpochValidatorsToDatabase(epochNumber, validators)
				// 缓存结果
				d.setCachedEpochValidators(epochNumber, validators)
				return validators, nil
			} else {
				lastErr = fmt.Errorf("ExtraData has no validators for epoch %d", epochNumber)
			}
		} else {
			lastErr = fmt.Errorf("failed to unmarshal ExtraData for epoch %d: %w", epochNumber, err)
		}
	} else {
		lastErr = fmt.Errorf("block %d (epoch %d start) not found", epochStartBlock, epochNumber)
	}

	return nil, fmt.Errorf("cannot get validators for epoch %d: %w", epochNumber, lastErr)
}

// getCachedEpochValidators 从缓存获取epoch验证者集合
func (d *DPoS) getCachedEpochValidators(epochNumber uint64) validator.AccountSet {
	if d.cache == nil {
		return nil
	}
	d.cache.lock.RLock()
	defer d.cache.lock.RUnlock()

	// 检查缓存是否存在且未过期
	if validators, exists := d.cache.epochValidatorsCache[epochNumber]; exists {
		if cacheTime, timeExists := d.cache.epochCacheTime[epochNumber]; timeExists {
			ttl := d.cache.epochCacheTTL
			if ttl == 0 {
				ttl = 5 * time.Minute // 默认5分钟
			}
			if time.Since(cacheTime) < ttl {
				return validators
			}
			// 缓存过期，删除
			delete(d.cache.epochValidatorsCache, epochNumber)
			delete(d.cache.epochCacheTime, epochNumber)
		}
	}

	return nil
}

// setCachedEpochValidators 设置epoch验证者集合到缓存
func (d *DPoS) setCachedEpochValidators(epochNumber uint64, validators validator.AccountSet) {
	if d.cache == nil {
		return
	}

	d.cache.lock.Lock()
	defer d.cache.lock.Unlock()

	// 初始化缓存map（如果未初始化）
	if d.cache.epochValidatorsCache == nil {
		d.cache.epochValidatorsCache = make(map[uint64]validator.AccountSet)
		d.cache.epochCacheTime = make(map[uint64]time.Time)
		d.cache.epochCacheTTL = 5 * time.Minute // 默认5分钟
	}

	// 限制缓存大小（保留最近的100个epoch）
	maxCacheSize := 100
	if len(d.cache.epochValidatorsCache) >= maxCacheSize {
		// 删除最旧的缓存（按epoch号）
		oldestEpoch := uint64(0)
		for epoch := range d.cache.epochValidatorsCache {
			if oldestEpoch == 0 || epoch < oldestEpoch {
				oldestEpoch = epoch
			}
		}
		if oldestEpoch > 0 {
			delete(d.cache.epochValidatorsCache, oldestEpoch)
			delete(d.cache.epochCacheTime, oldestEpoch)
		}
	}

	d.cache.epochValidatorsCache[epochNumber] = validators.Copy()
	d.cache.epochCacheTime[epochNumber] = time.Now()
}

// saveEpochValidatorsToDatabase 异步保存epoch验证者集合到数据库
func (d *DPoS) saveEpochValidatorsToDatabase(epochNumber uint64, validators validator.AccountSet) {
	if len(validators) == 0 {
		return
	}

	store, err := d.getStateStore()
	if err != nil {
		d.logger.Debug("无法获取state store，跳过保存epoch验证者到数据库",
			"epochNumber", epochNumber,
			"error", err)
		return
	}

	if err := store.SaveEpochValidators(epochNumber, validators); err != nil {
		d.logger.Warn("异步保存epoch验证者到数据库失败",
			"epochNumber", epochNumber,
			"error", err)
	} else {
		d.logger.Debug("✅ 异步保存epoch验证者到数据库成功",
			"epochNumber", epochNumber,
			"validatorsCount", len(validators))
	}
}

// getEpochValidatorsFromDatabase 从数据库获取epoch验证者
func (d *DPoS) getEpochValidatorsFromDatabase() (validator.AccountSet, error) {
	store, err := d.getStateStore()
	if err != nil {
		return nil, err
	}

	validators, err := store.GetEpochValidators()
	if err != nil {
		// 🆕 使用日志频率限制，10秒一次
		d.logOnceWithInterval("get_epoch_validators_failed", 10*time.Second, "warn",
			"⚠️ 从数据库获取epoch验证者失败", "error", err)
		return nil, err
	}

	// 🆕 使用日志频率限制，10秒一次
	d.logOnceWithInterval("get_epoch_validators_success", 10*time.Second, "debug",
		"✅ 从数据库获取epoch验证者成功", "count", len(validators))
	return validators, nil
}
