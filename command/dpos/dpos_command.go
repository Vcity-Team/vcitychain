package dpos

import (
	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command/dpos/validator_info"
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
		// Future DPoS commands can be added here:
		// - delegate management
		// - voting operations
		// - staking operations
		// - consensus status
	)

	return dposCmd
}

