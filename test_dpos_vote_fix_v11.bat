@echo off
echo Testing DPoS Vote Fix v11 - Private Key Loading Debug...
echo.

echo Starting node with DPoS consensus...
start /B cmd /c "main.exe server --chain ./genesis.json --data-dir ./data --libp2p 127.0.0.1:1478 --jsonrpc 127.0.0.1:10002 --block-gas-target 10000000 --seal"

echo Waiting for node to start...
timeout /t 10 /nobreak >nul

echo.
echo Testing DPoS vote command with private key loading debug...
echo Command: main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

echo.
echo Test completed. Check the logs above for:
echo 1. "=== NEWDPOS FUNCTION CALLED ===" message
echo 2. "=== PRIVATE KEY INITIALIZATION START ===" message
echo 3. "Initializing DPoS endpoint with private key" message
echo 4. "=== PRIVATE KEY DECODE SUCCESS ===" message
echo 5. "=== PRIVATE KEY CREATION SUCCESS ===" message
echo 6. "=== DPOS ENDPOINT CREATED WITH PRIVATE KEY ===" message
echo 7. Balance retrieval success
echo 8. Initial transaction hash
echo 9. "Signing transaction with private key..." message
echo 10. "Private key is available" message
echo 11. Transaction signing success
echo 12. Transaction pool integration
echo.
echo Expected Results:
echo - Should see: "=== NEWDPOS FUNCTION CALLED ==="
echo - Should see: "=== PRIVATE KEY INITIALIZATION START ==="
echo - Should see: "=== PRIVATE KEY DECODE SUCCESS ==="
echo - Should see: "=== PRIVATE KEY CREATION SUCCESS ==="
echo - Should see: "=== DPOS ENDPOINT CREATED WITH PRIVATE KEY ==="
echo - Should see: "Signing transaction with private key..."
echo - Should see: "Private key is available"
echo - Should see: "Transaction signed successfully"
echo - Should see: "Transaction added to pool successfully"
echo - No more "private key not available" errors
echo.
echo Stopping node...
taskkill /f /im main.exe >nul 2>&1
echo Node stopped.
