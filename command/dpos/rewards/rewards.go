package rewards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

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
	helper.RegisterJSONRPCFlag(rewardsCmd)

	return rewardsCmd
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	validatorAddress := args[0]
	epochNumberStr := args[1]

	// 解析epochNumber
	epochNumber, err := strconv.ParseUint(epochNumberStr, 10, 64)
	if err != nil {
		outputter.SetError(fmt.Errorf("invalid epoch number: %w", err))
		return
	}

	// 获取JSON-RPC地址
	jsonrpcAddress := helper.GetJSONRPCAddress(cmd)

	// 构建JSON-RPC请求
	method := "dpos_getValidatorRewardsInfo"
	params := []interface{}{validatorAddress, epochNumber}

	// 调用JSON-RPC
	result, err := callJSONRPC(jsonrpcAddress, method, params)
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to call JSON-RPC: %w", err))
		return
	}

	// 创建CommandResult
	commandResult := &ValidatorRewardsResult{
		Data: result,
	}
	outputter.SetCommandResult(commandResult)
}

// JSONRPCRequest represents a JSON-RPC request
type JSONRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

// JSONRPCResponse represents a JSON-RPC response
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      int         `json:"id"`
}

// JSONRPCError represents a JSON-RPC error
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func callJSONRPC(jsonrpcAddress, method string, params []interface{}) (interface{}, error) {
	// 构建JSON-RPC请求
	request := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	// 序列化请求
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	// 发送HTTP请求
	resp, err := http.Post(jsonrpcAddress, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// 解析响应
	var response JSONRPCResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// 检查错误
	if response.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error: %s (code: %d)", response.Error.Message, response.Error.Code)
	}

	return response.Result, nil
}
