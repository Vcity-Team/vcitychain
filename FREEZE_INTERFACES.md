# 冻结资金机制 - 接口设计文档（方案1，简化版）

## 一、核心冻结/解冻接口

### 1. 注册受托人（冻结资金）
**接口名**: `dpos_registerDelegate` (已存在，需修改)
**代码位置**: 
- RPC接口: `jsonrpc/dpos_endpoint.go:4438` - `RegisterDelegate`
- 核心逻辑: `consensus/dpos/validator_mgmt_delegate.go:738` - `RegisterDelegateWithKeyAndChainID`
- 交易创建: `consensus/dpos/validator_mgmt_delegate.go:787` - `createDelegateRegistrationTransactionWithChainID`
- 交易处理: `consensus/dpos/validator_mgmt_delegate.go:377` - `processDelegateRegistrationTransaction`

**当前实现分析**:
- 交易创建时设置了 `tx.Value = depositAmount`（第827行）
- 交易处理时只保存了 `Deposit` 字段（第441行），**没有明确的冻结逻辑**
- **需要修改**: 将 `tx.Value` 的处理改为冻结资金，而不是直接转账

**功能**: 注册受托人时冻结保证金
**参数**:
```json
{
  "registrant": "0x...",      // 注册者地址
  "name": "Delegate Name",    // 受托人名称
  "website": "https://...",    // 网站
  "description": "...",       // 描述
  "privateKey": "0x...",       // 私钥（用于签名）
  "chainID": 20230826          // 链ID
}
```
**返回**:
```json
{
  "success": true,
  "message": "Delegate registration submitted successfully",
  "txHash": "0x...",
  "frozenAmount": "100000000000000000000",  // 冻结金额
  "frozenAt": 1234567890                    // 冻结时间戳
}
```
**说明**: 
- 修改现有接口，将 `tx.Value` 的处理改为冻结资金（不直接转账）
- 冻结金额 = 保证金金额（delegate_deposit_amount）
- 冻结后资金仍属于用户，但不可用

---

### 2. 退出受托人（直接解冻）
**接口名**: `dpos_withdrawDelegate` (已存在，需修改)
**代码位置**: 
- RPC接口: `jsonrpc/dpos_endpoint.go:4536` - `WithdrawDelegate`
- 核心逻辑: `consensus/dpos/validator_mgmt_delegate.go:1188` - `WithdrawDelegate`

**当前实现分析**:
- 当前只检查是否有投票（第1203行）
- 只更新状态，**没有解冻逻辑**
- **需要修改**: 添加解冻逻辑，如果满足条件（无投票或已撤回投票），直接解冻资金

**功能**: 退出受托人，如果满足条件则直接解冻保证金（类似 Tron，无两阶段）

**处理流程（参考 Tron）**:
1. **检查投票状态**: 检查受托人是否还有投票（`TotalVotes > 0`）
   - **Tron 机制**: Tron 要求用户先手动撤回投票，然后才能退出
   - **实现方式**: 如果有投票，返回错误，提示用户先手动撤回投票
     - 错误信息：`"cannot withdraw while having votes. Please use dpos_vote to withdraw votes first"`
     - 返回投票信息：`totalVotes`, `votedDelegates` 等
     - 用户必须使用 `dpos_vote` 接口手动撤回所有投票后，才能退出

2. **检查解冻时间要求**: 检查是否满足解冻期要求
   - 如果注册时间不足最小冻结期（如7天），返回错误
   - 错误信息：`"cannot withdraw before minimum freeze period. Registered at: {frozenAt}, minimum period: {minPeriod} seconds"`
   - 返回剩余时间：`remainingTime`, `unfreezeAvailableAt`

3. **执行解冻**: 如果满足所有条件（无投票且满足解冻期）
   - 冻结资金解冻（进入锁定期）
   - 更新状态为 `RegStatusWithdrawn`
   - 从受托人列表中移除
   - 返回成功信息

**实现说明**:
- **参考 Tron 最佳实践**: 要求用户先手动撤回投票
  - 优点：用户明确控制，避免意外撤票
  - 用户必须使用 `dpos_vote` 接口手动撤回所有投票
  - 只有在无投票的情况下才能退出

**参数**:
```json
{
  "address": "0x...",          // 受托人地址
  "privateKey": "0x..."         // 私钥（用于签名交易，可选）
}
```

