package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	statemodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/state"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

func (d *DPoS) initStateModule() {
	if d.stateMgr != nil {
		return
	}

	deps := d.buildStateModuleDependencies()
	d.stateMgr = statemodule.NewManager(deps)
}

func (d *DPoS) buildStateModuleDependencies() statemodule.Dependencies {
	return statemodule.Dependencies{
		Logger: d.logger.Named("state_module"),
		FetchAccount: func(address types.Address) (*core.AccountInfo, error) {
			if d.config == nil || d.config.Executor == nil || d.config.Blockchain == nil {
				return nil, fmt.Errorf("state executor not available")
			}

			currentHeader := d.config.Blockchain.Header()
			if currentHeader == nil {
				return nil, fmt.Errorf("current header not available")
			}

			snapshot, err := d.config.Executor.StateAt(currentHeader.StateRoot)
			if err != nil {
				return nil, fmt.Errorf("failed to create state snapshot: %w", err)
			}

			account, err := snapshot.GetAccount(address)
			if err != nil {
				return nil, fmt.Errorf("failed to get account: %w", err)
			}

			if account == nil {
				return nil, nil
			}

			codeHash := types.ZeroHash
			if len(account.CodeHash) > 0 {
				codeHash = types.BytesToHash(account.CodeHash)
			}

			balance := account.Balance
			if balance == nil {
				balance = big.NewInt(0)
			}

			return &core.AccountInfo{
				Address:  address,
				Balance:  balance,
				Nonce:    account.Nonce,
				CodeHash: codeHash,
			}, nil
		},
		PersistValidators: func(blockNumber uint64, validators validator.AccountSet) error {
			store, err := d.getStateStore()
			if err != nil {
				return err
			}
			// 🆕 计算epoch号
			blocksPerEpoch := d.getEpochSize()
			consensusSwitchHeight := d.config.ConsensusSwitchHeight
			var epochNumber uint64
			if blockNumber < consensusSwitchHeight {
				epochNumber = 1
			} else {
				dposBlockNumber := blockNumber - consensusSwitchHeight
				currentEpoch := (dposBlockNumber / blocksPerEpoch) + 1
				epochNumber = currentEpoch + 1 // 下一个epoch
			}
			return store.SaveEpochValidators(epochNumber, validators)
		},
		LoadValidators: func(filterZeroVotingPower bool) (validator.AccountSet, error) {
			store, err := d.getStateStore()
			if err != nil {
				return nil, err
			}
			return store.GetValidatorsWithFilter(filterZeroVotingPower)
		},
	}
}
