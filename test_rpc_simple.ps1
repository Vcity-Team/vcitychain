# 单行命令：获取gasPrice并转换为十进制
$result = Invoke-RestMethod -Uri "http://127.0.0.1:8545" -Method Post -Body (@{jsonrpc="2.0";method="eth_gasPrice";params=@();id=0} | ConvertTo-Json) -ContentType "application/json"; $hexValue = $result.result; $decimalValue = [Convert]::ToInt64($hexValue, 16); Write-Host "十六进制: $hexValue -> 十进制: $decimalValue"; $decimalValue

