#!/bin/bash

# DPoS经济系统测试脚本
echo "=== DPoS经济系统接口测试 ==="

# 测试JSON-RPC接口
echo "1. 测试JSON-RPC接口..."

# 获取当前Epoch信息
echo "测试 dpos_getCurrentEpochInfo..."
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getCurrentEpochInfo","params":[],"id":1}' \
  http://localhost:8545

echo -e "\n\n测试 dpos_getEpochInfoByNumber..."
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getEpochInfoByNumber","params":[1],"id":2}' \
  http://localhost:8545

echo -e "\n\n测试 dpos_getValidatorBlockStats..."
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorBlockStats","params":["0x1234567890123456789012345678901234567890",1],"id":3}' \
  http://localhost:8545

echo -e "\n\n测试 dpos_getValidatorRewardsInfo..."
curl -X POST \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"dpos_getValidatorRewardsInfo","params":["0x1234567890123456789012345678901234567890",1],"id":4}' \
  http://localhost:8545

echo -e "\n\n2. 测试CLI命令..."

# 测试CLI命令
echo "测试 ./main dpos epoch..."
./main dpos epoch

echo -e "\n\n测试 ./main dpos epoch 1..."
./main dpos epoch 1

echo -e "\n\n测试 ./main dpos stats..."
./main dpos stats 0x1234567890123456789012345678901234567890 1

echo -e "\n\n测试 ./main dpos rewards..."
./main dpos rewards 0x1234567890123456789012345678901234567890 1

echo -e "\n\n=== 测试完成 ==="

