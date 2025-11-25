package dpos

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/common"
)

// GetParameterProposal 获取提案信息
func (d *DPoS) GetParameterProposal(proposalID string) (*ParameterProposal, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	proposal, err := d.governanceHydrateProposal(proposalID)
	if err != nil || proposal == nil {
		if errors.Is(err, errProposalStoreUnavailable) {
			d.logger.Warn("⚠️ [GetProposal] ProposalStore不可用，无法从数据库查询",
				"proposalID", proposalID,
				"stateIsNil", d.state == nil,
				"proposalStoreIsNil", d.state != nil && d.state.ProposalStore == nil)
		} else if err != nil {
			d.logger.Info("❌ [GetProposal] 从数据库加载提案失败", "proposalID", proposalID, "error", err)
		}
		return nil, fmt.Errorf("proposal not found")
	}

	d.logger.Info("✅ [GetParameterProposal] 获取提案成功", "proposalID", proposalID, "proposalType", proposal.ProposalType)
	return proposal, nil
}

// GetActiveProposals 获取活跃提案列表
func (d *DPoS) GetActiveProposals() ([]*ParameterProposal, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	var activeProposals []*ParameterProposal
	for proposalID := range d.activeProposals {
		if proposal, exists := d.parameterProposals[proposalID]; exists {
			activeProposals = append(activeProposals, proposal)
		}
	}

	return activeProposals, nil
}

// GetVotableCurrentParameters 获取可表决参数列表（包含当前值）
func (d *DPoS) GetVotableCurrentParameters() map[string]*ParameterInfo {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 返回副本以避免外部修改
	result := make(map[string]*ParameterInfo)
	for name, info := range d.votableParameters {
		// 创建副本
		infoCopy := *info

		// 🆕 添加当前值
		d.parameterValuesMutex.RLock()
		if currentValue, exists := d.parameterCurrentValues[name]; exists {
			infoCopy.CurrentValue = currentValue
			d.logger.Debug("Found current value for parameter",
				"param", name,
				"value", currentValue)
		} else {
			// 如果缓存中没有，尝试从不同来源获取默认值
			var defaultValue interface{}
			var err error

			// 🆕 优先从数据库（ParameterStore）读取（经过治理流程修改的值是权威数据源）
			if d.state != nil && d.state.ParameterStore != nil {
				if dbValue, dbErr := d.state.ParameterStore.GetParameterValue(name); dbErr == nil {
					infoCopy.CurrentValue = dbValue
					d.logger.Debug("从数据库读取参数值",
						"param", name,
						"value", dbValue)
					// 更新缓存
					d.parameterValuesMutex.Lock()
					d.parameterCurrentValues[name] = dbValue
					d.parameterValuesMutex.Unlock()
				} else {
					// 数据库中没有，尝试从配置文件获取（初始默认值）
					if defaultValue, err = d.getConfigParameterValue(name); err == nil {
						infoCopy.CurrentValue = defaultValue
						d.logger.Debug("从配置文件读取参数值",
							"param", name,
							"value", defaultValue)
					} else {
						// 如果配置文件也没有，尝试从治理参数获取
						if defaultValue, err = d.getGovernanceParameterValue(name); err == nil {
							infoCopy.CurrentValue = defaultValue
							d.logger.Debug("从治理参数读取参数值",
								"param", name,
								"value", defaultValue)
						} else {
							// 最后尝试使用 getCurrentParameterValue（会再次检查数据库和配置）
							if defaultValue, err = d.getCurrentParameterValue(name); err == nil {
								infoCopy.CurrentValue = defaultValue
								d.logger.Debug("通过getCurrentParameterValue读取参数值",
									"param", name,
									"value", defaultValue)
							} else {
								d.logger.Debug("无法获取参数值",
									"param", name,
									"cacheSize", len(d.parameterCurrentValues))
							}
						}
					}
				}
			} else {
				// ParameterStore 不可用，回退到配置文件
				if defaultValue, err = d.getConfigParameterValue(name); err == nil {
					infoCopy.CurrentValue = defaultValue
					d.logger.Debug("从配置文件读取参数值（ParameterStore不可用）",
						"param", name,
						"value", defaultValue)
				}
			}
		}
		d.parameterValuesMutex.RUnlock()

		// 调试：确认CurrentValue是否被设置
		d.logger.Debug("Setting result for parameter",
			"param", name,
			"currentValue", infoCopy.CurrentValue,
			"hasCurrentValue", infoCopy.CurrentValue != nil)

		result[name] = &infoCopy
	}
	return result
}

// UpdateParameterValue 更新参数值（公共方法）
func (d *DPoS) UpdateParameterValue(parameter string, newValue interface{}) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 根据参数类型更新相应的配置
	switch parameter {
	case "dpos_reward_amount":
		if amount, ok := newValue.(string); ok {
			if bigAmount, ok := new(big.Int).SetString(amount, 10); ok {
				d.config.RewardAmount = bigAmount
				d.logger.Info("Updated reward amount", "newValue", amount)
			}
		}
	case "dpos_delegate_threshold":
		if threshold, ok := newValue.(string); ok {
			if bigThreshold, ok := new(big.Int).SetString(threshold, 10); ok {
				d.config.MinVotingPower = bigThreshold
				d.logger.Info("Updated delegate threshold", "newValue", threshold)
			}
		}
	case "block_time_s":
		if blockTime, ok := newValue.(uint64); ok {
			d.config.BlockTime = common.Duration{Duration: time.Duration(blockTime) * time.Second}
			d.logger.Info("Updated block time", "newValue", blockTime)
		}
	case "dpos_epoch_duration":
		if duration, ok := newValue.(string); ok {
			if parsedDuration, err := time.ParseDuration(duration); err == nil {
				d.config.EpochDuration = parsedDuration
				d.logger.Info("Updated epoch duration", "newValue", duration)
			}
		}
	default:
		return fmt.Errorf("unknown parameter: %s", parameter)
	}

	return nil
}
