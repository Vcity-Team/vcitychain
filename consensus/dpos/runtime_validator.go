package dpos

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/umbracle/fastrlp"
)

// parseValidatorsFromGenesis 从创世块解析DPoS验证者
func (r *dposRuntime) parseValidatorsFromGenesis() error {

	// 1. 获取创世块
	if r.config == nil || r.config.blockchain == nil {
		return fmt.Errorf("blockchain not available")
	}

	genesisHeader, exists := r.config.blockchain.GetHeaderByNumber(0)
	if !exists {
		return fmt.Errorf("genesis block not found")
	}
	_ = genesisHeader // 不再读取 extraData，保留获取仅用于存在性校验

	// 2. 解析验证者地址（仅允许 staking 合约 JSON-RPC 获取，不允许回退 genesis extraData）
	var ibftValidators validator.AccountSet
	source := "unknown"
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok && dposInstance != nil {
			if bootstrap, src, berr := dposInstance.getBootstrapValidators(); berr == nil && len(bootstrap) > 0 {
				ibftValidators = bootstrap
				source = src
			} else if berr != nil {
				return fmt.Errorf("failed to get bootstrap validators from staking contract: %w", berr)
			}
		}
	}
	if ibftValidators == nil || ibftValidators.Len() == 0 {
		return fmt.Errorf("bootstrap validators is empty (source=%s)", source)
	}

	// 3. 设置最小质押门槛（从配置读取 dpos_delegate_threshold）
	// 注意：runtime 中没有直接访问 DPoS 实例，需要通过 backend 获取
	var minStakeAmount *big.Int
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			minStakeAmount = dposInstance.getDelegateThreshold()
		}
	}
	// 如果无法从 backend 获取，使用默认值
	if minStakeAmount == nil {
		minStakeAmount, _ = new(big.Int).SetString("1000000000000000000000", 10) // 默认1000 VCITY
		r.logger.Warn("⚠️ 无法从配置读取 dpos_delegate_threshold，使用默认值", "defaultValue", minStakeAmount.String())
	} else {
		r.logger.Info("✅ 从配置读取最小质押门槛", "amount", minStakeAmount.String(), "source", "dpos_delegate_threshold")
	}

	validValidatorCount := 0
	insufficientBalanceCount := 0

	// 4. 清空现有的delegates
	r.delegates = make(validator.AccountSet, 0)

	// 5. 为每个验证者地址检查余额并生成BLS公钥
	for i := 0; i < ibftValidators.Len(); i++ {
		ibftValidator := ibftValidators[i]
		address := ibftValidator.Address

		// 创世验证者初始权重为0，在共识切换高度由 CreateGenesisVotes 写入投票后再有权重
		initialVotingPower := big.NewInt(0)

		delegate := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: initialVotingPower,
			BlsKey:      nil, // BLS公钥将在需要时获取
			IsActive:    true,
		}

		// 直接添加到 delegates
		r.delegates = append(r.delegates, delegate)
		validValidatorCount++
	}

	// DPoS验证者筛选结果汇总
	r.logger.Info("🚨 DPoS验证者筛选完成",
		"totalCandidates", ibftValidators.Len(),
		"validValidators", validValidatorCount,
		"insufficientBalance", insufficientBalanceCount,
		"successRate", fmt.Sprintf("%.1f%%", float64(validValidatorCount)/float64(ibftValidators.Len())*100))

	// 检查验证者数量
	if validValidatorCount == 0 {
		return fmt.Errorf("no valid validators found")
	}

	if validValidatorCount < 2 {
		r.logger.Warn("⚠️ 警告：DPoS验证者数量过少",
			"validValidators", validValidatorCount,
			"建议至少需要2个验证者")
	}

	r.logger.Info("✅ DPoS验证者解析完成", "count", validValidatorCount)

	// 不再逐条打印创世验证者详情，避免启动刷屏（需要排查时请查看 staking 合约返回）

	// 构建创世验证者映射
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			// 初始化创世验证者映射（使用独立锁，避免与 d.lock 互相阻塞）
			dposInstance.genesisValidatorsMu.Lock()
			if dposInstance.genesisValidators == nil {
				dposInstance.genesisValidators = make(map[types.Address]bool)
			}
			// 将解析出的验证者添加到映射中
			for _, delegate := range r.delegates {
				dposInstance.genesisValidators[delegate.Address] = true
			}
			cnt := len(dposInstance.genesisValidators)
			addresses := make([]string, 0, cnt)
			for addr := range dposInstance.genesisValidators {
				addresses = append(addresses, addr.String())
			}
			dposInstance.genesisValidatorsMu.Unlock()

			r.logger.Info("✅ 创世验证者映射已构建",
				"count", cnt,
				"addresses", addresses)

			// 权重在共识切换高度创建创世投票后再从投票记录计算并更新
			r.logger.Debug("创世验证者初始权重为0，将在共识切换高度通过投票记录更新",
				"validatorCount", len(r.delegates),
				"consensusSwitchHeight", func() uint64 {
					if dposInstance.config != nil {
						return dposInstance.config.ConsensusSwitchHeight
					}
					return 0
				}())
		}
	}

	return nil
}

