package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// CreateProposalRequest 创建提案请求
type CreateProposalRequest struct {
	Parameter   string      `json:"parameter"`
	NewValue    interface{} `json:"newValue"`
	Description string      `json:"description"`
	Proposer    string      `json:"proposer"`
}

// CreateProposalResponse 创建提案响应
type CreateProposalResponse struct {
	Success    bool   `json:"success"`
	ProposalID string `json:"proposalId,omitempty"`
	Error      string `json:"error,omitempty"`
	Message    string `json:"message,omitempty"`
}

// GetCreateCommand 获取创建提案命令
func GetCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-proposal",
		Short: "创建参数提案",
		Long:  "创建一个新的参数修改提案",
		RunE:  runCreateProposal,
	}

	cmd.Flags().String("parameter", "", "要修改的参数名称 (必需)")
	cmd.Flags().String("new-value", "", "新的参数值 (必需)")
	cmd.Flags().String("description", "", "提案描述 (必需)")
	cmd.Flags().String("proposer", "", "提案者地址 (可选)")

	cmd.MarkFlagRequired("parameter")
	cmd.MarkFlagRequired("new-value")
	cmd.MarkFlagRequired("description")

	return cmd
}

func runCreateProposal(cmd *cobra.Command, args []string) error {
	// 获取参数
	parameter, _ := cmd.Flags().GetString("parameter")
	newValueStr, _ := cmd.Flags().GetString("new-value")
	description, _ := cmd.Flags().GetString("description")
	proposer, _ := cmd.Flags().GetString("proposer")

	// 解析新值
	var newValue interface{}
	if err := json.Unmarshal([]byte(newValueStr), &newValue); err != nil {
		// 如果不是JSON格式，直接使用字符串
		newValue = newValueStr
	}

	// 构建请求
	request := CreateProposalRequest{
		Parameter:   parameter,
		NewValue:    newValue,
		Description: description,
		Proposer:    proposer,
	}

	// 调用RPC
	response, err := callCreateProposalRPC(request)
	if err != nil {
		return fmt.Errorf("RPC调用失败: %w", err)
	}

	// 输出结果
	output, _ := json.MarshalIndent(response, "", "  ")
	fmt.Println(string(output))

	return nil
}

func callCreateProposalRPC(request CreateProposalRequest) (*CreateProposalResponse, error) {
	// 构建JSON-RPC请求
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_createParameterProposal",
		"params":  []interface{}{request.Parameter, request.NewValue, request.Description, request.Proposer},
		"id":      1,
	}

	// 序列化请求
	requestBody, err := json.Marshal(rpcRequest)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 尝试多个端口
	ports := []string{"8545", "9632", "8080"}
	for _, port := range ports {
		url := fmt.Sprintf("http://localhost:%s", port)

		// 发送HTTP请求
		resp, err := http.Post(url, "application/json",
			bytes.NewBuffer(requestBody))
		if err != nil {
			fmt.Printf("尝试端口 %s 失败: %v\n", port, err)
			continue
		}
		defer resp.Body.Close()

		// 读取响应
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("读取端口 %s 响应失败: %v\n", port, err)
			continue
		}

		// 解析响应
		var rpcResponse struct {
			Result CreateProposalResponse `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.Unmarshal(body, &rpcResponse); err != nil {
			fmt.Printf("解析端口 %s 响应失败: %v\n", port, err)
			continue
		}

		if rpcResponse.Error != nil {
			return nil, fmt.Errorf("RPC错误: %s", rpcResponse.Error.Message)
		}

		return &rpcResponse.Result, nil
	}

	return nil, fmt.Errorf("所有端口都无法连接")
}
