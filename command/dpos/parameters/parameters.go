package parameters

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

// GetCommand returns the parameters command
func GetCommand() *cobra.Command {
	paramsCmd := &cobra.Command{
		Use:   "parameters",
		Short: "Get DPoS votable parameters information",
		Long:  "Get current votable parameters list and their details",
		Run:   runCommand,
	}

	helper.RegisterJSONOutputFlag(paramsCmd)

	return paramsCmd
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// 调用JSON-RPC获取可表决参数
	result, err := callJSONRPC("dpos_getVotableCurrentParameters", []interface{}{})
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to call JSON-RPC: %w", err))
		return
	}

	// 创建CommandResult
	commandResult := &ParametersResult{
		Data: result.(map[string]interface{}),
	}
	outputter.SetCommandResult(commandResult)
}

func callJSONRPC(method string, params []interface{}) (interface{}, error) {
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

	// 发送HTTP请求到本地JSON-RPC服务器
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 尝试多个可能的端口
	ports := []string{"8545", "9632", "8080"}
	var lastErr error

	for _, port := range ports {
		url := fmt.Sprintf("http://localhost:%s", port)
		
		resp, err := client.Post(url, "application/json", bytes.NewBuffer(requestBody))
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		// 读取响应
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}

		// 解析响应
		var response map[string]interface{}
		if err := json.Unmarshal(responseBody, &response); err != nil {
			lastErr = err
			continue
		}

		// 检查是否有错误
		if errorData, exists := response["error"]; exists {
			return nil, fmt.Errorf("JSON-RPC error: %v", errorData)
		}

		// 返回结果
		if result, exists := response["result"]; exists {
			return result, nil
		}

		// 如果没有result字段，返回整个响应
		return response, nil
	}

	// 如果所有端口都失败，返回最后一个错误
	if lastErr != nil {
		return nil, fmt.Errorf("failed to connect to JSON-RPC server on any port %v: %w", ports, lastErr)
	}

	return nil, fmt.Errorf("no response from JSON-RPC server")
}
