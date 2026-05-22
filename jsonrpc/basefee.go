package jsonrpc

import (
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/types"
)

type baseFeeCalculator interface {
	GetHeaderByNumber(uint64) (*types.Header, bool)
	GetForksInTime(blockNumber uint64) chain.ForksInTime
	CalculateBaseFee(parent *types.Header) uint64
}

func displayBaseFeeForHeader(store baseFeeCalculator, header *types.Header) uint64 {
	if header == nil {
		return 0
	}

	var parent *types.Header
	if header.Number > 0 {
		parent, _ = store.GetHeaderByNumber(header.Number - 1)
	}

	forks := store.GetForksInTime(header.Number)

	return chain.RPCDisplayBaseFee(header, parent, forks, store.CalculateBaseFee)
}
