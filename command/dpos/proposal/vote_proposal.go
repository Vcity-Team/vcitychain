package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// VoteProposalRequest 投票请求
type VoteProposalRequest struct {
	ProposalID string `json:"proposalId"`
	Voter      string `json:"voter"`
	Support    bool   `json:"support"`
	PrivateKey string `json:"privateKey"` // 🆕 私钥字段
}

// VoteProposalResponse 投票响应
type VoteProposalResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// GetVoteCommand 获取投票命令
func GetVoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vote",
		Short: "对提案进行投票",
		Long:  "对指定的参数提案进行投票",
		RunE:  runVoteProposal,
	}

	cmd.Flags().String("proposal-id", "", "提案ID (必需)")
	cmd.Flags().String("voter", "", "投票者地址 (必需)")
	cmd.Flags().String("support", "", "是否支持提案 (true/false)")
	cmd.Flags().String("oppose", "", "是否反对提案 (true/false，与support互斥)")
	cmd.Flags().String("private-key", "", "私钥 (hex格式, 64字符, 必需)")

	cmd.MarkFlagRequired("proposal-id")
	cmd.MarkFlagRequired("voter")
	cmd.MarkFlagRequired("private-key")

	return cmd
}

func runVoteProposal(cmd *cobra.Command, args []string) error {
	// 获取参数
	proposalID, _ := cmd.Flags().GetString("proposal-id")
	voter, _ := cmd.Flags().GetString("voter")
	privateKey, _ := cmd.Flags().GetString("private-key")
	supportStr, _ := cmd.Flags().GetString("support")
	opposeStr, _ := cmd.Flags().GetString("oppose")

	// 验证私钥格式
	if len(privateKey) != 64 {
		return fmt.Errorf("私钥长度错误: 期望64字符，实际%d字符", len(privateKey))
	}

	// 调试日志：记录原始标志值
	fmt.Printf("🔍 命令行参数解析:\n")
	fmt.Printf("  - proposalID: %s\n", proposalID)
	fmt.Printf("  - voter: %s\n", voter)
	fmt.Printf("  - privateKey: %s... (已隐藏)\n", privateKey[:8])
	fmt.Printf("  - supportStr: '%s'\n", supportStr)
	fmt.Printf("  - opposeStr: '%s'\n", opposeStr)

	// 确定最终的支持状态
	var support bool
	if supportStr != "" && opposeStr != "" {
		return fmt.Errorf("不能同时指定 --support 和 --oppose")
	} else if supportStr != "" {
		// 解析支持参数
		switch strings.ToLower(supportStr) {
		case "true", "1", "yes", "y":
			support = true
		case "false", "0", "no", "n":
			support = false
		default:
			return fmt.Errorf("无效的 --support 值: %s，请使用 true/false", supportStr)
		}
	} else if opposeStr != "" {
		// 解析反对参数
		switch strings.ToLower(opposeStr) {
		case "true", "1", "yes", "y":
			support = false
		case "false", "0", "no", "n":
			support = true
		default:
			return fmt.Errorf("无效的 --oppose 值: %s，请使用 true/false", opposeStr)
		}
	} else {
		return fmt.Errorf("必须指定 --support 或 --oppose")
	}

	fmt.Printf("  - 最终support值: %t\n", support)

	// 构建请求
	request := VoteProposalRequest{
		ProposalID: proposalID,
		Voter:      voter,
		Support:    support,
		PrivateKey: privateKey,
	}

	// 调试日志：记录请求对象
	fmt.Printf("🔍 请求对象:\n")
	fmt.Printf("  - ProposalID: %s\n", request.ProposalID)
	fmt.Printf("  - Voter: %s\n", request.Voter)
	fmt.Printf("  - Support: %t\n", request.Support)
	fmt.Printf("  - PrivateKey: %s... (已隐藏)\n", request.PrivateKey[:8])

	// 调用RPC
	response, err := callVoteProposalRPC(request)
	if err != nil {
		return fmt.Errorf("RPC调用失败: %w", err)
	}

	// 输出结果
	output, _ := json.MarshalIndent(response, "", "  ")
	fmt.Println(string(output))

	return nil
}

func callVoteProposalRPC(request VoteProposalRequest) (*VoteProposalResponse, error) {
	// 构建JSON-RPC请求 - 添加私钥作为第4个参数
	params := []interface{}{request.ProposalID, request.Voter, request.Support, request.PrivateKey}
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_voteOnParameterProposal",
		"params":  params,
		"id":      1,
	}

	// 调试日志：记录RPC请求参数
	fmt.Printf("🔍 RPC请求参数:\n")
	fmt.Printf("  - params[0] (proposalID): %s\n", params[0])
	fmt.Printf("  - params[1] (voter): %s\n", params[1])
	fmt.Printf("  - params[2] (support): %t\n", params[2])
	fmt.Printf("  - params[3] (privateKey): %s... (已隐藏)\n", request.PrivateKey[:8])

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
			Result VoteProposalResponse `json:"result"`
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
