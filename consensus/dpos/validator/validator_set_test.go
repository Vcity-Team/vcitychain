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

func TestValidatorSet_GetQuorumSizeByValidatorCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		validatorCount     int
		expectedQuorumSize int
	}{
		{1, 1}, // 1个验证者需要1个签名
		{2, 1}, // 2个验证者需要1个签名 (2/2 = 1)
		{3, 2}, // 3个验证者需要2个签名 (3/2 = 1.5 → 2)
		{4, 2}, // 4个验证者需要2个签名 (4/2 = 2)
		{5, 3}, // 5个验证者需要3个签名 (5/2 = 2.5 → 3)
	}

	for _, c := range cases {
		quorumSize := GetQuorumSizeByValidatorCount(c.validatorCount)
		require.Equal(t, c.expectedQuorumSize, quorumSize)
	}
}
