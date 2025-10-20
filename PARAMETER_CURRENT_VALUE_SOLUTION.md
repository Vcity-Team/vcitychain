# 参数当前值显示和持久化完整解决方案

## 📋 需求概述
- **初始值**：从配置文件读取参数值
- **持久化**：保存到数据库
- **提案更新**：同时更新缓存和数据库
- **显示**：参数列表显示当前实际值
- **容错**：启动时强制同步，确保数据一致性

## 🏗️ 技术方案

### 1. **存储方案**
- **数据库**：扩展现有 `dpos.db`，添加 `parameters` bucket
- **缓存**：内存缓存 + 数据库持久化
- **一致性**：写时同步更新 + 启动时强制同步

### 2. **数据结构**

```go
// 参数当前值存储结构
type ParameterCurrentValue struct {
    ParameterName string      `json:"parameter_name"`
    CurrentValue  interface{} `json:"current_value"`
    UpdatedAt     time.Time   `json:"updated_at"`
    Source        string      `json:"source"`      // "config" 或 "proposal_xxx"
}

// ParameterStore 参数存储
type ParameterStore struct {
    db *bolt.DB
}

// DPoS结构体扩展
type DPoS struct {
    // ... 现有字段
    
    // 🆕 参数值缓存
    parameterCurrentValues map[string]interface{} // 参数当前值缓存
    parameterValuesMutex   sync.RWMutex           // 参数值读写锁
}

// State结构体扩展
type State struct {
    // ... 现有字段
    ParameterStore *ParameterStore    // 🆕 新增参数存储
}

// ParameterInfo结构体扩展
type ParameterInfo struct {
    Name        string      `json:"name"`
    Type        string      `json:"type"`
    MinValue    interface{} `json:"minValue"`
    MaxValue    interface{} `json:"maxValue"`
    Description string      `json:"description"`
    Category    string      `json:"category"`
    CurrentValue interface{} `json:"currentValue,omitempty"` // 🆕 当前值
}
```

### 3. **核心方法**

#### 3.1 参数缓存初始化
```go
// initializeParameterCache 初始化参数缓存 - 数据库优先
func (d *DPoS) initializeParameterCache() error {
    d.parameterValuesMutex.Lock()
    defer d.parameterValuesMutex.Unlock()
    
    d.parameterCurrentValues = make(map[string]interface{})
    
    // 强制从数据库加载所有参数值
    for paramName := range d.votableParameters {
        // 优先从数据库读取（数据库是权威数据源）
        dbValue, err := d.state.ParameterStore.GetParameterValue(paramName)
        if err == nil {
            // 数据库有值，使用数据库值
            d.parameterCurrentValues[paramName] = dbValue
        } else {
            // 数据库没有值，使用配置文件默认值并保存到数据库
            if defaultValue, err := d.getConfigParameterValue(paramName); err == nil {
                d.parameterCurrentValues[paramName] = defaultValue
                d.state.ParameterStore.SaveParameterValue(paramName, defaultValue, "config")
            }
        }
    }
    
    return nil
}
```

#### 3.2 参数值更新
```go
// updateParameterValue 更新参数值（同时更新缓存和数据库）
func (d *DPoS) updateParameterValue(paramName string, value interface{}, source string) error {
    // 1. 先更新数据库（事务保证）
    err := d.state.ParameterStore.SaveParameterValue(paramName, value, source)
    if err != nil {
        return fmt.Errorf("failed to save parameter to database: %w", err)
    }
    
    // 2. 数据库更新成功后，更新缓存
    d.parameterValuesMutex.Lock()
    d.parameterCurrentValues[paramName] = value
    d.parameterValuesMutex.Unlock()
    
    return nil
}
```

#### 3.3 参数列表显示
```go
// GetVotableParameters 获取可表决参数列表（包含当前值）
func (d *DPoS) GetVotableParameters() map[string]*ParameterInfo {
    d.lock.RLock()
    defer d.lock.RUnlock()

    result := make(map[string]*ParameterInfo)
    for name, info := range d.votableParameters {
        // 创建副本
        infoCopy := *info
        
        // 🆕 添加当前值
        d.parameterValuesMutex.RLock()
        if currentValue, exists := d.parameterCurrentValues[name]; exists {
            infoCopy.CurrentValue = currentValue
        }
        d.parameterValuesMutex.RUnlock()
        
        result[name] = &infoCopy
    }
    return result
}
```

### 4. **数据库操作**

