package fork

import (
	"errors"

	"github.com/Vcity-Team/vcitychain/consensus/ibft/hook"
	"github.com/Vcity-Team/vcitychain/contracts/staking"
	"github.com/Vcity-Team/vcitychain/helper/hex"
	stakingHelper "github.com/Vcity-Team/vcitychain/helper/staking"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
	"github.com/Vcity-Team/vcitychain/validators/store"
)

var (
	ErrTxInLastEpochOfBlock = errors.New("block must not have transactions in the last of epoch")
)

// HeaderModifier is an interface for the struct that modifies block header for additional process
type HeaderModifier interface {
	ModifyHeader(*types.Header, types.Address) error
	VerifyHeader(*types.Header) error
	ProcessHeader(*types.Header) error
}

// registerHeaderModifierHooks registers hooks to modify header by validator store
func registerHeaderModifierHooks(
	hooks *hook.Hooks,
	validatorStore store.ValidatorStore,
) {
	if modifier, ok := validatorStore.(HeaderModifier); ok {
		hooks.ModifyHeaderFunc = modifier.ModifyHeader
		hooks.VerifyHeaderFunc = modifier.VerifyHeader
		hooks.ProcessHeaderFunc = modifier.ProcessHeader
	}
}

// Updatable is an interface for the struct that updates validators in the middle
type Updatable interface {
	// UpdateValidatorSet updates validators forcibly
	// in order that new validators are available from the given height
	UpdateValidatorSet(validators.Validators, uint64) error
}

// registerUpdateValidatorsHooks registers hooks to update validators in the middle
func registerUpdateValidatorsHooks(
	hooks *hook.Hooks,
	validatorStore store.ValidatorStore,
	validators validators.Validators,
	fromHeight uint64,
) {
	if us, ok := validatorStore.(Updatable); ok {
		hooks.PostInsertBlockFunc = func(b *types.Block) error {
			if fromHeight != b.Number()+1 {
				return nil
			}

			// update validators if the block height is the one before beginning height
			return us.UpdateValidatorSet(validators, fromHeight)
		}
	}
}

// registerPoSVerificationHooks registers that hooks to prevent the last epoch block from having transactions
func registerTxInclusionGuardHooks(hooks *hook.Hooks, epochSize uint64) {
	isLastEpoch := func(height uint64) bool {
		return height > 0 && height%epochSize == 0
	}

	hooks.ShouldWriteTransactionFunc = func(height uint64) bool {
		return !isLastEpoch(height)
	}

	hooks.VerifyBlockFunc = func(block *types.Block) error {
		if isLastEpoch(block.Number()) && len(block.Transactions) > 0 {
			return ErrTxInLastEpochOfBlock
		}

		return nil
	}
}

// registerStakingContractDeploymentHooks registers hooks
// to deploy or update staking contract
func registerStakingContractDeploymentHooks(
	hooks *hook.Hooks,
	fork *IBFTFork,
) {
	hooks.PreCommitStateFunc = func(header *types.Header, txn *state.Transition) error {
		// safe check
		if header.Number != fork.Deployment.Value {
			return nil
		}

		if txn.AccountExists(staking.AddrStakingContract) && !fork.Redeployment {
			// update bytecode of deployed contract
			codeBytes, err := hex.DecodeHex(stakingHelper.StakingSCBytecode)
			if err != nil {
				return err
			}

			return txn.SetCodeDirectly(staking.AddrStakingContract, codeBytes)
		} else {
			// deploy contract
			contractState, err := stakingHelper.PredeployStakingSC(
				fork.Validators,
				getPreDeployParams(fork),
			)

			if err != nil {
				return err
			}

			return txn.SetAccountDirectly(staking.AddrStakingContract, contractState)
		}
	}
}

// getPreDeployParams returns PredeployParams for Staking Contract from IBFTFork
func getPreDeployParams(fork *IBFTFork) stakingHelper.PredeployParams {
	params := stakingHelper.PredeployParams{
		MaxValidatorCount: stakingHelper.MaxValidatorCount,
	}

	if fork.MinValidatorCount != nil {
		params.EpochSize = fork.EpochSize
	}

	if fork.MaxValidatorCount != nil {
		params.MaxValidatorCount = fork.MaxValidatorCount.Value
	}

	return params
}
