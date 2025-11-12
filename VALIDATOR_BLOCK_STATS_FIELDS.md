# `dpos_getValidatorBlockStats` 返回字段说明

## 代码位置
- **RPC接口**: `jsonrpc/dpos_endpoint.go:3672-3697`
- **实现逻辑**: `consensus/dpos/query_stats.go:224-264`

## 返回字段说明（共7个）

### 1. `validatorAddress` (string)
- **类型**: 字符串
- **说明**: 验证者地址（十六进制格式，如 `0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127`）
- **来源**: 查询参数传入的验证者地址
- **用途**: 标识要查询的验证者

### 2. `epochNumber` (uint64)
- **类型**: 无符号整数
- **说明**: 查询的Epoch编号
- **来源**: 查询参数传入的Epoch编号
- **用途**: 标识查询哪个Epoch的统计数据

### 3. `blocksProduced` (uint64)
- **类型**: 无符号整数
- **说明**: 该验证者在该Epoch中实际出块的数量
- **来源**: `blockTracker.GetEpochBlockCounts(epochNumber)[validatorAddress]`
- **用途**: 显示验证者的出块表现

### 4. `totalEpochBlocks` (uint64)
- **类型**: 无符号整数
- **说明**: 该Epoch的总出块数（所有验证者出块的总和）
- **来源**: `blockTracker.GetTotalEpochBlocks(epochNumber)`
- **用途**: 用于计算出块占比

### 5. `blockPercentage` (float64)
- **类型**: 浮点数
- **说明**: 该验证者出块数占总出块数的百分比
- **计算公式**: `(blocksProduced / totalEpochBlocks) * 100`
- **特殊情况**: 如果 `totalEpochBlocks = 0`，则返回 `0.0`
- **用途**: 显示验证者的出块占比（0-100%）

### 6. `isActive` (bool)
- **类型**: 布尔值
- **说明**: 该验证者当前是否为活跃状态
- **来源**: 从 `getAllValidators()` 中查找对应验证者的 `IsActive` 字段
- **默认值**: 如果找不到该验证者，默认为 `false`
- **用途**: 标识验证者是否处于活跃状态

### 7. `votingPower` (string)
- **类型**: 字符串（大整数）
- **说明**: 该验证者的投票权重（总得票数）
- **来源**: 从 `getAllValidators()` 中查找对应验证者的 `VotingPower` 字段
- **默认值**: 如果找不到该验证者，默认为 `"0"`
- **格式**: 大整数字符串（wei单位）
- **用途**: 显示验证者的投票权重

---

## 示例返回

```json
{
  "validatorAddress": "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127",
  "epochNumber": 45,
  "blocksProduced": 0,
  "totalEpochBlocks": 100,
  "blockPercentage": 0.0,
  "isActive": false,
  "votingPower": "0"
}
```

---

## 注意事项

1. **如果 `blockTracker` 未初始化**，会返回错误：
   ```json
   {
     "error": "block tracker not initialized"
   }
   ```

2. **如果找不到验证者**：
   - `isActive` 默认为 `false`
   - `votingPower` 默认为 `"0"`

3. **出块占比计算**：
   - 如果 `totalEpochBlocks = 0`，`blockPercentage` 为 `0.0`
   - 否则按公式计算：`(blocksProduced / totalEpochBlocks) * 100`

---

## 如果实际返回了8个字段

如果实际返回结果中有第8个字段，可能是：
- 运行时动态添加的字段
- 其他代码路径添加的字段
- 或者需要查看实际返回结果才能确定

请提供实际返回的完整JSON，我可以指出多出来的字段。

