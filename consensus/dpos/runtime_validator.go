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

	// 2. 解析验证者地址
	ibftValidators, err := r.parseValidatorsFromExtraData(genesisHeader.ExtraData)
	if err != nil {
		return fmt.Errorf("failed to parse validators from extraData: %w", err)
	}

	// 打印从创世文件解析出的验证者地址
	r.logger.Info("🔍 从创世文件ExtraData解析出的验证者地址",
		"totalCount", ibftValidators.Len(),
		"extraDataLength", len(genesisHeader.ExtraData))

	for i, validator := range ibftValidators {
		r.logger.Info("📝 创世验证者地址",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive)
	}

	// 3. 设置最小质押门槛
	minStakeAmount := big.NewInt(0)
	minStakeAmount.SetString("1000000000000000000000", 10) // 1000 VCITY

	validValidatorCount := 0
	insufficientBalanceCount := 0

	// 4. 清空现有的delegates
	r.delegates = make(validator.AccountSet, 0)

	// 5. 为每个验证者地址检查余额并生成BLS公钥
	for i := 0; i < ibftValidators.Len(); i++ {
		ibftValidator := ibftValidators[i]
		address := ibftValidator.Address

		// 创建DPoS验证者（BLS公钥延迟获取）
		// 创世验证者使用固定权重1000 VCITY，不受余额影响
		fixedVotingPower := new(big.Int)
		fixedVotingPower.SetString("1000000000000000000000", 10) // 1000 VCITY

		delegate := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: fixedVotingPower, // 使用固定权重，不依赖余额
			BlsKey:      nil,              // BLS公钥将在需要时获取
			IsActive:    true,
		}

		// 直接添加到 delegates
		r.delegates = append(r.delegates, delegate)
		validValidatorCount++

		r.logger.Info("✅ DPoS验证者创建成功（BLS公钥延迟获取）",
			"address", address.String(),
			"votingPower", fixedVotingPower.String(),
			"validatorIndex", validValidatorCount,
			"note", "创世验证者使用固定权重")
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

	// 打印最终解析出的创世验证者列表
	r.logger.Info("🎯 最终解析出的创世验证者列表",
		"totalCount", len(r.delegates))

	for i, delegate := range r.delegates {
		r.logger.Info("📋 创世验证者详情",
			"index", i,
			"address", delegate.Address.String(),
			"votingPower", delegate.VotingPower.String(),
			"isActive", delegate.IsActive,
			"hasBlsKey", delegate.BlsKey != nil)
	}

	// 构建创世验证者映射
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok {
			// 初始化创世验证者映射
			dposInstance.genesisValidators = make(map[types.Address]bool)

			// 将解析出的验证者添加到映射中
			for _, delegate := range r.delegates {
				dposInstance.genesisValidators[delegate.Address] = true
			}

			r.logger.Info("✅ 创世验证者映射已构建",
				"count", len(dposInstance.genesisValidators),
				"addresses", func() []string {
					addresses := make([]string, 0, len(dposInstance.genesisValidators))
					for addr := range dposInstance.genesisValidators {
						addresses = append(addresses, addr.String())
					}
					return addresses
				}())

			// 立即将创世验证者的固定权重保存到数据库
			for _, delegate := range r.delegates {
				// 创建DelegateInfo并保存到数据库
				delegateInfo := &DelegateInfo{
					Address:        delegate.Address,
					VotingPower:    new(big.Int).Set(delegate.VotingPower), // 使用固定权重1000 VCITY
					TotalVotes:     new(big.Int).Set(delegate.VotingPower), // 使用固定权重1000 VCITY
					ProducedBlocks: 0,
					MissedBlocks:   0,
					LastBlockTime:  0,
					IsActive:       delegate.IsActive,
				}

				dposInstance.populateCommissionFields(delegate.Address, delegateInfo)

				// 保存到数据库
				if err := dposInstance.state.StakeStore.setDelegateInfo(delegate.Address, delegateInfo, nil); err != nil {
					r.logger.Error("❌ 保存创世验证者权重到数据库失败",
						"address", delegate.Address.String(),
						"error", err)
				} else {
					r.logger.Info("✅ 创世验证者权重已保存到数据库",
						"address", delegate.Address.String(),
						"votingPower", delegate.VotingPower.String())
				}
			}
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

		// 应用 DelegateCount 限制，只取前N个受托人
		if r.config != nil && r.config.DelegateCount > 0 {
			maxDelegates := int(r.config.DelegateCount)
			originalCount := len(delegates)
			if len(delegates) > maxDelegates {
				delegates = delegates[:maxDelegates]
				r.logger.Info("🎯 runtime初始化：限制受托人数量为前N个",
					"originalCount", originalCount,
					"limitedCount", maxDelegates,
					"configDelegateCount", r.config.DelegateCount)
			} else {
				r.logger.Debug("🎯 runtime初始化：受托人数量未超过限制",
					"actualCount", originalCount,
					"configDelegateCount", r.config.DelegateCount)
			}
		}

		r.delegates = delegates
		r.logger.Debug("initialized delegates from backend", "count", len(r.delegates))

		// 如果从backend获取的delegates为空，尝试从extraData解析
		if len(r.delegates) == 0 {
			r.logger.Info("🎯 从backend获取的delegates为空，尝试从extraData解析验证者")

			// 直接在dposRuntime中解析extraData验证者
			if err := r.parseValidatorsFromGenesis(); err != nil {
				r.logger.Error("Failed to parse validators from genesis", "error", err)
				// 继续使用空集合
			} else {
				r.logger.Info("✅ 已从extraData解析验证者", "count", len(r.delegates))
				fromExtraData = true
			}
		} else {
			r.logger.Info("✅ 从backend获取到验证者，跳过extraData解析", "count", len(r.delegates))
		}

		// 按voterpower排序并截取前N个验证者
		if len(r.delegates) > 0 {
			r.logger.Info("🔍 开始按voterpower排序并截取前N个验证者",
				"originalCount", len(r.delegates),
				"configDelegateCount", r.config.DelegateCount)

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

			// 截取前N个验证者
			maxDelegates := int(r.config.DelegateCount)
			originalCount := len(r.delegates)
			if len(r.delegates) > maxDelegates {
				r.delegates = r.delegates[:maxDelegates]
				r.logger.Info("🎯 限制验证者数量为前N个",
					"originalCount", originalCount,
					"limitedCount", maxDelegates,
					"configDelegateCount", r.config.DelegateCount)
			}

			r.logger.Info("✅ 验证者排序和截取完成",
				"finalCount", len(r.delegates),
				"maxDelegates", maxDelegates)
		}

		r.logger.Debug("=== 受托人集合详细信息 ===")
		for i, delegate := range r.delegates {
			r.logger.Debug("受托人信息",
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}
		r.logger.Info("=== 受托人集合详细信息结束 ===")

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
			r.logger.Info("✅ 已同步验证者数据到 d.runtime.delegates 和 d.delegates", "count", len(dposInstance.runtime.delegates))

			// 只有在从extraData解析验证者时才同步到数据库
			if fromExtraData {
				if err := dposInstance.syncDelegatesToDatabase(r.delegates); err != nil {
					r.logger.Warn("⚠️ 同步验证者数据到数据库失败", "error", err)
				} else {
					r.logger.Info("✅ 验证者数据已准备，将在BLS公钥获取完成后保存到数据库", "count", len(r.delegates))
				}
			} else {
				r.logger.Info("✅ 验证者数据来自数据库，无需同步", "count", len(r.delegates))
			}
		} else {
			r.logger.Warn("⚠️ 无法访问 d.runtime，验证者数据同步失败")
		}
	} else {
		r.logger.Warn("⚠️ backend为nil，无法同步验证者数据")
	}

	r.logger.Info("✅ dposRuntime.initializeDelegates 结束")
	return nil
}
