# DPoS RPC 接口（统一版）

本文档对 DPoS RPC 接口采用统一结构编写，与代码 `jsonrpc/dpos_endpoint.go` 中 *DPOS 导出方法一致，便于查阅与维护。

---

## 文档约定

### 统一结构（每个接口均包含）

| 小节         | 说明 |
|--------------|------|
| **功能说明** | 一句话描述接口用途 |
| **RPC 方法** | 方法名，如 `dpos_xxx` |
| **请求参数** | 参数格式 + 参数表（参数名、类型、必填、说明） |
| **返回结果** | 示例 JSON（仅 `result` 内容）+ 返回字段说明表 |
| **调用示例** | cURL 示例；多种入参时用注释区分 |

可选小节（按需）：**错误响应**、**注意事项**、**与 xxx 的区别**。

### 参数约定（统一写法）

- **格式说明**：每个接口的「请求参数」先写**支持的参数形式**，再写**参数表**。参数表列：参数名、类型、必填、说明。
- **用语统一**：
  - **支持数组格式**：`[ 参数1, 参数2, ... ]`，元素**顺序**与参数表从上到下一致。
  - **支持对象格式**：`{ "参数名": 值, ... }`，key 与参数表中的**参数名**一致。
  - **支持字符串格式**：单参数时可传 `"值"`（或 `["值"]`，依实现而定）。
  - **支持数字格式**：单参数为数字时可传 `10` 或 `[10]`。
  - **无参数**：写「无」或「传 `[]` / 空数组」。
- **对象可包在数组**：部分接口 `params` 可为对象或「仅含一个对象的数组」`[ 对象 ]`，文档会写明「对象格式（可包在数组中）」。
- **推荐**：调用时**对象格式**更易读、易扩展；数组格式紧凑，顺序需与文档一致。

**对象格式支持说明（与代码一致）**  
- **仅支持对象格式、不支持数组**：`dpos_registerDelegate`（必须传 `{ registrant, name, website, description, privateKey, ... }`）。
- **不支持对象格式、仅支持数组或字符串**（实现中未解析 `map[string]interface{}`）：
  - `dpos_getVoterRewardByValidator`：`[voterAddress, validatorAddress, fromEpoch, toEpoch]`；
  - `dpos_getStakingInfo`：`[]` 或 `[blockNumber]`（按位置）；
  - `dpos_getEpochInfoByNumber`：`[epochNumber]`（按位置）；
  - `dpos_getValidatorBlockStats`：`[validatorAddress, epochNumber]`（按位置）。
- **无参数接口**（如 `dpos_getCurrentEpochInfo`、`dpos_getLatestEpochInfo`、`dpos_getDelegateRegistrations`、`dpos_getVotableCurrentParameters`、`dpos_getConsensusSwitchHeight` 等）：传 `[]` 或不传。
- 其余接口在实现中均同时支持**数组**与**对象**两种参数形式。

### 术语

- **验证者 / 候选人 / delegate**：均指可被投票的节点地址，本文档统一用「验证者」。
- **投票者 / voter**：发起投票的地址。
- **提案 ID**：统一写作 `proposalId`（camelCase）。
- **金额**：Wei 为整数字符串；Ether 为保留 6 位小数的字符串（除非另有说明）。

### 返回格式说明

- 示例中的返回内容为 JSON-RPC 的 `result` 字段；实际响应仍为 `{"jsonrpc":"2.0","id":1,"result":{...}}`。
- **成功**：顶层均包含 `"success": true`，业务数据在 `data`、`validator`、`summary`、`records` 等字段或与 `success` 同级。
- **失败**：统一为 `"success": false` 且 `"error": "错误信息"`。
- 涉及私钥的接口仅作说明，示例中的私钥为占位，请勿用于生产。
- **返回字段说明**：各接口「返回结果」下的字段表中，「说明（中文）」列均为该字段的**中文解释**，便于前端与调用方理解。

**返回格式核对**  
本文档各接口的返回结构与字段已与 `jsonrpc/dpos_endpoint.go` 及 `consensus/dpos` 相关类型核对；若实现变更请以代码为准并同步更新此处。

---

## 1 治理相关接口

### 1. dpos_createParameterProposal

**功能说明**  
创建参数表决提案。

**RPC 方法**  
`dpos_createParameterProposal`

**请求参数**

- 支持数组格式：`[parameter, newValue, description, proposer, proposerPrivateKey]`
- 支持对象格式：`{ parameter, newValue, description, proposer, proposerPrivateKey }`

| 参数名             | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| parameter          | string | 是   | 要修改的参数名称（必须是可投票参数） |
| newValue           | any    | 是   | 参数的新值 |
| description        | string | 否   | 提案描述（可选） |
| proposer           | string | 是   | 提案者地址（必须是验证者） |
| proposerPrivateKey | string | 是   | 提案者私钥（64 位十六进制，不含 0x 前缀） |

**返回结果**

```json
{
  "success": true,
  "txHash": "0x...",
  "proposalId": "proposal_0x...",
  "parameter": "参数名",
  "newValue": "新值",
  "proposer": "0x...",
  "currentBlockNumber": 12345,
  "message": "Parameter proposal transaction created and broadcasted successfully",
  "note": "Proposal will be created when transaction is included in a block"
}
```

**返回字段说明**

| 字段名             | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success            | boolean | 布尔值，表示本次 RPC 调用是否成功 |
| txHash             | string | 创建提案的交易哈希（0x 开头的十六进制字符串） |
| proposalId         | string | 提案唯一标识，由交易哈希派生，格式如 proposal_0x... |
| parameter          | string | 要修改的链上参数名称 |
| newValue           | any    | 参数的新值（类型依参数而定） |
| proposer           | string | 提案者账户地址（须为验证者） |
| currentBlockNumber | number | 当前链上区块高度 |
| message            | string | 成功时的提示文案 |
| note               | string | 补充说明（如：提案将在交易被打包后正式创建） |

**调用示例**

```bash
# 数组格式
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_createParameterProposal","params":["dpos_missed_blocks_percentage",1500,"提高错过区块阈值到15%","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","a1b2c3d4e5f6789012345678901234567890123456789012345678901234567890"],"id":1}'

# 对象格式
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_createParameterProposal","params":{"parameter":"dpos_missed_blocks_percentage","newValue":1500,"description":"提高错过区块阈值到15%","proposer":"0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","proposerPrivateKey":"a1b2c3d4e5f6789012345678901234567890123456789012345678901234567890"},"id":1}'
```

---

### 2. dpos_createRecoveryProposal

**功能说明**  
创建验证者恢复提案。

**RPC 方法**  
`dpos_createRecoveryProposal`

**请求参数**

