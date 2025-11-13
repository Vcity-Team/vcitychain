# 余额显示负数问题分析

## 问题描述

查询投票者余额时，结果显示为负数 `-1278`：

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"
[bigint]::Parse($r.result.Replace("0x",""),[System.Globalization.NumberStyles]::HexNumber) / [bigint]::Pow(10,18)
# 结果：-1278
```

## 问题原因分析

### 原因1：PowerShell 的 bigint 解析问题（最可能）

PowerShell 的 `[bigint]::Parse` 在处理十六进制字符串时，如果字符串以 `F` 开头（表示最高位为1），可能会被解释为负数（有符号整数）。

**示例**：
```powershell
# 如果余额是 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF
# 这会被解析为 -1（有符号整数）
```

### 原因2：余额值过大导致溢出

如果余额值非常大，超过了 PowerShell 的整数范围，可能导致溢出。

### 原因3：十六进制字符串格式问题

`eth_getBalance` 返回的格式可能是 `"0x..."`，需要正确处理。

## 解决方案

### 方案1：使用正确的 bigint 解析方法（推荐）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 方法1：使用无符号解析
$hexValue = $r.result.Replace("0x","")
$balance = [bigint]::Zero
for ($i = 0; $i -lt $hexValue.Length; $i += 1) {
    $digit = [Convert]::ToInt32($hexValue.Substring($i, 1), 16)
    $balance = $balance * 16 + $digit
}
$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

### 方案2：使用字符串处理（更简单）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 方法2：使用 Convert 类
$hexValue = $r.result.Replace("0x","")
$balance = [bigint]::Zero
$balance = [bigint]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)
$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

### 方案3：使用正确的 NumberStyles（最佳）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 方法3：确保使用无符号解析
$hexValue = $r.result
if ($hexValue -match "^0x") {
    $hexValue = $hexValue.Substring(2)
}

# 使用正确的解析方式
$balance = [bigint]::Zero
$balance = [bigint]::Parse($hexValue, [System.Globalization.NumberStyles]::AllowHexSpecifier -bor [System.Globalization.NumberStyles]::HexNumber)
$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

### 方案4：使用 .NET 的 BigInteger.Parse（最可靠）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

# 方法4：使用 System.Numerics.BigInteger
Add-Type -AssemblyName System.Numerics
$hexValue = $r.result.Replace("0x","")
$balance = [System.Numerics.BigInteger]::Parse($hexValue, [System.Globalization.NumberStyles]::HexNumber)
$balanceVCITY = $balance / [System.Numerics.BigInteger]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

## 推荐的完整脚本

```powershell
# 查询余额（正确处理大整数）
function Get-Balance {
    param(
        [string]$Address,
        [string]$RpcUrl = "http://209.53.43.252:9545"
    )
    
    $request = @{
        jsonrpc = "2.0"
        method = "eth_getBalance"
        params = @($Address, "latest")
        id = 1
    } | ConvertTo-Json
    
    $response = Invoke-RestMethod -Uri $RpcUrl -Method Post -Body $request -ContentType "application/json"
    
    # 获取十六进制值
    $hexValue = $response.result
    
    # 移除 0x 前缀
    if ($hexValue -match "^0x") {
        $hexValue = $hexValue.Substring(2)
    }
    
    # 使用 System.Numerics.BigInteger 解析（最可靠）
    Add-Type -AssemblyName System.Numerics
    $balanceWei = [System.Numerics.BigInteger]::Parse($hexValue, [System.Globalization.NumberStyles]::HexNumber)
    
    # 转换为 VCITY
    $balanceVCITY = $balanceWei / [System.Numerics.BigInteger]::Pow(10, 18)
    
    return @{
        Address = $Address
        BalanceWei = $balanceWei.ToString()
        BalanceVCITY = $balanceVCITY.ToString()
        HexValue = "0x" + $hexValue
    }
}

# 使用示例
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$result = Get-Balance -Address $addr
Write-Host "地址: $($result.Address)" -ForegroundColor Cyan
Write-Host "余额 (Wei): $($result.BalanceWei)" -ForegroundColor Yellow
Write-Host "余额 (VCITY): $($result.BalanceVCITY)" -ForegroundColor Green
```

## 验证方法

### 1. 先查看原始返回值

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"
Write-Host "原始返回值: $($r.result)" -ForegroundColor Yellow
```

### 2. 检查十六进制字符串

```powershell
$hexValue = $r.result.Replace("0x","")
Write-Host "十六进制值: $hexValue" -ForegroundColor Cyan
Write-Host "长度: $($hexValue.Length)" -ForegroundColor Cyan
Write-Host "第一个字符: $($hexValue[0])" -ForegroundColor Cyan
```

### 3. 使用在线工具验证

如果十六进制值以 `F` 开头且很长，可能是负数表示。可以使用在线十六进制转换工具验证。

## 根本原因

**PowerShell 的 `[bigint]::Parse` 在处理十六进制时，如果最高位为 1，会将其解释为负数（有符号整数）。**

**解决方案**：使用 `System.Numerics.BigInteger` 或确保使用无符号解析。

## 快速修复命令

```powershell
# 一行命令（使用 System.Numerics.BigInteger）
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"; $r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"; Add-Type -AssemblyName System.Numerics; $hex = $r.result.Replace("0x",""); $balance = [System.Numerics.BigInteger]::Parse($hex, [System.Globalization.NumberStyles]::HexNumber); $balance / [System.Numerics.BigInteger]::Pow(10, 18)
```

