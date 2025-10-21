# DPoS CLI 命令使用说明

## 概述

本文档介绍如何使用 `./main dpos` 命令进行DPoS相关的操作，包括投票、治理、受托人管理等。

## 基本语法

```bash
./main dpos <子命令> [选项]
```

## 全局选项

- `--json-rpc string`: JSON-RPC服务器地址 (默认: "http://localhost:8545")
- `--json`: 以JSON格式输出结果

## 1. 投票相关命令

### 1.1 投票给候选人

```bash
./main dpos vote --chain-id <链ID> --voter <投票者地址> --candidate <候选人地址> --amount <投票数量> --private-key <私钥>
```

**参数说明：**
- `--chain-id`: 链ID (必需)
- `--voter`: 投票者地址 (必需)
- `--candidate`: 候选人地址 (必需)
- `--amount`: 投票数量，单位wei (必需)
- `--private-key`: 私钥，64位十六进制字符串 (必需)

**示例：**
```bash
./main dpos vote --chain-id 20250526 --voter 0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe --candidate 0x8dd5455146BA56205F0529B0f500d1b5cE76890B --amount 1000000000000000000000 --private-key 9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756
```

### 1.2 查询投票信息

```bash
./main dpos voting-staking-info --voter <投票者地址>
```

**示例：**
```bash
./main dpos voting-staking-info --voter 0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe
```

### 1.3 查询验证者投票详情

```bash
./main dpos validator-voting-details --validator <验证者地址>
```

**示例：**
```bash
./main dpos validator-voting-details --validator 0x8dd5455146BA56205F0529B0f500d1b5cE76890B
```

## 2. 治理相关命令

### 2.1 创建参数提案

```bash
./main dpos proposal create-proposal --parameter <参数名> --new-value <新值> --description <描述> --proposer <提案者地址>
```

**参数说明：**
- `--parameter`: 要修改的参数名 (必需)
- `--new-value`: 新的参数值 (必需)
- `--description`: 提案描述 (必需)
- `--proposer`: 提案者地址 (必需)

**示例：**
```bash
./main dpos proposal create-proposal --parameter "dpos_validator_reward_ratio" --new-value 80 --description "提高验证者奖励比例到80%" --proposer "0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9"
```

### 2.2 对提案投票

```bash
./main dpos proposal vote --proposal-id <提案ID> --voter <投票者地址> --support <true/false>
```

**参数说明：**
- `--proposal-id`: 提案ID (必需)
- `--voter`: 投票者地址 (必需)
- `--support`: 是否支持提案，true/false (必需)

**示例：**
```bash
./main dpos proposal vote --proposal-id "proposal_202510211031_0" --voter "0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C" --support true
```

### 2.3 查询提案详情

```bash
./main dpos proposal get --proposal-id <提案ID>
```

**示例：**
```bash
./main dpos proposal get --proposal-id "proposal_202510211031_0"
```

### 2.4 执行提案

```bash
./main dpos proposal execute --proposal-id <提案ID>
```

**示例：**
```bash
./main dpos proposal execute --proposal-id "proposal_202510211031_0"
```

### 2.5 查询当前参数值

```bash
./main dpos current-params
```

## 3. 受托人管理命令

### 3.1 注册受托人候选人

```bash
./main dpos delegate register --address <地址> --name <名称> [--website <网站>] [--description <描述>]
```

**参数说明：**
- `--address`: 要注册的地址 (必需)
- `--name`: 受托人名称 (必需)
- `--website`: 官方网站 (可选)
- `--description`: 描述信息 (可选)

**示例：**
```bash
./main dpos delegate register --address "0x8dd5455146BA56205F0529B0f500d1b5cE76890B" --name "MyDelegate" --website "https://mydelegate.com" --description "专业验证节点"
```

### 3.2 列出所有受托人候选人

```bash
./main dpos delegate list [--jsonrpc <JSON-RPC接口>]
```

**参数说明：**
- `--jsonrpc`: JSON-RPC接口地址（默认：http://0.0.0.0:8545）

**功能：**
- 显示所有已注册的受托人候选人
- 显示受托人的详细信息，包括：
  - 地址
  - 名称
  - 网站
  - 描述
  - 保证金金额
  - 总投票数
  - 是否活跃
  - 注册状态（候选人/活跃/非活跃/已撤回）
  - 最后投票时间
  - 创建时间

**示例：**
```bash
# 列出所有受托人
./main dpos delegate list

# 使用自定义JSON-RPC接口
./main dpos delegate list --jsonrpc http://localhost:8545
```

**输出示例：**
```
Status: Success
Message: Delegate registrations retrieved successfully
Count: 1

Delegate Registrations:
=====================

1. Address: 0x8dd5455146BA56205F0529B0f500d1b5cE76890B
   Name: MyDelegate1
   Website: https://mydelegate1.com
   Description: 专业验证节点1
   Status: Candidate
   Deposit: 0
   Total Votes: 0
   Is Active: false
   Last Vote Time: Never
   Created At: 2025-01-21 15:14:01
```

## 4. 经济系统命令

### 4.1 查询Epoch信息

