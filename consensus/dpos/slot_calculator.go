package dpos

import (
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
)

// calculateCurrentSlot 计算当前时间 slot
func (d *DPoS) calculateCurrentSlot() (int, error) {
	if d.runtime == nil || d.runtime.config == nil || d.runtime.config.blockScheduler == nil {
		return 0, ErrRuntimeNotInitialized
	}
	now := time.Now()
	genesisTime := d.runtime.config.blockScheduler.GetGenesisTime()
	blockWindow := d.runtime.config.blockScheduler.GetBlockWindow()
	timeSinceGenesis := now.Sub(genesisTime)
	return int(timeSinceGenesis / blockWindow), nil
}

// calculateCurrentValidatorIndex 计算当前验证者索引
func (d *DPoS) calculateCurrentValidatorIndex(validators validator.AccountSet) (int, error) {
	slot, err := d.calculateCurrentSlot()
	if err != nil {
		return 0, err
	}
	if len(validators) == 0 {
		return 0, ErrRuntimeNotInitialized
	}
	return slot % len(validators), nil
}

