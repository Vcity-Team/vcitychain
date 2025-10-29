package dpos

import (
	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command/dpos/block_producers"
	"github.com/Vcity-Team/vcitychain/command/dpos/delegate"
	"github.com/Vcity-Team/vcitychain/command/dpos/epoch"
	getVoteByHash "github.com/Vcity-Team/vcitychain/command/dpos/get_vote_by_hash"
	"github.com/Vcity-Team/vcitychain/command/dpos/min_threshold"
	"github.com/Vcity-Team/vcitychain/command/dpos/parameters"
	"github.com/Vcity-Team/vcitychain/command/dpos/proposal"
	"github.com/Vcity-Team/vcitychain/command/dpos/rewards"
	"github.com/Vcity-Team/vcitychain/command/dpos/stats"
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
		vote.GetCommand(),
		voting_staking_info.GetCommand(),
		validator_voting_details.GetCommand(),
		getVoteByHash.GetCommand(),
		block_producers.GetCommand(),
		// Economic system commands
		epoch.GetCommand(),
		stats.GetCommand(),
		rewards.GetCommand(),
		// Governance commands
		parameters.GetCommand(),
		// current-params 已移到 proposal 子命令下 🆕
		proposal.GetCommand(),
		min_threshold.GetCommand(),
		// Delegate management commands
		delegate.GetCommand(),
		// Future DPoS commands can be added here:
		// - staking operations
		// - consensus status
	)

	return dposCmd
}
