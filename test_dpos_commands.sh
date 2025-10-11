#!/bin/bash

echo "=== 测试DPoS经济系统命令（真实状态数据）==="

# 测试当前epoch信息
echo "1. 测试当前epoch信息:"
./main dpos epoch

echo -e "\n2. 测试指定epoch信息:"
./main dpos epoch --number 1

echo -e "\n3. 测试验证者出块统计:"
./main dpos stats --validator 0x1234567890123456789012345678901234567890 --epoch 1

echo -e "\n4. 测试验证者奖励信息:"
./main dpos rewards --validator 0x1234567890123456789012345678901234567890 --epoch 1

echo -e "\n=== 测试JSON-RPC接口（真实状态数据）==="

# 测试JSON-RPC接口
echo "5. 测试JSON-RPC - 当前epoch:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getCurrentEpoch","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n6. 测试JSON-RPC - 指定epoch:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochInfo","params":[1],"id":1}' \
  http://localhost:8545

echo -e "\n7. 测试JSON-RPC - 验证者出块统计:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorBlockStats","params":["0x1234567890123456789012345678901234567890",1],"id":1}' \
  http://localhost:8545

echo -e "\n8. 测试JSON-RPC - 验证者奖励:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorRewards","params":["0x1234567890123456789012345678901234567890",1],"id":1}' \
  http://localhost:8545

echo -e "\n9. 测试JSON-RPC - 所有验证者:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidators","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n10. 测试JSON-RPC - 奖励账户信息:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getRewardAccountInfo","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n11. 测试JSON-RPC - 验证者账户信息:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorAccountInfo","params":["0x1234567890123456789012345678901234567890"],"id":1}' \
  http://localhost:8545

echo -e "\n12. 测试JSON-RPC - 质押信息:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getStakingInfo","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n13. 测试JSON-RPC - 共识状态:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getConsensusState","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n14. 测试JSON-RPC - 当前轮次:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getCurrentRound","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n15. 测试JSON-RPC - 当前委托者:"
curl -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getCurrentDelegate","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n=== 测试完成 ==="


