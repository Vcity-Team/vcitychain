@echo off
echo Testing DPoS Vote Fix v9 - Private Key Signing...
echo.

echo Starting node with DPoS consensus...
start /B cmd /c "main.exe server --chain ./genesis.json --data-dir ./data --libp2p 127.0.0.1:1478 --json-rpc 127.0.0.1:10002 --block-gas-target 10000000 --seal"

echo Waiting for node to start...
timeout /t 10 /nobreak >nul

echo.
echo Testing DPoS vote command with private key signing...
echo Command: main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

main.exe dpos vote --chain-id 888 --voter 0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C --candidate 0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E --amount 1000000000000000000000

echo.
echo Test completed. Check the logs above for:
echo 1. Balance retrieval success
echo 2. Initial transaction hash
echo 3. "Signing transaction with private key..." message
echo 4. "Transaction signed successfully" with R, S, V values
echo 5. "Transaction signed and hash recalculated" with new hash
echo 6. "Adding transaction to pool for DPoS voting history..." message
echo 7. "Store implements AddTx interface, attempting to add transaction..."
echo 8. "Transaction added to pool successfully"
echo 9. DPoS state update status
echo 10. Final status
echo.
echo Expected Results:
echo - Should see: "Signing transaction with private key..."
echo - Should see: "Transaction signed successfully" with R, S, V values
echo - Should see: "Transaction signed and hash recalculated" with new hash
echo - Should see: "Adding transaction to pool for DPoS voting history..."
echo - Should see: "Store implements AddTx interface, attempting to add transaction..."
echo - Should see: "Transaction added to pool successfully"
echo - Should see: "Updating DPoS state..."
echo - Should see: "Store has GetConsensus method, attempting to get consensus engine..."
echo - Should see consensus engine type and methods
echo - txAdded should be: true
echo - No more signature errors
echo.
echo Stopping node...
taskkill /f /im main.exe >nul 2>&1
echo Node stopped.

