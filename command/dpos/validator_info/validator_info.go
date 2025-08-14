package validator_info

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/spf13/cobra"
	"github.com/umbracle/ethgo/jsonrpc"
)

var (
	params validatorInfoParams
)

// GetCommand returns the validator-info command
func GetCommand() *cobra.Command {
	validatorInfoCmd := &cobra.Command{
		Use:     "validator-info",
		Short:   "Get DPoS validator information",
		Long:    "Retrieves comprehensive information about DPoS validators, including delegates, staking info, and consensus status",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	// Register JSON-RPC flag
	helper.RegisterJSONRPCFlag(validatorInfoCmd)

	// Add command flags
	setFlags(validatorInfoCmd)

	return validatorInfoCmd
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
		"the chain ID to query",
	)

	// Mark chain-id as required
	cmd.MarkFlagRequired("chain-id")
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

// runCommand executes the main command logic
func runCommand(cmd *cobra.Command, _ []string) error {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	// Create JSON-RPC client
	client, err := jsonrpc.NewClient(params.jsonRPC)
	if err != nil {
		return fmt.Errorf("failed to create JSON-RPC client: %w", err)
	}

	// Get current block number
	blockNumber, err := client.Eth().BlockNumber()
	if err != nil {
		return fmt.Errorf("failed to get current block number: %w", err)
	}

	// Get DPoS state information
	dposState, err := getDPoSState(client, blockNumber)
	if err != nil {
		return fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// Set the command result
	outputter.SetCommandResult(dposState)

	return nil
}

// getDPoSState retrieves DPoS consensus state information
func getDPoSState(client *jsonrpc.Client, blockNumber uint64) (*ValidatorInfoResult, error) {
	// Try to get DPoS data from running node first
	delegates, err := getDPoSDelegatesFromNode(client, blockNumber)
	if err != nil {
		// Fallback to local state
		delegates, err = getDPoSDelegatesFromLocalState(blockNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to get DPoS delegates: %w", err)
		}
	}

	stakingInfo, err := getDPoSStakingInfoFromNode(client, blockNumber)
	if err != nil {
		// Fallback to local state
		stakingInfo, err = getDPoSStakingInfoFromLocalState(blockNumber)
		if err != nil {
			// Don't fail completely, try to get from genesis delegates
			stakingInfo, err = getStakingInfoFromGenesisDelegates()
			if err != nil {
				stakingInfo = []*dpos.StakeInfo{}
			}
		}
	}

	currentRound, currentDelegate, err := getDPoSConsensusStatusFromNode(client)
	if err != nil {
		// Fallback to local state
		currentRound, currentDelegate, err = getDPoSConsensusStatusFromLocalState()
		if err != nil {
			// Don't fail completely, try to calculate from genesis
			currentRound, currentDelegate, err = calculateConsensusStatusFromGenesis()
			if err != nil {
				// Use default values
				currentRound = 1
				currentDelegate = types.ZeroAddress
			}
		}
	}

	// Convert delegates to output format
	delegateInfos := make([]DelegateInfo, 0, len(delegates))
	activeDelegates := uint64(0)
	totalStake := big.NewInt(0)

	for i, delegate := range delegates {
		// All initial delegates from genesis should be active
		if delegate.IsActive {
			activeDelegates++
		}
		totalStake.Add(totalStake, delegate.VotingPower)

		delegateInfos = append(delegateInfos, convertValidatorToDelegateInfo(delegate, uint64(i)))
	}

	// Convert staking info to output format
	stakingInfos := make([]StakeInfo, 0, len(stakingInfo))
	for _, stake := range stakingInfo {
		stakingInfos = append(stakingInfos, convertStakeInfo(stake))
	}

	result := &ValidatorInfoResult{
		ChainID:         params.chainID,
		BlockHeight:     blockNumber,
		CurrentRound:    currentRound,
		CurrentDelegate: currentDelegate.String(),
		Delegates:       delegateInfos,
		StakingInfo:     stakingInfos,
		TotalStake:      totalStake.String(),
		ActiveDelegates: activeDelegates,
	}

	return result, nil
}

// getDPoSDelegatesFromNode attempts to get DPoS delegates from the running node
func getDPoSDelegatesFromNode(client *jsonrpc.Client, blockNumber uint64) (validator.AccountSet, error) {
	// Try to call custom RPC methods for DPoS delegates
	// These would need to be implemented in the node's JSON-RPC server

	// Method 1: Try dpos_getDelegates
	delegates, err := callCustomRPCMethod(client, "dpos_getDelegates", []interface{}{blockNumber})
	if err == nil && delegates != nil {
		return parseDelegatesFromRPC(delegates)
	}

	// Method 2: Try dpos_getValidatorSet
	validators, err := callCustomRPCMethod(client, "dpos_getValidatorSet", []interface{}{blockNumber})
	if err == nil && validators != nil {
		return parseDelegatesFromRPC(validators)
	}

	// Method 3: Try to get from genesis if available
	return getDelegatesFromGenesis()
}

// getDPoSStakingInfoFromNode attempts to get DPoS staking information from the running node
func getDPoSStakingInfoFromNode(client *jsonrpc.Client, blockNumber uint64) ([]*dpos.StakeInfo, error) {
	// Try to call custom RPC methods for DPoS staking info

	// Method 1: Try dpos_getStakingInfo
	stakingInfo, err := callCustomRPCMethod(client, "dpos_getStakingInfo", []interface{}{blockNumber})
	if err == nil && stakingInfo != nil {
		return parseStakingInfoFromRPC(stakingInfo)
	}

	// Method 2: Try dpos_getVotingPower
	votingPower, err := callCustomRPCMethod(client, "dpos_getVotingPower", []interface{}{blockNumber})
	if err == nil && votingPower != nil {
		// Convert voting power info to staking info
		return convertVotingPowerToStakingInfo(votingPower)
	}

	// Method 3: Try to get staking info from genesis delegates
	return getStakingInfoFromGenesisDelegates()
}

// getDPoSConsensusStatusFromNode attempts to get DPoS consensus status from the running node
func getDPoSConsensusStatusFromNode(client *jsonrpc.Client) (uint64, types.Address, error) {
	// Try to call custom RPC methods for DPoS consensus status

	// Method 1: Try dpos_getCurrentRound
	currentRound, err := callCustomRPCMethod(client, "dpos_getCurrentRound", []interface{}{})
	if err == nil && currentRound != nil {
		if round, ok := currentRound.(float64); ok {
			return uint64(round), types.ZeroAddress, nil
		}
	}

	// Method 2: Try dpos_getCurrentDelegate
	currentDelegate, err := callCustomRPCMethod(client, "dpos_getCurrentDelegate", []interface{}{})
	if err == nil && currentDelegate != nil {
		if delegateAddr, ok := currentDelegate.(string); ok {
			return 1, types.StringToAddress(delegateAddr), nil
		}
	}

	// Method 3: Try dpos_getConsensusState
	consensusState, err := callCustomRPCMethod(client, "dpos_getConsensusState", []interface{}{})
	if err == nil && consensusState != nil {
		return parseConsensusStateFromRPC(consensusState)
	}

	// Method 4: Try to calculate from genesis and block height
	return calculateConsensusStatusFromGenesis()
}

// callCustomRPCMethod calls a custom RPC method
func callCustomRPCMethod(client *jsonrpc.Client, method string, params []interface{}) (interface{}, error) {
	// For now, we'll use a simple approach since the custom RPC methods may not be implemented
	// In a real implementation, you would need to implement these methods in the node's JSON-RPC server

	// Try to call the method using the client's Call method
	var result interface{}

	// Create a simple RPC call structure
	callParams := make([]interface{}, 0)
	if len(params) > 0 {
		callParams = params
	}

	// Try to call the method
	err := client.Call(method, callParams, &result)
	if err != nil {
		// Return error to indicate method not implemented
		return nil, fmt.Errorf("custom RPC method %s not implemented or failed: %w", method, err)
	}

	return result, nil
}

// parseDelegatesFromRPC parses delegate data from RPC response
func parseDelegatesFromRPC(data interface{}) (validator.AccountSet, error) {
	if data == nil {
		return validator.AccountSet{}, fmt.Errorf("RPC data is nil")
	}

	// Try to parse as array/slice
	var delegatesArray []interface{}
	switch v := data.(type) {
	case []interface{}:
		delegatesArray = v
	case []map[string]interface{}:
		// Convert to []interface{}
		delegatesArray = make([]interface{}, len(v))
		for i, item := range v {
			delegatesArray[i] = item
		}
	default:
		return validator.AccountSet{}, fmt.Errorf("unexpected RPC data type: %T", data)
	}

	if len(delegatesArray) == 0 {
		return validator.AccountSet{}, nil
	}

	delegates := make(validator.AccountSet, 0, len(delegatesArray))

	for _, delegateData := range delegatesArray {
		delegateMap, ok := delegateData.(map[string]interface{})
		if !ok {
			continue
		}

		// Extract address
		addrStr, ok := delegateMap["address"].(string)
		if !ok {
			continue
		}
		addr := types.StringToAddress(addrStr)

		// Extract voting power
		var votingPower *big.Int
		if vpStr, ok := delegateMap["votingPower"].(string); ok {
			if vp, ok := new(big.Int).SetString(vpStr, 10); ok {
				votingPower = vp
			}
		} else if vpFloat, ok := delegateMap["votingPower"].(float64); ok {
			votingPower = new(big.Int).SetUint64(uint64(vpFloat))
		} else if vpInt, ok := delegateMap["votingPower"].(int64); ok {
			votingPower = new(big.Int).SetInt64(vpInt)
		}

		if votingPower == nil {
			// Default voting power
			votingPower = new(big.Int)
			votingPower.SetString("1000000000000000000000", 10) // 1 ETH
		}

		// Extract active status
		isActive := true // Default to active
		if active, ok := delegateMap["isActive"].(bool); ok {
			isActive = active
		}

		delegate := &validator.ValidatorMetadata{
			Address:     addr,
			VotingPower: votingPower,
			IsActive:    isActive,
		}

		delegates = append(delegates, delegate)
	}

	return delegates, nil
}

// parseStakingInfoFromRPC parses staking info data from RPC response
func parseStakingInfoFromRPC(data interface{}) ([]*dpos.StakeInfo, error) {
	if data == nil {
		return []*dpos.StakeInfo{}, fmt.Errorf("RPC data is nil")
	}

	// Try to parse as array/slice
	var stakingArray []interface{}
	switch v := data.(type) {
	case []interface{}:
		stakingArray = v
	case []map[string]interface{}:
		// Convert to []interface{}
		stakingArray = make([]interface{}, len(v))
		for i, item := range v {
			stakingArray[i] = item
		}
	default:
		return []*dpos.StakeInfo{}, fmt.Errorf("unexpected RPC data type: %T", data)
	}

	if len(stakingArray) == 0 {
		return []*dpos.StakeInfo{}, nil
	}

	stakingInfos := make([]*dpos.StakeInfo, 0, len(stakingArray))

	for _, stakingData := range stakingArray {
		stakingMap, ok := stakingData.(map[string]interface{})
		if !ok {
			continue
		}

		// Extract staker address
		stakerStr, ok := stakingMap["staker"].(string)
		if !ok {
			continue
		}
		staker := types.StringToAddress(stakerStr)

		// Extract amount
		var amount *big.Int
		if amountStr, ok := stakingMap["amount"].(string); ok {
			if amt, ok := new(big.Int).SetString(amountStr, 10); ok {
				amount = amt
			}
		} else if amountFloat, ok := stakingMap["amount"].(float64); ok {
			amount = new(big.Int).SetUint64(uint64(amountFloat))
		}

		if amount == nil {
			amount = big.NewInt(0)
		}

		// Extract delegate address
		var delegate types.Address = types.ZeroAddress
		if delegateStr, ok := stakingMap["delegate"].(string); ok {
			delegate = types.StringToAddress(delegateStr)
		}

		// Extract timestamps
		var startTime, endTime uint64
		if startTimeFloat, ok := stakingMap["startTime"].(float64); ok {
			startTime = uint64(startTimeFloat)
		}
		if endTimeFloat, ok := stakingMap["endTime"].(float64); ok {
			endTime = uint64(endTimeFloat)
		}

		// Extract boolean flags
		isLocked := false
		if locked, ok := stakingMap["isLocked"].(bool); ok {
			isLocked = locked
		}

		isActive := true // Default to active
		if active, ok := stakingMap["isActive"].(bool); ok {
			isActive = active
		}

		// Extract rewards
		var rewards *big.Int = big.NewInt(0)
		if rewardsStr, ok := stakingMap["rewards"].(string); ok {
			if rew, ok := new(big.Int).SetString(rewardsStr, 10); ok {
				rewards = rew
			}
		}

		stakingInfo := &dpos.StakeInfo{
			Staker:    staker,
			Amount:    amount,
			StartTime: startTime,
			EndTime:   endTime,
			IsLocked:  isLocked,
			IsActive:  isActive,
			Delegate:  delegate,
			Rewards:   rewards,
		}

		stakingInfos = append(stakingInfos, stakingInfo)
	}

	return stakingInfos, nil
}

// parseConsensusStateFromRPC parses consensus state data from RPC response
func parseConsensusStateFromRPC(data interface{}) (uint64, types.Address, error) {
	if data == nil {
		return 1, types.ZeroAddress, fmt.Errorf("RPC data is nil")
	}

	// Try to parse as map
	consensusMap, ok := data.(map[string]interface{})
	if !ok {
		return 1, types.ZeroAddress, fmt.Errorf("unexpected RPC data type: %T", data)
	}

	// Extract current round
	var currentRound uint64 = 1 // Default to round 1
	if roundFloat, ok := consensusMap["currentRound"].(float64); ok {
		currentRound = uint64(roundFloat)
	} else if roundInt, ok := consensusMap["currentRound"].(int64); ok {
		currentRound = uint64(roundInt)
	} else if roundStr, ok := consensusMap["currentRound"].(string); ok {
		if round, err := strconv.ParseUint(roundStr, 10, 64); err == nil {
			currentRound = round
		}
	}

	// Extract current delegate
	var currentDelegate types.Address = types.ZeroAddress
	if delegateStr, ok := consensusMap["currentDelegate"].(string); ok {
		currentDelegate = types.StringToAddress(delegateStr)
	}

	// Extract delegate index if available
	if delegateIndexFloat, ok := consensusMap["currentDelegateIndex"].(float64); ok {
		_ = uint64(delegateIndexFloat) // Ignore unused variable
	}

	return currentRound, currentDelegate, nil
}

// convertVotingPowerToStakingInfo converts voting power info to staking info
func convertVotingPowerToStakingInfo(data interface{}) ([]*dpos.StakeInfo, error) {
	if data == nil {
		return []*dpos.StakeInfo{}, fmt.Errorf("voting power data is nil")
	}

	// Try to parse as map or array
	var votingPowerMap map[string]interface{}
	var votingPowerArray []interface{}

	switch v := data.(type) {
	case map[string]interface{}:
		votingPowerMap = v
	case []interface{}:
		votingPowerArray = v
	default:
		return []*dpos.StakeInfo{}, fmt.Errorf("unexpected voting power data type: %T", data)
	}

	stakingInfos := make([]*dpos.StakeInfo, 0)

	// Handle map format
	if votingPowerMap != nil {
		for delegateStr, powerData := range votingPowerMap {
			delegate := types.StringToAddress(delegateStr)
			
			var votingPower *big.Int = big.NewInt(0)
			switch vp := powerData.(type) {
			case string:
				if vp, ok := new(big.Int).SetString(vp, 10); ok {
					votingPower = vp
				}
			case float64:
				votingPower = new(big.Int).SetUint64(uint64(vp))
			case int64:
				votingPower = new(big.Int).SetInt64(vp)
			}

			stakingInfo := &dpos.StakeInfo{
				Staker:    delegate, // Self-delegation
				Amount:    votingPower,
				StartTime: uint64(time.Now().Unix()),
				EndTime:   0,
				IsLocked:  false,
				IsActive:  true,
				Delegate:  delegate,
				Rewards:   big.NewInt(0),
			}

			stakingInfos = append(stakingInfos, stakingInfo)
		}
	}

	// Handle array format
	if votingPowerArray != nil {
		for _, powerData := range votingPowerArray {
			powerMap, ok := powerData.(map[string]interface{})
			if !ok {
				continue
			}

			// Extract delegate address
			delegateStr, ok := powerMap["delegate"].(string)
			if !ok {
				continue
			}
			delegate := types.StringToAddress(delegateStr)

			// Extract voting power
			var votingPower *big.Int = big.NewInt(0)
			if vpStr, ok := powerMap["votingPower"].(string); ok {
				if vp, ok := new(big.Int).SetString(vpStr, 10); ok {
					votingPower = vp
				}
			} else if vpFloat, ok := powerMap["votingPower"].(float64); ok {
				votingPower = new(big.Int).SetUint64(uint64(vpFloat))
			}

			stakingInfo := &dpos.StakeInfo{
				Staker:    delegate, // Self-delegation
				Amount:    votingPower,
				StartTime: uint64(time.Now().Unix()),
				EndTime:   0,
				IsLocked:  false,
				IsActive:  true,
				Delegate:  delegate,
				Rewards:   big.NewInt(0),
			}

			stakingInfos = append(stakingInfos, stakingInfo)
		}
	}

	return stakingInfos, nil
}

// getDPoSDelegatesFromLocalState attempts to get DPoS delegates from local state database
func getDPoSDelegatesFromLocalState(blockNumber uint64) (validator.AccountSet, error) {
	if params.dataDir == "" {
		return validator.AccountSet{}, fmt.Errorf("data directory not specified")
	}

	// Try to get from genesis first (most reliable for initial delegates)
	delegates, err := getDelegatesFromGenesis()
	if err != nil {
	} else if len(delegates) > 0 {
		return delegates, nil
	}

	// If no genesis delegates, try to get from local state files
	// Look for DPoS state files
	statePaths := []string{
		filepath.Join(params.dataDir, "consensus", "dpos.db"),
		filepath.Join(params.dataDir, "dpos.db"),
		filepath.Join(params.dataDir, "chaindata", "consensus", "dpos.db"),
	}

	for _, statePath := range statePaths {
		if _, err := os.Stat(statePath); err == nil {
			// Try to read delegates from this file
			delegates, err := readDelegatesFromStateFile(statePath)
			if err == nil && len(delegates) > 0 {
				return delegates, nil
			}
		}
	}

	// If still no delegates found, return empty set
	return validator.AccountSet{}, nil
}

// readDelegatesFromStateFile reads delegates from a DPoS state file
func readDelegatesFromStateFile(filePath string) (validator.AccountSet, error) {
	// This would read and parse the DPoS state file
	// For now, return empty set
	return validator.AccountSet{}, nil
}

// getDelegatesFromGenesis attempts to get initial delegates from genesis file
func getDelegatesFromGenesis() (validator.AccountSet, error) {
	if params.dataDir == "" {
		return validator.AccountSet{}, fmt.Errorf("data directory not specified")
	}

	genesisPath := filepath.Join(params.dataDir, "genesis.json")
	if _, err := os.Stat(genesisPath); os.IsNotExist(err) {
		return validator.AccountSet{}, fmt.Errorf("genesis file not found at %s", genesisPath)
	}

	// Read and parse genesis file
	genesisData, err := os.ReadFile(genesisPath)
	if err != nil {
		return validator.AccountSet{}, fmt.Errorf("failed to read genesis file: %w", err)
	}

	var genesis map[string]interface{}
	if err := json.Unmarshal(genesisData, &genesis); err != nil {
		return validator.AccountSet{}, fmt.Errorf("failed to parse genesis file: %w", err)
	}

	// Look for DPoS configuration in genesis
	// First try: genesis.params.engine.dpos (correct path)
	if params, ok := genesis["params"].(map[string]interface{}); ok {
		if engineConfig, ok := params["engine"].(map[string]interface{}); ok {
			if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
				if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok {
					delegates := make(validator.AccountSet, 0, len(initialDelegates))

					for _, delegateData := range initialDelegates {
						if delegateMap, ok := delegateData.(map[string]interface{}); ok {
							if addrStr, ok := delegateMap["address"].(string); ok {
								addr := types.StringToAddress(addrStr)

								// Parse voting power from stake field
								votingPower := new(big.Int)
								if stakeStr, ok := delegateMap["stake"].(string); ok {
									if vp, ok := new(big.Int).SetString(stakeStr, 10); ok {
										votingPower = vp
									} else {
										// Fallback to default if parsing fails
										votingPower.SetString("1000000000000000000000", 10) // Default 1 token
									}
								} else {
									// Fallback to default if stake field not found
									votingPower.SetString("1000000000000000000000", 10) // Default 1 token
								}

								// All initial delegates are active by default
								isActive := true

								delegate := &validator.ValidatorMetadata{
									Address:     addr,
									VotingPower: votingPower,
									IsActive:    isActive,
								}

								delegates = append(delegates, delegate)
							}
						}
					}

					if len(delegates) > 0 {
						return delegates, nil
					}
				}
			}
		}
	}

	// Second try: genesis.engine.dpos (alternative path)
	if engineConfig, ok := genesis["engine"].(map[string]interface{}); ok {
		if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
			if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok {
				delegates := make(validator.AccountSet, 0, len(initialDelegates))

				for _, delegateData := range initialDelegates {
					if delegateMap, ok := delegateData.(map[string]interface{}); ok {
						if addrStr, ok := delegateMap["address"].(string); ok {
							addr := types.StringToAddress(addrStr)

							// Parse voting power from stake field
							votingPower := new(big.Int)
							if stakeStr, ok := delegateMap["stake"].(string); ok {
								if vp, ok := new(big.Int).SetString(stakeStr, 10); ok {
									votingPower = vp
								} else {
									votingPower.SetString("1000000000000000000000", 10)
								}
							} else {
								votingPower.SetString("1000000000000000000000", 10)
							}

							delegate := &validator.ValidatorMetadata{
								Address:     addr,
								VotingPower: votingPower,
								IsActive:    true, // All initial delegates are active
							}

							delegates = append(delegates, delegate)
						}
					}
				}

				if len(delegates) > 0 {
					return delegates, nil
				}
			}
		}
	}

	// If no DPoS config found, return empty set
	return validator.AccountSet{}, nil
}

