package delegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

var (
	registerParams = &registerDelegateParams{}
)

type registerDelegateParams struct {
	jsonRPC     string
	chainID     uint64
	genesisPath string
	address     string
	name        string
	website     string
	description string
	privateKey  string
}

// GetRegisterCommand returns the register delegate command
func GetRegisterCommand() *cobra.Command {
	registerCmd := &cobra.Command{
		Use:     "register",
		Short:   "Register as a DPoS delegate candidate",
		Long:    "Register as a DPoS delegate candidate to be eligible for voting",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	// Register JSON-RPC flag
	helper.RegisterJSONRPCFlag(registerCmd)

	// Add command flags
	setFlags(registerCmd)

	return registerCmd
}

// setFlags sets the command flags
func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&registerParams.address,
		"address",
		"",
		"the address to register as delegate (required)",
	)
	cmd.Flags().StringVar(
		&registerParams.name,
		"name",
		"",
		"delegate name (required)",
	)
	cmd.Flags().StringVar(
		&registerParams.website,
		"website",
		"",
		"delegate website (optional)",
	)
	cmd.Flags().StringVar(
		&registerParams.description,
		"description",
		"",
		"delegate description (optional)",
	)
	cmd.Flags().StringVar(
		&registerParams.privateKey,
		"private-key",
		"",
		"private key for signing the registration transaction (required)",
	)
	cmd.Flags().Uint64Var(
		&registerParams.chainID,
		"chain-id",
		0,
		"chain ID for transaction signing (if not specified, will be read from genesis file)",
	)
	cmd.Flags().StringVar(
		&registerParams.genesisPath,
		"genesis",
		"./genesis.json",
		"path to the genesis file (default: ./genesis.json)",
	)

	// Mark required flags
	cmd.MarkFlagRequired("address")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("private-key")
}

// runPreRun runs the pre-run validation
func runPreRun(cmd *cobra.Command, _ []string) error {
	// Get JSON-RPC URL from flag
	jsonRPC, err := cmd.Flags().GetString("jsonrpc")
	if err != nil {
		return fmt.Errorf("failed to get json-rpc flag: %w", err)
	}
	registerParams.jsonRPC = jsonRPC

	// Get genesis path from flag
	genesisPath, err := cmd.Flags().GetString("genesis")
	if err != nil {
		return fmt.Errorf("failed to get genesis flag: %w", err)
	}
	registerParams.genesisPath = genesisPath

	// Get chain-id from flag
	chainID, err := cmd.Flags().GetUint64("chain-id")
	if err != nil {
		return fmt.Errorf("failed to get chain-id flag: %w", err)
	}

	// If chain-id is not specified, try to read from genesis file
	if chainID == 0 {
		chainConfig, err := chain.ImportFromFile(registerParams.genesisPath)
		if err != nil {
			return fmt.Errorf("failed to read genesis file at %s: %w. Please specify chain-id manually or provide a valid genesis file", registerParams.genesisPath, err)
		}
		registerParams.chainID = uint64(chainConfig.Params.ChainID)
	} else {
		// User manually specified chain-id, use the specified value
		registerParams.chainID = chainID
	}

	return registerParams.validateFlags()
}

// validateFlags validates the command flags
func (p *registerDelegateParams) validateFlags() error {
	if p.address == "" {
		return fmt.Errorf("address is required")
	}

	if p.name == "" {
		return fmt.Errorf("name is required")
	}

	if p.privateKey == "" {
		return fmt.Errorf("private-key is required")
	}

	// Validate address format
	if !isValidHexAddress(p.address) {
		return fmt.Errorf("invalid address format: %s", p.address)
	}

	// Validate private key format
	if !isValidPrivateKey(p.privateKey) {
		return fmt.Errorf("invalid private key format: %s", p.privateKey)
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

// isValidPrivateKey checks if a string is a valid private key
func isValidPrivateKey(key string) bool {
	// Private key should be 64 hex characters (32 bytes)
	if len(key) != 64 {
		return false
	}

	// Check if all characters are valid hex
	for i := 0; i < len(key); i++ {
		c := key[i]
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

	// Call RPC method
	result, err := registerDelegate(registerParams)
	if err != nil {
		return fmt.Errorf("failed to register delegate: %w", err)
	}

	// Set the command result
	outputter.SetCommandResult(result)

	return nil
}

// registerDelegate calls the dpos_registerDelegate RPC method
func registerDelegate(params *registerDelegateParams) (*RegisterDelegateResult, error) {
	// Prepare request
	requestBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_registerDelegate",
		"params": map[string]interface{}{
			"registrant":  params.address, // 使用registrant而不是address
			"name":        params.name,
			"website":     params.website,
			"description": params.description,
			"privateKey":  params.privateKey, // 添加私钥参数
			"chainID":     params.chainID,    // 添加chainID参数
		},
		"id": 1,
	}

	// Convert to JSON
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make HTTP request
	resp, err := http.Post(params.jsonRPC, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var response struct {
		Result *RegisterDelegateResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", response.Error.Message)
	}

	return response.Result, nil
}
