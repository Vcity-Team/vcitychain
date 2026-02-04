# DPoS RPC API 

This document describes the DPoS RPC API in a unified structure, aligned with the exported methods in `jsonrpc/dpos_endpoint.go` for easier reference and maintenance.

---

## Document Conventions

### Unified Structure (each endpoint includes)

| Section | Description |
|---------|-------------|
| **Description** | One-line description of the endpoint purpose |
| **RPC method** | Method name, e.g. `dpos_xxx` |
| **Request parameters** | Parameter format + parameter table (name, type, required, description) |
| **Response** | Example JSON (only `result` content) + response field table |
| **Example** | cURL example; use comments when multiple parameter forms are shown |

Optional sections (as needed): **Error response**, **Notes**, **Difference from xxx**.

### Parameter Conventions

- **Format**: For each endpoint, "Request parameters" first state **supported forms**, then the **parameter table** with columns: name, type, required, description.
- **Wording**:
  - **Array format**: `[ param1, param2, ... ]`, with **order** matching the parameter table top to bottom.
  - **Object format**: `{ "paramName": value, ... }`, keys matching **parameter names** in the table.
  - **String format**: For a single string param you may pass `"value"` (or `["value"]` depending on implementation).
  - **Number format**: For a single numeric param you may pass `10` or `[10]`.
  - **No parameters**: Write "None" or "pass `[]` / empty array".
- **Object in array**: For some endpoints `params` may be an object or "array with single object" `[ object ]`; the doc will state "object format (may be wrapped in array)".
- **Recommendation**: **Object format** is easier to read and extend; array format is compact but order must match the doc.

**Object format support (aligned with code)**  
- **Object only, no array**: `dpos_registerDelegate` (must pass `{ registrant, name, website, description, privateKey, ... }`).
- **No object, array or string only** (implementation does not parse `map[string]interface{}`):
  - `dpos_getVoterRewardByValidator`: `[voterAddress, validatorAddress, fromEpoch, toEpoch]`;
  - `dpos_getStakingInfo`: `[]` or `[blockNumber]` (by position);
  - `dpos_getEpochInfoByNumber`: `[epochNumber]` (by position);
  - `dpos_getValidatorBlockStats`: `[validatorAddress, epochNumber]` (by position).
- **No-parameter endpoints** (e.g. `dpos_getCurrentEpochInfo`, `dpos_getLatestEpochInfo`, `dpos_getDelegateRegistrations`, `dpos_getVotableCurrentParameters`, `dpos_getConsensusSwitchHeight`): pass `[]` or omit.
- All other endpoints support both **array** and **object** parameter forms.

### Terminology

- **Validator / candidate / delegate**: All refer to the node address that can receive votes; this document uses "validator" consistently.
- **Voter**: The address that casts votes.
- **Proposal ID**: Written as `proposalId` (camelCase).
- **Amounts**: Wei as integer string; Ether as string with 6 decimal places (unless stated otherwise).

### Response Format

- Example responses show the JSON-RPC `result` field; the full response is still `{"jsonrpc":"2.0","id":1,"result":{...}}`.
- **Success**: Top level includes `"success": true`; payload is in `data`, `validator`, `summary`, `records`, etc., or at the same level as `success`.
- **Failure**: Unified as `"success": false` and `"error": "error message"`.
- Endpoints involving private keys are for documentation only; example private keys are placeholders and must not be used in production.
- **Field descriptions**: In the "Response" field tables, the description column explains each field for clients and callers.

**Response alignment**  
Response structures and fields in this document have been checked against `jsonrpc/dpos_endpoint.go` and types in `consensus/dpos`; if the implementation changes, follow the code and update this document accordingly.

---

## 1 Governance

### 1. dpos_createParameterProposal

**Description**  
Create a parameter change proposal.

**RPC method**  
`dpos_createParameterProposal`

**Request parameters**

