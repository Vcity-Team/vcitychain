# Monitor validator balance
param(
    [Parameter(Mandatory=$false)]
    [string]$Address = "0xe22611289BAb9CDDB85B23Dc2716006f931Bc41C",
    
    [Parameter(Mandatory=$false)]
    [string]$RpcUrl = "http://localhost:8545",
    
    [Parameter(Mandatory=$false)]
    [int]$Interval = 3,
    
    [Parameter(Mandatory=$false)]
    [switch]$ClearScreen = $true
)

$previousBalance = $null
$iteration = 0

Write-Host "Starting balance monitor..." -ForegroundColor Green
Write-Host "Address: $Address" -ForegroundColor Cyan
Write-Host "RPC: $RpcUrl" -ForegroundColor Cyan
Write-Host "Interval: $Interval seconds" -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop" -ForegroundColor Yellow
Write-Host ""

while ($true) {
    try {
        $iteration++
        $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
        
        $body = @{
            jsonrpc = "2.0"
            method = "eth_getBalance"
            params = @($Address, "latest")
            id = 1
        } | ConvertTo-Json
        
        $response = Invoke-RestMethod -Uri $RpcUrl -Method Post -Body $body -ContentType "application/json" -ErrorAction Stop
        $hex = $response.result
        
        if ($hex -and $hex -ne "0x0") {
            $hexValue = $hex.Substring(2)
            $decimal = [System.Numerics.BigInteger]::Parse($hexValue, [System.Globalization.NumberStyles]::AllowHexSpecifier)
            $vcity = [double]$decimal / 1e18
            
            $isNegative = ($decimal.Sign -lt 0) -or ($vcity -lt 0)
            
            if ($isNegative) {
                Write-Host "" -ForegroundColor Red
                Write-Host "===============================================" -ForegroundColor Red
                Write-Host "ERROR: Negative balance detected!" -ForegroundColor Red
                Write-Host "===============================================" -ForegroundColor Red
                Write-Host "Time: $timestamp" -ForegroundColor Red
                Write-Host "Address: $Address" -ForegroundColor Red
                Write-Host "Hex: $hex" -ForegroundColor Red
                Write-Host "Decimal: $decimal" -ForegroundColor Red
                Write-Host "VCITY: $($vcity.ToString('F18'))" -ForegroundColor Red
                if ($previousBalance -ne $null) {
                    $diff = $vcity - $previousBalance
                    Write-Host "Previous: $($previousBalance.ToString('F18')) VCITY" -ForegroundColor Red
                    Write-Host "Change: $($diff.ToString('F18')) VCITY" -ForegroundColor Red
                }
                Write-Host "RPC: $RpcUrl" -ForegroundColor Red
                Write-Host "===============================================" -ForegroundColor Red
                Write-Host "" -ForegroundColor Red
                Write-Host "Exiting..." -ForegroundColor Red
                exit 1
            }
            
            $diff = $null
            $diffText = ""
            $diffColor = "White"
            if ($previousBalance -ne $null) {
                $diff = $vcity - $previousBalance
                if ($diff -gt 0) {
                    $diffText = "+$($diff.ToString('F18')) VCITY"
                    $diffColor = "Green"
                } elseif ($diff -lt 0) {
                    $diffText = "$($diff.ToString('F18')) VCITY"
                    $diffColor = "Red"
                } else {
                    $diffText = "0 VCITY (no change)"
                    $diffColor = "Gray"
                }
            } else {
                $diffText = "First query"
                $diffColor = "Cyan"
            }
            
            if ($ClearScreen -and $iteration -gt 1) {
                Clear-Host
                Write-Host "Starting balance monitor..." -ForegroundColor Green
                Write-Host "Address: $Address" -ForegroundColor Cyan
                Write-Host "RPC: $RpcUrl" -ForegroundColor Cyan
                Write-Host "Interval: $Interval seconds" -ForegroundColor Cyan
                Write-Host "Press Ctrl+C to stop" -ForegroundColor Yellow
                Write-Host ""
            }
            
            Write-Host "===============================================" -ForegroundColor DarkGray
            Write-Host "Time: $timestamp" -ForegroundColor White
            Write-Host "Hex: $hex" -ForegroundColor Yellow
            Write-Host "Decimal: $decimal" -ForegroundColor Cyan
            Write-Host "VCITY: $($vcity.ToString('F18'))" -ForegroundColor Green
            Write-Host "Change: $diffText" -ForegroundColor $diffColor
            Write-Host "===============================================" -ForegroundColor DarkGray
            Write-Host ""
            
            $previousBalance = $vcity
        } else {
            Write-Host "[$timestamp] Balance is 0 or query failed" -ForegroundColor Red
        }
        
        Start-Sleep -Seconds $Interval
    }
    catch {
        Write-Host "[$timestamp] Query failed: $($_.Exception.Message)" -ForegroundColor Red
        Start-Sleep -Seconds $Interval
    }
}

