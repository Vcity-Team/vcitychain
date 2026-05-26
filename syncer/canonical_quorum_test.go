package syncer

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestComputeBootHashMajority_6of7(t *testing.T) {
	majority := types.StringToHash("0x83241a23690f3e667fe6579c0b32a835cdfbbc9ada6d0e64170d238d0578fd39")
	outlier := types.StringToHash("0xd49aeefd9f0e5b891f97579c6e8c03b34d6421006f493b19293f73ff0daa5970")
	local := uint64(15952487)
	var reports []bootHashReport
	for i := 0; i < 6; i++ {
		reports = append(reports, bootHashReport{
			peer:      peer.ID("boot-majority"),
			rpcHeight: local,
			hash:      majority,
			hashOK:    true,
		})
	}
	reports = append(reports, bootHashReport{
		peer:      peer.ID("boot-outlier"),
		rpcHeight: local + 68,
		hash:      outlier,
		hashOK:    true,
	})
	maj := computeBootHashMajority(local, reports)
	require.True(t, maj.ok)
	require.Equal(t, majority, maj.majorityHash)
	require.Equal(t, 6, maj.majorityVotes)
	require.Equal(t, local, maj.tip) // tip from majority boots' max RPC at local, not outlier+68
	require.Len(t, maj.outliers, 1)
}

func TestComputeBootHashMajority_3x3Split(t *testing.T) {
	h1 := types.StringToHash("0xaaa")
	h2 := types.StringToHash("0xbbb")
	local := uint64(100)
	reports := []bootHashReport{
		{peer: peer.ID("a1"), rpcHeight: local, hash: h1, hashOK: true},
		{peer: peer.ID("a2"), rpcHeight: local, hash: h1, hashOK: true},
		{peer: peer.ID("a3"), rpcHeight: local, hash: h1, hashOK: true},
		{peer: peer.ID("b1"), rpcHeight: local, hash: h2, hashOK: true},
		{peer: peer.ID("b2"), rpcHeight: local, hash: h2, hashOK: true},
		{peer: peer.ID("b3"), rpcHeight: local, hash: h2, hashOK: true},
	}
	maj := computeBootHashMajority(local, reports)
	require.False(t, maj.ok)
}

func TestHashQuorumRequiredVotes(t *testing.T) {
	require.Equal(t, 4, hashQuorumRequiredVotes(7))
	require.Equal(t, 3, hashQuorumRequiredVotes(4))
	require.Equal(t, 2, hashQuorumRequiredVotes(2))
}

func TestBootHashDisagreesWithLocal_zhuzhu1Scenario(t *testing.T) {
	majority := types.StringToHash("0x83241a23690f3e667fe6579c0b32a835cdfbbc9ada6d0e64170d238d0578fd39")
	minority := types.StringToHash("0xd49aeefd9f0e5b891f97579c6e8c03b34d6421006f493b19293f73ff0daa5970")
	reports := make([]bootHashReport, 0, 7)
	for i := 0; i < 6; i++ {
		reports = append(reports, bootHashReport{hash: majority, hashOK: true, rpcHeight: 15952487})
	}
	reports = append(reports, bootHashReport{hash: minority, hashOK: true, rpcHeight: 15952555})
	disagree, agree := bootHashDisagreesWithLocal(minority, reports)
	require.Equal(t, 6, disagree)
	require.Equal(t, 1, agree) // outlier boot 与本地同孤链 hash
	require.True(t, disagree >= 2 && disagree > agree)

	disagree2, agree2 := bootHashDisagreesWithLocal(majority, reports)
	require.Equal(t, 1, disagree2)
	require.Equal(t, 6, agree2)
	require.False(t, disagree2 >= 2 && disagree2 > agree2)
}

func TestComputeBootHashMajority_tipUsesMajorityRPCOnly(t *testing.T) {
	majority := types.StringToHash("0x83241a23690f3e667fe6579c0b32a835cdfbbc9ada6d0e64170d238d0578fd39")
	outlier := types.StringToHash("0xd49aeefd9f0e5b891f97579c6e8c03b34d6421006f493b19293f73ff0daa5970")
	local := uint64(15952487)
	reports := []bootHashReport{
		{hash: majority, hashOK: true, rpcHeight: 15952487},
		{hash: majority, hashOK: true, rpcHeight: 15952488},
		{hash: majority, hashOK: true, rpcHeight: 15952489},
		{hash: outlier, hashOK: true, rpcHeight: 15952555},
	}
	maj := computeBootHashMajority(local, reports)
	require.True(t, maj.ok)
	require.Equal(t, uint64(15952489), maj.tip)
}
