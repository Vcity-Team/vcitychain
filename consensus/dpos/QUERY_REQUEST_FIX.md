# 查询请求修复说明

## 问题描述

在 DPoS 共识中，系统会定期发送签名查询请求来查询其他节点的待处理签名请求。但是这些查询请求经常被误判为"无效的签名请求"，导致以下错误日志：

```
[WARN] polygon.server.dpos.runtime.network-integration: 收到无效的签名请求：CheckpointHash 为全零，忽略此请求: blockNumber=0 proposer=0x0000000000000000000000000000000000000000
```

## 问题根源

1. **查询请求识别逻辑不一致**：不同模块使用不同的方式识别查询请求
2. **查询请求构造方式问题**：使用时间戳生成的哈希值可能不包含预期的标识符
3. **类型转换错误**：在验证过程中存在类型不匹配的问题

## 修复内容

### 1. 统一查询请求标识符

**修改前：**
```go
queryHash := types.BytesToHash([]byte(fmt.Sprintf("QUERY_%d", time.Now().Unix())))
blockHash := types.BytesToHash([]byte(fmt.Sprintf("QUERY_BLOCK_%d", time.Now().Unix())))

queryRequest := &proto.SignatureRequest{
    BlockNumber:    0,
    BlockHash:      blockHash.Bytes(),
    CheckpointHash: queryHash.Bytes(),
    // ...
}
```

**修改后：**
```go
queryRequest := &proto.SignatureRequest{
    BlockNumber:    0,
    BlockHash:      []byte("QUERY_REQUEST"),      // 直接使用字符串标识符
    CheckpointHash: []byte("QUERY_REQUEST"),      // 直接使用字符串标识符
    // ...
}
```

### 2. 统一查询请求识别逻辑

**修改前：**
- `dpos.go` 使用 `string(protoRequest.BlockHash) == "QUERY_REQUEST"`
- `network_integration.go` 使用 `strings.Contains(blockHashStr, "QUERY_")`

**修改后：**
- 两个模块都使用 `string(protoRequest.BlockHash) == "QUERY_REQUEST"`

### 3. 添加验证和调试日志

**新增验证：**
```go
// 确保 delegates 不为空
if len(r.delegates) == 0 {
    r.logger.Error("delegates 集合为空，无法计算验证者哈希")
    return nil, fmt.Errorf("empty delegates set: cannot calculate validators hash")
}

// 最终验证：确保checkpointHash不为空
if checkpointHash == (types.Hash{}) {
    r.logger.Error("checkpointHash 仍然为空，无法发送签名请求")
    return nil, fmt.Errorf("invalid checkpointHash: cannot be zero")
}
```

**新增调试日志：**
```go
r.logger.Debug("发送签名查询请求",
    "blockNumber", queryRequest.BlockNumber,
    "blockHash", string(queryRequest.BlockHash),
    "checkpointHash", string(queryRequest.CheckpointHash),
    "proposer", types.Address(r.config.Key.Address()).String())
```

## 修复的文件

1. `consensus/dpos/dpos.go`
   - 修改 `broadcastSignatureQuery()` 方法
   - 修改 `queryPeerForPendingRequests()` 方法
   - 修改 `handleSignatureRequestMessage()` 方法
   - 添加 delegates 和 checkpointHash 验证

2. `consensus/dpos/network_integration.go`
   - 修改 `handleSignatureRequest()` 方法
   - 统一查询请求识别逻辑

## 预期效果

1. **消除误判**：查询请求不再被误判为无效请求
2. **提高可靠性**：在发送签名请求前进行充分验证
3. **便于调试**：添加详细的日志信息，便于问题排查
4. **统一逻辑**：所有模块使用相同的查询请求识别方式

## 测试建议

1. 启动多个 DPoS 节点
2. 观察日志中是否还有"无效的签名请求"警告
3. 验证查询请求是否正常处理
4. 检查签名收集流程是否正常

## 注意事项

1. 修改后的查询请求使用固定的字符串标识符，不再依赖时间戳
2. 所有查询请求都使用相同的标识符，便于统一识别
3. 添加的验证可能会阻止某些边界情况下的签名请求发送，这是预期的安全行为