// getDPoSStakingInfoFromLocalState attempts to get DPoS staking information from local state
func getDPoSStakingInfoFromLocalState(blockNumber uint64) ([]*dpos.StakeInfo, error) {
	if params.dataDir == "" {
		return []*dpos.StakeInfo{}, fmt.Errorf("data directory not specified")
	}

	// For now, return empty slice since we don't have persistent staking info
	// In a real implementation, you would read from DPoS state files
	return []*dpos.StakeInfo{}, nil
}

// getDPoSConsensusStatusFromLocalState attempts to get DPoS consensus status from local state
func getDPoSConsensusStatusFromLocalState() (uint64, types.Address, error) {
	if params.dataDir == "" {
		return 0, types.ZeroAddress, fmt.Errorf("data directory not specified")
	}

	// Get current block height from JSON-RPC client
	client, err := jsonrpc.NewClient(params.jsonRPC)
	if err != nil {
		return 1, types.ZeroAddress, err
	}

	blockNumber, err := client.Eth().BlockNumber()
	if err != nil {
		return 1, types.ZeroAddress, err
	}

	// Try to get consensus status from genesis file first
	genesisPath := filepath.Join(params.dataDir, "genesis.json")
	if _, err := os.Stat(genesisPath); err == nil {
		// Read and parse genesis file
		genesisData, err := os.ReadFile(genesisPath)
		if err == nil {
			var genesis map[string]interface{}
			if err := json.Unmarshal(genesisData, &genesis); err == nil {
				// Look for DPoS configuration in genesis
				// First try: genesis.params.engine.dpos (correct path)
				if params, ok := genesis["params"].(map[string]interface{}); ok {
					if engineConfig, ok := params["engine"].(map[string]interface{}); ok {
						if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
							// Try to get delegate count and calculate current round
							if delegateCount, ok := dposConfig["delegateCount"].(float64); ok {
								delegateCountInt := int(delegateCount)

								// Calculate current round based on block height
								// Each delegate produces one block per round
								// Round starts from 1
								currentRound := uint64(1)
								if blockNumber > 0 {
									// Calculate round: (blockNumber - 1) / delegateCount + 1
									currentRound = uint64((blockNumber-1)/uint64(delegateCountInt)) + 1
								}

								// Calculate current delegate index
								// Current delegate index: (blockNumber - 1) % delegateCount
								currentDelegateIndex := uint64(0)
								if blockNumber > 0 {
									currentDelegateIndex = (blockNumber - 1) % uint64(delegateCountInt)
								}

								// Try to get current delegate from initial delegates
								var currentDelegate types.Address = types.ZeroAddress
								if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok && len(initialDelegates) > 0 {
									if int(currentDelegateIndex) < len(initialDelegates) {
										if delegateData, ok := initialDelegates[currentDelegateIndex].(map[string]interface{}); ok {
											if addrStr, ok := delegateData["address"].(string); ok {
												currentDelegate = types.StringToAddress(addrStr)
											}
										}
									}
								}

								return currentRound, currentDelegate, nil
							}
						}
					}
				}

				// Second try: genesis.engine.dpos (alternative path)
				if engineConfig, ok := genesis["engine"].(map[string]interface{}); ok {
					if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
						// Try to get delegate count and calculate current round
						if delegateCount, ok := dposConfig["delegateCount"].(float64); ok {
							delegateCountInt := int(delegateCount)

							// Calculate current round based on block height
							currentRound := uint64(1)
							if blockNumber > 0 {
								currentRound = uint64((blockNumber-1)/uint64(delegateCountInt)) + 1
							}

							// Calculate current delegate index
							currentDelegateIndex := uint64(0)
							if blockNumber > 0 {
								currentDelegateIndex = (blockNumber - 1) % uint64(delegateCountInt)
							}

							// Try to get current delegate from initial delegates
							var currentDelegate types.Address = types.ZeroAddress
							if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok && len(initialDelegates) > 0 {
								if int(currentDelegateIndex) < len(initialDelegates) {
									if delegateData, ok := initialDelegates[currentDelegateIndex].(map[string]interface{}); ok {
										if addrStr, ok := delegateData["address"].(string); ok {
											currentDelegate = types.StringToAddress(addrStr)
											return currentRound, currentDelegate, nil
										}
									}
								}
							}

							return currentRound, currentDelegate, nil
						}
					}
				}
			}
		}
	}

	// If no genesis config found, return default values
	return 1, types.ZeroAddress, nil
}

