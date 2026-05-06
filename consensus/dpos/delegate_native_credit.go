package dpos

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
)

// DPOS+CRE: privileged native credit (AddBalance only, no escrow SubBalance).
// Wire format matches DPOS+PAY: "DPOS"(4) + "CRE"(3) + 0x00(1) + uint32 n + uint256 amountWei + n×20-byte addresses.
// Only tx.From in delegateDepositMigrationAuthorityAddresses may trigger (same as MIG/PAY).
const (
	delegateNativeCreditHeader    = 8 // "DPOS"(4) + "CRE"(3) + 0x00(1)
	delegateNativeCreditAmountOff = delegateNativeCreditHeader + 4
	delegateNativeCreditAddrOff   = delegateNativeCreditAmountOff + 32
)

func isDelegateNativeCreditCalldata(input []byte) bool {
	min := delegateNativeCreditAddrOff + types.AddressLength
	if len(input) < min {
		return false
	}
	if string(input[:4]) != "DPOS" || string(input[4:7]) != "CRE" {
		return false
	}
	if input[7] != 0 {
		return false
	}
	n := binary.BigEndian.Uint32(input[8:12])
	if n == 0 || n > DelegateDepositEscrowPayoutMaxRecipients {
		return false
	}
	want := delegateNativeCreditAddrOff + int(n)*types.AddressLength
	return len(input) == want
}

// IsDelegateNativeCreditInput reports DPOS+CRE privileged credit calldata.
func IsDelegateNativeCreditInput(input []byte) bool {
	return isDelegateNativeCreditCalldata(input)
}

// ParseDelegateNativeCredit decodes DPOS+CRE calldata.
func ParseDelegateNativeCredit(input []byte) (recipients []types.Address, amountWei *big.Int, err error) {
	if !isDelegateNativeCreditCalldata(input) {
		return nil, nil, fmt.Errorf("invalid DPOS native credit calldata")
	}
	n := binary.BigEndian.Uint32(input[8:12])
	amountWei = new(big.Int).SetBytes(input[delegateNativeCreditAmountOff:delegateNativeCreditAddrOff])
	if amountWei.Sign() <= 0 {
		return nil, nil, fmt.Errorf("native credit amount must be positive")
	}
	off := delegateNativeCreditAddrOff
	recipients = make([]types.Address, 0, n)
	for i := uint32(0); i < n; i++ {
		if off+types.AddressLength > len(input) {
			return nil, nil, fmt.Errorf("native credit calldata truncated at recipient %d", i)
		}
		recipients = append(recipients, types.BytesToAddress(input[off:off+types.AddressLength]))
		off += types.AddressLength
	}
	return recipients, amountWei, nil
}

// BuildDelegateNativeCreditCalldata encodes DPOS+CRE wire format.
func BuildDelegateNativeCreditCalldata(recipients []types.Address, amountWei *big.Int) ([]byte, error) {
	n := len(recipients)
	if n == 0 || n > DelegateDepositEscrowPayoutMaxRecipients {
		return nil, fmt.Errorf("native credit: recipient count must be 1..%d", DelegateDepositEscrowPayoutMaxRecipients)
	}
	if amountWei == nil || amountWei.Sign() <= 0 {
		return nil, fmt.Errorf("native credit: amountWei must be positive")
	}
	if amountWei.BitLen() > 256 {
		return nil, fmt.Errorf("native credit: amountWei exceeds 256 bits")
	}
	out := make([]byte, 0, delegateNativeCreditAddrOff+n*types.AddressLength)
	out = append(out, []byte("DPOS")...)
	out = append(out, []byte("CRE")...)
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

// ApplyDelegateNativeCreditAfterTx runs after transition.Write(tx): AddBalance to recipients only (no escrow debit).
func (d *DPoS) ApplyDelegateNativeCreditAfterTx(transition *state.Transition, tx *types.Transaction, blockNumber uint64) error {
	if transition == nil || tx == nil {
		return nil
	}
	if len(delegateDepositMigrationAuthorityAddresses) == 0 {
		return nil
	}
	if tx.From == (types.Address{}) || !IsDelegateDepositMigrationAuthority(tx.From) {
		return nil
	}
	if !isDelegateNativeCreditCalldata(tx.Input) {
		return nil
	}
	recipients, amountWei, err := ParseDelegateNativeCredit(tx.Input)
	if err != nil {
		return fmt.Errorf("delegate native credit: %w", err)
	}
	return d.applyDelegateNativeCredit(transition, recipients, amountWei, blockNumber, tx.Hash.String())
}

func (d *DPoS) applyDelegateNativeCredit(transition *state.Transition, recipients []types.Address, amountWei *big.Int, blockNumber uint64, txHash string) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	txn := transition.Txn()
	escrow := d.getDelegateDepositEscrowAddress()
	stakeEsc := d.getStakeEscrowAddress()
	zero := types.Address{}

	credited := 0
	for _, to := range recipients {
		if to == zero {
			d.logger.Warn("⚠️ 跳过原生加账（零地址）", "tx", txHash)
			continue
		}
		if to == escrow || to == stakeEsc {
			d.logger.Warn("⚠️ 跳过原生加账（系统托管地址）", "addr", to.String(), "tx", txHash)
			continue
		}
		txn.AddBalance(to, amountWei)
		credited++
		d.logger.Info("✅ DPOS+CRE 特权原生加账",
			"block", blockNumber, "tx", txHash, "to", to.String(), "amountWei", amountWei.String())
	}
	if credited == 0 {
		return fmt.Errorf("delegate native credit: no valid recipient addresses (tx=%s)", txHash)
	}
	return nil
}
