package min_threshold

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

// GetCommand returns the min-threshold command
func GetCommand() *cobra.Command {
	minThresholdCmd := &cobra.Command{
		Use:   "min-threshold",
		Short: "Query the minimum voting threshold",
		Long:  "Query the minimum voting threshold required to participate in DPoS governance voting",
		Run:   runCommand,
	}

	minThresholdCmd.Flags().String("server", "http://localhost:8545", "JSON-RPC server address")
	helper.RegisterJSONOutputFlag(minThresholdCmd)

	return minThresholdCmd
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	server, _ := cmd.Flags().GetString("server")

	// 调用JSON-RPC获取最小投票门槛
	result, err := callJSONRPC(server, "dpos_getMinVotingThreshold", []interface{}{})
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to call JSON-RPC: %w", err))
		return
	}

	// 创建CommandResult
	commandResult := &MinThresholdResult{
		Data: result.(map[string]interface{}),
	}
	outputter.SetCommandResult(commandResult)
}

func callJSONRPC(server, method string, params []interface{}) (interface{}, error) {
	// 构建JSON-RPC请求
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}

	// 序列化请求
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 发送HTTP请求
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Post(server, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("RPC调用失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// 解析响应
	var jsonResp map[string]interface{}
	if err := json.Unmarshal(responseBody, &jsonResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// 检查RPC错误
	if errorObj, exists := jsonResp["error"]; exists && errorObj != nil {
		if errorMap, ok := errorObj.(map[string]interface{}); ok {
			if message, ok := errorMap["message"].(string); ok {
				return nil, fmt.Errorf("RPC调用失败: RPC错误: %s", message)
			}
		}
		return nil, fmt.Errorf("RPC调用失败: RPC错误")
	}

	// 返回结果
	if result, exists := jsonResp["result"]; exists {
		return result, nil
	}

	return nil, fmt.Errorf("no result in response")
}