// parseValidatorsFromExtraData 从extraData解析验证者地址
func (r *dposRuntime) parseValidatorsFromExtraData(extraData []byte) (validator.AccountSet, error) {
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short")
	}

	// 使用与 ForkManager 相同的解析逻辑
	ibftValidators, err := r.parseValidatorsFromExtraDataDirectly(extraData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse IBFT validators: %w", err)
	}

	return ibftValidators, nil
}

// parseValidatorsFromExtraDataDirectly 直接解析extraData中的验证者地址
func (r *dposRuntime) parseValidatorsFromExtraDataDirectly(extraData []byte) (validator.AccountSet, error) {

	// 使用与 ForkManager 相同的解析逻辑
	// Remove only the vanity bytes (32 bytes) from extraData
	// The rest is RLP data containing validators and seals
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short: %d bytes", len(extraData))
	}

	// Extract the RLP-encoded data
	// extraData format: [vanity(32)] + [RLP(IstanbulExtra)]
	rlpData := extraData[32:]

	// 创建验证者列表
	validatorList := make([]*validator.ValidatorMetadata, 0)

	// Parse RLP data using the same method as ForkManager
	err := types.UnmarshalRlp(func(p *fastrlp.Parser, v *fastrlp.Value) error {
		// Get the top-level list
		elems, err := v.GetElems()
		if err != nil {
			return fmt.Errorf("expected array: %w", err)
		}

		// Process each element
		for _, elem := range elems {
			// Try to get bytes
			if bytes, err := elem.GetBytes(nil); err == nil {
				// If it's 20 bytes, it might be an address
				if len(bytes) == 20 {
					addr := types.BytesToAddress(bytes)
					delegate := &validator.ValidatorMetadata{
						Address:     addr,
						VotingPower: big.NewInt(0), // 将在后续步骤中设置
						BlsKey:      nil,           // BLS公钥将在需要时获取
						IsActive:    true,
					}
					validatorList = append(validatorList, delegate)
				}
			} else {
				// Try to get sub-elements
				if subElems, err := elem.GetElems(); err == nil {
					for _, subElem := range subElems {
						if subBytes, err := subElem.GetBytes(nil); err == nil {
							// If it's 20 bytes, it might be an address
							if len(subBytes) == 20 {
								addr := types.BytesToAddress(subBytes)
								delegate := &validator.ValidatorMetadata{
									Address:     addr,
									VotingPower: big.NewInt(0), // 将在后续步骤中设置
									BlsKey:      nil,           // BLS公钥将在需要时获取
									IsActive:    true,
								}
								validatorList = append(validatorList, delegate)
							}
						}
					}
				}
			}
		}

		return nil
	}, rlpData)

	if err != nil {
		return nil, fmt.Errorf("failed to parse RLP data: %w", err)
	}

	return validatorList, nil
}

