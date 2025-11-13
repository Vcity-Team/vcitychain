# 奖励分配调试命令

## 问题
投票者奖励（266.86 VCITY）比出块者奖励（17.5 VCITY）高很多。

## 调试步骤

### 1. 检查单个 Epoch 的奖励（不是累计）

```powershell
# 查询单个 epoch 的奖励（例如 epoch 368）
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$epoch = 368
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="dpos_getEpochRewardDetails";params=@($epoch);id=1}|ConvertTo-Json) -ContentType "application/json"
$r.result | Where-Object { $_.recipient -eq $addr } | Format-Table epochNumber,recipient,rewardType,amount,blockCount -AutoSize
```

### 2. 检查该地址的投票信息

```powershell
# 查询地址的投票信息
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="dpos_getVoterInfo";params=@($addr);id=1}|ConvertTo-Json) -ContentType "application/json"
$r.result | ConvertTo-Json -Depth 10
```

### 3. 检查整个 Epoch 的奖励分配

```powershell
# 查询 epoch 368 的所有奖励记录
$epoch = 368
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="dpos_getEpochRewardDetails";params=@($epoch);id=1}|ConvertTo-Json) -ContentType "application/json"
Write-Host "Epoch $epoch 奖励分配:" -ForegroundColor Green
Write-Host "总记录数: $($r.result.Count)" -ForegroundColor Yellow
$validatorRewards = $r.result | Where-Object { $_.rewardType -eq "validator" -or $_.rewardType -eq "validator+voter" }
$voterRewards = $r.result | Where-Object { $_.rewardType -eq "voter" }
Write-Host "验证者奖励记录: $($validatorRewards.Count)" -ForegroundColor Cyan
Write-Host "投票者奖励记录: $($voterRewards.Count)" -ForegroundColor Cyan

# 计算总奖励
$totalValidatorReward = ($validatorRewards | ForEach-Object { [bigint]::Parse($_.amount) } | Measure-Object -Sum).Sum
$totalVoterReward = ($voterRewards | ForEach-Object { [bigint]::Parse($_.amount) } | Measure-Object -Sum).Sum
$totalReward = $totalValidatorReward + $totalVoterReward

Write-Host "`n总验证者奖励: $($totalValidatorReward / [bigint]::Pow(10,18)) VCITY" -ForegroundColor Green
Write-Host "总投票者奖励: $($totalVoterReward / [bigint]::Pow(10,18)) VCITY" -ForegroundColor Green
Write-Host "总奖励: $($totalReward / [bigint]::Pow(10,18)) VCITY" -ForegroundColor Yellow
Write-Host "预期总奖励: 150 VCITY" -ForegroundColor Yellow
```

### 4. 检查验证者的出块情况

```powershell
# 查询 epoch 368 的出块详情
$epoch = 368
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="dpos_getBlockProducers";params=@($epoch);id=1}|ConvertTo-Json) -ContentType "application/json"
Write-Host "Epoch $epoch 出块详情:" -ForegroundColor Green
$r.result.producers | ForEach-Object { Write-Host "  $($_.address): $($_.count) 个区块" -ForegroundColor Cyan }
Write-Host "总区块数: $($r.result.actualBlocksFound)" -ForegroundColor Yellow
```

### 5. 检查当前 Epoch 信息

```powershell
# 查询当前 epoch 信息
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="dpos_getCurrentEpochInfo";params=@();id=1}|ConvertTo-Json) -ContentType "application/json"
Write-Host "当前 Epoch: $($r.result.epochNumber)" -ForegroundColor Green
Write-Host "Epoch 大小: $($r.result.epochSize) 个区块" -ForegroundColor Yellow
```

## 关键检查点

1. **奖励总额是否正确**
   - 所有验证者奖励 + 所有投票者奖励 = 150 VCITY
   - 如果超过，说明有重复计算

2. **投票者是否投票给了多个验证者**
   - 如果投票给了多个验证者，需要确认奖励是否被重复计算

3. **投票权重是否正确**
   - 检查投票者的总投票权重
   - 检查整个 epoch 的总投票权重

4. **奖励记录是单个 epoch 还是累计**
   - `dpos_getRewardHistory` 返回的是多个 epoch 的累计
   - `dpos_getEpochRewardDetails` 返回的是单个 epoch 的详情

## 可能的问题

### 问题1：查询的是累计奖励
如果使用 `dpos_getRewardHistory` 查询 epoch 360-368，返回的是这9个 epoch 的累计奖励：
- 9个 epoch * 45 VCITY（投票者奖励池）= 405 VCITY
- 但实际可能因为权重分配，某些投票者获得更多

### 问题2：投票权重计算错误
如果某个投票者的投票权重占很大比例，可能获得大部分投票者奖励池。

### 问题3：奖励被重复计算
如果奖励计算逻辑有问题，可能导致重复计算。

