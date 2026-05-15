package dpos

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	bolt "go.etcd.io/bbolt"
)

// setBlockExecPersistSideEffects toggles whether block execution may write Bolt side effects.
func (d *DPoS) setBlockExecPersistSideEffects(persist bool) {
	d.blockExecPersistSideEffects = persist
}

func (d *DPoS) persistBlockSideEffects() bool {
	return d.blockExecPersistSideEffects
}

// OnRewindToHeight implements blockchain.StateHealer.
func (d *DPoS) OnRewindToHeight(targetHeight uint64) error {
	if d == nil || d.dataDir == "" {
		return nil
	}
	return rollbackDPoSMetadataAboveHeight(d.dataDir, targetHeight, d.logger)
}

// PrepareSameHeightForkReplay implements blockchain.StateHealer.
// Resets Bolt rows that a replaced same-height block (e.g. wrong-fork DPOS+CAN) may have applied.
func (d *DPoS) PrepareSameHeightForkReplay(replacedBlock *types.Block) error {
	if d == nil || replacedBlock == nil || d.state == nil || d.state.RegistrationStore == nil {
		return nil
	}
	for _, tx := range replacedBlock.Transactions {
		if tx == nil || !d.isDelegateCancelRegistrationTransaction(tx) {
			continue
		}
		delegate, err := parseDelegateCancelRegistrationAddress(tx.Input)
		if err != nil {
			continue
		}
		reg, err := d.state.RegistrationStore.GetRegistration(delegate)
		if err != nil || reg == nil {
			continue
		}
		if !reg.DepositRefunded {
			continue
		}
		reg.DepositRefunded = false
		if err := d.state.RegistrationStore.SaveRegistration(reg); err != nil {
			return fmt.Errorf("prepare fork replay: reset registration for %s: %w", delegate.String(), err)
		}
		d.logger.Info("🔄 heal: reset delegate registration after same-height fork",
			"delegate", delegate.String(),
			"blockNumber", replacedBlock.Number(),
		)
	}
	return nil
}

func rollbackDPoSMetadataAboveHeight(dataDir string, targetHeight uint64, logger hclog.Logger) error {
	dposDBPath := filepath.Join(dataDir, "consensus", "dpos", "dpos.db")
	db, err := bolt.Open(dposDBPath, 0666, nil)
	if err != nil {
		return fmt.Errorf("open dpos db: %w", err)
	}
	defer db.Close()

	return db.Update(func(tx *bolt.Tx) error {
		if b := tx.Bucket([]byte("VotingPowerAtBlock")); b != nil {
			c := b.Cursor()
			for k, _ := c.First(); k != nil; k, _ = c.Next() {
				if len(k) < 8 {
					continue
				}
				h := binary.BigEndian.Uint64(k)
				if h > targetHeight {
					if err := c.Delete(); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
