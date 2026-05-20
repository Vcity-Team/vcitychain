package dpos

import (
	"fmt"

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
		return nil, fmt.Errorf("runtime not initialized")
	}
	return d.runtime.buildBlock()
}

func (d *DPoS) validateConsensusBlock(block *types.Block) error {
	if d.blockchain == nil {
		return fmt.Errorf("blockchain wrapper not available")
	}
	wrapper, ok := d.blockchain.(*blockchainWrapper)
	if !ok {
		return fmt.Errorf("blockchain wrapper not available")
	}
	if d.config == nil || d.config.Blockchain == nil {
		return fmt.Errorf("blockchain config not initialized")
	}
	parent := d.config.Blockchain.Header()
	if parent == nil {
		return fmt.Errorf("parent header not found")
	}
	_, err := wrapper.ProcessBlock(parent, block)
	return err
}

func (d *DPoS) shouldProduceConsensusBlock(blockNumber uint64, myAddress types.Address) bool {
	if d.blockScheduler == nil {
		return false
	}

	validators := d.GetValidators()
	if len(validators) == 0 {
		return false
	}

	addresses := make([]types.Address, len(validators))
	for i, v := range validators {
		addresses[i] = v.Address
	}

	return d.blockScheduler.ShouldProduceBlockNow(
		myAddress,
		addresses,
		blockNumber,
		"ConsensusModule",
		nil,
	)
}

func (d *DPoS) isEpochEndConsensusBlock(blockNumber uint64) bool {
	if d.runtime != nil {
		return d.runtime.isEpochEndBlock(blockNumber)
	}
	return false
}
