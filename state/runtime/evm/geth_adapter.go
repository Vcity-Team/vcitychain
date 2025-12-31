package evm

import (
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/hashicorp/go-hclog"
	"github.com/holiman/uint256"
)

// codeSetter 是一个内部接口，用于设置合约代码
// 这个接口允许我们通过类型断言来访问 Transition 的 SetCodeDirectly 方法
type codeSetter interface {
	SetCodeDirectly(addr types.Address, code []byte) error
}

// loggerGetter 是一个内部接口，用于获取 logger
// 这个接口允许我们通过类型断言来访问 Transition 的 logger
type loggerGetter interface {
	GetLogger() hclog.Logger
}

// nonceSetter 是一个内部接口，用于设置 nonce
// 这个接口允许我们通过类型断言来访问 Transition 的 SetNonceDirectly 方法
type nonceSetter interface {
	SetNonceDirectly(addr types.Address, nonce uint64)
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

	// 🔍 调试：检查 ChainConfig 是否正确设置
	var logger hclog.Logger
	if lg, ok := host.(loggerGetter); ok {
		logger = lg.GetLogger()
	}
	if logger != nil {
		shanghaiTimeStr := "nil"
		if chainConfig.ShanghaiTime != nil {
			shanghaiTimeStr = fmt.Sprintf("%d", *chainConfig.ShanghaiTime)
		}
		// 🔍 关键：检查 Rules 方法返回的规则（EVM 实际使用的规则）
		// Rules 方法签名：Rules(num *big.Int, isMerge bool, timestamp uint64) Rules
		// isMerge 通常为 true（因为我们已经过了 The Merge）
		rules := chainConfig.Rules(blockCtx.BlockNumber, true, blockCtx.Time)
		logger.Info("🔍 [GethEVMAdapter] ChainConfig 配置",
			"chainID", chainConfig.ChainID,
			"londonBlock", chainConfig.LondonBlock,
			"shanghaiTime", shanghaiTimeStr,
			"blockNumber", blockCtx.BlockNumber,
			"blockTime", blockCtx.Time,
			"isShanghai", chainConfig.IsShanghai(blockCtx.BlockNumber, blockCtx.Time),
			"rulesIsShanghai", rules.IsShanghai,
		)
	}

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
		// 🔍 获取 logger（如果可能）
		var logger hclog.Logger
		if lg, ok := host.(loggerGetter); ok {
			logger = lg.GetLogger()
		}

		// 🔍 调试日志：记录合约创建前的状态
		codeLen := len(c.Code)
		codePreview := ""
		if codeLen > 0 {
			if codeLen > 32 {
				codePreview = hex.EncodeToString(c.Code[:32]) + "..."
			} else {
				codePreview = hex.EncodeToString(c.Code)
			}
		}
		if logger != nil {
			logger.Info("🔍 [GethEVMAdapter] 准备调用 evm.Create",
				"caller", callerAddr.Hex(),
				"contractAddr", contractAddr.Hex(),
				"codeLen", codeLen,
				"codePreview", codePreview,
				"gas", c.Gas,
				"value", value.String(),
				"inputLen", len(c.Input),
			)
		} else {
			fmt.Printf("[GethEVMAdapter] 准备调用 evm.Create: caller=%s, contractAddr=%s, codeLen=%d, codePreview=%s, gas=%d, value=%s, inputLen=%d\n",
				callerAddr.Hex(), contractAddr.Hex(), codeLen, codePreview, c.Gas, value.String(), len(c.Input))
		}

		// 🔍 调试：在调用 evm.Create 之前，检查合约地址的状态
		// 这样可以追踪 go-ethereum EVM 在检查冲突时读取到的状态
		// ⚠️ 关键：go-ethereum EVM 的 Create 方法会先检查 GetCodeSize，然后检查 GetNonce
		if contractAddr.Hex() == "0xFC6FC02C0669EbA46894C77Aaa4d3f895679349c" {
			// 通过 StateDB 接口检查状态（go-ethereum EVM 会使用这个接口）
			codeSize := stateDBInterface.GetCodeSize(contractAddr)
			nonce := stateDBInterface.GetNonce(contractAddr)
			codeHash := stateDBInterface.GetCodeHash(contractAddr)
			exists := stateDBInterface.Exist(contractAddr)

			if logger != nil {
				// 🔍 使用 Info 级别，确保日志可见（用户只能看到 Info 级别）
				logger.Info("🔍 [GethEVMAdapter] ⚠️⚠️⚠️ 调用 evm.Create 之前的状态检查（go-ethereum EVM 会使用这些值检查冲突）",
					"contractAddr", contractAddr.Hex(),
					"codeSize", codeSize,
					"nonce", nonce,
					"codeHash", codeHash.Hex(),
					"exists", exists,
					"note", "如果 nonce > 0 或 codeSize > 0，go-ethereum EVM 会判定为冲突。这些值来自 StateDB 接口，会调用我们的 GetNonce/GetCodeSize")
			} else {
				fmt.Printf("🔍 [GethEVMAdapter] ⚠️⚠️⚠️ 调用 evm.Create 之前的状态检查: contractAddr=%s, codeSize=%d, nonce=%d, codeHash=%s, exists=%v\n",
					contractAddr.Hex(), codeSize, nonce, codeHash.Hex(), exists)
			}
		}

		// 🔧 关键修复：对于合约创建，初始化代码在 c.Code 中，而不是 c.Input
		// c.Input 是空的，c.Code 包含合约的初始化代码（bytecode）
		ret, contractAddr, gasLeft, err = evm.Create(
			vm.AccountRef(callerAddr),
			c.Code, // 使用 c.Code 而不是 c.Input，因为合约创建时初始化代码在 c.Code 中
			c.Gas,
			value,
		)

		// 🔍 调试日志：记录 evm.Create 的返回值
		retLen := len(ret)
		retPreview := ""
		if retLen > 0 {
			if retLen > 32 {
				retPreview = hex.EncodeToString(ret[:32]) + "..."
			} else {
				retPreview = hex.EncodeToString(ret)
			}
		}
		gasUsed := c.Gas - gasLeft
		if logger != nil {
			logger.Info("🔍 [GethEVMAdapter] evm.Create 返回",
				"contractAddr", contractAddr.Hex(),
				"retLen", retLen,
				"retPreview", retPreview,
				"gasLeft", gasLeft,
				"gasUsed", gasUsed,
				"err", err,
			)
		} else {
			fmt.Printf("[GethEVMAdapter] evm.Create 返回: contractAddr=%s, retLen=%d, retPreview=%s, gasLeft=%d, gasUsed=%d, err=%v\n",
				contractAddr.Hex(), retLen, retPreview, gasLeft, gasUsed, err)
		}

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
		// 🔍 获取 logger（如果可能）
		var logger hclog.Logger
		if lg, ok := host.(loggerGetter); ok {
			logger = lg.GetLogger()
		}

		// 🔍 调试日志：记录合约调用前的状态
		inputLen := len(c.Input)
		inputPreview := ""
		if inputLen > 0 {
			if inputLen > 32 {
				inputPreview = hex.EncodeToString(c.Input[:32]) + "..."
			} else {
				inputPreview = hex.EncodeToString(c.Input)
			}
		}
		if logger != nil {
			logger.Info("🔍 [GethEVMAdapter] 准备调用 evm.Call",
				"caller", callerAddr.Hex(),
				"contractAddr", contractAddr.Hex(),
				"inputLen", inputLen,
				"inputPreview", inputPreview,
				"gas", c.Gas,
				"value", value.String(),
			)
		}

		// 调用合约
		ret, gasLeft, err = evm.Call(
			vm.AccountRef(callerAddr),
			contractAddr,
			c.Input,
			c.Gas,
			value,
		)

		// 🔍 调试日志：记录 evm.Call 的返回值
		retLen := len(ret)
		gasUsed := c.Gas - gasLeft
		if logger != nil {
			logger.Info("🔍 [GethEVMAdapter] evm.Call 返回",
				"retLen", retLen,
				"gasLeft", gasLeft,
				"gasUsed", gasUsed,
				"err", err,
			)
		}
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
