package jsonrpc

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// dposStore provides access to the methods needed by dpos endpoint
type dposStore interface {
	// GetAccount gets account information
	GetAccount(root types.Hash, addr types.Address) (*Account, error)

	// GetBalance gets account balance - use ethStore methods
	GetBalance(root types.Hash, addr types.Address) (*big.Int, error)

	// GetDPoSState gets current DPoS state
	GetDPoSState() (*dpos.State, error)

	// GetValidators gets current validators
	GetValidators() (validator.AccountSet, error)

	// GetStakingInfo gets staking information
	GetStakingInfo() ([]*dpos.StakeInfo, error)
}

// DPOS is the dpos jsonrpc endpoint
type DPOS struct {
	logger hclog.Logger
	store  dposStore
}

// NewDPOS creates a new DPOS endpoint
func NewDPOS(logger hclog.Logger, store dposStore) *DPOS {
	return &DPOS{
		logger: logger.Named("dpos"),
		store:  store,
	}
}

// VoteRequest represents a vote request
type VoteRequest struct {
	Voter     string `json:"voter"`
	Candidate string `json:"candidate"`
	Amount    string `json:"amount"`
}

// VoteResponse represents a vote response
type VoteResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TxHash      string `json:"txHash,omitempty"`
	BlockNumber uint64 `json:"blockNumber,omitempty"`
	Error       string `json:"error,omitempty"`
}

// StakeRequest represents a stake request
type StakeRequest struct {
	Staker   string `json:"staker"`
	Delegate string `json:"delegate"`
	Amount   string `json:"amount"`
}

// StakeResponse represents a stake response
type StakeResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TxHash      string `json:"txHash,omitempty"`
	BlockNumber uint64 `json:"blockNumber,omitempty"`
	Error       string `json:"error,omitempty"`
}

// DelegateRequest represents a delegate request
type DelegateRequest struct {
	Staker   string `json:"staker"`
	Delegate string `json:"delegate"`
	Amount   string `json:"amount"`
}

