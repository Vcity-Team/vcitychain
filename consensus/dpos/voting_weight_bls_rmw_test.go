package dpos

import (
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"

	"github.com/Vcity-Team/vcitychain/types"
)

// TestUpdateVotingPowerPreservesBlsPublicKey ensures vote-weight updates are RMW
// and do not wipe DelegateInfo.BlsPublicKey (root cause of Extra missing keys).
func TestUpdateVotingPowerPreservesBlsPublicKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dpos.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store := &StakeStore{db: db}
	addr := types.StringToAddress("0x1111111111111111111111111111111111111111")
	blsBytes := make([]byte, 128)
	for i := range blsBytes {
		blsBytes[i] = byte(i + 1)
	}
	require.NoError(t, store.setDelegateInfo(addr, &DelegateInfo{
		Address:      addr,
		VotingPower:  big.NewInt(1000),
		TotalVotes:   big.NewInt(1000),
		IsActive:     true,
		IsRegistered: true,
		BlsPublicKey: blsBytes,
	}, nil))

	d := &DPoS{
		logger: hclog.NewNullLogger(),
		state:  &State{StakeStore: store},
	}

	require.NoError(t, d.updateVotingPowerInDatabase(addr, big.NewInt(2500)))

	got, err := store.GetDelegateInfo(addr)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, int64(2500), got.VotingPower.Int64())
	require.Equal(t, blsBytes, got.BlsPublicKey, "BlsPublicKey must survive voting-power update")
	require.True(t, got.IsActive)
}
