# Epoch计算修复说明

## 问题描述

在DPoS系统中，`isEpochEndBlock()` 方法和 `GetCurrentEpochInfo()` / `GetEpochInfoByNumber()` 方法使用了不同的epoch计算逻辑，导致计算结果不一致。

### 问题表现

1. **`isEpochEndBlock()` 方法（正确）**：
   - 考虑了共识切换高度
   - 计算：`currentEpoch = ((blockNumber - consensusSwitchHeight) / epochSize) + 1`
   - 结果：758（正确）

2. **`GetCurrentEpochInfo()` 方法（错误）**：
   - 没有考虑共识切换高度
   - 计算：`currentEpoch = (blockNumber / epochSize) + 1`
   - 结果：2226（错误）

## 修复方案

### 修复内容

1. **修复 `GetCurrentEpochInfo()` 方法**：
   - 使用与 `isEpochEndBlock()` 相同的epoch计算逻辑
   - 考虑共识切换高度
   - 统一计算方式

2. **修复 `GetEpochInfoByNumber()` 方法**：
   - 使用相同的epoch计算逻辑获取当前epoch
   - 正确计算指定epoch的区块范围
   - 考虑共识切换高度

### 修复后的计算逻辑

```go
// 统一的epoch计算逻辑
consensusSwitchHeight := d.config.ConsensusSwitchHeight
epochSize := d.getEpochSize()

var epochNumber uint64
var firstBlockInEpoch uint64

if currentBlockNumber < consensusSwitchHeight {
    // 在共识切换之前，epoch为0
    epochNumber = 0
    firstBlockInEpoch = 0
} else {
    // 计算DPoS epoch：从共识切换高度开始
    dposBlockNumber := currentBlockNumber - consensusSwitchHeight
    epochNumber = (dposBlockNumber / epochSize) + 1
    firstBlockInEpoch = consensusSwitchHeight + (epochNumber-1)*epochSize
}
```

### 验证计算

根据测试数据：
- `blockNumber = 11159`
- `consensusSwitchHeight = 7370`
- `epochSize = 5`

**修复后的正确计算**：
```
dposBlockNumber = 11159 - 7370 = 3789
currentEpoch = (3789 / 5) + 1 = 757 + 1 = 758 ✅
firstBlockInEpoch = 7370 + (758-1) * 5 = 7370 + 3785 = 11155 ✅
lastBlockInEpoch = 11155 + 5 - 1 = 11159 ✅
```

## 修复效果

### 修复前
```bash
./main dpos epoch
{
  "epochNumber": 2226,  # 错误
  "currentBlockNumber": 11126,
  "consensusSwitchHeight": 7370
}

./main dpos epoch 1
{
  "epochNumber": 1,
  "currentEpoch": 2246,  # 错误
  "firstBlockInEpoch": 0,  # 错误
  "lastBlockInEpoch": 4   # 错误
}
```

### 修复后
```bash
./main dpos epoch
{
  "epochNumber": 758,  # 正确
  "currentBlockNumber": 11159,
  "consensusSwitchHeight": 7370,
  "firstBlockInEpoch": 11155,  # 正确
  "lastBlockInEpoch": 11159    # 正确
}

./main dpos epoch 1
{
  "epochNumber": 1,
  "currentEpoch": 758,  # 正确
  "firstBlockInEpoch": 7370,  # 正确
  "lastBlockInEpoch": 7374    # 正确
}
```

## 技术细节

### 关键修改点

1. **GetCurrentEpochInfo() 方法**：
   - 移除了对 `epochManager.GetEpochInfo()` 的依赖（用于epoch计算）
   - 使用统一的epoch计算逻辑
   - 保持时间相关信息的获取

2. **GetEpochInfoByNumber() 方法**：
   - 使用相同的epoch计算逻辑获取当前epoch
   - 正确计算指定epoch的区块范围
   - 考虑共识切换高度

### 兼容性

- 保持API接口不变
- 保持返回数据结构不变
- 只修复计算逻辑，不影响其他功能

## 测试验证

### 测试命令
```bash
# 查看当前epoch信息
./main dpos epoch

# 查看指定epoch信息
./main dpos epoch 1
./main dpos epoch 758
```

### 预期结果
- 所有epoch计算都应该与 `isEpochEndBlock()` 方法保持一致
- 区块范围计算应该正确
- epoch状态判断应该准确

## 总结

通过统一epoch计算逻辑，解决了不同方法间计算结果不一致的问题，确保了DPoS系统的一致性和正确性。
