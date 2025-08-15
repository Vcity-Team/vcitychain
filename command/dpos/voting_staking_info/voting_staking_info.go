package voting_staking_info

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/umbracle/ethgo/jsonrpc"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

var (
	params = &votingStakingInfoParams{}
)

type votingStakingInfoParams struct {
	jsonRPC string
	dataDir string
	chainID uint64
}

// VotingStakingInfoResult represents the result of the voting staking info command
type VotingStakingInfoResult struct {
	Success       bool                     `json:"success"`
	NetworkStats  map[string]interface{}   `json:"networkStats"`
	Validators    []map[string]interface{} `json:"validators"`
	StakingInfo   interface{}              `json:"stakingInfo"`
	VotingDetails []map[string]interface{} `json:"votingDetails"` // 新增：投票明细
	DPoSState     interface{}              `json:"dposState"`
	LastUpdated   string                   `json:"lastUpdated"`
	BlockHeight   uint64                   `json:"blockHeight"`
	Error         string                   `json:"error,omitempty"`
}

// GetOutput returns the string representation of the result
func (r *VotingStakingInfoResult) GetOutput() string {
	if !r.Success {
		return fmt.Sprintf("Error: %s", r.Error)
	}

	output := fmt.Sprintf("DPoS Network Voting & Staking Information\n")
	output += fmt.Sprintf("==========================================\n\n")

	if r.NetworkStats != nil {
		output += fmt.Sprintf("Network Statistics:\n")
		output += fmt.Sprintf("- Total Validators: %v\n", r.NetworkStats["totalValidators"])
		output += fmt.Sprintf("- Active Validators: %v\n", r.NetworkStats["activeValidators"])

		// 格式化质押数量显示
		if totalStaked, ok := r.NetworkStats["totalStaked"]; ok {
			if stakedStr, ok := totalStaked.(string); ok {
				if stakedBigInt, ok := new(big.Int).SetString(stakedStr, 10); ok {
					ethAmount := new(big.Float).Quo(new(big.Float).SetInt(stakedBigInt), new(big.Float).SetFloat64(1e18))
					output += fmt.Sprintf("- Total Staked: %s ETH (%s Wei)\n", ethAmount.Text('f', 2), stakedStr)
				} else {
					output += fmt.Sprintf("- Total Staked: %v\n", totalStaked)
				}
			} else {
				output += fmt.Sprintf("- Total Staked: %v\n", totalStaked)
			}
		}

		// 格式化投票数量显示
		if totalVotes, ok := r.NetworkStats["totalVotes"]; ok {
			if votesStr, ok := totalVotes.(string); ok {
				if votesBigInt, ok := new(big.Int).SetString(votesStr, 10); ok {
					ethAmount := new(big.Float).Quo(new(big.Float).SetInt(votesBigInt), new(big.Float).SetFloat64(1e18))
					output += fmt.Sprintf("- Total Votes: %s ETH (%s Wei)\n", ethAmount.Text('f', 2), votesStr)
				} else {
					output += fmt.Sprintf("- Total Votes: %v\n", totalVotes)
				}
			} else {
				output += fmt.Sprintf("- Total Votes: %v\n", totalVotes)
			}
		}

		output += fmt.Sprintf("- Staking Transactions: %v\n", r.NetworkStats["stakingTransactions"])
		output += fmt.Sprintf("- Voting Transactions: %v\n", r.NetworkStats["votingTransactions"])
		output += fmt.Sprintf("- Consensus Threshold: %v\n", r.NetworkStats["consensusThreshold"])
		output += fmt.Sprintf("\n")
	}

	if len(r.Validators) > 0 {
		output += fmt.Sprintf("Validators (%d):\n", len(r.Validators))
		for i, validator := range r.Validators {
			output += fmt.Sprintf("%d. Address: %v, Voting Power: %v, Active: %v\n",
				i+1, validator["address"], validator["votingPower"], validator["isActive"])
		}
		output += fmt.Sprintf("\n")
	}

	// 新增：显示投票明细
	if len(r.VotingDetails) > 0 {
		output += fmt.Sprintf("Voting Details:\n")
		output += fmt.Sprintf("===============\n")
		for i, votingDetail := range r.VotingDetails {
			output += fmt.Sprintf("%d. Delegate: %v\n", i+1, votingDetail["delegateAddress"])

			// 格式化 Total Votes 显示
			if totalVotes, ok := votingDetail["totalVotes"]; ok {
				if votesStr, ok := totalVotes.(string); ok {
					if votesBigInt, ok := new(big.Int).SetString(votesStr, 10); ok {
						ethAmount := new(big.Float).Quo(new(big.Float).SetInt(votesBigInt), new(big.Float).SetFloat64(1e18))
						output += fmt.Sprintf("   Total Votes: %s ETH (%s Wei)\n", ethAmount.Text('f', 2), votesStr)
					} else {
						output += fmt.Sprintf("   Total Votes: %v\n", totalVotes)
					}
				} else {
					output += fmt.Sprintf("   Total Votes: %v\n", totalVotes)
				}
			}

			if voters, ok := votingDetail["voters"].([]map[string]interface{}); ok && len(voters) > 0 {
				output += fmt.Sprintf("   Voters:\n")
				for j, voter := range voters {
					// 格式化 Voter Amount 显示
					if amount, ok := voter["amount"]; ok {
						if amountStr, ok := amount.(string); ok {
							if amountBigInt, ok := new(big.Int).SetString(amountStr, 10); ok {
								ethAmount := new(big.Float).Quo(new(big.Float).SetInt(amountBigInt), new(big.Float).SetFloat64(1e18))
								output += fmt.Sprintf("     %d. Voter: %v, Amount: %s ETH (%s Wei)\n",
									j+1, voter["voterAddress"], ethAmount.Text('f', 2), amountStr)
							} else {
								output += fmt.Sprintf("     %d. Voter: %v, Amount: %v\n",
									j+1, voter["voterAddress"], amount)
							}
						} else {
							output += fmt.Sprintf("     %d. Voter: %v, Amount: %v\n",
								j+1, voter["voterAddress"], amount)
						}
					} else {
						output += fmt.Sprintf("     %d. Voter: %v, Amount: N/A\n",
							j+1, voter["voterAddress"])
					}
				}
			}
			output += fmt.Sprintf("\n")
		}
	}

	output += fmt.Sprintf("Last Updated: %s\n", r.LastUpdated)
	output += fmt.Sprintf("Block Height: %d\n", r.BlockHeight)

	return output
}

