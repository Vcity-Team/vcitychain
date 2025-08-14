package validator_info

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
)

// validatorInfoParams holds the parameters for the validator info command
type validatorInfoParams struct {
	dataDir string
	jsonRPC string
	chainID uint64
}

// validateFlags validates the command flags
func (v *validatorInfoParams) validateFlags() error {
	if _, err := helper.ParseJSONRPCAddress(v.jsonRPC); err != nil {
		return fmt.Errorf("failed to parse json rpc address. Error: %w", err)
	}

	if v.chainID == 0 {
		return fmt.Errorf("chain ID is required")
	}

	return nil
}

// ValidatorInfoResult represents the result of validator info query
type ValidatorInfoResult struct {
	ChainID         uint64         `json:"chainId"`
	BlockHeight     uint64         `json:"blockHeight"`
	CurrentRound    uint64         `json:"currentRound"`
	CurrentDelegate string         `json:"currentDelegate"`
	Delegates       []DelegateInfo `json:"delegates"`
	StakingInfo     []StakeInfo    `json:"stakingInfo"`
	TotalStake      string         `json:"totalStake"`
	ActiveDelegates uint64         `json:"activeDelegates"`
}

// DelegateInfo represents information about a delegate/validator
type DelegateInfo struct {
	Address     string `json:"address"`
	VotingPower string `json:"votingPower"`
	IsActive    bool   `json:"isActive"`
	Index       uint64 `json:"index"`
	StakeAmount string `json:"stakeAmount"`
}

// StakeInfo represents information about a staker
type StakeInfo struct {
	Staker    string `json:"staker"`
	Amount    string `json:"amount"`
	StartTime uint64 `json:"startTime"`
	EndTime   uint64 `json:"endTime"`
	IsLocked  bool   `json:"isLocked"`
	IsActive  bool   `json:"isActive"`
	Delegate  string `json:"delegate"`
	Rewards   string `json:"rewards"`
}

// GetOutput formats the result for CLI output
func (vr ValidatorInfoResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[DPoS VALIDATOR INFO]\n")

	// Basic chain information
	vals := make([]string, 0)
	vals = append(vals, fmt.Sprintf("Chain ID|%d", vr.ChainID))
	vals = append(vals, fmt.Sprintf("Block Height|%d", vr.BlockHeight))
	vals = append(vals, fmt.Sprintf("Current Round|%d", vr.CurrentRound))
	vals = append(vals, fmt.Sprintf("Current Delegate|%s", vr.CurrentDelegate))
	vals = append(vals, fmt.Sprintf("Total Stake|%s", vr.TotalStake))
	vals = append(vals, fmt.Sprintf("Active Delegates|%d", vr.ActiveDelegates))

	buffer.WriteString(helper.FormatKV(vals))
	buffer.WriteString("\n")

	// Delegates information
	if len(vr.Delegates) > 0 {
		buffer.WriteString("\n[DELEGATES]\n")
		for i, delegate := range vr.Delegates {
			buffer.WriteString(fmt.Sprintf("\nDelegate %d:\n", i+1))
			delegateVals := []string{
				fmt.Sprintf("Address|%s", delegate.Address),
				fmt.Sprintf("Voting Power|%s", delegate.VotingPower),
				fmt.Sprintf("Stake Amount|%s", delegate.StakeAmount),
				fmt.Sprintf("Is Active|%v", delegate.IsActive),
				fmt.Sprintf("Index|%d", delegate.Index),
			}
			buffer.WriteString(helper.FormatKV(delegateVals))
		}
	}

	// Staking information
	if len(vr.StakingInfo) > 0 {
		buffer.WriteString("\n[STAKING INFO]\n")
		for i, stake := range vr.StakingInfo {
			buffer.WriteString(fmt.Sprintf("\nStaker %d:\n", i+1))
			stakeVals := []string{
				fmt.Sprintf("Staker Address|%s", stake.Staker),
				fmt.Sprintf("Amount|%s", stake.Amount),
				fmt.Sprintf("Delegate|%s", stake.Delegate),
				fmt.Sprintf("Start Time|%d", stake.StartTime),
				fmt.Sprintf("End Time|%d", stake.EndTime),
				fmt.Sprintf("Is Locked|%v", stake.IsLocked),
				fmt.Sprintf("Is Active|%v", stake.IsActive),
				fmt.Sprintf("Rewards|%s", stake.Rewards),
			}
			buffer.WriteString(helper.FormatKV(stakeVals))
		}
	}

	buffer.WriteString("\n")

	return buffer.String()
}

// convertValidatorToDelegateInfo converts validator.ValidatorMetadata to DelegateInfo
func convertValidatorToDelegateInfo(v *validator.ValidatorMetadata, index uint64) DelegateInfo {
	return DelegateInfo{
		Address:     v.Address.String(),
		VotingPower: v.VotingPower.String(),
		IsActive:    v.IsActive,
		Index:       index,
		StakeAmount: v.VotingPower.String(), // In DPoS, voting power equals stake amount
	}
}

// convertStakeInfo converts dpos.StakeInfo to StakeInfo
func convertStakeInfo(s *dpos.StakeInfo) StakeInfo {
	return StakeInfo{
		Staker:    s.Staker.String(),
		Amount:    s.Amount.String(),
		StartTime: s.StartTime,
		EndTime:   s.EndTime,
		IsLocked:  s.IsLocked,
		IsActive:  s.IsActive,
		Delegate:  s.Delegate.String(),
		Rewards:   s.Rewards.String(),
	}
}

// calculateTotalStake calculates total stake from delegates
func calculateTotalStake(delegates validator.AccountSet) *big.Int {
	total := big.NewInt(0)
	for _, delegate := range delegates {
		total.Add(total, delegate.VotingPower)
	}
	return total
}