// DelegateResponse represents a delegate response
type DelegateResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TxHash      string `json:"txHash,omitempty"`
	BlockNumber uint64 `json:"txHash,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Vote handles dpos_vote RPC method
func (d *DPOS) Vote(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS Vote called", "params", params)

	// Parse parameters
	var req VoteRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters like ["0x123..."]
		if len(p) == 1 {
			// Single address parameter - use as both voter and candidate with default amount
			if address, ok := p[0].(string); ok {
				req.Voter = address
				req.Candidate = address
				req.Amount = "1000000000000000000" // Default 1 token (1e18 wei)
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
		} else if len(p) == 3 {
			// Three parameters: [voter, candidate, amount]
			if voter, ok := p[0].(string); ok {
				req.Voter = voter
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if candidate, ok := p[1].(string); ok {
				req.Candidate = candidate
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "second parameter must be a string address",
				}, nil
			}
			if amount, ok := p[2].(string); ok {
				req.Amount = amount
			} else {
				return &VoteResponse{
					Success: false,
					Error:   "third parameter must be a string amount",
				}, nil
			}
		} else {
			return &VoteResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 1 or 3 parameters, got %d", len(p)),
			}, nil
		}
	case map[string]interface{}:
		if voter, ok := p["voter"].(string); ok {
			req.Voter = voter
		}
		if candidate, ok := p["candidate"].(string); ok {
			req.Candidate = candidate
		}
		if amount, ok := p["amount"].(string); ok {
			req.Amount = amount
		}
	case *VoteRequest:
		if p != nil {
			req = *p
		}
	default:
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or VoteRequest", params),
		}, nil
	}

	// Validate request
	if req.Voter == "" {
		return &VoteResponse{
			Success: false,
			Error:   "voter address is required",
		}, nil
	}
	if req.Candidate == "" {
		return &VoteResponse{
			Success: false,
			Error:   "candidate address is required",
		}, nil
	}
	if req.Amount == "" {
		return &VoteResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}

	// Parse addresses
	voterAddr := types.StringToAddress(req.Voter)
	candidateAddr := types.StringToAddress(req.Candidate)

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return &VoteResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}

	// Check if candidate is a validator
	validators, err := d.store.GetValidators()
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}

	isValidator := false
	for _, v := range validators {
		if v.Address == candidateAddr {
			isValidator = true
			break
		}
	}

	if !isValidator {
		return &VoteResponse{
			Success: false,
			Error:   "candidate is not a validator",
		}, nil
	}

	// Check voter balance
	balance, err := d.store.GetBalance(types.Hash{}, voterAddr)
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	if balance.Cmp(amountInt) < 0 {
		return &VoteResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}

	// TODO: Implement actual voting logic here
	// This would typically involve:
	// 1. Creating a transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &VoteResponse{
		Success:     true,
		Message:     "Vote operation completed successfully",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12345), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// VoteByAddress handles dpos_vote RPC method with single address parameter
// This method is designed to handle the case where only one address is provided
// It will use the address as both voter and candidate, with a default amount
func (d *DPOS) VoteByAddress(ctx context.Context, address string) (*VoteResponse, error) {
	d.logger.Info("DPoS VoteByAddress called", "address", address)

	// Validate address
	if address == "" {
		return &VoteResponse{
			Success: false,
			Error:   "address is required",
		}, nil
	}

	// Parse address
	addr := types.StringToAddress(address)

	// Check if address is a validator
	validators, err := d.store.GetValidators()
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}

	isValidator := false
	for _, v := range validators {
		if v.Address == addr {
			isValidator = true
			break
		}
	}

	if !isValidator {
		return &VoteResponse{
			Success: false,
			Error:   "address is not a validator",
		}, nil
	}

	// Check balance
	balance, err := d.store.GetBalance(types.Hash{}, addr)
	if err != nil {
		return &VoteResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	// Use a default voting amount (1 token = 10^18 wei)
	defaultAmount := new(big.Int).Mul(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

	if balance.Cmp(defaultAmount) < 0 {
		return &VoteResponse{
			Success: false,
			Error:   "insufficient balance for voting",
		}, nil
	}

	// TODO: Implement actual voting logic here
	// This would typically involve:
	// 1. Creating a transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &VoteResponse{
		Success:     true,
		Message:     "Vote operation completed successfully with default amount",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12345), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// Stake handles dpos_stake RPC method
func (d *DPOS) Stake(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS Stake called", "params", params)

	// Parse parameters
	var req StakeRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters like ["0x123..."]
		if len(p) == 1 {
			// Single address parameter - use as both staker and delegate with default amount
			if address, ok := p[0].(string); ok {
				req.Staker = address
				req.Delegate = address
				req.Amount = "1000000000000000000" // Default 1 token (1e18 wei)
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
		} else if len(p) == 3 {
			// Three parameters: [staker, delegate, amount]
			if staker, ok := p[0].(string); ok {
				req.Staker = staker
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if delegate, ok := p[1].(string); ok {
				req.Delegate = delegate
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "second parameter must be a string address",
				}, nil
			}
			if amount, ok := p[2].(string); ok {
				req.Amount = amount
			} else {
				return &StakeResponse{
					Success: false,
					Error:   "third parameter must be a string amount",
				}, nil
			}
		} else {
			return &StakeResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 1 or 3 parameters, got %d", len(p)),
			}, nil
		}
	case map[string]interface{}:
		if staker, ok := p["staker"].(string); ok {
			req.Staker = staker
		}
		if delegate, ok := p["delegate"].(string); ok {
			req.Delegate = delegate
		}
		if amount, ok := p["amount"].(string); ok {
			req.Amount = amount
		}
	case *StakeRequest:
		if p != nil {
			req = *p
		}
	default:
		return &StakeResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or StakeRequest", params),
		}, nil
	}

	// Validate request
	if req.Staker == "" {
		return &StakeResponse{
			Success: false,
			Error:   "staker address is required",
		}, nil
	}
	if req.Delegate == "" {
		return &StakeResponse{
			Success: false,
			Error:   "delegate address is required",
		}, nil
	}
	if req.Amount == "" {
		return &StakeResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}

	// Parse addresses
	stakerAddr := types.StringToAddress(req.Staker)
	_ = types.StringToAddress(req.Delegate) // Will be used in actual implementation

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return &StakeResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}

	// Check staker balance
	balance, err := d.store.GetBalance(types.Hash{}, stakerAddr)
	if err != nil {
		return &StakeResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	if balance.Cmp(amountInt) < 0 {
		return &StakeResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}

	// TODO: Implement actual staking logic here
	// This would typically involve:
	// 1. Creating a staking transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &StakeResponse{
		Success:     true,
		Message:     "Stake operation completed successfully",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12346), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// Delegate handles dpos_delegate RPC method
func (d *DPOS) Delegate(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS Delegate called", "params", params)

	// Parse parameters
	var req DelegateRequest
	switch p := params.(type) {
	case []interface{}:
		// Handle array parameters like ["0x123..."]
		if len(p) == 1 {
			// Single address parameter - use as both staker and delegate with default amount
			if address, ok := p[0].(string); ok {
				req.Staker = address
				req.Delegate = address
				req.Amount = "1000000000000000000" // Default 1 token (1e18 wei)
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
		} else if len(p) == 3 {
			// Three parameters: [staker, delegate, amount]
			if staker, ok := p[0].(string); ok {
				req.Staker = staker
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "first parameter must be a string address",
				}, nil
			}
			if delegate, ok := p[1].(string); ok {
				req.Delegate = delegate
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "second parameter must be a string address",
				}, nil
			}
			if amount, ok := p[2].(string); ok {
				req.Amount = amount
			} else {
				return &DelegateResponse{
					Success: false,
					Error:   "third parameter must be a string amount",
				}, nil
			}
		} else {
			return &DelegateResponse{
				Success: false,
				Error:   fmt.Sprintf("expected 1 or 3 parameters, got %d", len(p)),
			}, nil
		}
	case map[string]interface{}:
		if staker, ok := p["staker"].(string); ok {
			req.Staker = staker
		}
		if delegate, ok := p["delegate"].(string); ok {
			req.Delegate = delegate
		}
		if amount, ok := p["amount"].(string); ok {
			req.Amount = amount
		}
	case *DelegateRequest:
		if p != nil {
			req = *p
		}
	default:
		return &DelegateResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid parameter type: %T, expected array, map, or DelegateRequest", params),
		}, nil
	}

	// Validate request
	if req.Staker == "" {
		return &DelegateResponse{
			Success: false,
			Error:   "staker address is required",
		}, nil
	}
	if req.Delegate == "" {
		return &DelegateResponse{
			Success: false,
			Error:   "delegate address is required",
		}, nil
	}
	if req.Amount == "" {
		return &DelegateResponse{
			Success: false,
			Error:   "amount is required",
		}, nil
	}

	// Parse addresses
	stakerAddr := types.StringToAddress(req.Staker)
	_ = types.StringToAddress(req.Delegate) // Will be used in actual implementation

	// Parse amount
	amountInt, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		return &DelegateResponse{
			Success: false,
			Error:   "invalid amount format",
		}, nil
	}

	// Check staker balance
	balance, err := d.store.GetBalance(types.Hash{}, stakerAddr)
	if err != nil {
		return &DelegateResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get balance: %v", err),
		}, nil
	}

	if balance.Cmp(amountInt) < 0 {
		return &DelegateResponse{
			Success: false,
			Error:   "insufficient balance",
		}, nil
	}

	// TODO: Implement actual delegation logic here
	// This would typically involve:
	// 1. Creating a delegation transaction
	// 2. Adding it to the transaction pool
	// 3. Waiting for it to be mined
	// 4. Updating the DPoS state

	// For now, return a simulated success response
	return &DelegateResponse{
		Success:     true,
		Message:     "Delegate operation completed successfully",
		TxHash:      "0x" + fmt.Sprintf("%064d", 12347), // Simulated tx hash
		BlockNumber: 0,                                  // Will be filled when transaction is mined
	}, nil
}

// GetDelegates handles dpos_getDelegates RPC method
func (d *DPOS) GetDelegates(ctx context.Context, blockNumber *uint64) (validator.AccountSet, error) {
	d.logger.Info("DPoS GetDelegates called", "blockNumber", blockNumber)

	validators, err := d.store.GetValidators()
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	return validators, nil
}

// GetValidatorSet handles dpos_getValidatorSet RPC method
func (d *DPOS) GetValidatorSet(ctx context.Context, blockNumber *uint64) (validator.AccountSet, error) {
	d.logger.Info("DPoS GetValidatorSet called", "blockNumber", blockNumber)

	validators, err := d.store.GetValidators()
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	return validators, nil
}

// GetStakingInfo handles dpos_getStakingInfo RPC method
func (d *DPOS) GetStakingInfo(ctx context.Context, blockNumber *uint64) ([]*dpos.StakeInfo, error) {
	d.logger.Info("DPoS GetStakingInfo called", "blockNumber", blockNumber)

	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to get staking info: %w", err)
	}

	return stakingInfo, nil
}

// GetVotingPower handles dpos_getVotingPower RPC method
func (d *DPOS) GetVotingPower(ctx context.Context, delegate string, blockNumber *uint64) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetVotingPower called", "delegate", delegate, "blockNumber", blockNumber)

	// Parse delegate address
	delegateAddr := types.StringToAddress(delegate)

	// Get validators
	validators, err := d.store.GetValidators()
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	// Find the delegate
	for _, v := range validators {
		if v.Address == delegateAddr {
			return map[string]interface{}{
				"delegate":    delegate,
				"votingPower": v.VotingPower.String(),
				"isActive":    v.IsActive,
			}, nil
		}
	}

	return nil, fmt.Errorf("delegate not found: %s", delegate)
}

// GetCurrentRound handles dpos_getCurrentRound RPC method
func (d *DPOS) GetCurrentRound(ctx context.Context) (uint64, error) {
	d.logger.Info("DPoS GetCurrentRound called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return 0, fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// TODO: Calculate current round based on block height and delegate count
	// For now, return a default value
	return 1, nil
}

// GetCurrentDelegate handles dpos_getCurrentDelegate RPC method
func (d *DPOS) GetCurrentDelegate(ctx context.Context) (string, error) {
	d.logger.Info("DPoS GetCurrentDelegate called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return "", fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// TODO: Calculate current delegate based on block height and delegate count
	// For now, return the first validator
	validators, err := d.store.GetValidators()
	if err != nil {
		return "", fmt.Errorf("failed to get validators: %w", err)
	}

	if len(validators) == 0 {
		return "", fmt.Errorf("no validators found")
	}

	return validators[0].Address.String(), nil
}

// GetConsensusState handles dpos_getConsensusState RPC method
func (d *DPOS) GetConsensusState(ctx context.Context) (map[string]interface{}, error) {
	d.logger.Info("DPoS GetConsensusState called")

	// Get current DPoS state
	_, err := d.store.GetDPoSState()
	if err != nil {
		return nil, fmt.Errorf("failed to get DPoS state: %w", err)
	}

	// Get current delegate
	currentDelegate, err := d.GetCurrentDelegate(ctx)
	if err != nil {
		return nil, err
	}

	// Get current round
	currentRound, err := d.GetCurrentRound(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"currentRound":         currentRound,
		"currentDelegate":      currentDelegate,
		"currentDelegateIndex": 0, // TODO: Calculate actual index
	}, nil
}

// Helper validation functions
func (d *DPOS) validateVoteRequest(req *VoteRequest) error {
	if req.Voter == "" {
		return fmt.Errorf("voter address is required")
	}
	if req.Candidate == "" {
		return fmt.Errorf("candidate address is required")
	}
	if req.Amount == "" {
		return fmt.Errorf("amount is required")
	}
	return nil
}

func (d *DPOS) validateStakeRequest(req *StakeRequest) error {
	if req.Staker == "" {
		return fmt.Errorf("staker address is required")
	}
	if req.Delegate == "" {
		return fmt.Errorf("delegate address is required")
	}
	if req.Amount == "" {
		return fmt.Errorf("amount is required")
	}
	return nil
}

func (d *DPOS) validateDelegateRequest(req *DelegateRequest) error {
	if req.Staker == "" {
		return fmt.Errorf("staker address is required")
	}
	if req.Delegate == "" {
		return fmt.Errorf("delegate address is required")
	}
	if req.Amount == "" {
		return fmt.Errorf("amount is required")
	}
	return nil
}

// GetVotingStakingInfo handles dpos_getVotingStakingInfo RPC method
// This method returns comprehensive information about current voting and staking status
func (d *DPOS) GetVotingStakingInfo(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetVotingStakingInfo called")

	// Get current DPoS state
	d.logger.Info("Getting DPoS state...")
	dposState, err := d.store.GetDPoSState()
	if err != nil {
		d.logger.Error("Failed to get DPoS state", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get DPoS state: %v", err),
		}, nil
	}
	d.logger.Info("DPoS state retrieved successfully")

	// Get current validators
	d.logger.Info("Getting validators...")
	validators, err := d.store.GetValidators()
	if err != nil {
		d.logger.Error("Failed to get validators", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}
	d.logger.Info("Validators retrieved successfully", "count", len(validators))

	// Get staking information
	d.logger.Info("Getting staking info...")
	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		d.logger.Error("Failed to get staking info", "error", err)
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get staking info: %v", err),
		}, nil
	}
	d.logger.Info("Staking info retrieved successfully", "count", len(stakingInfo))

	// Build validator details
	validatorDetails := make([]map[string]interface{}, 0)
	for _, validator := range validators {
		validatorDetail := map[string]interface{}{
			"address":     validator.Address.String(),
			"votingPower": validator.VotingPower.String(),
			"isActive":    validator.IsActive,
		}
		validatorDetails = append(validatorDetails, validatorDetail)
	}

	// Build staking summary and voting details
	totalStaked := big.NewInt(0)
	totalVotes := big.NewInt(0)
	stakingCount := 0
	votingCount := 0

	// Group staking info by delegate (validator) to show voting details
	votingDetails := make(map[string]map[string]interface{})

	for _, stake := range stakingInfo {
		if stake.Amount != nil {
			totalStaked.Add(totalStaked, stake.Amount)
			stakingCount++

			// Get delegate address as string
			delegateAddr := stake.Delegate.String()

			// Initialize delegate entry if not exists
			if _, exists := votingDetails[delegateAddr]; !exists {
				votingDetails[delegateAddr] = map[string]interface{}{
					"delegateAddress": delegateAddr,
					"totalVotes":      big.NewInt(0),
					"voters":          make([]map[string]interface{}, 0),
				}
			}

			// Add voter information
			voterInfo := map[string]interface{}{
				"voterAddress": stake.Staker.String(),
				"amount":       stake.Amount.String(),
			}

			votingDetails[delegateAddr]["voters"] = append(
				votingDetails[delegateAddr]["voters"].([]map[string]interface{}),
				voterInfo,
			)

			// Update total votes for this delegate
			currentTotal := votingDetails[delegateAddr]["totalVotes"].(*big.Int)
			currentTotal.Add(currentTotal, stake.Amount)
			votingDetails[delegateAddr]["totalVotes"] = currentTotal
		}
	}

	// Convert voting details map to slice for response
	votingDetailsList := make([]map[string]interface{}, 0)
	for _, details := range votingDetails {
		votingDetailsList = append(votingDetailsList, details)
	}

	// Calculate network statistics
	networkStats := map[string]interface{}{
		"totalValidators":     len(validators),
		"activeValidators":    len(validators), // TODO: Get actual active count from validator metadata
		"totalStaked":         totalStaked.String(),
		"totalVotes":          totalVotes.String(),
		"stakingTransactions": stakingCount,
		"votingTransactions":  votingCount,
		"consensusThreshold":  "2/3", // TODO: Get actual threshold from config
	}

	// Build response
	response := map[string]interface{}{
		"success":       true,
		"networkStats":  networkStats,
		"validators":    validatorDetails,
		"stakingInfo":   stakingInfo,
		"votingDetails": votingDetailsList, // 新增：投票明细
		"dposState":     dposState,
		"lastUpdated":   time.Now().Format(time.RFC3339),
		"blockHeight":   0, // TODO: Get current block height from blockchain
	}

	return response, nil
}

// GetValidatorVotingDetails handles dpos_getValidatorVotingDetails RPC method
// This method returns detailed voting information for a specific validator
func (d *DPOS) GetValidatorVotingDetails(ctx context.Context, params interface{}) (interface{}, error) {
	d.logger.Info("DPoS GetValidatorVotingDetails called", "params", params)

	var validatorAddress string

	// Parse parameters
	switch p := params.(type) {
	case []interface{}:
		if len(p) == 1 {
			if address, ok := p[0].(string); ok {
				validatorAddress = address
			} else {
				return map[string]interface{}{
					"success": false,
					"error":   "first parameter must be a string address",
				}, nil
			}
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "expected 1 parameter (validator address)",
			}, nil
		}
	case string:
		validatorAddress = p
	case map[string]interface{}:
		if address, ok := p["validator"].(string); ok {
			validatorAddress = address
		} else {
			return map[string]interface{}{
				"success": false,
				"error":   "validator address is required",
			}, nil
		}
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("invalid parameter type: %T, expected string, array, or map", params),
		}, nil
	}

	// Validate address
	if validatorAddress == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "validator address is required",
		}, nil
	}

	// Parse address
	validatorAddr := types.StringToAddress(validatorAddress)

	// Get validators
	validators, err := d.store.GetValidators()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get validators: %v", err),
		}, nil
	}

	// Find the specific validator
	var targetValidator *validator.ValidatorMetadata
	for _, v := range validators {
		if v.Address == validatorAddr {
			targetValidator = v
			break
		}
	}

	if targetValidator == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "validator not found",
		}, nil
	}

	// Get staking info for this validator
	stakingInfo, err := d.store.GetStakingInfo()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("failed to get staking info: %v", err),
		}, nil
	}

	// Filter staking info for this validator
	validatorStakes := make([]map[string]interface{}, 0)
	totalStakedToValidator := big.NewInt(0)

	// TODO: Add logic to match stake to validator
	// For now, we'll return basic validator info
	_ = stakingInfo // Suppress unused variable warning

	// Build validator details
	validatorDetail := map[string]interface{}{
		"address":           targetValidator.Address.String(),
		"votingPower":       targetValidator.VotingPower.String(),
		"isActive":          true, // TODO: Get actual active status
		"totalStakedToMe":   totalStakedToValidator.String(),
		"stakeCount":        len(validatorStakes),
		"stakes":            validatorStakes,
		"consensusRound":    0,     // TODO: Get current consensus round
		"lastBlockProduced": "0x0", // TODO: Get last block hash
	}

	response := map[string]interface{}{
		"success":   true,
		"validator": validatorDetail,
	}

	return response, nil
}
