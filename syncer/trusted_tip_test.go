package syncer

import (
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestComputeTrustedBootnodeTip_MedianK2Quorum(t *testing.T) {
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

func TestComputeTrustedBootnodeTip_NoQuorumSplit(t *testing.T) {
	b1 := peer.ID("bootnode-1")
	b2 := peer.ID("bootnode-2")
	s := &syncer{
		logger:             hclog.NewNullLogger(),
		peerMap:            new(PeerMap),
		trustedBootnodeIDs: map[peer.ID]struct{}{b1: {}, b2: {}},
	}
	s.peerMap.Put(
		&NoForkPeer{ID: b1, Number: 100},
		&NoForkPeer{ID: b2, Number: 200},
	)

	res := s.computeTrustedBootnodeTip(0)
	require.Equal(t, uint64(0), res.Tip)
	require.False(t, res.Quorum)
}
