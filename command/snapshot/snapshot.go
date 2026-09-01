package snapshot

import (
	"fmt"
	"strings"

	"github.com/Vcity-Team/vcitychain/archive"
	"github.com/Vcity-Team/vcitychain/command"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:     "snapshot <create|restore>",
		Short:   "Create or restore a node data directory snapshot",
		Args:    cobra.ExactArgs(1),
		PreRunE: runPreRun,
		Run:     runCommand,
	}

	snapshotCmd.Flags().StringVar(
		&params.dataDir,
		dataDirFlag,
		"",
		"node data directory",
	)
	snapshotCmd.Flags().StringVar(
		&params.out,
		outFlag,
		"",
		"snapshot archive output path",
	)
	snapshotCmd.Flags().StringVar(
		&params.snapshot,
		snapshotFlag,
		"",
		"snapshot archive input path",
	)

	return snapshotCmd
}

func runPreRun(_ *cobra.Command, args []string) error {
	if err := validateAction(args[0]); err != nil {
		return err
	}
	if args[0] == "create" {
		return params.validateCreateFlags()
	}
	return params.validateRestoreFlags()
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	var err error
	switch args[0] {
	case "create":
		err = archive.CreateSnapshot(params.dataDir, params.out)
	case "restore":
		err = archive.RestoreSnapshot(params.snapshot, params.dataDir)
	}
	if err != nil {
		outputter.SetError(err)
		return
	}

	outputter.SetCommandResult(&snapshotResult{
		action:  args[0],
		dataDir: params.dataDir,
	})
}

type snapshotResult struct {
	action  string
	dataDir string
}

func (r *snapshotResult) GetOutput() string {
	return fmt.Sprintf("\n[%s SNAPSHOT]\nData Directory|%s\n", strings.ToUpper(r.action), r.dataDir)
}
