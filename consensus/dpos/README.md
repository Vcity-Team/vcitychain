
# DPoS 共识模块说明

本目录实现了 Delegated Proof of Stake (DPoS) 共识机制，结构和 PolyBFT 共识保持一致，便于扩展和维护。

## 目录结构

```
consensus/dpos/
├── dpos.go                # DPoS 共识主入口，实现共识接口
├── consensus_runtime.go   # 共识运行时，负责状态管理和共识主循环
├── fsm.go                 # 有限状态机，负责区块构建、验证等
├── extra.go               # 区块额外数据结构
├── state.go               # 状态存储与管理
├── block_builder.go       # 区块构建器
├── proposer_calculator.go # 出块者轮询与计算
├── stake_manager.go       # 质押与投票管理
├── validator/             # 验证者集合与元数据
├── wallet/                # 钱包与密钥管理
├── signer/                # 签名工具
├── proto/                 # 协议定义
├── contractsapi/          # 合约API（如有）
├── bitmap/                # 位图工具
└── ...
```

## 主要模块职责
- `dpos.go`：DPoS共识主入口，实现共识接口（如Factory、Initialize、Start等）
- `consensus_runtime.go`：共识运行时，负责轮次、出块、状态切换等
- `fsm.go`：有限状态机，负责区块构建、验证、投票等
- `stake_manager.go`：质押、解质押、投票逻辑
- `validator/`：验证者集合、元数据、快照等
- `wallet/`：密钥管理、签名
- `proto/`：gRPC/Protobuf协议定义

## 集成到主程序
1. 在 `consensus/consensus.go` 的 Factory 逻辑中增加 DPoS 分支：
   ```go
   import "github.com/Vcity-Team/vcitychain/consensus/dpos"
   // ...
   switch config.Name {
   case "dpos":
       return dpos.Factory(params)
   // ...
   }
   ```
2. 配置文件中指定 `consensus: dpos`，并补充 DPoS 配置项。

## 运行和测试
- 通过主程序启动节点，配置 `consensus: dpos` 即可运行 DPoS 共识。
- 可用 `go test ./consensus/dpos/...` 运行单元测试。
- 通过 RPC/CLI 进行质押、解质押、投票等操作。

---
如需扩展功能，请参考 PolyBFT 目录的实现方式。
