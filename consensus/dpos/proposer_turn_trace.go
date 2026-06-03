package dpos

import (
	"fmt"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// proposerTurnTraceState 按 plannedNext 高度去重，避免 500ms 监测循环刷屏。
type proposerTurnTraceState struct {
	lastSlotDiagPlanned uint64
	lastBlockStages     map[uint64]map[string]struct{} // plannedNext -> stage set
}

func (r *dposRuntime) proposerTraceState() *proposerTurnTraceState {
	if r.proposerTurnTrace == nil {
		r.proposerTurnTrace = &proposerTurnTraceState{
			lastBlockStages: make(map[uint64]map[string]struct{}),
		}
	}
	return r.proposerTurnTrace
}

// traceMyProposerTurnBlocked 轮到本节点但未触发 [出块验证] / 本地出块时，记录拦截阶段与原因（每高度每 stage 一条）。
func (r *dposRuntime) traceMyProposerTurnBlocked(
	stage, reason string,
	localTip uint64,
	validators []types.Address,
	myAddress types.Address,
	eligible func(types.Address) bool,
	extra ...interface{},
) {
	if r.config == nil || r.config.blockScheduler == nil {
		return
	}
	if !r.config.blockScheduler.DesignatedProposerForNext(localTip, myAddress, validators, eligible) {
		return
	}
	planned := localTip + 1
	r.proposerTraceMu.Lock()
	if !r.proposerTraceStageOnceLocked(planned, stage) {
		r.proposerTraceMu.Unlock()
		return
	}
	r.proposerTraceMu.Unlock()
	args := []interface{}{
		"stage", stage,
		"blockedReason", reason,
		"localTip", localTip,
		"plannedNextBlockNumber", planned,
		"myAddress", myAddress.String(),
		"myValidatorIndex", myValidatorElectionIndex(validators, myAddress),
		"note", "轮值轮到本节点但未出现 [出块验证] ShouldProduceBlockNow返回true",
	}
	args = append(args, extra...)
	r.logger.Info("🚧 [出块追踪] 轮值轮到本节点但被拦截", args...)
}

func (r *dposRuntime) proposerTraceStageOnceLocked(plannedNext uint64, stage string) bool {
	st := r.proposerTraceState()
	if st.lastBlockStages[plannedNext] == nil {
		st.lastBlockStages[plannedNext] = make(map[string]struct{})
	}
	if _, ok := st.lastBlockStages[plannedNext][stage]; ok {
		return false
	}
	st.lastBlockStages[plannedNext][stage] = struct{}{}
	if len(st.lastBlockStages) > 256 {
		for h := range st.lastBlockStages {
			if h+256 < plannedNext {
				delete(st.lastBlockStages, h)
			}
		}
	}
	return true
}

func myValidatorElectionIndex(validators []types.Address, myAddress types.Address) int {
	ordered := orderValidatorAddressesForLeaderElection(validators)
	for i, v := range ordered {
		if v == myAddress {
			return i
		}
	}
	return -1
}

// emitProposerSlotDiagnostic 每个 plannedNext 高度打一条：当前链上 proposer 是谁、是否轮到本节点。
func (r *dposRuntime) emitProposerSlotDiagnostic(
	localTip uint64,
	validators []types.Address,
	myAddress types.Address,
	eligible func(types.Address) bool,
) {
	if r.config == nil || r.config.blockScheduler == nil {
		return
	}
	planned := localTip + 1
	r.proposerTraceMu.Lock()
	st := r.proposerTraceState()
	if st.lastSlotDiagPlanned == planned {
		r.proposerTraceMu.Unlock()
		return
	}
	st.lastSlotDiagPlanned = planned
	r.proposerTraceMu.Unlock()

	bs := r.config.blockScheduler
	ordered := orderValidatorAddressesForLeaderElection(validators)
	n := len(ordered)
	if n == 0 {
		return
	}

	myIdx := myValidatorElectionIndex(validators, myAddress)
	proposerSlot := bs.ProposerSlotForTip(localTip)
	proposerIdx := proposerSlot % n
	expected := ordered[proposerIdx]
	isMyTurn := expected == myAddress
	if isMyTurn && eligible != nil && !eligible(expected) {
		isMyTurn = false
	}

	nextExists := false
	if r.config.blockchain != nil {
		_, nextExists = r.config.blockchain.GetHeaderByNumber(planned)
	}

	now := time.Now().UTC()
	schedulingTime := bs.schedulingTimeForTip(localTip)
	chainSlot := bs.slotAt(schedulingTime)
	wallSlot := bs.slotAt(now)
	lagBehind := uint64(0)
	if tip := r.canonicalGateTip(); tip > localTip {
		lagBehind = tip - localTip
	}

	r.logger.Info("📊 [出块追踪] 下一块 proposer 轮值快照（每高度一条）",
		"localTip", localTip,
		"plannedNextBlockNumber", planned,
		"proposerSlot", proposerSlot,
		"proposerIndex", proposerIdx,
		"expectedProposer", fmt.Sprintf("[%d]%s", proposerIdx, expected.String()),
		"myAddress", myAddress.String(),
		"myValidatorIndex", myIdx,
		"isMyTurn", isMyTurn,
		"nextBlockAlreadyInDB", nextExists,
		"chainSlot", chainSlot,
		"wallSlot", wallSlot,
		"wallAheadOfChainSlots", wallSlot-chainSlot,
		"schedulingTimeUTC", schedulingTime.Format("2006-01-02 15:04:05.000"),
		"chainSchedulingDueAbsoluteUTC", bs.ChainSchedulingDueUTC().Format("2006-01-02 15:04:05.000"),
		"earliestProduceWallUTC", bs.earliestProduceWallUTC(now).Format("2006-01-02 15:04:05.000"),
		"chainTipAheadOfWall", bs.chainTipAheadOfWall(now).String(),
		"trustedCanonicalTip", r.canonicalGateTip(),
		"lagBehindTrustedTip", lagBehind,
		"activeValidatorCount", n,
		"slotsUntilMyTurn", func() int {
			if myIdx < 0 {
				return -1
			}
			return (proposerIdx - myIdx + n) % n
		}(),
	)
}