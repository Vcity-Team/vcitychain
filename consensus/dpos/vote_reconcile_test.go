package dpos

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestStakeInfoToPendingVoteRecord(t *testing.T) {
	addr := types.StringToAddress("0x3333333333333333333333333333333333333333")
	si := &StakeInfo{
		Staker:         addr,
		Delegate:       types.StringToAddress("0x4444444444444444444444444444444444444444"),
		Amount:         big.NewInt(1000),
		StartTime:      42,
		IsActive:       true,
		EffectiveEpoch: 10,
		Applied:        false,
	}

	if rec := stakeInfoToPendingVoteRecord(si, 9); rec != nil {
		t.Fatal("epoch 9 should not include effective epoch 10")
	}
	rec := stakeInfoToPendingVoteRecord(si, 10)
	if rec == nil || rec.Timestamp != 42 || rec.EffectiveEpoch != 10 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	rec = stakeInfoToPendingVoteRecord(si, 99)
	if rec == nil {
		t.Fatal("expected overdue record for max epoch 99")
	}

	applied := *si
	applied.Applied = true
	if stakeInfoToPendingVoteRecord(&applied, 99) != nil {
		t.Fatal("applied stake should be skipped")
	}
}
