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

const testCommissionRemovalEpoch = uint64(700)
const testStakeWeightActivationEpoch = uint64(700)

// newZeroCommissionRewardDistributor 模拟主网已关闭佣金且已启用质押权重 SR 池分配的分发器。
func newZeroCommissionRewardDistributor(stakeStore *StakeStore, rewardAmount *big.Int) *RewardDistributor {
	rd := NewRewardDistributor(nil, types.ZeroAddress, rewardAmount, nil, stakeStore, 1000, 21*24*time.Hour, hclog.NewNullLogger())
	rd.SetCommissionRemovedAtEpochChecker(func(epoch uint64) bool {
		return epoch >= testCommissionRemovalEpoch
	})
	rd.SetStakeWeightPoolSplitAtEpochChecker(func(epoch uint64) bool {
		return epoch >= testStakeWeightActivationEpoch
	})
	rd.SetDistributionEpoch(testCommissionRemovalEpoch)
	return rd
}

// newBlockCountSplitRewardDistributor 模拟激活 epoch 之前：零佣金但 SR 间仍按出块数分配 voter 池。
func newBlockCountSplitRewardDistributor(stakeStore *StakeStore, rewardAmount *big.Int) *RewardDistributor {
	rd := newZeroCommissionRewardDistributor(stakeStore, rewardAmount)
	rd.SetStakeWeightPoolSplitAtEpochChecker(func(uint64) bool { return false })
	return rd
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
	rd := newZeroCommissionRewardDistributor(stakeStore, rewardAmount)

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

func TestRewardDistributor_WithVoters_ZeroCommission(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0x2")

	delegateInfo := &DelegateInfo{
		Address:        validatorAddr,
		VotingPower:    big.NewInt(200),
		TotalVotes:     big.NewInt(200),
		IsActive:       true,
		IsRegistered:   true,
		CommissionRate: 1000, // 链上仍可能有历史佣金率，但 epoch 已关闭佣金
	}
	require.NoError(t, stakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil))

	rewardAmount := big.NewInt(1000)
	rd := newZeroCommissionRewardDistributor(stakeStore, rewardAmount)

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
			DelegateVotes:  map[types.Address]*big.Int{validatorAddr: big.NewInt(100)},
		},
		voter2Addr: {
			Address:        voter2Addr,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{validatorAddr},
			DelegateVotes:  map[types.Address]*big.Int{validatorAddr: big.NewInt(100)},
		},
	}

	rewards := rd.CalculateRewards(validators, voters, blockCounts, 10)
	require.Len(t, rewards, 2)

	voterShareExpected := big.NewInt(500)
	require.Zero(t, voterShareExpected.Cmp(rewards[voter1Addr]))
	require.Zero(t, voterShareExpected.Cmp(rewards[voter2Addr]))
	require.Nil(t, rewards[validatorAddr])
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

func TestRewardDistributor_StakeRatioOnSameValidator_ZeroCommission(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0x6")

	delegateInfo := &DelegateInfo{
		Address:        validatorAddr,
		VotingPower:    big.NewInt(300),
		TotalVotes:     big.NewInt(300),
		IsActive:       true,
		IsRegistered:   true,
		CommissionRate: 1000,
	}
	require.NoError(t, stakeStore.setDelegateInfo(validatorAddr, delegateInfo, nil))

	rewardAmount := big.NewInt(1000)
	rd := newZeroCommissionRewardDistributor(stakeStore, rewardAmount)

	validators := validator.AccountSet{
		{Address: validatorAddr, VotingPower: big.NewInt(300), IsActive: true},
	}
	blockCounts := map[types.Address]uint64{validatorAddr: 10}

	voterLarge := types.StringToAddress("0x7")
	voterSmall := types.StringToAddress("0x8")
	voters := map[types.Address]*VoterInfo{
		voterLarge: {
			Address:        voterLarge,
			VotingPower:    big.NewInt(200),
			VotedDelegates: []types.Address{validatorAddr},
			DelegateVotes:  map[types.Address]*big.Int{validatorAddr: big.NewInt(200)},
		},
		voterSmall: {
			Address:        voterSmall,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{validatorAddr},
			DelegateVotes:  map[types.Address]*big.Int{validatorAddr: big.NewInt(100)},
		},
	}

	rewards := rd.CalculateRewards(validators, voters, blockCounts, 10)

	distributable := big.NewInt(1000)
	expectedLarge := new(big.Int).Mul(distributable, big.NewInt(200))
	expectedLarge.Div(expectedLarge, big.NewInt(300))
	expectedSmall := new(big.Int).Mul(distributable, big.NewInt(100))
	expectedSmall.Div(expectedSmall, big.NewInt(300))

	require.Zero(t, expectedLarge.Cmp(rewards[voterLarge]))
	require.Zero(t, expectedSmall.Cmp(rewards[voterSmall]))
	require.Equal(t, int64(2), new(big.Int).Div(expectedLarge, expectedSmall).Int64())

	// 整除余数归验证者（零佣金下无佣金截留）
	remainder := new(big.Int).Sub(distributable, new(big.Int).Add(expectedLarge, expectedSmall))
	require.Zero(t, remainder.Cmp(rewards[validatorAddr]))
}