// GetCommand returns the voting staking info command
func GetCommand() *cobra.Command {
	votingStakingInfoCmd := &cobra.Command{
		Use:     "voting-staking-info",
		Short:   "Get DPoS network voting and staking information",
		Long:    "Retrieve comprehensive information about current DPoS network voting and staking status",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	// Register JSON-RPC flag
	helper.RegisterJSONRPCFlag(votingStakingInfoCmd)

	// Add command flags
	setFlags(votingStakingInfoCmd)

	return votingStakingInfoCmd
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

	// Mark required flags
	cmd.MarkFlagRequired("chain-id")
}

// runPreRun runs the pre-run validation
func runPreRun(cmd *cobra.Command, _ []string) error {
	// Get JSON-RPC address from flag, with fallback to default
	jsonRPC := "http://localhost:8545" // Default value
	if jsonRPCFlag := cmd.Flag("jsonrpc"); jsonRPCFlag != nil {
		jsonRPC = jsonRPCFlag.Value.String()
	}

	// Validate JSON-RPC address
	if jsonRPC == "" {
		return fmt.Errorf("JSON-RPC address is required")
	}

	// Validate chain ID
	if params.chainID == 0 {
		return fmt.Errorf("chain ID is required")
	}

	return nil
}

// runCommand runs the main command logic
func runCommand(cmd *cobra.Command, _ []string) error {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// Get JSON-RPC address from flag
	jsonRPC := "http://localhost:8545" // Default value
	if jsonRPCFlag := cmd.Flag("jsonrpc"); jsonRPCFlag != nil {
		jsonRPC = jsonRPCFlag.Value.String()
	}

	// 直接使用HTTP请求，跳过有问题的第三方库
	result, err := callVotingStakingInfoRPCMethodHTTPWithAddress("dpos_getVotingStakingInfo", []interface{}{}, jsonRPC)
	if err != nil {
		return fmt.Errorf("failed to get voting staking info: %w", err)
	}

	// Set the result and output it
	outputter.SetCommandResult(result)
	return nil
}

// callVotingStakingInfoRPCMethod calls the dpos_getVotingStakingInfo RPC method
func callVotingStakingInfoRPCMethod(client *jsonrpc.Client) (*VotingStakingInfoResult, error) {
	var result interface{}

	// Try to call the method using the client's Call method
	err := client.Call("dpos_getVotingStakingInfo", []interface{}{}, &result)
	if err != nil {
		// If the client.Call fails, try direct HTTP request as fallback
		fallbackResult, fallbackErr := callVotingStakingInfoRPCMethodHTTP("dpos_getVotingStakingInfo", []interface{}{})
		if fallbackErr != nil {
			return nil, fmt.Errorf("RPC method dpos_getVotingStakingInfo failed: %w (fallback also failed: %v)", err, fallbackErr)
		}
		return fallbackResult, nil
	}

	// Parse the result
	return parseVotingStakingInfoResult(result)
}

// callVotingStakingInfoRPCMethodHTTP makes a direct HTTP request to bypass potential umbracle library issues
func callVotingStakingInfoRPCMethodHTTP(method string, methodParams []interface{}) (*VotingStakingInfoResult, error) {
	return callVotingStakingInfoRPCMethodHTTPWithAddress(method, methodParams, "http://localhost:8545")
}

// callVotingStakingInfoRPCMethodHTTPWithAddress makes a direct HTTP request with specified JSON-RPC address
func callVotingStakingInfoRPCMethodHTTPWithAddress(method string, methodParams []interface{}, jsonRPC string) (*VotingStakingInfoResult, error) {
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

	fmt.Printf("Sending JSON-RPC request to %s: %s\n", jsonRPC, string(requestJSON))

	// Make HTTP request
	resp, err := http.Post(jsonRPC, "application/json", bytes.NewBuffer(requestJSON))
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("HTTP response status: %s\n", resp.Status)

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	fmt.Printf("HTTP response body: %s\n", string(body))

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
		return parseVotingStakingInfoResult(resultObj)
	}

	return nil, fmt.Errorf("no result in response")
}

