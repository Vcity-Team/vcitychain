//go:build !windows

package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ensureDataDirNotLocked refuses snapshot creation while a node holds a
// LevelDB lock, which would produce an inconsistent snapshot.
func ensureDataDirNotLocked(dataDir string) error {
	for _, rel := range []string{"blockchain/LOCK", "trie/LOCK"} {
		file, err := os.Open(filepath.Join(dataDir, rel))
		if err != nil {
			continue // no lock file means no running node is holding it
		}

		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		_ = file.Close()
		if lockErr == nil {
			continue
		}
		if errors.Is(lockErr, syscall.EWOULDBLOCK) {
			return fmt.Errorf("data dir %q is in use by a running node; stop it before creating a snapshot", dataDir)
		}

		return lockErr
	}

	return nil
}
