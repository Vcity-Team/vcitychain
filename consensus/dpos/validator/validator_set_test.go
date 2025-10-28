package validator

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/require"
)

func TestValidatorSet_HasQuorum(t *testing.T) {
	t.Parallel()

	// enough signers for quorum (2/3 super-majority of validators are signers)
	validators := NewTestValidatorsWithAliases(t, []string{"A", "B", "C", "D", "E", "F", "G"})
	vs := validators.ToValidatorSet()

	signers := make(map[types.Address]struct{})

	validators.IterAcct([]string{"A", "B", "C", "D", "E"}, func(v *TestValidator) {
		signers[v.Address()] = struct{}{}
	})

	require.True(t, vs.HasQuorum(1, signers))

	// not enough signers for quorum (less than 2/3 super-majority of validators are signers)
	signers = make(map[types.Address]struct{})

	validators.IterAcct([]string{"A", "B", "C", "D"}, func(v *TestValidator) {
		signers[v.Address()] = struct{}{}
	})
	require.False(t, vs.HasQuorum(1, signers))
}
