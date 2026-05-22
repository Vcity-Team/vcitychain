package jsonrpc

import "github.com/Vcity-Team/vcitychain/chain"

// capLondonRPCGasPrice keeps eth_gasPrice wallet-friendly on chains with legacy zero base-fee headers.
func capLondonRPCGasPrice(price uint64) uint64 {
	const capMul = 2
	cap := chain.GenesisBaseFee * capMul
	if price > cap {
		return cap
	}
	return price
}
