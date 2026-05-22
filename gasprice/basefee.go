package gasprice

import (
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/types"
)

func (g *GasHelper) effectiveHeaderBaseFee(header *types.Header) uint64 {
	if header == nil {
		return 0
	}

	forks := g.backend.Config().Forks.At(header.Number)

	var parent *types.Header
	if header.Number > 0 {
		if parentBlock, ok := g.backend.GetBlockByNumber(header.Number-1, false); ok && parentBlock != nil {
			parent = parentBlock.Header
		}
	}

	return chain.EffectiveHeaderBaseFee(header, parent, forks, g.backend.CalculateBaseFee)
}
