package dpos

import (
    "math/big"

    "github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
    "github.com/Vcity-Team/vcitychain/helper/common"
    "github.com/Vcity-Team/vcitychain/types"
)

// RewardsConfig holds configuration for reward token distribution on the bridge contracts.
type RewardsConfig struct {
    TokenAddress  types.Address
    WalletAddress types.Address
    WalletAmount  *big.Int
}

// BridgeConfig represents configuration required for bridge-related smart contracts.
type BridgeConfig struct {
    StateSenderAddr                   types.Address
    CheckpointManagerAddr             types.Address
    ExitHelperAddr                    types.Address
    RootERC20PredicateAddr            types.Address
    ChildMintableERC20PredicateAddr   types.Address
    RootNativeERC20Addr               types.Address
    ChildERC20Addr                    types.Address
    RootERC721PredicateAddr           types.Address
    ChildMintableERC721PredicateAddr  types.Address
    ChildERC721Addr                   types.Address
    RootERC1155PredicateAddr          types.Address
    ChildMintableERC1155PredicateAddr types.Address
    ChildERC1155Addr                  types.Address
    CustomSupernetManagerAddr         types.Address
    StakeManagerAddr                  types.Address
    JSONRPCEndpoint                   string
    EventTrackerStartBlocks           map[types.Address]uint64
}

// PolyBFTConfig aggregates the configuration required by the PolyBFT consensus runtime
// when running in the DPoS module.
type PolyBFTConfig struct {
    InitialValidatorSet []*validator.GenesisValidator
    Bridge              *BridgeConfig
    RewardConfig        *RewardsConfig

    EpochSize           uint64
    SprintSize          uint64
    EpochReward         uint64
    MaxValidatorSetSize uint64
    BlockTimeDrift      uint64

    BlockTime               common.Duration
    BlockTrackerPollInterval common.Duration
}

// IsBridgeEnabled returns true when bridge related configuration is provided and enabled.
func (c *PolyBFTConfig) IsBridgeEnabled() bool {
    if c == nil {
        return false
    }

    if c.Bridge == nil {
        return false
    }

    // Having a JSON RPC endpoint or state sender configured is enough to consider bridge enabled
    if c.Bridge.JSONRPCEndpoint != "" {
        return true
    }

    // If either state sender or checkpoint manager address is set, assume bridge functionality is desired.
    if c.Bridge.StateSenderAddr != (types.Address{}) {
        return true
    }

    if c.Bridge.CheckpointManagerAddr != (types.Address{}) {
        return true
    }

    return false
}