**返回（成功）**:
```json
{
  "success": true,
  "message": "Delegate withdrawn and unfrozen successfully",
  "txHash": "0x...",
  "unfrozenAmount": "100000000000000000000",  // 解冻金额
  "unfrozenAt": 1234567890,                    // 解冻时间
  "lockPeriod": 1209600,                       // 锁定期（秒，14天）
  "unfreezeAvailableAt": 1235777490            // 资金可用时间（当前时间 + 锁定期）
}
```

**返回（有投票，需要先撤回）**:
```json
{
  "success": false,
  "error": "cannot withdraw while having votes. Please use dpos_vote to withdraw votes first",
  "code": "HAS_ACTIVE_VOTES",
  "voteInfo": {
    "totalVotes": "500000000000000000000",
    "votedDelegates": [
      {
        "delegate": "0x...",
        "votes": "300000000000000000000"
      },
      {
        "delegate": "0x...",
        "votes": "200000000000000000000"
      }
    ]
  },
  "withdrawVoteInterface": "dpos_vote",
  "note": "Use dpos_vote with amount=0 or negative amount to withdraw votes manually"
}
```

**返回（不满足解冻期）**:
```json
{
  "success": false,
  "error": "cannot withdraw before minimum freeze period",
  "code": "MIN_FREEZE_PERIOD_NOT_MET",
  "frozenAt": 1234567890,
  "currentTime": 1235000000,
  "minFreezePeriod": 604800,                   // 最小冻结期（秒，7天）
  "elapsedTime": 43410,                        // 已冻结时间（秒）
  "remainingTime": 561390,                     // 剩余时间（秒）
  "unfreezeAvailableAt": 1235126390            // 最早可解冻时间
}
```

**说明**:
- **参考 Tron**: 直接退出注册，无需两阶段
- **撤回投票机制（参考 Tron 最佳实践）**:
  - **Tron 的做法**: 用户必须先手动撤回投票，然后才能申请退出
  - **我们的实现**: 要求用户先手动撤回投票
    - 如果有投票，返回错误，提示用户使用 `dpos_vote` 撤回投票
    - 用户必须手动撤回所有投票后，才能退出
    - 用户明确控制，避免意外撤票
- **撤回投票接口**: `dpos_vote` (在 `jsonrpc/dpos_endpoint.go:326`)
  - 使用方式：调用 `dpos_vote` 时，将 `amount` 设置为 0 或负数来撤回投票
    - `amount = 0`: 撤回对该受托人的所有投票
    - `amount < 0`: 减少投票（绝对值表示减少的数量）
  - 需要修改：当前代码中投票验证不允许 amount <= 0，需要修改验证逻辑（`consensus/dpos/voting_validator.go:55-56`）
- **解冻期检查**: 必须满足最小冻结期要求（防止频繁注册/退出）
- 如果满足条件（无投票且满足解冻期），直接解冻资金
- 解冻后进入锁定期（14天），锁定期结束后资金可用
- 状态更新为 RegStatusWithdrawn

---

## 二、查询冻结信息接口

### 3. 查询冻结信息（智能接口，支持单个和批量）
**接口名**: `dpos_getFreezeInfo` (新增)
**功能**: 查询账户的冻结信息（智能接口，自动识别是否为受托人并返回相应信息）
**参数（单个地址）**:
```json
{
  "address": "0x..."           // 账户地址
}
```

**参数（批量查询）**:
```json
{
  "addresses": ["0x...", "0x...", "0x..."]  // 地址列表（最多100个）
}
```
**返回（普通账户，有冻结资金）**:
```json
{
  "success": true,
  "address": "0x...",
  "isDelegate": false,                          // 是否为受托人
  "frozenAmount": "100000000000000000000",      // 冻结金额
  "frozenAt": 1234567890,                       // 冻结时间
  "unfreezeAt": 1235777490,                     // 解冻时间（退出注册时设置）
  "unfreezeAvailableAt": 1235777490,           // 资金可用时间（解冻时间 + 锁定期）
  "lockPeriod": 1209600,                        // 锁定期（秒，14天）
  "remainingLockTime": 864000,                  // 剩余锁定时间（秒，0表示已可用）
  "canWithdraw": false,                         // 是否可以提取（锁定期已过）
  "status": "frozen"                            // 状态：frozen（冻结中）, unfreezing（解冻中，锁定期内）, withdrawn（已提取）
}
```