func TestRewardDistributor_MultiDelegateNoDilution_ZeroCommission(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	validatorAddr := types.StringToAddress("0x9")
	otherValidator := types.StringToAddress("0xa")

	for _, addr := range []types.Address{validatorAddr, otherValidator} {
		info := &DelegateInfo{
			Address:        addr,
			VotingPower:    big.NewInt(100),
			TotalVotes:     big.NewInt(100),
			IsActive:       true,
			IsRegistered:   true,
			CommissionRate: 1000,
		}
		require.NoError(t, stakeStore.setDelegateInfo(addr, info, nil))
	}

	rewardAmount := big.NewInt(1000)
	rd := newZeroCommissionRewardDistributor(stakeStore, rewardAmount)

	validators := validator.AccountSet{
		{Address: validatorAddr, VotingPower: big.NewInt(100), IsActive: true},
		{Address: otherValidator, VotingPower: big.NewInt(100), IsActive: true},
	}
	blockCounts := map[types.Address]uint64{
		validatorAddr:  5,
		otherValidator: 5,
	}

	multiVoter := types.StringToAddress("0xb")
	competitor := types.StringToAddress("0xc")
	voters := map[types.Address]*VoterInfo{
		multiVoter: {
			Address:     multiVoter,
			VotingPower: big.NewInt(3000),
			VotedDelegates: []types.Address{
				validatorAddr,
				otherValidator,
			},
			DelegateVotes: map[types.Address]*big.Int{
				validatorAddr:  big.NewInt(2000),
				otherValidator: big.NewInt(1000),
			},
		},
		competitor: {
			Address:        competitor,
			VotingPower:    big.NewInt(1000),
			VotedDelegates: []types.Address{validatorAddr},
			DelegateVotes:  map[types.Address]*big.Int{validatorAddr: big.NewInt(1000)},
		},
	}

	var target *validator.ValidatorMetadata
	for _, v := range validators {
		if v.Address == validatorAddr {
			target = v
			break
		}
	}
	require.NotNil(t, target)

	validatorAmount, voterRewards := rd.computeRewardsForValidator(target, voters, validators, blockCounts, 10)

	// SR A 有效质押 = 2000+1000=3000，SR B=1000；R 按 3:1 分 => SR A 池 750
	distributable := big.NewInt(750)
	expectedMulti := new(big.Int).Mul(distributable, big.NewInt(2000))
	expectedMulti.Div(expectedMulti, big.NewInt(3000))
	expectedCompetitor := new(big.Int).Mul(distributable, big.NewInt(1000))
	expectedCompetitor.Div(expectedCompetitor, big.NewInt(3000))

	require.Zero(t, expectedMulti.Cmp(voterRewards[multiVoter]))
	require.Zero(t, expectedCompetitor.Cmp(voterRewards[competitor]))
	require.Equal(t, int64(2), new(big.Int).Div(expectedMulti, expectedCompetitor).Int64())

	remainder := new(big.Int).Sub(distributable, new(big.Int).Add(expectedMulti, expectedCompetitor))
	require.Zero(t, remainder.Cmp(validatorAmount))
}

func TestRewardDistributor_StakeWeightedBetweenValidators_ZeroCommission(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	largeSR := types.StringToAddress("0xd1")
	smallSR := types.StringToAddress("0xd2")

	for addr, stake := range map[types.Address]*big.Int{
		largeSR: big.NewInt(300),
		smallSR: big.NewInt(100),
	} {
		info := &DelegateInfo{
			Address:      addr,
			VotingPower:  new(big.Int).Set(stake),
			TotalVotes:   new(big.Int).Set(stake),
			IsActive:     true,
			IsRegistered: true,
		}
		require.NoError(t, stakeStore.setDelegateInfo(addr, info, nil))
	}

	rewardAmount := big.NewInt(1000)
	rd := newZeroCommissionRewardDistributor(stakeStore, rewardAmount)

	validators := validator.AccountSet{
		{Address: largeSR, VotingPower: big.NewInt(300), IsActive: true},
		{Address: smallSR, VotingPower: big.NewInt(100), IsActive: true},
	}
	blockCounts := map[types.Address]uint64{
		largeSR: 5,
		smallSR: 5,
	}

	voterLarge := types.StringToAddress("0xd3")
	voterSmall := types.StringToAddress("0xd4")
	voters := map[types.Address]*VoterInfo{
		voterLarge: {
			Address:        voterLarge,
			VotingPower:    big.NewInt(300),
			VotedDelegates: []types.Address{largeSR},
			DelegateVotes:  map[types.Address]*big.Int{largeSR: big.NewInt(300)},
		},
		voterSmall: {
			Address:        voterSmall,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{smallSR},
			DelegateVotes:  map[types.Address]*big.Int{smallSR: big.NewInt(100)},
		},
	}

	rewards := rd.CalculateRewards(validators, voters, blockCounts, 10)

	// 出块相同、质押 3:1 => 大 SR 750，小 SR 250；零佣金下 voter 拿满各自 SR 池
	require.Zero(t, big.NewInt(750).Cmp(rewards[voterLarge]))
	require.Zero(t, big.NewInt(250).Cmp(rewards[voterSmall]))
}

