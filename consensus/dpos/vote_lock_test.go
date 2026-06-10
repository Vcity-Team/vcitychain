package dpos

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestStakeCountsTowardLocked(t *testing.T) {
	addr := types.StringToAddress("0x1111111111111111111111111111111111111111")
	amount := big.NewInt(1000)

	applied := &StakeInfo{
		Staker:   addr,
		Amount:   amount,
		IsActive: true,
		Applied:  true,
	}
	pending := &StakeInfo{
		Staker:   addr,
		Amount:   amount,
		IsActive: true,
		Applied:  false,
	}
	voided := &StakeInfo{
		Staker:   addr,
		Amount:   big.NewInt(0),
		IsActive: false,
		Applied:  true,
	}

	if !StakeCountsTowardLocked(applied, false) {
		t.Fatal("applied stake should count when lock inactive")
	}
	if StakeCountsTowardLocked(pending, false) {
		t.Fatal("pending stake should not count when lock inactive")
	}
	if !StakeCountsTowardLocked(pending, true) {
		t.Fatal("pending stake should count when lock active")
	}
	if StakeCountsTowardLocked(voided, true) {
		t.Fatal("voided stake should not count")
	}
}

func TestSumLockedVoteWeiFromStakes(t *testing.T) {
	addr := types.StringToAddress("0x2222222222222222222222222222222222222222")
	infos := []*StakeInfo{
		{Staker: addr, Amount: big.NewInt(100), IsActive: true, Applied: true},
		{Staker: addr, Amount: big.NewInt(50), IsActive: true, Applied: false},
		{Staker: addr, Amount: big.NewInt(25), IsActive: true, Applied: false, PendingUnvote: true},
	}

	lockedInactive := sumLockedVoteWeiFromStakes(infos, addr, false)
	if lockedInactive.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("inactive lock total = %s, want 100", lockedInactive.String())
	}

	lockedActive := sumLockedVoteWeiFromStakes(infos, addr, true)
	if lockedActive.Cmp(big.NewInt(175)) != 0 {
		t.Fatalf("active lock total = %s, want 175", lockedActive.String())
	}
}
