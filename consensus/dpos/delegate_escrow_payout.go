package dpos

import (
	"encoding/binary"

	"github.com/Vcity-Team/vcitychain/types"
)

// DPOS+PAY calldata recognition remains for historical tx decoding / vote exclusion.
// Privileged escrow payout apply + submit RPC were removed.

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

// IsDelegateDepositEscrowPayoutInput reports whether input matches DPOS+PAY escrow payout calldata.
func IsDelegateDepositEscrowPayoutInput(input []byte) bool {
	return isDelegateDepositEscrowPayoutCalldata(input)
}
