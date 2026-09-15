//go:build !windows

package archive

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCreateSnapshotRefusesLockedDataDir(t *testing.T) {
	src := t.TempDir()
	lockPath := filepath.Join(src, "blockchain", "LOCK")
	writeFile(t, lockPath, []byte{})

	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("failed to open lock file: %v", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("failed to lock file: %v", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err == nil {
		t.Fatal("CreateSnapshot should refuse a data dir locked by a running node")
	}
}

func TestCreateSnapshotAllowsUnlockedDataDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "blockchain", "LOCK"), []byte{})
	writeFile(t, filepath.Join(src, "blockchain", "chain.db"), []byte("data"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot should allow an unlocked data dir: %v", err)
	}
}
