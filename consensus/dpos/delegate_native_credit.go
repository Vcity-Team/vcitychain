package dpos

import (
	"encoding/binary"

	"github.com/Vcity-Team/vcitychain/types"
)

// DPOS+CRE calldata recognition remains for historical tx decoding / vote exclusion.
// Privileged native credit apply + submit RPC were removed.

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
