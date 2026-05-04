package dpos

import "github.com/Vcity-Team/vcitychain/types"

// stakeEscrowAddress is a system-owned address used to hold frozen voting/staking funds.
// Funds are moved here on vote (freeze) and returned to the voter on unvote (unfreeze).
//
// NOTE: This address must not be a user-controlled key. It is used only for protocol-level
// balance moves during block execution (no signatures).
var stakeEscrowAddress = types.StringToAddress("0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfD")

// delegateDepositEscrowAddress holds SR/candidate registration deposits (native token).
// Registrations use tx.To = this address (Value = deposit). Refunds use Transition SubBalance/AddBalance.
//
// NOTE: Not a user-controlled key; no private key.
var delegateDepositEscrowAddress = types.StringToAddress("0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfC")

func (d *DPoS) getStakeEscrowAddress() types.Address {
	return stakeEscrowAddress
}

func (d *DPoS) getDelegateDepositEscrowAddress() types.Address {
	return delegateDepositEscrowAddress
}

// delegateDepositMigrationAuthorityAddress is the ONLY tx.From allowed to trigger
// DPOSMIG deposit migration (sweep listed contract/native accounts into delegateDepositEscrowAddress).
// Fixed project EOA; must match the account used to broadcast migration txs (e.g. MetaMask + hex data).
var delegateDepositMigrationAuthorityAddress = types.StringToAddress("0xc3035426C12cf7674A2AaA9C21Cd3529732447A4")

func (d *DPoS) getDelegateDepositMigrationAuthorityAddress() types.Address {
	return delegateDepositMigrationAuthorityAddress
}

// DelegateDepositMigrationAuthorityEOA is the only tx.From allowed for DPOS+MIG deposit sweep transactions.
func DelegateDepositMigrationAuthorityEOA() types.Address {
	return delegateDepositMigrationAuthorityAddress
}

