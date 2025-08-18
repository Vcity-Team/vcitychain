@echo off
echo Testing DPoS Vote Fix v10 - Private Key Loading Fix...
echo.

echo Starting node with DPoS consensus...
start /B cmd /c "main.exe server --chain ./genesis.json --data-dir ./data --libp2p 127.0.0.1:1478 --json-rpc 127.0.0.1:10002 --block-gas-target 10000000 --seal"

echo Waiting for node to start...
timeout /t 10 /nobreak >nul

echo.
echo Testing DPoS vote command with private key loading fix...
echo Command: main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

echo.
echo Test completed. Check the logs above for:
echo 1. "Initializing DPoS endpoint with private key" message
echo 2. "Private key decoded successfully" message
echo 3. "Private key created successfully" message
echo 4. Balance retrieval success
echo 5. Initial transaction hash
echo 6. "Signing transaction with private key..." message
echo 7. "Private key is available" message
echo 8. "Transaction hash for signing" message
echo 9. "Transaction signed successfully" with signature length
echo 10. "Transaction signed successfully" with R, S, V values
echo 11. "Transaction signed and hash recalculated" with new hash
echo 12. Transaction pool integration
echo 13. DPoS state update
echo.
echo Expected Results:
echo - Should see: "Initializing DPoS endpoint with private key"
echo - Should see: "Private key decoded successfully"
echo - Should see: "Private key created successfully"
echo - Should see: "Signing transaction with private key..."
echo - Should see: "Private key is available"
echo - Should see: "Transaction signed successfully" with R, S, V values
echo - Should see: "Transaction signed and hash recalculated"
echo - Should see: "Transaction added to pool successfully"
echo - No more "private key not available" errors
echo.
echo Stopping node...
taskkill /f /im main.exe >nul 2>&1
echo Node stopped.
