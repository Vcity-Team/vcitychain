package syncer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrustedCatchUpLag(t *testing.T) {
	meta := trustedTipResult{Branch: trustedTipBranchMaxCluster, Tip: 105, MaxBootHeight: 103}
	require.Equal(t, uint64(2), trustedCatchUpLag(meta, 103))
	require.Equal(t, uint64(0), trustedCatchUpLag(meta, 105))
}
