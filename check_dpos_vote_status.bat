@echo off
echo Checking DPoS Vote Status...
echo.

set TX_HASH=0xa517deef17ba56cbbb62a764114c2a331d2325a424bd56f43260b9de4dacbd92
set VOTER_ADDR=0x860072c3A6860Dd1F0a6592fA6F93AE9E69b4F8C
set CANDIDATE_ADDR=0xBbb79Ca6d1402FFa5A8023C8767770B8e62c2F5E

echo Transaction Hash: %TX_HASH%
echo Voter Address: %VOTER_ADDR%
echo Candidate Address: %CANDIDATE_ADDR%
echo.

echo 1. Checking transaction status...
curl -X POST -H "Content-Type: application/json" --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionByHash\",\"params\":[\"%TX_HASH%\"],\"id\":1}" http://localhost:10002
echo.
echo.

echo 2. Checking transaction receipt...
curl -X POST -H "Content-Type: application/json" --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionReceipt\",\"params\":[\"%TX_HASH%\"],\"id\":1}" http://localhost:10002
echo.
echo.

echo 3. Checking DPoS consensus state...
curl -X POST -H "Content-Type: application/json" --data "{\"jsonrpc\":\"2.0\",\"method\":\"dpos_getConsensusState\",\"params\":[],\"id\":1}" http://localhost:10002
echo.
echo.

echo 4. Checking latest block...
curl -X POST -H "Content-Type: application/json" --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"latest\",true],\"id\":1}" http://localhost:10002
echo.
echo.

echo 5. Checking voter account balance...
curl -X POST -H "Content-Type: application/json" --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"%VOTER_ADDR%\",\"latest\"],\"id\":1}" http://localhost:10002
echo.
echo.

echo 6. Using DPoS CLI commands...
echo.
echo Current Round:
main.exe dpos getCurrentRound --chain-id 888
echo.
echo Current Delegate:
main.exe dpos getCurrentDelegate --chain-id 888
echo.
echo Voting-Staking Info:
main.exe dpos voting-staking-info --chain-id 888
echo.

echo Check completed!
echo.
echo Expected Results:
echo - Transaction should be in pool or confirmed
echo - DPoS state should show updated voting power
echo - Voter balance should reflect the vote amount
echo - Current delegate should potentially change