**返回（受托人账户）**:
```json
{
  "success": true,
  "address": "0x...",
  "isDelegate": true,                           // 是否为受托人
  "frozenAmount": "100000000000000000000",      // 冻结金额
  "frozenAt": 1234567890,                       // 冻结时间
  "unfreezeAt": 0,                              // 解冻时间（0表示未解冻）
  "unfreezeAvailableAt": 0,                     // 资金可用时间（0表示未解冻）
  "lockPeriod": 1209600,                        // 锁定期（秒，14天）
  "remainingLockTime": 0,                        // 剩余锁定时间
  "canWithdraw": false,                         // 是否可以提取
  "status": "frozen",                           // 状态
  "registrationInfo": {                         // 受托人注册信息（仅受托人返回）
    "name": "Delegate Name",
    "status": "frozen",
    "deposit": "100000000000000000000",
    "createdAt": 1234567890
  },
  "voteInfo": {                                 // 投票信息（仅受托人返回）
    "totalVotes": "500000000000000000000",
    "hasVotes": true
  }
}
```

**返回（普通账户，无冻结资金）**:
```json
{
  "success": true,
  "address": "0x...",
  "isDelegate": false,
  "frozenAmount": "0",
  "status": "none"                              // 无冻结资金
}
```

**返回（批量查询）**:
```json
{
  "success": true,
  "freezeInfos": [
    {
      "address": "0x...",
      "isDelegate": true,
      "frozenAmount": "100000000000000000000",
      "frozenAt": 1234567890,
      "unfreezeAt": 0,
      "unfreezeAvailableAt": 0,
      "lockPeriod": 1209600,
      "remainingLockTime": 0,
      "canWithdraw": false,
      "status": "frozen",
      "registrationInfo": {                    // 仅受托人返回
        "name": "Delegate Name",
        "status": "frozen",
        "deposit": "100000000000000000000",
        "createdAt": 1234567890
      },
      "voteInfo": {                            // 仅受托人返回
        "totalVotes": "500000000000000000000",
        "hasVotes": true
      }
    },
    // ...
  ],
  "total": 3
}
```

**说明**: 
- **智能接口**: 自动识别账户是否为受托人
- **受托人账户**: 自动包含注册信息和投票信息
- **普通账户**: 只返回冻结信息（如果有）
- **统一接口**: 支持单个地址和批量查询，避免接口重复
- **参数灵活性**: 
  - 传入 `address`（字符串）: 单个查询，返回单个对象
  - 传入 `addresses`（数组）: 批量查询，返回数组
- **批量限制**: 最多支持100个地址

---

## 三、账户余额相关接口

### 5. 查询账户余额（包含冻结）
**接口名**: `dpos_getAccountBalance` (新增)
**功能**: 查询账户余额，包括可用余额和冻结余额
**参数**:
```json
{
  "address": "0x..."           // 账户地址
}
```
**返回**:
```json
{
  "success": true,
  "address": "0x...",
  "availableBalance": "500000000000000000000",  // 可用余额（不包含冻结）
  "frozenBalance": "100000000000000000000",      // 冻结余额
  "totalBalance": "600000000000000000000"       // 总余额 = 可用 + 冻结
}
```
**说明**:
- **现有查询余额接口**: `eth_getBalance` (在 `jsonrpc/eth_endpoint.go:740`)
- `eth_getBalance` 只返回可用余额（不包含冻结）
- `dpos_getAccountBalance` 返回完整余额信息（包含冻结）

---

**注意**: 删除 `dpos_getFrozenBalance` 接口，因为：
- `dpos_getAccountBalance` 已包含冻结余额信息
- `dpos_getFreezeInfo` 已包含详细的冻结信息
- 避免接口重复

---

## 四、冻结历史记录接口

