package dpos

import "testing"

func TestVoterPoolStakeWeightActivationEpoch(t *testing.T) {
	d := &DPoS{
		config: &DPoSConfig{
			VoterPoolStakeWeightActivationEpoch: 800,
		},
	}
	if d.GetVoterPoolStakeWeightActivationEpoch() != 800 {
		t.Fatalf("activation epoch = %d, want 800", d.GetVoterPoolStakeWeightActivationEpoch())
	}
	if d.isVoterPoolStakeWeightSplitAtEpoch(799) {
		t.Fatal("epoch 799 should still use block-count split")
	}
	if !d.isVoterPoolStakeWeightSplitAtEpoch(800) {
		t.Fatal("epoch 800 should use stake-weight split")
	}
	if !d.isVoterPoolStakeWeightSplitAtEpoch(801) {
		t.Fatal("epoch 801 should use stake-weight split")
	}
}

func TestVoterPoolStakeWeightActivationEpochZero(t *testing.T) {
	d := &DPoS{config: &DPoSConfig{}}
	if d.isVoterPoolStakeWeightSplitAtEpoch(1000) {
		t.Fatal("activation 0 should keep block-count split")
	}
}
