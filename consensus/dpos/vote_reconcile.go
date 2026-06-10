package dpos

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// VoteReconcileStats 单地址对账结果。
type VoteReconcileStats struct {
	Voter            types.Address
	AppliedCount     int
	VoidedCount      int
	TrimmedWei       *big.Int
	PendingFixed     int
	AppliedOverdue   int
	RemainingApplied *big.Int
	RemainingPending *big.Int
}

// ApplyScheduledVotesUpTo 补跑所有 effectiveEpoch<=currentEpoch 且未应用的投票。
// maxCount=0 表示不限制批次大小。
func (d *DPoS) ApplyScheduledVotesUpTo(blockNumber uint64, maxCount int) (applied int, voided int, err error) {
	if blockNumber == 0 {
		blockNumber = d.getCurrentBlockNumber()
	}
	if blockNumber == 0 {
		return 0, 0, fmt.Errorf("cannot get current block number")
	}
	meta := d.getEpochForBlock(blockNumber)
	if meta == nil {
		return 0, 0, fmt.Errorf("cannot get epoch for block %d", blockNumber)
	}
	currentEpoch := meta.Number
	if currentEpoch == 0 {
		return 0, 0, fmt.Errorf("epoch is 0 for block %d", blockNumber)
	}

	d.lock.Lock()
	defer d.lock.Unlock()

	records := d.collectPendingVotesUpTo(currentEpoch)
	sort.Slice(records, func(i, j int) bool {
		if records[i].EffectiveEpoch != records[j].EffectiveEpoch {
			return records[i].EffectiveEpoch < records[j].EffectiveEpoch
		}
		return records[i].Timestamp < records[j].Timestamp
	})

	limit := len(records)
	if maxCount > 0 && maxCount < limit {
		limit = maxCount
	}

	for i := 0; i < limit; i++ {
		rec := records[i]
		ok, void, applyErr := d.applyOrVoidPendingVoteRecord(rec, blockNumber)
		if applyErr != nil {
			d.logger.Warn("ApplyScheduledVotesUpTo: record failed",
				"voter", rec.Voter.String(),
				"delegate", rec.Delegate.String(),
				"error", applyErr)
			continue
		}
		if void {
			voided++
		} else if ok {
			applied++
		}
	}
	return applied, voided, nil
}

// ReconcileVoter 对单个投票者做对账：补跑逾期 pending、裁剪超额 applied。
func (d *DPoS) ReconcileVoter(voter types.Address, blockNumber uint64) (*VoteReconcileStats, error) {
	if voter == (types.Address{}) {
		return nil, fmt.Errorf("invalid voter address")
	}
	if blockNumber == 0 {
		blockNumber = d.getCurrentBlockNumber()
	}
	if blockNumber == 0 {
		return nil, fmt.Errorf("cannot get current block number")
	}
	meta := d.getEpochForBlock(blockNumber)
	if meta == nil {
		return nil, fmt.Errorf("cannot get epoch for block %d", blockNumber)
	}
	currentEpoch := meta.Number

	d.lock.Lock()
	defer d.lock.Unlock()

	stats := &VoteReconcileStats{
		Voter:      voter,
		TrimmedWei: big.NewInt(0),
	}

	records := d.collectPendingVotesUpToForVoter(voter, currentEpoch)
	sort.Slice(records, func(i, j int) bool {
		if records[i].EffectiveEpoch != records[j].EffectiveEpoch {
			return records[i].EffectiveEpoch < records[j].EffectiveEpoch
		}
		return records[i].Timestamp < records[j].Timestamp
	})
	for _, rec := range records {
		ok, void, applyErr := d.applyOrVoidPendingVoteRecord(rec, blockNumber)
		if applyErr != nil {
			return stats, applyErr
		}
		if void {
			stats.VoidedCount++
		} else if ok {
			stats.AppliedCount++
			if rec.EffectiveEpoch < currentEpoch {
				stats.AppliedOverdue++
			}
			stats.PendingFixed++
		}
	}

	balance := d.queryVoterNativeBalance(voter)
	trimmed, err := d.trimAppliedStakesToBalance(voter, balance)
	if err != nil {
		return stats, err
	}
	if trimmed != nil && trimmed.Sign() > 0 {
		stats.TrimmedWei.Add(stats.TrimmedWei, trimmed)
	}

	stats.RemainingApplied = d.calculateTotalVotedAmount(voter)
	stats.RemainingPending = d.calculatePendingScheduledAmount(voter)
	return stats, nil
}

