#!/bin/bash

# DPoS JSON-RPC接口测试脚本
# 使用方法: ./test_dpos_jsonrpc.sh [JSON-RPC_URL]

JSON_RPC_URL=${1:-"http://localhost:8545"}

echo "🧪 测试DPoS JSON-RPC接口"
echo "📡 JSON-RPC URL: $JSON_RPC_URL"
echo ""

# 测试函数
test_rpc() {
    local method=$1
    local params=$2
    local description=$3
    
    echo "🔍 测试: $description"
    echo "📤 方法: $method"
    echo "📤 参数: $params"
    
    response=$(curl -s -X POST \
        -H "Content-Type: application/json" \
        -d "{
            \"jsonrpc\": \"2.0\",
            \"method\": \"$method\",
            \"params\": $params,
            \"id\": 1
        }" \
        "$JSON_RPC_URL")
    
    echo "📥 响应: $response"
    echo ""
}

# 1. 测试获取当前Epoch信息
test_rpc "dpos_getCurrentEpoch" "[]" "获取当前Epoch信息"

# 2. 测试获取指定Epoch信息
test_rpc "dpos_getEpochInfo" "[5]" "获取Epoch 5信息"

# 3. 测试获取所有验证者
test_rpc "dpos_getAllValidators" "[]" "获取所有验证者"

# 4. 测试获取验证者集合
test_rpc "dpos_getValidatorSet" "[]" "获取验证者集合"

# 5. 测试获取验证者出块统计
test_rpc "dpos_getValidatorBlockStats" "[\"0x1234567890123456789012345678901234567890\", 5]" "获取验证者出块统计"

# 6. 测试获取所有验证者出块统计
test_rpc "dpos_getAllValidatorStats" "[5]" "获取所有验证者出块统计"

# 7. 测试获取验证者奖励
test_rpc "dpos_getValidatorRewards" "[\"0x1234567890123456789012345678901234567890\", 5]" "获取验证者奖励"

# 8. 测试获取所有验证者奖励
test_rpc "dpos_getAllValidatorRewards" "[5]" "获取所有验证者奖励"

# 9. 测试获取质押信息
test_rpc "dpos_getStakingInfo" "[]" "获取质押信息"

# 10. 测试获取共识状态
test_rpc "dpos_getConsensusState" "[]" "获取共识状态"

# 11. 测试获取当前轮次
test_rpc "dpos_getCurrentRound" "[]" "获取当前轮次"

# 12. 测试获取当前委托者
test_rpc "dpos_getCurrentDelegate" "[]" "获取当前委托者"

echo "✅ 所有测试完成！"
echo ""
echo "💡 提示:"
echo "   - 如果看到 'DPoS engine not available' 错误，说明DPoS引擎未初始化"
echo "   - 如果看到连接错误，请确保节点正在运行并监听 $JSON_RPC_URL"
echo "   - 某些方法可能需要真实的验证者地址，请替换为实际的地址"


