//go:build windows

package archive

// ensureDataDirNotLocked is a no-op on Windows; LevelDB lock detection there
// needs a different mechanism, so snapshots must be taken with the node stopped.
func ensureDataDirNotLocked(string) error {
	return nil
}