- 支持数组格式：`[validatorAddress, recoveryReason, description, proposer, proposerPrivateKey]`
- 支持对象格式：`{ validatorAddress, recoveryReason, description, proposer, proposerPrivateKey }`

| 参数名             | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validatorAddress   | string | 是   | 要恢复的验证者地址 |
| recoveryReason     | string | 是   | 恢复原因 |
| description        | string | 否   | 提案描述（可选） |
| proposer           | string | 是   | 提案者地址（必须是验证者） |
| proposerPrivateKey | string | 是   | 提案者私钥（64 位十六进制，不含 0x） |

**返回结果**  
与 dpos_createParameterProposal 结构类似，但 proposalType 为 "recovery"。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_createRecoveryProposal","params":["0x907b5aa2540da7b8c8c8c8c8c8c8c8c8c8c8c8c8","验证者因网络问题暂时离线，现已恢复","恢复验证者提案","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","a1b2c3d4e5f6789012345678901234567890123456789012345678901234567890"],"id":1}'
```

---

### 3. dpos_getParameterProposal

**功能说明**  
获取提案详细信息（参数提案或恢复提案）。

**RPC 方法**  
`dpos_getParameterProposal`

**请求参数**

- 支持数组格式：`[proposalId]`
- 支持对象格式：`{ proposalId: "proposal_0x..." }`
- 支持字符串格式：`"proposal_0x..."`

| 参数名     | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| proposalId | string | 是   | 提案 ID |

**返回结果**

```json
{
  "success": true,
  "proposal": {
    "proposalId": "proposal_0x...",
    "proposalType": "parameter",
    "parameter": "参数名",
    "newValue": "新值",
    "oldValue": "旧值",
    "status": "Active",
    "voteStats": {},
    "timeInfo": {},
    "voterDetails": {}
  }
}
```

**返回字段说明**

| 字段名                          | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success                         | boolean | 布尔值，表示本次请求是否成功 |
| proposal                        | object | 提案完整信息对象 |
| proposal.proposalId             | string | 提案唯一标识（如 proposal_0x...） |
| proposal.proposalType           | string | 提案类型：parameter 参数提案 / recovery 恢复提案 |
| proposal.parameter / newValue / oldValue | - | 参数名、新值、旧值（参数提案时存在） |
| proposal.validator / recoveryReason | - | 待恢复的验证者地址、恢复原因（恢复提案时存在） |
| proposal.status                 | string | 提案状态：Active 进行中 / Passed 已通过 / Rejected 已否决 / Executed 已执行 |
| proposal.voteStats              | object | 投票统计：总票数、支持/反对票、是否通过、门槛等 |
| proposal.timeInfo               | object | 与区块高度、时间相关的信息（表决期、剩余块数等） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getParameterProposal","params":["proposal_0x2f773b9c6421f65"],"id":1}'
```

---

### 4. dpos_voteOnParameterProposal

**功能说明**  
对提案进行投票（支持/反对）。

**RPC 方法**  
`dpos_voteOnParameterProposal`

**请求参数**

- 支持数组格式：`[proposalId, voter, support, privateKey]`
- 支持对象格式：`{ proposalId, voter, support, privateKey }`

| 参数名     | 类型    | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| proposalId | string  | 是   | 提案 ID |
| voter      | string  | 是   | 投票者地址 |
| support    | boolean | 是   | 是否支持（true=支持，false=反对） |
| privateKey | string  | 是   | 投票者私钥（64 位十六进制，不含 0x） |

**返回结果**

```json
{
  "success": true,
  "txHash": "0x...",
  "proposalId": "proposal_0x...",
  "voter": "0x...",
  "support": true,
  "currentBlockNumber": 12345,
  "message": "Vote transaction created and broadcasted successfully",
  "note": "Vote will be recorded when transaction is included in a block"
}
```

**返回字段说明**

| 字段名             | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success            | boolean | 布尔值，表示本次 RPC 调用是否成功 |
| txHash             | string | 本次投票交易的哈希（0x 开头） |
| proposalId         | string | 被投票的提案 ID |
| voter              | string | 投票人账户地址 |
| support            | boolean | 是否支持该提案：true 支持，false 反对 |
| currentBlockNumber | number | 当前链上区块高度 |
| message            | string | 成功时的提示文案 |
| note               | string | 补充说明（如：投票将在交易被打包后计入） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_voteOnParameterProposal","params":["proposal_0x2f773b9c6421f65","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127",true,"a1b2c3d4e5f6..."],"id":1}'
```

---

### 5. dpos_getActiveProposals

**功能说明**  
获取所有活跃提案列表。

**RPC 方法**  
`dpos_getActiveProposals`

**请求参数**  
无。传 `[]` 或空数组。

**返回结果**

```json
{
  "success": true,
  "proposals": [
    {
      "proposalId": "proposal_0x...",
      "parameter": "参数名",
      "oldValue": "旧值",
      "newValue": "新值",
      "proposer": "0x...",
      "startBlock": 1000,
      "endBlock": 2000,
      "status": "Active",
      "threshold": 50,
      "description": "提案描述",
      "createdAt": "2024-01-01 12:00:00",
      "createdAtTs": 1704067200,
      "votes": 5,
      "isProposalExpired": false
    }
  ],
  "count": 1
}
```

**返回字段说明**

| 字段名    | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success   | boolean | 布尔值，表示本次请求是否成功 |
| proposals | array  | 当前活跃提案列表，按创建时间倒序 |
| count     | number | 返回的提案条数 |

**proposals 每项**：proposalId、parameter/oldValue/newValue、proposer、startBlock/endBlock、status、threshold、description、createdAt/createdAtTs、votes、isProposalExpired（提案执行有效期是否已过期）。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getActiveProposals","params":[],"id":1}'
```

---

### 6. dpos_getVotableCurrentParameters

**功能说明**  
获取所有可投票参数的当前值及元数据。

**RPC 方法**  
`dpos_getVotableCurrentParameters`

**请求参数**  
无。

**返回结果**

```json
{
  "success": true,
  "parameters": {
    "dpos_missed_blocks_percentage": {
      "name": "dpos_missed_blocks_percentage",
      "type": "uint64",
      "minValue": 0,
      "maxValue": 10000,
      "description": "验证者错过区块的百分比阈值（基点，10000=100%）",
      "category": "consensus",
      "currentValue": 1200
    }
  },
  "count": 1
}
```

**返回字段说明**

