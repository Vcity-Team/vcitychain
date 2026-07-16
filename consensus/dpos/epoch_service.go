package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
)

// processEpochBoundary encapsulates the logic executed at each epoch end block.
func (d *DPoS) processEpochBoundary(r *dposRuntime, parent *types.Header, nextBlockNumber uint64) {
	if d.epochLifecycle != nil {
		result, err := d.epochLifecycle.ProcessBoundary(core.EpochBoundaryContext{
			ParentHash:      parent.Hash,
			NextBlockNumber: nextBlockNumber,
		})
		if err != nil {
			r.logger.Error("⚠️ [epochBoundary] 模块化处理失败",
				"blockNumber", nextBlockNumber,
				"error", err)
			return
		}
		d.pendingFaultFlags = convertCoreFaultFlagsToLocal(result.FaultFlags)
		return
	}

	// Apply scheduled recovery proposals before calculating next epoch validators
	d.applyScheduledRecoveryProposals(r, nextBlockNumber)

	// Run fault detection and persist the results
	d.runEpochFaultDetection(r, parent, nextBlockNumber)
}

func (d *DPoS) applyScheduledRecoveryProposals(r *dposRuntime, nextBlockNumber uint64) {
	currentEpoch := uint64(0)
	if epochMeta := d.getEpochForBlock(nextBlockNumber - 1); epochMeta != nil {
		currentEpoch = epochMeta.Number
	}

	scheduledProps := d.governanceLoadScheduled(currentEpoch)

	seen := make(map[string]bool)
	for _, prop := range scheduledProps {
		if prop == nil || prop.ID == "" || seen[prop.ID] {
			continue
		}
		seen[prop.ID] = true

		if prop.Schedule.Scheduled && prop.Schedule.EffectiveEpoch == currentEpoch && !prop.Schedule.Applied {
			if prop.ProposalType == "validator_recovery" {
				r.logger.Info("🔄 [epochBoundary] 开始应用恢复提案", "proposalID", prop.ID, "validator", prop.ValidatorAddress.String(), "currentEpoch", currentEpoch)

				validatorAddr := prop.ValidatorAddress
				if validatorAddr == (types.Address{}) {
					validatorAddr = types.StringToAddress(prop.Parameter)
				}

				if validatorAddr == (types.Address{}) || d.state == nil || d.state.StakeStore == nil {
					continue
				}

				if err := d.state.StakeStore.ClearValidatorFaultStatus(validatorAddr, prop.ID); err != nil {
					r.logger.Error("❌ [epochBoundary] 清除故障标志失败", "error", err, "proposalID", prop.ID, "validator", validatorAddr.String())
					continue
				}

				r.logger.Info("✅ [epochBoundary] 验证者故障标志已清除（数据库）", "proposalID", prop.ID, "validator", validatorAddr.String())

				if d.faultyValidators != nil {
					delete(d.faultyValidators, validatorAddr)
					r.logger.Info("✅ [epochBoundary] 验证者故障标志已清除（内存）", "proposalID", prop.ID, "validator", validatorAddr.String())
				}

				if err := d.reloadValidatorsAfterRecovery(); err != nil {
					r.logger.Error("❌ [epochBoundary] 重新加载验证者集合失败", "error", err, "proposalID", prop.ID)
				} else {
					r.logger.Info("✅ [epochBoundary] 验证者集合已重新加载", "proposalID", prop.ID, "validator", validatorAddr.String())
				}

				prop.Schedule.Applied = true
				prop.Schedule.AppliedAtBlock = nextBlockNumber
				r.logger.Info("✅ [epochBoundary] 恢复提案已应用（计算验证者集合前）", "proposalID", prop.ID, "validator", validatorAddr.String())
			}
		}
	}
}

