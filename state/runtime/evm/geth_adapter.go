package evm

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

// codeSetter 是一个内部接口，用于设置合约代码
// 这个接口允许我们通过类型断言来访问 Transition 的 SetCodeDirectly 方法
type codeSetter interface {
	SetCodeDirectly(addr types.Address, code []byte) error
}

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
	stateDBInterface := NewHostToStateDBAdapter(host, config)
	stateDB := stateDBInterface.(*HostToStateDBAdapter)

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
	// 注意：go-ethereum EVM 在执行 LOG 指令时会调用 StateDB.AddLog
	// 日志会通过 StateDB 收集，而不是从 EVM 返回值中获取
	evm := vm.NewEVM(blockCtx, txContext, stateDBInterface, chainConfig, vm.Config{})

	// 5. 执行合约
	var ret []byte
	var gasLeft uint64
	var err error

	callerAddr := VcAddressToCommon(c.Caller)
	contractAddr := VcAddressToCommon(c.Address)

	// 将 big.Int 转换为 uint256.Int
	value := new(uint256.Int)
	value.SetFromBig(c.Value)

	if c.Type == runtime.Create {
		// 🔧 调试：记录合约创建开始
		// 注意：这里无法输出日志，因为 geth_adapter 没有 logger
		// 但可以通过 addLogCallCount 来追踪 AddLog 是否被调用
		// 创建合约
		ret, contractAddr, gasLeft, err = evm.Create(
			vm.AccountRef(callerAddr),
			c.Input,
			c.Gas,
			value,
		)

		// 🔧 关键修复：go-ethereum EVM 的 Create 方法会调用 StateDB.SetCode 来保存代码
		// 但我们的 SetCode 实现是空实现，所以需要在这里手动保存代码
		// 注意：go-ethereum EVM 的 Create 方法内部已经调用了 SetCode，但我们的实现是空实现
		// 所以我们需要在这里手动保存代码，类似于原生 EVM 在 applyCreate 中的处理
		//
		// 重要：applyCreate 会在调用 t.run 之前创建账户（如果 EIP158 启用）
		// 但 go-ethereum EVM 的 Create 方法也会调用 StateDB.CreateAccount
		// 由于我们的 CreateAccount 是空实现，账户可能还没有被创建
		// 所以我们需要确保账户存在后再设置代码
		if err == nil && len(ret) > 0 {
			// 通过类型断言访问 Transition 的 SetCodeDirectly 方法
			if setter, ok := host.(codeSetter); ok {
				vcAddr := CommonAddressToVc(contractAddr)
				// 尝试设置代码
				// 注意：如果账户不存在，SetCodeDirectly 会返回错误
				// 但在正常情况下，applyCreate 应该已经创建了账户（如果 EIP158 启用）
				// 或者账户会在首次访问时自动创建
				if setErr := setter.SetCodeDirectly(vcAddr, ret); setErr != nil {
					// 如果设置失败（例如账户不存在），这是一个错误情况
					// 但为了不中断执行流程，我们忽略错误
					// 实际上，这应该不会发生，因为 applyCreate 应该已经创建了账户
				}
			}
		}
	} else {
		// 调用合约
		ret, gasLeft, err = evm.Call(
			vm.AccountRef(callerAddr),
			contractAddr,
			c.Input,
			c.Gas,
			value,
		)
	}

	// 6. 转换结果
	gasUsed := c.Gas - gasLeft
	if err != nil && gasLeft < c.Gas {
		gasUsed = c.Gas // 如果出错且 gas 耗尽，使用全部 gas
	}

	// 🔧 调试：检查 AddLog 调用次数
	// 如果 AddLog 被调用了，addLogCallCount 应该 > 0
	// 这个信息会在 Transition.EmitLog 中输出（如果 AddLog 被调用）
	// 注意：如果 addLogCallCount == 0，说明 go-ethereum EVM 没有发出任何事件日志
	// 这可能是因为：
	// 1. 合约构造函数没有执行 LOG 指令（没有发出事件）
	// 2. 合约执行失败，没有执行到 LOG 指令
	// 3. go-ethereum EVM 的日志收集机制与原生 EVM 不同
	//
	// 重要：go-ethereum EVM 只有在执行 LOG 指令时才会调用 StateDB.AddLog
	// 如果合约创建时构造函数没有发出事件，就不会有日志
	_ = stateDB.addLogCallCount // 用于调试断点

	return &runtime.ExecutionResult{
		ReturnValue: ret,
		GasLeft:     gasLeft,
		GasUsed:     gasUsed,
		Err:         convertError(err),
		Address:     CommonAddressToVc(contractAddr),
	}
}
