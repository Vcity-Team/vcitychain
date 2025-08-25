package vote

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/umbracle/ethgo/jsonrpc"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

var (
	params = &voteParams{}
)

type voteParams struct {
	jsonRPC    string
	dataDir    string
	chainID    uint64
	voter      string
	candidate  string
	amount     string
	privateKey string // 新增：私钥参数
}

// GetCommand returns the vote command
func GetCommand() *cobra.Command {
	voteCmd := &cobra.Command{
		Use:     "vote",
		Short:   "Vote for a DPoS candidate",
		Long:    "Vote for a DPoS candidate by staking tokens on their behalf",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	// Register JSON-RPC flag
	helper.RegisterJSONRPCFlag(voteCmd)

	// Add command flags
	setFlags(voteCmd)

	return voteCmd
}

// setFlags sets the command flags
func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.dataDir,
		"data-dir",
		"",
		"the directory for the Polygon Edge data",
	)

	cmd.Flags().Uint64Var(
		&params.chainID,
		"chain-id",
		0,
		"the chain ID to interact with",
	)

	cmd.Flags().StringVar(
		&params.voter,
		"voter",
		"",
		"the address of the voter (your address)",
	)

	cmd.Flags().StringVar(
		&params.candidate,
		"candidate",
		"",
		"the address of the candidate to vote for",
	)

	cmd.Flags().StringVar(
		&params.amount,
		"amount",
		"",
		"the amount of tokens to stake (in wei)",
	)

	cmd.Flags().StringVar(
		&params.privateKey,
		"private-key",
		"",
		"the private key for signing the vote transaction (hex format, 64 characters)",
	)

	// Mark required flags
	cmd.MarkFlagRequired("chain-id")
	cmd.MarkFlagRequired("voter")
	cmd.MarkFlagRequired("candidate")
	cmd.MarkFlagRequired("amount")
	cmd.MarkFlagRequired("private-key")
}

// runPreRun runs the pre-run validation
func runPreRun(cmd *cobra.Command, _ []string) error {
	// Get JSON-RPC address from flag, with fallback to default
	jsonRPC := "http://localhost:8545" // Default value

	if cmd.Flags().Changed("jsonrpc") {
		if jsonRPCFlag := cmd.Flag("jsonrpc"); jsonRPCFlag != nil {
			jsonRPC = jsonRPCFlag.Value.String()
		}
	}

	// Set JSON-RPC address in params
	params.jsonRPC = jsonRPC

	// Fix data directory path if it's relative
	if params.dataDir != "" {
		// If it's a relative path starting with ./, convert to absolute
		if strings.HasPrefix(params.dataDir, "./") {
			// Get current working directory
			cwd, err := os.Getwd()
			if err == nil {
				// Convert relative path to absolute
				params.dataDir = filepath.Join(cwd, params.dataDir[2:])
			}
		}
	}

	return params.validateFlags()
}

// validateFlags validates the command flags
func (p *voteParams) validateFlags() error {
	if p.chainID == 0 {
		return fmt.Errorf("chain-id is required")
	}

	if p.voter == "" {
		return fmt.Errorf("voter address is required")
	}

	if p.candidate == "" {
		return fmt.Errorf("candidate address is required")
	}

	if p.amount == "" {
		return fmt.Errorf("amount is required")
	}

	// Validate addresses - check if they are valid hex addresses
	if !isValidHexAddress(p.voter) {
		return fmt.Errorf("invalid voter address: %s", p.voter)
	}

	if !isValidHexAddress(p.candidate) {
		return fmt.Errorf("invalid candidate address: %s", p.candidate)
	}

	// Validate amount
	amount, ok := new(big.Int).SetString(p.amount, 10)
	if !ok {
		return fmt.Errorf("invalid amount format: %s", p.amount)
	}

	if amount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("amount must be greater than 0")
	}

	// Validate private key
	if p.privateKey == "" {
		return fmt.Errorf("private key is required")
	}

	// Check if private key is valid hex format (64 characters)
	if len(p.privateKey) != 64 {
		return fmt.Errorf("private key must be 64 hex characters, got %d", len(p.privateKey))
	}

	// Validate hex format
	if _, err := hex.DecodeString(p.privateKey); err != nil {
		return fmt.Errorf("invalid private key hex format: %s", err)
	}

	return nil
}