| 字段名                        | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success                       | boolean | 布尔值，表示本次请求是否成功 |
| parameters                    | object | 以参数名为 key、参数信息为 value 的映射 |
| count                         | number | 可投票参数的数量 |
| parameters[].name              | string | 参数名（如 dpos_missed_blocks_percentage） |
| parameters[].type             | string | 参数类型（如 uint64） |
| parameters[].minValue / maxValue | number | 允许的最小/最大值 |
| parameters[].currentValue      | number | 当前链上生效的值 |
| parameters[].description      | string | 参数含义的中文或英文描述 |
| parameters[].category        | string | 参数分类（如 consensus） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVotableCurrentParameters","params":[],"id":1}'
```

---

### 7. dpos_executeParameterUpdate

**功能说明**  
执行已通过的参数更新提案。

**RPC 方法**  
`dpos_executeParameterUpdate`

**请求参数**

- 支持数组格式：`[proposalId, executor, executorPrivateKey]`
- 支持对象格式：`{ proposalId, executor, executorPrivateKey }`

| 参数名            | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| proposalId        | string | 是   | 提案 ID |
| executor          | string | 是   | 执行者地址 |
| executorPrivateKey | string | 是   | 执行者私钥（64 位十六进制，不含 0x） |

**返回结果**

```json
{
  "success": true,
  "txHash": "0x...",
  "proposalId": "proposal_0x...",
  "executor": "0x...",
  "currentBlockNumber": 12345,
  "message": "Execute proposal transaction created and broadcasted successfully",
  "note": "Proposal will be executed when transaction is included in a block"
}
```

**返回字段说明**

| 字段名             | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success            | boolean | 布尔值，表示本次 RPC 调用是否成功 |
| txHash             | string | 执行提案交易的哈希（0x 开头） |
| proposalId         | string | 被执行的提案 ID |
| executor           | string | 执行人账户地址（任意地址均可执行已通过的提案） |
| currentBlockNumber | number | 当前链上区块高度 |
| message            | string | 成功时的提示文案 |
| note               | string | 补充说明（如：执行将在交易被打包后生效） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_executeParameterUpdate","params":["proposal_0x2f773b9c6421f65","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","a1b2c3d4e5f6..."],"id":1}'
```

---

## 2 投票相关接口

### 8. dpos_vote

**功能说明**  
投票给验证者（委托投票）或撤销投票。

**RPC 方法**  
`dpos_vote`

**请求参数**

- 支持数组：单参数 `[address]`、三参数 `[voter, candidate, amount]`、四参数 `[voter, candidate, amount, privateKey]`
- 支持对象格式：`{ voter, candidate, amount, privateKey }`

| 参数名     | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| voter      | string | 是   | 投票者地址 |
| candidate  | string | 是   | 验证者地址（候选人） |
| amount     | string | 是   | 投票金额（Wei 字符串）；-1 表示撤销 |
| privateKey | string | 否   | 投票者私钥；若提供则创建并广播交易 |

**返回结果**

```json
{
  "success": true,
  "message": "Vote operation completed successfully",
  "txHash": "0x...",
  "blockNumber": 12345
}
```

**返回字段说明**

| 字段名      | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success     | boolean | 操作是否成功 |
| message     | string | 操作消息 |
| txHash      | string | 交易哈希（提供私钥时） |
| blockNumber | number | 打包区块号 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_vote","params":["0x8f27...","0x907b...","1000000000000000000","a1b2c3d4e5f6..."],"id":1}'
```

---

### 9. dpos_getVoteRecords

**功能说明**  
查询投票/质押记录的明细列表，支持按投票者、被投票验证者过滤，按活跃/生效状态过滤，以及分页和排序。

**RPC 方法**  
`dpos_getVoteRecords`

**请求参数**

- 对象格式（可包在数组中）：`[ { voter?, delegate?, onlyActive?, includePending?, limit?, offset?, order? } ]` 或直接传对象。

| 参数名        | 类型    | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| voter         | string  | 否*  | 投票者地址（与 delegate 至少填一个） |
| delegate      | string  | 否*  | 被投票的验证者地址 |
| onlyActive    | boolean | 否   | 是否只返回活跃记录（IsActive=true） |
| includePending | boolean | 否   | 是否包含待生效记录（Applied=false） |
| limit         | number  | 否   | 返回条数上限 |
| offset        | number  | 否   | 分页偏移量 |
| order         | string  | 否   | 排序：asc / desc，按 startTime |

**返回结果**

```json
{
  "success": true,
  "total": 50,
  "records": [
    {
      "voter": "0x...",
      "delegate": "0x...",
      "amountWei": "1000000000000000000",
      "amountEther": "1.000000",
      "originalAmountWei": "1000000000000000000",
      "originalAmountEther": "1.000000",
      "startTime": 1769485285,
      "endTime": 1769485285,
      "isLocked": false,
      "isActive": true,
      "applied": true,
      "effectiveEpoch": 3,
      "pendingUnvote": false,
      "unvoteEffectiveEpoch": 0
    }
  ]
}
```

**返回字段说明**

| 字段名    | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success   | boolean | 布尔值，表示本次请求是否成功 |
| total     | number | 符合筛选条件的投票记录总数（分页前的总条数） |
| records   | array  | 当前页的投票记录列表（已按分页与排序截取） |

**records 每项**

**返回字段说明**

| 字段名                        | 类型    | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| voter                         | string  | 投票人账户地址 |
| delegate                      | string  | 被投票的验证者（候选人）地址 |
| amountWei / amountEther       | string  | 当前生效的投票金额（Wei/Ether）；已撤销时为 0 |
| originalAmountWei / originalAmountEther | string | 撤销前的原投票金额；仅当已撤销（amount=0）时可能有值，老数据可能无此字段 |
| startTime / endTime           | number  | 记录创建/结束的 Unix 时间戳（秒） |
| isLocked                      | boolean | 该笔投票是否处于锁定状态 |
| isActive                      | boolean | 是否为当前仍有效的记录（true 表示仍计入统计） |
| applied                       | boolean | 是否已在某个 epoch 边界生效（true 表示已写入共识状态） |
| effectiveEpoch                | number  | 该笔投票生效的 epoch 编号 |
| pendingUnvote                 | boolean | 是否处于「待撤销」状态（将在本 epoch 边界执行撤销） |
| unvoteEffectiveEpoch          | number  | 撤销生效的 epoch 编号（当 pendingUnvote 为 true 时有意义） |

**修改说明**  
- 新增 originalAmountWei / originalAmountEther：已撤销的投票可展示「当初投了多少」；老数据无此字段时前端可用 amount 为 0 表示已撤销。  
- 新增 pendingUnvote、unvoteEffectiveEpoch：支持撤销在 epoch 边界生效，前端可展示「待撤销，本 epoch 边界生效」。

**调用示例**

```bash
curl -X POST https://testnet-rpc.vcity.app \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVoteRecords","params":[{"voter":"0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","onlyActive":true,"includePending":false,"limit":100,"offset":0,"order":"desc"}],"id":1}'
```

---

## 3 质押相关接口

### 10. dpos_getStakingInfo

**功能说明**  
获取质押信息（验证者列表及各自质押状态）。

**RPC 方法**  
`dpos_getStakingInfo`

**请求参数**

- 无参数：`[]`
- 可选区块：数组 `[blockNumber]` 或对象 `{ blockNumber }`

| 参数名      | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| blockNumber | number | 否   | 查询的区块号 |

**返回结果**

```json
{
  "success": true,
  "data": [
    {
      "staker": "0x5E20...",
      "amount": "1000000000000000000000",
      "amountEther": "1000.000000",
      "effectiveAmountWei": "800000000000000000000",
      "effectiveAmountEther": "800.000000",
      "pendingAmountWei": "200000000000000000000",
      "pendingAmountEther": "200.000000",
      "isActive": true,
      "faultFlag": { "isFaulty": false, "missedBlocks": 0, "reason": "" },
      "rewards": "1234567890000000000000"
    }
  ],
  "currentEpoch": 53
}
```

**返回字段说明**

| 字段名       | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success      | boolean | 布尔值，表示本次请求是否成功 |
| data         | array  | 验证者列表，每项为该验证者的质押/得票汇总信息 |
| currentEpoch | number | 当前链上 epoch 编号，用于判断某笔投票是否已生效 |

**data 每项**

**返回字段说明**

| 字段名                              | 类型    | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| staker                              | string  | 验证者（候选人）账户地址 |
| amount / amountEther               | string  | 该验证者获得的总得票（Wei 整数字符串 / Ether 6 位小数） |
| effectiveAmountWei / effectiveAmountEther | string | 已生效的得票金额（已计入共识权重的部分） |
| pendingAmountWei / pendingAmountEther     | string | 未生效的得票金额（将在下一个 epoch 边界生效） |
| isActive                            | boolean | 该验证者是否为当前活跃验证者 |
| faultFlag                           | object  | 故障相关信息：isFaulty 是否故障、missedBlocks 漏块数、reason 原因说明 |
| rewards                             | string  | 该验证者累计获得的奖励 Wei（可选，无则无此字段） |

**修改说明**  
- 每个验证者拆分为已生效与未生效：effectiveAmountWei/Ether、pendingAmountWei/Ether，便于前端区分「当前生效票数」与「待生效票数」。  
- 顶层增加 currentEpoch，便于对照生效 epoch。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getStakingInfo","params":[],"id":1}'
```

