package snapshot

import (
	"errors"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
)

var (
	ErrIncorrectNonce = errors.New("incorrect nonce")
	nonceAuthVote     = types.BytesToNonce(0xffffffffffffffff)
	nonceDropVote     = types.BytesToNonce(0x0000000000000000)
)

// isAuthorize is a helper function to return the bool value from Nonce
func isAuthorize(
	nonce types.Nonce,
) (bool, error) {
	switch nonce {
	case nonceAuthVote:
		return true, nil
	case nonceDropVote:
		return false, nil
	default:
		return false, ErrIncorrectNonce
	}
}

// shouldProcessVote is a helper function to return
// the flag indicating whether vote should be processed or not
// based on vote action and validator set
func shouldProcessVote(
	validators validators.Validators,
	candidate types.Address,
	voteAction bool, // true => add, false => remove
) bool {
	// if vote action is...
	// true  => validator set expects not to have a candidate
	// false => validator set expects     to have a candidate
	return voteAction != validators.Includes(candidate)
}

// addsOrDelsCandidate is a helper function to add/remove candidate to/from validators
func addsOrDelsCandidate(
	validators validators.Validators,
	candidate validators.Validator,
	updateAction bool,
) error {
	if updateAction {
		return validators.Add(candidate)
	} else {
		return validators.Del(candidate.Addr())
	}
}
