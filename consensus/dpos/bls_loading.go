package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// initializeBLSLoadingState 初始化BLS加载状态
func (d *DPoS) initializeBLSLoadingState() {
	d.blsLoadingComplete = false
	d.blsLoadingWaitCh = make(chan struct{})
	d.logger.Debug("🔑 BLS加载状态已初始化")
}

// waitForBLSKeysLoaded 等待BLS公钥加载完成
func (d *DPoS) waitForBLSKeysLoaded() error {
	d.blsLoadingMutex.RLock()
	if d.blsLoadingComplete {
		d.blsLoadingMutex.RUnlock()
		return nil
	}
	d.blsLoadingMutex.RUnlock()

	d.logger.Info("⏳ 等待BLS公钥加载完成...")

	// 等待BLS加载完成信号
	select {
	case <-d.blsLoadingWaitCh:
		d.logger.Info("✅ BLS公钥加载完成，可以开始区块验证")
		return nil
	case <-time.After(5 * time.Minute): // 5分钟超时
		return fmt.Errorf("timeout waiting for BLS keys to load")
	}
}

// asyncLoadBLSKeys 异步加载BLS公钥（严格等待所有BLS加载完成）
func (d *DPoS) asyncLoadBLSKeys() {
	d.logger.Info("🔑 开始异步加载BLS公钥...")

	// 1. 首先尝试从数据库加载已缓存的BLS公钥
	if err := d.loadBLSKeysFromDatabase(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载BLS公钥失败", "error", err)
		// 数据库加载失败不阻止启动，继续尝试网络获取
	}

	validators := d.getAllValidators()

	for {
		allBLSLoaded := true
		failedValidators := []types.Address{}

		for _, validator := range validators {
			if validator.BlsKey == nil {
				allBLSLoaded = false

				// 尝试获取BLS公钥
				blsKey, err := d.getBLSKeyForValidator(validator.Address)
				if err != nil {
					d.logger.Warn("BLS公钥获取失败", "address", validator.Address.String(), "error", err)
					failedValidators = append(failedValidators, validator.Address)
					continue
				}

				// 保存BLS公钥
				validator.BlsKey = blsKey
				d.saveBLSKeyToCache(validator.Address, blsKey)
				d.saveBLSKeyToDatabase(validator.Address, blsKey)

				d.logger.Debug("✅ BLS公钥获取成功", "address", validator.Address.String())
			}
		}

		// 如果所有BLS都加载完成，发送完成信号
		if allBLSLoaded {
			d.logger.Info("✅ 所有BLS公钥加载完成，可以开始区块验证")

			// 设置完成状态并发送信号
			d.blsLoadingMutex.Lock()
			d.blsLoadingComplete = true
			d.blsLoadingMutex.Unlock()

			// 发送完成信号（非阻塞）
			select {
			case d.blsLoadingWaitCh <- struct{}{}:
			default:
			}

			break
		}

		// 如果还有失败的，等待后重试
		if len(failedValidators) > 0 {
			d.logger.Warn("部分BLS公钥获取失败，等待重试", "failedCount", len(failedValidators))
			time.Sleep(10 * time.Second) // 等待10秒后重试
		}
	}
}

