package dpos

import (
	"math/big"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

func createTestStakeStore(t *testing.T) *StakeStore {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "stake.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: time.Second})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = db.Close()
	})

	return &StakeStore{db: db}
}

func TestRewardDistributor_NoVoters(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0x1")

	delegateInfo := &DelegateInfo{
		Address:      validatorAddr,
		VotingPower:  big.NewInt(100),
		TotalVotes:   big.NewInt(100),
		IsActive:     true,
		IsRegistered: true,
	}
	require.NoError(t, stakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil))

	rewardAmount := big.NewInt(1000)
	rd := NewRewardDistributor(nil, types.ZeroAddress, rewardAmount, nil, stakeStore, 1000, 21*24*time.Hour, hclog.NewNullLogger())

	validators := validator.AccountSet{
		{
			Address:     validatorAddr,
			VotingPower: big.NewInt(100),
			IsActive:    true,
		},
	}
	blockCounts := map[types.Address]uint64{validatorAddr: 10}

	rewards := rd.CalculateRewards(validators, map[types.Address]*VoterInfo{}, blockCounts, 10)
	require.Len(t, rewards, 1)

	expected := big.NewInt(1000)
	actual := rewards[validatorAddr]
	require.NotNil(t, actual)
	require.Zero(t, expected.Cmp(actual))
}

func TestRewardDistributor_WithVoters(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0x2")

	delegateInfo := &DelegateInfo{
		Address:        validatorAddr,
		VotingPower:    big.NewInt(200),
		TotalVotes:     big.NewInt(200),
		IsActive:       true,
		IsRegistered:   true,
		CommissionRate: 1000, // 10%
	}
	require.NoError(t, stakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil))

	rewardAmount := big.NewInt(1000)
	rd := NewRewardDistributor(nil, types.ZeroAddress, rewardAmount, nil, stakeStore, 1000, 21*24*time.Hour, hclog.NewNullLogger())

	validators := validator.AccountSet{
		{
			Address:     validatorAddr,
			VotingPower: big.NewInt(200),
			IsActive:    true,
		},
	}
	blockCounts := map[types.Address]uint64{validatorAddr: 10}

	voter1Addr := types.StringToAddress("0x3")
	voter2Addr := types.StringToAddress("0x4")
	voters := map[types.Address]*VoterInfo{
		voter1Addr: {
			Address:        voter1Addr,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{validatorAddr},
		},
		voter2Addr: {
			Address:        voter2Addr,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{validatorAddr},
		},
	}

	rewards := rd.CalculateRewards(validators, voters, blockCounts, 10)
	require.Len(t, rewards, 3)

	commissionExpected := big.NewInt(100) // 10% of 1000
	voterShareExpected := big.NewInt(450)

	require.Zero(t, commissionExpected.Cmp(rewards[validatorAddr]))
	require.Zero(t, voterShareExpected.Cmp(rewards[voter1Addr]))
	require.Zero(t, voterShareExpected.Cmp(rewards[voter2Addr]))
}

func TestRewardDistributor_CommissionAutoTurn(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0x5")

	effective := 5 * time.Second
	pastTime := uint64(time.Now().Add(-2 * effective).Unix())

	delegateInfo := &DelegateInfo{
		Address:               validatorAddr,
		VotingPower:           big.NewInt(100),
		TotalVotes:            big.NewInt(100),
		IsActive:              true,
		IsRegistered:          true,
		CommissionRate:        500,
		PendingCommissionRate: 800,
		CommissionUpdateTime:  pastTime,
	}
	require.NoError(t, stakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil))

	rd := NewRewardDistributor(nil, types.ZeroAddress, big.NewInt(1000), nil, stakeStore, 1000, effective, hclog.NewNullLogger())

	rate := rd.getCommissionRate(validatorAddr)
	require.Equal(t, uint64(800), rate)

	updatedInfo, err := stakeStore.GetDelegateInfo(validatorAddr)
	require.NoError(t, err)
	require.NotNil(t, updatedInfo)
	require.Equal(t, uint64(800), updatedInfo.CommissionRate)
	require.Equal(t, uint64(0), updatedInfo.PendingCommissionRate)
	require.NotZero(t, updatedInfo.CommissionUpdateTime)
}
