package syncer

import (
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestComputeTrustedBootnodeTip_MaxClusterQuorum(t *testing.T) {
	heights := []uint64{15878201, 15878201, 15878241, 15878297, 15878321, 15878415}
	s := &syncer{logger: hclog.NewNullLogger()}
	res := s.computeTrustedBootnodeQuorum(15878415, nil, heights)
	require.True(t, res.Quorum)
	require.Equal(t, uint64(15878415), res.Tip)
}

func TestComputeTrustedBootnodeTip_MedianCluster(t *testing.T) {
	heights := []uint64{100, 101, 99}
	s := &syncer{logger: hclog.NewNullLogger()}
	res := s.computeTrustedBootnodeQuorum(90, nil, heights)
	require.True(t, res.Quorum)
	require.Equal(t, uint64(101), res.Tip)
}

func TestTryLocalMaxBootAgree(t *testing.T) {
	tip, ok := tryLocalMaxBootAgree(15905892, 15905892, 1, 6)
	require.True(t, ok)
	require.Equal(t, uint64(15905892), tip)

	_, ok = tryLocalMaxBootAgree(15905892, 15905892, 1, 1)
	require.False(t, ok)

	_, ok = tryLocalMaxBootAgree(15905800, 15905892, 1, 6)
	require.False(t, ok)
}

func TestComputeTrustedBootnodeTip_SingleBootFallback(t *testing.T) {
	b1 := peer.ID("bootnode-1")
	s := &syncer{
		logger:             hclog.NewNullLogger(),
		trustedBootnodeIDs: map[peer.ID]struct{}{b1: {}},
	}
	res := s.computeTrustedBootnodeQuorum(0, nil, []uint64{200})
	require.True(t, res.SingleBootFallback)
	require.Equal(t, uint64(200), res.Tip)
}
