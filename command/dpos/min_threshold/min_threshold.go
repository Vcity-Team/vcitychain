package min_threshold

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

// MinThresholdResult 最小投票门槛查询结果
type MinThresholdResult struct {
	Success   bool   `json:"success"`
	Threshold string `json:"threshold"`
	Message   string `json:"message"`
}

// GetMinVotingThreshold 获取最小投票门槛
func GetMinVotingThreshold(server string) (*MinThresholdResult, error) {
	// 构建JSON-RPC请求
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getMinVotingThreshold",
		"params":  []interface{}{},
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

	resp, err := client.Post(server, "application/json",
		helper.NewJSONReader(requestBody))
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

	// 解析结果
	resultData, exists := jsonResp["result"]
	if !exists {
		return nil, fmt.Errorf("no result in response")
	}

	resultBytes, err := json.Marshal(resultData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var result MinThresholdResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}
