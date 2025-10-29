package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// CreateRecoveryRequest 创建恢复提案请求
type CreateRecoveryRequest struct {
	ValidatorAddress string `json:"validatorAddress"`
	RecoveryReason   string `json:"recoveryReason"`
	Description      string `json:"description"`
	Proposer         string `json:"proposer"`
}

// CreateRecoveryResponse 创建恢复提案响应
type CreateRecoveryResponse struct {
	Success    bool   `json:"success"`
	ProposalID string `json:"proposalId,omitempty"`
	Error      string `json:"error,omitempty"`
	Message    string `json:"message,omitempty"`
}

// GetCreateRecoveryCommand 获取创建恢复提案命令
func GetCreateRecoveryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-recovery",
		Short: "创建验证者恢复提案",
		Long:  "为故障验证者创建一个恢复提案，申请恢复出块权限",
		RunE:  runCreateRecovery,
	}

	cmd.Flags().String("validator", "", "要恢复的验证者地址 (必需)")
	cmd.Flags().String("reason", "", "恢复理由 (必需)")
	cmd.Flags().String("description", "", "提案描述 (可选)")
	cmd.Flags().String("proposer", "", "提案者地址 (可选)")

	cmd.MarkFlagRequired("validator")
	cmd.MarkFlagRequired("reason")

	return cmd
}

func runCreateRecovery(cmd *cobra.Command, args []string) error {
	// 获取参数
	validatorAddr, _ := cmd.Flags().GetString("validator")
	reason, _ := cmd.Flags().GetString("reason")
	description, _ := cmd.Flags().GetString("description")
	proposer, _ := cmd.Flags().GetString("proposer")

	// 如果description为空，使用默认值
	if description == "" {
		description = fmt.Sprintf("申请恢复验证者 %s 的出块权限，理由：%s", validatorAddr, reason)
	}

	// 构建请求
	request := CreateRecoveryRequest{
		ValidatorAddress: validatorAddr,
		RecoveryReason:   reason,
		Description:      description,
		Proposer:         proposer,
	}

	// 调用RPC
	response, err := callCreateRecoveryRPC(request)
	if err != nil {
		return fmt.Errorf("RPC调用失败: %w", err)
	}

	// 输出结果
	output, _ := json.MarshalIndent(response, "", "  ")
	fmt.Println(string(output))

	return nil
}

func callCreateRecoveryRPC(request CreateRecoveryRequest) (*CreateRecoveryResponse, error) {
	// 构建JSON-RPC请求
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_createRecoveryProposal",
		"params":  []interface{}{request.ValidatorAddress, request.RecoveryReason, request.Description, request.Proposer},
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
			Result CreateRecoveryResponse `json:"result"`
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
