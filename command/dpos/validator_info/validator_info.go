package validator_info

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
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
	"go.etcd.io/bbolt"
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

	// Try to create JSON-RPC client and get runtime data
	var blockNumber uint64 = 0
	var client *jsonrpc.Client
	var err error

	// Try to connect to JSON-RPC server
	client, err = jsonrpc.NewClient(params.jsonRPC)
	if err == nil {
		// Try to get current block number
		blockNumber, err = client.Eth().BlockNumber()
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to get block number from JSON-RPC: %v\n", err)
			fmt.Printf("📋 Falling back to local data sources...\n")
			client = nil // Mark client as unavailable
		} else {
			fmt.Printf("✅ Connected to JSON-RPC server, block number: %d\n", blockNumber)
		}
	} else {
		fmt.Printf("⚠️  Warning: Failed to connect to JSON-RPC server: %v\n", err)
		fmt.Printf("📋 Falling back to local data sources...\n")
		client = nil // Mark client as unavailable
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
	var delegates validator.AccountSet
	var err error

	// Try to get DPoS data from running node first (if client is available)
	if client != nil {
		fmt.Printf("🔄 Attempting to get delegates from running node (memory state)...\n")
		delegates, err = getDPoSDelegatesFromNode(client, blockNumber)
		if err != nil {
			fmt.Printf("⚠️  Failed to get delegates from node: %v\n", err)
			fmt.Printf("📋 Falling back to local state...\n")
		} else {
			fmt.Printf("✅ Successfully got delegates from running node (memory): %d delegates\n", len(delegates))
		}
	} else {
		fmt.Printf("📋 JSON-RPC client not available, using local data sources...\n")
	}

	// If node data failed or client unavailable, return error (no fallback to files)
	if client == nil || err != nil {
		return nil, fmt.Errorf("failed to get DPoS delegates from running node: %w", err)
	}

	// Get staking information
	var stakingInfo []*dpos.StakeInfo
	if client != nil {
		fmt.Printf("🔄 Attempting to get staking info from running node...\n")
		stakingInfo, err = getDPoSStakingInfoFromNode(client, blockNumber)
		if err != nil {
			fmt.Printf("⚠️  Failed to get staking info from node: %v\n", err)
			fmt.Printf("📋 Falling back to local state...\n")
		} else {
			fmt.Printf("✅ Successfully got staking info from running node: %d stakers\n", len(stakingInfo))
		}
	}

	// If node data failed or client unavailable, return error (no fallback to files)
	if client == nil || err != nil {
		return nil, fmt.Errorf("failed to get DPoS staking info from running node: %w", err)
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

	// Method 1: Try dpos_getAllValidators using direct HTTP call (new method to get all validators including zero voting power)
	fmt.Printf("🔍 Calling dpos_getAllValidators with blockNumber: %d\n", blockNumber)
	allValidators, err := callCustomRPCMethodDirect(params.jsonRPC, "dpos_getAllValidators", []interface{}{})
	if err == nil && allValidators != nil {
		fmt.Printf("🔍 dpos_getAllValidators returned data: %+v\n", allValidators)
		result, parseErr := parseDelegatesFromRPC(allValidators)
		if parseErr == nil && len(result) > 0 {
			fmt.Printf("✅ Successfully parsed %d delegates from dpos_getAllValidators\n", len(result))
			return result, nil
		} else {
			fmt.Printf("⚠️  Failed to parse delegates from dpos_getAllValidators: %v\n", parseErr)
		}
	} else {
		fmt.Printf("⚠️  dpos_getAllValidators failed: %v\n", err)
	}

	// Method 2: Try dpos_getDelegates using direct HTTP call
	fmt.Printf("🔍 Calling dpos_getDelegates with blockNumber: %d\n", blockNumber)
	delegates, err := callCustomRPCMethodDirect(params.jsonRPC, "dpos_getDelegates", []interface{}{})
	if err == nil && delegates != nil {
		fmt.Printf("🔍 dpos_getDelegates returned data: %+v\n", delegates)
		result, parseErr := parseDelegatesFromRPC(delegates)
		if parseErr == nil && len(result) > 0 {
			fmt.Printf("✅ Successfully parsed %d delegates from dpos_getDelegates\n", len(result))
			return result, nil
		} else {
			fmt.Printf("⚠️  Failed to parse delegates from dpos_getDelegates: %v\n", parseErr)
		}
	} else {
		fmt.Printf("⚠️  dpos_getDelegates failed: %v\n", err)
	}

	// Method 3: Try dpos_getValidatorSet using direct HTTP call
	fmt.Printf("🔍 Calling dpos_getValidatorSet with blockNumber: %d\n", blockNumber)
	validators, err := callCustomRPCMethodDirect(params.jsonRPC, "dpos_getValidatorSet", []interface{}{})
	if err == nil && validators != nil {
		fmt.Printf("🔍 dpos_getValidatorSet returned data: %+v\n", validators)
		result, parseErr := parseDelegatesFromRPC(validators)
		if parseErr == nil && len(result) > 0 {
			fmt.Printf("✅ Successfully parsed %d delegates from dpos_getValidatorSet\n", len(result))
			return result, nil
		} else {
			fmt.Printf("⚠️  Failed to parse delegates from dpos_getValidatorSet: %v\n", parseErr)
		}
	} else {
		fmt.Printf("⚠️  dpos_getValidatorSet failed: %v\n", err)
	}

	// If all RPC methods fail, return error to trigger fallback to local state
	return validator.AccountSet{}, fmt.Errorf("all RPC methods failed to get delegates")
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
	// Try to call the method using the client's Call method
	var result interface{}

	// Create a simple RPC call structure
	callParams := make([]interface{}, 0)
	if len(params) > 0 {
		callParams = params
	}

	// For DPoS methods, try without parameters first (blockNumber is optional)
	if method == "dpos_getDelegates" || method == "dpos_getValidatorSet" {
		fmt.Printf("🔍 Trying %s without parameters first...\n", method)
		// Use a more generic result type to avoid JSON unmarshal issues
		var genericResult interface{}
		err := client.Call(method, []interface{}{}, &genericResult)
		if err == nil {
			fmt.Printf("🔍 Raw RPC result for %s (no params): %+v (type: %T)\n", method, genericResult, genericResult)
			return genericResult, nil
		}
		fmt.Printf("⚠️  %s without parameters failed: %v\n", method, err)
	}

	// Try to call the method - fix JSON unmarshal issue
	err := client.Call(method, callParams, &result)
	if err != nil {
		// Return error to indicate method not implemented or failed
		return nil, fmt.Errorf("custom RPC method %s failed: %w", method, err)
	}

	// Debug: Print the raw result
	fmt.Printf("🔍 Raw RPC result for %s: %+v (type: %T)\n", method, result, result)

	return result, nil
}

// parseDelegatesFromRPC parses delegate data from RPC response
func parseDelegatesFromRPC(data interface{}) (validator.AccountSet, error) {
	if data == nil {
		return validator.AccountSet{}, fmt.Errorf("RPC data is nil")
	}

	fmt.Printf("🔍 Parsing RPC data: type=%T, value=%+v\n", data, data)

	// Try to parse as array/slice
	var delegatesArray []interface{}
	switch v := data.(type) {
	case []interface{}:
		delegatesArray = v
		fmt.Printf("🔍 Data is []interface{} with %d items\n", len(v))
	case []map[string]interface{}:
		// Convert to []interface{}
		delegatesArray = make([]interface{}, len(v))
		for i, item := range v {
			delegatesArray[i] = item
		}
		fmt.Printf("🔍 Data is []map[string]interface{} with %d items\n", len(v))
	default:
		fmt.Printf("⚠️  Unexpected RPC data type: %T\n", data)
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

		// Extract address - try different field name variations
		var addrStr string
		var addrOk bool
		if addrStr, addrOk = delegateMap["Address"].(string); !addrOk {
			// Fallback to lowercase
			addrStr, addrOk = delegateMap["address"].(string)
		}
		if !addrOk {
			fmt.Printf("⚠️  Failed to extract address from delegate data: %+v\n", delegateMap)
			continue
		}
		addr := types.StringToAddress(addrStr)

		// Extract voting power - handle both string and numeric formats
		var votingPower *big.Int
		// Try different field name variations
		if vpStr, ok := delegateMap["VotingPower"].(string); ok {
			if vp, ok := new(big.Int).SetString(vpStr, 10); ok {
				votingPower = vp
			}
		} else if vpFloat, ok := delegateMap["VotingPower"].(float64); ok {
			votingPower = new(big.Int).SetUint64(uint64(vpFloat))
		} else if vpInt, ok := delegateMap["VotingPower"].(int64); ok {
			votingPower = new(big.Int).SetInt64(vpInt)
		} else if vpUint, ok := delegateMap["VotingPower"].(uint64); ok {
			votingPower = new(big.Int).SetUint64(vpUint)
		} else if vpStr, ok := delegateMap["votingPower"].(string); ok {
			// Fallback to lowercase
			if vp, ok := new(big.Int).SetString(vpStr, 10); ok {
				votingPower = vp
			}
		} else if vpFloat, ok := delegateMap["votingPower"].(float64); ok {
			// Fallback to lowercase
			votingPower = new(big.Int).SetUint64(uint64(vpFloat))
		} else if vpInt, ok := delegateMap["votingPower"].(int64); ok {
			// Fallback to lowercase
			votingPower = new(big.Int).SetInt64(vpInt)
		} else if vpUint, ok := delegateMap["votingPower"].(uint64); ok {
			// Fallback to lowercase
			votingPower = new(big.Int).SetUint64(vpUint)
		}

		if votingPower == nil {
			// Default voting power
			votingPower = new(big.Int)
			votingPower.SetString("1000000000000000000000", 10) // 1 ETH
		}

		// Extract active status
		isActive := true // Default to active
		if active, ok := delegateMap["IsActive"].(bool); ok {
			isActive = active
		} else if active, ok := delegateMap["isActive"].(bool); ok {
			// Fallback to lowercase
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

	// Handle array format
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

	return stakingInfos, nil
}

// getDPoSDelegatesFromLocalState attempts to get DPoS delegates from local state database
func getDPoSDelegatesFromLocalState(blockNumber uint64) (validator.AccountSet, error) {
	if params.dataDir == "" {
		return validator.AccountSet{}, fmt.Errorf("data directory not specified")
	}

	// Try to get from local state files first (runtime state)
	// Dynamically search for DPoS database files in the data directory
	fmt.Printf("🔍 Searching for DPoS database files in data directory: %s\n", params.dataDir)

	// Search for all .db files in the data directory and subdirectories
	dbFiles, err := findDatabaseFiles(params.dataDir)
	if err != nil {
		fmt.Printf("⚠️  Error searching for database files: %v\n", err)
	} else {
		fmt.Printf("🔍 Found %d potential database files\n", len(dbFiles))
		for _, dbFile := range dbFiles {
			fmt.Printf("🔍 Checking database file: %s\n", dbFile)
			// Try to read delegates from this file with timeout
			delegates, err := readDelegatesFromStateFileWithTimeout(dbFile, 10*time.Second)
			if err == nil && len(delegates) > 0 {
				fmt.Printf("✅ Successfully read %d delegates from database file: %s\n", len(delegates), dbFile)
				return delegates, nil
			} else {
				fmt.Printf("⚠️  Failed to read delegates from database file %s: %v\n", dbFile, err)
			}
		}
	}

	// If no local state delegates, try to get from genesis (fallback)
	delegates, err := getDelegatesFromGenesis()
	if err != nil {
		return validator.AccountSet{}, err
	}

	return delegates, nil
}

// findDatabaseFiles recursively searches for database files in the given directory
func findDatabaseFiles(dataDir string) ([]string, error) {
	var dbFiles []string

	// Walk through the directory tree
	err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		// Check if it's a .db file
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(path), ".db") {
			dbFiles = append(dbFiles, path)
		}

		return nil
	})

	return dbFiles, err
}

