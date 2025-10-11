# 测试奖励记录和查询功能
# 作者：AI Assistant
# 日期：2025-01-11

Write-Host "🎯 测试DPoS奖励记录和查询功能" -ForegroundColor Green
Write-Host "=================================" -ForegroundColor Green

# 配置
$jsonRpcUrl = "http://localhost:9545"
$validatorAddress = "0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C"  # 示例验证者地址
$voterAddress = "0x1234567890123456789012345678901234567890"     # 示例投票者地址

# 测试函数
function Test-JsonRpcCall {
    param(
        [string]$Method,
        [array]$Params = @(),
        [string]$Description
    )
    
    Write-Host "`n🔍 测试: $Description" -ForegroundColor Yellow
    Write-Host "方法: $Method" -ForegroundColor Cyan
    Write-Host "参数: $($Params | ConvertTo-Json -Compress)" -ForegroundColor Cyan
    
    $body = @{
        jsonrpc = "2.0"
        method = $Method
        params = $Params
        id = 1
    } | ConvertTo-Json -Depth 10
    
    try {
        $response = Invoke-RestMethod -Uri $jsonRpcUrl -Method Post -Body $body -ContentType "application/json"
        
        if ($response.result) {
            Write-Host "✅ 成功" -ForegroundColor Green
            Write-Host "结果: $($response.result | ConvertTo-Json -Depth 10)" -ForegroundColor White
        } else {
            Write-Host "❌ 失败" -ForegroundColor Red
            Write-Host "错误: $($response.error | ConvertTo-Json -Depth 10)" -ForegroundColor Red
        }
    } catch {
        Write-Host "❌ 请求失败: $($_.Exception.Message)" -ForegroundColor Red
    }
}

# 1. 测试查询验证者奖励历史
Test-JsonRpcCall -Method "dpos_getValidatorRewardHistory" -Params @($validatorAddress, 1, 10) -Description "查询验证者奖励历史"

# 2. 测试查询投票者奖励历史
Test-JsonRpcCall -Method "dpos_getVoterRewardHistory" -Params @($voterAddress, 1, 10) -Description "查询投票者奖励历史"

# 3. 测试查询指定epoch的奖励详情
Test-JsonRpcCall -Method "dpos_getEpochRewardDetails" -Params @(1) -Description "查询Epoch 1的奖励详情"

# 4. 测试查询当前epoch信息
Test-JsonRpcCall -Method "dpos_getCurrentEpochInfo" -Params @() -Description "查询当前Epoch信息"

# 5. 测试查询所有验证者
Test-JsonRpcCall -Method "dpos_getAllValidators" -Params @() -Description "查询所有验证者"

Write-Host "`n🎉 测试完成！" -ForegroundColor Green
Write-Host "如果看到奖励记录，说明功能正常工作" -ForegroundColor Green
Write-Host "如果没有记录，可能需要等待几个epoch让奖励分发" -ForegroundColor Yellow