func (d *DPoS) runEpochFaultDetection(r *dposRuntime, parent *types.Header, nextBlockNumber uint64) {
	if d.epochManager != nil {
		d.epochManager.TriggerEpochSwitch(nextBlockNumber)
	}

	keyAddr := types.Address(r.config.Key.Address())
	headerPreview := &types.Header{
		ParentHash: parent.Hash,
		Number:     nextBlockNumber,
		Miner:      keyAddr[:],
		Timestamp:  uint64(time.Now().Unix()),
	}
	d.SetPendingEpochEndHeader(headerPreview)

	faultFlags, err := d.detectValidatorFaults(nextBlockNumber)
	if err != nil {
		d.ClearPendingEpochEndHeader(nextBlockNumber)
		r.logger.Error("❌ [epochBoundary] 故障检测失败", "blockNumber", nextBlockNumber, "error", err)
		return
	}
	d.ClearPendingEpochEndHeader(nextBlockNumber)

	// 公测：主网质押复查合并进 FaultFlags（RPC 失败跳过，不影响漏块故障）
	if mainnetFlags := d.detectMainnetEligibilityFaults(nextBlockNumber); len(mainnetFlags) > 0 {
		faultFlags = MergeFaultFlags(faultFlags, mainnetFlags)
		r.logger.Info("🔗 [epochBoundary] 已合并主网准入故障标志",
			"blockNumber", nextBlockNumber,
			"mainnetFlags", len(mainnetFlags),
			"totalFlags", len(faultFlags))
	}

	for _, faultFlag := range faultFlags {
		if faultFlag.IsFaulty {
			if err := d.saveFaultStatusToDatabase(faultFlag); err != nil {
				r.logger.Warn("⚠️ [epochBoundary] 保存故障状态到数据库失败",
					"blockNumber", nextBlockNumber,
					"address", faultFlag.NodeAddress.String(),
					"error", err)
			} else {
				r.logger.Info("✅ [epochBoundary] 故障状态已保存到数据库",
					"blockNumber", nextBlockNumber,
					"address", faultFlag.NodeAddress.String(),
					"isFaulty", faultFlag.IsFaulty,
					"epoch", faultFlag.EpochNumber,
					"missedBlocks", faultFlag.MissedBlocks,
					"reason", faultFlag.Reason)
			}
			d.updateMemoryFaultStatus(faultFlag)
			continue
		}

		// 主网质押恢复：允许清故障（仅主网类 reason）；漏块故障仍须走恢复提案
		if d.applyMainnetStakeFaultClear(faultFlag) {
			r.logger.Info("✅ [epochBoundary] 已清除主网质押类故障",
				"blockNumber", nextBlockNumber,
				"address", faultFlag.NodeAddress.String(),
				"reason", faultFlag.Reason)
		}
	}

	d.pendingFaultFlags = faultFlags

	if d.state != nil && d.state.StakeStore != nil {
		if err := d.state.StakeStore.SaveCurrentEpoch(d.currentEpoch); err != nil {
			r.logger.Warn("⚠️ [epochBoundary] 保存currentEpoch到数据库失败",
				"epoch", d.currentEpoch,
				"blockNumber", nextBlockNumber,
				"error", err)
		} else {
			r.logger.Info("✅ [epochBoundary] currentEpoch已保存到数据库",
				"epoch", d.currentEpoch,
				"blockNumber", nextBlockNumber)
		}
	} else {
		r.logger.Warn("⚠️ [epochBoundary] StakeStore不可用，无法保存currentEpoch",
			"epoch", d.currentEpoch,
			"blockNumber", nextBlockNumber)
	}

	if err := d.updateBlockProducersFromFaultFlags(faultFlags); err != nil {
		r.logger.Error("❌ [epochBoundary] 本地更新出块者列表失败",
			"blockNumber", nextBlockNumber,
			"error", err)
	}

	// 有清故障时重新从 DB 加载名单（把已恢复的 SR 加回）
	for _, faultFlag := range faultFlags {
		if faultFlag.IsFaulty {
			continue
		}
		if faultFlag.Reason == FaultReasonMainnetStakeRestored {
			if err := d.reloadValidatorsAfterRecovery(); err != nil {
				r.logger.Error("❌ [epochBoundary] 主网恢复后重新加载验证者失败", "error", err)
			}
			break
		}
	}
}
