package evm

import (
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
)

// ContractRefAdapter 将 runtime.Contract 适配成 vm.ContractRef
type ContractRefAdapter struct {
	contract *runtime.Contract
}

// NewContractRefAdapter 创建 ContractRef 适配器
func NewContractRefAdapter(contract *runtime.Contract) vm.ContractRef {
	return &ContractRefAdapter{
		contract: contract,
	}
}

// Address 返回合约地址
func (c *ContractRefAdapter) Address() common.Address {
	return VcAddressToCommon(c.contract.Address)
}
