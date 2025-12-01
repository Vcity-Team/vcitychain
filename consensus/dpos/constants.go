package dpos

import "math/big"

// 默认投票权重常量（1000 VCITY = 1000 * 1e18 wei）
const DefaultVotingPowerStr = "1000000000000000000000"

// DefaultVotingPower 返回默认投票权重的 big.Int 值
func DefaultVotingPower() *big.Int {
	result, _ := new(big.Int).SetString(DefaultVotingPowerStr, 10)
	return result
}

// MustDefaultVotingPower 返回默认投票权重的 big.Int 值（如果失败会 panic）
func MustDefaultVotingPower() *big.Int {
	result, ok := new(big.Int).SetString(DefaultVotingPowerStr, 10)
	if !ok {
		panic("failed to parse default voting power")
	}
	return result
}
