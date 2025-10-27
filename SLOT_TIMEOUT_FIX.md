# Slot超时自动跳到下一个出块者 - 功能增强

## 📋 问题描述

在当前系统中，当某个节点（如节点A）的出块slot时间窗口已过（比如节点A由于故障下线），整个系统会继续等待，直到节点A的slot再次出现。这不符合TRON模式的行为。

**预期行为：**
- 节点A的slot过期后，系统应该立即跳到下一个出块者（节点B）
- 继续现有的逻辑：在epoch结束时进行故障检测
- 如果节点A漏块超过门槛，标记为故障
- 下一个epoch的出块者序列会自动过滤故障节点

## 🔧 修改内容

### 修改文件
- `consensus/dpos/block_scheduler.go`

### 修改方法
- `ShouldProduceBlockNow()` - 第290-430行

### 核心逻辑变更

**修改前：**
```go
} else if now.After(slotEnd) {
    // 时间窗口已过，跳过
    return false  // ❌ 直接返回false，系统继续等待
}
```

**修改后：**
```go
} else if now.After(slotEnd) {
    // 🆕 TRON模式：当前slot时间窗口已过，检查下一个slot是否轮到我
    nextSlot := currentSlot + 1
    nextExpectedIndex := nextSlot % bs.validatorCount
    
    // 如果下一个slot轮到我，可以出块（跳过故障的节点）
    if validatorIndex == nextExpectedIndex {
        nextSlotStart := bs.genesisTime.Add(time.Duration(nextSlot) * bs.blockWindow)
        nextSlotEnd := nextSlotStart.Add(bs.blockWindow)
        
        // 检查是否在下一个slot的时间窗口内
        if now.After(nextSlotStart) && now.Before(nextSlotEnd) {
            bs.logger.Info("✅ 前一个slot已过期，当前是我的下一个slot", ...)
            return true  // ✅ 可以出块
        }
    }
    
    // 时间窗口已过且不是我的下一个slot
    return false
}
```

## 🎯 功能说明

### 1. 自动跳过故障节点
当某个节点的slot过期后，系统会自动跳到下一个节点：

```
场景：4个节点 [A, B, C, D]
节点A的slot过期 -> 系统自动跳到节点B
节点B出块 -> 继续轮转
```

### 2. TRON模式兼容
- 基于固定时间窗口的slot调度
- 使用绝对时间计算（而不是相对区块号）
- 超时后自动跳到下一个出块者

### 3. 故障检测机制
- **检测时机：** Epoch结束时的最后一个区块
- **检测方法：** 统计每个验证者的漏块数
- **故障标记：** 漏块数 > MaxMissedBlocks
- **过滤机制：** 下一个epoch的出块者序列自动过滤故障节点

## 📊 影响范围

### ✅ 受影响
- **只影响：** `ShouldProduceBlockNow()` 的判断逻辑
- **行为变化：** 超时后允许跳到下一个出块者

### ✅ 不受影响
- `updateRound()` - 轮次更新逻辑（独立计算）
- `getCurrentDelegate()` - 获取当前出块者
- Epoch切换和故障检测
- 区块生产和验证流程
- 投票和BLS签名
- 奖励分发

## 🔍 代码示例

### 场景1：正常出块
```
时间：10:00:00 - 10:00:02 (节点A的slot)
节点A：✅ 出块
```

### 场景2：节点A故障
```
时间：10:00:00 - 10:00:02 (节点A的slot) - A故障不出块
时间：10:00:02 - 10:00:04 (节点B的slot)
节点B：✅ 检测到前一个slot过期，立即出块
```

### 场景3：节点A恢复
```
节点A：下一个epoch结束后如果漏块<门槛，继续在出块序列
```

## 🧪 测试建议

### 单元测试
```go
// 测试：slot超时后跳到下一个出块者
func TestSlotTimeout(t *testing.T) {
    scheduler := NewBlockScheduler(...)
    
    // 当前时间已过slot X
    now := time.Now()
    pastSlot := now.Add(-5 * time.Second) // 5秒前的slot
    
    // 设置系统时间为下一个slot
    result := scheduler.ShouldProduceBlockNow(
        nextValidatorIndex, 
        currentBlockNumber
    )
    
    assert.True(t, result) // 应该返回true
}
```

### 集成测试
1. **单节点故障测试**
   - 启动4个节点
   - 停止节点A
   - 验证系统继续出块（节点B、C、D）

2. **Epoch故障检测测试**
   - 让节点A漏掉5个块
   - Epoch结束时应该标记为故障
   - 下一个epoch的出块序列应该过滤节点A

## 📝 日志输出

修改后会新增以下日志：

```
✅ 前一个slot已过期，当前是我的下一个slot
  validatorIndex=1
  currentSlot=0 (已过期)
  nextSlot=1
  nextExpectedIndex=1
  action="在下一个slot出块"
```

## 🔒 安全性

- **并发安全：** 使用 `bs.mutex.RLock()` 保护
- **线程安全：** 只读访问，不修改状态
- **一致性：** 所有节点基于相同时间计算，结果一致

## 📈 性能影响

- **最小
- 新增一次 slot 计算（很快）
- 仅需一次取模运算
- 对系统性能影响可忽略

## 🎉 总结

此次修改实现了TRON模式的slot超时自动跳过功能：

1. ✅ 节点故障后系统自动跳到下一个出块者
2. ✅ 在epoch结束时进行故障检测
3. ✅ 自动过滤故障节点从下一个epoch出块序列
4. ✅ 向后兼容，不影响现有功能
5. ✅ 影响范围很小，只修改判断逻辑

修改已完成！✅