func TestRewardDistributor_BeforeStakeWeightActivationUsesBlockSplit(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	largeSR := types.StringToAddress("0xf1")
	smallSR := types.StringToAddress("0xf2")

	for addr, stake := range map[types.Address]*big.Int{
		largeSR: big.NewInt(300),
		smallSR: big.NewInt(100),
	} {
		info := &DelegateInfo{
			Address:      addr,
			VotingPower:  new(big.Int).Set(stake),
			TotalVotes:   new(big.Int).Set(stake),
			IsActive:     true,
			IsRegistered: true,
		}
		require.NoError(t, stakeStore.setDelegateInfo(addr, info, nil))
	}

	rd := newBlockCountSplitRewardDistributor(stakeStore, big.NewInt(1000))

	validators := validator.AccountSet{
		{Address: largeSR, VotingPower: big.NewInt(300), IsActive: true},
		{Address: smallSR, VotingPower: big.NewInt(100), IsActive: true},
	}
	blockCounts := map[types.Address]uint64{largeSR: 5, smallSR: 5}

	voterLarge := types.StringToAddress("0xf3")
	voterSmall := types.StringToAddress("0xf4")
	voters := map[types.Address]*VoterInfo{
		voterLarge: {
			Address:        voterLarge,
			VotingPower:    big.NewInt(300),
			VotedDelegates: []types.Address{largeSR},
			DelegateVotes:  map[types.Address]*big.Int{largeSR: big.NewInt(300)},
		},
		voterSmall: {
			Address:        voterSmall,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{smallSR},
			DelegateVotes:  map[types.Address]*big.Int{smallSR: big.NewInt(100)},
		},
	}

	rewards := rd.CalculateRewards(validators, voters, blockCounts, 10)

	// 出块相同 => 各 500，与质押 3:1 无关（激活 epoch 之前）
	require.Zero(t, big.NewInt(500).Cmp(rewards[voterLarge]))
	require.Zero(t, big.NewInt(500).Cmp(rewards[voterSmall]))
}

func TestRewardDistributor_ZeroCommission_StakeWeightAndInternalSplit(t *testing.T) {
	stakeStore := createTestStakeStore(t)
	largeSR := types.StringToAddress("0xe1")
	smallSR := types.StringToAddress("0xe2")

	for addr, stake := range map[types.Address]*big.Int{
		largeSR: big.NewInt(300),
		smallSR: big.NewInt(100),
	} {
		info := &DelegateInfo{
			Address:        addr,
			VotingPower:    new(big.Int).Set(stake),
			TotalVotes:     new(big.Int).Set(stake),
			IsActive:       true,
			IsRegistered:   true,
			CommissionRate: 1000,
		}
		require.NoError(t, stakeStore.setDelegateInfo(addr, info, nil))
	}

	rd := newZeroCommissionRewardDistributor(stakeStore, big.NewInt(1000))

	validators := validator.AccountSet{
		{Address: largeSR, VotingPower: big.NewInt(300), IsActive: true},
		{Address: smallSR, VotingPower: big.NewInt(100), IsActive: true},
	}
	blockCounts := map[types.Address]uint64{largeSR: 5, smallSR: 5}

	voterLargeA := types.StringToAddress("0xe3")
	voterLargeB := types.StringToAddress("0xe4")
	voterSmall := types.StringToAddress("0xe5")
	voters := map[types.Address]*VoterInfo{
		voterLargeA: {
			Address:        voterLargeA,
			VotingPower:    big.NewInt(200),
			VotedDelegates: []types.Address{largeSR},
			DelegateVotes:  map[types.Address]*big.Int{largeSR: big.NewInt(200)},
		},
		voterLargeB: {
			Address:        voterLargeB,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{largeSR},
			DelegateVotes:  map[types.Address]*big.Int{largeSR: big.NewInt(100)},
		},
		voterSmall: {
			Address:        voterSmall,
			VotingPower:    big.NewInt(100),
			VotedDelegates: []types.Address{smallSR},
			DelegateVotes:  map[types.Address]*big.Int{smallSR: big.NewInt(100)},
		},
	}

	rewards := rd.CalculateRewards(validators, voters, blockCounts, 10)

	largePool := big.NewInt(750)
	expectedLargeA := new(big.Int).Mul(largePool, big.NewInt(200))
	expectedLargeA.Div(expectedLargeA, big.NewInt(300))
	expectedLargeB := new(big.Int).Mul(largePool, big.NewInt(100))
	expectedLargeB.Div(expectedLargeB, big.NewInt(300))

	require.Zero(t, expectedLargeA.Cmp(rewards[voterLargeA]))
	require.Zero(t, expectedLargeB.Cmp(rewards[voterLargeB]))
	require.Zero(t, big.NewInt(250).Cmp(rewards[voterSmall]))
	require.Equal(t, int64(2), new(big.Int).Div(expectedLargeA, expectedLargeB).Int64())
	require.Nil(t, rewards[largeSR])
	require.Nil(t, rewards[smallSR])
}
