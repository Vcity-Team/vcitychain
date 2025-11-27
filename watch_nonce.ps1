# Monitor account nonce with TPS calculation and chart
param(
    [Parameter(Mandatory=$false)]
    [string]$Address = "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe",
    
    [Parameter(Mandatory=$false)]
    [string]$RpcUrl = "http://localhost:8545",
    
    [Parameter(Mandatory=$false)]
    [int]$Interval = 3,
    
    [Parameter(Mandatory=$false)]
    [switch]$ClearScreen = $true,
    
    [Parameter(Mandatory=$false)]
    [int]$ChartWidth = 40
)

# Statistics tracking
$script:startTime = Get-Date
$script:previousNonce = $null
$script:previousTime = $null
$script:iteration = 0
$script:totalTransactions = 0
$script:tpsHistory = @()  # Array to store TPS values for chart
$script:maxTpsHistory = 30  # Keep last 30 TPS values for chart
$script:initialNonce = $null

# Function to draw ASCII chart
function Draw-TpsChart {
    param(
        [array]$tpsValues,
        [int]$width = 40
    )
    
    if ($tpsValues.Count -eq 0) {
        return
    }
    
    $maxTps = ($tpsValues | Measure-Object -Maximum).Maximum
    $minTps = ($tpsValues | Measure-Object -Minimum).Minimum
    
    if ($maxTps -eq $minTps) {
        $maxTps = $minTps + 1
    }
    
    $height = 10
    Write-Host ""
    Write-Host "TPS Chart (Last $($tpsValues.Count) intervals):" -ForegroundColor Cyan
    Write-Host "Max: $([math]::Round($maxTps, 2)) TPS | Min: $([math]::Round($minTps, 2)) TPS" -ForegroundColor DarkGray
    Write-Host ("-" * ($width + 10)) -ForegroundColor DarkGray
    
    # Draw chart from top to bottom
    for ($i = $height; $i -ge 0; $i--) {
        $threshold = $minTps + ($maxTps - $minTps) * ($i / $height)
        $line = "{0,6:F2} |" -f $threshold
        
        foreach ($tps in $tpsValues) {
            if ($tps -ge $threshold) {
                $line += "#"
            } else {
                $line += " "
            }
        }
        
        Write-Host $line -ForegroundColor DarkGray
    }
    
    # Draw bottom axis
    Write-Host ("-" * ($width + 10)) -ForegroundColor DarkGray
    $axisLine = "       "
    for ($i = 0; $i -lt [Math]::Min($tpsValues.Count, $width); $i++) {
        if ($i % 5 -eq 0) {
            $axisLine += "|"
        } else {
            $axisLine += " "
        }
    }
    Write-Host $axisLine -ForegroundColor DarkGray
    
    # Show latest TPS values
    $latestValues = $tpsValues[-10..-1]
    if ($latestValues) {
        $valuesLine = "Latest: "
        foreach ($v in $latestValues) {
            $valuesLine += "$([math]::Round($v, 2)) "
        }
        Write-Host $valuesLine -ForegroundColor Yellow
    }
}

# Function to show statistics
function Show-Statistics {
    $endTime = Get-Date
    $duration = $endTime - $script:startTime
    $totalSeconds = $duration.TotalSeconds
    
    Write-Host ""
    Write-Host "===============================================" -ForegroundColor Green
    Write-Host "           MONITORING STATISTICS" -ForegroundColor Green
    Write-Host "===============================================" -ForegroundColor Green
    Write-Host "Start Time: $($script:startTime.ToString('yyyy-MM-dd HH:mm:ss'))" -ForegroundColor White
    Write-Host "End Time:   $($endTime.ToString('yyyy-MM-dd HH:mm:ss'))" -ForegroundColor White
    Write-Host "Duration:   $($duration.ToString('hh\:mm\:ss')) ($([math]::Round($totalSeconds, 2)) seconds)" -ForegroundColor Cyan
    Write-Host ""
    
    if ($script:initialNonce -ne $null -and $script:previousNonce -ne $null) {
        $totalTxs = $script:previousNonce - $script:initialNonce
        Write-Host "Initial Nonce: $script:initialNonce" -ForegroundColor White
        Write-Host "Final Nonce:   $script:previousNonce" -ForegroundColor White
        Write-Host "Total Transactions: $totalTxs" -ForegroundColor Green
        Write-Host ""
        
        if ($totalSeconds -gt 0) {
            $avgTps = $totalTxs / $totalSeconds
            Write-Host "Average TPS: $([math]::Round($avgTps, 4))" -ForegroundColor Yellow
        }
        
        if ($script:tpsHistory.Count -gt 0) {
            $maxTps = ($script:tpsHistory | Measure-Object -Maximum).Maximum
            $minTps = ($script:tpsHistory | Measure-Object -Minimum).Minimum
            $avgRecentTps = ($script:tpsHistory | Measure-Object -Average).Average
            Write-Host "Peak TPS:    $([math]::Round($maxTps, 4))" -ForegroundColor Red
            Write-Host "Min TPS:     $([math]::Round($minTps, 4))" -ForegroundColor DarkGray
            Write-Host "Recent Avg:  $([math]::Round($avgRecentTps, 4))" -ForegroundColor Cyan
        }
    }
    
    Write-Host "Total Queries: $script:iteration" -ForegroundColor White
    Write-Host "===============================================" -ForegroundColor Green
}