---

## 4 验证者相关接口

### 11. dpos_getVotingPower

**功能说明**  
获取指定验证者的投票权重（总得票）。

**RPC 方法**  
`dpos_getVotingPower`

**请求参数**

- 数组：`[delegate]` 或 `[delegate, blockNumber]`
- 对象：`{ delegate, blockNumber? }`

| 参数名      | 类型          | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| delegate    | string        | 是   | 验证者地址 |
| blockNumber | string/number | 否   | 区块号（"latest" 或数字） |

**返回结果**

```json
{
  "success": true,
  "delegate": "0x7744E828e4Bd34aAfBB409C3b574B3647198EE65",
  "votingPower": "101000000000000000000000",
  "isActive": true
}
```

**返回字段说明**

| 字段名      | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success     | boolean | 请求是否成功 |
| delegate    | string | 验证者地址 |
| votingPower | string | 投票权重（Wei），即总得票 |
| isActive    | boolean | 是否为当前活跃验证者 |

**错误响应**  
验证者未找到时返回 `{ "success": false, "error": "delegate not found: 0x..." }`。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVotingPower","params":["0x907b5aa2540da7b8c8c8c8c8c8c8c8c8c8c8c8c8"],"id":1}'
```

---

### 12. dpos_getValidatorVotingDetails

**功能说明**  
查询指定验证者（或任意地址）的投票/质押汇总：该地址收到的投票（别人投给我）、该地址投出的投票（我投给别人）、投票权重、活跃状态等。

**RPC 方法**  
`dpos_getValidatorVotingDetails`

**请求参数**

- 数组：`[validator]`
- 字符串：`"0x..."`
- 对象：`{ validator: "0x..." }`

| 参数名    | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validator | string | 是   | 验证者或任意地址 |

**返回结果**

```json
{
  "success": true,
  "validator": {
    "address": "0xf125B5c2c268558493f14f74db4243323D363501",
    "votingPower": "2200000000000000000000",
    "isActive": true,
    "totalStakedToMe": "2200000000000000000000",
    "totalStakedToMeEther": "2200.000000",
    "stakeCount": 2,
    "stakes": [],
    "totalVotedByMe": "200000000000000000000",
    "totalVotedByMeEther": "200000.000000",
    "myVoteCount": 1,
    "myVotes": [],
    "hasInboundVotes": true,
    "currentEpoch": 53
  }
}
```

**返回字段说明**

| 字段名                                    | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success                                   | boolean | 布尔值，表示本次请求是否成功 |
| validator                                 | object | 所查地址的投票/质押汇总详情 |
| validator.address                         | string | 被查询的账户地址（验证者或任意地址） |
| validator.votingPower / totalStakedToMe   | string | 别人投给该地址的总票数（Wei），包含已生效与未生效 |
| validator.isActive                        | boolean | 该地址是否为当前活跃验证者 |
| validator.stakeCount / stakes             | number/array | 投给该地址的投票者数量，及按投票者汇总的列表（每项含已生效/未生效金额） |
| validator.totalVotedByMe / myVoteCount / myVotes | - | 该地址投给别人的总金额，及按被投验证者汇总的列表（每项含已生效/未生效金额） |
| validator.hasInboundVotes                 | boolean | 是否有人给该地址投过票 |
| validator.currentEpoch                    | number | 当前链上 epoch，用于区分某笔投票是否已生效 |

**stakes / myVotes 每项** 除 amount 外，还包含：effectiveAmountWei/Ether（已生效金额）、pendingAmountWei/Ether（未生效金额）；若存在待撤销则含 pendingUnvote、unvoteEffectiveEpoch。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorVotingDetails","params":["0x907b5aa2540da7b8c8c8c8c8c8c8c8c8c8c8c8c8"],"id":1}'
```

---

### 13. dpos_registerDelegate

**功能说明**  
注册受托人（验证者）信息。

**RPC 方法**  
`dpos_registerDelegate`

**请求参数**  
仅支持对象格式：`{ registrant, name, website?, description?, privateKey, chainID? }`

| 参数名     | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| registrant | string | 是   | 注册者地址 |
| name       | string | 是   | 受托人名称 |
| website    | string | 否   | 网站地址 |
| description | string | 否   | 描述 |
| privateKey | string | 是   | 注册者私钥（64 位十六进制） |
| chainID    | number | 否   | 链 ID |

**返回结果**

```json
{
  "success": true,
  "message": "Delegate registered successfully",
  "txHash": "0x...",
  "registrant": "0x...",
  "name": "验证者名称"
}
```