### 6. 查询冻结历史
**接口名**: `dpos_getFreezeHistory` (新增)
**功能**: 查询账户的冻结/解冻历史记录
**参数**:
```json
{
  "address": "0x...",          // 账户地址
  "fromBlock": 0,              // 起始区块（可选）
  "toBlock": 0,                // 结束区块（可选）
  "limit": 100                 // 返回记录数限制（可选）
}
```
**返回**:
```json
{
  "success": true,
  "address": "0x...",
  "history": [
    {
      "type": "freeze",                        // freeze（冻结）, unfreeze（解冻）
      "amount": "100000000000000000000",
      "blockNumber": 12345,
      "txHash": "0x...",
      "timestamp": 1234567890,
      "description": "Delegate registration deposit"
    },
    {
      "type": "unfreeze",
      "amount": "100000000000000000000",
      "blockNumber": 23456,
      "txHash": "0x...",
      "timestamp": 1234567890,
      "unfreezeAvailableAt": 1235777490        // 资金可用时间
    }
  ]
}
```

---

## 五、参数配置接口

### 7. 查询冻结相关参数
**说明**: 
- **已合并到 `dpos_getVotableCurrentParameters`**: 冻结相关参数（`min_freeze_period` 和 `unfreeze_lock_period`）已加入到可表决参数列表中，可通过 `dpos_getVotableCurrentParameters` 统一查询
- **参数说明**:
  - `min_freeze_period`: 最小冻结期（秒，7天，从YAML读取，可通过治理提案修改）
  - `unfreeze_lock_period`: 解冻锁定期（秒，14天，从YAML读取，可通过治理提案修改）
  - `delegate_deposit_amount`: 保证金金额（固定值，不在可表决参数中，可通过其他接口查询）
- **使用方式**: 调用 `dpos_getVotableCurrentParameters`，在返回的 `parameters` 对象中查找 `min_freeze_period` 和 `unfreeze_lock_period`

---

## 六、辅助工具接口

### 8. 检查是否可以退出注册
**接口名**: `dpos_canWithdrawDelegate` (新增)
**功能**: 检查受托人是否可以退出注册（解冻）
**参数**:
```json
{
  "address": "0x..."           // 受托人地址
}
```
**返回**:
```json
{
  "success": true,
  "address": "0x...",
  "canWithdraw": false,                    // 是否可以退出
  "reason": "has_active_votes",            // 原因（如果可以退出则为空）
  "details": {
    "hasFrozenAmount": true,
    "frozenAmount": "100000000000000000000",
    "hasVotes": true,
    "totalVotes": "500000000000000000000"
  }
}
```

---

## 八、接口优先级分类（简化后）

### 🔴 高优先级（核心功能，必须实现）
1. `dpos_registerDelegate` (修改) - 注册时冻结资金
   - 代码位置: `jsonrpc/dpos_endpoint.go:4438`, `consensus/dpos/validator_mgmt_delegate.go:738`
   - 修改点: 将 `tx.Value` 处理改为冻结，而非直接转账

2. `dpos_withdrawDelegate` (修改) - 退出受托人并解冻
   - 代码位置: `jsonrpc/dpos_endpoint.go:4536`, `consensus/dpos/validator_mgmt_delegate.go:1188`
   - 修改点: 添加解冻逻辑，满足条件时直接解冻（无需两阶段）

3. `dpos_getFreezeInfo` (新增) - 查询冻结信息
   - 通用接口，查询账户冻结状态

4. `dpos_getAccountBalance` (新增) - 查询余额（含冻结）
   - 补充 `eth_getBalance`（只返回可用余额）

### 🟡 中优先级（重要功能，建议实现）
5. `dpos_getFreezeHistory` (新增) - 冻结历史记录
   - 查询冻结/解冻历史

6. `dpos_canWithdrawDelegate` (新增) - 检查是否可以退出
   - 辅助接口，检查退出条件


### ❌ 已删除的接口（重复）
- ~~`dpos_getDelegateFreezeInfo`~~ - 与 `dpos_getFreezeInfo` 重复，已合并为智能接口

### ❌ 已删除的接口（不需要）
- ~~`dpos_requestUnfreeze`~~ - 不需要两阶段，退出时直接解冻
- ~~`dpos_withdrawUnfreeze`~~ - 不需要两阶段，退出时直接解冻
- ~~`dpos_getFrozenBalance`~~ - 与 `dpos_getAccountBalance` 重复
- ~~`dpos_calculateUnfreezeTime`~~ - 不需要请求解冻，此接口多余

---

## 十、接口实现注意事项

### 1. 安全性
- 所有涉及资金操作的接口都需要私钥签名
- 验证锁定期和状态
- 防止重复解冻和提取

