# 查询投票者信息的 PowerShell 命令

## 1. 查询所有投票者的投票权重

```powershell
# 查询所有质押信息（包括投票者）
$rpcUrl = "http://209.53.43.252:9545"
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getStakingInfo";params=@("latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 筛选出所有投票者（有 Delegate 的条目）
$voters = $r.result | Where-Object { $_.delegate -ne $null -and $_.delegate -ne "" }

Write-Host "`n========== 所有投票者信息 ==========" -ForegroundColor Green
Write-Host "投票者总数: $($voters.Count)" -ForegroundColor Yellow

# 按投票权重排序
$votersSorted = $voters | Sort-Object { [bigint]::Parse($_.amountWei) } -Descending

# 显示每个投票者的信息
$totalVotingPower = [bigint]::Zero
foreach ($voter in $votersSorted) {
    $votingPower = [bigint]::Parse($voter.amountWei)
    $totalVotingPower = $totalVotingPower + $votingPower
    $votingPowerVCITY = $votingPower / [bigint]::Pow(10, 18)
    Write-Host "`n投票者: $($voter.staker)" -ForegroundColor Cyan
    Write-Host "  投票给: $($voter.delegate)" -ForegroundColor White
    Write-Host "  投票权重: $($voter.amountWei) Wei = $votingPowerVCITY VCITY" -ForegroundColor Yellow
}

Write-Host "`n========== 总投票权重 ==========" -ForegroundColor Green
Write-Host "总投票权重: $($totalVotingPower.ToString()) Wei = $($totalVotingPower / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Yellow
```

## 2. 检查投票者奖励池的精确值

```powershell
# 查询当前 epoch 信息
$rpcUrl = "http://209.53.43.252:9545"
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getCurrentEpochInfo";params=@();id=1}|ConvertTo-Json) -ContentType "application/json"

Write-Host "`n========== 投票者奖励池计算 ==========" -ForegroundColor Green

# 从配置获取奖励参数（需要从节点配置或查询获取）
$rewardAmount = [bigint]::Parse("100000000000000000000")  # 100 VCITY
$voterRatio = 30  # 30%

# 计算投票者奖励池
$voterRewardPool = $rewardAmount * $voterRatio / 100

Write-Host "总奖励池: $($rewardAmount.ToString()) Wei = $($rewardAmount / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Yellow
Write-Host "投票者奖励比例: $voterRatio%" -ForegroundColor Yellow
Write-Host "投票者奖励池: $($voterRewardPool.ToString()) Wei = $($voterRewardPool / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green
```

## 3. 查询总投票权重（详细版）

```powershell
# 查询所有质押信息
$rpcUrl = "http://209.53.43.252:9545"
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getStakingInfo";params=@("latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 筛选出所有投票者（有 Delegate 的条目）
$voters = $r.result | Where-Object { $_.delegate -ne $null -and $_.delegate -ne "" }

# 计算总投票权重
$totalVotingPower = [bigint]::Zero
foreach ($voter in $voters) {
    $votingPower = [bigint]::Parse($voter.amountWei)
    $totalVotingPower = $totalVotingPower + $votingPower
}

Write-Host "`n========== 总投票权重统计 ==========" -ForegroundColor Green
Write-Host "投票者数量: $($voters.Count)" -ForegroundColor Yellow
Write-Host "总投票权重: $($totalVotingPower.ToString()) Wei" -ForegroundColor Yellow
Write-Host "总投票权重: $($totalVotingPower / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green
```

## 4. 查询特定投票者的权重占比

```powershell
# 查询特定投票者的信息
$rpcUrl = "http://209.53.43.252:9545"
$targetAddr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"

# 查询所有质押信息
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getStakingInfo";params=@("latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 筛选出所有投票者
$voters = $r.result | Where-Object { $_.delegate -ne $null -and $_.delegate -ne "" }

# 计算总投票权重
$totalVotingPower = [bigint]::Zero
foreach ($voter in $voters) {
    $votingPower = [bigint]::Parse($voter.amountWei)
    $totalVotingPower = $totalVotingPower + $votingPower
}

