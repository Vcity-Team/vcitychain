package chain

import "github.com/Vcity-Team/vcitychain/types"

// EffectiveHeaderBaseFee returns the base fee wallets and RPC should expose for a header.
// Stored headers may have BaseFee==0 on legacy blocks; when London is active, derive from parent.
func EffectiveHeaderBaseFee(
	header *types.Header,
	parent *types.Header,
	forks ForksInTime,
	calc func(parent *types.Header) uint64,
) uint64 {
	if header == nil {
		return 0
	}

	if !forks.London {
		return header.BaseFee
	}

	if header.BaseFee != 0 {
		return header.BaseFee
	}

	if calc != nil && parent != nil {
		return calc(parent)
	}

	return GenesisBaseFee
}

// RPCDisplayBaseFee is the base fee exposed via JSON-RPC (eth_getBlock*, eth_feeHistory).
// Many Vcity deployments store BaseFee==0 without enabling the London fork; wallets still
// expect a non-zero baseFeePerGas for fee estimation.
func RPCDisplayBaseFee(
	header *types.Header,
	parent *types.Header,
	forks ForksInTime,
	calc func(parent *types.Header) uint64,
) uint64 {
	if header == nil {
		return 0
	}

	if header.BaseFee != 0 {
		return header.BaseFee
	}

	if forks.London {
		return EffectiveHeaderBaseFee(header, parent, forks, calc)
	}

	return GenesisBaseFee
}
