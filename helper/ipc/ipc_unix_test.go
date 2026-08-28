//go:build !windows

package ipc

import (
	"path/filepath"
	"testing"
)

func TestListenMissingSocketFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")

	lis, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen should ignore a missing socket file: %v", err)
	}

	if err := lis.Close(); err != nil {
		t.Fatalf("failed to close listener: %v", err)
	}
}
