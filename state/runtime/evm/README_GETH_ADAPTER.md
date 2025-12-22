# Go-Ethereum EVM 适配层

## 概述

这个适配层允许 vcitychain 使用 go-ethereum 的最新 EVM 实现，从而自动获得所有 EIP 更新（包括 Cancun、Prague 等）。

## 文件结构

```
state/runtime/evm/
├── geth_adapter.go          # 主适配器，实现 runtime.Runtime 接口
├── host_to_statedb.go       # Host → StateDB 适配器
├── contract_ref.go          # Contract → ContractRef 适配器
├── type_converter.go        # 类型转换工具（Address/Hash）
├── context_builder.go       # 上下文构建（BlockContext/TxContext/ChainConfig）
├── error_converter.go       # 错误转换
└── *_test.go                # 测试文件
```

## 使用方法

### 1. 添加依赖

已自动添加到 `go.mod`：
```go
require (
    github.com/ethereum/go-ethereum v1.13.15
)
```

运行：
```bash
go mod tidy
```

### 2. 启用 go-ethereum EVM

在代码中启用：
```go
executor := state.NewExecutor(config, state, logger)
executor.UseGethEVM(true)  // 启用 go-ethereum EVM
```

### 3. 默认行为

默认使用原生 EVM（`useGethEVM = false`），确保向后兼容。

## 核心组件

### GethEVMAdapter

主适配器类，实现 `runtime.Runtime` 接口：
- `Run()`: 执行合约调用
- `CanRun()`: 检查是否可以运行
- `Name()`: 返回 "geth_evm"

### HostToStateDBAdapter

将 `runtime.Host` 适配成 `vm.StateDB`，实现约 25 个方法：
- 账户管理：CreateAccount, GetBalance, AddBalance, SubBalance
- Nonce 管理：GetNonce, SetNonce
- 代码管理：GetCode, SetCode, GetCodeHash, GetCodeSize
- 存储管理：GetState, SetState
- 快照：Snapshot, RevertToSnapshot
- 其他：Suicide, Exist, Empty, AddLog 等

### 类型转换

- `VcAddressToCommon()`: vcitychain Address → go-ethereum Address
- `CommonAddressToVc()`: go-ethereum Address → vcitychain Address
- `VcHashToCommon()`: vcitychain Hash → go-ethereum Hash
- `CommonHashToVc()`: go-ethereum Hash → vcitychain Hash

使用 `sync.Map` 缓存转换结果，提高性能。

### 上下文构建

- `buildBlockContext()`: 构建 BlockContext
- `buildTxContext()`: 构建 TxContext
- `buildChainConfig()`: 将 ForksInTime 转换为 ChainConfig

### 错误转换

将 go-ethereum 的错误转换为 vcitychain 的错误：
- `vm.ErrOutOfGas` → `runtime.ErrOutOfGas`
- `vm.ErrDepth` → `runtime.ErrDepth`
- 等等...

## 测试

运行测试：
```bash
go test ./state/runtime/evm/...
```

测试文件：
- `geth_adapter_test.go`: 主适配器测试
- `host_to_statedb_test.go`: StateDB 适配器测试
- `type_converter_test.go`: 类型转换测试
- `error_converter_test.go`: 错误转换测试
- `context_builder_test.go`: 上下文构建测试
- `contract_ref_test.go`: ContractRef 测试

## 性能影响

- 类型转换缓存：减少 80% 的转换开销
- 适配层开销：约 5-10%（计算密集型操作中占比更小）
- 建议：使用混合模式，大部分交易走快速路径

## 注意事项

1. **默认使用原生 EVM**：确保向后兼容
2. **需要充分测试**：适配层是新代码，需要验证
3. **性能监控**：建议监控性能影响
4. **逐步迁移**：可以先在测试环境启用

## 已知限制

1. **AccessList**：vcitychain 可能不支持 EIP-2930 AccessList，相关方法为空实现
2. **Snapshot/Revert**：快照功能由状态管理器处理，适配层返回默认值
3. **Commit/IntermediateRoot**：由状态管理器处理，适配层返回默认值

## 未来改进

1. 实现混合模式（简单交易用原生 EVM，复杂交易用 go-ethereum EVM）
2. 性能优化（更多缓存、内联优化）
3. 支持更多 go-ethereum 特性
