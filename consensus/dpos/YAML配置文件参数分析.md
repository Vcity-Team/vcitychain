# YAML配置文件参数分析

## 当前配置文件状态

**文件**：`c:\work\nodes-bk\node1\node-config-validator.yaml`

**当前tx_pool配置**（第31-33行）：
```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 4096
  # max_account_enqueued 缺失（使用默认值128）
```

## 配置结构体支持

**文件**：`command/server/config/config.go`

**位置**：第87-92行
```go
// TxPool defines the TxPool configuration params
type TxPool struct {
    PriceLimit         uint64 `json:"price_limit" yaml:"price_limit"`
    MaxSlots           uint64 `json:"max_slots" yaml:"max_slots"`
    MaxAccountEnqueued uint64 `json:"max_account_enqueued" yaml:"max_account_enqueued"`
}
```

**结论**：
- ✅ `max_slots` 已支持（yaml标签：`max_slots`）
- ✅ `max_account_enqueued` 已支持（yaml标签：`max_account_enqueued`）

## 配置解析流程

### 1. 配置文件读取

**文件**：`command/server/config/config.go`

**位置**：第186-212行
```go
func ReadConfigFile(path string) (*Config, error) {
    // ...
    case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
        unmarshalFunc = yaml.Unmarshal
    // ...
    if err := unmarshalFunc(data, config); err != nil {
        return nil, err
    }
    return config, nil
}
```

**说明**：使用 `gopkg.in/yaml.v3` 库解析YAML文件，会自动将YAML字段映射到结构体字段。

### 2. 参数传递流程

```
YAML配置文件
  ↓
command/server/config/config.go:ReadConfigFile()
  ↓ 解析到 config.TxPool.MaxSlots 和 config.TxPool.MaxAccountEnqueued
command/server/params.go (第224-225行)
  ↓ 传递到 server.Config
server/server.go (第586-588行)
  ↓ 传递到 txpool.Config
txpool/txpool.go (第216和221行)
  ↓ 实际使用
```

## 需要修改的内容

### 当前配置（第31-33行）

```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 4096
```

### 修改后配置

```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 16384                    # 从4096改为16384（4倍）
  max_account_enqueued: 1024          # 新增：从默认128改为1024（8倍）
```

## 修改位置分析

### 修改点1：max_slots

**位置**：第33行
- **当前值**：`4096`
- **修改为**：`16384`
- **说明**：已有字段，只需修改值

### 修改点2：max_account_enqueued

**位置**：第34行（新增）
- **当前状态**：缺失（使用默认值128）
- **修改为**：`1024`
- **说明**：新增字段，需要添加到tx_pool部分

## YAML格式要求

### 正确的YAML格式

```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 16384
  max_account_enqueued: 1024
```

### 注意事项

1. **缩进**：使用2个空格缩进（与现有格式一致）
2. **字段名**：使用下划线 `max_account_enqueued`（不是 `maxAccountEnqueued`）
3. **值类型**：整数，不需要引号

## 验证配置是否生效

### 方法1：启动时查看日志

启动节点时，应该会显示交易池配置信息，检查：
- `max_slots` 是否为 16384
- `max_account_enqueued` 是否为 1024

### 方法2：测试压测

使用修改后的配置进行压测：
- 如果之前发送128+个交易就报错，现在应该能发送1024+个交易
- 如果之前发送超过4096个slot就报错，现在应该能发送16384+个slot

### 方法3：RPC接口查询

```bash
curl -X POST http://127.0.0.1:8545 \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"txpool_status","params":[],"id":1}'
```

## 配置优先级

### 优先级顺序（从高到低）

1. **命令行参数**：`--max-enqueued 1024` （最高优先级）
2. **YAML配置文件**：`max_account_enqueued: 1024`
3. **默认值**：128（最低优先级）

**说明**：
- 如果同时使用命令行参数和YAML配置，命令行参数会覆盖YAML配置
- 如果YAML配置文件中没有某个字段，会使用默认值

## 完整修改示例

### 修改前（第31-33行）

```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 4096
```

### 修改后（第31-34行）

```yaml
tx_pool:
  price_limit: 1000000000
  max_slots: 16384                    # 🆕 从4096改为16384（对标TRON）
  max_account_enqueued: 1024          # 🆕 新增：从默认128改为1024（对标TRON）
```

## 配置文件完整示例（修改后）

```yaml
dpos_validators_count: 4
backup_validators_count: 10
max_missed_blocks: 25
base_fee_config: "1000000000:2:8"  # baseFee:baseFeeEM:baseFeeChangeDenom
burn_contract: "0:0x0000000000000000000000000000000000000000"
dpos_proposal_vote_period: "2m"  # 表决期2分钟
dpos_proposal_valid_period: "1d"  # 有效期1天
dpos_delegate_threshold: "1000000000000000000000"  # 1000 VCITY
dpos_epoch_duration: "300s"  
dpos_reward_distribution: "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe"  # 奖励分发账户
dpos_reward_amount: "150000000000000000000"  # 每个epoch 150VCITY
dpos_validator_reward_ratio: 70  # 验证者奖励比例 70%
dpos_voter_reward_ratio: 30      # 投票者奖励比例 30%
chain_config: ./genesis.json
secrets_config: ""
data_dir: "./node1"
block_gas_target: "0x3938700"
grpc_addr: "0.0.0.0:9632"
jsonrpc_addr: "0.0.0.0:8545"
telemetry:
  prometheus_addr: "0.0.0.0:5001"
network:
  no_discover: false
  libp2p_addr: 0.0.0.0:1480
  nat_addr: ""
  dns_addr: ""
  max_peers: 100
  max_outbound_peers: 50
  max_inbound_peers: -1
seal: true
tx_pool:
  price_limit: 1000000000
  max_slots: 16384                    # 🆕 从4096改为16384（对标TRON）
  max_account_enqueued: 1024          # 🆕 新增：从默认128改为1024（对标TRON）
log_level: INFO
restore_file: ""
block_time_s: 3
headers:
  access_control_allow_origins:
    - '*'
```

## 修改总结

### 需要修改的2个地方

1. **第33行**：`max_slots: 4096` → `max_slots: 16384`
2. **第34行**：新增 `max_account_enqueued: 1024`

### 修改原因

- **max_slots: 16384**：对标TRON，支持更大的交易池容量
- **max_account_enqueued: 1024**：对标TRON，解决压测时的队列满问题

### 配置生效确认

修改后，重启节点，配置会自动生效。如果同时使用命令行参数，命令行参数会覆盖YAML配置。

