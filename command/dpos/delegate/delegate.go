package delegate

import (
	"github.com/spf13/cobra"
)

// GetCommand returns the delegate command
func GetCommand() *cobra.Command {
	delegateCmd := &cobra.Command{
		Use:   "delegate",
		Short: "DPoS delegate management commands",
		Long:  "Commands for managing DPoS delegates and candidates",
	}

	// Add subcommands
	delegateCmd.AddCommand(
		GetRegisterCommand(),
		GetListCommand(),
		// Future delegate commands can be added here:
		// - withdraw delegate
		// - update delegate info
	)

	return delegateCmd
}
