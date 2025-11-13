# CLI JSON-RPC 参数问题修复

## 问题描述

执行命令时出现错误：
```
Error: failed to get validator voting details: HTTP request failed: Post "": unsupported protocol scheme ""
```

## 问题原因

代码中获取 JSON-RPC 地址的方式不正确，导致地址为空。

## 解决方案

### 方法1：使用正确的参数格式（推荐）

标志名是 `--jsonrpc`（不是 `--json-rpc`）：

```bash
# 正确的命令格式
./main dpos validator-voting-details --chain-id 100 --validator 0x4B4d3526CeeBb3f028516311D46FD043B5c93192 --jsonrpc http://209.53.43.252:9545

# 或者使用短格式（如果支持）
./main dpos validator-voting-details --chain-id 100 --validator 0x4B4d3526CeeBb3f028516311D46FD043B5c93192 --jsonrpc=http://209.53.43.252:9545
```

### 方法2：使用环境变量（如果支持）

某些命令可能支持环境变量，但需要查看具体实现。

### 方法3：使用 curl 直接调用 JSON-RPC（临时方案）

如果 CLI 命令仍有问题，可以直接使用 curl：

```bash
curl -X POST http://209.53.43.252:9545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getValidatorVotingDetails",
    "params": ["0x4B4d3526CeeBb3f028516311D46FD043B5c93192"],
    "id": 1
  }' | jq '.result.validator'
```

## 代码修复

已修复 `command/dpos/validator_voting_details/validator_voting_details.go` 中的问题：

**修复前**：
```go
if cmd.Flags().Changed("jsonrpc") {
    jsonRPC = params.jsonRPC  // params.jsonRPC 可能为空
}
```

**修复后**：
```go
if cmd.Flags().Changed("jsonrpc") {
    jsonRPC, _ = cmd.Flags().GetString("jsonrpc")
} else {
    // Try to get from helper function
    jsonRPC = helper.GetJSONRPCAddress(cmd)
}
```

## 验证命令

修复后，使用以下命令验证：

```bash
# 基本查询
./main dpos validator-voting-details --chain-id 100 --validator 0x4B4d3526CeeBb3f028516311D46FD043B5c93192 --jsonrpc http://209.53.43.252:9545

# JSON 输出
./main dpos validator-voting-details --chain-id 100 --validator 0x4B4d3526CeeBb3f028516311D46FD043B5c93192 --jsonrpc http://209.53.43.252:9545 --json
```

## 其他相关命令

所有使用 `RegisterJSONRPCFlag` 的命令都应该使用 `--jsonrpc` 参数：

```bash
# voting-staking-info
./main dpos voting-staking-info --chain-id 100 --jsonrpc http://209.53.43.252:9545

# epoch
./main dpos epoch --chain-id 100 --current --jsonrpc http://209.53.43.252:9545

# stats
./main dpos stats --chain-id 100 --all --epoch 0 --jsonrpc http://209.53.43.252:9545
```

## 注意事项

1. **参数格式**：使用 `--jsonrpc`（不是 `--json-rpc`）
2. **URL 格式**：确保 URL 包含协议（`http://` 或 `https://`）
3. **引号**：在 PowerShell 中，URL 可能需要引号
4. **默认值**：如果不指定 `--jsonrpc`，默认使用 `http://0.0.0.0:8545`

## 如果问题仍然存在

1. **检查命令帮助**：
   ```bash
   ./main dpos validator-voting-details --help
   ```

2. **查看所有可用标志**：
   ```bash
   ./main dpos validator-voting-details --help | grep jsonrpc
   ```

3. **使用 curl 验证节点是否在线**：
   ```bash
   curl -X POST http://209.53.43.252:9545 \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
   ```

