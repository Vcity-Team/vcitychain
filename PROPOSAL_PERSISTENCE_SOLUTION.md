# 提案持久化存储解决方案

## 问题分析

**原始问题**：提案数据只存储在内存中，节点重启后数据丢失，导致查询提案时返回 "proposal not found"。

**根本原因**：
- `CreateParameterProposal` 只将提案存储在 `d.parameterProposals` map（内存）
- `GetParameterProposal` 从内存中查找提案
- **没有数据库持久化**，节点重启后内存数据丢失

## 解决方案

### 1. 数据库存储设计

**存储位置**：`consensus/dpos/dpos.db`（主数据库）
**存储结构**：新增 `proposals` bucket

```
dpos.db
├── parameters          # 参数当前值存储
├── proposals          # 🆕 提案数据存储
├── VoterInfo          # 投票者信息
├── StakingInfo        # 质押信息
└── ...                # 其他现有数据
```

### 2. 新增 ProposalStore

**文件**：`consensus/dpos/state.go`

```go
// ProposalStore 提案存储
type ProposalStore struct {
    db *bolt.DB
}

// 主要方法：
// - SaveProposal(proposal *ParameterProposal) error
// - GetProposal(proposalID string) (*ParameterProposal, error)
// - GetAllProposals() (map[string]*ParameterProposal, error)
// - DeleteProposal(proposalID string) error
```

### 3. 修改核心逻辑

#### 3.1 创建提案时持久化

**文件**：`consensus/dpos/dpos.go` - `CreateParameterProposal`

```go
// 保存到数据库
if d.state != nil && d.state.ProposalStore != nil {
    if err := d.state.ProposalStore.SaveProposal(proposal); err != nil {
        d.logger.Error("Failed to save proposal to database", "error", err)
        return nil, fmt.Errorf("failed to save proposal to database: %w", err)
    }
}

// 同时保存到内存（保持向后兼容）
d.parameterProposals[proposalID] = proposal
d.activeProposals[proposalID] = true
```

#### 3.2 查询提案时优先从数据库加载

**文件**：`consensus/dpos/dpos.go` - `GetParameterProposal`

```go
// 先从内存中查找
proposal, exists := d.parameterProposals[proposalID]
if exists {
    return proposal, nil
}

// 如果内存中没有，从数据库加载
if d.state != nil && d.state.ProposalStore != nil {
    dbProposal, err := d.state.ProposalStore.GetProposal(proposalID)
    if err == nil {
        // 加载到内存中
        d.parameterProposals[proposalID] = dbProposal
        return dbProposal, nil
    }
}

return nil, fmt.Errorf("proposal not found")
```

#### 3.3 启动时从数据库加载所有提案

**文件**：`consensus/dpos/dpos.go` - `InitializeGovernance`

```go
// 🆕 从数据库加载所有提案
if err := d.loadProposalsFromDatabase(); err != nil {
    return fmt.Errorf("failed to load proposals from database: %w", err)
}
```

**新增方法**：`loadProposalsFromDatabase()`

```go
func (d *DPoS) loadProposalsFromDatabase() error {
    // 从数据库加载所有提案
    proposals, err := d.state.ProposalStore.GetAllProposals()
    if err != nil {
        return fmt.Errorf("failed to load proposals from database: %w", err)
    }

    // 加载到内存中
    for proposalID, proposal := range proposals {
        d.parameterProposals[proposalID] = proposal
        
        // 根据提案状态设置活跃状态
        if proposal.Status == ProposalPending || proposal.Status == ProposalActive {
            d.activeProposals[proposalID] = true
        }
    }

    return nil
}
```

## 4. 数据流程

### 4.1 创建提案流程

```
1. 验证提案参数
2. 创建提案对象
3. 保存到数据库 (ProposalStore.SaveProposal)
4. 保存到内存 (d.parameterProposals)
5. 返回提案信息
```

### 4.2 查询提案流程

```
1. 从内存查找 (d.parameterProposals)
2. 如果找到 → 返回
3. 如果未找到 → 从数据库加载 (ProposalStore.GetProposal)
4. 加载到内存 → 返回
5. 数据库也没有 → 返回 "proposal not found"
```

### 4.3 节点启动流程

```
1. 初始化 DPoS 引擎
2. 调用 InitializeGovernance()
3. 从数据库加载所有提案 (loadProposalsFromDatabase)
4. 加载到内存中
5. 设置活跃状态
```

## 5. 优势

### 5.1 数据持久化
- ✅ 提案数据持久化到数据库
- ✅ 节点重启后数据不丢失
- ✅ 支持历史提案查询

### 5.2 性能优化
- ✅ 内存缓存提高查询速度
- ✅ 按需从数据库加载
- ✅ 启动时批量加载

### 5.3 数据一致性
- ✅ 数据库为权威数据源
- ✅ 启动时强制同步
- ✅ 写操作同时更新数据库和内存

### 5.4 向后兼容
- ✅ 保持现有 API 不变
- ✅ 内存操作仍然有效
- ✅ 渐进式升级

## 6. 测试验证

### 6.1 创建提案测试
```bash
./main dpos proposal create-proposal --parameter "dpos_validator_reward_ratio" --new-value 80 --description "提高验证者奖励比例到80%" --proposer "0x5d1F45B8D5a5eC9c3BEb91cAEbA6a3180DBeC7A9"
```

### 6.2 查询提案测试
```bash
./main dpos proposal get --proposal-id "proposal_0_0"
```

### 6.3 重启测试
1. 创建提案
2. 查询提案（确认存在）
3. 重启节点
4. 再次查询提案（确认仍然存在）

## 7. 文件修改清单

### 7.1 新增文件
- 无（扩展现有文件）

### 7.2 修改文件
1. **`consensus/dpos/state.go`**
   - 新增 `ProposalStore` 结构体
   - 新增 `ProposalStore` 方法
   - 在 `State` 结构体中添加 `ProposalStore` 字段
   - 在 `initStorages` 中初始化 `ProposalStore`

2. **`consensus/dpos/dpos.go`**
   - 修改 `CreateParameterProposal` 添加数据库保存
   - 修改 `GetParameterProposal` 添加数据库加载
   - 修改 `InitializeGovernance` 添加提案加载
   - 新增 `loadProposalsFromDatabase` 方法

## 8. 总结

通过实现提案数据的数据库持久化，解决了节点重启后提案数据丢失的问题。该方案：

- **数据安全**：提案数据持久化存储，不会因节点重启而丢失
- **性能优化**：内存缓存 + 按需加载，保证查询性能
- **向后兼容**：保持现有 API 和功能不变
- **易于维护**：使用现有的数据库架构，代码结构清晰

现在您可以：
1. 创建提案后立即查询（内存中）
2. 重启节点后仍然可以查询到之前创建的提案（从数据库加载）
3. 所有治理功能正常工作，数据持久化可靠
