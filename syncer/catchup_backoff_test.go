package syncer

import (
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestTrustedQuorumIndicatesNextBlock(t *testing.T) {
	s := &syncer{logger: hclog.NewNullLogger()}

	ahead := trustedTipResult{Quorum: true, Branch: trustedTipBranchMaxCluster, Tip: 101, MaxBootHeight: 101}
	require.True(t, s.trustedQuorumIndicatesNextBlock(100, ahead))

	caught := trustedTipResult{Quorum: true, Branch: trustedTipBranchMaxCluster, Tip: 100, MaxBootHeight: 100}
	require.False(t, s.trustedQuorumIndicatesNextBlock(100, caught))

	noQ := trustedTipResult{Quorum: false, Branch: trustedTipBranchNoQuorum, Tip: 105}
	require.False(t, s.trustedQuorumIndicatesNextBlock(100, noQ))

	single := trustedTipResult{SingleBootFallback: true, Branch: trustedTipBranchSingleBoot, Tip: 101}
	require.True(t, s.trustedQuorumIndicatesNextBlock(100, single))
}

func TestCatchUpRetryBackoff(t *testing.T) {
	s := &syncer{logger: hclog.NewNullLogger()}
	ahead := trustedTipResult{Quorum: true, Branch: trustedTipBranchMaxCluster, Tip: 101}
	require.Equal(t, trustedAheadCatchUpRetryBackoff, s.catchUpRetryBackoff(100, ahead))

	caught := trustedTipResult{Quorum: true, Branch: trustedTipBranchMaxCluster, Tip: 100}
	require.Equal(t, syncBestNotAheadWakeInterval, s.catchUpRetryBackoff(100, caught))
}

func TestCatchUpProbeAllowed(t *testing.T) {
	b1 := peer.ID("b1")
	b2 := peer.ID("b2")
	s := &syncer{
		logger:             hclog.NewNullLogger(),
		trustedBootnodeIDs: map[peer.ID]struct{}{b1: {}, b2: {}},
		trustedBootRPCHeight: map[peer.ID]uint64{
			b1: 101,
			b2: 101,
		},
	}
	meta := trustedTipResult{Quorum: true, Branch: trustedTipBranchMaxCluster, Tip: 100, Heights: []uint64{100, 101}}
	require.True(t, s.catchUpProbeAllowed(101, meta))
	require.True(t, s.bootsRPCAtLeast(101) >= trustedBootnodeQuorumK)
}

func TestCountHeightsAtLeast(t *testing.T) {
	require.Equal(t, 2, countHeightsAtLeast([]uint64{100, 101, 99}, 101))
	require.Equal(t, 0, countHeightsAtLeast([]uint64{100}, 101))
}