### 2. 一致性
- 冻结金额必须等于保证金金额（固定值）
- 解冻流程：退出注册时直接解冻，无需两阶段（参考 Tron）
- 状态转换必须符合状态机

### 3. 性能
- 批量查询接口需要优化
- 历史记录查询需要分页
- 统计信息可以缓存

### 4. 兼容性
- 保持现有接口的向后兼容
- 新增字段使用可选参数
- 提供迁移路径

---

## 十一、接口调用示例

### 完整流程示例（简化版，参考 Tron）

```javascript
// 1. 注册受托人（冻结资金）
const registerResult = await rpc.call('dpos_registerDelegate', {
  registrant: '0x...',
  name: 'My Delegate',
  website: 'https://...',
  description: '...',
  privateKey: '0x...',
  chainID: 20230826
});
// 返回: { success: true, txHash: '0x...', frozenAmount: '100...', frozenAt: 1234567890 }

// 2. 查询冻结信息（智能接口，自动识别是否为受托人）
const freezeInfo = await rpc.call('dpos_getFreezeInfo', {
  address: '0x...'
});
// 如果是受托人，返回: { 
//   isDelegate: true,
//   frozenAmount: '100...', 
//   status: 'frozen', 
//   canWithdraw: false,
//   registrationInfo: { name: '...', ... },
//   voteInfo: { totalVotes: '...', ... }
// }
// 如果是普通账户，返回: { 
//   isDelegate: false,
//   frozenAmount: '100...', 
//   status: 'frozen', 
//   canWithdraw: false
// }

// 3. 查询账户余额（包含冻结）
const balance = await rpc.call('dpos_getAccountBalance', {
  address: '0x...'
});
// 返回: { availableBalance: '500...', frozenBalance: '100...', totalBalance: '600...' }

// 4. 检查是否可以退出
const canWithdraw = await rpc.call('dpos_canWithdrawDelegate', {
  address: '0x...'
});
// 返回: { canWithdraw: false, reason: 'has_active_votes' }

// 5. 如果有投票，先手动撤回投票（推荐方式，参考 Tron）
// 方式1：撤回对特定受托人的投票
const withdrawVoteResult = await rpc.call('dpos_vote', {
  voter: '0x...',
  candidate: '0x...',  // 要撤回投票的受托人地址
  amount: '0',         // amount=0 表示撤回所有投票（需要修改代码支持）
  privateKey: '0x...'
});
// 方式2：减少投票（使用负数）
// const withdrawVoteResult = await rpc.call('dpos_vote', {
//   voter: '0x...',
//   candidate: '0x...',
//   amount: '-100000000000000000000',  // 减少100个代币的投票
//   privateKey: '0x...'
// });

// 6. 退出受托人（直接解冻，无需两阶段）
// 注意：必须先手动撤回所有投票，否则会返回错误
const withdrawResult = await rpc.call('dpos_withdrawDelegate', {
  address: '0x...',
  privateKey: '0x...'
});

// 情况1：有投票，需要先撤回
// 返回: { 
//   success: false,
//   error: 'cannot withdraw while having votes. Please use dpos_vote to withdraw votes first',
//   code: 'HAS_ACTIVE_VOTES',
//   voteInfo: { totalVotes: '500...', votedDelegates: [...] },
//   withdrawVoteInterface: 'dpos_vote',
//   note: 'Use dpos_vote with amount=0 or negative amount to withdraw votes manually'
// }

// 情况2：不满足解冻期
// 返回: {
//   success: false,
//   error: 'cannot withdraw before minimum freeze period',
//   code: 'MIN_FREEZE_PERIOD_NOT_MET',
//   remainingTime: 561390,
//   unfreezeAvailableAt: 1235126390
// }

// 情况3：成功退出
// 返回: { 
//   success: true, 
//   unfrozenAmount: '100...', 
//   unfrozenAt: 1234567890,
//   unfreezeAvailableAt: 1235777490,  // 14天后可用
//   lockPeriod: 1209600
// }

// 7. 等待锁定期（14天）后，资金自动可用
// 无需额外操作，锁定期结束后资金自动转为可用余额
```

---

## 十二、与 Tron 接口对比