// readDelegatesFromStateFileWithTimeout reads delegates from a DPoS state file with timeout
func readDelegatesFromStateFileWithTimeout(filePath string, timeout time.Duration) (validator.AccountSet, error) {
	resultChan := make(chan struct {
		delegates validator.AccountSet
		err       error
	}, 1)

	go func() {
		delegates, err := readDelegatesFromStateFile(filePath)
		resultChan <- struct {
			delegates validator.AccountSet
			err       error
		}{delegates, err}
	}()

	select {
	case result := <-resultChan:
		return result.delegates, result.err
	case <-time.After(timeout):
		return validator.AccountSet{}, fmt.Errorf("timeout reading delegates from %s after %v", filePath, timeout)
	}
}

// readDelegatesFromStateFile reads delegates from a DPoS state file
func readDelegatesFromStateFile(filePath string) (validator.AccountSet, error) {
	fmt.Printf("🔍 Attempting to read delegates from BoltDB: %s\n", filePath)

	// Open the BoltDB database with timeout
	db, err := bbolt.Open(filePath, 0600, &bbolt.Options{
		ReadOnly: true,
		Timeout:  5 * time.Second, // 5 second timeout
	})
	if err != nil {
		fmt.Printf("⚠️  Failed to open BoltDB file: %v\n", err)
		return validator.AccountSet{}, fmt.Errorf("failed to open BoltDB file: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Printf("⚠️  Error closing database: %v\n", closeErr)
		}
	}()

	var delegates validator.AccountSet

	// Try to read from different buckets
	buckets := []string{
		"fullValidatorSetBucket",
		"DelegateInfo",
		"validatorSet",
		"delegates",
	}

	for _, bucketName := range buckets {
		err = db.View(func(tx *bbolt.Tx) error {
			bucket := tx.Bucket([]byte(bucketName))
			if bucket == nil {
				return nil // Bucket doesn't exist, try next one
			}

			fmt.Printf("🔍 Found bucket: %s\n", bucketName)

			// Iterate through all key-value pairs in the bucket
			cursor := bucket.Cursor()
			for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
				fmt.Printf("🔍 Processing key: %s, value length: %d\n", string(key), len(value))

				// Try to parse as JSON
				var delegateData map[string]interface{}
				if err := json.Unmarshal(value, &delegateData); err != nil {
					fmt.Printf("⚠️  Failed to parse JSON for key %s: %v\n", string(key), err)
					continue
				}

				// Try to extract delegate information
				if addrStr, ok := delegateData["address"].(string); ok {
					addr := types.StringToAddress(addrStr)

					// Parse voting power
					votingPower := new(big.Int)
					if stakeStr, ok := delegateData["stake"].(string); ok {
						if vp, ok := new(big.Int).SetString(stakeStr, 10); ok {
							votingPower = vp
						}
					} else if stakeFloat, ok := delegateData["stake"].(float64); ok {
						votingPower.SetUint64(uint64(stakeFloat))
					} else if stakeInt, ok := delegateData["stake"].(int64); ok {
						votingPower.SetInt64(stakeInt)
					}

					// Check if delegate is active
					isActive := true
					if active, ok := delegateData["isActive"].(bool); ok {
						isActive = active
					}

					delegate := &validator.ValidatorMetadata{
						Address:     addr,
						VotingPower: votingPower,
						IsActive:    isActive,
					}

					fmt.Printf("✅ Found delegate: %s, votingPower: %s\n", addrStr, votingPower.String())
					delegates = append(delegates, delegate)
				}
			}
			return nil
		})

		if err != nil {
			fmt.Printf("⚠️  Error reading bucket %s: %v\n", bucketName, err)
			continue
		}

		// If we found delegates, return them
		if len(delegates) > 0 {
			fmt.Printf("✅ Successfully read %d delegates from bucket %s\n", len(delegates), bucketName)
			return delegates, nil
		}
	}

	fmt.Printf("⚠️  No delegates found in any bucket\n")
	return validator.AccountSet{}, fmt.Errorf("no delegates found in database")
}

