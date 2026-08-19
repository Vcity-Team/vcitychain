package dpos

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatorCommissionFromMergedReward_SubtractsSelfVoterShare(t *testing.T) {
	t.Parallel()

	producer := big.NewInt(9_120_000_000_000_000_000) // 9.12 VCITY
	selfVoter := big.NewInt(3_518_424_273_909_474)    // ~0.0035 VCITY self-vote
	remainder := big.NewInt(14)                       // integer-division dust
	total := new(big.Int).Add(producer, selfVoter)
	total.Add(total, remainder)

	// Bug behavior: Rewards - producer only → includes self-voter (double-count with voter row).
	buggy := new(big.Int).Sub(total, producer)
	require.Equal(t, new(big.Int).Add(selfVoter, remainder).String(), buggy.String())

	got := validatorCommissionFromMergedReward(total, producer, selfVoter)
	require.Equal(t, remainder.String(), got.String(),
		"validator record must be commission/remainder only, not self-voter share")
}

func TestValidatorCommissionFromMergedReward_NoVoterShare(t *testing.T) {
	t.Parallel()

	producer := big.NewInt(1000)
	remainder := big.NewInt(7)
	total := new(big.Int).Add(producer, remainder)

	got := validatorCommissionFromMergedReward(total, producer, nil)
	require.Equal(t, remainder.String(), got.String())
}

func TestVoterRewardAmountByRecipient_SumsSelfAndCrossVotes(t *testing.T) {
	t.Parallel()

	sr := "0xaaaa"
	other := "0xbbbb"

	byRecipient := voterRewardAmountByRecipient([]*VoterRewardDetail{
		{VoterAddress: sr, ValidatorAddress: sr, Amount: big.NewInt(100)},    // self-vote
		{VoterAddress: sr, ValidatorAddress: other, Amount: big.NewInt(50)},  // SR votes for other
		{VoterAddress: other, ValidatorAddress: sr, Amount: big.NewInt(200)}, // external voter
	})

	require.Equal(t, "150", byRecipient[sr].String())
	require.Equal(t, "200", byRecipient[other].String())
}
