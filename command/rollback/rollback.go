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
		Long: "Rollback blockchain to a specific block height. Updates chain head and canonical index, " +
			"optionally deletes block data, cleans DPoS metadata, and by default finalizes execution-layer state " +
			"(verify target stateRoot in trie; same path as sync HealCanonicalBlockState). " +
			"Use --replay-blocks only to re-execute and re-write blocks above the target (state drift fix); " +
			"it will re-apply those transactions and is not appropriate when removing unwanted txs.",
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

	cmd.Flags().BoolVar(
		&params.healState,
		healStateFlag,
		true,
		"after metadata rollback, verify target stateRoot in trie and align execution head (requires genesis config via --config or data-dir/genesis.json)",
	)

	cmd.Flags().BoolVar(
		&params.replayBlocks,
		replayBlocksFlag,
		false,
		"re-execute and write blocks from target+1 through pre-rollback head using HealCanonicalBlockState (collects blocks before deletion)",
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


