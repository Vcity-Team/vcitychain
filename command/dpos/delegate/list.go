package delegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

// listDelegateParams holds parameters for listing delegates
type listDelegateParams struct {
	jsonRPC string
}

// ListDelegateResult represents the result of listing delegates
type ListDelegateResult struct {
	Success       bool                    `json:"success"`
	Message       string                  `json:"message"`
	Registrations []*DelegateRegistration `json:"registrations,omitempty"`
	Count         int                     `json:"count"`
}

// DelegateRegistration represents a delegate registration
type DelegateRegistration struct {
	Address      string `json:"address"`
	Name         string `json:"name"`
	Website      string `json:"website"`
	Description  string `json:"description"`
	Status       int    `json:"status"`     // RPC returns number, not string
	Deposit      int64  `json:"deposit"`    // RPC returns number, not string
	TotalVotes   int64  `json:"totalVotes"` // RPC returns number, not string
	IsActive     bool   `json:"isActive"`
	LastVoteTime int64  `json:"lastVoteTime"` // RPC returns number, not string
	CreatedAt    int64  `json:"createdAt"`    // RPC returns number, not string
}

// GetCommand returns the list command
func GetListCommand() *cobra.Command {
	var params listDelegateParams

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all DPoS delegate candidates",
		Long:  "List all registered DPoS delegate candidates with their status and information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListCommand(cmd, &params)
		},
	}

	// Add flags
	cmd.Flags().StringVar(&params.jsonRPC, "jsonrpc", "http://0.0.0.0:8545", "the JSON-RPC interface")

	return cmd
}

// runListCommand executes the list command
func runListCommand(cmd *cobra.Command, params *listDelegateParams) error {
	// Get JSON-RPC URL from flag
	jsonRPC, err := cmd.Flags().GetString("jsonrpc")
	if err != nil {
		return fmt.Errorf("failed to get json-rpc flag: %w", err)
	}
	params.jsonRPC = jsonRPC

	// Call the RPC method
	result, err := listDelegates(params)
	if err != nil {
		return fmt.Errorf("failed to list delegates: %w", err)
	}

	// Output the result directly
	fmt.Print(result.GetOutput())

	return nil
}

// listDelegates calls the dpos_getDelegateRegistrations RPC method
func listDelegates(params *listDelegateParams) (*ListDelegateResult, error) {
	// Prepare request
	requestBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "dpos_getDelegateRegistrations",
		"params":  []interface{}{},
		"id":      1,
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
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for RPC error
	if errorData, exists := response["error"]; exists {
		return nil, fmt.Errorf("RPC error: %v", errorData)
	}

	// Extract result
	resultData, exists := response["result"]
	if !exists {
		return nil, fmt.Errorf("no result in response")
	}

	// Parse the result based on its type
	var result ListDelegateResult

	// Check if resultData is already a map
	if resultMap, ok := resultData.(map[string]interface{}); ok {
		// Convert map to JSON bytes and unmarshal
		jsonBytes, err := json.Marshal(resultMap)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal result map: %w", err)
		}
		if err := json.Unmarshal(jsonBytes, &result); err != nil {
			return nil, fmt.Errorf("failed to parse result map: %w", err)
		}
	} else {
		// Try to parse as string first
		if resultStr, ok := resultData.(string); ok {
			if err := json.Unmarshal([]byte(resultStr), &result); err != nil {
				return nil, fmt.Errorf("failed to parse result string: %w", err)
			}
		} else {
			// Try to convert to JSON and parse
			jsonBytes, err := json.Marshal(resultData)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal result data: %w", err)
			}
			if err := json.Unmarshal(jsonBytes, &result); err != nil {
				return nil, fmt.Errorf("failed to parse result: %w", err)
			}
		}
	}

	return &result, nil
}

// GetOutput returns the formatted output for the list result
func (r *ListDelegateResult) GetOutput() string {
	if !r.Success {
		return fmt.Sprintf("Status: Failed\nMessage: %s", r.Message)
	}

	output := fmt.Sprintf("Status: Success\nMessage: %s\nCount: %d\n\n", r.Message, r.Count)

	if len(r.Registrations) == 0 {
		output += "No delegate registrations found."
		return output
	}

	output += "Delegate Registrations:\n"
	output += "=====================\n\n"

	for i, reg := range r.Registrations {
		// Convert status number to string
		statusStr := "Unknown"
		switch reg.Status {
		case 0:
			statusStr = "Candidate"
		case 1:
			statusStr = "Active"
		case 2:
			statusStr = "Inactive"
		case 3:
			statusStr = "Withdrawn"
		}

		// Convert timestamps to human-readable format
		lastVoteTimeStr := "Never"
		if reg.LastVoteTime > 0 {
			lastVoteTimeStr = time.Unix(reg.LastVoteTime, 0).Format("2006-01-02 15:04:05")
		}

		createdAtStr := "Unknown"
		if reg.CreatedAt > 0 {
			createdAtStr = time.Unix(reg.CreatedAt, 0).Format("2006-01-02 15:04:05")
		}

		output += fmt.Sprintf("%d. Address: %s\n", i+1, reg.Address)
		output += fmt.Sprintf("   Name: %s\n", reg.Name)
		output += fmt.Sprintf("   Website: %s\n", reg.Website)
		output += fmt.Sprintf("   Description: %s\n", reg.Description)
		output += fmt.Sprintf("   Status: %s\n", statusStr)
		output += fmt.Sprintf("   Deposit: %d\n", reg.Deposit)
		output += fmt.Sprintf("   Total Votes: %d\n", reg.TotalVotes)
		output += fmt.Sprintf("   Is Active: %t\n", reg.IsActive)
		output += fmt.Sprintf("   Last Vote Time: %s\n", lastVoteTimeStr)
		output += fmt.Sprintf("   Created At: %s\n", createdAtStr)
		output += "\n"
	}

	return output
}

// GetJSONResult returns the JSON output for the list result
func (r *ListDelegateResult) GetJSONResult() interface{} {
	return r
}
