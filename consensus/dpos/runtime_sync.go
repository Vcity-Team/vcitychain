package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
)

// syncDelegatesToRuntime 同步 delegates 到 runtime（带锁保护）
func (d *DPoS) syncDelegatesToRuntime(delegates validator.AccountSet) error {
	if d.runtime == nil {
		d.logger.Debug("runtime为空，无法同步delegates")
		return nil // runtime 未初始化，不报错
	}

	if d.runtime.lock.TryLock() {
		defer d.runtime.lock.Unlock()
		d.runtime.delegates = delegates.Copy()
		d.logger.Debug("✅ delegates已同步到runtime", "count", len(delegates))
		return nil
	}

	d.logger.Debug("⚠️ 无法获取runtime.lock，跳过同步delegates")
	return nil // 无法获取锁，不报错
}

// syncDelegatesToRuntimeWithRetry 同步 delegates 到 runtime（带重试机制）
func (d *DPoS) syncDelegatesToRuntimeWithRetry(delegates validator.AccountSet, maxRetries int, retryDelay time.Duration) error {
	if d.runtime == nil {
		d.logger.Debug("runtime为空，无法同步delegates")
		return nil
	}

	for i := 0; i < maxRetries; i++ {
		if d.runtime.lock.TryLock() {
			defer d.runtime.lock.Unlock()
			d.runtime.delegates = delegates.Copy()
			d.logger.Debug("✅ delegates已同步到runtime", "count", len(delegates), "attempt", i+1)
			return nil
		}

		if i < maxRetries-1 {
			time.Sleep(retryDelay)
			retryDelay *= 2 // 指数退避
		}
	}

	d.logger.Warn("⚠️ 无法获取runtime.lock，已达到最大重试次数", "maxRetries", maxRetries)
	return nil // 重试失败，不报错
}
