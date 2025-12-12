# 删除 epochValidators 修改点总结

## 目标
完全删除 `epochValidators` 数据库缓存，以 `ExtraData` 作为唯一数据源。

## 修改点分类

### 1. 删除数据库存储/读取函数

#### 1.1 `state_store_stake.go`
- ❌ **删除** `SaveEpochValidators(validators validator.AccountSet) error` (约1239行)
- ❌ **删除** `GetEpochValidators() (validator.AccountSet, error)` (约1261行)
- ❌ **删除** 数据库 bucket `"epochValidators"` 相关代码

#### 1.2 `validator_mgmt_fault.go`
- ❌ **删除** `saveNextEpochValidators(validators validator.AccountSet) error` (约763-797行)
- ❌ **删除** `getEpochValidatorsFromDatabase() (validator.AccountSet, error)` (约865-941行)
- ❌ **删除** `applyNextEpochValidatorsFromExtra(validators validator.AccountSet, blockNumber uint64) error` (约799-860行)
- ❌ **删除** `alignNextEpochValidatorsWithConfig(blockNumber uint64) error` (如果存在)

### 2. 删除/修改 `getValidatorsForEpoch` 函数

#### 2.1 `validator_mgmt_fault.go`
- ❌ **删除** `getValidatorsForEpoch` 中的数据库回退逻辑（方式3：从数据库读取）
- ✅ **保留** 方式1：从 ExtraData 读取
- ✅ **保留** 方式2：从内存 delegates 读取（作为临时回退）

### 3. 修改出块逻辑 - 从 ExtraData 读取

#### 3.1 `block_scheduler.go`
- ❌ **删除** `getEpochValidatorsFromDatabase()` 调用 (240行)
- ✅ **修改** 回退逻辑：直接使用 `GetSortedValidatorsWithLimitFilterFaulty()` 或从当前区块的 ExtraData 读取
- ✅ **修改** 注释：删除 "getEpochValidatorsFromDatabase() 已经处理了截取和保存" 相关注释

#### 3.2 `block_delegate.go`
- ❌ **删除** `getEpochValidatorsFromDatabase()` 调用 (20行)
- ✅ **修改** 回退逻辑：从当前区块的 ExtraData 读取或使用 `GetSortedValidatorsWithLimitFilterFaulty()`

#### 3.3 `block_production.go`
- ✅ **确认** 是否已从 ExtraData 读取，如果没有则修改

### 4. 删除启动时的比对和覆盖逻辑

#### 4.1 `validator_mgmt_fault.go`
- ❌ **删除** `executeSlashing` 中调用 `getEpochValidatorsFromDatabase()` 的逻辑 (485行)
- ❌ **删除** 启动时的比对逻辑（如果有）

#### 4.2 `validator_mgmt_delegate.go` 或 `storage.go`
- ❌ **删除** 启动时调用 `alignNextEpochValidatorsWithConfig` 的逻辑
- ❌ **删除** `loadValidatorsFromDatabaseWithLimit` 中保存到 `epochValidators` 的逻辑

### 5. 删除结构体字段

#### 5.1 `dpos.go`
- ❌ **删除** `epochValidators validator.AccountSet` 字段 (332行)

#### 5.2 `fault_detector.go`
- ❌ **删除** `fd.dposInstance.epochValidators = validators` 赋值 (51行)

### 6. 删除同步写入逻辑

#### 6.1 `blockchain_wrapper.go`
- ❌ **删除** `updateNextEpochValidatorsFromLocal` 中调用 `saveNextEpochValidators` 的逻辑 (957行)
- ✅ **保留** ExtraData 写入逻辑（这是唯一数据源）

#### 6.2 `validator_mgmt_fault.go`
- ❌ **删除** `calculateNextEpochValidators` 中调用 `saveNextEpochValidators` 的逻辑 (538行)
- ✅ **保留** 计算逻辑，但只返回结果，不保存到数据库

### 7. 修改依赖注入

#### 7.1 `epoch_module.go`
- ❌ **删除** `SaveNextEpochValidators` 依赖注入 (142行)
- ✅ **保留** `CalculateNextEpochValidators` 依赖注入

#### 7.2 `modules/epoch/lifecycle.go`
- ❌ **删除** `SaveNextEpochValidators` 接口定义和调用 (38行, 269-270行)
- ✅ **保留** `CalculateNextEpochValidators` 接口定义和调用

#### 7.3 `state_module.go`
- ❌ **删除** `SaveEpochValidators` 调用 (70行)

#### 7.4 `adapters.go`
- ❌ **删除** `SaveEpochValidators` 调用 (521行)

### 8. 修改 ExtraData 读取逻辑

#### 8.1 `extra.go`
- ✅ **确认** `NextEpochValidators` 解析逻辑正确
- ✅ **确认** 从 ExtraData 读取验证者集合的逻辑完整

#### 8.2 `validator_provider.go`
- ✅ **修改** `GetValidatorsForEpoch` 优先从 ExtraData 读取
- ❌ **删除** 数据库回退逻辑

### 9. 其他调用点检查

#### 9.1 `fault_detector.go`
- ✅ **确认** `getValidatorsForEpoch` 调用是否受影响 (68行)
- ✅ **修改** 如果 `getValidatorsForEpoch` 删除数据库回退，这里需要确保有正确的回退

#### 9.2 `validator_mgmt_fault.go`
- ✅ **确认** `executeSlashing` 中 `getValidatorsForEpoch` 的使用 (637行)
- ✅ **修改** 如果删除数据库回退，确保有正确的回退逻辑

## 修改优先级

### 高优先级（核心逻辑）
1. ✅ 修改出块逻辑，从 ExtraData 读取 (`block_scheduler.go`, `block_delegate.go`)
2. ✅ 删除 `getEpochValidatorsFromDatabase` 函数
3. ✅ 删除 `saveNextEpochValidators` 函数
4. ✅ 删除数据库存储/读取函数 (`SaveEpochValidators`, `GetEpochValidators`)

### 中优先级（依赖清理）
5. ✅ 删除 `applyNextEpochValidatorsFromExtra` 函数
6. ✅ 删除结构体字段 (`dpos.go`, `fault_detector.go`)
7. ✅ 删除依赖注入 (`epoch_module.go`, `modules/epoch/lifecycle.go`)

### 低优先级（清理和优化）
8. ✅ 删除同步写入逻辑 (`blockchain_wrapper.go`)
9. ✅ 修改 `getValidatorsForEpoch` 删除数据库回退
10. ✅ 清理注释和日志

## 注意事项

1. **ExtraData 是唯一数据源**：所有验证者集合必须从区块的 ExtraData 读取
2. **故障过滤**：出块时使用 `GetSortedValidatorsWithLimitFilterFaulty()` 确保故障节点不参与
3. **回退逻辑**：如果 ExtraData 不可用，可以回退到内存 `delegates` 或实时查询（带故障过滤）
4. **启动时**：不再从数据库加载 `epochValidators`，完全依赖 ExtraData
5. **测试**：确保删除后所有出块逻辑正常工作
