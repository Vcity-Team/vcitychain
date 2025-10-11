# DPoS经济系统测试脚本 (PowerShell版本)
Write-Host "=== DPoS经济系统接口测试 ===" -ForegroundColor Green

# 测试JSON-RPC接口
Write-Host "1. 测试JSON-RPC接口..." -ForegroundColor Yellow

# 获取当前Epoch信息
Write-Host "测试 dpos_getCurrentEpochInfo..." -ForegroundColor Cyan
$body1 = @{
    jsonrpc = "2.0"
    method = "dpos_getCurrentEpochInfo"
    params = @()
    id = 1
} | ConvertTo-Json

try {
    $response1 = Invoke-RestMethod -Uri "http://localhost:8545" -Method Post -Body $body1 -ContentType "application/json"
    $response1 | ConvertTo-Json -Depth 10
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n测试 dpos_getEpochInfoByNumber..." -ForegroundColor Cyan
$body2 = @{
    jsonrpc = "2.0"
    method = "dpos_getEpochInfoByNumber"
    params = @(1)
    id = 2
} | ConvertTo-Json

try {
    $response2 = Invoke-RestMethod -Uri "http://localhost:8545" -Method Post -Body $body2 -ContentType "application/json"
    $response2 | ConvertTo-Json -Depth 10
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n测试 dpos_getValidatorBlockStats..." -ForegroundColor Cyan
$body3 = @{
    jsonrpc = "2.0"
    method = "dpos_getValidatorBlockStats"
    params = @("0x1234567890123456789012345678901234567890", 1)
    id = 3
} | ConvertTo-Json

try {
    $response3 = Invoke-RestMethod -Uri "http://localhost:8545" -Method Post -Body $body3 -ContentType "application/json"
    $response3 | ConvertTo-Json -Depth 10
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n测试 dpos_getValidatorRewardsInfo..." -ForegroundColor Cyan
$body4 = @{
    jsonrpc = "2.0"
    method = "dpos_getValidatorRewardsInfo"
    params = @("0x1234567890123456789012345678901234567890", 1)
    id = 4
} | ConvertTo-Json

try {
    $response4 = Invoke-RestMethod -Uri "http://localhost:8545" -Method Post -Body $body4 -ContentType "application/json"
    $response4 | ConvertTo-Json -Depth 10
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n2. 测试CLI命令..." -ForegroundColor Yellow

# 测试CLI命令
Write-Host "测试 ./main dpos epoch..." -ForegroundColor Cyan
try {
    & ./main dpos epoch
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n测试 ./main dpos epoch 1..." -ForegroundColor Cyan
try {
    & ./main dpos epoch 1
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n测试 ./main dpos stats..." -ForegroundColor Cyan
try {
    & ./main dpos stats 0x1234567890123456789012345678901234567890 1
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n测试 ./main dpos rewards..." -ForegroundColor Cyan
try {
    & ./main dpos rewards 0x1234567890123456789012345678901234567890 1
} catch {
    Write-Host "Error: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n=== 测试完成 ===" -ForegroundColor Green

