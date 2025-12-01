package dpos

import (
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// findValidatorIndex 在验证者集合中查找指定地址的索引（线性查找）
// 返回索引和是否找到
func findValidatorIndex(validators validator.AccountSet, address types.Address) (int, bool) {
	for i, v := range validators {
		if v.Address == address {
			return i, true
		}
	}
	return -1, false
}

// findValidator 在验证者集合中查找指定地址的验证者
// 返回验证者指针和是否找到
func findValidator(validators validator.AccountSet, address types.Address) (*validator.ValidatorMetadata, bool) {
	index, found := findValidatorIndex(validators, address)
	if !found {
		return nil, false
	}
	return validators[index], true
}

// containsValidator 检查验证者集合中是否包含指定地址
func containsValidator(validators validator.AccountSet, address types.Address) bool {
	_, found := findValidatorIndex(validators, address)
	return found
}
