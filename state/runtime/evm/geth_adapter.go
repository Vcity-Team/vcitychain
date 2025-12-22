package evm

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

var _ runtime.Runtime = &GethEVMAdapter{}

// GethEVMAdapter 是 go-ethereum EVM 的适配器
type GethEVMAdapter struct {
	chainID int64 // 用于构建 ChainConfig
}

// NewGethEVMAdapter 创建新的 GethEVMAdapter
func NewGethEVMAdapter(chainID int64) *GethEVMAdapter {
	return &GethEVMAdapter{
		chainID: chainID,
	}
}

// CanRun 实现 runtime.Runtime 接口
func (g *GethEVMAdapter) CanRun(*runtime.Contract, runtime.Host, *chain.ForksInTime) bool {
	return true
}

// Name 实现 runtime.Runtime 接口
func (g *GethEVMAdapter) Name() string {
	return "geth_evm"
}

// Run 实现 runtime.Runtime 接口
func (g *GethEVMAdapter) Run(
	c *runtime.Contract,
	host runtime.Host,
	config *chain.ForksInTime,
) *runtime.ExecutionResult {
	// 1. 创建 StateDB 适配器
	stateDB := NewHostToStateDBAdapter(host, config)

	// 2. 获取交易上下文以构建 BlockContext
	txCtx := host.GetTxContext()

	// 构建一个简单的 Header 用于 BlockContext
	header := &types.Header{
		Number:     uint64(txCtx.Number),
		Timestamp:  uint64(txCtx.Timestamp),
		GasLimit:   uint64(txCtx.GasLimit),
		Difficulty: new(big.Int).SetBytes(txCtx.Difficulty.Bytes()).Uint64(),
		BaseFee:    txCtx.BaseFee.Uint64(),
		Miner:      txCtx.Coinbase.Bytes(),
	}

	// 3. 构建上下文
	blockCtx := buildBlockContext(host, header)
	txContext := buildTxContext(host)
	chainConfig := buildChainConfig(config, g.chainID)

	// 4. 创建 go-ethereum EVM 实例
	evm := vm.NewEVM(blockCtx, txContext, stateDB, chainConfig, vm.Config{})

	// 5. 执行合约
	var ret []byte
	var gasLeft uint64
	var err error

	callerAddr := VcAddressToCommon(c.Caller)
	contractAddr := VcAddressToCommon(c.Address)

	if c.Type == runtime.Create {
		// 创建合约
		ret, contractAddr, gasLeft, err = evm.Create(
			vm.AccountRef(callerAddr),
			c.Input,
			c.Gas,
			c.Value,
		)
	} else {
		// 调用合约
		ret, gasLeft, err = evm.Call(
			vm.AccountRef(callerAddr),
			contractAddr,
			c.Input,
			c.Gas,
			c.Value,
		)
	}

	// 6. 转换结果
	gasUsed := c.Gas - gasLeft
	if err != nil && gasLeft < c.Gas {
		gasUsed = c.Gas // 如果出错且 gas 耗尽，使用全部 gas
	}

	return &runtime.ExecutionResult{
		ReturnValue: ret,
		GasLeft:     gasLeft,
		GasUsed:     gasUsed,
		Err:         convertError(err),
		Address:     CommonAddressToVc(contractAddr),
	}
}