**返回字段说明**

| 字段名     | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success    | boolean | 是否成功 |
| message    | string | 成功时的提示文案 |
| txHash     | string | 交易哈希 |
| registrant | string | 注册者地址 |
| name       | string | 验证者名称 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_registerDelegate","params":{"registrant":"0x8f27...","name":"我的验证者节点","website":"https://...","description":"...","privateKey":"a1b2c3d4e5f6..."},"id":1}'
```

---

### 14. dpos_getDelegateRegistrations

**功能说明**  
获取所有受托人注册信息。

**RPC 方法**  
`dpos_getDelegateRegistrations`

**请求参数**  
无。传 `[]` 或空数组。

**返回结果**

```json
{
  "delegates": [
    {
      "address": "0x...",
      "name": "验证者名称",
      "website": "https://...",
      "description": "描述",
      "registeredAt": 1704067200
    }
  ],
  "count": 1
}
```

**返回字段说明**

| 字段名   | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| delegates | array | 受托人列表，每项含 address、name、website、description、registeredAt |
| count    | number | 受托人数量 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getDelegateRegistrations","params":[],"id":1}'
```

---

### 15. dpos_withdrawDelegate

**功能说明**  
撤销受托人注册。

**RPC 方法**  
`dpos_withdrawDelegate`

**请求参数**  
对象格式：`{ registrant, privateKey }`

| 参数名     | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| registrant | string | 是   | 注册者地址 |
| privateKey | string | 是   | 注册者私钥 |

**返回结果**

```json
{
  "success": true,
  "message": "Delegate withdrawal successful",
  "txHash": "0x...",
  "registrant": "0x..."
}
```

**返回字段说明**

| 字段名     | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success    | boolean | 是否成功 |
| message    | string | 成功时的提示文案 |
| txHash     | string | 交易哈希 |
| registrant | string | 注册者地址 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_withdrawDelegate","params":{"registrant":"0x8f27...","privateKey":"a1b2c3d4e5f6..."},"id":1}'
```

---

### 16. dpos_canWithdrawDelegate

**功能说明**  
检查指定地址是否可以撤销受托人注册。

**RPC 方法**  
`dpos_canWithdrawDelegate`

**请求参数**

- 数组：`[address]`
- 字符串：`"0x..."`
- 对象：`{ address: "0x..." }`

| 参数名  | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| address | string | 是   | 验证者地址 |

**返回结果**

```json
{
  "canWithdraw": true,
  "reason": "可以撤销",
  "blockNumber": 12345
}
```

**返回字段说明**

| 字段名       | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| canWithdraw  | boolean | 是否可以撤销 |
| reason       | string | 说明文案 |
| blockNumber  | number | 当前区块高度 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_canWithdrawDelegate","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 17. dpos_getFreezeInfo

**功能说明**  
获取验证者冻结信息。

**RPC 方法**  
`dpos_getFreezeInfo`

**请求参数**

- 数组：`[address]`
- 字符串：`"0x..."`
- 对象：`{ address: "0x..." }`

| 参数名  | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| address | string | 是   | 验证者地址 |

**返回结果**

```json
{
  "address": "0x...",
  "isFrozen": false,
  "freezeStartBlock": 0,
  "freezeEndBlock": 0,
  "freezeReason": "",
  "missedBlocks": 0,
  "missedBlocksPercentage": 0
}
```

**返回字段说明**

| 字段名                 | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| address                | string | 验证者地址 |
| isFrozen               | boolean | 是否被冻结 |
| freezeStartBlock / freezeEndBlock | number | 冻结起止区块 |
| freezeReason           | string | 冻结原因 |
| missedBlocks / missedBlocksPercentage | number | 错过区块数及占比 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getFreezeInfo","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 18. dpos_getValidatorCommission

**功能说明**  
获取验证者佣金率。

**RPC 方法**  
`dpos_getValidatorCommission`

**请求参数**

- 数组：`[validatorAddress]`
- 字符串：`"0x..."`
- 对象：`{ validator: "0x..." }`

| 参数名           | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validatorAddress | string | 是   | 验证者地址 |

**返回结果**

```json
{
  "validator": "0x...",
  "commissionRate": 10,
  "commissionRatePercent": "10.00%",
  "lastUpdatedBlock": 12345
}
```

**返回字段说明**

| 字段名                | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| validator             | string | 验证者地址 |
| commissionRate        | number | 佣金率（基点，100=1%） |
| commissionRatePercent | string | 佣金率百分比显示 |
| lastUpdatedBlock      | number | 最后更新区块 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorCommission","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 19. dpos_updateCommission

**功能说明**  
更新验证者佣金率。

**RPC 方法**  
`dpos_updateCommission`

**请求参数**  
对象格式：`{ validator, newCommissionRate, privateKey }`

| 参数名             | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validator          | string | 是   | 验证者地址 |
| newCommissionRate  | number | 是   | 新佣金率（基点，100=1%） |
| privateKey         | string | 是   | 验证者私钥 |

**返回结果**

```json
{
  "success": true,
  "validator": "0x...",
  "oldCommissionRate": 10,
  "newCommissionRate": 15,
  "txHash": "0x...",
  "message": "Commission rate updated successfully"
}
```

**返回字段说明**

| 字段名             | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success            | boolean | 是否成功 |
| validator          | string | 验证者地址 |
| oldCommissionRate | number | 原佣金率 |
| newCommissionRate | number | 新佣金率 |
| txHash             | string | 交易哈希 |
| message            | string | 成功时的提示文案 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_updateCommission","params":{"validator":"0x8f27...","newCommissionRate":15,"privateKey":"a1b2c3d4e5f6..."},"id":1}'
```

---

### 20. dpos_getVoterSlashingHistory

**功能说明**  
获取投票者惩罚（slash）历史。

**RPC 方法**  
`dpos_getVoterSlashingHistory`

**请求参数**

- 数组：`[voterAddress]` 或 `[voterAddress, fromEpoch, toEpoch]`
- 对象：`{ voter, fromEpoch?, toEpoch? }`

| 参数名       | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| voterAddress | string | 是   | 投票者地址 |
| fromEpoch    | number | 否   | 起始 Epoch |
| toEpoch      | number | 否   | 结束 Epoch |

**返回结果**

```json
{
  "voter": "0x...",
  "slashingHistory": [
    {
      "epoch": 10,
      "amount": "1000000000000000000",
      "reason": "验证者被惩罚",
      "blockNumber": 12345
    }
  ],
  "totalSlashing": "1000000000000000000"
}
```

**返回字段说明**

| 字段名           | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| voter            | string | 投票者地址 |
| slashingHistory  | array  | 惩罚记录列表，每项含 epoch、amount、reason、blockNumber |
| totalSlashing    | string | 惩罚总额（Wei 字符串） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVoterSlashingHistory","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

