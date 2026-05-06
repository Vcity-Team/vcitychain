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

// delegateDepositMigrationAuthorityAddresses lists every tx.From allowed to trigger
// DPOS+MIG, DPOS+PAY escrow payout, and DPOS+CRE native credit (same allowlist as
// dpos_submitDelegateDepositEscrowPayoutMulti / dpos_submitDelegateNativeCreditMulti coSigners).
// Fixed project EOAs; each must match the key used when broadcasting from that address.
var delegateDepositMigrationAuthorityAddresses = []types.Address{
	types.StringToAddress("0xc3035426c12cf7674a2aaa9c21cd3529732447a4"),
	types.StringToAddress("0x1b05c37cf8596f4caa840b129c946bc656b6fa7e"),
	types.StringToAddress("0x86dec1bf83139c4d6875fb2dee6698199711143d"),
	types.StringToAddress("0x699c84dc11204e3018fecacef5d29aaec90adbd6"),
	types.StringToAddress("0x687c64124001aac96f130aa1a083b030b2357c24"),
	types.StringToAddress("0x33a9db457e38dbd5ed2e975e338951bc10ceca33"),
	types.StringToAddress("0x7f338cbca61df3d29b07213e8e06f054aff4cca3"),
}

// IsDelegateDepositMigrationAuthority reports whether addr may send DPOS+MIG / DPOS+PAY txs.
func IsDelegateDepositMigrationAuthority(addr types.Address) bool {
	if addr == (types.Address{}) {
		return false
	}
	for _, a := range delegateDepositMigrationAuthorityAddresses {
		if a == addr {
			return true
		}
	}
	return false
}

// DelegateDepositMigrationAuthorityEOA returns the first entry (legacy callers / display).
// Signing must use the private key for one of the addresses in delegateDepositMigrationAuthorityAddresses.
func DelegateDepositMigrationAuthorityEOA() types.Address {
	return getDelegateDepositMigrationAuthorityPrimary()
}

func getDelegateDepositMigrationAuthorityPrimary() types.Address {
	if len(delegateDepositMigrationAuthorityAddresses) == 0 {
		return types.Address{}
	}
	return delegateDepositMigrationAuthorityAddresses[0]
}

// DelegateDepositMigrationAuthorityAddresses returns a copy of the migration / co-sign allowlist.
func DelegateDepositMigrationAuthorityAddresses() []types.Address {
	return append([]types.Address(nil), delegateDepositMigrationAuthorityAddresses...)
}

// DelegateDepositEscrowAddr is the fixed registration deposit escrow (DPOS+REG / DPOS+CAN tx To).
func DelegateDepositEscrowAddr() types.Address {
	return delegateDepositEscrowAddress
}