- Array: `[parameter, newValue, description, proposer, proposerPrivateKey]`
- Object: `{ parameter, newValue, description, proposer, proposerPrivateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| parameter | string | Yes | Parameter name to change (must be a votable parameter) |
| newValue | any | Yes | New value for the parameter |
| description | string | No | Proposal description (optional) |
| proposer | string | Yes | Proposer address (must be a validator) |
| proposerPrivateKey | string | Yes | Proposer private key (64 hex chars, no 0x prefix) |

**Response**

```json
{
  "success": true,
  "txHash": "0x...",
  "proposalId": "proposal_0x...",
  "parameter": "parameter_name",
  "newValue": "new_value",
  "proposer": "0x...",
  "currentBlockNumber": 12345,
  "message": "Parameter proposal transaction created and broadcasted successfully",
  "note": "Proposal will be created when transaction is included in a block"
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the RPC call succeeded |
| txHash | string | Transaction hash that creates the proposal (0x-prefixed hex) |
| proposalId | string | Unique proposal ID derived from tx hash, e.g. proposal_0x... |
| parameter | string | On-chain parameter name to change |
| newValue | any | New value (type depends on parameter) |
| proposer | string | Proposer address (must be a validator) |
| currentBlockNumber | number | Current chain block height |
| message | string | Success message |
| note | string | Additional note (e.g. proposal is created when tx is mined) |

**Example**

```bash
# Array format
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_createParameterProposal","params":["dpos_missed_blocks_percentage",1500,"Raise missed block threshold to 15%","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","a1b2c3d4e5f6789012345678901234567890123456789012345678901234567890"],"id":1}'

# Object format
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_createParameterProposal","params":{"parameter":"dpos_missed_blocks_percentage","newValue":1500,"description":"Raise missed block threshold to 15%","proposer":"0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","proposerPrivateKey":"a1b2c3d4e5f6789012345678901234567890123456789012345678901234567890"},"id":1}'
```

---

### 2. dpos_createRecoveryProposal

**Description**  
Create a validator recovery proposal.

**RPC method**  
`dpos_createRecoveryProposal`

**Request parameters**

- Array: `[validatorAddress, recoveryReason, description, proposer, proposerPrivateKey]`
- Object: `{ validatorAddress, recoveryReason, description, proposer, proposerPrivateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validatorAddress | string | Yes | Validator address to recover |
| recoveryReason | string | Yes | Reason for recovery |
| description | string | No | Proposal description (optional) |
| proposer | string | Yes | Proposer address (must be a validator) |
| proposerPrivateKey | string | Yes | Proposer private key (64 hex chars, no 0x) |

**Response**  
Same structure as dpos_createParameterProposal, with proposalType "recovery".

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_createRecoveryProposal","params":["0x907b5aa2540da7b8c8c8c8c8c8c8c8c8c8c8c8c8","Validator was temporarily offline due to network, now recovered","Recovery proposal","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","a1b2c3d4e5f6789012345678901234567890123456789012345678901234567890"],"id":1}'
```

---

### 3. dpos_getParameterProposal

**Description**  
Get proposal details (parameter or recovery proposal).

**RPC method**  
`dpos_getParameterProposal`

**Request parameters**

- Array: `[proposalId]`
- Object: `{ proposalId: "proposal_0x..." }`
- String: `"proposal_0x..."`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| proposalId | string | Yes | Proposal ID |

**Response**

```json
{
  "success": true,
  "proposal": {
    "proposalId": "proposal_0x...",
    "proposalType": "parameter",
    "parameter": "parameter_name",
    "newValue": "new_value",
    "oldValue": "old_value",
    "status": "Active",
    "voteStats": {},
    "timeInfo": {},
    "voterDetails": {}
  }
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| proposal | object | Full proposal object |
| proposal.proposalId | string | Unique proposal ID (e.g. proposal_0x...) |
| proposal.proposalType | string | "parameter" or "recovery" |
| proposal.parameter / newValue / oldValue | - | Parameter name, new value, old value (for parameter proposals) |
| proposal.validator / recoveryReason | - | Validator to recover, recovery reason (for recovery proposals) |
| proposal.status | string | Active / Passed / Rejected / Executed |
| proposal.voteStats | object | Vote counts, for/against, passed or not, threshold, etc. |
| proposal.timeInfo | object | Block height and time info (voting period, remaining blocks, etc.) |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getParameterProposal","params":["proposal_0x2f773b9c6421f65"],"id":1}'
```

---

### 4. dpos_voteOnParameterProposal

**Description**  
Vote on a proposal (support or oppose).

**RPC method**  
`dpos_voteOnParameterProposal`

**Request parameters**

- Array: `[proposalId, voter, support, privateKey]`
- Object: `{ proposalId, voter, support, privateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| proposalId | string | Yes | Proposal ID |
| voter | string | Yes | Voter address |
| support | boolean | Yes | true = support, false = oppose |
| privateKey | string | Yes | Voter private key (64 hex chars, no 0x) |

**Response**

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

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the RPC call succeeded |
| txHash | string | Vote transaction hash (0x-prefixed) |
| proposalId | string | Proposal ID voted on |
| voter | string | Voter address |
| support | boolean | true = support, false = oppose |
| currentBlockNumber | number | Current chain block height |
| message | string | Success message |
| note | string | Note (e.g. vote is counted when tx is mined) |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_voteOnParameterProposal","params":["proposal_0x2f773b9c6421f65","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127",true,"a1b2c3d4e5f6..."],"id":1}'
```

---

### 5. dpos_getActiveProposals

**Description**  
Get all active proposals.

**RPC method**  
`dpos_getActiveProposals`

**Request parameters**  
None. Pass `[]` or empty array.

**Response**

```json
{
  "success": true,
  "proposals": [
    {
      "proposalId": "proposal_0x...",
      "parameter": "parameter_name",
      "oldValue": "old_value",
      "newValue": "new_value",
      "proposer": "0x...",
      "startBlock": 1000,
      "endBlock": 2000,
      "status": "Active",
      "threshold": 50,
      "description": "Proposal description",
      "createdAt": "2024-01-01 12:00:00",
      "createdAtTs": 1704067200,
      "votes": 5,
      "isProposalExpired": false
    }
  ],
  "count": 1
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| proposals | array | List of active proposals, newest first |
| count | number | Number of proposals returned |

Each item in **proposals**: proposalId, parameter/oldValue/newValue, proposer, startBlock/endBlock, status, threshold, description, createdAt/createdAtTs, votes, isProposalExpired (whether execution window has expired).

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getActiveProposals","params":[],"id":1}'
```

---

### 6. dpos_getVotableCurrentParameters

**Description**  
Get current values and metadata for all votable parameters.

**RPC method**  
`dpos_getVotableCurrentParameters`

**Request parameters**  
None.

**Response**

```json
{
  "success": true,
  "parameters": {
    "dpos_missed_blocks_percentage": {
      "name": "dpos_missed_blocks_percentage",
      "type": "uint64",
      "minValue": 0,
      "maxValue": 10000,
      "description": "Validator missed-block percentage threshold (basis points, 10000=100%)",
      "category": "consensus",
      "currentValue": 1200
    }
  },
  "count": 1
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| parameters | object | Map of parameter name to parameter info |
| count | number | Number of votable parameters |
| parameters[].name | string | Parameter name |
| parameters[].type | string | Parameter type (e.g. uint64) |
| parameters[].minValue / maxValue | number | Allowed min/max |
| parameters[].currentValue | number | Current on-chain value |
| parameters[].description | string | Parameter description |
| parameters[].category | string | Category (e.g. consensus) |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVotableCurrentParameters","params":[],"id":1}'
```

---

### 7. dpos_executeParameterUpdate

**Description**  
Execute a passed parameter update proposal.

**RPC method**  
`dpos_executeParameterUpdate`

**Request parameters**

- Array: `[proposalId, executor, executorPrivateKey]`
- Object: `{ proposalId, executor, executorPrivateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| proposalId | string | Yes | Proposal ID |
| executor | string | Yes | Executor address |
| executorPrivateKey | string | Yes | Executor private key (64 hex chars, no 0x) |

**Response**

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

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the RPC call succeeded |
| txHash | string | Execute-proposal transaction hash (0x-prefixed) |
| proposalId | string | Proposal ID executed |
| executor | string | Executor address (any address may execute a passed proposal) |
| currentBlockNumber | number | Current chain block height |
| message | string | Success message |
| note | string | Note (e.g. execution takes effect when tx is mined) |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_executeParameterUpdate","params":["proposal_0x2f773b9c6421f65","0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","a1b2c3d4e5f6..."],"id":1}'
```

---

## 2 Voting

### 8. dpos_vote

**Description**  
Vote for a validator (delegate) or revoke vote.

**RPC method**  
`dpos_vote`

**Request parameters**

- Array: single `[address]`, three `[voter, candidate, amount]`, or four `[voter, candidate, amount, privateKey]`
- Object: `{ voter, candidate, amount, privateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| voter | string | Yes | Voter address |
| candidate | string | Yes | Validator (candidate) address |
| amount | string | Yes | Vote amount in Wei string; -1 to revoke |
| privateKey | string | No | Voter private key; if provided, creates and broadcasts tx |

**Response**

```json
{
  "success": true,
  "message": "Vote operation completed successfully",
  "txHash": "0x...",
  "blockNumber": 12345
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the operation succeeded |
| message | string | Operation message |
| txHash | string | Transaction hash (when private key provided) |
| blockNumber | number | Block number where tx was included |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_vote","params":["0x8f27...","0x907b...","1000000000000000000","a1b2c3d4e5f6..."],"id":1}'
```

---

### 9. dpos_getVoteRecords

**Description**  
Query vote/staking records with filters (voter, delegate, active/pending), pagination and ordering.

**RPC method**  
`dpos_getVoteRecords`

**Request parameters**

- Object (may be wrapped in array): `[ { voter?, delegate?, onlyActive?, includePending?, limit?, offset?, order? } ]` or object.

| Name | Type | Required | Description |
|------|------|----------|-------------|
| voter | string | No* | Voter address (at least one of voter/delegate) |
| delegate | string | No* | Delegate (validator) address |
| onlyActive | boolean | No | Only return active records (IsActive=true) |
| includePending | boolean | No | Include pending records (Applied=false) |
| limit | number | No | Max records to return |
| offset | number | No | Pagination offset |
| order | string | No | Sort: asc / desc by startTime |

**Response**

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

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| total | number | Total matching records (before pagination) |
| records | array | Vote records for current page (paginated and sorted) |

**Each record**: voter, delegate, amountWei/amountEther, originalAmountWei/originalAmountEther, startTime/endTime, isLocked, isActive, applied, effectiveEpoch, pendingUnvote, unvoteEffectiveEpoch. originalAmount* is set when vote was revoked; pendingUnvote and unvoteEffectiveEpoch indicate revoke at epoch boundary.

**Example**

```bash
curl -X POST https://testnet-rpc.vcity.app \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVoteRecords","params":[{"voter":"0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127","onlyActive":true,"includePending":false,"limit":100,"offset":0,"order":"desc"}],"id":1}'
```

---

## 3 Staking

### 10. dpos_getStakingInfo

**Description**  
Get staking info (validator list and staking state per validator).

**RPC method**  
`dpos_getStakingInfo`

**Request parameters**

- No params: `[]`
- Optional block: array `[blockNumber]` or object `{ blockNumber }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| blockNumber | number | No | Block number to query |

**Response**

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

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| data | array | Validator list; each item is staking/vote summary for that validator |
| currentEpoch | number | Current chain epoch (to tell if a vote has taken effect) |

**Each data item**: staker, amount/amountEther, effectiveAmountWei/Ether, pendingAmountWei/Ether, isActive, faultFlag (isFaulty, missedBlocks, reason), rewards (optional).

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getStakingInfo","params":[],"id":1}'
```

---

## 4 Validators

### 11. dpos_getVotingPower

**Description**  
Get voting power (total votes) for a validator.

**RPC method**  
`dpos_getVotingPower`

**Request parameters**

- Array: `[delegate]` or `[delegate, blockNumber]`
- Object: `{ delegate, blockNumber? }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| delegate | string | Yes | Validator address |
| blockNumber | string/number | No | Block number ("latest" or number) |

**Response**

```json
{
  "success": true,
  "delegate": "0x7744E828e4Bd34aAfBB409C3b574B3647198EE65",
  "votingPower": "101000000000000000000000",
  "isActive": true
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| delegate | string | Validator address |
| votingPower | string | Voting power in Wei (total votes) |
| isActive | boolean | Whether the validator is currently active |

**Error**  
When validator not found: `{ "success": false, "error": "delegate not found: 0x..." }`.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVotingPower","params":["0x907b5aa2540da7b8c8c8c8c8c8c8c8c8c8c8c8c8"],"id":1}'
```

---

### 12. dpos_getValidatorVotingDetails

**Description**  
Get voting/staking summary for a validator (or any address): votes received, votes cast, voting power, active status.

**RPC method**  
`dpos_getValidatorVotingDetails`

**Request parameters**

- Array: `[validator]`
- String: `"0x..."`
- Object: `{ validator: "0x..." }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validator | string | Yes | Validator or any address |

**Response**

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

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| validator | object | Voting/staking summary for the address |
| validator.address | string | Queried address |
| validator.votingPower / totalStakedToMe | string | Total votes received (Wei), effective + pending |
| validator.isActive | boolean | Whether address is an active validator |
| validator.stakeCount / stakes | number/array | Number of stakers and list per staker (effective/pending amounts) |
| validator.totalVotedByMe / myVoteCount / myVotes | - | Total voted by this address and list per delegate |
| validator.hasInboundVotes | boolean | Whether anyone has voted for this address |
| validator.currentEpoch | number | Current chain epoch |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorVotingDetails","params":["0x907b5aa2540da7b8c8c8c8c8c8c8c8c8c8c8c8c8"],"id":1}'
```

---

### 13. dpos_registerDelegate

**Description**  
Register delegate (validator) info.

**RPC method**  
`dpos_registerDelegate`

**Request parameters**  
Object only: `{ registrant, name, website?, description?, privateKey, chainID? }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| registrant | string | Yes | Registrant address |
| name | string | Yes | Delegate name |
| website | string | No | Website URL |
| description | string | No | Description |
| privateKey | string | Yes | Registrant private key (64 hex chars) |
| chainID | number | No | Chain ID |

**Response**

```json
{
  "success": true,
  "message": "Delegate registered successfully",
  "txHash": "0x...",
  "registrant": "0x...",
  "name": "Validator name"
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the call succeeded |
| message | string | Success message |
| txHash | string | Transaction hash |
| registrant | string | Registrant address |
| name | string | Validator name |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_registerDelegate","params":{"registrant":"0x8f27...","name":"My validator node","website":"https://...","description":"...","privateKey":"a1b2c3d4e5f6..."},"id":1}'
```

---

### 14. dpos_getDelegateRegistrations

**Description**  
Get all delegate registrations.

**RPC method**  
`dpos_getDelegateRegistrations`

**Request parameters**  
None. Pass `[]` or empty array.

**Response**

```json
{
  "delegates": [
    {
      "address": "0x...",
      "name": "Validator name",
      "website": "https://...",
      "description": "Description",
      "registeredAt": 1704067200
    }
  ],
  "count": 1
}
```

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| delegates | array | List of delegates (address, name, website, description, registeredAt) |
| count | number | Number of delegates |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getDelegateRegistrations","params":[],"id":1}'
```

---

### 15. dpos_withdrawDelegate

**Description**  
Withdraw delegate registration.

**RPC method**  
`dpos_withdrawDelegate`

**Request parameters**  
Object: `{ registrant, privateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| registrant | string | Yes | Registrant address |
| privateKey | string | Yes | Registrant private key |

**Response**

```json
{
  "success": true,
  "message": "Delegate withdrawal successful",
  "txHash": "0x...",
  "registrant": "0x..."
}
```

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_withdrawDelegate","params":{"registrant":"0x8f27...","privateKey":"a1b2c3d4e5f6..."},"id":1}'
```

---

### 16. dpos_canWithdrawDelegate

**Description**  
Check whether an address can withdraw delegate registration.

**RPC method**  
`dpos_canWithdrawDelegate`

**Request parameters**

- Array: `[address]`
- String: `"0x..."`
- Object: `{ address: "0x..." }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| address | string | Yes | Validator address |

**Response**

```json
{
  "canWithdraw": true,
  "reason": "Can withdraw",
  "blockNumber": 12345
}
```

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_canWithdrawDelegate","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 17. dpos_getFreezeInfo

**Description**  
Get validator freeze info.

**RPC method**  
`dpos_getFreezeInfo`

**Request parameters**

- Array: `[address]`
- String: `"0x..."`
- Object: `{ address: "0x..." }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| address | string | Yes | Validator address |

**Response**

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

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getFreezeInfo","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 18. dpos_getValidatorCommission

**Description**  
Get validator commission rate.

**RPC method**  
`dpos_getValidatorCommission`

**Request parameters**

- Array: `[validatorAddress]`
- String: `"0x..."`
- Object: `{ validator: "0x..." }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validatorAddress | string | Yes | Validator address |

**Response**

```json
{
  "validator": "0x...",
  "commissionRate": 10,
  "commissionRatePercent": "10.00%",
  "lastUpdatedBlock": 12345
}
```

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorCommission","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 19. dpos_updateCommission

**Description**  
Update validator commission rate.

**RPC method**  
`dpos_updateCommission`

**Request parameters**  
Object: `{ validator, newCommissionRate, privateKey }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validator | string | Yes | Validator address |
| newCommissionRate | number | Yes | New commission (basis points, 100=1%) |
| privateKey | string | Yes | Validator private key |

**Response**

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

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_updateCommission","params":{"validator":"0x8f27...","newCommissionRate":15,"privateKey":"a1b2c3d4e5f6..."},"id":1}'
```

---

### 20. dpos_getVoterSlashingHistory

**Description**  
Get voter slashing history.

**RPC method**  
`dpos_getVoterSlashingHistory`

**Request parameters**

- Array: `[voterAddress]` or `[voterAddress, fromEpoch, toEpoch]`
- Object: `{ voter, fromEpoch?, toEpoch? }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| voterAddress | string | Yes | Voter address |
| fromEpoch | number | No | Start epoch |
| toEpoch | number | No | End epoch |

**Response**

```json
{
  "voter": "0x...",
  "slashingHistory": [
    {
      "epoch": 10,
      "amount": "1000000000000000000",
      "reason": "Validator was slashed",
      "blockNumber": 12345
    }
  ],
  "totalSlashing": "1000000000000000000"
}
```

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVoterSlashingHistory","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

## 5 Epoch

### 21. dpos_getCurrentEpochInfo

**Description**  
Get current epoch info.

**RPC method**  
`dpos_getCurrentEpochInfo`

**Request parameters**  
None. Pass `[]` or empty array.

**Response**

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

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getCurrentEpochInfo","params":[],"id":1}'
```

---

### 22. dpos_getEpochInfoByNumber

**Description**  
Get epoch info by epoch number.

**RPC method**  
`dpos_getEpochInfoByNumber`

**Request parameters**

- Number: `10`
- Array: `[10]`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| epochNumber | number | Yes | Epoch number |

**Response**  
Same structure as dpos_getCurrentEpochInfo.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochInfoByNumber","params":[10],"id":1}'
```

---

### 23. dpos_getLatestEpochInfo

**Description**  
Get latest epoch info (same as dpos_getCurrentEpochInfo).

**RPC method**  
`dpos_getLatestEpochInfo`

**Request parameters**  
None. Pass `[]` or empty array.

**Response**  
Same as dpos_getCurrentEpochInfo.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getLatestEpochInfo","params":[],"id":1}'
```

---

## 6 Rewards

### 24. dpos_getEpochRewardDetails

**Description**  
Get reward details for a given epoch (internal use).

**RPC method**  
`dpos_getEpochRewardDetails`

**Request parameters**

- Array: `[epochNumber]`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| epochNumber | number | Yes | Epoch number |

**Response**

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

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochRewardDetails","params":[10],"id":1}'
```

---

### 25. dpos_getRewardHistory

**Description**  
Get reward summary and details for an address in an epoch range (general purpose).

**RPC method**  
`dpos_getRewardHistory`

**Request parameters**

- Array: `[address, fromEpoch, toEpoch, includeRecords?]`
- Object: `{ address, fromEpoch, toEpoch, type?, includeRecords? }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| address | string | Yes | Address (validator or voter) |
| fromEpoch | number | Yes | Start epoch |
| toEpoch | number | Yes | End epoch |
| type | string | No | "validator" or "voter"; omit for all |
| includeRecords | boolean | No | Include detail array; false = summary only |

**Response**

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

**Error**  
Invalid params or DPoS/RewardStore unavailable: `{ "success": false, "error": "error message" }`.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getRewardHistory","params":{"address":"0x8f27...","fromEpoch":0,"toEpoch":100,"includeRecords":true},"id":1}'
```

---

### 28. dpos_getValidatorRewardsInfo

**Description**  
Get validator rewards and block production summary (computed on the fly, includes undistributed; internal/debug).

**RPC method**  
`dpos_getValidatorRewardsInfo`

**Request parameters**  
Object: `{ validator, epoch? }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validator | string | Yes | Validator address |
| epoch | number | No | Epoch number |

**Response**

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

**Difference from dpos_getRewardHistory**  
getRewardHistory reads from RewardStore (only already-distributed). getValidatorRewardsInfo computes in real time (includes undistributed) and focuses on block production and aggregates.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorRewardsInfo","params":{"validator":"0x8f27..."},"id":1}'
```

---

### 27. dpos_getEpochRangeRewardDetails

**Description**  
Query all reward detail records in an epoch range (used by DPoS UI).

**RPC method**  
`dpos_getEpochRangeRewardDetails`

**Request parameters**

- Array: `[fromEpoch, toEpoch]`
- Object: `{ fromEpoch, toEpoch }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| fromEpoch | number | Yes | Start epoch |
| toEpoch | number | No | End epoch |

**Response**

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

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochRangeRewardDetails","params":[1,10],"id":1}'
```

---

### 30. dpos_getVoterRewardByValidator

**Description**  
Get total and detail of voter rewards for a given voter and validator in an epoch range.

**RPC method**  
`dpos_getVoterRewardByValidator`

**Request parameters**  
Array only (by position): `[voterAddress, validatorAddress, fromEpoch, toEpoch]`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| voterAddress | string | Yes | Voter address |
| validatorAddress | string | Yes | Validator address (vote target) |
| fromEpoch | number | Yes | Start epoch |
| toEpoch | number | Yes | End epoch |

**Response**

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

**Error**  
Invalid params or query failure: `{ "success": false, "error": "error message" }`.

**Example**

```bash
curl -X POST https://testnet-rpc.vcity.app \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getVoterRewardByValidator","params":["0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe","0x5E202620321A1411F4a160127AebF6cde8b56175",0,100],"id":1}'
```

---

## 7 Block producers

### 29. dpos_getBlockProducers

**Description**  
Get block producers and block distribution for a block range or epoch.

**RPC method**  
`dpos_getBlockProducers`

**Request parameters**

- Block range: `[startBlock, endBlock]`
- Epoch: `[epochNumber]` or `[{ epoch: epochNumber }]`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| startBlock | number | Yes* | Start block (block-range mode) |
| endBlock | number | Yes* | End block (block-range mode) |
| epoch | number | Yes* | Epoch number (epoch mode) |

**Response**

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

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getBlockProducers","params":[7370,7380],"id":1}'
```

---

### 32. dpos_getValidatorBlockStats

**Description**  
Get block production stats for a validator in an epoch.

**RPC method**  
`dpos_getValidatorBlockStats`

**Request parameters**  
Array only (by position): `[validatorAddress, epochNumber]`; or object: `{ validatorAddress, epochNumber }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validatorAddress | string | Yes | Validator address |
| epochNumber | number | Yes | Epoch number |

**Response**

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

**Error**  
DPoS engine unavailable or method not implemented: `{ "success": false, "error": "..." }`.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorBlockStats","params":["0x7744E828e4Bd34aAfBB409C3b574B3647198EE65",10],"id":1}'
```

---

## 8 Slashing

### 31. dpos_getValidatorSlashingHistory

**Description**  
Get all slashing history for a validator (aggregates slashing for all voters under that validator).

**RPC method**  
`dpos_getValidatorSlashingHistory`

**Request parameters**

- Array: `["0x..."]`
- Object: `{ validator: "0x..." }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| validator | string | Yes | Validator address |

**Response**

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

**Example**

```bash
curl -X POST http://127.0.0.1:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorSlashingHistory","params":["0x655c9867d6B462aF9f5F984B127072E552405135"],"id":1}'
```

---

## 9 Other

### 32. dpos_getConsensusState

**Description**  
Get current consensus state (round, current block producer, etc.).

**RPC method**  
`dpos_getConsensusState`

**Request parameters**  
None. Pass `[]` or empty array.

**Response**

```json
{
  "success": true,
  "currentRound": 10,
  "currentDelegate": "0x1234567890123456789012345678901234567890",
  "currentDelegateIndex": 0
}
```

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getConsensusState","params":[],"id":1}'
```

---

### 33. dpos_getAccountBalance

**Description**  
Get account balance.

**RPC method**  
`dpos_getAccountBalance`

**Request parameters**

- Array: `[address]`
- String: `"0x..."`
- Object: `{ address: "0x..." }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| address | string | Yes | Account address |

**Response**

```json
{
  "address": "0x...",
  "balance": "1000000000000000000"
}
```

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getAccountBalance","params":["0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"],"id":1}'
```

---

### 34. dpos_getConsensusSwitchHeight

**Description**  
Get consensus switch height (block height where DPoS takes effect), current block height, and whether switch has occurred.

**RPC method**  
`dpos_getConsensusSwitchHeight`

**Request parameters**  
None. Pass `[]` or omit params.

**Response**

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

**Response fields**

| Field | Type | Description |
|-------|------|-------------|
| success | boolean | Whether the request succeeded |
| consensusSwitchHeight | number | Consensus switch block height (0 = not configured or unavailable) |
| switchBlockTimestamp | number | Timestamp of switch block |
| switchBlockHash | string | Hash of switch block |
| currentBlockHeight | number | Current block height |
| isDPoSActive | boolean | Whether chain is on DPoS (currentBlockHeight >= consensusSwitchHeight) |

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getConsensusSwitchHeight","params":[],"id":1}'
```

---

### 35. dpos_getValidatorsFromBlockExtraData

**Description**  
Parse and return the validator set from a block header's ExtraData (address, voting power, active status).

**RPC method**  
`dpos_getValidatorsFromBlockExtraData`

**Request parameters**

- Array: `[blockNumber]`
- Object: `{ blockNumber: number }`

| Name | Type | Required | Description |
|------|------|----------|-------------|
| blockNumber | number | Yes | Block number |

**Response**

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

**Error**  
Invalid params, block not found, or DPoS engine unavailable/not implemented: returned via JSON-RPC error, not in result.

**Example**

```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorsFromBlockExtraData","params":[12345],"id":1}'
```

---

## API index (35 endpoints)

| # | RPC method | Category |
|---|------------|----------|
| 1 | dpos_createParameterProposal | 1 Governance |
| 2 | dpos_createRecoveryProposal | 1 Governance |
| 3 | dpos_getParameterProposal | 1 Governance |
| 4 | dpos_voteOnParameterProposal | 1 Governance |
| 5 | dpos_getActiveProposals | 1 Governance |
| 6 | dpos_getVotableCurrentParameters | 1 Governance |
| 7 | dpos_executeParameterUpdate | 1 Governance |
| 8 | dpos_vote | 2 Voting |
| 9 | dpos_getVoteRecords | 2 Voting |
| 10 | dpos_getStakingInfo | 3 Staking |
| 11 | dpos_getVotingPower | 4 Validators |
| 12 | dpos_getValidatorVotingDetails | 4 Validators |
| 13 | dpos_registerDelegate | 4 Validators |
| 14 | dpos_getDelegateRegistrations | 4 Validators |
| 15 | dpos_withdrawDelegate | 4 Validators |
| 16 | dpos_canWithdrawDelegate | 4 Validators |
| 17 | dpos_getFreezeInfo | 4 Validators |
| 18 | dpos_getValidatorCommission | 4 Validators |
| 19 | dpos_updateCommission | 4 Validators |
| 20 | dpos_getVoterSlashingHistory | 4 Validators |
| 21 | dpos_getCurrentEpochInfo | 5 Epoch |
| 22 | dpos_getEpochInfoByNumber | 5 Epoch |
| 23 | dpos_getLatestEpochInfo | 5 Epoch |
| 24 | dpos_getEpochRewardDetails | 6 Rewards |
| 25 | dpos_getRewardHistory | 6 Rewards |
| 26 | dpos_getValidatorRewardsInfo | 6 Rewards |
| 27 | dpos_getEpochRangeRewardDetails | 6 Rewards |
| 28 | dpos_getVoterRewardByValidator | 6 Rewards |
| 29 | dpos_getBlockProducers | 7 Block producers |
| 30 | dpos_getValidatorBlockStats | 7 Block producers |
| 31 | dpos_getValidatorSlashingHistory | 8 Slashing |
| 32 | dpos_getConsensusState | 9 Other |
| 33 | dpos_getAccountBalance | 9 Other |
| 34 | dpos_getConsensusSwitchHeight | 9 Other |
| 35 | dpos_getValidatorsFromBlockExtraData | 9 Other |

The endpoint count matches the exported *DPOS methods in `jsonrpc/dpos_endpoint.go`, 35 in total.