// getAllValidators 获取所有验证者
func (d *DPoS) getAllValidators() validator.AccountSet {
	if d.runtime != nil && len(d.runtime.delegates) > 0 {
		d.logger.Debug("🔍 getAllValidators: 使用runtime.delegates", "count", len(d.runtime.delegates))
		// 添加详细日志：打印每个验证者的VotingPower
		for i, validator := range d.runtime.delegates {
			d.logger.Debug("🔍 runtime.delegates验证者信息",
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil)
		}
		return d.runtime.delegates
	}
	d.logger.Debug("🔍 getAllValidators: 使用d.delegates", "count", len(d.delegates))
	// 添加详细日志：打印每个验证者的VotingPower
	for i, validator := range d.delegates {
		d.logger.Info("🔍 d.delegates验证者信息",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}
	return d.delegates
}

// saveBLSKeyToCache 保存BLS公钥到缓存
func (d *DPoS) saveBLSKeyToCache(address types.Address, blsKey *bls.PublicKey) {
	if d.runtime != nil && d.runtime.networkIntegration != nil {
		blsKeyBytes := blsKey.Marshal()
		if err := d.runtime.networkIntegration.SaveBLSKey(address, blsKeyBytes); err != nil {
			d.logger.Warn("⚠️ 保存BLS公钥到缓存失败",
				"address", address.String(),
				"error", err)
		} else {
			d.logger.Debug("✅ BLS公钥已保存到缓存", "address", address.String())
		}
	}
}

// syncLoadBLSKeys 同步加载BLS公钥
func (d *DPoS) syncLoadBLSKeys() error {
	d.logger.Debug("开始同步加载BLS公钥")

	// 1. 从数据库加载已缓存的BLS公钥
	if err := d.loadBLSKeysFromDatabase(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载BLS公钥失败", "error", err)
		// 数据库加载失败不阻止启动，继续尝试网络获取
	} else {
		d.logger.Debug("数据库中的BLS公钥已加载（若存在）")
	}

	// 2. 获取所有验证者
	validators := d.getAllValidators()
	d.logger.Debug("🔍 获取到验证者数量", "count", len(validators))
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需获取BLS公钥")
		return nil
	}

	// 3. 同步获取缺失的BLS公钥（带重试机制）
	maxRetries := 1
	missingValidators := make([]types.Address, 0)

	for _, validator := range validators {
		if validator.BlsKey == nil {
			d.logger.Debug("🔑 开始获取BLS公钥", "address", validator.Address.String())

			// 非本地验证者时检查网络连通性，避免盲目请求
			isLocalValidator := d.key != nil && validator.Address == types.Address(d.key.Address())
			if !isLocalValidator && d.runtime != nil && d.runtime.networkIntegration != nil {
				if peerID, has, connected := d.runtime.networkIntegration.GetValidatorConnectivity(validator.Address); has {
					if !connected {
						d.logger.Debug("跳过BLS请求（验证者未连接）",
							"address", validator.Address.String(),
							"peerID", peerID.String())
						missingValidators = append(missingValidators, validator.Address)
						continue
					}
				} else {
					d.logger.Debug("跳过BLS请求（未知Peer映射）", "address", validator.Address.String())
					missingValidators = append(missingValidators, validator.Address)
					continue
				}
			}

			var blsKey *bls.PublicKey
			var err error

			// 重试机制
			for retry := 0; retry < maxRetries; retry++ {
				blsKey, err = d.getBLSKeyForValidator(validator.Address)
				if err == nil {
					break // 成功获取，跳出重试循环
				}

				if retry < maxRetries-1 {
					d.logger.Warn("⚠️ BLS公钥获取失败，准备重试",
						"address", validator.Address.String(),
						"retry", retry+1,
						"maxRetries", maxRetries,
						"error", err)
				} else {
					d.logger.Error("❌ BLS公钥获取失败，已达到最大重试次数",
						"address", validator.Address.String(),
						"maxRetries", maxRetries,
						"error", err)
				}
			}

			if err != nil {
				d.logger.Warn("⚠️ 启动阶段无法获取BLS公钥，将在后续按需加载",
					"address", validator.Address.String(),
					"maxRetries", maxRetries,
					"error", err)
				missingValidators = append(missingValidators, validator.Address)
				continue
			}

			// 保存BLS公钥
			validator.BlsKey = blsKey
			d.saveBLSKeyToCache(validator.Address, blsKey)
			d.saveBLSKeyToDatabase(validator.Address, blsKey)

			d.logger.Debug("BLS公钥获取成功", "address", validator.Address.String())
		} else {
			d.logger.Debug("✅ BLS公钥已存在", "address", validator.Address.String())
		}
	}

	// 修复：检查所有验证者是否都有BLS公钥
	allBLSLoaded := true
	for _, validator := range validators {
		if validator.BlsKey == nil {
			allBLSLoaded = false
			d.logger.Debug("验证者BLS公钥未加载", "address", validator.Address.String())
		}
	}

	if len(missingValidators) > 0 {
		d.logger.Warn("部分验证者BLS公钥启动时缺失，将在后续按需获取",
			"missingCount", len(missingValidators),
			"missingValidators", func() []string {
				addrs := make([]string, 0, len(missingValidators))
				for _, addr := range missingValidators {
					addrs = append(addrs, addr.String())
				}
				return addrs
			}())
	}

	if !allBLSLoaded {
		d.logger.Warn("部分验证者BLS公钥未加载完成，继续启动")
	} else {
		d.logger.Debug("所有验证者BLS公钥已加载")
	}

	d.blsLoadingMutex.Lock()
	d.blsLoadingComplete = true
	d.blsLoadingMutex.Unlock()

	// 发送完成信号
	select {
	case d.blsLoadingWaitCh <- struct{}{}:
		d.logger.Debug("✅ BLS加载完成信号已发送")
	default:
		d.logger.Debug("ℹ️ BLS加载完成信号通道已满")
	}

	d.logger.Debug("检查数据库验证者状态…")
	if d.state != nil && d.state.StakeStore != nil {
		dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		d.logger.Debug("GetValidatorsWithFilter(false)", "count", len(dbValidators), "error", err)

		if err == nil && len(dbValidators) > 0 {
			for i, validator := range dbValidators {
				faultInfo := d.getValidatorFaultInfo(validator.Address)
				d.logger.Debug("数据库验证者",
					"index", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"hasBlsKey", validator.BlsKey != nil,
					"faultFlag", faultInfo)
			}
			d.logger.Debug("数据库已有验证者，跳过保存", "count", len(dbValidators))
		} else {
			d.logger.Debug("数据库无验证者或获取失败，准备保存", "count", len(dbValidators), "error", err)
			// 数据库没有验证者，保存验证者信息到数据库
			if err := d.saveValidatorsWithBLSKeysToDatabase(); err != nil {
				d.logger.Warn("⚠️ 保存验证者信息到数据库失败", "error", err)
				// 不返回错误，继续启动流程
			}
		}
	} else {
		d.logger.Warn("⚠️ 状态存储不可用，无法检查数据库状态")
	}

	return nil
}

