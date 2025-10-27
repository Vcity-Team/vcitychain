# 节点A故障等待问题分析

## 🔍 问题现象

节点A故障后，其余4个节点都打印日志：
```
⏭️ 不是当前委托者，跳过区块生产: 
  currentDelegate=0x2B34f911915FB398DC2b055639Ca76A613CFFedb  // 节点A的地址
  keyAddr=0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9          // 节点B的地址
  currentDelegateIndex=4                                      // 可能是节点A的索引
```

所有节点都在等待，没有节点出块。

## 🔎 问题根源

### 调用链分析

1. **主循环** (`continuousBlockMonitoring`, 第1018-1186行)
   - 每10毫秒循环一次
   - 调用 `shouldProduceBlockNow()` 检查是否应该出块
   - 如果是当前委托者（`currentDelegate == keyAddr`），才调用 `produceBlock()`

2. **shouldProduceBlockNow()** (第1189-1234行)
   - 使用 `r.currentDelegateIndex` 调用 `ShouldProduceBlockNow()`
   - 返回true/false

3. **getCurrentDelegate()** (第2316-2380行)
   - 返回 `validators[r.currentDelegateIndex]`
   - 依赖 `r.currentDelegateIndex` 的值

4. **updateRound()** 在哪里被调用？
   - 第1712行：节点自己出块后调用 `r.updateRound(block.Block.Number())`
   - 第5370行：接收其他节点的区块后调用 `d.runtime.updateRound(header.Number)`

### 问题分析

**当前系统的问题**：

1. `currentDelegateIndex` 只在以下情况更新：
   - 节点出块后（第1712行）
   - 接收到新区块后（第5370行）

2. 如果节点A故障，不出块：
   - 没有新区块产生
   - `updateRound()` 不会被调用
   - `currentDelegateIndex` 一直停留在节点A的索引上

3. 其他节点的行为：
   - 主循环每10毫秒检查一次
   - 调用 `getCurrentDelegate()` 返回节点A的地址
   - `currentDelegate != keyAddr`（A != B），所以跳过
   - 继续等待...

4. 即使修改了 `ShouldProduceBlockNow()` 允许跳到下一个slot：
   - 它能返回true（让节点B可以出块）
   - 但是在第1155行，`currentDelegate == keyAddr` 检查失败
   - 因为 `getCurrentDelegate()` 还是返回节点A
   - 所以最终还是跳过

## 💡 解决方案

### 问题本质

`currentDelegateIndex` 需要**定期自动更新**，而不应该依赖新区块才能更新！

### 我的修改的局限性

我之前的修改只在 `ShouldProduceBlockNow()` 层面允许跳到下一个slot，但是：

1. **第4行检查** (第350-364行)：如果 `validatorIndex != expectedValidatorIndex`，直接返回false
   - 当前节点B的 `validatorIndex` 可能不等于 `expectedValidatorIndex`（节点A的索引）
   - 直接被拒，不会继续检查

2. **第15行检查** (第1155行)：如果 `currentDelegate != keyAddr`，跳过出块
   - `getCurrentDelegate()` 依赖 `currentDelegateIndex`
   - 如果 `currentDelegateIndex` 没有更新，还是返回节点A

### 解决方案1：在主循环中定期调用 updateRound()

在主循环中，定期调用 `updateRound()` 来更新 `currentDelegateIndex`：

```go
func (r *dposRuntime) continuousBlockMonitoring() {
	ticker := time.NewTicker(2 * time.Second) // 每2秒更新一次
	defer ticker.Stop()
	
	for {
		select {
		case <-r.closeCh:
			r.logger.Info("🛑 停止区块监测")
			return
		case <-ticker.C:
			// 🔧 定期更新 currentDelegateIndex（基于时间）
			r.updateRound()
		default:
			// ... 原有的逻辑
		}
	}
}
```

### 解决方案2：updateRound() 内部已经计算了

看第1805-1828行，`updateRound()` 内部会**主动**计算当前slot并更新 `currentDelegateIndex`！

```go
// 使用slot计算（与BlockScheduler保持一致）
if r.config.blockScheduler != nil {
    now := time.Now()
    timeSinceGenesis := now.Sub(genesisTime)
    currentSlot := int(timeSinceGenesis / blockWindow)
    
    if actualDelegateCount == 0 {
        r.currentDelegateIndex = 0
    } else {
        r.currentDelegateIndex = uint64(currentSlot % actualDelegateCount)
    }
}
```

**只需要定期调用 `updateRound()` 就行！**

## 📊 总结

**问题**：
- `currentDelegateIndex` 没有定期自动更新
- 节点A故障后，`currentDelegateIndex` 一直停留在A
- 其他节点认为当前应该出块的是A，都在等待

**解决方案**：
- 在主循环中定期调用 `updateRound()`（比如每1-2秒一次）
- `updateRound()` 内部已经会基于当前时间计算正确的索引

**我的修改无效的原因**：
- 修改了 `ShouldProduceBlockNow()` 的逻辑
- 但是在主循环的第1155行还有检查 `currentDelegate == keyAddr`
- 如果 `currentDelegateIndex` 没更新，这个检查永远失败

