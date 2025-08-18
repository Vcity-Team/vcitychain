package get_vote_by_hash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

var params = &cmdParams{}

type cmdParams struct {
	jsonRPC string
	dataDir string
	chainID uint64
	txHash  string
}

// VoteInfoResult represents the result of the getVoteByHash command
type VoteInfoResult struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// GetOutput implements command.CommandResult interface
func (r *VoteInfoResult) GetOutput() string {
	if r.Success {
		if data, err := json.MarshalIndent(r.Data, "", "  "); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", r.Data)
	}
	return fmt.Sprintf("Error: %s", r.Error)
}

// GetCommand returns the getVoteByHash command
func GetCommand() *cobra.Command {
	c := &cobra.Command{
		Use:     "getVoteByHash",
		Short:   "Get human-readable DPoS vote details by transaction hash",
		Long:    "Query a DPoS vote transaction by hash and return parsed voter, candidate and amount details",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	// Register JSON-RPC flag
	helper.RegisterJSONRPCFlag(c)

	setFlags(c)
	return c
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&params.dataDir, "data-dir", "", "the directory for the Polygon Edge data")

	cmd.Flags().Uint64Var(&params.chainID, "chain-id", 0, "the chain ID to interact with")
	cmd.Flags().StringVar(&params.txHash, "tx-hash", "", "the transaction hash to query")

	cmd.MarkFlagRequired("chain-id")
	cmd.MarkFlagRequired("tx-hash")
}

func runPreRun(cmd *cobra.Command, _ []string) error {
	// JSON-RPC address with default
	jsonRPC := "http://localhost:8545"
	if cmd.Flags().Changed("jsonrpc") {
		if f := cmd.Flag("jsonrpc"); f != nil {
			params.jsonRPC = f.Value.String()
		}
	}
	if params.jsonRPC == "" {
		params.jsonRPC = jsonRPC
	}

	if params.chainID == 0 {
		return fmt.Errorf("chain-id is required")
	}
	if params.txHash == "" {
		return fmt.Errorf("tx-hash is required")
	}
	if !isValidHash(params.txHash) {
		return fmt.Errorf("invalid tx-hash: %s", params.txHash)
	}
	return nil
}

func isValidHash(h string) bool {
	return len(h) == 66 && strings.HasPrefix(h, "0x")
}

func runCommand(cmd *cobra.Command, _ []string) error {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// Use direct HTTP request to bypass third-party package issues
	result, err := callGetVoteByHashHTTP(params.txHash, params.jsonRPC)
	if err != nil {
		outputter.SetError(fmt.Errorf("failed to get vote by hash: %w", err))
		return nil
	}

	outputter.SetCommandResult(result)
	return nil
}

// callGetVoteByHashHTTP makes a direct HTTP request to bypass potential umbracle library issues
func callGetVoteByHashHTTP(txHash, jsonRPC string) (*VoteInfoResult, error) {
	// Construct JSON-RPC request manually
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "dpos_getVoteByHash",
		"params":  []interface{}{txHash},
	}

	// Marshal request to JSON
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make HTTP request
	resp, err := http.Post(jsonRPC, "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var rpcResp map[string]interface{}
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for error in response
	if errObj, ok := rpcResp["error"]; ok && errObj != nil {
		return &VoteInfoResult{
			Success: false,
			Error:   fmt.Sprintf("%v", errObj),
		}, nil
	}

	// Extract result
	resultField, exists := rpcResp["result"]
	if !exists {
		return &VoteInfoResult{
			Success: false,
			Error:   "no result field in response",
		}, nil
	}

	// Convert result to map if possible
	var resultData map[string]interface{}
	if resultMap, ok := resultField.(map[string]interface{}); ok {
		resultData = resultMap
	} else {
		resultData = map[string]interface{}{
			"raw_result": resultField,
		}
	}

	return &VoteInfoResult{
		Success: true,
		Data:    resultData,
	}, nil
}