| 功能 | Tron 接口 | 我们的接口 | 说明 |
|------|-----------|------------|------|
| 冻结资金 | `wallet/freezebalancev2` | `dpos_registerDelegate` (修改) | 注册受托人时冻结 |
| 解冻资金 | `wallet/unfreezebalancev2` | `dpos_withdrawDelegate` (修改) | **直接解冻，无需两阶段** |
| 查询冻结 | `wallet/getaccountresource` | `dpos_getFreezeInfo` (新增) | 查询冻结信息 |
| 查询余额 | `wallet/getaccount` | `dpos_getAccountBalance` (新增) | 查询余额（含冻结） |

**关键差异**:
- Tron 没有两阶段解冻流程，退出注册时直接解冻
- 我们的设计也采用相同方式，简化流程

---

## 十三、关键修改点总结

### 代码修改位置

1. **注册受托人 - 冻结逻辑**
   - 文件: `consensus/dpos/validator_mgmt_delegate.go`
   - 函数: `processDelegateRegistrationTransaction` (第377行)
   - 修改: 将 `tx.Value` 的处理改为冻结资金，而非直接转账
   - 需要: 添加冻结余额字段到账户模型

2. **退出受托人 - 解冻逻辑**
   - 文件: `consensus/dpos/validator_mgmt_delegate.go`
   - 函数: `WithdrawDelegate` (第1188行)
   - 修改内容:
     - 检查投票状态：如果有投票，返回错误并提示使用 `dpos_vote` 手动撤回投票（参考 Tron）
     - 检查解冻期：验证是否满足最小冻结期要求（从 YAML 配置读取）
     - 添加解冻逻辑：满足条件时直接解冻（无需两阶段）
   - 需要: 
     - 实现锁定期机制（从 YAML 配置读取，默认14天）
     - 实现最小冻结期检查（从 YAML 配置读取，默认7天）
     - 实现冻结余额解冻逻辑
     - **配置读取**: 从 `DPoSConfig` 中读取解冻期配置
     - **投票检查**: 必须无投票才能退出，要求用户先手动撤回

3. **撤回投票接口（参考 Tron）**
   - **Tron 机制**: 用户必须先手动撤回投票，然后才能申请退出
   - **现有接口**: `dpos_vote` (在 `jsonrpc/dpos_endpoint.go:326`)
   - **当前问题**: 代码验证不允许 `amount <= 0`（`voting_validator.go:55-56`）
   - **实现方案**: 修改 `validateVote` 函数，支持撤回投票
     - `amount = 0`: 撤回对该受托人的所有投票
     - `amount < 0`: 减少投票（绝对值表示减少的数量）
     - 修改位置: `consensus/dpos/voting_validator.go:52-67` - `validateVote` 函数
     - 修改逻辑:
       ```go
       // 修改前: 不允许 amount <= 0
       if vote.Amount.Cmp(big.NewInt(0)) <= 0 {
           return errors.New("vote amount must be positive")
       }
       
       // 修改后: 允许 amount <= 0 表示撤回投票
       // amount = 0: 撤回所有投票
       // amount < 0: 减少投票（绝对值）
       if vote.Amount.Cmp(big.NewInt(0)) <= 0 {
           // 检查是否有足够的投票可以撤回
           // 实现撤回逻辑
       }
       ```
   - **退出时的投票处理**: 要求用户先手动撤回投票，然后才能退出
     - 如果有投票，返回错误，提示用户使用 `dpos_vote` 撤回投票
     - 只有在无投票的情况下才能退出

4. **账户余额模型扩展**
   - 需要添加: `frozenBalance` 字段
   - 现有: `eth_getBalance` 只返回可用余额
   - 新增: `dpos_getAccountBalance` 返回完整余额信息

