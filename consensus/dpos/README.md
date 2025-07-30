
# DPoS (Delegated Proof of Stake) 共识模块

## 概述

DPoS是一种高效的共识机制，通过委托投票的方式选择区块生产者。本模块实现了完整的DPoS功能，包括投票、受托人管理、奖励分配等核心特性。

## 特性

### ✅ 核心功能
- **投票机制**: 支持用户投票给受托人
- **受托人管理**: 自动选举和管理受托人集合
- **奖励分配**: 基于投票权重的奖励计算
- **区块生产**: 受托人轮流出块

### ✅ 生产级特性
- **安全性**: 签名验证、防重放攻击、参数边界检查
- **性能优化**: 批量处理、内存缓存、并发处理
- **监控指标**: Prometheus指标、健康检查、性能统计
- **配置管理**: 文件配置、环境变量、配置验证
- **测试覆盖**: 单元测试、集成测试、性能测试

## 快速开始

### 1. 安装依赖

```bash
go mod tidy
```

### 2. 基础配置

创建配置文件 `dpos-config.json`:

```json
{
  "block_time": "15s",
  "round_time": "30s",
  "delegate_count": 21,
  "epoch_length": 100,
  "min_vote_amount": "1000000000000000000",
  "max_vote_amount": "100000000000000000000000",
  "vote_lock_time": "24h",
  "block_reward": "1000000000000000000",
  "epoch_reward": "100000000000000000000",
  "reward_decay": 0.95,
  "batch_size": 100,
  "batch_timeout": "100ms",
  "worker_count": 4,
  "cache_ttl": "5m",
  "metrics_port": 9090,
  "enable_metrics": true,
  "max_voting_power": "1000000000000000000000000",
  "max_delegates": 100,
  "signature_verify": true,
  "db_path": "./dpos.db",
  "db_timeout": "5s",
  "initial_validators": [
    {
      "address": "0x1234567890123456789012345678901234567890",
      "voting_power": "1000000000000000000000",
      "is_active": true
    }
  ]
}
```

### 3. 环境变量配置

```bash
export DPOS_BLOCK_TIME="15s"
export DPOS_DELEGATE_COUNT="21"
export DPOS_METRICS_PORT="9090"
export DPOS_ENABLE_METRICS="true"
export DPOS_DB_PATH="./dpos.db"
```

### 4. 启动DPoS

```go
package main

import (
    "log"
    "github.com/Vcity-Team/vcitychain/consensus/dpos"
)

func main() {
    // 加载配置
    config, err := dpos.LoadConfigFromFile("dpos-config.json")
    if err != nil {
        log.Fatal("Failed to load config:", err)
    }
    
    // 创建DPoS实例
    dposInstance := dpos.NewDPoS(config)
    
    // 启动DPoS
    if err := dposInstance.Start(); err != nil {
        log.Fatal("Failed to start DPoS:", err)
    }
    
    // 等待信号
    select {}
}
```

## 配置说明

### 基础配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `block_time` | duration | 15s | 区块生产间隔 |
| `round_time` | duration | 30s | 轮次时间 |
| `delegate_count` | uint64 | 21 | 受托人数量 |
| `epoch_length` | uint64 | 100 | 周期长度 |

### 投票配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `min_vote_amount` | big.Int | 1 token | 最小投票金额 |
| `max_vote_amount` | big.Int | 100K tokens | 最大投票金额 |
| `vote_lock_time` | duration | 24h | 投票锁定时间 |

### 奖励配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `block_reward` | big.Int | 1 token | 区块奖励 |
| `epoch_reward` | big.Int | 100 tokens | 周期奖励 |
| `reward_decay` | float64 | 0.95 | 奖励衰减系数 |

### 性能配置

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `batch_size` | int | 100 | 批量处理大小 |
| `batch_timeout` | duration | 100ms | 批量处理超时 |
| `worker_count` | int | 4 | 工作协程数量 |
| `cache_ttl` | duration | 5m | 缓存过期时间 |

## 监控与运维

### 1. Prometheus指标

DPoS提供以下Prometheus指标：

- `dpos_total_votes`: 总投票数
- `dpos_total_delegates`: 受托人数量
- `dpos_active_voters`: 活跃投票者数量
- `dpos_block_rewards`: 区块奖励总额
- `dpos_block_time_seconds`: 区块生产时间
- `dpos_vote_processing_time_seconds`: 投票处理时间
- `dpos_delegate_update_time_seconds`: 受托人更新时间

### 2. 健康检查

```bash
# 健康检查
curl http://localhost:9090/health

# 统计信息
curl http://localhost:9090/dpos/stats

# Prometheus指标
curl http://localhost:9090/metrics
```

### 3. 日志监控

DPoS使用结构化日志，包含以下字段：

- `level`: 日志级别 (debug, info, warn, error)
- `msg`: 日志消息
- `voter`: 投票者地址
- `delegate`: 受托人地址
- `amount`: 投票金额
- `block`: 区块号
- `round`: 轮次号

### 4. 性能监控

```bash
# 运行性能测试
go test -bench=. -benchmem ./consensus/dpos/

# 监控内存使用
go tool pprof http://localhost:9090/debug/pprof/heap

# 监控CPU使用
go tool pprof http://localhost:9090/debug/pprof/profile
```

## 故障排查

### 常见问题

