package syncer

import (
	"math/big"
	"testing"
	"time"

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

func TestPickSyncPeerForTarget_ForceBulkBootOnly(t *testing.T) {
	bootAhead := peer.ID("boot-ahead")
	bootStale := peer.ID("boot-stale")
	nonBoot := peer.ID("non-boot-high")

	s := &syncer{
		logger:             hclog.NewNullLogger(),
		peerMap:            new(PeerMap),
		trustedBootnodeIDs: map[peer.ID]struct{}{bootAhead: {}, bootStale: {}},
	}
	s.peerMap.Put(
		&NoForkPeer{ID: bootAhead, Number: 100, Distance: bigZero()},
		&NoForkPeer{ID: bootStale, Number: 99, Distance: bigZero()},
		&NoForkPeer{ID: nonBoot, Number: 200, Distance: bigZero()},
	)

	p := s.pickSyncPeerForTarget(99, 102, nil, true)
	require.NotNil(t, p)
	require.Equal(t, bootAhead, p.ID)

	// forceBulk 且 syncTarget>local：P2P 不超前时仍用 boot + syncTarget 作 bulk 逻辑高度
	p = s.pickSyncPeerForTarget(100, 102, nil, true)
	require.NotNil(t, p)
	require.True(t, p.Number > 100)

	p = s.pickSyncPeerForTarget(99, 102, nil, false)
	require.NotNil(t, p)
	require.Equal(t, nonBoot, p.ID)

	// skip 与 pull-distrust 应让 force-bulk 换到另一 boot，而非死磕最高 RPC 源
	skipBoot := map[peer.ID]bool{bootAhead: true}
	p = s.pickSyncPeerForTarget(99, 102, skipBoot, true)
	require.NotNil(t, p)
	require.Equal(t, bootStale, p.ID)

	s.pullDistrustMu.Lock()
	s.pullDistrustUntil[bootAhead] = time.Now().Add(time.Minute)
	s.pullDistrustMu.Unlock()
	p = s.pickSyncPeerForTarget(99, 102, nil, true)
	require.NotNil(t, p)
	require.Equal(t, bootStale, p.ID)
}

func TestPickBootPeerForTrustedBulk_PrefersHighestRPC(t *testing.T) {
	bootLow := peer.ID("boot-low")
	bootHigh := peer.ID("boot-high")
	s := &syncer{
		logger:             hclog.NewNullLogger(),
		peerMap:            new(PeerMap),
		trustedBootnodeIDs: map[peer.ID]struct{}{bootLow: {}, bootHigh: {}},
	}
	s.setTrustedBootRPCHeight(bootLow, 15921244)
	s.setTrustedBootRPCHeight(bootHigh, 15921249)

	p := s.pickBootPeerForTrustedBulk(15921244, 15921249, nil)
	require.NotNil(t, p)
	require.Equal(t, bootHigh, p.ID)
	require.Equal(t, uint64(15921249), p.Number)

	s.bulkBootRotateIdx.Store(1)
	p2 := s.pickBootPeerForTrustedBulk(15921244, 15921249, nil)
	require.NotNil(t, p2)
	require.Equal(t, bootLow, p2.ID)
}

func bigZero() *big.Int { return big.NewInt(0) }

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