# 查找目标投票者
$targetVoter = $voters | Where-Object { $_.staker -eq $targetAddr }
if ($targetVoter) {
    $targetVotingPower = [bigint]::Parse($targetVoter.amountWei)
    $ratio = ($targetVotingPower * 100) / $totalVotingPower
    
    Write-Host "`n========== 投票者权重占比 ==========" -ForegroundColor Green
    Write-Host "投票者地址: $targetAddr" -ForegroundColor Cyan
    Write-Host "投票者权重: $($targetVotingPower.ToString()) Wei = $($targetVotingPower / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Yellow
    Write-Host "总投票权重: $($totalVotingPower.ToString()) Wei = $($totalVotingPower / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Yellow
    Write-Host "权重占比: $ratio%" -ForegroundColor Green
    
    # 计算预期奖励
    $rewardAmount = [bigint]::Parse("100000000000000000000")  # 100 VCITY
    $voterRatio = 30  # 30%
    $voterRewardPool = $rewardAmount * $voterRatio / 100
    $expectedReward = ($voterRewardPool * $targetVotingPower) / $totalVotingPower
    
    Write-Host "`n预期投票者奖励: $($expectedReward.ToString()) Wei = $($expectedReward / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green
} else {
    Write-Host "未找到投票者: $targetAddr" -ForegroundColor Red
}
```

## 5. 综合查询（一键查询所有信息）

```powershell
# 综合查询脚本
$rpcUrl = "http://209.53.43.252:9545"
$targetAddr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"

Write-Host "`n========== 开始查询投票者信息 ==========" -ForegroundColor Green

# 1. 查询所有质押信息
$r = Invoke-RestMethod -Uri $rpcUrl -Method Post -Body (@{jsonrpc="2.0";method="dpos_getStakingInfo";params=@("latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 2. 筛选出所有投票者
$voters = $r.result | Where-Object { $_.delegate -ne $null -and $_.delegate -ne "" }

# 3. 计算总投票权重
$totalVotingPower = [bigint]::Zero
foreach ($voter in $voters) {
    $votingPower = [bigint]::Parse($voter.amountWei)
    $totalVotingPower = $totalVotingPower + $votingPower
}

# 4. 显示总投票权重
Write-Host "`n总投票权重: $($totalVotingPower.ToString()) Wei = $($totalVotingPower / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Yellow

# 5. 计算投票者奖励池
$rewardAmount = [bigint]::Parse("100000000000000000000")  # 100 VCITY
$voterRatio = 30  # 30%
$voterRewardPool = $rewardAmount * $voterRatio / 100
Write-Host "投票者奖励池: $($voterRewardPool.ToString()) Wei = $($voterRewardPool / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green

# 6. 查询目标投票者
$targetVoter = $voters | Where-Object { $_.staker -eq $targetAddr }
if ($targetVoter) {
    $targetVotingPower = [bigint]::Parse($targetVoter.amountWei)
    $ratio = ($targetVotingPower * 100) / $totalVotingPower
    $expectedReward = ($voterRewardPool * $targetVotingPower) / $totalVotingPower
    
    Write-Host "`n目标投票者: $targetAddr" -ForegroundColor Cyan
    Write-Host "  投票权重: $($targetVotingPower.ToString()) Wei = $($targetVotingPower / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Yellow
    Write-Host "  权重占比: $ratio%" -ForegroundColor Yellow
    Write-Host "  预期奖励: $($expectedReward.ToString()) Wei = $($expectedReward / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green
} else {
    Write-Host "`n未找到投票者: $targetAddr" -ForegroundColor Red
}

# 7. 显示所有投票者（按权重排序）
Write-Host "`n========== 所有投票者列表（按权重排序） ==========" -ForegroundColor Green
$votersSorted = $voters | Sort-Object { [bigint]::Parse($_.amountWei) } -Descending
foreach ($voter in $votersSorted) {
    $votingPower = [bigint]::Parse($voter.amountWei)
    $votingPowerVCITY = $votingPower / [bigint]::Pow(10, 18)
    $voterRatio = ($votingPower * 100) / $totalVotingPower
    Write-Host "$($voter.staker) -> $($voter.delegate): $votingPowerVCITY VCITY ($voterRatio%)" -ForegroundColor White
}
```

## 使用说明

1. **查询所有投票者**：运行第1个脚本
2. **检查奖励池**：运行第2个脚本
3. **查询总投票权重**：运行第3个脚本
4. **查询特定投票者**：运行第4个脚本，修改 `$targetAddr` 变量
5. **一键查询所有信息**：运行第5个脚本（推荐）

## 注意事项

- 确保 RPC 节点地址正确（当前为 `http://209.53.43.252:9545`）
- 如果查询失败，检查节点是否在线
- 奖励池计算基于配置值（100 VCITY，30%），如果配置不同需要修改