#### 1. 投票失败

**症状**: 投票交易被拒绝

**可能原因**:
- 投票金额超出限制
- 受托人不存在或不活跃
- 投票者被锁定
- 签名验证失败

**解决方案**:
```bash
# 检查投票者状态
curl -X POST http://localhost:9090/dpos/voter/0x123... -d '{"address":"0x123..."}'

# 检查受托人状态
curl -X POST http://localhost:9090/dpos/delegate/0x456... -d '{"address":"0x456..."}'
```

#### 2. 区块生产延迟

**症状**: 区块生产时间超过预期

**可能原因**:
- 网络延迟
- 受托人离线
- 系统负载过高

**解决方案**:
```bash
# 检查受托人活跃状态
curl http://localhost:9090/dpos/stats

# 检查系统资源
top -p $(pgrep dpos)

# 检查网络连接
ping delegate-node.example.com
```

#### 3. 内存使用过高

**症状**: 内存使用持续增长

**可能原因**:
- 缓存未及时清理
- 内存泄漏
- 投票数据过多

**解决方案**:
```bash
# 调整缓存TTL
export DPOS_CACHE_TTL="2m"

# 重启服务
systemctl restart dpos

# 分析内存使用
go tool pprof http://localhost:9090/debug/pprof/heap
```

### 日志分析

#### 错误日志示例

```
{"level":"error","msg":"vote validation failed","error":"vote amount exceeds maximum","voter":"0x123...","amount":"1000000000000000000000000"}
```

#### 警告日志示例

```
{"level":"warn","msg":"delegate deactivated due to insufficient stake","address":"0x456...","stake":"500000000000000000"}
```

#### 信息日志示例

```
{"level":"info","msg":"vote processed successfully","voter":"0x123...","delegate":"0x456...","amount":"1000000000000000000","round":100}
```

## 安全建议

### 1. 网络安全

- 使用HTTPS进行API通信
- 配置防火墙规则
- 启用DDoS防护

### 2. 密钥管理

- 使用硬件安全模块(HSM)
- 定期轮换密钥
- 备份密钥到安全位置

### 3. 访问控制

- 限制API访问权限
- 使用API密钥认证
- 监控异常访问

### 4. 数据安全

- 加密敏感数据
- 定期备份数据库
- 监控数据完整性

## 性能优化

### 1. 系统调优

```bash
# 增加文件描述符限制
echo "* soft nofile 65536" >> /etc/security/limits.conf
echo "* hard nofile 65536" >> /etc/security/limits.conf

# 调整内核参数
echo "net.core.somaxconn = 65535" >> /etc/sysctl.conf
echo "net.ipv4.tcp_max_syn_backlog = 65535" >> /etc/sysctl.conf
sysctl -p
```

### 2. 数据库优化

```bash
# 调整BoltDB参数
export DPOS_DB_TIMEOUT="10s"
export DPOS_BATCH_SIZE="200"
```

### 3. 缓存优化

```bash
# 增加缓存大小
export DPOS_CACHE_TTL="10m"
export DPOS_WORKER_COUNT="8"
```

## 部署指南

### Docker部署

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o dpos ./cmd/dpos

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/dpos .
COPY --from=builder /app/dpos-config.json .
EXPOSE 9090
CMD ["./dpos"]
```

```bash
# 构建镜像
docker build -t dpos:latest .

# 运行容器
docker run -d \
  --name dpos \
  -p 9090:9090 \
  -v /data/dpos:/root/data \
  dpos:latest
```

### Kubernetes部署

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dpos
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dpos
  template:
    metadata:
      labels:
        app: dpos
    spec:
      containers:
      - name: dpos
        image: dpos:latest
        ports:
        - containerPort: 9090
        volumeMounts:
        - name: dpos-data
          mountPath: /root/data
        env:
        - name: DPOS_METRICS_PORT
          value: "9090"
        - name: DPOS_DB_PATH
          value: "/root/data/dpos.db"
      volumes:
      - name: dpos-data
        persistentVolumeClaim:
          claimName: dpos-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: dpos-service
spec:
  selector:
    app: dpos
  ports:
  - port: 9090
    targetPort: 9090
  type: ClusterIP
```

## 测试

### 运行测试

```bash
# 运行所有测试
go test ./consensus/dpos/...

# 运行特定测试
go test -v -run TestVoteProcessing ./consensus/dpos/

# 运行性能测试
go test -bench=. -benchmem ./consensus/dpos/

# 运行集成测试
go test -v -run TestIntegration ./consensus/dpos/
```

### 测试覆盖率

```bash
# 生成覆盖率报告
go test -coverprofile=coverage.out ./consensus/dpos/

# 查看覆盖率报告
go tool cover -html=coverage.out -o coverage.html
```

## 贡献指南

### 开发环境设置

1. Fork项目
2. 创建功能分支
3. 编写代码和测试
4. 运行测试套件
5. 提交Pull Request

### 代码规范

- 遵循Go代码规范
- 添加适当的注释
- 编写单元测试
- 更新文档

### 提交规范

```
feat: 添加新功能
fix: 修复bug
docs: 更新文档
style: 代码格式调整
refactor: 代码重构
test: 添加测试
chore: 构建过程或辅助工具的变动
```

## 许可证

本项目采用MIT许可证，详见LICENSE文件。
