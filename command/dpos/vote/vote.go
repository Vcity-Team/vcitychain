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
	// private-key is only required for vote/unvote operations, not for queries
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

	// Allow amount = 0 (query), amount = -1 (unvote), amount > 0 (vote)
	if amount.Cmp(big.NewInt(0)) < 0 && amount.Cmp(big.NewInt(-1)) != 0 {
		return fmt.Errorf("amount can only be 0 (query), -1 (unvote), or positive (vote)")
	}

	// Check if private key is required
	needsPrivateKey := amount.Cmp(big.NewInt(0)) != 0 // Required for vote (>0) and unvote (-1)
	if needsPrivateKey && p.privateKey == "" {
		return fmt.Errorf("private key is required for voting/unvoting operations")
	}

	// Validate private key when required
	if needsPrivateKey {
		// Check if private key is valid hex format (64 characters)
		if len(p.privateKey) != 64 {
			return fmt.Errorf("private key must be 64 hex characters, got %d", len(p.privateKey))
		}

		// Validate hex format
		if _, err := hex.DecodeString(p.privateKey); err != nil {
			return fmt.Errorf("invalid private key hex format: %s", err)
		}
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

	// For query operations (amount = 0), don't pass private key even if provided
	privateKey := params.privateKey
	if amount.Cmp(big.NewInt(0)) == 0 {
		privateKey = ""
	}

	// Attempt to vote using RPC methods
	result, err := voteForCandidate(client, params.voter, params.candidate, amount, privateKey)
	if err != nil {
		return fmt.Errorf("failed to vote: %w", err)
	}

	// Set the command result
	outputter.SetCommandResult(result)

	return nil
}

// voteForCandidate attempts to vote for a candidate using RPC methods
func voteForCandidate(client *jsonrpc.Client, voter, candidate string, amount *big.Int, privateKey string) (*VoteResult, error) {
	// Determine operation type and parameters
	var methodParams []interface{}

	if amount.Cmp(big.NewInt(0)) == 0 {
		// Query unvote information (3 parameters, no private key)
		methodParams = []interface{}{voter, candidate, amount.String()}
	} else {
		// Vote or unvote transaction (4 parameters, with private key)
		methodParams = []interface{}{voter, candidate, amount.String(), privateKey}
	}

	result, err := callVoteRPCMethod(client, "dpos_vote", methodParams)
	if err == nil && result != nil {
		return result, nil
	}

	// If RPC method fails, return a simulated result
	operationType := "vote"
	if amount.Cmp(big.NewInt(0)) == 0 {
		operationType = "query"
	} else if amount.Cmp(big.NewInt(-1)) == 0 {
		operationType = "unvote"
	}

	return &VoteResult{
		Success:     false,
		Message:     fmt.Sprintf("No DPoS %s RPC methods available on this node", operationType),
		Voter:       voter,
		Candidate:   candidate,
		Amount:      amount.String(),
		TxHash:      "",
		BlockNumber: 0,
	}, nil
}

// callVoteRPCMethod calls a voting RPC method
func callVoteRPCMethod(client *jsonrpc.Client, method string, methodParams []interface{}) (*VoteResult, error) {
	// 直接使用HTTP方法，避免umbracle库的bug
	fallbackResult, fallbackErr := callVoteRPCMethodHTTP(method, methodParams)
	if fallbackErr != nil {
		return nil, fmt.Errorf("HTTP method %s failed: %w", method, fallbackErr)
	}
	return fallbackResult, nil
}

// callVoteRPCMethodHTTP makes a direct HTTP request to bypass potential umbracle library issues
func callVoteRPCMethodHTTP(method string, methodParams []interface{}) (*VoteResult, error) {
	return callVoteRPCMethodHTTPWithAddress(method, methodParams, params.jsonRPC)
}

// callVoteRPCMethodHTTPWithAddress makes a direct HTTP request with specified JSON-RPC address
func callVoteRPCMethodHTTPWithAddress(method string, methodParams []interface{}, jsonRPC string) (*VoteResult, error) {
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
	resp, err := http.Post(jsonRPC, "application/json", bytes.NewReader(requestJSON))
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
	if data == nil {
		return nil, fmt.Errorf("RPC response is nil")
	}

	// Try to parse as map
	resultMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected RPC response type: %T", data)
	}

	// Extract common fields
	success := true
	if successVal, ok := resultMap["success"].(bool); ok {
		success = successVal
	}

	message := fmt.Sprintf("Vote operation completed via %s", method)
	if msg, ok := resultMap["message"].(string); ok {
		message = msg
	}

	// 检查是否有错误信息
	if errorMsg, ok := resultMap["error"].(string); ok && errorMsg != "" {
		message = errorMsg
		success = false
	}

	txHash := ""
	if hash, ok := resultMap["txHash"].(string); ok {
		txHash = hash
	}

	var blockNumber uint64
	if block, ok := resultMap["blockNumber"].(float64); ok {
		blockNumber = uint64(block)
	}

	// Handle UnvoteResponse format for query operations (amount = 0)
	if withdrawAmount, hasWithdrawAmount := resultMap["withdrawAmount"]; hasWithdrawAmount {
		// This is an UnvoteResponse (query result)
		originalAmount := "0"
		if origAmt, ok := resultMap["originalAmount"]; ok {
			if amtStr, ok := origAmt.(string); ok {
				originalAmount = amtStr
			}
		}

		totalSlashAmount := "0"
		if slashAmt, ok := resultMap["totalSlashAmount"]; ok {
			if amtStr, ok := slashAmt.(string); ok {
				totalSlashAmount = amtStr
			}
		}

		withdrawAmountStr := "0"
		if withdrawAmt, ok := withdrawAmount.(string); ok {
			withdrawAmountStr = withdrawAmt
		}

		// Format detailed unvote information
		message = fmt.Sprintf("Unvote Query - Original: %s, Withdrawable: %s, Slashed: %s",
			originalAmount, withdrawAmountStr, totalSlashAmount)

		return &VoteResult{
			Success:     success,
			Message:     message,
			Voter:       params.voter,
			Candidate:   params.candidate,
			Amount:      "0", // Query operation
			TxHash:      "",
			BlockNumber: 0,
		}, nil
	}

	// Handle VoteResponse format for vote/unvote transactions (amount != 0)
	if txHash == "" {
		// Check for transaction hash in different formats
		if hash, ok := resultMap["txHash"].(string); ok && hash != "" {
			txHash = hash
		} else if hash, ok := resultMap["transactionHash"].(string); ok && hash != "" {
			txHash = hash
		}
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

	return result, nil
}
