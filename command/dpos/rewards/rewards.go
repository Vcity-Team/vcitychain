package rewards

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

// GetCommand returns the rewards command
func GetCommand() *cobra.Command {
	rewardsCmd := &cobra.Command{
		Use:   "rewards <validatorAddress> <epochNumber>",
		Short: "Get validator reward information",
		Long:  "Get reward information for a specific validator in a specific epoch",
		Args:  cobra.ExactArgs(2),
		Run:   runCommand,
	}

	helper.RegisterJSONOutputFlag(rewardsCmd)

	return rewardsCmd
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	validatorAddress := args[0]
	epochNumberStr := args[1]

	// 解析epoch号
	epochNumber, err := strconv.ParseUint(epochNumberStr, 10, 64)
	if err != nil {
		outputter.SetError(fmt.Errorf("invalid epoch number: %w", err))
		return
	}

	// 验证地址格式
	if !isValidAddress(validatorAddress) {
		outputter.SetError(fmt.Errorf("invalid validator address: %s", validatorAddress))
		return
	}

	// 获取奖励信息
	rewardsInfo, err := getValidatorRewards(validatorAddress, epochNumber)
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to get validator rewards: %w", err))
		return
	}

	// 创建CommandResult
	commandResult := &ValidatorRewardsResult{
		Data: rewardsInfo,
	}
	outputter.SetCommandResult(commandResult)
}

// getValidatorRewards 获取验证者奖励信息
func getValidatorRewards(validatorAddress string, epochNumber uint64) (map[string]interface{}, error) {
	// 通过JSON-RPC调用本地节点获取真实奖励数据
	jsonrpcURL := "http://localhost:8545"
	
	// 构建JSON-RPC请求
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getValidatorRewardsInfo",
		"params":  []interface{}{validatorAddress, epochNumber},
		"id":      1,
	}

	// 序列化请求
	requestData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	// 创建HTTP客户端
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// 发送请求
	resp, err := client.Post(jsonrpcURL, "application/json", strings.NewReader(string(requestData)))
	if err != nil {
		// 如果JSON-RPC调用失败，返回模拟数据
		return getMockRewardsData(validatorAddress, epochNumber), nil
	}
	defer resp.Body.Close()

	// 解析响应
	var jsonrpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&jsonrpcResp); err != nil {
		// 如果解析失败，返回模拟数据
		return getMockRewardsData(validatorAddress, epochNumber), nil
	}

	// 检查是否有错误
	if err, exists := jsonrpcResp["error"]; exists {
		return map[string]interface{}{
			"validatorAddress": validatorAddress,
			"epochNumber":      epochNumber,
			"error":            err,
			"status":           "jsonrpc_error",
		}, nil
	}

	// 返回结果
	if result, exists := jsonrpcResp["result"]; exists {
		if resultMap, ok := result.(map[string]interface{}); ok {
			return resultMap, nil
		}
	}

	// 如果结果格式不对，返回模拟数据
	return getMockRewardsData(validatorAddress, epochNumber), nil
}

// getMockRewardsData 返回模拟奖励数据
func getMockRewardsData(validatorAddress string, epochNumber uint64) map[string]interface{} {
	return map[string]interface{}{
		"validatorAddress": validatorAddress,
		"epochNumber":      epochNumber,
		"blockRewards": map[string]interface{}{
			"totalBlocks":      0,
			"rewardPerBlock":   "0",
			"totalBlockReward": "0",
		},
		"votingRewards": map[string]interface{}{
			"totalVotes":      0,
			"rewardPerVote":   "0",
			"totalVoteReward": "0",
		},
		"delegationRewards": map[string]interface{}{
			"totalStake":       "0",
			"rewardPerStake":   "0",
			"totalStakeReward": "0",
		},
		"totalRewards": "0",
		"status":       "mock_data",
		"message":      "Using mock data - local node not available or method not implemented",
		"note":         "Start local node with --jsonrpc-addr 0.0.0.0:8545 to get real data",
	}
}

// isValidAddress 简单的地址格式验证
func isValidAddress(address string) bool {
	// 检查是否以0x开头
	if !strings.HasPrefix(address, "0x") {
		return false
	}

	// 检查长度（0x + 40个十六进制字符）
	if len(address) != 42 {
		return false
	}

	// 检查是否都是有效的十六进制字符
	hexPart := address[2:]
	for _, char := range hexPart {
		if !((char >= '0' && char <= '9') ||
			(char >= 'a' && char <= 'f') ||
			(char >= 'A' && char <= 'F')) {
			return false
		}
	}

	return true
}
