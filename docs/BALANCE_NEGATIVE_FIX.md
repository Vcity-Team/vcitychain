# 余额负数问题修复方案

## 问题

原始返回值：`0xbab527d7e52fd9fec4`

这个值最高位是 `b`（二进制 `1011`），最高位为1，会被解释为负数。

## 解决方案

### 方法1：手动逐位解析（最可靠）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

$hexValue = $r.result.Replace("0x","")
$balance = [bigint]::Zero

# 逐位解析，确保无符号
for ($i = 0; $i -lt $hexValue.Length; $i++) {
    $digit = [Convert]::ToInt32($hexValue.Substring($i, 1), 16)
    $balance = $balance * 16 + $digit
}

$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
Write-Host "余额 (Wei): $balance" -ForegroundColor Yellow
```

### 方法2：使用 Convert.ToUInt64 分段处理

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

$hexValue = $r.result.Replace("0x","")
$balance = [bigint]::Zero

# 每16个字符（64位）分段处理
$chunkSize = 16
for ($i = 0; $i -lt $hexValue.Length; $i += $chunkSize) {
    $chunk = $hexValue.Substring($i, [Math]::Min($chunkSize, $hexValue.Length - $i))
    $chunkValue = [Convert]::ToUInt64($chunk, 16)
    $balance = $balance * [bigint]::Pow(16, $chunk.Length) + $chunkValue
}

$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

### 方法3：使用字符串拼接确保无符号（推荐）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

$hexValue = $r.result.Replace("0x","")

# 在字符串前加 "0" 确保被解析为正数
$balance = [bigint]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)

$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
Write-Host "余额 (Wei): $balance" -ForegroundColor Yellow
```

### 方法4：使用 System.Numerics.BigInteger（确保无符号）

```powershell
Add-Type -AssemblyName System.Numerics

$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

$hexValue = $r.result.Replace("0x","")

# 方法4a：在字符串前加 "0"
$balance = [System.Numerics.BigInteger]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)

# 或者方法4b：手动逐位解析
$balance = [System.Numerics.BigInteger]::Zero
for ($i = 0; $i -lt $hexValue.Length; $i++) {
    $digit = [Convert]::ToInt32($hexValue.Substring($i, 1), 16)
    $balance = $balance * 16 + $digit
}

$balanceVCITY = $balance / [System.Numerics.BigInteger]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

## 完整测试脚本

```powershell
# 测试所有方法
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
$r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"

$hexValue = $r.result.Replace("0x","")
Write-Host "原始十六进制值: 0x$hexValue" -ForegroundColor Cyan
Write-Host ""

# 方法1：手动逐位解析
$balance1 = [bigint]::Zero
for ($i = 0; $i -lt $hexValue.Length; $i++) {
    $digit = [Convert]::ToInt32($hexValue.Substring($i, 1), 16)
    $balance1 = $balance1 * 16 + $digit
}
Write-Host "方法1 (逐位解析): $($balance1 / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green

# 方法2：字符串前加 "0"
$balance2 = [bigint]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)
Write-Host "方法2 (加0前缀): $($balance2 / [bigint]::Pow(10, 18)) VCITY" -ForegroundColor Green

# 方法3：使用 System.Numerics.BigInteger
Add-Type -AssemblyName System.Numerics
$balance3 = [System.Numerics.BigInteger]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)
Write-Host "方法3 (BigInteger加0): $($balance3 / [System.Numerics.BigInteger]::Pow(10, 18)) VCITY" -ForegroundColor Green

# 验证：所有方法应该得到相同的结果
if ($balance1 -eq $balance2 -and $balance2 -eq $balance3) {
    Write-Host "`n✅ 所有方法结果一致！" -ForegroundColor Green
} else {
    Write-Host "`n❌ 方法结果不一致！" -ForegroundColor Red
}
```

## 推荐的一行命令（最简单）

```powershell
$addr = "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"; $r = Invoke-RestMethod -Uri "http://209.53.43.252:9545" -Method Post -Body (@{jsonrpc="2.0";method="eth_getBalance";params=@($addr,"latest");id=1}|ConvertTo-Json) -ContentType "application/json"; $hex = $r.result.Replace("0x",""); $balance = [bigint]::Parse("0" + $hex, [System.Globalization.NumberStyles]::HexNumber); $balance / [bigint]::Pow(10, 18)
```

## 封装为函数

```powershell
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
    
    $hexValue = $response.result.Replace("0x","")
    
    # 在字符串前加 "0" 确保无符号解析
    $balanceWei = [bigint]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)
    $balanceVCITY = $balanceWei / [bigint]::Pow(10, 18)
    
    return @{
        Address = $Address
        BalanceWei = $balanceWei.ToString()
        BalanceVCITY = $balanceVCITY.ToString()
        HexValue = "0x" + $hexValue
    }
}

# 使用
$result = Get-Balance -Address "0x8f2728e24F8e85e7F4A85616c3CBa3bB14c34127"
Write-Host "地址: $($result.Address)" -ForegroundColor Cyan
Write-Host "余额 (Wei): $($result.BalanceWei)" -ForegroundColor Yellow
Write-Host "余额 (VCITY): $($result.BalanceVCITY)" -ForegroundColor Green
```

## 关键点

**核心解决方案**：在十六进制字符串前加 `"0"`，确保被解析为无符号整数。

```powershell
# ❌ 错误（会被解释为负数）
$balance = [bigint]::Parse($hexValue, [System.Globalization.NumberStyles]::HexNumber)

# ✅ 正确（确保无符号）
$balance = [bigint]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)
```

## 验证

对于你的值 `0xbab527d7e52fd9fec4`：

```powershell
$hexValue = "bab527d7e52fd9fec4"
$balance = [bigint]::Parse("0" + $hexValue, [System.Globalization.NumberStyles]::HexNumber)
$balanceVCITY = $balance / [bigint]::Pow(10, 18)
Write-Host "余额: $balanceVCITY VCITY" -ForegroundColor Green
```

这应该会显示正确的正数余额。

