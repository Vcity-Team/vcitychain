package dpos

import "math/big"

// isZeroOrNil 检查 big.Int 是否为 nil 或零值
func isZeroOrNil(v *big.Int) bool {
	return v == nil || v.Sign() == 0
}

// isPositive 检查 big.Int 是否为正数
func isPositive(v *big.Int) bool {
	return v != nil && v.Sign() > 0
}

// isNonPositive 检查 big.Int 是否为非正数（零或负数）
func isNonPositive(v *big.Int) bool {
	return v == nil || v.Sign() <= 0
}

