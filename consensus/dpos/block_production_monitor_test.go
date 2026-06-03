package dpos

import (
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestProduceWakeDuration_NoScheduler(t *testing.T) {
	t.Parallel()

	r := &dposRuntime{}
	require.Equal(t, blockProductionFallbackDelay, r.produceWakeDuration())
}

func TestProduceWakeDuration_EarliestPassed(t *testing.T) {
	t.Parallel()

	parentTS := time.Date(2026, 6, 3, 7, 31, 1, 0, time.UTC)
	local := &types.Header{Number: 100, Timestamp: uint64(parentTS.Unix())}
	bs := &BlockScheduler{
		blockWindow: 3 * time.Second,
		genesisTime: time.Unix(0, 0).UTC(),
		blockchain:  &schedulerChainMock{local: local},
		logger:      hclog.NewNullLogger(),
	}
	r := &dposRuntime{
		config: &runtimeConfig{blockScheduler: bs},
	}

	require.Equal(t, blockProductionRecheckDelay, r.produceWakeDuration())
}

func TestProduceWakeDuration_NonDesignatedCapsAtPollInterval(t *testing.T) {
	t.Parallel()

	parentTS := time.Now().UTC().Add(-time.Second)
	local := &types.Header{Number: 100, Timestamp: uint64(parentTS.Unix())}
	bs := &BlockScheduler{
		blockWindow: 3 * time.Second,
		genesisTime: time.Unix(0, 0).UTC(),
		blockchain:  &schedulerChainMock{local: local},
		logger:      hclog.NewNullLogger(),
	}
	r := &dposRuntime{
		config: &runtimeConfig{blockScheduler: bs},
	}

	got := r.produceWakeDuration()
	require.Equal(t, blockProductionPollInterval, got)
}

func TestProduceWakeDuration_NonDesignatedShortWait(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	parentTS := now.Add(-2500 * time.Millisecond)
	local := &types.Header{Number: 100, Timestamp: uint64(parentTS.Unix())}
	bs := &BlockScheduler{
		blockWindow: 3 * time.Second,
		genesisTime: time.Unix(0, 0).UTC(),
		blockchain:  &schedulerChainMock{local: local},
		logger:      hclog.NewNullLogger(),
	}
	r := &dposRuntime{
		config: &runtimeConfig{blockScheduler: bs},
	}

	got := r.produceWakeDuration()
	require.Greater(t, got, blockProductionRecheckDelay)
	require.LessOrEqual(t, got, blockProductionPollInterval)
}

func TestRearmProduceTimer_DoesNotPanic(t *testing.T) {
	t.Parallel()

	r := &dposRuntime{}
	timer := time.NewTimer(blockProductionRecheckDelay)
	defer timer.Stop()

	require.NotPanics(t, func() {
		r.rearmProduceTimer(timer)
	})
}
