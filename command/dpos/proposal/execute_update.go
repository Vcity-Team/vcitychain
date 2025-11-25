package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// ExecuteUpdateResponse 执行更新响应
type ExecuteUpdateResponse struct {
	Success           bool   `json:"success"`
	Error             string `json:"error,omitempty"`
	Message           string `json:"message,omitempty"`
	TxHash            string `json:"txHash,omitempty"`
	ProposalId        string `json:"proposalId,omitempty"`
	Executor          string `json:"executor,omitempty"`
	CurrentBlockNumber uint64 `json:"currentBlockNumber,omitempty"`
	Note              string `json:"note,omitempty"`
}

// GetExecuteUpdateCommand 获取执行更新命令
func GetExecuteUpdateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "执行参数更新",
		Long:  "执行已通过的参数提案更新",
		RunE:  runExecuteUpdate,
	}

	cmd.Flags().String("proposal-id", "", "提案ID (必需)")
	cmd.MarkFlagRequired("proposal-id")

	// 新增：执行者与私钥
	cmd.Flags().String("executor", "", "执行者地址 (必需)")
	cmd.Flags().String("executor-private-key", "", "执行者私钥 (hex, 64位, 必需)")
	cmd.MarkFlagRequired("executor")
	cmd.MarkFlagRequired("executor-private-key")

	return cmd
}

func runExecuteUpdate(cmd *cobra.Command, args []string) error {
	// 获取参数
	proposalID, _ := cmd.Flags().GetString("proposal-id")
	executor, _ := cmd.Flags().GetString("executor")
	executorPrivKey, _ := cmd.Flags().GetString("executor-private-key")

	// 调用RPC
	response, err := callExecuteUpdateRPC(proposalID, executor, executorPrivKey)
	if err != nil {
		return fmt.Errorf("RPC调用失败: %w", err)
	}

	// 输出结果
	output, _ := json.MarshalIndent(response, "", "  ")
	fmt.Println(string(output))

	return nil
}

func callExecuteUpdateRPC(proposalID, executor, executorPrivKey string) (*ExecuteUpdateResponse, error) {
	// 构建JSON-RPC请求
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_executeParameterUpdate",
		// 与 RPC 端保持一致，传入 [proposalID, executor, executorPrivateKey]
		"params": []string{proposalID, executor, executorPrivKey},
		"id":     1,
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
			Result ExecuteUpdateResponse `json:"result"`
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