## 5 Epoch 相关接口

### 21. dpos_getCurrentEpochInfo

**功能说明**  
获取当前 Epoch 信息。

**RPC 方法**  
`dpos_getCurrentEpochInfo`

**请求参数**  
无。传 `[]` 或空数组。

**返回结果**

```json
{
  "epochNumber": 10,
  "startBlock": 1000,
  "endBlock": 2000,
  "validators": [
    { "address": "0x...", "votingPower": "1000000000000000000" }
  ],
  "totalBlocks": 1000,
  "currentBlock": 1500
}
```

**返回字段说明**

| 字段名       | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| epochNumber  | number | Epoch 编号 |
| startBlock / endBlock | number | 起始/结束区块 |
| validators   | array  | 验证者列表及投票权重 |
| totalBlocks  | number | 该 Epoch 总区块数 |
| currentBlock | number | 当前区块高度 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getCurrentEpochInfo","params":[],"id":1}'
```

---

### 22. dpos_getEpochInfoByNumber

**功能说明**  
获取指定 Epoch 信息。

**RPC 方法**  
`dpos_getEpochInfoByNumber`

**请求参数**

- 数字：`10`
- 数组：`[10]`

| 参数名      | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| epochNumber | number | 是   | Epoch 编号 |

**返回结果**  
与 dpos_getCurrentEpochInfo 结构相同。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochInfoByNumber","params":[10],"id":1}'
```

---

### 23. dpos_getLatestEpochInfo

**功能说明**  
获取最新 Epoch 信息（与 dpos_getCurrentEpochInfo 相同）。

**RPC 方法**  
`dpos_getLatestEpochInfo`

**请求参数**  
无。传 `[]` 或空数组。

**返回结果**  
与 dpos_getCurrentEpochInfo 相同。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getLatestEpochInfo","params":[],"id":1}'
```

---

## 6 奖励相关接口

### 24. dpos_getEpochRewardDetails

**功能说明**  
获取指定 Epoch 的奖励详情（内部使用）。

**RPC 方法**  
`dpos_getEpochRewardDetails`

**请求参数**

- 数组：`[epochNumber]`

| 参数名      | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| epochNumber | number | 是   | Epoch 编号 |

**返回结果**

```json
[
  {
    "epoch": 10,
    "validator": "0x...",
    "voter": "0x...",
    "reward": "1000000000000000000",
    "type": "validator"
  }
]
```

**返回字段说明**  
数组每项含 epoch、validator、voter、reward、type（validator/voter 等）。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochRewardDetails","params":[10],"id":1}'
```

---

### 25. dpos_getRewardHistory

**功能说明**  
获取指定地址在指定 Epoch 区间内的奖励汇总与明细（通用接口）。

**RPC 方法**  
`dpos_getRewardHistory`

**请求参数**

- 数组：`[address, fromEpoch, toEpoch, includeRecords?]`
- 对象：`{ address, fromEpoch, toEpoch, type?, includeRecords? }`

| 参数名         | 类型    | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| address        | string  | 是   | 地址（验证者或投票者） |
| fromEpoch      | number  | 是   | 起始 Epoch |
| toEpoch        | number  | 是   | 结束 Epoch |
| type           | string  | 否   | "validator" 或 "voter"，不传则查全部 |
| includeRecords | boolean | 否   | 是否返回明细数组；false 时仅返回总额与统计 |

**返回结果**

```json
{
  "success": true,
  "summary": {
    "address": "0x...",
    "fromEpoch": 0,
    "toEpoch": 100,
    "totalRewardWei": "1234567890000000000000",
    "validatorRewardWei": "1000000000000000000000",
    "voterRewardWei": "234567890000000000000",
    "totalRewardEther": "1234.567890",
    "validatorRewardEther": "1000.000000",
    "voterRewardEther": "234.567890",
    "recordCount": 150,
    "validatorRecordCount": 100,
    "voterRecordCount": 45,
    "otherRecordCount": 5,
    "validatorRecords": [],
    "voterRecords": [],
    "otherRewardRecords": []
  }
}
```

**返回字段说明**  
includeRecords=false 时明细数组为空；true 时为具体记录列表。summary 含 address、fromEpoch、toEpoch、各类 rewardWei/Ether、recordCount 及明细数组。

**错误响应**  
参数错误或 DPoS/RewardStore 不可用时返回 `{ "success": false, "error": "错误信息" }`。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getRewardHistory","params":{"address":"0x8f27...","fromEpoch":0,"toEpoch":100,"includeRecords":true},"id":1}'
```

---

### 26. dpos_getValidatorRewardsInfo

**功能说明**  
获取验证者奖励与出块信息汇总（实时计算，含未发放；内部/调试用）。

**RPC 方法**  
`dpos_getValidatorRewardsInfo`

**请求参数**  
对象格式：`{ validator, epoch? }`

| 参数名    | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validator | string | 是   | 验证者地址 |
| epoch     | number | 否   | Epoch 编号 |

**返回结果**

```json
{
  "validator": "0x...",
  "totalRewards": "10000000000000000000",
  "epochRewards": [
    { "epoch": 10, "reward": "1000000000000000000" }
  ],
  "blocksProduced": 1000
}
```

**返回字段说明**  
validator、totalRewards、epochRewards（每项含 epoch、reward）、blocksProduced。

**与 dpos_getRewardHistory 的区别**  
getRewardHistory 从 RewardStore 读已发放记录；未发放则无。getValidatorRewardsInfo 实时计算，未发放也能查；字段侧重出块与汇总。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorRewardsInfo","params":{"validator":"0x8f27..."},"id":1}'
```

---

### 27. dpos_getEpochRangeRewardDetails

**功能说明**  
查询指定 Epoch 范围内所有奖励详情记录（dpos 界面调用）。

**RPC 方法**  
`dpos_getEpochRangeRewardDetails`

**请求参数**

- 数组：`[fromEpoch, toEpoch]`
- 对象：`{ fromEpoch, toEpoch }`

| 参数名    | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| fromEpoch | number | 是   | 起始 Epoch |
| toEpoch   | number | 否   | 结束 Epoch |

**返回结果**

```json
{
  "success": true,
  "fromEpoch": 1,
  "toEpoch": 10,
  "totalRecords": 25,
  "data": [
    {
      "id": 1,
      "epoch_number": 1,
      "recipient": "0x7744...",
      "reward_type": "validator",
      "amount": "50000000000000000000",
      "vote_weight": "1000000000000000000000",
      "timestamp": "2026-01-20T18:36:47Z",
      "transaction_hash": "0x1234...",
      "status": "completed"
    }
  ]
}
```

