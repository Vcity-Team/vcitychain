package dpos

import (
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestCommissionRemovalActivationEpoch(t *testing.T) {
	d := &DPoS{
		config: &DPoSConfig{
			CommissionRemovalActivationEpoch: 700,
		},
	}
	if d.GetCommissionRemovalActivationEpoch() != 700 {
		t.Fatalf("activation epoch = %d, want 700", d.GetCommissionRemovalActivationEpoch())
	}
	if d.isCommissionRemovedAtEpoch(699) {
		t.Fatal("epoch 699 should still use commission")
	}
	if !d.isCommissionRemovedAtEpoch(700) {
		t.Fatal("epoch 700 should remove commission")
	}
}

func TestRewardDistributor_CommissionRemovedAtEpoch(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0xa1")
	delegateInfo := &DelegateInfo{
		Address:        validatorAddr,
		VotingPower:    big.NewInt(100),
		TotalVotes:     big.NewInt(100),
		IsActive:       true,
		IsRegistered:   true,
		CommissionRate: 1000,
	}
	require.NoError(t, stakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil))

	rd := NewRewardDistributor(nil, types.ZeroAddress, big.NewInt(1000), nil, stakeStore, 1000, 21*24*time.Hour, hclog.NewNullLogger())
	rd.SetCommissionRemovedAtEpochChecker(func(epoch uint64) bool {
		return epoch >= 700
	})
	rd.SetDistributionEpoch(700)

	require.Equal(t, uint64(0), rd.getCommissionRate(validatorAddr))

	rd.SetDistributionEpoch(699)
	require.Equal(t, uint64(1000), rd.getCommissionRate(validatorAddr))
}
