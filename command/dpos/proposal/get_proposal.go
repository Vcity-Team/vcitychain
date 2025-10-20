package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// GetProposalResponse 获取提案响应
type GetProposalResponse struct {
	Success  bool                   `json:"success"`
	Proposal map[string]interface{} `json:"proposal,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// GetProposalCommand 获取提案命令
func GetProposalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "获取提案信息",
		Long:  "根据提案ID获取提案的详细信息",
		RunE:  runGetProposal,
	}

	cmd.Flags().String("proposal-id", "", "提案ID (必需)")
	cmd.MarkFlagRequired("proposal-id")

	return cmd
}

func runGetProposal(cmd *cobra.Command, args []string) error {
	// 获取参数
	proposalID, _ := cmd.Flags().GetString("proposal-id")

	// 调用RPC
	response, err := callGetProposalRPC(proposalID)
	if err != nil {
		return fmt.Errorf("RPC调用失败: %w", err)
	}

	// 输出结果
	output, _ := json.MarshalIndent(response, "", "  ")
	fmt.Println(string(output))

	return nil
}

func callGetProposalRPC(proposalID string) (*GetProposalResponse, error) {
	// 构建JSON-RPC请求
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getParameterProposal",
		"params":  []string{proposalID},
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
			Result GetProposalResponse `json:"result"`
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