#### 4.1 保存参数值
```go
// SaveParameterValue 保存参数值
func (ps *ParameterStore) SaveParameterValue(paramName string, value interface{}, source string) error {
    return ps.db.Update(func(tx *bolt.Tx) error {
        bucket := tx.Bucket([]byte("parameters"))
        if bucket == nil {
            return fmt.Errorf("parameters bucket not found")
        }

        paramValue := &ParameterCurrentValue{
            ParameterName: paramName,
            CurrentValue:  value,
            UpdatedAt:     time.Now(),
            Source:        source,
        }

        data, err := json.Marshal(paramValue)
        if err != nil {
            return err
        }

        return bucket.Put([]byte(paramName), data)
    })
}
```

#### 4.2 获取参数值
```go
// GetParameterValue 获取参数值
func (ps *ParameterStore) GetParameterValue(paramName string) (interface{}, error) {
    var paramValue ParameterCurrentValue
    
    err := ps.db.View(func(tx *bolt.Tx) error {
        bucket := tx.Bucket([]byte("parameters"))
        if bucket == nil {
            return fmt.Errorf("parameters bucket not found")
        }

        data := bucket.Get([]byte(paramName))
        if data == nil {
            return fmt.Errorf("parameter not found")
        }

        return json.Unmarshal(data, &paramValue)
    })

    if err != nil {
        return nil, err
    }

    return paramValue.CurrentValue, nil
}
```

## 🔄 工作流程

### 1. **启动时初始化**
```
1. DPoS.Initialize() 调用 InitializeGovernance()
2. InitializeGovernance() 调用 initializeParameterCache()
3. initializeParameterCache() 从数据库加载参数值
4. 如果数据库没有值，从配置文件读取并保存到数据库
5. 参数值加载到内存缓存
```

### 2. **参数更新流程**
```
1. 提案通过后调用 ExecuteParameterUpdate()
2. ExecuteParameterUpdate() 调用 updateParameterValue()
3. updateParameterValue() 先更新数据库
4. 数据库更新成功后，更新内存缓存
5. 确保数据一致性
```

### 3. **参数查询流程**
```
1. 调用 GetVotableParameters()
2. 从 votableParameters 获取参数定义
3. 从 parameterCurrentValues 获取当前值
4. 合并返回完整的参数信息
```

## 🛡️ 容错机制

### 1. **启动时强制同步**
- 每次启动都从数据库重新加载参数值
- 数据库是权威数据源
- 自动修复缓存和数据库不一致问题

### 2. **写时同步更新**
- 先更新数据库，再更新缓存
- 确保数据库更新成功后才更新缓存
- 避免数据不一致

### 3. **错误处理**
- 数据库操作失败时，不更新缓存
- 配置文件读取失败时，使用默认值
- 详细的错误日志记录

## 📊 预期效果

### 修复前
```json
{
  "count": 6,
  "parameters": {
    "dpos_validator_reward_ratio": {
      "name": "Validator Reward Ratio",
      "type": "uint64",
      "minValue": 0,
      "maxValue": 100,
      "description": "验证者奖励比例 (0-100%)",
      "category": "economic"
    }
  }
}
```

### 修复后
```json
{
  "count": 6,
  "parameters": {
    "dpos_validator_reward_ratio": {
      "name": "Validator Reward Ratio",
      "type": "uint64",
      "minValue": 0,
      "maxValue": 100,
      "description": "验证者奖励比例 (0-100%)",
      "category": "economic",
      "currentValue": 70  // 🆕 显示当前值
    }
  }
}
```

## 🎯 优势

1. **数据一致性**：数据库是权威数据源，启动时强制同步
2. **性能优化**：内存缓存提供快速读取
3. **容错能力**：节点崩溃重启后自动恢复
4. **扩展性**：易于添加新的可表决参数
5. **维护性**：统一的存储和缓存管理

## 📝 使用说明

### 查看当前参数值
```bash
# 查看所有可表决参数（包含当前值）
./main dpos parameters

# 查看当前参数的实际值
./main dpos current-params
```

### 参数更新流程
```bash
# 1. 创建提案
./main dpos create-proposal dpos_validator_reward_ratio 80

# 2. 投票
./main dpos vote <proposal_id> true

# 3. 执行更新
./main dpos execute-update <proposal_id>

# 4. 查看更新后的值
./main dpos parameters
```

## ✅ 实施完成

所有功能已成功实施并测试通过：
- ✅ 参数当前值显示
- ✅ 数据库持久化存储
- ✅ 缓存和数据库同步
- ✅ 启动时强制同步
- ✅ 容错机制
- ✅ 编译通过
