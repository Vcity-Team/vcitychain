package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// ListActiveProposalsResponse RPC响应结构
type ListActiveProposalsResponse struct {
	Success   bool                     `json:"success"`
	Count     int                      `json:"count"`
	Proposals []map[string]interface{} `json:"proposals"`
	Error     string                   `json:"error,omitempty"`
}

// GetActiveListCommand 列出活跃提案
func GetActiveListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-active",
		Short: "查看所有活跃提案",
		Long:  "通过 dpos_getActiveProposals RPC 查询当前所有仍处于 pending/active 状态的提案",
		RunE: func(cmd *cobra.Command, args []string) error {
			response, err := callListActiveProposalsRPC()
			if err != nil {
				return fmt.Errorf("RPC调用失败: %w", err)
			}

			output, _ := json.MarshalIndent(response, "", "  ")
			fmt.Println(string(output))
			return nil
		},
	}

	return cmd
}

func callListActiveProposalsRPC() (*ListActiveProposalsResponse, error) {
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getActiveProposals",
		"params":  []interface{}{},
		"id":      1,
	}

	requestBody, err := json.Marshal(rpcRequest)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	ports := []string{"8545", "9632", "8080"}
	for _, port := range ports {
		url := fmt.Sprintf("http://localhost:%s", port)

		resp, err := http.Post(url, "application/json", bytes.NewBuffer(requestBody))
		if err != nil {
			fmt.Printf("尝试端口 %s 失败: %v\n", port, err)
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("读取端口 %s 响应失败: %v\n", port, err)
			continue
		}

		var rpcResponse struct {
			Result ListActiveProposalsResponse `json:"result"`
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
