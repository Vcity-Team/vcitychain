package dpos

import (
	"math/big"
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/require"
)

func TestCalculateProducerRewards(t *testing.T) {
	perBlock := big.NewInt(7600000000000000) // 0.0076 VCITY
	blockCounts := map[types.Address]uint64{
		types.StringToAddress("0x1000000000000000000000000000000000000001"): 57,
		types.StringToAddress("0x2000000000000000000000000000000000000002"): 58,
	}

	rewards, details := calculateProducerRewardsForTest(blockCounts, perBlock)
	require.Len(t, rewards, 2)
	require.Len(t, details, 2)

	addr1 := types.StringToAddress("0x1000000000000000000000000000000000000001")
	expected1 := new(big.Int).Mul(perBlock, big.NewInt(57))
	require.Equal(t, 0, rewards[addr1].Cmp(expected1))

	addr2 := types.StringToAddress("0x2000000000000000000000000000000000000002")
	expected2 := new(big.Int).Mul(perBlock, big.NewInt(58))
	require.Equal(t, 0, rewards[addr2].Cmp(expected2))
}

func TestComputeProducerRewardPool(t *testing.T) {
	d := &DPoS{
		config: &DPoSConfig{
			BlockProducerRewardPerBlock: big.NewInt(7600000000000000),
		},
	}
	pool := d.computeProducerRewardPool(1200)
	expected := new(big.Int).Mul(big.NewInt(7600000000000000), big.NewInt(1200))
	require.Equal(t, 0, pool.Cmp(expected))
}

func TestProducerRewardActivationEpoch(t *testing.T) {
	d := &DPoS{
		config: &DPoSConfig{
			BlockProducerRewardPerBlock:   big.NewInt(1),
			ProducerRewardActivationEpoch: 100,
		},
	}
	require.False(t, d.isProducerRewardActive(99))
	require.True(t, d.isProducerRewardActive(100))
}

func TestMergeRewardMaps(t *testing.T) {
	base := map[types.Address]*big.Int{
		types.StringToAddress("0x1"): big.NewInt(100),
	}
	extra := map[types.Address]*big.Int{
		types.StringToAddress("0x1"): big.NewInt(50),
		types.StringToAddress("0x2"): big.NewInt(20),
	}
	mergeRewardMaps(base, extra)
	require.Equal(t, int64(150), base[types.StringToAddress("0x1")].Int64())
	require.Equal(t, int64(20), base[types.StringToAddress("0x2")].Int64())
}

func calculateProducerRewardsForTest(
	blockCounts map[types.Address]uint64,
	perBlock *big.Int,
) (map[types.Address]*big.Int, []*ProducerRewardDetail) {
	d := &DPoS{}
	return d.calculateProducerRewards(blockCounts, perBlock)
}