**返回字段说明**  
success、fromEpoch、toEpoch、totalRecords、data（数组每项含 id、epoch_number、recipient、reward_type、amount、vote_weight、timestamp、transaction_hash、status）。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochRangeRewardDetails","params":[1,10],"id":1}'
```

---

### 28. dpos_getVoterRewardByValidator

**功能说明**  
查询指定投票者在指定验证者处、在给定 Epoch 区间内获得的投票者奖励总额与明细。

**RPC 方法**  
`dpos_getVoterRewardByValidator`

**请求参数**  
仅支持数组格式（按位置）：`[voterAddress, validatorAddress, fromEpoch, toEpoch]`

| 参数名           | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| voterAddress     | string | 是   | 投票者地址 |
| validatorAddress | string | 是   | 验证者地址（被投票目标） |
| fromEpoch       | number | 是   | 起始 Epoch |
| toEpoch          | number | 是   | 结束 Epoch |

**返回结果**

```json
{
  "success": true,
  "voterAddress": "0x4BCB...",
  "validatorAddress": "0x5E20...",
  "fromEpoch": 0,
  "toEpoch": 100,
  "hasVote": false,
  "filteredRewardWei": "1224806558916761241328",
  "filteredRewardEther": "1224.806559",
  "filteredRecordCount": 47,
  "voterRecords": [
    {
      "id": 0,
      "epoch_number": 47,
      "recipient": "0x4BCB...",
      "reward_type": "voter",
      "amount": "15497201894102453723",
      "vote_weight": "1000000000000000000000",
      "validator_address": "0x5E20...",
      "blocks_produced": 400,
      "reward_per_block": "38743004735256134",
      "timestamp": "2026-01-29T02:58:53.324238565Z",
      "status": "completed"
    }
  ],
  "voteAmountWei": "",
  "voteAmountEther": "",
  "note": ""
}
```

**返回字段说明**

| 字段名                | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success               | boolean | 请求是否成功 |
| voterAddress / validatorAddress | string | 投票者/验证者地址 |
| fromEpoch / toEpoch   | number | 查询 Epoch 范围 |
| hasVote               | boolean | 当前是否仍有投票给该验证者 |
| filteredRewardWei / filteredRewardEther | string | 该验证者下投票者奖励总额 |
| filteredRecordCount   | number | 匹配记录数 |
| voterRecords          | array  | 奖励明细（仅来自该验证者） |

**错误响应**  
参数错误或查询失败时返回 `{ "success": false, "error": "错误信息" }`。

**调用示例**

```bash
curl -X POST https://testnet-rpc.vcity.app \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVoterRewardByValidator","params":["0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe","0x5E202620321A1411F4a160127AebF6cde8b56175",0,100],"id":1}'
```

---

## 7 区块生产者相关接口

### 29. dpos_getBlockProducers

**功能说明**  
获取指定区块范围或 Epoch 内的区块生产者及出块分布。

**RPC 方法**  
`dpos_getBlockProducers`

**请求参数**

- 区块范围：`[startBlock, endBlock]`
- Epoch：`[epochNumber]` 或 `[{ epoch: epochNumber }]`

| 参数名      | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| startBlock  | number | 是*  | 起始区块（区块范围模式） |
| endBlock    | number | 是*  | 结束区块（区块范围模式） |
| epoch       | number | 是*  | Epoch 编号（Epoch 模式） |

**返回结果**

```json
{
  "success": true,
  "mode": "blockRange",
  "startBlock": 7370,
  "endBlock": 7380,
  "totalBlocks": 11,
  "actualBlocksFound": 11,
  "producers": [
    { "address": "0xe226...", "blocks": [7370, 7374, 7378, 7382], "count": 4 },
    { "address": "0x7744...", "blocks": [7371, 7375, 7379], "count": 3 }
  ]
}
```

**返回字段说明**

| 字段名              | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success             | boolean | 请求是否成功 |
| mode                | string | blockRange / epoch |
| startBlock / endBlock | number | 区块范围 |
| totalBlocks / actualBlocksFound | number | 总区块数 / 实际找到数 |
| producers           | array  | 生产者地址、出块列表、出块数 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getBlockProducers","params":[7370,7380],"id":1}'
```

---

### 30. dpos_getValidatorBlockStats

**功能说明**  
获取指定验证者在指定 Epoch 内的出块统计信息。

**RPC 方法**  
`dpos_getValidatorBlockStats`

**请求参数**  
仅支持数组格式（按位置）：`[validatorAddress, epochNumber]`；或对象：`{ validatorAddress, epochNumber }`

| 参数名           | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validatorAddress | string | 是   | 验证者地址 |
| epochNumber      | number | 是   | Epoch 编号 |

**返回结果**

```json
{
  "success": true,
  "data": {
    "validator": "0x...",
    "epoch": 10,
    "blocksProduced": 100,
    "totalBlocks": 400
  }
}
```

**返回字段说明**

| 字段名 | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success | boolean | 请求是否成功 |
| data   | object | 出块统计（validator、epoch、blocksProduced、totalBlocks） |

**错误响应**  
DPoS 引擎不可用或该引擎未实现该方法时返回 `{ "success": false, "error": "..." }`。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorBlockStats","params":["0x7744E828e4Bd34aAfBB409C3b574B3647198EE65",10],"id":1}'
```

---

## 8 区块削减相关接口

### 31. dpos_getValidatorSlashingHistory

**功能说明**  
获取指定验证者的所有削减历史记录（聚合该验证者下所有投票者的削减）。

**RPC 方法**  
`dpos_getValidatorSlashingHistory`

**请求参数**

- 数组：`["0x..."]`
- 对象：`{ validator: "0x..." }`

| 参数名    | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| validator | string | 是   | 验证者地址 |

**返回结果**

```json
{
  "success": true,
  "validator": "0x655c9867d6B462aF9f5F984B127072E552405135",
  "lastSlashTime": 1768913678,
  "totalSlashAmount": "50000000000000000",
  "totalSlashCount": 1,
  "slashingHistory": [
    {
      "blockNumber": 7465,
      "epochNumber": 6,
      "missedBlocks": 3,
      "missedBlocksPercentage": 10000,
      "oldVoteAmount": "10000000000000000000",
      "newVoteAmount": "9950000000000000000",
      "slashAmount": "50000000000000000",
      "slashRate": 50,
      "reason": "Epoch 6: missed blocks percentage reached threshold...",
      "validatorAddr": "0x655c...",
      "voterAddress": "0x4BCB...",
      "timestamp": 1768913678
    }
  ]
}
```

**返回字段说明**  
success、validator、lastSlashTime、totalSlashAmount、totalSlashCount、slashingHistory（每项含 blockNumber、epochNumber、missedBlocks、missedBlocksPercentage、oldVoteAmount、newVoteAmount、slashAmount、slashRate、reason、validatorAddr、voterAddress、timestamp）。

**调用示例**

```bash
curl -X POST http://127.0.0.1:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorSlashingHistory","params":["0x655c9867d6B462aF9f5F984B127072E552405135"],"id":1}'
```

---

## 9 其他查询接口

### 32. dpos_getConsensusState

**功能说明**  
获取当前共识状态（轮次、当前出块验证者等）。

**RPC 方法**  
`dpos_getConsensusState`

**请求参数**  
无。传 `[]` 或空数组。

**返回结果**

```json
{
  "success": true,
  "currentRound": 10,
  "currentDelegate": "0x1234567890123456789012345678901234567890",
  "currentDelegateIndex": 0
}
```

**返回字段说明**

| 字段名               | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success              | boolean | 请求是否成功 |
| currentRound         | number | 当前轮次 |
| currentDelegate      | string | 当前出块验证者地址 |
| currentDelegateIndex | number | 当前验证者索引 |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getConsensusState","params":[],"id":1}'
```

