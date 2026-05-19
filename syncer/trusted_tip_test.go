package syncer

import (
	"fmt"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestComputeTrustedBootnodeTip_MaxClusterQuorum(t *testing.T) {
	ids := make([]peer.ID, 6)
	for i := range ids {
		ids[i] = peer.ID(fmt.Sprintf("boot-%d", i))
	}
	s := &syncer{
		logger:             hclog.NewNullLogger(),
		peerMap:            new(PeerMap),
		trustedBootnodeIDs: map[peer.ID]struct{}{},
	}
	for _, id := range ids {
		s.trustedBootnodeIDs[id] = struct{}{}
	}

	heights := []uint64{15878201, 15878201, 15878241, 15878297, 15878321, 15878415}
	for i, h := range heights {
		s.peerMap.Put(&NoForkPeer{ID: ids[i], Number: h})
	}

	res := s.computeTrustedBootnodeTip(15878415)
	require.True(t, res.Quorum)
	require.Equal(t, uint64(15878415), res.Tip)
}

func TestComputeTrustedBootnodeTip_MedianCluster(t *testing.T) {
	b1 := peer.ID("bootnode-1")
	b2 := peer.ID("bootnode-2")
	b3 := peer.ID("bootnode-3")

	s := &syncer{
		logger:             hclog.NewNullLogger(),
		peerMap:            new(PeerMap),
		trustedBootnodeIDs: map[peer.ID]struct{}{b1: {}, b2: {}, b3: {}},
	}
	s.peerMap.Put(
		&NoForkPeer{ID: b1, Number: 100},
		&NoForkPeer{ID: b2, Number: 101},
		&NoForkPeer{ID: b3, Number: 99},
	)

	res := s.computeTrustedBootnodeTip(90)
	require.True(t, res.Quorum)
	require.Equal(t, uint64(101), res.Tip)
}

func TestComputeTrustedBootnodeTip_SingleBootFallback(t *testing.T) {
	b1 := peer.ID("bootnode-1")
	s := &syncer{
		logger:             hclog.NewNullLogger(),
		peerMap:            new(PeerMap),
		trustedBootnodeIDs: map[peer.ID]struct{}{b1: {}},
	}
	s.peerMap.Put(&NoForkPeer{ID: b1, Number: 200})

	res := s.computeTrustedBootnodeTip(0)
	require.True(t, res.SingleBootFallback)
	require.Equal(t, uint64(200), res.Tip)
}
