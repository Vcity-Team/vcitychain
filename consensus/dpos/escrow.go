package dpos

import "github.com/Vcity-Team/vcitychain/types"

// stakeEscrowAddress is a system-owned address used to hold frozen voting/staking funds.
// Funds are moved here on vote (freeze) and returned to the voter on unvote (unfreeze).
//
// NOTE: This address must not be a user-controlled key. It is used only for protocol-level
// balance moves during block execution (no signatures).
var stakeEscrowAddress = types.StringToAddress("0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfD")

func (d *DPoS) getStakeEscrowAddress() types.Address {
	return stakeEscrowAddress
}

