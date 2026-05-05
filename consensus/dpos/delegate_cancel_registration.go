package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
)

// 链上取消注册：与 REG 一样走交易（本块内 transition.Write 之后由共识层完成托管扣款与余额增加），
// 不在此处校验冻结期 / FreezeStore；仅校验 Bolt 注册与投票等安全条件。
//
// Wire: "DPOS"(4) + "CAN"(3) + 0x00(1) + delegate(20)。
// 交易形态：To = 候选人托管地址，Value = 0，From = calldata 中的 delegate。

const delegateCancelRegistrationWireSize = 28 // 8 + 20

// BuildDelegateCancelRegistrationCalldata 构造 DPOS+CAN 取消注册 calldata。
func BuildDelegateCancelRegistrationCalldata(delegate types.Address) []byte {
	out := make([]byte, 0, delegateCancelRegistrationWireSize)
	out = append(out, []byte("DPOS")...)
	out = append(out, []byte("CAN")...)
	out = append(out, 0)
	out = append(out, delegate.Bytes()...)
	return out
}

func isDelegateCancelRegistrationCalldata(input []byte) bool {
	if len(input) < delegateCancelRegistrationWireSize {
		return false
	}
	if string(input[:4]) != "DPOS" || string(input[4:7]) != "CAN" || input[7] != 0 {
		return false
	}
	return true
}

func parseDelegateCancelRegistrationAddress(input []byte) (types.Address, error) {
	if !isDelegateCancelRegistrationCalldata(input) {
		return types.Address{}, fmt.Errorf("invalid DPOS cancel registration calldata")
	}
	return types.BytesToAddress(input[8:28]), nil
}

func (d *DPoS) isDelegateCancelRegistrationTransaction(tx *types.Transaction) bool {
	if tx == nil || len(tx.Input) < delegateCancelRegistrationWireSize {
		return false
	}
	if !isDelegateCancelRegistrationCalldata(tx.Input) {
		return false
	}
	escrow := d.getDelegateDepositEscrowAddress()
	if tx.To == nil || *tx.To != escrow {
		return false
	}
	if tx.Value != nil && tx.Value.Sign() != 0 {
		return false
	}
	return true
}

// ApplyDelegateCancelRegistrationAfterTx 在 transition.Write(tx) 成功后调用：托管 SubBalance + 注册者 AddBalance，并更新 RegistrationStore。
func (d *DPoS) ApplyDelegateCancelRegistrationAfterTx(
	transition *state.Transition,
	tx *types.Transaction,
	blockNumber uint64,
) error {
	if transition == nil || tx == nil {
		return nil
	}
	if !d.isDelegateCancelRegistrationTransaction(tx) {
		return nil
	}

	delegate, err := parseDelegateCancelRegistrationAddress(tx.Input)
	if err != nil {
		return err
	}
	if tx.From != (types.Address{}) && tx.From != delegate {
		return fmt.Errorf("delegate cancel: sender %s must match calldata delegate %s",
			tx.From.String(), delegate.String())
	}
	if d.isGenesisValidator(delegate) {
		return fmt.Errorf("delegate cancel: genesis validators cannot cancel via DPOS+CAN")
	}

	d.lock.Lock()
	defer d.lock.Unlock()

	if d.state == nil || d.state.RegistrationStore == nil {
		return fmt.Errorf("delegate cancel: registration store unavailable")
	}

	reg, err := d.state.RegistrationStore.GetRegistration(delegate)
	if err != nil {
		return fmt.Errorf("delegate cancel: get registration: %w", err)
	}
	if reg == nil {
		return fmt.Errorf("delegate cancel: delegate %s not registered", delegate.String())
	}
	// RPC WithdrawDelegate 只把 Bolt 标成 Withdrawn，链上托管款要靠本交易退回。
	if reg.Status == RegStatusWithdrawn {
		if reg.DepositRefunded {
			return nil
		}
		if !reg.DepositHeldInEscrow {
			return fmt.Errorf("delegate cancel: withdrawn without escrow-held deposit; cannot refund via CAN for %s",
				delegate.String())
		}
		// 已走 RPC 退出时仍要求无投票（与 WithdrawDelegate 前置一致）
		if reg.TotalVotes != nil && reg.TotalVotes.Sign() > 0 {
			return fmt.Errorf("delegate cancel: delegate %s still has votes", delegate.String())
		}
	} else {
		if !reg.DepositHeldInEscrow {
			return fmt.Errorf("delegate cancel: only escrow (To=escrow) registrations supported; delegate %s",
				delegate.String())
		}
		if reg.TotalVotes != nil && reg.TotalVotes.Sign() > 0 {
			return fmt.Errorf("delegate cancel: delegate %s still has votes", delegate.String())
		}
	}

	if reg.Deposit == nil || reg.Deposit.Sign() <= 0 {
		return fmt.Errorf("delegate cancel: zero or missing deposit in registration")
	}
	amount := new(big.Int).Set(reg.Deposit)

	escrow := d.getDelegateDepositEscrowAddress()
	escrowBal := transition.GetBalance(escrow)
	if escrowBal.Cmp(amount) < 0 {
		return fmt.Errorf("delegate cancel: escrow balance insufficient (have %s need %s)",
			escrowBal.String(), amount.String())
	}

	txn := transition.Txn()
	if err := txn.SubBalance(escrow, amount); err != nil {
		return fmt.Errorf("delegate cancel: SubBalance escrow: %w", err)
	}
	txn.AddBalance(delegate, amount)

	reg.Status = RegStatusWithdrawn
	reg.IsActive = false
	reg.UnfreezeAt = 0
	reg.UnfreezeAvailableAt = 0
	reg.DepositRefunded = true

	if err := d.state.RegistrationStore.SaveRegistration(reg); err != nil {
		return fmt.Errorf("delegate cancel: save registration: %w", err)
	}

	for i, del := range d.delegates {
		if del.Address == delegate {
			d.delegates = append(d.delegates[:i], d.delegates[i+1:]...)
			break
		}
	}

	// 与历史「候选人保证金已从托管退回」同一语义，便于 grep；仅在本交易成功完成 trie 退款时出现。
	d.logger.Info("✅ 候选人保证金已从托管退回注册地址（链上取消注册 DPOS+CAN）",
		"block", blockNumber,
		"delegate", delegate.String(),
		"amountWei", amount.String(),
		"escrow", escrow.String(),
		"txHash", tx.Hash.String())

	return nil
}
