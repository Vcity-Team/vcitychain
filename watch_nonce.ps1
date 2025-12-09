# Monitor multiple account nonces with TPS calculation and chart
param(
    [Parameter(Mandatory=$false)]
    [string[]]$Addresses = @(
        "0x4BCBB0e87ff0Bd8c6bD4968617b17b2e2DC12EBe",
        "0x607C48332bdb69AdE0833678214A867D7a7392Ef",
        "0x6fF9038d8BcA8a3680B1FF81e4d6c0d76e975311",
        "0x32cb7D2e9931C692BB8399cfA692150F4dD7d3CE",
        "0xBCc9A6a75891eFAd1cc4e6498E4059D68eF85098"
    ),
    
    [Parameter(Mandatory=$false)]
    [string]$RpcUrl = "http://localhost:8545",
    
    [Parameter(Mandatory=$false)]
    [int]$Interval = 3,
    
    [Parameter(Mandatory=$false)]
    [switch]$ClearScreen = $true,
    
    [Parameter(Mandatory=$false)]
    [int]$ChartWidth = 40
)

# Per-account statistics tracking
$script:accounts = @{}
$script:startTime = Get-Date
$script:iteration = 0
$script:totalTransactions = 0
$script:totalTpsHistory = @()  # Combined TPS history for all accounts (no trimming)

# Initialize account tracking
foreach ($addr in $Addresses) {
    $script:accounts[$addr] = @{
        Address = $addr
        PreviousNonce = $null
        PreviousTime = $null
        InitialNonce = $null
        TotalTransactions = 0
        TpsHistory = @()
        CurrentTps = 0.0
    }
}

