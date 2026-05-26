package syncer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatchUpBurstLimit(t *testing.T) {
	require.Equal(t, 0, catchUpBurstLimit(0))
	require.Equal(t, 1, catchUpBurstLimit(1))
	require.Equal(t, 4, catchUpBurstLimit(4))
	require.Equal(t, catchUpBurstSmallLagMax, catchUpBurstLimit(20))
	require.Equal(t, catchUpBurstBlocksMedium, catchUpBurstLimit(100))
	require.Equal(t, catchUpBurstBlocksMedium, catchUpBurstLimit(32))
	require.Equal(t, catchUpBurstBlocksLarge, catchUpBurstLimit(500))
	require.Equal(t, catchUpBurstBlocksHuge, catchUpBurstLimit(2000))
}
