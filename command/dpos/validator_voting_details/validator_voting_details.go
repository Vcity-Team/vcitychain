package validator_voting_details

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
	params = &validatorVotingDetailsParams{}
)

type validatorVotingDetailsParams struct {
	jsonRPC       string
	dataDir       string
	chainID       uint64
	validatorAddr string
}

// ValidatorVotingDetailsResult represents the result of the validator voting details command
type ValidatorVotingDetailsResult struct {
	Success   bool                   `json:"success"`
	Validator map[string]interface{} `json:"validator"`
	Error     string                 `json:"error,omitempty"`
}

// GetCommand returns the validator voting details command
func GetCommand() *cobra.Command {
	validatorVotingDetailsCmd := &cobra.Command{
		Use:     "validator-voting-details",
		Short:   "Get detailed voting information for a specific validator",
		Long:    "Retrieve comprehensive voting and staking details for a specific DPoS validator",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	// Register JSON-RPC flag
	helper.RegisterJSONRPCFlag(validatorVotingDetailsCmd)

	// Add command flags
	setFlags(validatorVotingDetailsCmd)

	return validatorVotingDetailsCmd
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
		&params.validatorAddr,
		"validator",
		"",
		"the address of the validator to query",
	)

	// Mark required flags
	cmd.MarkFlagRequired("chain-id")
	cmd.MarkFlagRequired("validator")
}

