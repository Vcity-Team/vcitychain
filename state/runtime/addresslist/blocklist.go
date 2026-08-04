package addresslist

import (
	"errors"

	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/types"
)

// ErrAccountBlacklisted is returned when a transaction involves a blocklisted account.
var ErrAccountBlacklisted = errors.New("account is blacklisted")

// RoleGetter resolves the ACL role for an address.
type RoleGetter func(addr types.Address) Role

// CheckBlockedTx reports whether a transaction should be rejected by the
// transactions block list.
//
// Rules:
//   - SystemCaller is always exempt
//   - Calls targeting the block-list contract itself are exempt (so Admin can always setNone)
//   - EnabledRole on From or To rejects the transaction
//   - Contract creation (to == nil) only checks From
func CheckBlockedTx(getRole RoleGetter, from types.Address, to *types.Address) error {
	if from == contracts.SystemCaller {
		return nil
	}

	if to != nil && *to == contracts.BlockListTransactionsAddr {
		return nil
	}

	if getRole(from) == EnabledRole {
		return ErrAccountBlacklisted
	}

	if to != nil && getRole(*to) == EnabledRole {
		return ErrAccountBlacklisted
	}

	return nil
}