// isValidHexAddress checks if a string is a valid hex address
func isValidHexAddress(addr string) bool {
	if len(addr) != 42 || !strings.HasPrefix(addr, "0x") {
		return false
	}

	// Check if all characters after 0x are valid hex
	for i := 2; i < len(addr); i++ {
		c := addr[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// runCommand executes the main command logic
func runCommand(cmd *cobra.Command, _ []string) error {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// Create JSON-RPC client
	client, err := jsonrpc.NewClient(params.jsonRPC)
	if err != nil {
		return fmt.Errorf("failed to create JSON-RPC client: %w", err)
	}

	// Parse amount
	amount, _ := new(big.Int).SetString(params.amount, 10)

	// Attempt to vote using RPC methods
	result, err := voteForCandidate(client, params.voter, params.candidate, amount, params.privateKey)
	if err != nil {
		return fmt.Errorf("failed to vote: %w", err)
	}

	// Set the command result
	outputter.SetCommandResult(result)

	return nil
}

// voteForCandidate attempts to vote for a candidate using RPC methods
func voteForCandidate(client *jsonrpc.Client, voter, candidate string, amount *big.Int, privateKey string) (*VoteResult, error) {
	fmt.Printf("🚀 === DPoS投票命令开始执行 ===\n")
	fmt.Printf("📝 投票参数: voter=%s, candidate=%s, amount=%s\n", voter, candidate, amount.String())
	fmt.Printf("🌐 连接到JSON-RPC: %s\n", params.jsonRPC)

	// Method 1: Try dpos_vote
	fmt.Printf("🔍 尝试方法1: dpos_vote\n")
	result, err := callVoteRPCMethod(client, "dpos_vote", []interface{}{
		voter,
		candidate,
		amount.String(),
		privateKey, // 添加私钥参数
	})
	if err == nil && result != nil {
		fmt.Printf("✅ 方法1成功: dpos_vote\n")
		return result, nil
	}
	fmt.Printf("❌ 方法1失败: dpos_vote - %v\n", err)

	// Method 2: Try dpos_stake
	fmt.Printf("🔍 尝试方法2: dpos_stake\n")
	result, err = callVoteRPCMethod(client, "dpos_stake", []interface{}{
		voter,
		candidate,
		amount.String(),
		privateKey, // 添加私钥参数
	})
	if err == nil && result != nil {
		fmt.Printf("✅ 方法2成功: dpos_stake\n")
		return result, nil
	}
	fmt.Printf("❌ 方法2失败: dpos_stake - %v\n", err)

	// Method 3: Try dpos_delegate
	fmt.Printf("🔍 尝试方法3: dpos_delegate\n")
	result, err = callVoteRPCMethod(client, "dpos_delegate", []interface{}{
		voter,
		candidate,
		amount.String(),
		privateKey, // 添加私钥参数
	})
	if err == nil && result != nil {
		fmt.Printf("✅ 方法3成功: dpos_delegate\n")
		return result, nil
	}
	fmt.Printf("❌ 方法3失败: dpos_delegate - %v\n", err)

	// If all RPC methods fail, return a simulated result
	fmt.Printf("💥 所有RPC方法都失败了！\n")
	fmt.Printf("📋 返回失败结果\n")
	return &VoteResult{
		Success:     false,
		Message:     "No DPoS voting RPC methods available on this node",
		Voter:       voter,
		Candidate:   candidate,
		Amount:      amount.String(),
		TxHash:      "",
		BlockNumber: 0,
	}, nil
}

// callVoteRPCMethod calls a voting RPC method
func callVoteRPCMethod(client *jsonrpc.Client, method string, methodParams []interface{}) (*VoteResult, error) {
	fmt.Printf("📡 调用RPC方法: %s\n", method)
	fmt.Printf("📋 方法参数: %v\n", methodParams)

	var result interface{}

	// Try to call the method using the client's Call method
	fmt.Printf("🔄 尝试使用client.Call方法...\n")
	err := client.Call(method, methodParams, &result)
	if err != nil {
		fmt.Printf("❌ client.Call失败: %v\n", err)
		fmt.Printf("🔄 尝试HTTP回退方法...\n")

		// If the client.Call fails, try direct HTTP request as fallback
		// This is to work around potential issues with the umbracle/ethgo/jsonrpc library
		fallbackResult, fallbackErr := callVoteRPCMethodHTTP(method, methodParams)
		if fallbackErr != nil {
			fmt.Printf("❌ HTTP回退方法也失败了: %v\n", fallbackErr)
			return nil, fmt.Errorf("RPC method %s failed: %w (fallback also failed: %v)", method, err, fallbackErr)
		}
		fmt.Printf("✅ HTTP回退方法成功\n")
		return fallbackResult, nil
	}

	fmt.Printf("✅ client.Call方法成功\n")
	fmt.Printf("📊 原始结果: %v\n", result)

	// Parse the result
	parsedResult, parseErr := parseVoteResult(result, method)
	if parseErr != nil {
		fmt.Printf("❌ 解析结果失败: %v\n", parseErr)
	} else {
		fmt.Printf("✅ 结果解析成功\n")
	}

	return parsedResult, parseErr
}

// callVoteRPCMethodHTTP makes a direct HTTP request to bypass potential umbracle library issues
func callVoteRPCMethodHTTP(method string, methodParams []interface{}) (*VoteResult, error) {
	// Construct JSON-RPC request manually
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  methodParams,
	}

	// Marshal request to JSON
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make HTTP request
	resp, err := http.Post("http://localhost:8545", "application/json", bytes.NewReader(requestJSON))
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for error
	if errorField, exists := response["error"]; exists && errorField != nil {
		return nil, fmt.Errorf("RPC error: %v", errorField)
	}

	// Extract result
	resultField, exists := response["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Parse the result
	return parseVoteResult(resultField, method)
}

// parseVoteResult parses the RPC response for voting operations
func parseVoteResult(data interface{}, method string) (*VoteResult, error) {
	fmt.Printf("🔍 开始解析投票结果...\n")
	fmt.Printf("📊 原始数据类型: %T\n", data)
	fmt.Printf("📋 原始数据内容: %v\n", data)

	if data == nil {
		fmt.Printf("❌ RPC响应为空\n")
		return nil, fmt.Errorf("RPC response is nil")
	}

	// Try to parse as map
	resultMap, ok := data.(map[string]interface{})
	if !ok {
		fmt.Printf("❌ 意外的RPC响应类型: %T\n", data)
		return nil, fmt.Errorf("unexpected RPC response type: %T", data)
	}

	fmt.Printf("✅ 成功解析为map类型\n")
	fmt.Printf("🗂️ map内容: %v\n", resultMap)

	// Extract common fields
	success := true
	if successVal, ok := resultMap["success"].(bool); ok {
		success = successVal
		fmt.Printf("✅ 提取success字段: %v\n", success)
	} else {
		fmt.Printf("⚠️ 未找到success字段，使用默认值: %v\n", success)
	}

	message := fmt.Sprintf("Vote operation completed via %s", method)
	if msg, ok := resultMap["message"].(string); ok {
		message = msg
		fmt.Printf("✅ 提取message字段: %s\n", message)
	} else {
		fmt.Printf("⚠️ 未找到message字段，使用默认值: %s\n", message)
	}

	txHash := ""
	if hash, ok := resultMap["txHash"].(string); ok {
		txHash = hash
		fmt.Printf("✅ 提取txHash字段: %s\n", txHash)
	} else {
		fmt.Printf("⚠️ 未找到txHash字段，使用默认值: %s\n", txHash)
	}

	var blockNumber uint64
	if block, ok := resultMap["blockNumber"].(float64); ok {
		blockNumber = uint64(block)
		fmt.Printf("✅ 提取blockNumber字段: %d\n", blockNumber)
	} else {
		fmt.Printf("⚠️ 未找到blockNumber字段，使用默认值: %d\n", blockNumber)
	}

	result := &VoteResult{
		Success:     success,
		Message:     message,
		Voter:       params.voter,
		Candidate:   params.candidate,
		Amount:      params.amount,
		TxHash:      txHash,
		BlockNumber: blockNumber,
	}

	fmt.Printf("🎯 最终解析结果:\n")
	fmt.Printf("   Success: %v\n", result.Success)
	fmt.Printf("   Message: %s\n", result.Message)
	fmt.Printf("   Voter: %s\n", result.Voter)
	fmt.Printf("   Candidate: %s\n", result.Candidate)
	fmt.Printf("   Amount: %s\n", result.Amount)
	fmt.Printf("   TxHash: %s\n", result.TxHash)
	fmt.Printf("   BlockNumber: %d\n", result.BlockNumber)

	return result, nil
}
