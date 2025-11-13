# 投票者奖励精度问题分析

## 问题

如果投票者占投票奖池比重 100%，为什么奖励是 29.65 VCITY 而不是 30 VCITY？

## 配置信息

```
dpos_reward_amount: 100 VCITY = 100000000000000000000 Wei
dpos_voter_reward_ratio: 30%
```

## 理论计算

### 投票者奖励池计算

```go
voterReward = rewardAmount * voterRatio / 100
            = 100000000000000000000 * 30 / 100
            = 30000000000000000000 Wei
            = 30 VCITY
```

### 单个投票者奖励计算

```go
reward = voterReward * voter.VotingPower / totalVotingPower
```

如果投票者占 100%：
```
reward = 30000000000000000000 * 1 / 1
       = 30000000000000000000 Wei
       = 30 VCITY
```

## 实际观察

- **理论值**：30 VCITY = 30000000000000000000 Wei
- **实际值**：29.65 VCITY = 29651162790697674418 Wei
- **差值**：348837209302325582 Wei ≈ 0.3488 VCITY

## 可能的原因

### 原因1：存在其他投票者（最可能）

如果存在其他投票者，即使占比很小，也会分走部分奖励。

**示例计算**：
- 假设总投票权重 = 1000000000000000000000（1000 VCITY）
- 该投票者权重 = 988300000000000000000（988.3 VCITY，占 98.83%）
- 其他投票者权重 = 11700000000000000000（11.7 VCITY，占 1.17%）

计算：
```
该投票者奖励 = 30000000000000000000 * 988300000000000000000 / 1000000000000000000000
            = 29649000000000000000 Wei
            = 29.649 VCITY
```

**接近实际值 29.65 VCITY！**

### 原因2：整数除法精度损失

整数除法会丢失小数部分，导致精度损失。

**示例**：
```
如果 voterReward = 29999999999999999999（由于前面的计算精度损失）
那么即使占100%，也只能得到 29999999999999999999
```

### 原因3：奖励池计算时的精度损失

在计算投票者奖励池时，可能已经存在精度损失：

```go
voterReward = rewardAmount * voterRatio / 100
```

如果 `rewardAmount` 不是精确的 100 VCITY，或者计算过程中有精度损失，会导致 `voterReward` 略小于 30 VCITY。

## 验证方法

### 1. 查询所有投票者的投票权重

```powershell
# 查询所有投票者信息，确认是否有其他投票者
# 需要查看 totalVotingPower 和该投票者的 VotingPower
```

### 2. 检查投票者奖励池的精确值

```powershell
# 查询奖励计算日志，确认 voterReward 的精确值
# 检查是否是 30000000000000000000 Wei
```

### 3. 计算精度损失

```powershell
# 计算差值
$theoretical = 30000000000000000000
$actual = 29651162790697674418
$diff = $theoretical - $actual
Write-Host "差值: $diff Wei = $($diff / [bigint]::Pow(10,18)) VCITY" -ForegroundColor Yellow
```

## 结论

**最可能的原因**：存在其他投票者，虽然占比很小（约 1.17%），但分走了约 0.35 VCITY 的奖励。

**验证方法**：
1. 查询该投票者的实际投票权重
2. 查询总投票权重
3. 计算占比：投票者权重 / 总投票权重
4. 如果占比 < 100%，说明存在其他投票者

## 代码逻辑检查

从 `reward_distributor.go` 的代码看：

```go
// 计算投票者奖励池
voterReward = rewardAmount * voterRatio / 100  // 应该是 30 VCITY

// 计算单个投票者奖励
reward = voterReward * voter.VotingPower / totalVotingPower
```

**如果该投票者占 100%**：
- `voter.VotingPower == totalVotingPower`
- `reward = voterReward * 1 = voterReward = 30 VCITY`

**如果该投票者占 98.83%**：
- `reward = voterReward * 0.9883 ≈ 29.65 VCITY` ✅ **这正好匹配！**

**结论**：该投票者实际上占约 98.83%，而不是 100%，所以奖励是 29.65 VCITY 而不是 30 VCITY。

## 查询指定 Epoch 信息的 PowerShell 命令

### 基本用法

```powershell
# 查询指定 epoch 的信息（例如 epoch 368）
$rpcUrl = "http://209.53.43.252:9545"
$epochNumber = 368
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getEpochInfoByNumber";params=@($epochNumber);id=1}|ConvertTo-Json) -ContentType "application/json"

# 显示结果
$r.result | ConvertTo-Json -Depth 10
```

### 格式化显示

```powershell
# 查询指定 epoch 的信息并格式化显示
$rpcUrl = "http://209.53.43.252:9545"
$epochNumber = 368

$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getEpochInfoByNumber";params=@($epochNumber);id=1}|ConvertTo-Json) -ContentType "application/json"

Write-Host "`n========== Epoch $epochNumber 信息 ==========" -ForegroundColor Green
Write-Host "Epoch 编号: $($r.result.epochNumber)" -ForegroundColor Yellow
Write-Host "Epoch 状态: $($r.result.epochStatus)" -ForegroundColor Yellow
Write-Host "Epoch 大小: $($r.result.epochSize) 个区块" -ForegroundColor Yellow
Write-Host "第一个区块: $($r.result.firstBlockInEpoch)" -ForegroundColor Cyan
Write-Host "最后一个区块: $($r.result.lastBlockInEpoch)" -ForegroundColor Cyan
Write-Host "验证者数量: $($r.result.validators.Count)" -ForegroundColor Cyan
```

### 查询多个 Epoch

```powershell
# 查询多个 epoch 的信息
$rpcUrl = "http://209.53.43.252:9545"
$epochNumbers = @(365, 366, 367, 368)

foreach ($epochNumber in $epochNumbers) {
    Write-Host "`n========== 查询 Epoch $epochNumber ==========" -ForegroundColor Green
    $r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getEpochInfoByNumber";params=@($epochNumber);id=1}|ConvertTo-Json) -ContentType "application/json"
    
    if ($r.result) {
        Write-Host "Epoch $epochNumber: 状态=$($r.result.epochStatus), 区块范围=$($r.result.firstBlockInEpoch)-$($r.result.lastBlockInEpoch), 验证者数=$($r.result.validators.Count)" -ForegroundColor Cyan
    } else {
        Write-Host "Epoch $epochNumber: 查询失败" -ForegroundColor Red
    }
}
```

### 查询当前 Epoch（使用 dpos_getCurrentEpochInfo）

```powershell
# 查询当前 epoch 信息（不需要参数）
$rpcUrl = "http://209.53.43.252:9545"
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getCurrentEpochInfo";params=@();id=1}|ConvertTo-Json) -ContentType "application/json"

Write-Host "`n========== 当前 Epoch 信息 ==========" -ForegroundColor Green
Write-Host "当前 Epoch: $($r.result.epochNumber)" -ForegroundColor Yellow
Write-Host "Epoch 大小: $($r.result.epochSize) 个区块" -ForegroundColor Yellow
Write-Host "验证者数量: $($r.result.validators.Count)" -ForegroundColor Cyan
```

