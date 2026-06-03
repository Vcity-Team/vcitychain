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

func TestEarliestProduceTime_SyncedTipAheadOfWallUsesNowPlusBlockWindow(t *testing.T) {
	// 模拟：墙钟 06:53:10 sync 写入块头 06:53:13 的链尖 → 下一块墙钟约 now+3s，而非绝对 06:53:16。
	parentTS := time.Date(2026, 6, 3, 6, 53, 13, 0, time.UTC)
	now := time.Date(2026, 6, 3, 6, 53, 10, 767_000_000, time.UTC)
	local := &types.Header{Number: 16064232, Timestamp: uint64(parentTS.Unix())}

	bs := &BlockScheduler{
		blockWindow: 3 * time.Second,
		genesisTime: time.Unix(0, 0).UTC(),
		blockchain:  &schedulerChainMock{local: local},
		logger:      hclog.NewNullLogger(),
	}

	chainDue := bs.ChainSchedulingDueUTC()
	require.Equal(t, time.Date(2026, 6, 3, 6, 53, 16, 0, time.UTC), chainDue)

	got := bs.earliestProduceWallUTC(now)
	want := now.Add(3 * time.Second)
	require.Equal(t, want, got)

	ahead := bs.chainTipAheadOfWall(now)
	require.InDelta(t, 2.233, ahead.Seconds(), 0.01)
}

func TestEarliestProduceTime_WallPastTipBeforeChainDueAllowsNow(t *testing.T) {
	// 墙钟已 >= 父块 timestamp，但绝对 chainDue 仍未到：应立即允许（不再跳回 chainDue 等待）。
	parentTS := time.Date(2026, 6, 3, 7, 21, 34, 0, time.UTC)
	now := time.Date(2026, 6, 3, 7, 21, 36, 0, time.UTC)
	local := &types.Header{Number: 16064495, Timestamp: uint64(parentTS.Unix())}

	bs := &BlockScheduler{
		blockWindow: 3 * time.Second,
		genesisTime: time.Unix(0, 0).UTC(),
		blockchain:  &schedulerChainMock{local: local},
		logger:      hclog.NewNullLogger(),
	}

	require.Equal(t, time.Date(2026, 6, 3, 7, 21, 37, 0, time.UTC), bs.ChainSchedulingDueUTC())
	require.Equal(t, now, bs.earliestProduceWallUTC(now))
}

func TestEarliestProduceTime_WallClockAlignmentNotAlwaysFuture(t *testing.T) {
	genesis := time.Unix(0, 0).UTC()
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

	earliest := bs.EarliestProduceTime(time.Time{})
	now := time.Now().UTC()
	require.False(t, now.Before(earliest),
		"墙钟对齐时 EarliestProduceTime 不得恒在未来；earliest=%s now=%s",
		earliest.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))

	headerDue := bs.effectiveTimeForNextBlock()
	require.True(t, headerDue.After(earliest) || headerDue.Equal(earliest),
		"块头调度时间仍应使用墙钟对齐的 effectiveTimeForNextBlock")
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
