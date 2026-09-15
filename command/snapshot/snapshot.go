package snapshot

import (
	"fmt"
	"strconv"
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
	snapshotCmd.Flags().StringArrayVar(
		&params.urls,
		urlFlag,
		[]string{},
		"snapshot archive download URL; may be repeated to provide mirrors",
	)
	snapshotCmd.Flags().StringVar(
		&params.height,
		heightFlag,
		"",
		"latest block height recorded in the snapshot metadata",
	)
	snapshotCmd.Flags().StringVar(
		&params.hash,
		hashFlag,
		"",
		"latest block hash recorded in the snapshot metadata",
	)
	snapshotCmd.Flags().StringVar(
		&params.keyFile,
		keyFileFlag,
		"",
		"Ed25519 key file used for snapshot signing",
	)
	snapshotCmd.Flags().StringVar(
		&params.pubkey,
		pubkeyFlag,
		"",
		"Ed25519 public key (hex) used to verify a snapshot signature",
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
	if args[0] == "fetch" {
		return params.validateFetchFlags()
	}
	if args[0] == "verify" || args[0] == "info" {
		return params.validateSnapshotFileFlag()
	}
	if args[0] == "keygen" {
		return params.validateKeygenFlags()
	}
	if args[0] == "sign" {
		return params.validateSignFlags()
	}
	return params.validateRestoreFlags()
}

func runCommand(cmd *cobra.Command, args []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	var err error
	var resultPath string
	var resultFields []string
	switch args[0] {
	case "create":
		metadata := &archive.SnapshotMetadata{LatestHash: params.hash}
		if params.height != "" {
			metadata.LatestBlock, err = strconv.ParseUint(params.height, 10, 64)
			if err != nil {
				outputter.SetError(fmt.Errorf("invalid height: %w", err))
				return
			}
		}
		err = archive.CreateSnapshotWithMetadata(params.dataDir, params.out, metadata)
		resultPath = params.out
	case "fetch":
		err = fetchSnapshot(cmd.Context(), params.urls, params.out)
		resultPath = params.out
	case "restore":
		err = archive.RestoreSnapshotIfEmpty(params.snapshot, params.dataDir)
		resultPath = params.dataDir
	case "verify":
		err = archive.VerifySnapshot(params.snapshot)
		resultPath = params.snapshot
		resultFields = []string{"Verified|true"}
	case "info":
		var info *archive.SnapshotInfo
		info, err = archive.InspectSnapshot(params.snapshot)
		if info != nil {
			resultPath = info.Path
			resultFields = []string{
				fmt.Sprintf("Size|%d", info.Size),
				fmt.Sprintf("Checksum|%s", info.Checksum),
			}
			if info.Metadata != nil {
				resultFields = append(resultFields,
					fmt.Sprintf("CreatedAt|%s", info.Metadata.CreatedAt.Format("2006-01-02T15:04:05Z07:00")),
					fmt.Sprintf("LatestBlock|%d", info.Metadata.LatestBlock),
					fmt.Sprintf("LatestHash|%s", info.Metadata.LatestHash),
				)
			}
		}
	case "keygen":
		var publicKey string
		publicKey, err = archive.GenerateSnapshotKeypair(params.keyFile)
		resultPath = params.keyFile
		if publicKey != "" {
			resultFields = []string{fmt.Sprintf("PublicKey|%s", publicKey)}
		}
	case "sign":
		err = archive.SignSnapshot(params.snapshot, params.keyFile)
		resultPath = params.snapshot
	}
	if err == nil && args[0] == "verify" && params.pubkey != "" {
		err = archive.VerifySnapshotSignature(params.snapshot, params.pubkey)
		if err == nil {
			resultFields = append(resultFields, "Signature|verified")
		}
	}
	if err != nil {
		outputter.SetError(err)
		return
	}

	outputter.SetCommandResult(&snapshotResult{
		action: args[0],
		path:   resultPath,
		fields: resultFields,
	})
}

type snapshotResult struct {
	action string
	path   string
	fields []string
}

func (r *snapshotResult) GetOutput() string {
	output := fmt.Sprintf("\n[%s SNAPSHOT]\nPath|%s\n", strings.ToUpper(r.action), r.path)
	for _, field := range r.fields {
		output += field + "\n"
	}
	return output
}
