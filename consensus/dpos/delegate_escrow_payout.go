package dpos

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
)

// 托管保证金地址（DPOS+REG 收款）向若干地址固定金额转出：与 MIG 对齐 8 字节头 — "DPOS" + "PAY" + 0x00，
// 随后 uint32(大端) 收款地址数量、uint256 每笔 wei（32 字节大端）、再跟 count×20 字节地址。
// 仅当 tx.From 属于 delegateDepositMigrationAuthorityAddresses 时执行（与迁移同一特权集合）。
//
// DelegateDepositEscrowPayoutMaxRecipients caps recipients per tx (gas / DoS).
const (
	DelegateDepositEscrowPayoutMaxRecipients = DelegateDepositMigrationMaxAddresses
	delegateDepositEscrowPayoutHeader        = 8 // "DPOS"(4) + "PAY"(3) + 0x00(1)
	delegateDepositEscrowPayoutAmountOff     = delegateDepositEscrowPayoutHeader + 4
	delegateDepositEscrowPayoutAddrOff       = delegateDepositEscrowPayoutAmountOff + 32
)

func isDelegateDepositEscrowPayoutCalldata(input []byte) bool {
	min := delegateDepositEscrowPayoutAddrOff + types.AddressLength
	if len(input) < min {
		return false
	}
	if string(input[:4]) != "DPOS" || string(input[4:7]) != "PAY" {
		return false
	}
	if input[7] != 0 {
		return false
	}
	n := binary.BigEndian.Uint32(input[8:12])
	if n == 0 || n > DelegateDepositEscrowPayoutMaxRecipients {
		return false
	}
	want := delegateDepositEscrowPayoutAddrOff + int(n)*types.AddressLength
	return len(input) == want
}

// IsDelegateDepositEscrowPayoutInput reports whether input matches DPOS+PAY escrow payout calldata (RPC / parsers).
func IsDelegateDepositEscrowPayoutInput(input []byte) bool {
	return isDelegateDepositEscrowPayoutCalldata(input)
}

// ParseDelegateDepositEscrowPayout decodes DPOS+PAY calldata.
func ParseDelegateDepositEscrowPayout(input []byte) (recipients []types.Address, amountWei *big.Int, err error) {
	if !isDelegateDepositEscrowPayoutCalldata(input) {
		return nil, nil, fmt.Errorf("invalid DPOS escrow payout calldata")
	}
	n := binary.BigEndian.Uint32(input[8:12])
	amountWei = new(big.Int).SetBytes(input[delegateDepositEscrowPayoutAmountOff:delegateDepositEscrowPayoutAddrOff])
	if amountWei.Sign() <= 0 {
		return nil, nil, fmt.Errorf("escrow payout amount must be positive")
	}
	off := delegateDepositEscrowPayoutAddrOff
	recipients = make([]types.Address, 0, n)
	for i := uint32(0); i < n; i++ {
		if off+types.AddressLength > len(input) {
			return nil, nil, fmt.Errorf("escrow payout calldata truncated at recipient %d", i)
		}
		recipients = append(recipients, types.BytesToAddress(input[off:off+types.AddressLength]))
		off += types.AddressLength
	}
	return recipients, amountWei, nil
}

// BuildDelegateDepositEscrowPayoutCalldata encodes DPOS+PAY wire format.
func BuildDelegateDepositEscrowPayoutCalldata(recipients []types.Address, amountWei *big.Int) ([]byte, error) {
	n := len(recipients)
	if n == 0 || n > DelegateDepositEscrowPayoutMaxRecipients {
		return nil, fmt.Errorf("escrow payout: recipient count must be 1..%d", DelegateDepositEscrowPayoutMaxRecipients)
	}
	if amountWei == nil || amountWei.Sign() <= 0 {
		return nil, fmt.Errorf("escrow payout: amountWei must be positive")
	}
	if amountWei.BitLen() > 256 {
		return nil, fmt.Errorf("escrow payout: amountWei exceeds 256 bits")
	}
	out := make([]byte, 0, delegateDepositEscrowPayoutAddrOff+n*types.AddressLength)
	out = append(out, []byte("DPOS")...)
	out = append(out, []byte("PAY")...)
	out = append(out, 0)
	var nb [4]byte
	binary.BigEndian.PutUint32(nb[:], uint32(n))
	out = append(out, nb[:]...)
	amt := amountWei.Bytes()
	pad := make([]byte, 32)
	copy(pad[32-len(amt):], amt)
	out = append(out, pad...)
	for _, a := range recipients {
		out = append(out, a.Bytes()...)
	}
	return out, nil
}

// ApplyDelegateDepositEscrowPayoutAfterTx runs after transition.Write(tx): privileged payout from delegate deposit escrow.
func (d *DPoS) ApplyDelegateDepositEscrowPayoutAfterTx(transition *state.Transition, tx *types.Transaction, blockNumber uint64) error {
	if transition == nil || tx == nil {
		return nil
	}
	if len(delegateDepositMigrationAuthorityAddresses) == 0 {
		return nil
	}
	if tx.From == (types.Address{}) || !IsDelegateDepositMigrationAuthority(tx.From) {
		return nil
	}
	if !isDelegateDepositEscrowPayoutCalldata(tx.Input) {
		return nil
	}
	recipients, amountWei, err := ParseDelegateDepositEscrowPayout(tx.Input)
	if err != nil {
		return fmt.Errorf("delegate deposit escrow payout: %w", err)
	}
	return d.applyDelegateDepositEscrowPayout(transition, recipients, amountWei, blockNumber, tx.Hash.String())
}

func (d *DPoS) applyDelegateDepositEscrowPayout(transition *state.Transition, recipients []types.Address, amountWei *big.Int, blockNumber uint64, txHash string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	txn := transition.Txn()
	escrow := d.getDelegateDepositEscrowAddress()
	stakeEsc := d.getStakeEscrowAddress()
	zero := types.Address{}

	payCount := 0
	for _, to := range recipients {
		if to == zero || to == escrow || to == stakeEsc {
			continue
		}
		payCount++
	}
	if payCount == 0 {
		return fmt.Errorf("delegate deposit escrow payout: no valid recipient addresses (tx=%s)", txHash)
	}

	n := big.NewInt(int64(payCount))
	total := new(big.Int).Mul(amountWei, n)

	ebal := txn.GetBalance(escrow)
	if ebal == nil || ebal.Cmp(total) < 0 {
		return fmt.Errorf("delegate deposit escrow payout: insufficient escrow balance have=%v need=%s block=%d tx=%s",
			ebal, total.String(), blockNumber, txHash)
	}

	for _, to := range recipients {
		if to == zero {
			d.logger.Warn("⚠️ 跳过托管转出（零地址）", "tx", txHash)
			continue
		}
		if to == escrow || to == stakeEsc {
			d.logger.Warn("⚠️ 跳过托管转出（系统托管地址）", "addr", to.String(), "tx", txHash)
			continue
		}
		if err := txn.SubBalance(escrow, amountWei); err != nil {
			return fmt.Errorf("delegate deposit escrow payout: SubBalance escrow=%s amount=%s block=%d tx=%s: %w",
				escrow.String(), amountWei.String(), blockNumber, txHash, err)
		}
		txn.AddBalance(to, amountWei)
		d.logger.Info("✅ 候选人托管保证金已转出",
			"block", blockNumber, "tx", txHash, "to", to.String(), "amountWei", amountWei.String(), "escrow", escrow.String())
	}
	return nil
}
