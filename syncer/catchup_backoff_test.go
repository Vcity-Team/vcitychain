package syncer

import (
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestTrustedAheadOfLocal(t *testing.T) {
	ahead := trustedTipResult{Branch: trustedTipBranchMaxCluster, Tip: 101, MaxBootHeight: 101}
	require.True(t, trustedAheadOfLocal(ahead, 100))

	caught := trustedTipResult{Branch: trustedTipBranchMaxCluster, Tip: 100, MaxBootHeight: 100}
	require.False(t, trustedAheadOfLocal(caught, 100))

	noQ := trustedTipResult{Branch: trustedTipBranchNoQuorum, Tip: 105}
	require.False(t, trustedAheadOfLocal(noQ, 100))
}

func TestCatchUpRetryBackoff(t *testing.T) {
	s := &syncer{logger: hclog.NewNullLogger()}
	ahead := trustedTipResult{Branch: trustedTipBranchMaxCluster, Tip: 101}
	require.Equal(t, trustedAheadCatchUpRetryBackoff, s.catchUpRetryBackoff(100, ahead))

	caught := trustedTipResult{Branch: trustedTipBranchMaxCluster, Tip: 100}
	require.Equal(t, syncBestNotAheadWakeInterval, s.catchUpRetryBackoff(100, caught))

	s9 := &syncer{logger: hclog.NewNullLogger(), blockTimeout: 9 * time.Second}
	require.Equal(t, time.Second, s9.caughtUpWakeBackoff())
	require.Equal(t, time.Second, s9.catchUpRetryBackoff(100, caught))
}

func TestCaughtUpWakeBackoff(t *testing.T) {
	require.Equal(t, syncBestNotAheadWakeInterval, (&syncer{}).caughtUpWakeBackoff())

	s := &syncer{blockTimeout: 9 * time.Second}
	require.Equal(t, time.Second, s.caughtUpWakeBackoff())

	sShort := &syncer{blockTimeout: 2 * time.Second}
	require.Equal(t, syncCaughtUpMinWakeInterval, sShort.caughtUpWakeBackoff())

	sLong := &syncer{blockTimeout: 60 * time.Second}
	require.Equal(t, syncCaughtUpMaxWakeInterval, sLong.caughtUpWakeBackoff())
}