---

### 33. dpos_getAccountBalance

**功能说明**  
获取账户余额。

**RPC 方法**  
`dpos_getAccountBalance`

**请求参数**

- 数组：`[address]`
- 字符串：`"0x..."`
- 对象：`{ address: "0x..." }`

| 参数名  | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| address | string | 是   | 账户地址 |

**返回结果**

```json
{
  "address": "0x...",
  "balance": "1000000000000000000"
}
```

**返回字段说明**

| 字段名  | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| address | string | 账户地址 |
| balance | string | 余额（Wei 字符串） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getAccountBalance","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 34. dpos_getConsensusSwitchHeight

**功能说明**  
获取共识切换高度（DPoS 生效的区块高度）及当前区块高度、是否已切换等信息。

**RPC 方法**  
`dpos_getConsensusSwitchHeight`

**请求参数**  
无。传 `[]` 或不传 params 均可。

**返回结果**

```json
{
  "success": true,
  "consensusSwitchHeight": 1000,
  "switchBlockTimestamp": 1704067200,
  "switchBlockHash": "0x...",
  "currentBlockHeight": 15000,
  "isDPoSActive": true
}
```

**返回字段说明**

| 字段名                 | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success                | boolean | 请求是否成功 |
| consensusSwitchHeight  | number | 共识切换区块高度（0 表示未配置或无法获取） |
| switchBlockTimestamp   | number | 切换高度区块的时间戳 |
| switchBlockHash        | string | 切换高度区块哈希 |
| currentBlockHeight     | number | 当前区块高度 |
| isDPoSActive            | boolean | 当前是否已处于 DPoS 共识（currentBlockHeight >= consensusSwitchHeight） |

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getConsensusSwitchHeight","params":[],"id":1}'
```

---

### 35. dpos_getValidatorsFromBlockExtraData

**功能说明**  
从指定区块头的 ExtraData 中解析并返回该区块的验证者集合（地址、投票权重、是否活跃）。

**RPC 方法**  
`dpos_getValidatorsFromBlockExtraData`

**请求参数**

- 数组：`[blockNumber]`
- 对象：`{ blockNumber: number }`

| 参数名       | 类型   | 必填 | 说明 |
|------------------------|----------|------|--------------------------------------------------------------------------------|
| blockNumber  | number | 是   | 区块号 |

**返回结果**

```json
{
  "success": true,
  "blockNumber": 12345,
  "count": 4,
  "validators": [
    {
      "index": 0,
      "address": "0x...",
      "votingPower": "1000000000000000000000",
      "isActive": true
    }
  ]
}
```

**返回字段说明**

| 字段名      | 类型   | 说明（中文） |
|------------------------|----------|--------------------------------------------------------------------------------|
| success     | boolean | 请求是否成功 |
| blockNumber | number | 查询的区块号 |
| count       | number | 验证者数量 |
| validators  | array  | 验证者列表，每项含 index、address、votingPower、isActive |

**错误响应**  
参数错误、区块不存在、DPoS 引擎不可用或未实现该方法时，通过 JSON-RPC 的 error 返回，不放在 result 中。

**调用示例**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorsFromBlockExtraData","params":[12345],"id":1}'
```

---

## 接口索引（共 35 个）

| 序号 | RPC 方法 | 分类 |
|------|----------|------|
| 1 | dpos_createParameterProposal | 1 治理 |
| 2 | dpos_createRecoveryProposal | 1 治理 |
| 3 | dpos_getParameterProposal | 1 治理 |
| 4 | dpos_voteOnParameterProposal | 1 治理 |
| 5 | dpos_getActiveProposals | 1 治理 |
| 6 | dpos_getVotableCurrentParameters | 1 治理 |
| 7 | dpos_executeParameterUpdate | 1 治理 |
| 8 | dpos_vote | 2 投票 |
| 9 | dpos_getVoteRecords | 2 投票 |
| 10 | dpos_getStakingInfo | 3 质押 |
| 11 | dpos_getVotingPower | 4 验证者 |
| 12 | dpos_getValidatorVotingDetails | 4 验证者 |
| 13 | dpos_registerDelegate | 4 验证者 |
| 14 | dpos_getDelegateRegistrations | 4 验证者 |
| 15 | dpos_withdrawDelegate | 4 验证者 |
| 16 | dpos_canWithdrawDelegate | 4 验证者 |
| 17 | dpos_getFreezeInfo | 4 验证者 |
| 18 | dpos_getValidatorCommission | 4 验证者 |
| 19 | dpos_updateCommission | 4 验证者 |
| 20 | dpos_getVoterSlashingHistory | 4 验证者 |
| 21 | dpos_getCurrentEpochInfo | 5 Epoch |
| 22 | dpos_getEpochInfoByNumber | 5 Epoch |
| 23 | dpos_getLatestEpochInfo | 5 Epoch |
| 24 | dpos_getEpochRewardDetails | 6 奖励 |
| 25 | dpos_getRewardHistory | 6 奖励 |
| 26 | dpos_getValidatorRewardsInfo | 6 奖励 |
| 27 | dpos_getEpochRangeRewardDetails | 6 奖励 |
| 28 | dpos_getVoterRewardByValidator | 6 奖励 |
| 29 | dpos_getBlockProducers | 7 区块生产者 |
| 30 | dpos_getValidatorBlockStats | 7 区块生产者 |
| 31 | dpos_getValidatorSlashingHistory | 8 区块削减 |
| 32 | dpos_getConsensusState | 9 其他 |
| 33 | dpos_getAccountBalance | 9 其他 |
| 34 | dpos_getConsensusSwitchHeight | 9 其他 |
| 35 | dpos_getValidatorsFromBlockExtraData | 9 其他 |

说明：接口数量以 `jsonrpc/dpos_endpoint.go` 中 *DPOS 的导出方法为准，共 35 个。