// getStakingInfoFromGenesisDelegates attempts to get staking info from genesis delegates
func getStakingInfoFromGenesisDelegates() ([]*dpos.StakeInfo, error) {
	if params.dataDir == "" {
		return []*dpos.StakeInfo{}, fmt.Errorf("data directory not specified")
	}

	// Try to get delegates from genesis first
	delegates, err := getDelegatesFromGenesis()
	if err != nil {
		return []*dpos.StakeInfo{}, nil
	}

	if len(delegates) == 0 {
		return []*dpos.StakeInfo{}, nil
	}

	// Convert delegates to staking info
	stakingInfos := make([]*dpos.StakeInfo, 0, len(delegates))
	for _, delegate := range delegates {
		stakingInfo := &dpos.StakeInfo{
			Staker:    delegate.Address,
			Amount:    new(big.Int).Set(delegate.VotingPower),
			StartTime: uint64(time.Now().Unix()), // Use current time as start time
			EndTime:   0,                         // No end time for initial delegates
			IsLocked:  false,                     // Initial delegates are not locked
			IsActive:  delegate.IsActive,
			Rewards:   big.NewInt(0),    // No rewards initially
			Delegate:  delegate.Address, // Self-delegation
		}
		stakingInfos = append(stakingInfos, stakingInfo)
	}

	return stakingInfos, nil
}