// initializeDelegates 初始化受托人集合
func (r *dposRuntime) initializeDelegates() error {
	// 修复：使用当前区块号获取受托人集合
	fromExtraData := false
	if r.backend != nil {
		// 获取当前区块号
		currentBlockNumber := uint64(0)
		if r.config != nil && r.config.blockchain != nil {
			if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
				currentBlockNumber = currentHeader.Number
			}
		}

		delegates, err := r.backend.GetDelegates(currentBlockNumber, nil)
		if err != nil {
			r.logger.Error("failed to get current delegates from backend", "error", err)
			return fmt.Errorf("failed to get current delegates from backend: %w", err)
		}

		// 应用 DPoSValidatorsCount 限制，只取前N个受托人
		maxDelegates := 0
		if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance != nil && dposInstance.config != nil {
			maxDelegates = int(dposInstance.config.DPoSValidatorsCount)
		}
		if maxDelegates > 0 {
			originalCount := len(delegates)
			if len(delegates) > maxDelegates {
				delegates = delegates[:maxDelegates]
				r.logger.Info("🎯 runtime初始化：限制受托人数量为前N个",
					"originalCount", originalCount,
					"limitedCount", maxDelegates,
					"configDPoSValidatorsCount", maxDelegates)
			} else {
				r.logger.Debug("🎯 runtime初始化：受托人数量未超过限制",
					"actualCount", originalCount,
					"configDPoSValidatorsCount", maxDelegates)
			}
		}

		r.delegates = delegates
		r.logger.Debug("initialized delegates from backend", "count", len(r.delegates))

		// 如果从 backend 获取的 delegates 为空，执行启动引导解析
		if len(r.delegates) == 0 {
			// 直接在 dposRuntime 中执行启动引导解析（当前实现：仅允许通过 staking 合约读取）
			if err := r.parseValidatorsFromGenesis(); err != nil {
				r.logger.Error("Failed to parse validators from genesis", "error", err)
				// 继续使用空集合
			} else {
				r.logger.Info("✅ 已完成启动引导解析验证者", "count", len(r.delegates))
			}
		} else {
			r.logger.Info("✅ 从backend获取到验证者，跳过启动引导解析", "count", len(r.delegates))
		}

		// 按voterpower排序并截取前N个验证者
		if len(r.delegates) > 0 {
			maxDelegates := 0
			if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance != nil && dposInstance.config != nil {
				maxDelegates = int(dposInstance.config.DPoSValidatorsCount)
			}

			// 按标准化规则排序，确保所有节点完全一致
			sort.Slice(r.delegates, func(i, j int) bool {
				// 1. 首先按票数降序排序
				votingPowerCmp := r.delegates[i].VotingPower.Cmp(r.delegates[j].VotingPower)
				if votingPowerCmp != 0 {
					return votingPowerCmp > 0
				}
				// 2. 票数相同，按地址升序排序（确保完全一致）
				return bytes.Compare(r.delegates[i].Address[:], r.delegates[j].Address[:]) < 0
			})

			// 截取前N个验证者（使用上面已定义的 maxDelegates）
			if maxDelegates > 0 {
				if len(r.delegates) > maxDelegates {
					r.delegates = r.delegates[:maxDelegates]
				}
			}
		}

		r.logger.Debug("=== 受托人集合详细信息 ===")
		for i, delegate := range r.delegates {
			r.logger.Debug("受托人信息",
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}

		// 检查当前节点的地址是否在受托人集合中
		if r.config != nil && r.config.Key != nil {
			keyAddr := types.Address(r.config.Key.Address())
			r.logger.Debug("当前节点地址", "keyAddr", keyAddr.String())

			found := false
			for i, delegate := range r.delegates {
				if delegate.Address == keyAddr {
					r.logger.Debug("找到当前节点在受托人集合中", "index", i, "address", keyAddr.String())
					found = true
					break
				}
			}
			if !found {
				r.logger.Warn("当前节点不在受托人集合中", "keyAddr", keyAddr.String())
			}
		}
	} else {
		// 如果没有backend，使用空集合
		r.delegates = validator.AccountSet{}
		r.logger.Warn("no backend available, using empty delegate set")
	}

	// 同步数据到 d.runtime.delegates 和 d.delegates
	// r.backend 是 DPoS 实例，r.backend.runtime 就是 d.runtime
	if r.backend != nil {
		// 通过 backend 访问 DPoS 实例的 runtime
		if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance.runtime != nil {
			dposInstance.runtime.delegates = r.delegates.Copy()
			dposInstance.delegates = r.delegates.Copy()

			// 只有在从extraData解析验证者时才同步到数据库
			if fromExtraData {
				if err := dposInstance.syncDelegatesToDatabase(r.delegates); err != nil {
					r.logger.Warn("⚠️ 同步验证者数据到数据库失败", "error", err)
				}
			}
		} else {
			r.logger.Warn("⚠️ 无法访问 d.runtime，验证者数据同步失败")
		}
	} else {
		r.logger.Warn("⚠️ backend为nil，无法同步验证者数据")
	}

	return nil
}
