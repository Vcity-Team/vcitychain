package evm

import (
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/ethereum/go-ethereum/common"
)

// ContractRefAdapter 将 runtime.Contract 适配成 common.Address
// 注意：v1.16+ 不再需要 vm.ContractRef，直接使用 common.Address
type ContractRefAdapter struct {
	contract *runtime.Contract
}

// NewContractRefAdapter 创建 ContractRef 适配器（已废弃，v1.16+ 直接使用 common.Address）
// 保留此函数以保持向后兼容，但不再使用
func NewContractRefAdapter(contract *runtime.Contract) common.Address {
	return VcAddressToCommon(contract.Address)
}

// Address 返回合约地址
func (c *ContractRefAdapter) Address() common.Address {
	return VcAddressToCommon(c.contract.Address)
}