// calculateConsensusStatusFromGenesis calculates consensus status based on genesis config and current block height
func calculateConsensusStatusFromGenesis() (uint64, types.Address, error) {
	if params.dataDir == "" {
		return 1, types.ZeroAddress, fmt.Errorf("data directory not specified")
	}

	// Get current block height from JSON-RPC client
	client, err := jsonrpc.NewClient(params.jsonRPC)
	if err != nil {
		return 1, types.ZeroAddress, err
	}

	blockNumber, err := client.Eth().BlockNumber()
	if err != nil {
		return 1, types.ZeroAddress, err
	}

	// Try to get consensus status from genesis file
	genesisPath := filepath.Join(params.dataDir, "genesis.json")
	if _, err := os.Stat(genesisPath); err == nil {
		// Read and parse genesis file
		genesisData, err := os.ReadFile(genesisPath)
		if err == nil {
			var genesis map[string]interface{}
			if err := json.Unmarshal(genesisData, &genesis); err == nil {
				// Look for DPoS configuration in genesis
				// First try: genesis.params.engine.dpos (correct path)
				if params, ok := genesis["params"].(map[string]interface{}); ok {
					if engineConfig, ok := params["engine"].(map[string]interface{}); ok {
						if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
							// Try to get delegate count and calculate current round
							if delegateCount, ok := dposConfig["delegateCount"].(float64); ok {
								delegateCountInt := int(delegateCount)

								// Calculate current round based on block height
								// Each delegate produces one block per round
								// Round starts from 1
								currentRound := uint64(1)
								if blockNumber > 0 {
									// Calculate round: (blockNumber - 1) / delegateCount + 1
									currentRound = uint64((blockNumber-1)/uint64(delegateCountInt)) + 1
								}

								// Calculate current delegate index
								// Current delegate index: (blockNumber - 1) % delegateCount
								currentDelegateIndex := uint64(0)
								if blockNumber > 0 {
									currentDelegateIndex = (blockNumber - 1) % uint64(delegateCountInt)
								}

								// Try to get current delegate from initial delegates
								var currentDelegate types.Address = types.ZeroAddress
								if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok && len(initialDelegates) > 0 {
									if int(currentDelegateIndex) < len(initialDelegates) {
										if delegateData, ok := initialDelegates[currentDelegateIndex].(map[string]interface{}); ok {
											if addrStr, ok := delegateData["address"].(string); ok {
												currentDelegate = types.StringToAddress(addrStr)
											}
										}
									}
								}

								return currentRound, currentDelegate, nil
							}
						}
					}
				}

				// Second try: genesis.engine.dpos (alternative path)
				if engineConfig, ok := genesis["engine"].(map[string]interface{}); ok {
					if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
						// Try to get delegate count and calculate current round
						if delegateCount, ok := dposConfig["delegateCount"].(float64); ok {
							delegateCountInt := int(delegateCount)

							// Calculate current round based on block height
							// Each delegate produces one block per round
							// Round starts from 1
							currentRound := uint64(1)
							if blockNumber > 0 {
								// Calculate round: (blockNumber - 1) / delegateCount + 1
								currentRound = uint64((blockNumber-1)/uint64(delegateCountInt)) + 1
							}

							// Calculate current delegate index
							// Current delegate index: (blockNumber - 1) % delegateCount
							currentDelegateIndex := uint64(0)
							if blockNumber > 0 {
								currentDelegateIndex = (blockNumber - 1) % uint64(delegateCountInt)
							}

							// Try to get current delegate from initial delegates
							var currentDelegate types.Address = types.ZeroAddress
							if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok && len(initialDelegates) > 0 {
								if int(currentDelegateIndex) < len(initialDelegates) {
									if delegateData, ok := initialDelegates[currentDelegateIndex].(map[string]interface{}); ok {
										if addrStr, ok := delegateData["address"].(string); ok {
											currentDelegate = types.StringToAddress(addrStr)
											return currentRound, currentDelegate, nil
										}
									}
								}
							}

							return currentRound, currentDelegate, nil
						}
					}
				}
			}
		}
	}

	// If no genesis config found, return default values
	return 1, types.ZeroAddress, nil
}

// getMapKeys returns the keys of a map as a slice of strings
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
