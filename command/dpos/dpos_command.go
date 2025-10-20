package dpos

import (
	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command/dpos/current_params"
	"github.com/Vcity-Team/vcitychain/command/dpos/epoch"
	getVoteByHash "github.com/Vcity-Team/vcitychain/command/dpos/get_vote_by_hash"
	"github.com/Vcity-Team/vcitychain/command/dpos/parameters"
	"github.com/Vcity-Team/vcitychain/command/dpos/rewards"
	"github.com/Vcity-Team/vcitychain/command/dpos/stats"
	"github.com/Vcity-Team/vcitychain/command/dpos/validator_info"
	"github.com/Vcity-Team/vcitychain/command/dpos/validator_voting_details"
	"github.com/Vcity-Team/vcitychain/command/dpos/vote"
	"github.com/Vcity-Team/vcitychain/command/dpos/voting_staking_info"
	"github.com/Vcity-Team/vcitychain/command/helper"
)

// GetCommand returns the dpos command
func GetCommand() *cobra.Command {
	dposCmd := &cobra.Command{
		Use:   "dpos",
		Short: "DPoS consensus commands",
		Long:  "Commands for interacting with DPoS consensus mechanism",
	}

	// Register JSON output flag
	helper.RegisterJSONOutputFlag(dposCmd)

	// Add subcommands
	dposCmd.AddCommand(
		validator_info.GetCommand(),
		vote.GetCommand(),
		voting_staking_info.GetCommand(),
		validator_voting_details.GetCommand(),
		getVoteByHash.GetCommand(),
		// Economic system commands
		epoch.GetCommand(),
		stats.GetCommand(),
		rewards.GetCommand(),
		// Governance commands
		parameters.GetCommand(),
		current_params.GetCommand(),
		// Future DPoS commands can be added here:
		// - delegate management
		// - staking operations
		// - consensus status
	)

	return dposCmd
}
