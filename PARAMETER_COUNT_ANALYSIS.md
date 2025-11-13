# 参数数量对比分析

## 代码中定义的数量

### 1. `dpos_getValidatorBlockStats` 返回字段数量

**代码位置**: `consensus/dpos/query_stats.go:255-263`

**返回字段**（共 **7个**）:
1. `validatorAddress`
2. `epochNumber`
3. `blocksProduced`
4. `totalEpochBlocks`
5. `blockPercentage`
6. `isActive`
7. `votingPower`

---

### 2. `dpos_getVotableParameters` 返回参数数量

**代码位置**: `consensus/dpos/governance_init.go:127-194`

**定义的参数**（共 **9个**）:
1. `dpos_reward_amount`
2. `dpos_delegate_threshold`
3. `block_time_s`
4. `dpos_epoch_duration`
5. `governance_voting_threshold`
6. `governance_min_voting_threshold`
7. `dpos_proposal_vote_period`
8. `min_freeze_period`
9. `unfreeze_lock_period`

---

## 用户观察到的数量

- **第一个命令** (`dpos_getValidatorBlockStats`): **8个字段**
- **第二个命令** (`dpos_getVotableParameters`): **9个参数**

---

## 差异分析

### 可能的原因：

1. **`GetValidatorBlockStats` 多了一个字段**:
   - 代码中定义7个，用户看到8个
   - 可能运行时动态添加了某个字段
   - 或者用户数错了

2. **`GetVotableParameters` 多了一个参数**:
   - 代码中定义8个，用户看到9个
   - 可能：
     - 有动态添加的参数（代码中未在 `getDefaultVotableParameters` 中定义）
     - 或者用户数错了

---

## 需要检查的地方

1. **检查是否有动态添加参数的代码**
2. **检查实际运行时的返回结果**
3. **确认用户看到的完整返回结构**

---

## 建议

请提供：
1. 完整的 `dpos_getValidatorBlockStats` 返回结果（所有字段）
2. 完整的 `dpos_getVotableParameters` 返回结果（parameters 对象中的所有键）

这样我可以准确识别多出来的字段/参数。