func (d *DPoS) collectPendingVotesUpTo(maxEpoch uint64) []*VoteRecord {
	var out []*VoteRecord
	if d.state == nil || d.state.StakeStore == nil {
		return out
	}
	infos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return out
	}
	for _, si := range infos {
		rec := stakeInfoToPendingVoteRecord(si, maxEpoch)
		if rec != nil {
			out = append(out, rec)
		}
	}
	return out
}

func (d *DPoS) collectPendingVotesUpToForVoter(voter types.Address, maxEpoch uint64) []*VoteRecord {
	var out []*VoteRecord
	if d.state == nil || d.state.StakeStore == nil {
		return out
	}
	infos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return out
	}
	for _, si := range infos {
		if si == nil || si.Staker != voter {
			continue
		}
		if rec := stakeInfoToPendingVoteRecord(si, maxEpoch); rec != nil {
			out = append(out, rec)
		}
	}
	return out
}

func stakeInfoToPendingVoteRecord(si *StakeInfo, maxEpoch uint64) *VoteRecord {
	if si == nil || si.Applied || !si.IsActive {
		return nil
	}
	if si.Amount == nil || si.Amount.Sign() <= 0 {
		return nil
	}
	if si.EffectiveEpoch > maxEpoch {
		return nil
	}
	return &VoteRecord{
		Voter:          si.Staker,
		Delegate:       si.Delegate,
		Amount:         new(big.Int).Set(si.Amount),
		Timestamp:      si.StartTime,
		EffectiveEpoch: si.EffectiveEpoch,
		Applied:        false,
	}
}

func (d *DPoS) applyOrVoidPendingVoteRecord(rec *VoteRecord, blockNumber uint64) (applied bool, voided bool, err error) {
	if rec == nil {
		return false, false, nil
	}
	if d.isBoundaryStakeApplied(rec.Voter, rec.Delegate, rec.Timestamp) {
		return true, false, nil
	}

	balance := d.queryVoterNativeBalance(rec.Voter)
	if balance.Cmp(rec.Amount) < 0 {
		if voidErr := d.voidStakeRecord(rec.Voter, rec.Delegate, rec.Timestamp, nil); voidErr != nil {
			return false, false, voidErr
		}
		d.logger.Info("voided pending vote (insufficient balance)",
			"voter", rec.Voter.String(),
			"delegate", rec.Delegate.String(),
			"amount", rec.Amount.String(),
			"balance", balance.String(),
			"blockNumber", blockNumber)
		return false, true, nil
	}

	vote := &VoteMessage{
		Voter:          rec.Voter,
		Delegate:       rec.Delegate,
		Amount:         rec.Amount,
		Round:          d.currentRound,
		Timestamp:      rec.Timestamp,
		EffectiveEpoch: rec.EffectiveEpoch,
		Applied:        true,
	}
	if err := d.processVoteInternal(vote); err != nil {
		if voidErr := d.voidStakeRecord(rec.Voter, rec.Delegate, rec.Timestamp, nil); voidErr != nil {
			return false, false, voidErr
		}
		d.logger.Warn("voided pending vote after apply failure",
			"voter", rec.Voter.String(),
			"delegate", rec.Delegate.String(),
			"error", err)
		return false, true, nil
	}
	if !d.persistBoundaryVoteApplied(rec) {
		return false, false, fmt.Errorf("failed to persist applied flag for %s", rec.Voter.String())
	}
	d.markBoundaryVotesAppliedInMemory([]*VoteRecord{rec})
	d.pendingValidatorUpdate = true
	return true, false, nil
}

func (d *DPoS) queryVoterNativeBalance(voter types.Address) *big.Int {
	if d.balanceQuerier == nil {
		return big.NewInt(0)
	}
	bal, err := d.balanceQuerier.GetNativeTokenBalance(voter)
	if err != nil || bal == nil {
		return big.NewInt(0)
	}
	return bal
}

