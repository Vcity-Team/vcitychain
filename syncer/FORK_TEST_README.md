# 分叉处理测试说明

## 测试文件

已创建 `syncer/fork_test.go`，包含以下测试用例：

1. **TestHandleFork_Basic** - 测试基本的分叉处理流程
2. **TestFindCommonAncestor_LocalExists** - 测试在本地找到共同祖先
3. **TestDownloadForkChain_Basic** - 测试下载分叉链
4. **TestHandleFork_WithParentNotFound** - 测试遇到 ErrParentNotFound 时的分叉处理
5. **TestHandleFork_WithParentHashMismatch** - 测试遇到 ErrParentHashMismatch 时的分叉处理

## 运行测试

### 运行所有分叉测试
```powershell
cd c:\work\vcitychain
go test -v ./syncer -run TestHandleFork
```

### 运行特定测试
```powershell
# 测试基本分叉处理
go test -v ./syncer -run TestHandleFork_Basic

# 测试下载分叉链
go test -v ./syncer -run TestDownloadForkChain

# 测试找共同祖先
go test -v ./syncer -run TestFindCommonAncestor
```

## 实际场景测试

### 场景1：两个节点同时出块（你之前提到的场景）

**场景描述：**
- 节点 2 和节点 3 在高度 75530 同时出块
- 节点 1 同步节点 2 的区块
- 节点 4 同步节点 3 的区块
- 后续节点 1 或节点 4 从对方同步时触发分叉处理

**测试步骤：**

1. **准备测试环境**（需要 4 个节点）：
   ```bash
   # 启动 4 个节点
   ./vcitychain server --config ./node1/config.yaml
   ./vcitychain server --config ./node2/config.yaml
   ./vcitychain server --config ./node3/config.yaml
   ./vcitychain server --config ./node4/config.yaml
   ```

2. **模拟分叉**：
   - 修改节点 2 和节点 3 的出块逻辑，让它们在相同高度出块
   - 或者手动构造分叉区块

3. **观察日志**：
   - 查看节点 1 和节点 4 的日志
   - 应该看到 `🔀 检测到分叉，开始分叉处理` 的日志
   - 应该看到 `🔍 [找共同祖先]` 的日志
   - 应该看到 `📥 [下载分叉链]` 的日志
   - 应该看到 `✅ [分叉处理] 分叉处理完成` 的日志

### 场景2：使用单元测试模拟

由于完整的分叉测试需要多个节点，可以先运行单元测试验证逻辑：

```powershell
# 运行所有分叉相关的单元测试
go test -v ./syncer -run "TestHandleFork|TestDownloadForkChain|TestFindCommonAncestor"
```

## 测试验证点

1. **分叉检测**：
   - ✅ 当收到 `ErrParentNotFound` 或 `ErrParentHashMismatch` 时，应该触发分叉处理
   - ✅ 不应该 `os.Exit(1)`

2. **找共同祖先**：
   - ✅ 能够从分叉区块向上回溯找到共同祖先
   - ✅ 支持通过 hash 和高度两种方式请求区块

3. **下载分叉链**：
   - ✅ 能够从共同祖先的下一个区块下载到分叉区块
   - ✅ 下载的区块顺序正确

4. **写入分叉链**：
   - ✅ 分叉链区块能够成功验证
   - ✅ 分叉链区块能够成功写入（触发 reorg）

5. **日志级别**：
   - ✅ 所有分叉相关的日志都是 Info 级别

## 调试技巧

如果测试失败，可以：

1. **查看详细日志**：
   ```powershell
   go test -v ./syncer -run TestHandleFork_Basic 2>&1 | Select-String "分叉|fork|Fork"
   ```

2. **添加更多打印**：
   在 `syncer/syncer.go` 的 `handleFork` 函数中添加更多日志

3. **使用调试器**：
   ```powershell
   # 使用 delve 调试
   dlv test ./syncer -- -test.run TestHandleFork_Basic
   ```

## 注意事项

1. **Mock 对象**：单元测试使用 mock 对象，可能无法完全模拟真实场景
2. **网络延迟**：实际测试中需要考虑网络延迟和超时
3. **并发安全**：实际场景中多个节点同时处理分叉需要考虑并发安全

## 下一步

1. 修复其他测试文件的编译错误（`client_test.go` 中的 `statusTopicName` 未定义）
2. 运行单元测试验证基本逻辑
3. 在实际多节点环境中测试分叉处理

