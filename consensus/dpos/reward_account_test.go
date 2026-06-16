package dpos

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
)

func TestGetEffectiveRewardAccountForEpoch(t *testing.T) {
	oldAcct := types.StringToAddress("0x1111111111111111111111111111111111111111")
	newAcct := types.StringToAddress("0x2222222222222222222222222222222222222222")

	d := &DPoS{
		config: &DPoSConfig{
			RewardAccount:                 oldAcct,
			RewardAccountActivationEpoch:  100,
		},
		parameterCurrentValues: map[string]interface{}{
			"dpos_reward_distribution_account":                    newAcct.String(),
			"dpos_reward_distribution_activation_epoch":   uint64(100),
		},
	}

	if got := d.GetEffectiveRewardAccountForEpoch(99); got != oldAcct {
		t.Fatalf("epoch 99 account = %s, want %s", got.String(), oldAcct.String())
	}
	if got := d.GetEffectiveRewardAccountForEpoch(100); got != newAcct {
		t.Fatalf("epoch 100 account = %s, want %s", got.String(), newAcct.String())
	}
}

func TestGetEffectiveRewardAccountForEpoch_NoActivation(t *testing.T) {
	oldAcct := types.StringToAddress("0x3333333333333333333333333333333333333333")
	newAcct := types.StringToAddress("0x4444444444444444444444444444444444444444")

	d := &DPoS{
		config: &DPoSConfig{
			RewardAccount: oldAcct,
		},
		parameterCurrentValues: map[string]interface{}{
			"dpos_reward_distribution_account": newAcct.String(),
		},
	}

	if got := d.GetEffectiveRewardAccountForEpoch(999); got != oldAcct {
		t.Fatalf("activation=0 should keep config account, got %s", got.String())
	}
}
