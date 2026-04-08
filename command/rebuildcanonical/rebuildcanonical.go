package rebuildcanonical

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Vcity-Team/vcitychain/blockchain/storage"
	"github.com/Vcity-Team/vcitychain/blockchain/storage/leveldb"
	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
)

const (
	dataDirFlag   = "data-dir"
	toHeightFlag  = "to-height"
	forceFlag     = "force"
)

type params struct {
	dataDir      string
	toHeightRaw  string
	toHeight     uint64
	force        bool

	headNumber uint64
	headHash   types.Hash
}

var p = &params{}

func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebuild-canonical",
		Short: "Rebuild canonical height->hash index from current head backwards",
		Long:  "Rebuild the canonical height->hash mapping by walking backwards from the current head via parentHash and rewriting canonical entries. This fixes broken canonical indexes after reorgs without resyncing.",
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return p.validate()
		},
		Run: func(cmd *cobra.Command, _ []string) {
			outputter := command.InitializeOutputter(cmd)
			defer outputter.WriteOutput()

			written, err := p.execute()
			if err != nil {
				outputter.SetError(err)
				return
			}

			outputter.SetCommandResult(&Result{
				HeadNumber: p.headNumber,
				HeadHash:   p.headHash.String(),
				ToHeight:   p.toHeight,
				Written:    written,
			})
		},
	}

	cmd.Flags().StringVar(&p.dataDir, dataDirFlag, "", "the data directory of the node")
	cmd.Flags().StringVar(&p.toHeightRaw, toHeightFlag, "", "lowest block height to rebuild down to (inclusive). Can be decimal or 0x hex")
	cmd.Flags().BoolVar(&p.force, forceFlag, false, "skip confirmation prompt")

	helper.SetRequiredFlags(cmd, []string{dataDirFlag, toHeightFlag})

	return cmd
}

func (p *params) validate() error {
	if p.dataDir == "" {
		return fmt.Errorf("data-dir is required")
	}
	if p.toHeightRaw == "" {
		return fmt.Errorf("to-height is required")
	}

	var err error
	p.toHeight, err = common.ParseUint64orHex(&p.toHeightRaw)
	if err != nil {
		return fmt.Errorf("invalid to-height: %w", err)
	}

	return nil
}

func (p *params) execute() (int, error) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:  "rebuild-canonical",
		Level: hclog.LevelFromString("INFO"),
	})

	// best-effort: refuse to run while node likely running (same heuristic as rollback)
	lockFile := filepath.Join(p.dataDir, "blockchain", "LOCK")
	if _, err := os.Stat(lockFile); err == nil {
		return 0, fmt.Errorf("node appears to be running, please stop it first (lock file exists at %s)", lockFile)
	}

	blockchainPath := filepath.Join(p.dataDir, "blockchain")
	st, err := leveldb.NewLevelDBStorage(blockchainPath, logger)
	if err != nil {
		return 0, fmt.Errorf("failed to open blockchain storage: %w", err)
	}
	defer st.Close()

	headNumber, ok := st.ReadHeadNumber()
	if !ok {
		return 0, fmt.Errorf("failed to read head number")
	}
	headHash, ok := st.ReadHeadHash()
	if !ok {
		return 0, fmt.Errorf("failed to read head hash")
	}

	p.headNumber = headNumber
	p.headHash = headHash

	if p.toHeight > headNumber {
		return 0, fmt.Errorf("to-height %d is higher than head %d", p.toHeight, headNumber)
	}

	if !p.force {
		fmt.Printf("⚠️  This will rewrite canonical index entries from head=%d down to to-height=%d (inclusive).\n", headNumber, p.toHeight)
		fmt.Printf("Head hash: %s\n", headHash.String())
		fmt.Print("Proceed? (yes/no): ")
		var resp string
		_, _ = fmt.Scanln(&resp)
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "yes" && resp != "y" {
			return 0, fmt.Errorf("rebuild cancelled by user")
		}
	}

	bw := storage.NewBatchWriter(st)

	const batchEvery = 5000
	written := 0

	curHash := headHash
	curNum := headNumber
	for {
		// read header by hash
		hdr, err := st.ReadHeader(curHash)
		if err != nil {
			return 0, fmt.Errorf("failed to read header %s at height %d: %w", curHash.String(), curNum, err)
		}
		// sanity: header number must match the traversal number
		if hdr.Number != curNum {
			return 0, fmt.Errorf("header number mismatch while rebuilding canonical: expected %d got %d (hash %s)", curNum, hdr.Number, curHash.String())
		}

		bw.PutCanonicalHash(curNum, curHash)
		written++

		if written%batchEvery == 0 {
			if err := bw.WriteBatch(); err != nil {
				return 0, fmt.Errorf("failed to write batch: %w", err)
			}
			bw = storage.NewBatchWriter(st)
			logger.Info("progress", "written", written, "currentHeight", curNum, "currentHash", curHash.String()[:18])
		}

		if curNum == p.toHeight {
			break
		}
		if curNum == 0 {
			break
		}

		curHash = hdr.ParentHash
		curNum--
	}

	if err := bw.WriteBatch(); err != nil {
		return 0, fmt.Errorf("failed to write final batch: %w", err)
	}

	logger.Info("canonical index rebuild complete", "headNumber", headNumber, "toHeight", p.toHeight, "written", written)
	return written, nil
}

