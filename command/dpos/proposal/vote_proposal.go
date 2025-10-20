package proposal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// VoteProposalRequest 投票请求
type VoteProposalRequest struct {
	ProposalID string `json:"proposalId"`
	Voter      string `json:"voter"`
	Support    bool   `json:"support"`
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
	cmd.Flags().Bool("support", false, "是否支持提案 (必需)")

	cmd.MarkFlagRequired("proposal-id")
	cmd.MarkFlagRequired("voter")

	return cmd
}

func runVoteProposal(cmd *cobra.Command, args []string) error {
	// 获取参数
	proposalID, _ := cmd.Flags().GetString("proposal-id")
	voter, _ := cmd.Flags().GetString("voter")
	support, _ := cmd.Flags().GetBool("support")

	// 构建请求
	request := VoteProposalRequest{
		ProposalID: proposalID,
		Voter:      voter,
		Support:    support,
	}

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
	// 构建JSON-RPC请求
	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_voteOnParameterProposal",
		"params":  []interface{}{request.ProposalID, request.Voter, request.Support},
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
