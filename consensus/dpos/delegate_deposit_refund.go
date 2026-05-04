package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/state"
)

// applyDelegateDepositRefunds moves native deposit from delegateDepositEscrowAddress back to
// the registrant after WithdrawDelegate and unfreeze lock (same conditions as RPC),
// using block header time for eligibility so execution is deterministic for a given block.
//
// Only registrations with DepositHeldInEscrow==true (To=托管地址的新注册) are refunded;
// legacy To=nil 路径需另行迁移进托管后再标记该字段。
func (d *DPoS) applyDelegateDepositRefunds(transition *state.Transition, blockTimestamp uint64, blockNumber uint64) error {
	if transition == nil {
		return nil
	}

	d.lock.Lock()
	defer d.lock.Unlock()

	if d.state == nil || d.state.RegistrationStore == nil {
		return nil
	}

	regs, err := d.state.RegistrationStore.GetAllRegistrations()
	if err != nil {
		return fmt.Errorf("delegate deposit refund: list registrations: %w", err)
	}

	escrow := d.getDelegateDepositEscrowAddress()
	txn := transition.Txn()

	for _, reg := range regs {
		if reg == nil {
			continue
		}
		if !reg.DepositHeldInEscrow || reg.DepositRefunded {
			continue
		}
		if reg.Status != RegStatusWithdrawn {
			continue
		}
		if reg.Deposit == nil || reg.Deposit.Sign() <= 0 {
			continue
		}
		if reg.UnfreezeAvailableAt == 0 || blockTimestamp < reg.UnfreezeAvailableAt {
			continue
		}

		amount := new(big.Int).Set(reg.Deposit)
		if err := txn.SubBalance(escrow, amount); err != nil {
			return fmt.Errorf("delegate deposit refund: SubBalance escrow=%s amount=%s delegate=%s block=%d: %w",
				escrow.String(), amount.String(), reg.Address.String(), blockNumber, err)
		}
		txn.AddBalance(reg.Address, amount)

		reg.DepositRefunded = true
		if err := d.state.RegistrationStore.SaveRegistration(reg); err != nil {
			return fmt.Errorf("delegate deposit refund: save registration %s: %w", reg.Address.String(), err)
		}

		if d.state.FreezeStore != nil {
			fi, ferr := d.state.FreezeStore.GetFreezeInfo(reg.Address)
			if ferr == nil && fi != nil {
				fi.FrozenAmount = big.NewInt(0)
				fi.Status = "withdrawn"
				_ = d.state.FreezeStore.SaveFreezeInfo(fi)
			}
		}

		d.logger.Info("✅ 候选人保证金已从托管退回注册地址",
			"block", blockNumber,
			"blockTs", blockTimestamp,
			"delegate", reg.Address.String(),
			"amountWei", amount.String(),
			"escrow", escrow.String())
	}

	return nil
}
