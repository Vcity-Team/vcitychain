package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// 启动时直接调用和命令一样的数据源方法
func (d *DPoS) callCommandDataSourcesOnStartup() error {
	// 🆕 数据源1: 从store获取验证者信息 (与命令中的 GetValidators() 一致)
	if d.state != nil && d.state.StakeStore != nil {
		validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		if err != nil {
			d.logger.Warn("⚠️ 获取验证者信息失败", "error", err)
		} else {

			// 🆕 获取质押信息用于对比
			stakingInfo, stakingErr := d.state.StakeStore.GetStakingInfo()
			if stakingErr != nil {
				d.logger.Warn("⚠️ 获取质押信息失败，无法显示票数对比", "error", stakingErr)
			}

			for _, validator := range validators {
				// 🆕 查找对应的票数信息
				var totalVotes *big.Int
				var voterCount int
				for _, stake := range stakingInfo {
					if stake.Delegate.String() == validator.Address.String() {
						if totalVotes == nil {
							totalVotes = big.NewInt(0)
						}
						totalVotes.Add(totalVotes, stake.Amount)
						voterCount++
					}
				}
				_ = totalVotes
				_ = voterCount
			}
		}
	} else {
		d.logger.Warn("⚠️ store不可用，无法获取验证者信息")
	}

	// 🆕 数据源2: 从store获取质押信息 (与命令中的 GetStakingInfo() 一致)
	if d.state != nil && d.state.StakeStore != nil {
		_, err := d.state.StakeStore.GetStakingInfo()
		if err != nil {
			d.logger.Warn("⚠️ 获取质押信息失败", "error", err)
		}
	}

	// 🆕 对比：显示内存中的受托人信息
	d.lock.RLock()
	defer d.lock.RUnlock()

	d.logger.Debug("🎯 从与命令相同的数据源读取完成")
	return nil
}

// 🆕 将验证者数据同步到数据库
func (d *DPoS) syncDelegatesToDatabase(delegates validator.AccountSet) error {
	d.logger.Info("💾 开始将验证者数据同步到数据库...")

	if len(delegates) == 0 {
		d.logger.Warn("⚠️ 验证者集合为空，无需同步到数据库")
		return nil
	}

	// 验证者信息将在BLS公钥获取完成后统一保存到数据库
	d.logger.Info("💾 验证者信息将在BLS公钥获取完成后统一保存到数据库", "count", len(delegates))
	return nil
}

// syncRuntimeDelegatesWithRetry 异步同步 dposRuntime 的 delegates 状态，使用重试机制
func (d *DPoS) syncRuntimeDelegatesWithRetry() {
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	d.logger.Info("🔄 开始异步同步 dposRuntime delegates 状态", "maxRetries", maxRetries)

	for i := 0; i < maxRetries; i++ {
		if d.runtime != nil {
			if d.runtime.lock.TryLock() {
				// 同步主结构体的 delegates 到 runtime
				d.runtime.delegates = d.delegates.Copy()
				d.runtime.lock.Unlock()
				d.logger.Info("✅ dposRuntime delegates 同步完成",
					"count", len(d.delegates),
					"attempt", i+1,
					"totalAttempts", maxRetries)
				return
			} else {
				d.logger.Info("⚠️ 无法获取 runtime.lock，准备重试",
					"attempt", i+1,
					"maxRetries", maxRetries,
					"retryDelay", retryDelay)
			}
		} else {
			d.logger.Warn("⚠️ dposRuntime 为空，无法同步 delegates")
			return
		}

		// 如果不是最后一次尝试，等待后重试
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
			retryDelay *= 2 // 指数退避
		}
	}

	d.logger.Warn("⚠️ dposRuntime delegates 同步失败，已达到最大重试次数",
		"maxRetries", maxRetries,
		"delegatesCount", len(d.delegates))
}

// syncDelegateFromDatabase 从数据库同步指定验证者到内存
func (d *DPoS) syncDelegateFromDatabase(delegate types.Address) error {
	store, err := d.getStateStore()
	if err != nil {
		return err
	}

	// 从数据库获取验证者信息
	validators, err := store.GetValidatorsWithFilter(false)
	if err != nil {
		return WrapError("get validators from database", err)
	}

	// 查找指定验证者
	for _, validator := range validators {
		if validator.Address == delegate {
			// 更新内存中的验证者信息
			found := false
			for i, del := range d.delegates {
				if del.Address == delegate {
					d.delegates[i] = validator
					found = true
					break
				}
			}

			// 如果内存中不存在，添加到内存
			if !found {
				d.delegates = append(d.delegates, validator)
			}

			// 同步到runtime
			if d.runtime != nil {
				found = false
				for i, del := range d.runtime.delegates {
					if del.Address == delegate {
						d.runtime.delegates[i] = validator
						found = true
						break
					}
				}
				if !found {
					d.runtime.delegates = append(d.runtime.delegates, validator)
				}
			}

			d.logger.Debug("✅ 验证者信息已从数据库同步到内存",
				"delegate", delegate.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive)
			return nil
		}
	}

	return fmt.Errorf("delegate not found in database: %s", delegate.String())
}
