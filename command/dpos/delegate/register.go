package delegate

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

var (
	registerParams = &registerDelegateParams{}
)

type registerDelegateParams struct {
	jsonRPC     string
	address     string
	name        string
	website     string
	description string
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

	// Mark required flags
	cmd.MarkFlagRequired("address")
	cmd.MarkFlagRequired("name")
}

// runPreRun runs the pre-run validation
func runPreRun(cmd *cobra.Command, _ []string) error {
	// Get JSON-RPC URL from flag
	jsonRPC, err := cmd.Flags().GetString("jsonrpc")
	if err != nil {
		return fmt.Errorf("failed to get json-rpc flag: %w", err)
	}
	registerParams.jsonRPC = jsonRPC

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

	// Validate address format
	if !isValidHexAddress(p.address) {
		return fmt.Errorf("invalid address format: %s", p.address)
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
