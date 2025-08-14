package vote

import (
	"bytes"
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
	jsonRPC   string
	dataDir   string
	chainID   uint64
	voter     string
	candidate string
	amount    string
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

	// Mark required flags
	cmd.MarkFlagRequired("chain-id")
	cmd.MarkFlagRequired("voter")
	cmd.MarkFlagRequired("candidate")
	cmd.MarkFlagRequired("amount")
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
	result, err := voteForCandidate(client, params.voter, params.candidate, amount)
	if err != nil {
		return fmt.Errorf("failed to vote: %w", err)
	}

	// Set the command result
	outputter.SetCommandResult(result)

	return nil
}

// voteForCandidate attempts to vote for a candidate using RPC methods
func voteForCandidate(client *jsonrpc.Client, voter, candidate string, amount *big.Int) (*VoteResult, error) {
	// Method 1: Try dpos_vote
	result, err := callVoteRPCMethod(client, "dpos_vote", []interface{}{
		voter,
		candidate,
		amount.String(),
	})
	if err == nil && result != nil {
		return result, nil
	}

	// Method 2: Try dpos_stake
	result, err = callVoteRPCMethod(client, "dpos_stake", []interface{}{
		voter,
		candidate,
		amount.String(),
	})
	if err == nil && result != nil {
		return result, nil
	}

	// Method 3: Try dpos_delegate
	result, err = callVoteRPCMethod(client, "dpos_delegate", []interface{}{
		voter,
		candidate,
		amount.String(),
	})
	if err == nil && result != nil {
		return result, nil
	}

	// If all RPC methods fail, return a simulated result
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
	var result interface{}

	// Try to call the method using the client's Call method
	err := client.Call(method, methodParams, &result)
	if err != nil {
		// If the client.Call fails, try direct HTTP request as fallback
		// This is to work around potential issues with the umbracle/ethgo/jsonrpc library
		fallbackResult, fallbackErr := callVoteRPCMethodHTTP(method, methodParams)
		if fallbackErr != nil {
			return nil, fmt.Errorf("RPC method %s failed: %w (fallback also failed: %v)", method, err, fallbackErr)
		}
		return fallbackResult, nil
	}

	// Parse the result
	return parseVoteResult(result, method)
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

	txHash := ""
	if hash, ok := resultMap["txHash"].(string); ok {
		txHash = hash
	}

	var blockNumber uint64
	if block, ok := resultMap["blockNumber"].(float64); ok {
		blockNumber = uint64(block)
	}

	return &VoteResult{
		Success:     success,
		Message:     message,
		Voter:       params.voter,
		Candidate:   params.candidate,
		Amount:      params.amount,
		TxHash:      txHash,
		BlockNumber: blockNumber,
	}, nil
}