// callCustomRPCMethodDirect makes a direct HTTP JSON-RPC call to avoid client parsing issues
func callCustomRPCMethodDirect(jsonRPCURL, method string, params []interface{}) (interface{}, error) {
	// Create JSON-RPC request
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}

	// Marshal request to JSON
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	fmt.Printf("🔍 Making direct HTTP call to %s with method %s\n", jsonRPCURL, method)
	fmt.Printf("🔍 Request body: %s\n", string(requestBody))

	// Make HTTP POST request
	resp, err := http.Post(jsonRPCURL, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	fmt.Printf("🔍 Response body: %s\n", string(responseBody))

	// Parse JSON-RPC response
	var jsonRPCResponse struct {
		JSONRPC string      `json:"jsonrpc"`
		Result  interface{} `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ID int `json:"id"`
	}

	if err := json.Unmarshal(responseBody, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to parse JSON-RPC response: %w", err)
	}

	if jsonRPCResponse.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error: %s (code: %d)", jsonRPCResponse.Error.Message, jsonRPCResponse.Error.Code)
	}

	return jsonRPCResponse.Result, nil
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

	// Debug: Print genesis file structure (commented out for production)
	// fmt.Printf("🔍 DEBUG: Genesis file loaded from %s\n", genesisPath)
	// fmt.Printf("🔍 DEBUG: Genesis keys: %v\n", getMapKeys(genesis))

	// Look for DPoS configuration in genesis
	// First try: genesis.params.engine.dpos (correct path)
	if params, ok := genesis["params"].(map[string]interface{}); ok {
		if engineConfig, ok := params["engine"].(map[string]interface{}); ok {
			if dposConfig, ok := engineConfig["dpos"].(map[string]interface{}); ok {
				if initialDelegates, ok := dposConfig["initialDelegates"].([]interface{}); ok {
					// fmt.Printf("🔍 DEBUG: Found %d initial delegates in genesis\n", len(initialDelegates))
					delegates := make(validator.AccountSet, 0, len(initialDelegates))

					for _, delegateData := range initialDelegates {
						// fmt.Printf("🔍 DEBUG: Processing delegate %d: %v\n", i, delegateData)
						if delegateMap, ok := delegateData.(map[string]interface{}); ok {
							if addrStr, ok := delegateMap["address"].(string); ok {
								addr := types.StringToAddress(addrStr)
								// fmt.Printf("🔍 DEBUG: Delegate %d address: %s\n", i, addrStr)

								// Parse voting power from stake field
								votingPower := new(big.Int)
								if stakeStr, ok := delegateMap["stake"].(string); ok {
									if vp, ok := new(big.Int).SetString(stakeStr, 10); ok {
										votingPower = vp
									} else {
										// Fallback to default if parsing fails
										votingPower.SetString("1000000000000000000000", 10) // Default 1 token
									}
								} else if stakeFloat, ok := delegateMap["stake"].(float64); ok {
									// Handle numeric stake values
									votingPower.SetUint64(uint64(stakeFloat))
								} else if stakeInt, ok := delegateMap["stake"].(int64); ok {
									// Handle integer stake values
									votingPower.SetInt64(stakeInt)
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

								// fmt.Printf("🔍 DEBUG: Added delegate %d: %s, votingPower: %s\n", i, addrStr, votingPower.String())
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
							} else if stakeFloat, ok := delegateMap["stake"].(float64); ok {
								// Handle numeric stake values
								votingPower.SetUint64(uint64(stakeFloat))
							} else if stakeInt, ok := delegateMap["stake"].(int64); ok {
								// Handle integer stake values
								votingPower.SetInt64(stakeInt)
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
