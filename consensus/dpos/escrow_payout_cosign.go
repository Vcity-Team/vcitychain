package dpos

import "github.com/Vcity-Team/vcitychain/types"

// EscrowPayoutCoAuthorizerAllowlist returns a copy of the hardcoded allowlist
// (same as delegateDepositMigrationAuthorityAddresses in escrow.go).
func EscrowPayoutCoAuthorizerAllowlist() []types.Address {
	return append([]types.Address(nil), delegateDepositMigrationAuthorityAddresses...)
}

// EscrowPayoutMultiRequiredCoSignerCount is simple majority over the allowlist: len/2 + 1.
// Returns 0 when the allowlist is empty (escrow payout RPC disabled).
func EscrowPayoutMultiRequiredCoSignerCount() int {
	n := len(delegateDepositMigrationAuthorityAddresses)
	if n == 0 {
		return 0
	}
	return n/2 + 1
}