# Register cleanup handler
$null = Register-EngineEvent PowerShell.Exiting -Action {
    Show-Statistics
}

Write-Host "Starting nonce monitor with TPS calculation..." -ForegroundColor Green
Write-Host "Address: $Address" -ForegroundColor Cyan
Write-Host "RPC: $RpcUrl" -ForegroundColor Cyan
Write-Host "Interval: $Interval seconds" -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop" -ForegroundColor Yellow
Write-Host ""

while ($true) {
    try {
        $script:iteration++
        $currentTime = Get-Date
        $timestamp = $currentTime.ToString("yyyy-MM-dd HH:mm:ss")
        
        $body = @{
            jsonrpc = "2.0"
            method = "eth_getTransactionCount"
            params = @($Address, "latest")
            id = 1
        } | ConvertTo-Json
        
        $response = Invoke-RestMethod -Uri $RpcUrl -Method Post -Body $body -ContentType "application/json" -ErrorAction Stop
        $hex = $response.result
        
        if ($hex) {
            # Convert hex to decimal
            $hexValue = $hex.Substring(2)
            if ([string]::IsNullOrEmpty($hexValue)) {
                $nonce = 0
            } else {
                $nonce = [System.Convert]::ToUInt64($hexValue, 16)
            }
            
            # Set initial nonce
            if ($script:initialNonce -eq $null) {
                $script:initialNonce = $nonce
            }
            
            $diff = $null
            $diffText = ""
            $diffColor = "White"
            $currentTps = 0.0
            
            if ($script:previousNonce -ne $null) {
                $diff = $nonce - $script:previousNonce
                if ($diff -gt 0) {
                    $diffText = "+$diff (increased)"
                    $diffColor = "Green"
                    $script:totalTransactions += $diff
                    
                    # Calculate TPS
                    if ($script:previousTime -ne $null) {
                        $timeDiff = ($currentTime - $script:previousTime).TotalSeconds
                        if ($timeDiff -gt 0) {
                            $currentTps = $diff / $timeDiff
                            
                            # Add to history
                            $script:tpsHistory += $currentTps
                            if ($script:tpsHistory.Count -gt $script:maxTpsHistory) {
                                $script:tpsHistory = $script:tpsHistory[-$script:maxTpsHistory..-1]
                            }
                        }
                    }
                } elseif ($diff -lt 0) {
                    $diffText = "$diff (decreased)"
                    $diffColor = "Red"
                } else {
                    $diffText = "0 (no change)"
                    $diffColor = "Gray"
                }
            } else {
                $diffText = "First query"
                $diffColor = "Cyan"
            }
            
            if ($ClearScreen -and $script:iteration -gt 1) {
                Clear-Host
                Write-Host "Starting nonce monitor with TPS calculation..." -ForegroundColor Green
                Write-Host "Address: $Address" -ForegroundColor Cyan
                Write-Host "RPC: $RpcUrl" -ForegroundColor Cyan
                Write-Host "Interval: $Interval seconds" -ForegroundColor Cyan
                Write-Host "Press Ctrl+C to stop" -ForegroundColor Yellow
                Write-Host ""
            }
            
            Write-Host "===============================================" -ForegroundColor DarkGray
            Write-Host "Time: $timestamp" -ForegroundColor White
            Write-Host "Hex: $hex" -ForegroundColor Yellow
            Write-Host "Nonce: $nonce" -ForegroundColor Green
            Write-Host "Change: $diffText" -ForegroundColor $diffColor
            
            if ($currentTps -gt 0) {
                Write-Host "Current TPS: $([math]::Round($currentTps, 4))" -ForegroundColor Magenta
            }
            
            if ($script:previousNonce -ne $null) {
                Write-Host "Previous: $script:previousNonce" -ForegroundColor DarkGray
            }
            
            # Show running statistics
            $elapsed = $currentTime - $script:startTime
            if ($elapsed.TotalSeconds -gt 0 -and $script:totalTransactions -gt 0) {
                $avgTps = $script:totalTransactions / $elapsed.TotalSeconds
                Write-Host "Avg TPS: $([math]::Round($avgTps, 4))" -ForegroundColor Cyan
            }
            
            Write-Host "Total Txs: $script:totalTransactions" -ForegroundColor Yellow
            Write-Host "===============================================" -ForegroundColor DarkGray
            
            # Draw TPS chart if we have history
            if ($script:tpsHistory.Count -gt 0) {
                Draw-TpsChart -tpsValues $script:tpsHistory -width $ChartWidth
            }
            
            $script:previousNonce = $nonce
            $script:previousTime = $currentTime
        } else {
            Write-Host "[$timestamp] Query failed: No result returned" -ForegroundColor Red
        }
        
        Start-Sleep -Seconds $Interval
    }
    catch {
        Write-Host "[$timestamp] Query failed: $($_.Exception.Message)" -ForegroundColor Red
        Start-Sleep -Seconds $Interval
    }
}

