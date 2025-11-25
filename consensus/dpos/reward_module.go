package dpos

import (
	"fmt"
	"math/big"

	rewardmodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/reward"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// initRewardModule ensures the reward manager is backed by the shared module implementation.
func (d *DPoS) initRewardModule() {
	if d.reward != nil {
		return
	}

	deps := d.buildRewardDependencies()
	d.reward = rewardmodule.NewManager(deps)
}

// buildRewardDependencies wires the module dependencies to the legacy reward distributor.
func (d *DPoS) buildRewardDependencies() rewardmodule.Dependencies {
	deps := rewardmodule.Dependencies{
		CalculateRewards: func(validators validator.AccountSet, voters map[types.Address]interface{}, blockCounts map[types.Address]uint64, totalBlocks uint64) map[types.Address]*big.Int {
			typedVoters := revertVoterInterfaces(voters)
			return d.calculateRewardsWithDistributor(validators, typedVoters, blockCounts, totalBlocks)
		},
		DistributeRewards: func(epochNumber uint64, rewards map[types.Address]*big.Int, validators validator.AccountSet, voters map[types.Address]interface{}) error {
			typedVoters := revertVoterInterfaces(voters)
			return d.distributeRewardsWithDistributor(epochNumber, rewards, validators, typedVoters)
		},
		GetValidators: d.GetValidators,
		GetVoters: func() map[types.Address]interface{} {
			return convertVotersToInterfaces(d.GetVoters())
		},
		Logger: d.logger.Named("modules.reward"),
	}

	if d.blockTracker != nil {
		deps.GetEpochBlockCounts = func(epochNumber uint64) map[types.Address]uint64 {
			return d.blockTracker.GetEpochBlockCounts(epochNumber)
		}
		deps.GetTotalEpochBlocks = func(epochNumber uint64) uint64 {
			return d.blockTracker.GetTotalEpochBlocks(epochNumber)
		}
	}

	return deps
}

// calculateRewardsWithDistributor delegates reward calculation to the legacy distributor.
func (d *DPoS) calculateRewardsWithDistributor(
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
	blockCounts map[types.Address]uint64,
	totalBlocks uint64,
) map[types.Address]*big.Int {
	if d.rewardDistributor == nil {
		return map[types.Address]*big.Int{}
	}

	return d.rewardDistributor.CalculateRewards(validators, voters, blockCounts, totalBlocks)
}

// distributeRewardsWithDistributor uses the legacy distributor to apply rewards to state.
func (d *DPoS) distributeRewardsWithDistributor(
	epochNumber uint64,
	rewards map[types.Address]*big.Int,
	validators validator.AccountSet,
	voters map[types.Address]*VoterInfo,
) error {
	if d.rewardDistributor == nil {
		return fmt.Errorf("reward distributor not initialized")
	}

	// RewardDistributor.DistributeEpochRewards handles the actual balance updates and
	// relies on validators / voters directly, so we ignore the precomputed rewards map.
	return d.rewardDistributor.DistributeEpochRewards(epochNumber, validators, voters)
}

// convertVotersToInterfaces converts the strongly typed voter map for the module boundary.
func convertVotersToInterfaces(input map[types.Address]*VoterInfo) map[types.Address]interface{} {
	result := make(map[types.Address]interface{}, len(input))
	for addr, info := range input {
		result[addr] = info
	}
	return result
}

// revertVoterInterfaces converts the interface map back into the strongly typed structure.
func revertVoterInterfaces(input map[types.Address]interface{}) map[types.Address]*VoterInfo {
	result := make(map[types.Address]*VoterInfo, len(input))
	for addr, val := range input {
		if voter, ok := val.(*VoterInfo); ok && voter != nil {
			result[addr] = voter
		}
	}
	return result
}