5. **YAML 配置文件扩展**
   - 配置文件位置: `command/server/config/config.go`
   - 需要添加的配置项:
     ```yaml
     dpos:
       # 冻结相关配置
       freeze:
         min_freeze_period: 604800      # 最小冻结期（秒，7天）
         unfreeze_lock_period: 1209600  # 解冻锁定期（秒，14天）
     ```
   - 代码修改位置:
     - 配置结构: `consensus/dpos/dpos.go:119` - `DPoSConfig` 结构体
     - 添加字段:
       ```go
       type DPoSConfig struct {
           // ... 现有字段 ...
           
           // 冻结相关配置
           MinFreezePeriod    uint64 `json:"min_freeze_period" yaml:"min_freeze_period"`       // 最小冻结期（秒）
           UnfreezeLockPeriod uint64 `json:"unfreeze_lock_period" yaml:"unfreeze_lock_period"` // 解冻锁定期（秒）
       }
       ```
     - 默认值设置: `consensus/dpos/dpos.go:1944` - `DefaultDPoSConfig` 函数
       ```go
       func DefaultDPoSConfig() *DPoSConfig {
           return &DPoSConfig{
               // ... 现有默认值 ...
               MinFreezePeriod:    604800,  // 默认7天
               UnfreezeLockPeriod: 1209600, // 默认14天
           }
       }
       ```
     - 读取配置: 在 `WithdrawDelegate` 函数中通过 `d.config` 访问
       ```go
       minFreezePeriod := d.config.MinFreezePeriod
       unfreezeLockPeriod := d.config.UnfreezeLockPeriod
       ```

---

## 总结

### 简化后的接口列表

**必须实现（4个）**:
1. `dpos_registerDelegate` (修改) - 注册时冻结
2. `dpos_withdrawDelegate` (修改) - 退出时解冻
3. `dpos_getFreezeInfo` (新增) - 查询冻结信息（智能接口，自动识别受托人）
4. `dpos_getAccountBalance` (新增) - 查询余额（含冻结）

**建议实现（2个）**:
5. `dpos_getFreezeHistory` (新增) - 冻结历史
6. `dpos_canWithdrawDelegate` (新增) - 检查是否可以退出

**总计**: 6个接口（4个必须 + 2个建议）

**注意**: `dpos_getFreezeParameters` 已删除，冻结相关参数（`min_freeze_period` 和 `unfreeze_lock_period`）已合并到 `dpos_getVotableCurrentParameters` 接口中

### 已删除的接口（不需要或重复）
- ❌ `dpos_requestUnfreeze` - 不需要两阶段
- ❌ `dpos_withdrawUnfreeze` - 不需要两阶段
- ❌ `dpos_getFrozenBalance` - 与 `dpos_getAccountBalance` 重复
- ❌ `dpos_getDelegateFreezeInfo` - 与 `dpos_getFreezeInfo` 重复，已合并为智能接口
- ❌ `dpos_calculateUnfreezeTime` - 不需要请求解冻，此接口多余
- ❌ `dpos_setFreezeParameter` - 参数通过治理提案修改，不需要单独接口
- ❌ `dpos_getFreezeParameters` - 已合并到 `dpos_getVotableCurrentParameters`，冻结相关参数（`min_freeze_period` 和 `unfreeze_lock_period`）已加入可表决参数列表
- ❌ `dpos_getBatchFreezeInfo` - 已合并到 `dpos_getFreezeInfo`，支持通过 `addresses` 参数进行批量查询
- ❌ `dpos_getFreezeStatistics` - 已删除，可通过批量调用 `dpos_getFreezeInfo` 自行聚合统计信息
- ❌ `dpos_getPendingUnfreezes` - 已删除，可通过批量调用 `dpos_getFreezeInfo` 查询已知地址并自行筛选
- ❌ `dpos_subscribeFreezeEvents` - 已删除，WebSocket事件订阅功能可通过其他方式实现（如区块浏览器）

### 设计原则
1. **简化流程**: 参考 Tron，退出注册时直接解冻，无需两阶段
2. **避免重复**: 删除功能重复的接口
3. **固定保证金**: 删除最小/最大冻结金额限制（保证金是固定值）
4. **向后兼容**: 保持现有接口的兼容性
5. **配置化**: 解冻期参数从 YAML 配置文件读取，便于调整

### YAML 配置示例

在服务器配置文件中添加以下配置：

```yaml
# config.yaml
dpos:
  # 冻结相关配置
  freeze:
    min_freeze_period: 604800      # 最小冻结期（秒，7天）
    unfreeze_lock_period: 1209600  # 解冻锁定期（秒，14天）
```

或者在现有的 DPoS 配置段中添加：

```yaml
dpos:
  # 现有配置...
  min_freeze_period: 604800      # 最小冻结期（秒，7天）
  unfreeze_lock_period: 1209600  # 解冻锁定期（秒，14天）
```

