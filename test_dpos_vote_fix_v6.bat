@echo off
echo Testing DPoS Vote Fix v6 - Bypass Transaction Pool...
echo.

echo Starting node with DPoS consensus...
start /B cmd /c "main.exe server --chain ./genesis.json --data-dir ./data --libp2p 127.0.0.1:1478 --json-rpc 127.0.0.1:10002 --block-gas-target 10000000 --seal"

echo Waiting for node to start...
timeout /t 10 /nobreak >nul

echo.
echo Testing DPoS vote command with transaction pool bypass...
echo Command: main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

echo.
echo Test completed. Check the logs above for:
echo 1. Balance retrieval success
echo 2. Valid transaction hash (not 0x0000...)
echo 3. "DPoS voting bypasses transaction pool" message
echo 4. DPoS state update attempt
echo 5. Final status
echo.
echo Expected Results:
echo - Should see: "DPoS voting bypasses transaction pool - updating state directly..."
echo - Should see: "Updating DPoS state..."
echo - Should see: "Store has GetConsensus method" (if implemented)
echo - Should see: "DPoS state updated successfully" (if consensus engine supports it)
echo - No more transaction pool errors
echo.
echo Stopping node...
taskkill /f /im main.exe >nul 2>&1
echo Node stopped.
