package dpos

import (
	"encoding/binary"
)

// DPOS+MIG calldata recognition remains for historical tx decoding / vote exclusion.
// Privileged migration apply + submit RPC were removed.

const (
	DelegateDepositMigrationMaxAddresses = 64
	delegateDepositMigrationHeader       = 8 // "DPOS"(4) + "MIG"(3) + 0x00(1)
)

// IsDelegateDepositMigrationInput reports whether input matches DPOS+MIG migration calldata.
func IsDelegateDepositMigrationInput(input []byte) bool {
	return isDelegateDepositMigrationCalldata(input)
}

func isDelegateDepositMigrationCalldata(input []byte) bool {
	if len(input) < delegateDepositMigrationHeader+4 {
		return false
	}
	if string(input[:4]) != "DPOS" || string(input[4:7]) != "MIG" {
		return false
	}
	if input[7] != 0 {
		return false
	}
	n := binary.BigEndian.Uint32(input[8:12])
	if n == 0 || n > DelegateDepositMigrationMaxAddresses {
		return false
	}
	legacyLen := delegateDepositMigrationHeader + 4 + int(n)*20
	extLen := delegateDepositMigrationHeader + 4 + int(n)*40
	return len(input) == legacyLen || len(input) == extLen
}