// runPreRun runs the pre-run validation
func runPreRun(cmd *cobra.Command, _ []string) error {
	// Get JSON-RPC address from flag, with fallback to default
	jsonRPC := "http://localhost:8545" // Default value
	if cmd.Flags().Changed("json-rpc") {
		jsonRPC = params.jsonRPC
	}

	// Validate JSON-RPC address
	if jsonRPC == "" {
		return fmt.Errorf("JSON-RPC address is required")
	}

	// Validate chain ID
	if params.chainID == 0 {
		return fmt.Errorf("chain ID is required")
	}

	// Validate validator address
	if params.validatorAddr == "" {
		return fmt.Errorf("validator address is required")
	}

	// Validate address format
	if !isValidHexAddress(params.validatorAddr) {
		return fmt.Errorf("invalid validator address format: %s", params.validatorAddr)
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

// runCommand runs the main command logic
func runCommand(cmd *cobra.Command, _ []string) error {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// Get JSON-RPC address from flag
	jsonRPC := "http://localhost:8545" // Default value
	if cmd.Flags().Changed("jsonrpc") {
		jsonRPC, _ = cmd.Flags().GetString("jsonrpc")
	} else {
		// Try to get from helper function
		jsonRPC = helper.GetJSONRPCAddress(cmd)
	}

	// 直接使用HTTP请求，跳过有问题的第三方库
	result, err := callValidatorVotingDetailsRPCMethodHTTPWithAddress("dpos_getValidatorVotingDetails", []interface{}{params.validatorAddr}, jsonRPC)
	if err != nil {
		return fmt.Errorf("failed to get validator voting details: %w", err)
	}

	// Set the result and output it
	outputter.SetCommandResult(result)
	return nil
}



// callValidatorVotingDetailsRPCMethodHTTPWithAddress makes a direct HTTP request with specified JSON-RPC address
func callValidatorVotingDetailsRPCMethodHTTPWithAddress(method string, methodParams []interface{}, jsonRPC string) (*ValidatorVotingDetailsResult, error) {
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
	resp, err := http.Post(jsonRPC, "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse response
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for error in response
	if errorObj, exists := response["error"]; exists && errorObj != nil {
		if errorMap, ok := errorObj.(map[string]interface{}); ok {
			if message, exists := errorMap["message"]; exists {
				return nil, fmt.Errorf("RPC error: %v", message)
			}
		}
		return nil, fmt.Errorf("RPC error: %v", errorObj)
	}

	// Extract result
	if resultObj, exists := response["result"]; exists {
		return parseValidatorVotingDetailsResult(resultObj)
	}

	return nil, fmt.Errorf("no result in response")
}

// parseValidatorVotingDetailsResult parses the RPC result into ValidatorVotingDetailsResult
func parseValidatorVotingDetailsResult(result interface{}) (*ValidatorVotingDetailsResult, error) {
	// Try to parse as map first
	if resultMap, ok := result.(map[string]interface{}); ok {
		// Check if it's already in the right format
		if success, exists := resultMap["success"]; exists {
			// It's already in the right format, convert it
			successBool, _ := success.(bool)
			validator, _ := resultMap["validator"].(map[string]interface{})

			return &ValidatorVotingDetailsResult{
				Success:   successBool,
				Validator: validator,
			}, nil
		}
	}

	// If we can't parse it properly, return a basic result
	return &ValidatorVotingDetailsResult{
		Success: true,
		Validator: map[string]interface{}{
			"message": "Raw result received",
			"data":    result,
		},
	}, nil
}

// GetOutput returns the string representation of the result
func (r *ValidatorVotingDetailsResult) GetOutput() string {
	if !r.Success {
		return fmt.Sprintf("Error: %s", r.Error)
	}

	output := fmt.Sprintf("DPoS Validator Voting Details\n")
	output += fmt.Sprintf("==============================\n\n")

	if r.Validator != nil {
		if address, exists := r.Validator["address"]; exists {
			output += fmt.Sprintf("Address: %v\n", address)
		}
		if votingPower, exists := r.Validator["votingPower"]; exists {
			output += fmt.Sprintf("Voting Power: %v\n", votingPower)
		}
		if totalStakedToMe, exists := r.Validator["totalStakedToMe"]; exists {
			output += fmt.Sprintf("Total Staked to Me: %v", totalStakedToMe)
			if totalStakedToMeEther, exists := r.Validator["totalStakedToMeEther"]; exists {
				output += fmt.Sprintf(" (%v VCITY)", totalStakedToMeEther)
			}
			output += fmt.Sprintf("\n")
		}
		if isActive, exists := r.Validator["isActive"]; exists {
			output += fmt.Sprintf("Active: %v\n", isActive)
		}
		if stakeCount, exists := r.Validator["stakeCount"]; exists {
			output += fmt.Sprintf("Stake Count: %v\n", stakeCount)
		}

		// 显示入站投票明细（stakes）
		if stakes, exists := r.Validator["stakes"]; exists {
			if stakesList, ok := stakes.([]interface{}); ok && len(stakesList) > 0 {
				output += fmt.Sprintf("\n📥 Inbound Votes (别人投给我的):\n")
				output += fmt.Sprintf("----------------------------------------\n")
				for i, stake := range stakesList {
					if stakeMap, ok := stake.(map[string]interface{}); ok {
						output += fmt.Sprintf("\n%d. Staker: %v\n", i+1, stakeMap["staker"])
						if amountEther, exists := stakeMap["amountEther"]; exists {
							output += fmt.Sprintf("   Amount: %v VCITY", amountEther)
							if amountWei, exists := stakeMap["amountWei"]; exists {
								output += fmt.Sprintf(" (%v Wei)", amountWei)
							}
							output += fmt.Sprintf("\n")
						}
						if startTime, exists := stakeMap["startTime"]; exists {
							output += fmt.Sprintf("   Start Time: %v\n", startTime)
						}
						if endTime, exists := stakeMap["endTime"]; exists {
							output += fmt.Sprintf("   End Time: %v\n", endTime)
						}
						if isLocked, exists := stakeMap["isLocked"]; exists {
							output += fmt.Sprintf("   Is Locked: %v\n", isLocked)
						}
						if rewardsEther, exists := stakeMap["rewardsEther"]; exists {
							output += fmt.Sprintf("   Rewards: %v VCITY\n", rewardsEther)
						}
					}
				}
			} else {
				output += fmt.Sprintf("\n📥 Inbound Votes: None\n")
			}
		}

		// 显示出站投票明细（myVotes）
		if myVotes, exists := r.Validator["myVotes"]; exists {
			if votesList, ok := myVotes.([]interface{}); ok && len(votesList) > 0 {
				output += fmt.Sprintf("\n📤 Outbound Votes (我投给别人的):\n")
				output += fmt.Sprintf("----------------------------------------\n")
				for i, vote := range votesList {
					if voteMap, ok := vote.(map[string]interface{}); ok {
						output += fmt.Sprintf("\n%d. Delegate: %v\n", i+1, voteMap["delegate"])
						if amountEther, exists := voteMap["amountEther"]; exists {
							output += fmt.Sprintf("   Amount: %v VCITY", amountEther)
							if amountWei, exists := voteMap["amountWei"]; exists {
								output += fmt.Sprintf(" (%v Wei)", amountWei)
							}
							output += fmt.Sprintf("\n")
						}
						if startTime, exists := voteMap["startTime"]; exists {
							output += fmt.Sprintf("   Start Time: %v\n", startTime)
						}
						if endTime, exists := voteMap["endTime"]; exists {
							output += fmt.Sprintf("   End Time: %v\n", endTime)
						}
						if isLocked, exists := voteMap["isLocked"]; exists {
							output += fmt.Sprintf("   Is Locked: %v\n", isLocked)
						}
						if rewardsEther, exists := voteMap["rewardsEther"]; exists {
							output += fmt.Sprintf("   Rewards: %v VCITY\n", rewardsEther)
						}
					}
				}
			} else {
				output += fmt.Sprintf("\n📤 Outbound Votes: None\n")
			}
		}

		if totalVotedByMe, exists := r.Validator["totalVotedByMe"]; exists {
			output += fmt.Sprintf("\nTotal Voted by Me: %v", totalVotedByMe)
			if totalVotedByMeEther, exists := r.Validator["totalVotedByMeEther"]; exists {
				output += fmt.Sprintf(" (%v VCITY)", totalVotedByMeEther)
			}
			output += fmt.Sprintf("\n")
		}
		if myVoteCount, exists := r.Validator["myVoteCount"]; exists {
			output += fmt.Sprintf("My Vote Count: %v\n", myVoteCount)
		}

		if consensusRound, exists := r.Validator["consensusRound"]; exists {
			output += fmt.Sprintf("\nConsensus Round: %v\n", consensusRound)
		}
		if lastBlockProduced, exists := r.Validator["lastBlockProduced"]; exists {
			output += fmt.Sprintf("Last Block Produced: %v\n", lastBlockProduced)
		}
	}

	return output
}
