package evm

import (
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/hashicorp/go-hclog"
	"github.com/holiman/uint256"
)

type codeSetter interface {
	SetCodeDirectly(addr types.Address, code []byte) error
}

type loggerGetter interface {
	GetLogger() hclog.Logger
}

type nonceSetter interface {
	SetNonceDirectly(addr types.Address, nonce uint64)
}

var _ runtime.Runtime = &GethEVMAdapter{}

type GethEVMAdapter struct {
	chainID int64
}

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
	// 添加日志以确认进入了 GethEVMAdapter.Run
	var logger hclog.Logger
	if lg, ok := host.(loggerGetter); ok {
		logger = lg.GetLogger()
		if logger != nil {
			logger.Debug("🔍 [GethEVMAdapter.Run] 开始执行",
				"caller", c.Caller.String()[:16],
				"contractAddress", c.Address.String()[:16],
				"codeSize", len(c.Code),
				"gas", c.Gas,
				"type", c.Type)
		}
	}

	// 1. 创建 StateDB 适配器
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 创建 StateDB 适配器",
			"caller", c.Caller.String()[:16])
	}
	stateDBInterface := NewHostToStateDBAdapter(host, config)
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] StateDB 适配器创建完成",
			"caller", c.Caller.String()[:16])
	}

	// 2. 构建上下文
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 开始构建上下文",
			"caller", c.Caller.String()[:16])
	}
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 调用 buildBlockContext",
			"caller", c.Caller.String()[:16])
	}
	blockCtx := buildBlockContext(host)
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] buildBlockContext 完成",
			"caller", c.Caller.String()[:16])
	}
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 调用 buildTxContext",
			"caller", c.Caller.String()[:16])
	}
	txContext := buildTxContext(host)
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] buildTxContext 完成",
			"caller", c.Caller.String()[:16])
	}
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 调用 buildChainConfig",
			"caller", c.Caller.String()[:16])
	}
	chainConfig := buildChainConfig(config, g.chainID)
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] buildChainConfig 完成",
			"caller", c.Caller.String()[:16])
	}
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 上下文构建完成",
			"caller", c.Caller.String()[:16])
	}

	// 5. 创建 go-ethereum EVM 实例（v1.16+ 不再需要 TxContext 参数）
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] 创建 EVM 实例",
			"caller", c.Caller.String()[:16])
	}
	evm := vm.NewEVM(blockCtx, stateDBInterface, chainConfig, vm.Config{})
	// 设置交易上下文
	evm.SetTxContext(txContext)
	if logger != nil {
		logger.Debug("🔍 [GethEVMAdapter.Run] EVM 实例创建完成",
			"caller", c.Caller.String()[:16])
	}

	// 6. 执行合约
	var ret []byte
	var gasLeft uint64
	var err error

	callerAddr := VcAddressToCommon(c.Caller)
	contractAddr := VcAddressToCommon(c.Address)

	value := new(uint256.Int)
	value.SetFromBig(c.Value)

	if c.Type == runtime.Create {
		// 添加日志以追踪 EVM.Create 调用
		if lg, ok := host.(loggerGetter); ok {
			logger := lg.GetLogger()
			if logger != nil {
				logger.Debug("🔍 [GethEVMAdapter] 调用 evm.Create",
					"caller", callerAddr.String()[:16],
					"codeSize", len(c.Code),
					"gas", c.Gas,
					"value", value.String())
			}
		}
		ret, contractAddr, gasLeft, err = evm.Create(
			callerAddr,
			c.Code,
			c.Gas,
			value,
		)
		// 添加日志以追踪 EVM.Create 完成
		if lg, ok := host.(loggerGetter); ok {
			logger := lg.GetLogger()
			if logger != nil {
				logger.Debug("🔍 [GethEVMAdapter] evm.Create 完成",
					"caller", callerAddr.String()[:16],
					"contractAddr", contractAddr.String()[:16],
					"retLen", len(ret),
					"gasLeft", gasLeft,
					"gasUsed", c.Gas-gasLeft,
					"err", err)
			}
		}
	} else {
		var logger hclog.Logger
		if lg, ok := host.(loggerGetter); ok {
			logger = lg.GetLogger()
		}

		ret, gasLeft, err = evm.Call(
			callerAddr,
			contractAddr,
			c.Input,
			c.Gas,
			value,
		)

		retLen := len(ret)
		gasUsed := c.Gas - gasLeft
		if logger != nil && err != nil {
			logger.Debug("🔍 [GethEVMAdapter] evm.Call 返回",
				"retLen", retLen,
				"gasLeft", gasLeft,
				"gasUsed", gasUsed,
				"err", err,
			)
		}
	}
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
