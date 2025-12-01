package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
)

// 启动批量处理工作协程
func (d *DPoS) startBatchWorkers() {
	for i := 0; i < d.batchProcessor.workerCount; i++ {
		d.batchProcessor.wg.Add(1)
		go d.batchWorker(i)
	}
}

// 批量处理工作协程
func (d *DPoS) batchWorker(id int) {
	defer d.batchProcessor.wg.Done()

	voteBatch := make([]*VoteMessage, 0, d.batchProcessor.batchSize)
	delegateBatch := make([]*DelegateMessage, 0, d.batchProcessor.batchSize)

	ticker := time.NewTicker(d.batchProcessor.batchTimeout)
	defer ticker.Stop()

	for {
		select {
		case vote := <-d.batchProcessor.voteQueue:
			voteBatch = append(voteBatch, vote)
			if len(voteBatch) >= d.batchProcessor.batchSize {
				d.processVoteBatch(voteBatch)
				voteBatch = voteBatch[:0]
			}

		case delegate := <-d.batchProcessor.delegateQueue:
			delegateBatch = append(delegateBatch, delegate)
			if len(delegateBatch) >= d.batchProcessor.batchSize {
				d.processDelegateBatch(delegateBatch)
				delegateBatch = delegateBatch[:0]
			}

		case <-ticker.C:
			if len(voteBatch) > 0 {
				d.processVoteBatch(voteBatch)
				voteBatch = voteBatch[:0]
			}
			if len(delegateBatch) > 0 {
				d.processDelegateBatch(delegateBatch)
				delegateBatch = delegateBatch[:0]
			}

		case <-d.batchProcessor.stopCh:
			// 处理剩余批次
			if len(voteBatch) > 0 {
				d.processVoteBatch(voteBatch)
			}
			if len(delegateBatch) > 0 {
				d.processDelegateBatch(delegateBatch)
			}
			return
		}
	}
}

// 批量处理委托
func (d *DPoS) processDelegateBatch(delegates []*DelegateMessage) {
	d.lock.Lock()
	defer d.lock.Unlock()

	for _, msg := range delegates {
		switch msg.Action {
		case "register":
			found := false
			for _, del := range d.delegates {
				if del.Address == msg.Delegate {
					found = true
					break
				}
			}
			if !found {
				d.addDelegateSafely(&validator.ValidatorMetadata{
					Address:     msg.Delegate,
					VotingPower: msg.Stake,
					IsActive:    true,
				})
			}

		case "unregister":
			newSet := validator.AccountSet{}
			for _, del := range d.delegates {
				if del.Address != msg.Delegate {
					newSet = append(newSet, del)
				}
			}
			d.delegates = newSet

		case "update":
			for _, del := range d.delegates {
				if del.Address == msg.Delegate {
					del.VotingPower = msg.Stake
					break
				}
			}
		}
	}

	// 更新指标
	d.metrics.lock.Lock()
	d.metrics.TotalDelegates = uint64(len(d.delegates))
	d.metrics.lock.Unlock()

	d.logger.Debug("processed delegate batch", "count", len(delegates))
}
