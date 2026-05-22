package jsonrpc

import "github.com/Vcity-Team/vcitychain/chain"

// capRPCGasPrice keeps eth_gasPrice wallet-friendly on chains with legacy zero base-fee headers.
func capRPCGasPrice(price uint64) uint64 {
	const capMul = 2
	cap := chain.GenesisBaseFee * capMul
	if price > cap {
		return cap
	}
	return price
}

// capLondonRPCGasPrice is an alias kept for the London code path.
func capLondonRPCGasPrice(price uint64) uint64 {
	return capRPCGasPrice(price)
}
