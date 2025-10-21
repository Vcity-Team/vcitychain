# DPoS 受托人注册 RPC API 使用说明

## 📋 **概述**

本文档详细介绍了DPoS系统中受托人注册功能的RPC API接口。该功能参考了TRON的SR（超级代表）注册机制，实现了去中心化的受托人注册和投票系统。

## 🏗️ **系统架构**

### **注册流程**
1. **注册申请**：用户支付保证金注册为受托人候选人
2. **社区投票**：社区成员对候选人进行投票
3. **自动激活**：根据投票排名自动激活前N名受托人
4. **动态调整**：定期更新活跃受托人列表

### **关键特性**
- ✅ **去中心化**：无需管理员审批，完全由社区投票决定
- ✅ **保证金机制**：防止垃圾注册，确保质量
- ✅ **自动激活**：基于投票排名自动激活受托人
- ✅ **可退还**：退出时可退还保证金
- ✅ **动态调整**：支持实时更新活跃受托人

## 🔧 **RPC 接口列表**

### **1. 注册受托人**

**接口名称**: `dpos_registerDelegate`

**功能描述**: 注册为受托人候选人

**请求参数**:
```json
{
  "jsonrpc": "2.0",
  "method": "dpos_registerDelegate",
  "params": {
    "registrant": "0x1234567890123456789012345678901234567890",
    "name": "My Delegate",
    "website": "https://mydelegate.com",
    "description": "A reliable delegate for the community"
  },
  "id": 1
}
```

**参数说明**:
- `registrant` (string): 注册者地址
- `name` (string): 受托人名称
- `website` (string): 官方网站
- `description` (string): 描述信息

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "success": true,
    "message": "Delegate registration submitted successfully"
  }
}
```

**错误示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32602,
    "message": "insufficient balance for delegate registration: required 100000000000000000000, available 50000000000000000000"
  }
}
```

### **2. 获取受托人注册列表**

**接口名称**: `dpos_getDelegateRegistrations`

**功能描述**: 获取所有受托人注册信息

**请求参数**:
```json
{
  "jsonrpc": "2.0",
  "method": "dpos_getDelegateRegistrations",
  "params": [],
  "id": 1
}
```

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": [
    {
      "address": "0x1234567890123456789012345678901234567890",
      "name": "My Delegate",
      "website": "https://mydelegate.com",
      "description": "A reliable delegate for the community",
      "deposit": "100000000000000000000",
      "status": "candidate",
      "createdAt": 1761015000,
      "totalVotes": "0",
      "isActive": false,
      "lastVoteTime": 0
    }
  ]
}
```

### **3. 更新活跃受托人**

**接口名称**: `dpos_updateActiveDelegates`

**功能描述**: 根据投票排名更新活跃受托人列表

**请求参数**:
```json
{
  "jsonrpc": "2.0",
  "method": "dpos_updateActiveDelegates",
  "params": [],
  "id": 1
}
```

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "success": true,
    "message": "Active delegates updated successfully"
  }
}
```

### **4. 退出受托人**

**接口名称**: `dpos_withdrawDelegate`

**功能描述**: 退出受托人（退还保证金）

**请求参数**:
```json
{
  "jsonrpc": "2.0",
  "method": "dpos_withdrawDelegate",
  "params": {
    "address": "0x1234567890123456789012345678901234567890"
  },
  "id": 1
}
```

**参数说明**:
- `address` (string): 受托人地址

**返回示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "success": true,
    "message": "Delegate withdrawn successfully"
  }
}
```

**错误示例**:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32602,
    "message": "cannot withdraw while having votes"
  }
}
```

## 📊 **数据结构说明**

### **DelegateRegistration 结构**
```json
{
  "address": "0x1234567890123456789012345678901234567890",
  "name": "My Delegate",
  "website": "https://mydelegate.com",
  "description": "A reliable delegate for the community",
  "deposit": "100000000000000000000",
  "status": "candidate",
  "createdAt": 1761015000,
  "totalVotes": "0",
  "isActive": false,
  "lastVoteTime": 0
}
```

**字段说明**:
- `address`: 受托人地址
- `name`: 受托人名称
- `website`: 官方网站
- `description`: 描述信息
- `deposit`: 保证金金额（wei）
- `status`: 注册状态（candidate/active/inactive/withdrawn）
- `createdAt`: 注册时间戳
- `totalVotes`: 总投票数
- `isActive`: 是否为活跃受托人
- `lastVoteTime`: 最后投票时间

### **状态说明**
- `candidate`: 候选人（可接受投票）
- `active`: 活跃受托人
- `inactive`: 非活跃状态
- `withdrawn`: 已退出

## 🚀 **完整使用流程**

### **步骤1: 注册受托人**
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_registerDelegate",
    "params": {
      "registrant": "0x1234567890123456789012345678901234567890",
      "name": "My Delegate",
      "website": "https://mydelegate.com",
      "description": "A reliable delegate for the community"
    },
    "id": 1
  }'
```

### **步骤2: 查看注册列表**
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getDelegateRegistrations",
    "params": [],
    "id": 1
  }'
```

### **步骤3: 社区投票**
```bash
# 投票给受托人
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_vote",
    "params": {
      "voter": "0xabcdef1234567890123456789012345678901234",
      "delegate": "0x1234567890123456789012345678901234567890",
      "amount": "1000000000000000000000"
    },
    "id": 1
  }'
```

### **步骤4: 更新活跃受托人**
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_updateActiveDelegates",
    "params": [],
    "id": 1
  }'
```

### **步骤5: 查看最终结果**
```bash
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_getDelegateRegistrations",
    "params": [],
    "id": 1
  }'
```

## ⚙️ **配置参数**

### **默认配置**
- **保证金金额**: 100 VCITY (100000000000000000000 wei)
- **最大活跃受托人**: 21个
- **投票权重**: 基于账户余额

### **可配置参数**
- `delegate_deposit_amount`: 受托人保证金金额
- `max_active_delegates`: 最大活跃受托人数量

## 🔒 **安全机制**

### **注册限制**
- 需要足够的VCITY余额支付保证金
- 每个地址只能注册一次
- 保证金在注册时冻结

### **投票限制**
- 只能对已注册的受托人投票
- 投票权重基于账户余额
- 支持投票转移和撤销

### **退出限制**
- 有投票时不能退出
- 退出时退还保证金
- 从受托人列表中移除

## 📈 **监控和统计**

### **关键指标**
- 总注册人数
- 活跃受托人数量
- 平均投票权重
- 注册成功率

### **日志记录**
- 注册事件
- 投票事件
- 状态变更事件
- 错误事件

## 🐛 **常见错误**

### **余额不足**
```json
{
  "error": {
    "code": -32602,
    "message": "insufficient balance for delegate registration: required 100000000000000000000, available 50000000000000000000"
  }
}
```

### **重复注册**
```json
{
  "error": {
    "code": -32602,
    "message": "delegate 0x1234567890123456789012345678901234567890 already registered"
  }
}
```

### **有投票时不能退出**
```json
{
  "error": {
    "code": -32602,
    "message": "cannot withdraw while having votes"
  }
}
```

## 🔄 **版本历史**

- **v1.0.0**: 初始版本，支持基本的受托人注册和投票
- **v1.1.0**: 添加保证金机制和自动激活功能
- **v1.2.0**: 优化投票权重计算和状态管理

## 📞 **技术支持**

如有问题，请联系开发团队或查看相关文档。

---

**注意**: 本文档基于当前实现版本，如有更新请参考最新文档。