// preloadBLSKeysOnStartup 启动时预加载BLS公钥
func (d *DPoS) preloadBLSKeysOnStartup() error {
	d.logger.Info("🔑 开始启动时预加载BLS公钥（严格模式）...")

	// 1. 从数据库加载已缓存的BLS公钥
	if err := d.loadBLSKeysFromDatabase(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载BLS公钥失败", "error", err)
		// 数据库加载失败不阻止启动，继续尝试网络获取
	}

	// 2. 严格模式网络获取缺失的BLS公钥（必须成功）
	if err := d.fetchMissingBLSKeysFromNetwork(); err != nil {
		d.logger.Error("❌ 严格模式网络获取BLS公钥失败，启动终止", "error", err)
		return fmt.Errorf("strict mode BLS key fetching failed: %w", err)
	}

	d.logger.Info("✅ BLS公钥预加载完成（严格模式）")
	return nil
}

// fetchMissingBLSKeysFromNetwork 网络获取缺失的BLS公钥（严格模式，确保不遗漏）
func (d *DPoS) fetchMissingBLSKeysFromNetwork() error {
	d.logger.Debug("🌐 开始网络获取缺失的BLS公钥（严格模式）...")

	// 从当前内存中的验证者集合获取验证者信息
	validators := d.getAllValidators()
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需获取BLS公钥")
		return nil
	}

	// 获取本地节点地址
	myAddress := types.Address(d.key.Address())
	d.logger.Debug("🏠 本地节点地址", "address", myAddress.String())

	// 检查哪些验证者缺失BLS公钥
	missingValidators := make([]types.Address, 0)
	for _, validator := range validators {
		hasBLSKey := false
		if d.runtime != nil && d.runtime.networkIntegration != nil {
			if _, exists := d.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
				hasBLSKey = true
			}
		}

		if !hasBLSKey {
			missingValidators = append(missingValidators, validator.Address)
			d.logger.Debug("🔍 发现缺失的BLS公钥",
				"address", validator.Address.String(),
				"totalMissing", len(missingValidators))
		}
	}

	if len(missingValidators) == 0 {
		d.logger.Info("✅ 所有验证者BLS公钥已存在，无需网络获取")
		return nil
	}

	d.logger.Debug("🌐 开始严格模式网络获取",
		"missingCount", len(missingValidators),
		"totalValidators", len(validators))

	// 严格模式：逐个获取，确保不遗漏
	fetchedCount := 0
	for i, address := range missingValidators {
		d.logger.Debug("🔍 开始获取BLS公钥",
			"address", address.String(),
			"progress", fmt.Sprintf("%d/%d", i+1, len(missingValidators)))

		// 检查是否是本地地址
		if address == myAddress {
			d.logger.Debug("🏠 检测到本地地址，从文件获取BLS公钥", "address", address.String())

			// 从本地文件获取BLS公钥
			if d.runtime != nil && d.runtime.networkIntegration != nil {
				if keyBytes, err := d.runtime.networkIntegration.findBLSKeyFromGenesisFile(address); err == nil && len(keyBytes) > 0 {
					// 保存到缓存
					if err := d.runtime.networkIntegration.SaveBLSKey(address, keyBytes); err != nil {
						d.logger.Error("❌ 保存本地BLS公钥到缓存失败",
							"address", address.String(),
							"error", err)
						return fmt.Errorf("failed to save local BLS key to cache for %s: %w", address.String(), err)
					} else {
						fetchedCount++
						d.logger.Debug("✅ 本地BLS公钥获取成功",
							"address", address.String(),
							"blsKeyLength", len(keyBytes))
					}
				} else {
					d.logger.Error("❌ 从本地文件获取BLS公钥失败",
						"address", address.String(),
						"error", err)
					return fmt.Errorf("failed to load local BLS key for %s: %w", address.String(), err)
				}
			} else {
				d.logger.Error("❌ 网络集成层不可用，无法获取本地BLS公钥", "address", address.String())
				return fmt.Errorf("network integration not available for local BLS key")
			}
		} else {
			// 非本地地址，通过网络获取
			d.logger.Debug("🌐 非本地地址，通过网络获取BLS公钥", "address", address.String())

			// 严格模式：必须成功获取，失败则立即返回错误
			retryCount := 0
			maxRetries := 10 // 设置最大重试次数，避免无限循环

			for retryCount < maxRetries {
				retryCount++

				// 发起网络请求
				if d.runtime != nil && d.runtime.networkIntegration != nil {
					if err := d.runtime.networkIntegration.RequestBLSKey(address, myAddress); err != nil {
						d.logger.Warn("⚠️ 网络请求BLS公钥失败",
							"address", address.String(),
							"retry", retryCount,
							"maxRetries", maxRetries,
							"error", err)

						if retryCount >= maxRetries {
							d.logger.Error("❌ 达到最大重试次数，BLS公钥获取失败",
								"address", address.String(),
								"retryCount", retryCount)
							return fmt.Errorf("failed to get BLS key for %s after %d retries: %w", address.String(), maxRetries, err)
						}

						// 等待后重试
						time.Sleep(2 * time.Second)
						continue
					}

					// 等待网络响应
					d.logger.Debug("⏳ 等待BLS公钥响应",
						"address", address.String(),
						"waitTime", "5秒",
						"retry", retryCount)
					time.Sleep(5 * time.Second)

					// 检查是否成功获取
					if blsKeyBytes, exists := d.runtime.networkIntegration.GetBLSKey(address); exists {
						fetchedCount++
						d.logger.Debug("✅ BLS公钥获取成功",
							"address", address.String(),
							"blsKeyLength", len(blsKeyBytes),
							"retryCount", retryCount)
						break
					} else {
						d.logger.Warn("⚠️ 网络请求后仍未获取到BLS公钥",
							"address", address.String(),
							"retry", retryCount,
							"maxRetries", maxRetries)

						if retryCount >= maxRetries {
							d.logger.Error("❌ 达到最大重试次数，BLS公钥获取失败",
								"address", address.String(),
								"retryCount", retryCount)
							return fmt.Errorf("failed to get BLS key for %s after %d retries: no response received", address.String(), maxRetries)
						}

						// 等待后重试
						time.Sleep(3 * time.Second)
					}
				} else {
					d.logger.Error("❌ 网络集成层不可用", "address", address.String())
					return fmt.Errorf("network integration not available")
				}
			}
		}
	}

	d.logger.Debug("🌐 严格模式BLS公钥获取完成",
		"fetchedCount", fetchedCount,
		"totalMissing", len(missingValidators),
		"successRate", fmt.Sprintf("%.1f%%", float64(fetchedCount)/float64(len(missingValidators))*100))

	// 最终验证：确保所有验证者都有BLS公钥
	d.logger.Info("🔍 开始最终验证，确保所有验证者都有BLS公钥...")
	finalMissingCount := 0
	missingAddresses := make([]string, 0)

	for _, validator := range validators {
		hasBLSKey := false
		if d.runtime != nil && d.runtime.networkIntegration != nil {
			if _, exists := d.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
				hasBLSKey = true
			}
		}

		if !hasBLSKey {
			finalMissingCount++
			missingAddresses = append(missingAddresses, validator.Address.String())
			d.logger.Error("❌ 最终验证发现缺失BLS公钥",
				"address", validator.Address.String())
		}
	}

	if finalMissingCount > 0 {
		d.logger.Error("❌ 严格模式获取失败，仍有验证者缺失BLS公钥",
			"missingCount", finalMissingCount,
			"missingAddresses", missingAddresses)
		return fmt.Errorf("strict mode failed: %d validators still missing BLS keys: %v", finalMissingCount, missingAddresses)
	}

	d.logger.Debug("✅ 严格模式BLS公钥获取完全成功，所有验证者BLS公钥已获取")
	return nil
}
