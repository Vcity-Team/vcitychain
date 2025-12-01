package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/umbracle/fastrlp"
)

func (d *DPoS) parseValidatorsFromGenesis() error {
	d.logger.Info("🔍 开始从创世块解析DPoS验证者")

	// 1. 获取创世块
	genesisHeader, exists := d.config.Blockchain.GetHeaderByNumber(0)
	if !exists {
		return fmt.Errorf("genesis block not found")
	}

	// 保存创世块extraData
	d.genesisExtraData = genesisHeader.ExtraData
	d.logger.Info("📋 创世块extraData已加载", "length", len(d.genesisExtraData))

	// 2. 解析验证者地址
	ibftValidators, err := d.parseValidatorsFromExtraData(d.genesisExtraData)
	if err != nil {
		return WrapError("parse validators from extraData", err)
	}

	// 3. 设置最小质押门槛
	d.minStakeAmount = d.config.MinVotingPower
	if d.minStakeAmount == nil {
		// 使用默认值：1000 VCITY = 1000 * 1e18 wei
		d.minStakeAmount = DefaultVotingPower()
		d.logger.Info("使用默认最小质押门槛", "amount", d.minStakeAmount.String())
	}

	// 4. 直接操作 runtime.delegates（如果runtime已初始化）
	if d.runtime == nil {
		d.logger.Warn("⚠️ runtime未初始化，无法解析验证者")
		return fmt.Errorf("runtime not initialized")
	}

	// 清空现有的delegates
	d.runtime.delegates = make(validator.AccountSet, 0)

	// 🆕 初始化创世验证者映射（如果尚未初始化）
	d.lock.Lock()
	if d.genesisValidators == nil {
		d.genesisValidators = make(map[types.Address]bool)
		d.logger.Info("🔧 初始化创世验证者映射")
	}
	d.lock.Unlock()

	d.logger.Info("🚨 DPoS验证者筛选开始",
		"totalCandidates", ibftValidators.Len(),
		"minStakeAmount", d.minStakeAmount.String())

	validValidatorCount := 0
	insufficientBalanceCount := 0

	// 5. 为每个验证者地址检查余额并生成BLS公钥
	for i := 0; i < ibftValidators.Len(); i++ {
		ibftValidator := ibftValidators[i]
		address := ibftValidator.Address

		// 🆕 创世验证者使用固定权重1000 VCITY，不受余额影响
		fixedVotingPower := new(big.Int)
		fixedVotingPower.SetString("1000000000000000000000", 10) // 1000 VCITY

		delegate := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: fixedVotingPower, // 使用固定权重，不依赖余额
			BlsKey:      nil,              // BLS公钥将在需要时获取
			IsActive:    true,
		}

		// 直接添加到 runtime.delegates
		d.runtime.delegates = append(d.runtime.delegates, delegate)

		// 🆕 添加到创世验证者映射
		d.lock.Lock()
		d.genesisValidators[address] = true
		d.lock.Unlock()

		validValidatorCount++

		d.logger.Info("✅ DPoS验证者创建成功（BLS公钥延迟获取）",
			"address", address.String(),
			"votingPower", fixedVotingPower.String(),
			"validatorIndex", validValidatorCount,
			"note", "创世验证者使用固定权重")
	}

	// 关键日志：DPoS验证者筛选结果汇总
	d.logger.Info("🚨 DPoS验证者筛选完成",
		"totalCandidates", ibftValidators.Len(),
		"validValidators", validValidatorCount,
		"insufficientBalance", insufficientBalanceCount,
		"successRate", fmt.Sprintf("%.1f%%", float64(validValidatorCount)/float64(ibftValidators.Len())*100))

	if validValidatorCount == 0 {
		d.logger.Error("❌ 没有验证者满足DPoS质押要求",
			"totalCandidates", ibftValidators.Len(),
			"minStakeAmount", d.minStakeAmount.String())
		return fmt.Errorf("no validators meet DPoS stake requirements")
	}

	if validValidatorCount < 2 {
		d.logger.Warn("⚠️ 警告：DPoS验证者数量过少",
			"validValidators", validValidatorCount,
			"建议至少需要2个验证者")
	}

	d.logger.Info("✅ DPoS验证者解析完成", "count", validValidatorCount)

	return nil
}

// 🆕 新增：从extraData解析验证者地址
func (d *DPoS) parseValidatorsFromExtraData(extraData []byte) (validator.AccountSet, error) {
	// 开始解析extraData

	// Remove only the vanity bytes (32 bytes) from extraData
	// The rest is RLP data containing validators and seals
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short: %d bytes", len(extraData))
	}

	// Extract the RLP-encoded data
	// extraData format: [vanity(32)] + [RLP(IstanbulExtra)]
	rlpData := extraData[32:]

	// 提取RLP数据

	// Create validator accounts
	validatorList := make([]*validator.ValidatorMetadata, 0)

	// Parse RLP data using the same method as the test
	err := types.UnmarshalRlp(func(p *fastrlp.Parser, v *fastrlp.Value) error {
		// Get the top-level list
		elems, err := v.GetElems()
		if err != nil {
			return WrapError("expected array", err)
		}

		// 找到验证者列表

		// Process each element
		for _, elem := range elems {
			// Try to get bytes
			if bytes, err := elem.GetBytes(nil); err == nil {
				// If it's 20 bytes, it might be an address
				if len(bytes) == 20 {
					addr := types.BytesToAddress(bytes)
					validator := &validator.ValidatorMetadata{
						Address:     addr,
						VotingPower: big.NewInt(0), // 初始化为0，后续会更新
						IsActive:    true,
					}
					validatorList = append(validatorList, validator)

					// 解析验证者地址
				}
			} else {
				// Try to get sub-elements
				if subElems, err := elem.GetElems(); err == nil {
					// 找到子列表

					for _, subElem := range subElems {
						if subBytes, err := subElem.GetBytes(nil); err == nil {
							// If it's 20 bytes, it might be an address
							if len(subBytes) == 20 {
								addr := types.BytesToAddress(subBytes)
								validator := &validator.ValidatorMetadata{
									Address:     addr,
									VotingPower: big.NewInt(0), // 初始化为0，后续会更新
									IsActive:    true,
								}
								validatorList = append(validatorList, validator)

								// 解析子列表中的验证者地址
							}
						}
					}
				}
			}
		}

		return nil
	}, rlpData)

	if err != nil {
		return nil, WrapError("parse RLP data", err)
	}

	return validator.AccountSet(validatorList), nil
}

