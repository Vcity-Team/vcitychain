package syncer

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestRotatePeerIDs(t *testing.T) {
	ids := []peer.ID{"a", "b", "c", "d"}
	require.Equal(t, []peer.ID{"a", "b", "c", "d"}, rotatePeerIDs(ids, 0))
	require.Equal(t, []peer.ID{"b", "c", "d", "a"}, rotatePeerIDs(ids, 1))
	require.Equal(t, []peer.ID{"d", "a", "b", "c"}, rotatePeerIDs(ids, 7))
}