// voidStakeRecord 将质押记录置为无效；若已 applied 则同步扣减 delegate 权重。
func (d *DPoS) voidStakeRecord(voter, delegate types.Address, startTime uint64, dbTx *bolt.Tx) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("stake store not available")
	}

	ownTx := dbTx == nil
	if ownTx {
		tx, err := d.state.beginDBTransaction(true)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		dbTx = tx
	}

	stake, err := d.findStakeRecord(voter, delegate, startTime)
	if err != nil {
		return err
	}
	if stake == nil {
		if ownTx {
			return dbTx.Commit()
		}
		return nil
	}

	prevAmount := big.NewInt(0)
	if stake.Amount != nil {
		prevAmount.Set(stake.Amount)
	}
	if stake.Applied && prevAmount.Sign() > 0 {
		currentPower, _ := d.getVotingPowerFromDatabaseWithTx(stake.Delegate, dbTx)
		if currentPower == nil {
			currentPower = big.NewInt(0)
		}
		newPower := new(big.Int).Sub(currentPower, prevAmount)
		if newPower.Sign() < 0 {
			newPower = big.NewInt(0)
		}
		if err := d.updateVotingPowerInDatabaseWithTx(stake.Delegate, newPower, dbTx); err != nil {
			return err
		}
	}

	stake.Amount = big.NewInt(0)
	stake.IsActive = false
	if err := d.state.StakeStore.setStakingInfo(voter, stake, startTime, dbTx); err != nil {
		return err
	}

	if ownTx {
		return dbTx.Commit()
	}
	return nil
}

func (d *DPoS) findStakeRecord(voter, delegate types.Address, startTime uint64) (*StakeInfo, error) {
	infos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return nil, err
	}
	for _, si := range infos {
		if si == nil {
			continue
		}
		if si.Staker == voter && si.Delegate == delegate && si.StartTime == startTime {
			return si, nil
		}
	}
	return nil, nil
}

func (d *DPoS) getVotingPowerFromDatabaseWithTx(delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	if d.state == nil || d.state.StakeStore == nil {
		return big.NewInt(0), nil
	}
	info, err := d.state.StakeStore.getDelegateInfo(delegate, dbTx)
	if err != nil || info == nil || info.VotingPower == nil {
		return big.NewInt(0), err
	}
	return new(big.Int).Set(info.VotingPower), nil
}

// trimAppliedStakesToBalance 当已生效投票总额超过余额时，从新到旧裁剪。
func (d *DPoS) trimAppliedStakesToBalance(voter types.Address, balance *big.Int) (*big.Int, error) {
	if d.state == nil || d.state.StakeStore == nil {
		return big.NewInt(0), nil
	}
	if balance == nil {
		balance = big.NewInt(0)
	}

	infos, err := d.state.StakeStore.GetStakingInfo()
	if err != nil {
		return nil, err
	}

	var applied []*StakeInfo
	totalApplied := big.NewInt(0)
	for _, si := range infos {
		if si == nil || si.Staker != voter || !si.Applied || !si.IsActive {
			continue
		}
		if si.Amount == nil || si.Amount.Sign() <= 0 {
			continue
		}
		applied = append(applied, si)
		totalApplied.Add(totalApplied, si.Amount)
	}
	if totalApplied.Cmp(balance) <= 0 {
		return big.NewInt(0), nil
	}

	excess := new(big.Int).Sub(totalApplied, balance)
	trimmedTotal := big.NewInt(0)

	sort.Slice(applied, func(i, j int) bool {
		return applied[i].StartTime > applied[j].StartTime
	})

	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		return nil, err
	}
	defer dbTx.Rollback()

	remainingExcess := new(big.Int).Set(excess)
	for _, stake := range applied {
		if remainingExcess.Sign() <= 0 {
			break
		}
		cut := new(big.Int)
		if stake.Amount.Cmp(remainingExcess) <= 0 {
			cut.Set(stake.Amount)
		} else {
			cut.Set(remainingExcess)
		}
		if cut.Sign() <= 0 {
			continue
		}

		currentPower, _ := d.getVotingPowerFromDatabaseWithTx(stake.Delegate, dbTx)
		if currentPower == nil {
			currentPower = big.NewInt(0)
		}
		newPower := new(big.Int).Sub(currentPower, cut)
		if newPower.Sign() < 0 {
			newPower = big.NewInt(0)
		}
		if err := d.updateVotingPowerInDatabaseWithTx(stake.Delegate, newPower, dbTx); err != nil {
			return nil, err
		}

		stake.Amount.Sub(stake.Amount, cut)
		if stake.Amount.Sign() <= 0 {
			stake.Amount = big.NewInt(0)
			stake.IsActive = false
		}
		if err := d.state.StakeStore.setStakingInfo(voter, stake, stake.StartTime, dbTx); err != nil {
			return nil, err
		}

		trimmedTotal.Add(trimmedTotal, cut)
		remainingExcess.Sub(remainingExcess, cut)
	}

	if err := dbTx.Commit(); err != nil {
		return nil, err
	}
	d.pendingValidatorUpdate = true
	return trimmedTotal, nil
}