// parseVotingStakingInfoResult parses the RPC result into VotingStakingInfoResult
func parseVotingStakingInfoResult(result interface{}) (*VotingStakingInfoResult, error) {
	// Try to parse as map first
	if resultMap, ok := result.(map[string]interface{}); ok {
		// Check if it's already in the right format
		if success, exists := resultMap["success"]; exists {
			// It's already in the right format, convert it
			successBool, _ := success.(bool)
			networkStats, _ := resultMap["networkStats"].(map[string]interface{})
			validators, _ := resultMap["validators"].([]interface{})
			stakingInfo := resultMap["stakingInfo"]
			votingDetails, _ := resultMap["votingDetails"].([]interface{})
			dposState := resultMap["dposState"]
			lastUpdated, _ := resultMap["lastUpdated"].(string)
			blockHeight, _ := resultMap["blockHeight"].(float64)

			// Convert validators to the right format
			validatorsList := make([]map[string]interface{}, 0)
			for _, v := range validators {
				if validatorMap, ok := v.(map[string]interface{}); ok {
					validatorsList = append(validatorsList, validatorMap)
				}
			}

			// Convert voting details to the right format
			votingDetailsList := make([]map[string]interface{}, 0)
			for _, v := range votingDetails {
				if votingDetailMap, ok := v.(map[string]interface{}); ok {
					votingDetailsList = append(votingDetailsList, votingDetailMap)
				}
			}

			return &VotingStakingInfoResult{
				Success:       successBool,
				NetworkStats:  networkStats,
				Validators:    validatorsList,
				StakingInfo:   stakingInfo,
				VotingDetails: votingDetailsList,
				DPoSState:     dposState,
				LastUpdated:   lastUpdated,
				BlockHeight:   uint64(blockHeight),
			}, nil
		}
	}

	// If we can't parse it properly, return a basic result
	return &VotingStakingInfoResult{
		Success: true,
		NetworkStats: map[string]interface{}{
			"message": "Raw result received",
		},
		Validators:  []map[string]interface{}{},
		StakingInfo: result,
		DPoSState:   nil,
		LastUpdated: "now",
		BlockHeight: 0,
	}, nil
}
