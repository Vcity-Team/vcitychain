package dpos

import (
	consensusmodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/consensus"
	"github.com/Vcity-Team/vcitychain/types"
)

// initConsensusModule wires the shared consensus module as the default ConsensusManager implementation.
func (d *DPoS) initConsensusModule() {
	if d.consensus != nil {
		return
	}

	deps := consensusmodule.Dependencies{
		BuildBlock: func() (*types.FullBlock, error) {
			return d.buildConsensusBlock(nil)
		},
		ValidateBlock:      d.validateConsensusBlock,
		ShouldProduceBlock: d.shouldProduceConsensusBlock,
		IsEpochEndBlock:    d.isEpochEndConsensusBlock,
		Logger:             d.logger.Named("consensus_module"),
	}

	d.consensus = consensusmodule.NewManager(deps)
}

func (d *DPoS) buildConsensusBlock(parent *types.Header) (*types.FullBlock, error) {
	if d.runtime == nil {
		return nil, ErrRuntimeNotInitialized
	}
	block, err := d.runtime.buildBlock()
	if err != nil {
		return nil, WrapError("build consensus block", err)
	}
	return block, nil
}

func (d *DPoS) validateConsensusBlock(block *types.Block) error {
	if d.blockchain == nil {
		return ErrBlockchainNotAvailable
	}
	wrapper, ok := d.blockchain.(*blockchainWrapper)
	if !ok {
		return ErrBlockchainNotAvailable
	}
	if d.config == nil || d.config.Blockchain == nil {
		return ErrBlockchainConfigNotInitialized
	}
	parent := d.config.Blockchain.Header()
	if parent == nil {
		return ErrParentHeaderNotFound
	}
	_, err := wrapper.ProcessBlock(parent, block)
	if err != nil {
		return WrapError("validate consensus block", err)
	}
	return nil
}

func (d *DPoS) shouldProduceConsensusBlock(blockNumber uint64, myAddress types.Address) bool {
	if d.blockScheduler == nil {
		return false
	}

	validators := d.GetValidators()
	if len(validators) == 0 {
		return false
	}

	// 获取当前区块头（用于时间间隔检查）
	var currentBlock *types.Header
	if d.config != nil && d.config.Blockchain != nil {
		currentBlock = d.config.Blockchain.Header()
	}

	addresses := make([]types.Address, len(validators))
	for i, v := range validators {
		addresses[i] = v.Address
	}

	return d.blockScheduler.ShouldProduceBlockNow(
		myAddress,
		addresses,
		blockNumber,
		currentBlock,
		"ConsensusModule",
	)
}

func (d *DPoS) isEpochEndConsensusBlock(blockNumber uint64) bool {
	if d.runtime != nil {
		return d.runtime.isEpochEndBlock(blockNumber)
	}
	return false
}
