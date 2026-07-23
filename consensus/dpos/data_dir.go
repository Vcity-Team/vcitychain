package dpos

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/hashicorp/go-hclog"
)

const (
	dposDirName     = "dpos"
	dposDBFileName  = "dpos.db"
	dposRewardsName = "dpos.db.rewards"
)

// ResolveResult is the outcome of ResolveDPoSDataDir.
type ResolveResult struct {
	// DataDir is the directory that holds dpos.db / dpos.db.rewards (e.g. node1/dpos).
	DataDir string
	// Migrated is true when legacy consensus/dpos was copied to the new location.
	Migrated bool
	// UsedLegacyFallback is true when migration failed and the legacy path is still used.
	UsedLegacyFallback bool
}

// NewDPoSDataDir returns the canonical DPoS state directory: <nodeDataDir>/dpos.
func NewDPoSDataDir(nodeDataDir string) string {
	return filepath.Join(nodeDataDir, dposDirName)
}

// LegacyDPoSDataDir returns the pre-migration path: <nodeDataDir>/consensus/dpos.
func LegacyDPoSDataDir(nodeDataDir string) string {
	return filepath.Join(nodeDataDir, "consensus", dposDirName)
}

// ResolveNodeDataDir derives the node root from consensus.Config.
// Prefer Config.DataDir; fall back to parent of Path when Path ends with "consensus" or "dpos".
func ResolveNodeDataDir(cfg *consensus.Config) string {
	if cfg == nil {
		return ""
	}
	if dir := cfg.DataDir; dir != "" {
		return dir
	}
	if cfg.Path == "" {
		return ""
	}
	base := filepath.Base(cfg.Path)
	if base == "consensus" || base == dposDirName {
		return filepath.Dir(cfg.Path)
	}
	return cfg.Path
}

// NodeDataDirFromDPoSDataDir derives node root from a DPoS data directory
// (supports both node1/dpos and legacy node1/consensus/dpos).
func NodeDataDirFromDPoSDataDir(dposDataDir string) string {
	if dposDataDir == "" {
		return ""
	}
	if filepath.Base(dposDataDir) != dposDirName {
		return dposDataDir
	}
	parent := filepath.Dir(dposDataDir)
	if filepath.Base(parent) == "consensus" {
		return filepath.Dir(parent)
	}
	return parent
}

// ValidatorBLSKeyPath returns <nodeDataDir>/consensus/validator-bls.key.
func ValidatorBLSKeyPath(nodeDataDir string) string {
	if nodeDataDir == "" {
		return ""
	}
	return filepath.Join(nodeDataDir, "consensus", "validator-bls.key")
}

// DPoSDBPath returns <dposDataDir>/dpos.db.
func DPoSDBPath(dposDataDir string) string {
	return filepath.Join(dposDataDir, dposDBFileName)
}

// DPoSRewardsDBPath returns <dposDataDir>/dpos.db.rewards.
func DPoSRewardsDBPath(dposDataDir string) string {
	return filepath.Join(dposDataDir, dposRewardsName)
}

// ResolveDPoSDBPathForNode picks an existing DPoS DB under the node root
// (new path first, then legacy). Used by offline tools (rollback, heal).
func ResolveDPoSDBPathForNode(nodeDataDir string) (dbPath string, dposDataDir string, ok bool) {
	if nodeDataDir == "" {
		return "", "", false
	}
	candidates := []string{
		NewDPoSDataDir(nodeDataDir),
		LegacyDPoSDataDir(nodeDataDir),
	}
	for _, dir := range candidates {
		p := DPoSDBPath(dir)
		if fileExists(p) {
			return p, dir, true
		}
	}
	return "", "", false
}

// ResolveDPoSDataDir selects the DPoS state directory and migrates from the
// legacy consensus/dpos path when needed (mainnet-safe: copy then rename backup).
func ResolveDPoSDataDir(nodeDataDir string, logger hclog.Logger) (ResolveResult, error) {
	out := ResolveResult{}
	if nodeDataDir == "" {
		return out, fmt.Errorf("empty node data dir")
	}
	if logger == nil {
		logger = hclog.NewNullLogger()
	}

	newDir := NewDPoSDataDir(nodeDataDir)
	oldDir := LegacyDPoSDataDir(nodeDataDir)
	newDB := DPoSDBPath(newDir)
	oldDB := DPoSDBPath(oldDir)

	switch {
	case fileExists(newDB):
		out.DataDir = newDir
		if fileExists(oldDB) {
			logger.Warn("DPoS data found at both new and legacy paths; using new path",
				"new", newDir,
				"legacy", oldDir,
				"hint", "after verification you may remove the legacy directory")
		} else {
			logger.Info("DPoS data directory", "path", newDir)
		}
		return out, nil

	case fileExists(oldDB):
		logger.Info("migrating DPoS data from legacy path",
			"from", oldDir,
			"to", newDir)
		if err := migrateDPoSDataDir(oldDir, newDir, logger); err != nil {
			logger.Error("DPoS data migration failed; falling back to legacy path",
				"from", oldDir,
				"to", newDir,
				"error", err)
			// Best-effort cleanup of a partial new dir so we do not open a corrupt copy.
			_ = os.RemoveAll(newDir)
			out.DataDir = oldDir
			out.UsedLegacyFallback = true
			return out, nil
		}
		out.DataDir = newDir
		out.Migrated = true
		logger.Info("DPoS data migration completed",
			"path", newDir,
			"legacyBackup", "consensus/dpos.migrated-backup-*")
		return out, nil

	default:
		if err := os.MkdirAll(newDir, 0755); err != nil {
			return out, fmt.Errorf("create DPoS data dir %s: %w", newDir, err)
		}
		out.DataDir = newDir
		logger.Info("DPoS data directory initialized (empty)", "path", newDir)
		return out, nil
	}
}

func migrateDPoSDataDir(oldDir, newDir string, logger hclog.Logger) error {
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return fmt.Errorf("mkdir new dpos dir: %w", err)
	}

	names := []string{dposDBFileName, dposRewardsName}
	copiedAny := false
	for _, name := range names {
		src := filepath.Join(oldDir, name)
		dst := filepath.Join(newDir, name)
		if !fileExists(src) {
			continue
		}
		if fileExists(dst) {
			if err := verifySameSize(src, dst); err != nil {
				return err
			}
			copiedAny = true
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
		if err := verifySameSize(src, dst); err != nil {
			return err
		}
		copiedAny = true
		logger.Info("copied DPoS file", "file", name, "from", src, "to", dst)
	}

	if !copiedAny || !fileExists(DPoSDBPath(newDir)) {
		return fmt.Errorf("dpos.db missing after copy (old=%s new=%s)", oldDir, newDir)
	}

	backup := filepath.Join(filepath.Dir(oldDir), fmt.Sprintf("dpos.migrated-backup-%d", time.Now().Unix()))
	if err := os.Rename(oldDir, backup); err != nil {
		// Non-fatal: new path is ready; leave legacy in place for manual cleanup.
		logger.Warn("DPoS migrate: copy OK but failed to rename legacy dir; leave it for manual cleanup",
			"legacy", oldDir,
			"backupAttempt", backup,
			"error", err)
		return nil
	}
	logger.Info("legacy DPoS directory renamed to backup", "backup", backup)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func verifySameSize(a, b string) error {
	ai, err := os.Stat(a)
	if err != nil {
		return err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return err
	}
	if ai.Size() != bi.Size() {
		return fmt.Errorf("size mismatch %s (%d) vs %s (%d)", a, ai.Size(), b, bi.Size())
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