# Function to draw ASCII chart
function Draw-TpsChart {
    param(
        [array]$tpsValues,
        [int]$width = 40,
        [string]$title = "TPS Chart"
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
    Write-Host "$title (Last $($tpsValues.Count) intervals):" -ForegroundColor Cyan
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
    Write-Host "--- Per Account Statistics ---" -ForegroundColor Cyan
    
    $totalTxs = 0
    foreach ($addr in $script:accounts.Keys) {
        $acc = $script:accounts[$addr]
        if ($acc.InitialNonce -ne $null -and $acc.PreviousNonce -ne $null) {
            $accTxs = $acc.PreviousNonce - $acc.InitialNonce
            $totalTxs += $accTxs
            Write-Host "Account: $($addr.Substring(0, 16))..." -ForegroundColor White
            Write-Host "  Initial: $($acc.InitialNonce) | Final: $($acc.PreviousNonce) | Txs: $accTxs" -ForegroundColor Gray
            if ($acc.TpsHistory.Count -gt 0) {
                $avgTps = ($acc.TpsHistory | Measure-Object -Average).Average
                Write-Host "  Avg TPS: $([math]::Round($avgTps, 4))" -ForegroundColor Yellow
            }
        }
    }
    
    Write-Host ""
    Write-Host "--- Combined Statistics ---" -ForegroundColor Cyan
    Write-Host "Total Transactions: $totalTxs" -ForegroundColor Green
    
    if ($totalSeconds -gt 0 -and $totalTxs -gt 0) {
        $avgTps = $totalTxs / $totalSeconds
        Write-Host "Average TPS: $([math]::Round($avgTps, 4))" -ForegroundColor Yellow
    }
    
    if ($script:totalTpsHistory.Count -gt 0) {
        $maxTps = ($script:totalTpsHistory | Measure-Object -Maximum).Maximum
        $minTps = ($script:totalTpsHistory | Measure-Object -Minimum).Minimum
        $avgRecentTps = ($script:totalTpsHistory | Measure-Object -Average).Average
        Write-Host "Peak TPS:    $([math]::Round($maxTps, 4))" -ForegroundColor Red
        Write-Host "Min TPS:     $([math]::Round($minTps, 4))" -ForegroundColor DarkGray
        Write-Host "Recent Avg:  $([math]::Round($avgRecentTps, 4))" -ForegroundColor Cyan
    }
    
    Write-Host "Total Queries: $script:iteration" -ForegroundColor White
    Write-Host "===============================================" -ForegroundColor Green
}

# Register cleanup handler
$null = Register-EngineEvent PowerShell.Exiting -Action {
    Show-Statistics
}

Write-Host "Starting multi-account nonce monitor with TPS calculation..." -ForegroundColor Green
Write-Host "Monitoring $($Addresses.Count) accounts:" -ForegroundColor Cyan
foreach ($addr in $Addresses) {
    Write-Host "  - $addr" -ForegroundColor Gray
}
Write-Host "RPC: $RpcUrl" -ForegroundColor Cyan
Write-Host "Interval: $Interval seconds" -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop" -ForegroundColor Yellow
Write-Host ""

while ($true) {
    try {
        $script:iteration++
        $currentTime = Get-Date
        $timestamp = $currentTime.ToString("yyyy-MM-dd HH:mm:ss")
        $totalCurrentTps = 0.0
        $allNonces = @()
        
        # Query all accounts
        foreach ($addr in $Addresses) {
            try {
                $body = @{
                    jsonrpc = "2.0"
                    method = "eth_getTransactionCount"
                    params = @($addr, "latest")
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
                    
                    $acc = $script:accounts[$addr]
                    
                    # Set initial nonce
                    if ($acc.InitialNonce -eq $null) {
                        $acc.InitialNonce = $nonce
                    }
                    
                    $diff = $null
                    $currentTps = 0.0
                    
                    if ($acc.PreviousNonce -ne $null) {
                        $diff = $nonce - $acc.PreviousNonce
                        if ($diff -gt 0) {
                            $acc.TotalTransactions += $diff
                            $script:totalTransactions += $diff
                            
                            # Calculate TPS
                            if ($acc.PreviousTime -ne $null) {
                                $timeDiff = ($currentTime - $acc.PreviousTime).TotalSeconds
                                if ($timeDiff -gt 0) {
                                    $currentTps = $diff / $timeDiff
                                    $acc.CurrentTps = $currentTps
                                    $totalCurrentTps += $currentTps
                                    
                                    # Add to account history
                                    $acc.TpsHistory += $currentTps
                                    if ($acc.TpsHistory.Count -gt $script:maxTpsHistory) {
                                        $acc.TpsHistory = $acc.TpsHistory[-$script:maxTpsHistory..-1]
                                    }
                                }
                            }
                        }
                    }
                    
                    $acc.PreviousNonce = $nonce
                    $acc.PreviousTime = $currentTime
                    
                    $allNonces += @{
                        Address = $addr
                        Nonce = $nonce
                        Diff = $diff
                        Tps = $currentTps
                    }
                }
            }
            catch {
                Write-Host "[$timestamp] Query failed for $($addr.Substring(0, 16))...: $($_.Exception.Message)" -ForegroundColor Red
            }
        }
        
        # Add to total TPS history
        if ($totalCurrentTps -gt 0) {
            $script:totalTpsHistory += $totalCurrentTps
        }
        
        if ($ClearScreen -and $script:iteration -gt 1) {
            Clear-Host
            Write-Host "Starting multi-account nonce monitor with TPS calculation..." -ForegroundColor Green
            Write-Host "Monitoring $($Addresses.Count) accounts" -ForegroundColor Cyan
            Write-Host "RPC: $RpcUrl | Interval: $Interval seconds" -ForegroundColor Cyan
            Write-Host "Press Ctrl+C to stop" -ForegroundColor Yellow
            Write-Host ""
        }
        
        Write-Host "===============================================" -ForegroundColor DarkGray
        Write-Host "Time: $timestamp | Iteration: $script:iteration" -ForegroundColor White
        Write-Host "===============================================" -ForegroundColor DarkGray
        
        # Display per-account info
        foreach ($nonceInfo in $allNonces) {
            $addrShort = $nonceInfo.Address.Substring(0, 16) + "..."
            
            Write-Host "$addrShort : Nonce=$($nonceInfo.Nonce) " -NoNewline
            
            if ($nonceInfo.Diff -ne $null) {
                if ($nonceInfo.Diff -gt 0) {
                    Write-Host "Change=+$($nonceInfo.Diff) " -NoNewline -ForegroundColor Green
                } elseif ($nonceInfo.Diff -lt 0) {
                    Write-Host "Change=$($nonceInfo.Diff) " -NoNewline -ForegroundColor Red
                } else {
                    Write-Host "Change=0 " -NoNewline -ForegroundColor Gray
                }
            } else {
                Write-Host "Change=First " -NoNewline -ForegroundColor Cyan
            }
            
            if ($nonceInfo.Tps -gt 0) {
                Write-Host "TPS=$([math]::Round($nonceInfo.Tps, 2))" -ForegroundColor Magenta
            } else {
                Write-Host ""
            }
        }
        
        Write-Host "-----------------------------------------------" -ForegroundColor DarkGray
        
        # Show combined statistics
        if ($totalCurrentTps -gt 0) {
            Write-Host "Combined Current TPS: $([math]::Round($totalCurrentTps, 4))" -ForegroundColor Magenta
        }
        
        $elapsed = $currentTime - $script:startTime
        if ($elapsed.TotalSeconds -gt 0 -and $script:totalTransactions -gt 0) {
            $avgTps = $script:totalTransactions / $elapsed.TotalSeconds
            Write-Host "Combined Avg TPS: $([math]::Round($avgTps, 4))" -ForegroundColor Cyan
        }
        
        Write-Host "Total Transactions: $script:totalTransactions" -ForegroundColor Yellow
        Write-Host "===============================================" -ForegroundColor DarkGray
        
        # Draw combined TPS chart
        if ($script:totalTpsHistory.Count -gt 0) {
            Draw-TpsChart -tpsValues $script:totalTpsHistory -width $ChartWidth -title "Combined TPS Chart"
        }
        
        Start-Sleep -Seconds $Interval
    }
    catch {
        Write-Host "[$timestamp] Error: $($_.Exception.Message)" -ForegroundColor Red
        Start-Sleep -Seconds $Interval
    }
}