```bash
./main dpos epoch [--epoch-number <Epoch编号>]
```

**示例：**
```bash
# 查询最新Epoch
./main dpos epoch

# 查询指定Epoch
./main dpos epoch --epoch-number 1
```

### 4.2 查询统计信息

```bash
./main dpos stats
```

### 4.3 查询奖励信息

```bash
./main dpos rewards --address <地址>
```

**示例：**
```bash
./main dpos rewards --address 0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe
```

## 5. 验证者信息命令

### 5.1 查询验证者信息

```bash
./main dpos validator-info --address <验证者地址>
```

**示例：**
```bash
./main dpos validator-info --address 0x8dd5455146BA56205F0529B0f500d1b5cE76890B
```

### 5.2 查询最小质押门槛

```bash
./main dpos min-threshold
```

## 6. 参数管理命令

### 6.1 查询所有参数

```bash
./main dpos parameters
```

## 7. 常用命令组合

### 7.1 完整的投票流程

```bash
# 1. 注册为受托人候选人
./main dpos delegate register --address "0x8dd5455146BA56205F0529B0f500d1b5cE76890B" --name "MyDelegate"

# 2. 投票给候选人
./main dpos vote --chain-id 20250526 --voter "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe" --candidate "0x8dd5455146BA56205F0529B0f500d1b5cE76890B" --amount 1000000000000000000000 --private-key "9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756"

# 3. 查询投票结果
./main dpos voting-staking-info --voter "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe"
```

### 7.2 完整的治理流程

```bash
# 1. 创建提案
./main dpos proposal create-proposal --parameter "dpos_validator_reward_ratio" --new-value 80 --description "提高验证者奖励比例" --proposer "0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9"

# 2. 对提案投票
./main dpos proposal vote --proposal-id "proposal_202510211031_0" --voter "0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C" --support true

# 3. 查询提案状态
./main dpos proposal get --proposal-id "proposal_202510211031_0"

# 4. 执行提案（如果通过）
./main dpos proposal execute --proposal-id "proposal_202510211031_0"
```

## 8. 错误处理

### 8.1 常见错误

1. **地址格式错误**
   ```
   Error: invalid address format: 0x123
   ```
   解决：确保地址是42位十六进制字符串，以0x开头

2. **私钥格式错误**
   ```
   Error: private key must be 64 hex characters, got 32
   ```
   解决：确保私钥是64位十六进制字符串

3. **余额不足**
   ```
   Error: insufficient balance for delegate registration
   ```
   解决：确保账户有足够的VCITY余额

4. **受托人未注册**
   ```
   Error: delegate 0x... is not registered
   ```
   解决：先注册为受托人候选人，然后再投票

### 8.2 调试技巧

1. **使用JSON输出查看详细信息**
   ```bash
   ./main dpos vote --json --chain-id 20250526 --voter 0x... --candidate 0x... --amount 1000 --private-key 0x...
   ```

2. **检查节点连接**
   ```bash
   ./main dpos stats
   ```

3. **查看当前参数配置**
   ```bash
   ./main dpos current-params
   ```

## 9. 配置说明

### 9.1 节点配置

确保节点配置文件 `node-config-validator.yaml` 中包含：

```yaml
dpos_SR_threshold: "0"  # SR候选人保证金阈值
dpos_proposal_period: "24h"  # 提案周期
dpos_validator_reward_ratio: 70  # 验证者奖励比例
dpos_voter_reward_ratio: 30  # 投票者奖励比例
```

### 9.2 环境变量

- `VCITY_RPC_URL`: 默认RPC地址
- `VCITY_CHAIN_ID`: 默认链ID

## 10. 最佳实践

1. **安全操作**
   - 不要在命令行中直接输入私钥，使用环境变量
   - 定期备份钱包文件
   - 使用测试网络进行测试

2. **性能优化**
   - 合理设置投票数量，避免过多小额投票
   - 定期清理过期的提案
   - 监控网络状态

3. **治理参与**
   - 及时关注提案状态
   - 参与重要参数的治理投票
   - 保持受托人信息的更新

## 11. 示例脚本

### 11.1 批量投票脚本 (PowerShell)

```powershell
# 批量投票脚本
$voters = @("0x...", "0x...", "0x...")
$candidate = "0x8dd5455146BA56205F0529B0f500d1b5cE76890B"
$amount = "1000000000000000000000"
$privateKey = "9b3e66682f2f4daa58245b1a30a147915cb32cdab13e7f8ebe60fcaa9cce8756"

foreach ($voter in $voters) {
    Write-Host "Voting for $candidate with $voter"
    ./main dpos vote --chain-id 20250526 --voter $voter --candidate $candidate --amount $amount --private-key $privateKey
}
```

### 11.2 监控脚本 (PowerShell)

```powershell
# 监控受托人状态
while ($true) {
    Write-Host "Checking delegate status..."
    ./main dpos validator-info --address "0x8dd5455146BA56205F0529B0f500d1b5cE76890B"
    Start-Sleep 60
}
```

这个CLI使用说明涵盖了所有主要的DPoS操作命令，您可以根据需要选择相应的命令进行使用。
