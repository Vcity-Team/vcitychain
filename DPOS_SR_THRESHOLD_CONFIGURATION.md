# DPoS SR候选人保证金配置说明

## 概述

现在受托人注册的保证金金额可以通过配置文件动态设置，不再硬编码为1000 VCITY。通过修改 `dpos_SR_threshold` 配置项，可以灵活调整SR候选人注册所需的保证金。

## 配置说明

### 配置文件位置
- 节点配置文件：`node-config-validator.yaml`
- 配置项：`dpos_SR_threshold`

### 配置格式
```yaml
dpos_SR_threshold: "0"  # 设置为0表示不需要保证金
# 或者
dpos_SR_threshold: "1000000000000000000000"  # 1000 VCITY (18个0)
```

### 配置值说明
- `"0"`: 不需要保证金，任何人都可以注册为SR候选人
- `"1000000000000000000000"`: 需要1000 VCITY作为保证金
- `"100000000000000000000"`: 需要100 VCITY作为保证金
- 其他值：根据实际需求设置

## 使用示例

### 1. 设置不需要保证金
```yaml
# node-config-validator.yaml
dpos_SR_threshold: "0"
```

**效果**：
- 任何人都可以免费注册为SR候选人
- 注册时不会检查余额
- 适合测试环境或低门槛网络

### 2. 设置1000 VCITY保证金
```yaml
# node-config-validator.yaml
dpos_SR_threshold: "1000000000000000000000"
```

**效果**：
- 注册SR候选人需要锁定1000 VCITY
- 注册时会检查账户余额
- 适合生产环境，防止恶意注册

### 3. 设置100 VCITY保证金
```yaml
# node-config-validator.yaml
dpos_SR_threshold: "100000000000000000000"
```

**效果**：
- 注册SR候选人需要锁定100 VCITY
- 比1000 VCITY门槛更低
- 适合中等门槛的网络

## 配置优先级

系统按以下优先级读取保证金配置：

1. **配置文件** (`dpos_SR_threshold`) - 最高优先级
2. **治理参数** (`delegate_deposit_amount`) - 中等优先级
3. **默认值** (100 VCITY) - 最低优先级

## 相关RPC命令

### 注册SR候选人
```bash
# 使用RPC命令注册
curl -X POST http://localhost:8545 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "dpos_registerDelegate",
    "params": {
      "address": "0x8dd5455146BA56205F0529B0f500d1b5cE76890B",
      "name": "MyDelegate",
      "website": "https://mydelegate.com",
      "description": "专业验证节点"
    },
    "id": 1
  }'
```

### 查询SR候选人列表
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

## 配置验证

启动节点时，系统会输出配置信息：

```
💰 设置SR候选人保证金阈值 threshold=0
```

或者

```
💰 使用默认SR候选人保证金阈值 threshold=0
```

## 注意事项

1. **配置生效**：修改配置后需要重启节点才能生效
2. **余额检查**：如果设置了非零保证金，注册时会检查账户余额
3. **治理修改**：保证金也可以通过治理提案动态修改
4. **单位说明**：配置值使用wei单位（1 VCITY = 10^18 wei）

## 常见问题

### Q: 为什么设置保证金为0？
A: 设置为0可以让任何人都能注册为SR候选人，适合测试环境或低门槛的去中心化网络。

### Q: 如何计算正确的保证金金额？
A: 使用公式：`保证金金额 × 10^18`，例如1000 VCITY = 1000000000000000000000

### Q: 配置不生效怎么办？
A: 确保配置文件路径正确，重启节点，并检查日志中的配置加载信息。

### Q: 可以通过治理修改保证金吗？
A: 是的，通过 `delegate_deposit_amount` 治理参数可以动态修改保证金要求。

## 技术实现

### 代码修改点
1. **DPoSConfig结构体**：添加 `SRThreshold *big.Int` 字段
2. **服务器配置**：添加 `DPoSSRThreshold string` 字段
3. **配置解析**：在 `StartDPoSEngine` 和 `setupConsensus` 中解析配置
4. **保证金获取**：修改 `getDelegateDepositAmount` 方法优先读取配置

### 配置流程
```
YAML配置 → 服务器解析 → DPoS引擎 → 注册验证
```

这个配置系统提供了灵活的SR候选人注册门槛控制，可以根据网络需求调整注册难度。
