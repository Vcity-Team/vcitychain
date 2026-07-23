package dpos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestResolveNodeDataDir(t *testing.T) {
	t.Parallel()

	require.Equal(t, filepath.Join("data", "node1"), ResolveNodeDataDir(&consensus.Config{
		DataDir: filepath.Join("data", "node1"),
		Path:    filepath.Join("data", "node1", "consensus"),
	}))
	require.Equal(t, filepath.Join("data", "node1"), ResolveNodeDataDir(&consensus.Config{
		Path: filepath.Join("data", "node1", "consensus"),
	}))
	require.Equal(t, filepath.Join("data", "node1"), ResolveNodeDataDir(&consensus.Config{
		Path: filepath.Join("data", "node1", "dpos"),
	}))
}

func TestNodeDataDirFromDPoSDataDir(t *testing.T) {
	t.Parallel()

	require.Equal(t, filepath.Join("node1"), NodeDataDirFromDPoSDataDir(filepath.Join("node1", "dpos")))
	require.Equal(t, filepath.Join("node1"), NodeDataDirFromDPoSDataDir(filepath.Join("node1", "consensus", "dpos")))
	require.Equal(t, filepath.Join("node1", "consensus", "validator-bls.key"),
		ValidatorBLSKeyPath(NodeDataDirFromDPoSDataDir(filepath.Join("node1", "dpos"))))
	require.Equal(t, filepath.Join("node1", "consensus", "validator-bls.key"),
		ValidatorBLSKeyPath(NodeDataDirFromDPoSDataDir(filepath.Join("node1", "consensus", "dpos"))))
}

func TestResolveDPoSDataDir_NewEmpty(t *testing.T) {
	root := t.TempDir()
	res, err := ResolveDPoSDataDir(root, hclog.NewNullLogger())
	require.NoError(t, err)
	require.Equal(t, NewDPoSDataDir(root), res.DataDir)
	require.False(t, res.Migrated)
	require.False(t, res.UsedLegacyFallback)
	require.DirExists(t, res.DataDir)
}

func TestResolveDPoSDataDir_MigrateFromLegacy(t *testing.T) {
	root := t.TempDir()
	legacy := LegacyDPoSDataDir(root)
	require.NoError(t, os.MkdirAll(legacy, 0755))
	require.NoError(t, os.WriteFile(DPoSDBPath(legacy), []byte("main-db-content"), 0666))
	require.NoError(t, os.WriteFile(DPoSRewardsDBPath(legacy), []byte("rewards-content"), 0666))

	res, err := ResolveDPoSDataDir(root, hclog.NewNullLogger())
	require.NoError(t, err)
	require.True(t, res.Migrated)
	require.False(t, res.UsedLegacyFallback)
	require.Equal(t, NewDPoSDataDir(root), res.DataDir)
	require.FileExists(t, DPoSDBPath(res.DataDir))
	require.FileExists(t, DPoSRewardsDBPath(res.DataDir))

	got, err := os.ReadFile(DPoSDBPath(res.DataDir))
	require.NoError(t, err)
	require.Equal(t, []byte("main-db-content"), got)

	// legacy renamed to backup
	require.NoFileExists(t, DPoSDBPath(legacy))
	entries, err := os.ReadDir(filepath.Join(root, "consensus"))
	require.NoError(t, err)
	foundBackup := false
	for _, e := range entries {
		if len(e.Name()) >= len("dpos.migrated-backup-") &&
			e.Name()[:len("dpos.migrated-backup-")] == "dpos.migrated-backup-" {
			foundBackup = true
		}
	}
	require.True(t, foundBackup, "expected migrated-backup directory")
}

func TestResolveDPoSDataDir_PreferNew(t *testing.T) {
	root := t.TempDir()
	newDir := NewDPoSDataDir(root)
	legacy := LegacyDPoSDataDir(root)
	require.NoError(t, os.MkdirAll(newDir, 0755))
	require.NoError(t, os.MkdirAll(legacy, 0755))
	require.NoError(t, os.WriteFile(DPoSDBPath(newDir), []byte("new"), 0666))
	require.NoError(t, os.WriteFile(DPoSDBPath(legacy), []byte("old"), 0666))

	res, err := ResolveDPoSDataDir(root, hclog.NewNullLogger())
	require.NoError(t, err)
	require.Equal(t, newDir, res.DataDir)
	require.False(t, res.Migrated)
	got, err := os.ReadFile(DPoSDBPath(res.DataDir))
	require.NoError(t, err)
	require.Equal(t, []byte("new"), got)
}

func TestResolveDPoSDBPathForNode(t *testing.T) {
	root := t.TempDir()
	legacy := LegacyDPoSDataDir(root)
	require.NoError(t, os.MkdirAll(legacy, 0755))
	require.NoError(t, os.WriteFile(DPoSDBPath(legacy), []byte("x"), 0666))

	db, dir, ok := ResolveDPoSDBPathForNode(root)
	require.True(t, ok)
	require.Equal(t, legacy, dir)
	require.Equal(t, DPoSDBPath(legacy), db)
}
