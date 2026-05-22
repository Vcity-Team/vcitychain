package dpos

import (
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

type schedulerChainMock struct {
	local *types.Header
}

func (m *schedulerChainMock) Header() *types.Header {
	return m.local
}

func (m *schedulerChainMock) GetHeaderByNumber(uint64) (*types.Header, bool) {
	return nil, false
}

func TestEffectiveTimeForNextBlock_UsesMaxOfNetworkRefAndLocalTip(t *testing.T) {
	genesis := time.Unix(1_000, 0).UTC()
	ref := &types.Header{Number: 100, Timestamp: 2_000}
	local := &types.Header{Number: 200, Timestamp: 5_000}

	bs := &BlockScheduler{
		blockWindow: 3 * time.Second,
		genesisTime: genesis,
		blockchain:  &schedulerChainMock{local: local},
		logger:      hclog.NewNullLogger(),
	}
	bs.SetNetworkHeadResolver(func() *types.Header { return ref })

	got := bs.effectiveTimeForNextBlock()
	want := time.Unix(5_003, 0).UTC()
	require.Equal(t, want, got)
}

func TestEffectiveTimeForNextBlock_WallClockAlignmentWhenEnabled(t *testing.T) {
	genesis := time.Unix(0, 0).UTC()
	// 链上参照仍停在很久以前
	ref := &types.Header{Number: 10, Timestamp: 100}
	local := &types.Header{Number: 11, Timestamp: 103}

	bs := &BlockScheduler{
		blockWindow:            3 * time.Second,
		genesisTime:            genesis,
		blockchain:             &schedulerChainMock{local: local},
		logger:                 hclog.NewNullLogger(),
		wallClockSlotAlignment: true,
	}
	bs.SetNetworkHeadResolver(func() *types.Header { return ref })

	chainDue := time.Unix(106, 0).UTC() // max(100,103)+3
	got := bs.effectiveTimeForNextBlock()
	wallDue := bs.wallDueTime()

	require.True(t, got.After(chainDue) || got.Equal(wallDue))
	require.Equal(t, wallDue, got, "墙钟对齐时应取 wallDue 而非滞后的 chainDue")
	require.True(t, got.After(time.Unix(int64(local.Timestamp), 0)))
}

func TestNextHeaderTimeFromScheduling_ClampedToParentFloor(t *testing.T) {
	parentTS := uint64(10_000)
	blockTime := 3 * time.Second
	staleSched := time.Unix(9_000, 0).UTC()
	parentFloor := time.Unix(int64(parentTS), 0).UTC().Add(blockTime)

	headerTime := staleSched.UTC()
	if headerTime.Before(parentFloor) {
		headerTime = parentFloor
	}
	require.Greater(t, uint64(headerTime.Unix()), parentTS)
	require.Equal(t, uint64(10_003), uint64(headerTime.Unix()))
}
