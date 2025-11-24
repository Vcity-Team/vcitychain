package dpos

import (
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// ==================== 适配器：将现有实现包装为接口 ====================

// ConsensusManagerAdapter 共识管理器适配器
// 包装现有的dposRuntime.buildBlock等方法
type ConsensusManagerAdapter struct {
	dpos *DPoS
}

// NewConsensusManagerAdapter 创建共识管理器适配器
func NewConsensusManagerAdapter(dpos *DPoS) core.ConsensusManager {
	return &ConsensusManagerAdapter{
		dpos: dpos,
	}
}

// BuildBlock 构建区块
func (c *ConsensusManagerAdapter) BuildBlock(parent *types.Header) (*types.FullBlock, error) {
	if c.dpos.runtime == nil {
		return nil, fmt.Errorf("runtime not initialized")
	}
	return c.dpos.runtime.buildBlock()
}

// ValidateBlock 验证区块
func (c *ConsensusManagerAdapter) ValidateBlock(block *types.Block) error {
	// 调用现有的验证逻辑
	if c.dpos.blockchain != nil {
		if wrapper, ok := c.dpos.blockchain.(*blockchainWrapper); ok {
			parent := c.dpos.config.Blockchain.Header()
			if parent == nil {
				return fmt.Errorf("parent header not found")
			}
			_, err := wrapper.ProcessBlock(parent, block)
			return err
		}
	}
	return fmt.Errorf("blockchain wrapper not available")
}

// ShouldProduceBlock 判断是否应该出块
func (c *ConsensusManagerAdapter) ShouldProduceBlock(blockNumber uint64, myAddress types.Address) bool {
	if c.dpos.blockScheduler == nil {
		return false
	}
	
	// 获取验证者集合
	validators := c.dpos.GetValidators()
	if len(validators) == 0 {
		return false
	}
	
	validatorsList := make([]types.Address, len(validators))
	for i, v := range validators {
		validatorsList[i] = v.Address
	}
	
	return c.dpos.blockScheduler.ShouldProduceBlockNow(
		myAddress,
		validatorsList,
		blockNumber,
		"ConsensusManagerAdapter",
	)
}

// IsEpochEndBlock 判断是否是epoch结束区块
func (c *ConsensusManagerAdapter) IsEpochEndBlock(blockNumber uint64) bool {
	if c.dpos.runtime != nil {
		return c.dpos.runtime.isEpochEndBlock(blockNumber)
	}
	return false
}

// ValidatorManagerAdapter 验证者管理器适配器
type ValidatorManagerAdapter struct {
	dpos *DPoS
}

// NewValidatorManagerAdapter 创建验证者管理器适配器
func NewValidatorManagerAdapter(dpos *DPoS) core.ValidatorManager {
	return &ValidatorManagerAdapter{
		dpos: dpos,
	}
}

// GetValidators 获取验证者集合
func (v *ValidatorManagerAdapter) GetValidators(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	return v.dpos.GetDelegates(blockNumber, parents)
}

// GetCurrentValidators 获取当前验证者集合
func (v *ValidatorManagerAdapter) GetCurrentValidators() validator.AccountSet {
	return v.dpos.GetValidators()
}

// UpdateValidators 更新验证者集合
func (v *ValidatorManagerAdapter) UpdateValidators(blockNumber uint64, validators validator.AccountSet) error {
	v.dpos.delegates = validators.Copy()
	if v.dpos.runtime != nil {
		v.dpos.runtime.lock.Lock()
		v.dpos.runtime.delegates = validators.Copy()
		v.dpos.runtime.lock.Unlock()
	}
	return nil
}

// IsValidator 判断是否是验证者
func (v *ValidatorManagerAdapter) IsValidator(address types.Address, blockNumber uint64) bool {
	validators, err := v.GetValidators(blockNumber, nil)
	if err != nil {
		return false
	}
	return validators.ContainsAddress(address)
}

// GetVotingPower 获取投票权重
func (v *ValidatorManagerAdapter) GetVotingPower(blockNumber uint64, validatorAddr types.Address) (*big.Int, error) {
	return v.dpos.GetVotingPower(blockNumber, validatorAddr)
}

// EpochManagerAdapter Epoch管理器适配器
type EpochManagerAdapter struct {
	epochManager *TimeBasedEpochManager
}

// NewEpochManagerAdapter 创建Epoch管理器适配器
func NewEpochManagerAdapter(epochManager *TimeBasedEpochManager) core.EpochManager {
	return &EpochManagerAdapter{
		epochManager: epochManager,
	}
}

// GetCurrentEpoch 获取当前epoch
func (e *EpochManagerAdapter) GetCurrentEpoch(blockNumber uint64) uint64 {
	if e.epochManager == nil {
		return 0
	}
	return e.epochManager.GetCurrentEpoch(blockNumber)
}

// GetEpochInfo 获取epoch信息
func (e *EpochManagerAdapter) GetEpochInfo(blockNumber uint64) (uint64, time.Time, time.Duration) {
	if e.epochManager == nil {
		return 0, time.Now(), 0
	}
	return e.epochManager.GetEpochInfo(blockNumber)
}

// IsEpochEnd 判断是否是epoch结束
func (e *EpochManagerAdapter) IsEpochEnd(blockNumber uint64) bool {
	if e.epochManager == nil {
		return false
	}
	epochSize := e.epochManager.getEpochSize()
	consensusSwitchHeight := e.epochManager.consensusSwitchHeight
	
	if blockNumber < consensusSwitchHeight {
		return false
	}
	
	dposBlockNumber := blockNumber - consensusSwitchHeight
	currentEpoch := (dposBlockNumber / epochSize) + 1
	firstBlockInEpoch := consensusSwitchHeight + (currentEpoch-1)*epochSize
	lastBlockInEpoch := firstBlockInEpoch + epochSize - 1
	return blockNumber >= lastBlockInEpoch
}

// GetEpochSize 获取epoch大小
func (e *EpochManagerAdapter) GetEpochSize() uint64 {
	if e.epochManager == nil {
		return 0
	}
	return e.epochManager.getEpochSize()
}

// RewardManagerAdapter 奖励管理器适配器
type RewardManagerAdapter struct {
	dpos            *DPoS
	rewardDistributor *RewardDistributor
}

// NewRewardManagerAdapter 创建奖励管理器适配器
func NewRewardManagerAdapter(dpos *DPoS) core.RewardManager {
	return &RewardManagerAdapter{
		dpos:             dpos,
		rewardDistributor: dpos.rewardDistributor,
	}
}

// CalculateRewards 计算奖励
func (r *RewardManagerAdapter) CalculateRewards(epochNumber uint64) (map[types.Address]*big.Int, error) {
	if r.rewardDistributor == nil {
		return nil, fmt.Errorf("reward distributor not initialized")
	}
	
	// 获取该epoch的验证者和投票者
	validators := r.dpos.GetValidators()
	voters := r.dpos.voters
	
	// 获取出块统计
	blockCounts := r.dpos.blockTracker.GetEpochBlockCounts(epochNumber)
	totalBlocks := r.dpos.blockTracker.GetTotalEpochBlocks(epochNumber)
	
	// 计算奖励
	rewards := r.rewardDistributor.CalculateRewards(validators, voters, blockCounts, totalBlocks)
	return rewards, nil
}

// DistributeRewards 分发奖励
func (r *RewardManagerAdapter) DistributeRewards(epochNumber uint64, rewards map[types.Address]*big.Int) error {
	if r.rewardDistributor == nil {
		return fmt.Errorf("reward distributor not initialized")
	}
	
	validators := r.dpos.GetValidators()
	voters := r.dpos.voters
	
	return r.rewardDistributor.DistributeEpochRewards(epochNumber, validators, voters)
}

// GetRewardInfo 获取奖励信息
func (r *RewardManagerAdapter) GetRewardInfo(validatorAddress types.Address, epochNumber uint64) map[string]interface{} {
	info := make(map[string]interface{})
	
	if r.dpos.blockTracker != nil {
		blockCounts := r.dpos.blockTracker.GetEpochBlockCounts(epochNumber)
		totalBlocks := r.dpos.blockTracker.GetTotalEpochBlocks(epochNumber)
		
		info["blocksProduced"] = blockCounts[validatorAddress]
		info["totalBlocks"] = totalBlocks
	}
	
	return info
}

// FaultManagerAdapter 故障管理器适配器
type FaultManagerAdapter struct {
	dpos          *DPoS
	faultDetector *FaultDetector
}

// NewFaultManagerAdapter 创建故障管理器适配器
func NewFaultManagerAdapter(dpos *DPoS) core.FaultManager {
	return &FaultManagerAdapter{
		dpos:          dpos,
		faultDetector: nil, // 延迟初始化
	}
}

// DetectFaults 检测故障
func (f *FaultManagerAdapter) DetectFaults(blockNumber uint64) ([]core.FaultFlagInfo, error) {
	// 使用dpos的故障检测逻辑
	if f.dpos.pendingFaultFlags != nil {
		result := make([]core.FaultFlagInfo, len(f.dpos.pendingFaultFlags))
		for i, flag := range f.dpos.pendingFaultFlags {
			faultType := "missed_blocks"
			if flag.DoubleSigningHeight > 0 {
				faultType = "double_signing"
			}
			result[i] = core.FaultFlagInfo{
				ValidatorAddress: flag.NodeAddress,
				EpochNumber:      flag.EpochNumber,
				FaultType:        faultType,
				Reason:           flag.Reason,
			}
		}
		return result, nil
	}
	return nil, nil
}

// IsValidatorFaulty 判断验证者是否故障
func (f *FaultManagerAdapter) IsValidatorFaulty(validatorAddress types.Address) (bool, error) {
	if f.dpos.faultyValidators != nil {
		return f.dpos.faultyValidators[validatorAddress], nil
	}
	return false, nil
}

// GetFaultInfo 获取故障信息
func (f *FaultManagerAdapter) GetFaultInfo(validatorAddress types.Address) map[string]interface{} {
	info := make(map[string]interface{})
	
	if f.dpos.faultyValidators != nil {
		info["isFaulty"] = f.dpos.faultyValidators[validatorAddress]
	}
	
	if f.dpos.missedBlocksCount != nil {
		info["missedBlocks"] = f.dpos.missedBlocksCount[validatorAddress]
	}
	
	return info
}

// QueryManagerAdapter 查询管理器适配器
type QueryManagerAdapter struct {
	dpos *DPoS
}

// NewQueryManagerAdapter 创建查询管理器适配器
func NewQueryManagerAdapter(dpos *DPoS) core.QueryManager {
	return &QueryManagerAdapter{
		dpos: dpos,
	}
}

// GetCurrentEpochInfo 获取当前Epoch信息
func (q *QueryManagerAdapter) GetCurrentEpochInfo() map[string]interface{} {
	return q.dpos.GetCurrentEpochInfo()
}

// GetEpochInfoByNumber 获取指定Epoch信息
func (q *QueryManagerAdapter) GetEpochInfoByNumber(epochNumber uint64) map[string]interface{} {
	return q.dpos.GetEpochInfoByNumber(epochNumber)
}

// GetValidatorStats 获取验证者统计
func (q *QueryManagerAdapter) GetValidatorStats(validatorAddress types.Address, epochNumber uint64) map[string]interface{} {
	stats := make(map[string]interface{})
	
	if q.dpos.blockTracker != nil {
		blockCounts := q.dpos.blockTracker.GetEpochBlockCounts(epochNumber)
		totalBlocks := q.dpos.blockTracker.GetTotalEpochBlocks(epochNumber)
		
		stats["blocksProduced"] = blockCounts[validatorAddress]
		stats["totalBlocks"] = totalBlocks
	}
	
	return stats
}

