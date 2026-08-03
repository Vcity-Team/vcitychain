package dpos

import (
	"encoding/hex"
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestBuildCommissionUpdateCalldata(t *testing.T) {
	got := BuildCommissionUpdateCalldata(500)
	if string(got[:7]) != "DPOSCOM" {
		t.Fatalf("prefix: %q", got[:7])
	}
	if got[7] != 0x01 || got[8] != 0xf4 { // 500 = 0x01f4
		t.Fatalf("rate bytes: %x", got[7:9])
	}
}

func TestBuildDelegateRegistrationCalldata(t *testing.T) {
	addr := types.StringToAddress("0x1111111111111111111111111111111111111111")
	got := BuildDelegateRegistrationCalldata(addr, "n", "w", "d")
	if string(got[:4]) != "DPOS" || string(got[4:7]) != "REG" || got[7] != 0 {
		t.Fatalf("bad header: %x", got[:8])
	}
}

func TestBuildProposalDomainHashStable(t *testing.T) {
	p := &ParameterProposal{
		ProposalType: "parameter",
		Parameter:    "min_freeze_period",
		Proposer:     types.StringToAddress("0x2222222222222222222222222222222222222222"),
		CreatedAt:    1700000000,
	}
	h1 := BuildProposalDomainHash(100, p)
	h2 := BuildProposalDomainHash(100, p)
	if hex.EncodeToString(h1) != hex.EncodeToString(h2) {
		t.Fatal("hash not stable")
	}
	if len(h1) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(h1))
	}
}
