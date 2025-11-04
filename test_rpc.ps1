# PowerShell格式的RPC调用示例

# 方法1：使用 Invoke-RestMethod（推荐，自动解析JSON）
$body = @{
    jsonrpc = "2.0"
    method = "eth_gasPrice"
    params = @()
    id = 0
} | ConvertTo-Json

Invoke-RestMethod -Uri "http://127.0.0.1:8545" -Method Post -Body $body -ContentType "application/json"

# 方法2：使用 Invoke-WebRequest（需要手动解析JSON）
$body = @{
    jsonrpc = "2.0"
    method = "eth_gasPrice"
    params = @()
    id = 0
} | ConvertTo-Json

$response = Invoke-WebRequest -Uri "http://127.0.0.1:8545" -Method Post -Body $body -ContentType "application/json"
$response.Content | ConvertFrom-Json

# 方法3：单行命令（使用Invoke-RestMethod，并转换为十进制）
$result = Invoke-RestMethod -Uri "http://127.0.0.1:8545" -Method Post -Body (@{jsonrpc="2.0";method="eth_gasPrice";params=@();id=0} | ConvertTo-Json) -ContentType "application/json"
$hexValue = $result.result
$decimalValue = [Convert]::ToInt64($hexValue, 16)
Write-Host "十六进制: $hexValue"
Write-Host "十进制: $decimalValue"
$decimalValue

