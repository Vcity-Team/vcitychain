package rollback

import (
	"github.com/Vcity-Team/vcitychain/command"
	"github.com/spf13/cobra"

	"github.com/Vcity-Team/vcitychain/command/helper"
)

func GetCommand() *cobra.Command {
	rollbackCmd := &cobra.Command{
		Use:     "rollback",
		Short:   "Rollback blockchain to a specific block height",
		Long:    "Rollback blockchain to a specific block height. This will delete all blocks after the target height and set the target block as the new chain head.",
		PreRunE: runPreRun,
		Run:     runCommand,
	}

	setFlags(rollbackCmd)
	// 只设置 target-height 为必需，data-dir 可以通过 config 提供
	helper.SetRequiredFlags(rollbackCmd, []string{targetHeightFlag})

	return rollbackCmd
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.configPath,
		configFlag,
		"",
		"path to the node configuration file (yaml/json). If provided, data-dir will be read from config if not specified via --data-dir",
	)

	cmd.Flags().StringVar(
		&params.dataDir,
		dataDirFlag,
		"",
		"the data directory of the node (can be read from config file if --config is provided)",
	)

	cmd.Flags().StringVar(
		&params.targetHeightRaw,
		targetHeightFlag,
		"",
		"the target block height to rollback to (inclusive)",
	)

	cmd.Flags().BoolVar(
		&params.force,
		forceFlag,
		false,
		"skip confirmation prompt",
	)

	cmd.Flags().BoolVar(
		&params.keepBlocks,
		keepBlocksFlag,
		false,
		"keep block data, only update chain head (for debugging)",
	)
}

func runPreRun(_ *cobra.Command, _ []string) error {
	return params.validateFlags()
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	if err := params.executeRollback(); err != nil {
		outputter.SetError(err)
		return
	}

	outputter.SetCommandResult(params.getResult())
}


