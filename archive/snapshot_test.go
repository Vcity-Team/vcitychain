package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))
	writeFile(t, filepath.Join(src, "nested", "b.bin"), []byte{0x00, 0x01, 0x02})

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	if err := VerifySnapshot(snapshot); err != nil {
		t.Fatalf("VerifySnapshot failed: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "restored")
	if err := RestoreSnapshot(snapshot, restored); err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}

	if got := string(readFile(t, filepath.Join(restored, "a.txt"))); got != "alpha" {
		t.Fatalf("restored a.txt = %q, want %q", got, "alpha")
	}
	if got := readFile(t, filepath.Join(restored, "nested", "b.bin")); !bytes.Equal(got, []byte{0x00, 0x01, 0x02}) {
		t.Fatalf("restored b.bin = %v, want [0 1 2]", got)
	}
}

func TestSnapshotEmptyDir(t *testing.T) {
	src := t.TempDir()
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")

	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed for empty dir: %v", err)
	}
	if err := VerifySnapshot(snapshot); err != nil {
		t.Fatalf("VerifySnapshot failed: %v", err)
	}
	if err := RestoreSnapshot(snapshot, filepath.Join(t.TempDir(), "restored")); err != nil {
		t.Fatalf("RestoreSnapshot failed for empty snapshot: %v", err)
	}
}

// TestSnapshotRealisticDataDir verifies the snapshot helpers against a
// node-like data directory layout instead of a flat temp dir.
func TestSnapshotRealisticDataDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "config.yaml"), []byte("chain_config: genesis.json\n"))
	writeFile(t, filepath.Join(src, "consensus", "validator.key"), []byte("0123456789abcdef"))
	writeFile(t, filepath.Join(src, "consensus", "validator-bls.key"), []byte("bls-private-key"))
	writeFile(t, filepath.Join(src, "libp2p", "libp2p.key"), []byte("libp2p-private-key"))
	writeFile(t, filepath.Join(src, "db", "blockchain.db"), []byte("blockchain-database-bytes"))

	snapshot := filepath.Join(t.TempDir(), "node-snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed for realistic datadir: %v", err)
	}
	if err := VerifySnapshot(snapshot); err != nil {
		t.Fatalf("VerifySnapshot failed for realistic datadir: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "restored-node")
	if err := RestoreSnapshot(snapshot, restored); err != nil {
		t.Fatalf("RestoreSnapshot failed for realistic datadir: %v", err)
	}

	checks := map[string]string{
		"config.yaml":                 "chain_config: genesis.json\n",
		"consensus/validator.key":     "0123456789abcdef",
		"consensus/validator-bls.key": "bls-private-key",
		"libp2p/libp2p.key":           "libp2p-private-key",
		"db/blockchain.db":            "blockchain-database-bytes",
	}
	for rel, want := range checks {
		if got := string(readFile(t, filepath.Join(restored, filepath.FromSlash(rel)))); got != want {
			t.Fatalf("restored %s = %q, want %q", rel, got, want)
		}
	}
}

func TestCreateSnapshotMissingSource(t *testing.T) {
	err := CreateSnapshot(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "s.tar.gz"))
	if err == nil {
		t.Fatal("CreateSnapshot should fail when source dir does not exist")
	}
}

func TestCreateSnapshotRejectsExistingOutput(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "unused"), []byte("unused")) // ensure dir is a valid source

	dir := t.TempDir()
	snapshot := filepath.Join(dir, "snapshot.tar.gz")
	writeFile(t, snapshot, []byte("already exists"))

	if err := CreateSnapshot(src, snapshot); err == nil {
		t.Fatal("CreateSnapshot should reject an existing output file")
	}
}

func TestVerifySnapshotMissingFile(t *testing.T) {
	if err := VerifySnapshot(filepath.Join(t.TempDir(), "missing.tar.gz")); err == nil {
		t.Fatal("VerifySnapshot should fail for missing snapshot file")
	}
}

func TestVerifySnapshotMissingChecksum(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "snapshot.tar.gz")
	writeFile(t, snapshot, []byte("data"))

	if err := VerifySnapshot(snapshot); err == nil {
		t.Fatal("VerifySnapshot should fail when checksum file is missing")
	}
}

func TestVerifySnapshotTampered(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	if err := os.WriteFile(snapshot, append([]byte("tampered"), readFile(t, snapshot)...), 0644); err != nil {
		t.Fatalf("failed to tamper snapshot: %v", err)
	}
	if err := VerifySnapshot(snapshot); err == nil {
		t.Fatal("VerifySnapshot should fail for tampered snapshot")
	}
}

func TestVerifySnapshotRejectsWrongName(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))

	dir := t.TempDir()
	snapshot := filepath.Join(dir, "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	checksumFile := snapshot + ".sha256"
	data := readFile(t, checksumFile)
	if err := os.WriteFile(checksumFile, bytes.Replace(data, []byte("snapshot.tar.gz"), []byte("other.tar.gz"), 1), 0644); err != nil {
		t.Fatalf("failed to alter checksum name: %v", err)
	}
	if err := VerifySnapshot(snapshot); err == nil {
		t.Fatal("VerifySnapshot should fail when checksum references another file")
	}
}

func TestRestoreSnapshotMissingFile(t *testing.T) {
	if err := RestoreSnapshot(filepath.Join(t.TempDir(), "missing.tar.gz"), t.TempDir()); err == nil {
		t.Fatal("RestoreSnapshot should fail for missing snapshot file")
	}
}

func TestRestoreSnapshotCreatesDestination(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "does-not-exist", "restored")
	if err := RestoreSnapshot(snapshot, restored); err != nil {
		t.Fatalf("RestoreSnapshot should create destination dir: %v", err)
	}
	if got := string(readFile(t, filepath.Join(restored, "a.txt"))); got != "alpha" {
		t.Fatalf("restored a.txt = %q, want %q", got, "alpha")
	}
}

func TestRestoreSnapshotRejectsUnsafePath(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "malicious.tar.gz")
	writeTarEntry(t, snapshot, "../escape.txt", []byte("bad"))

	if err := RestoreSnapshot(snapshot, filepath.Join(dir, "restored")); err == nil {
		t.Fatal("RestoreSnapshot should reject unsafe paths")
	}
}

func TestRestoreSnapshotIfEmptyEmptyDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	dataDir := filepath.Join(t.TempDir(), "empty-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("failed to create empty data dir: %v", err)
	}
	if err := RestoreSnapshotIfEmpty(snapshot, dataDir); err != nil {
		t.Fatalf("RestoreSnapshotIfEmpty failed for empty data dir: %v", err)
	}
	if got := string(readFile(t, filepath.Join(dataDir, "a.txt"))); got != "alpha" {
		t.Fatalf("restored a.txt = %q, want %q", got, "alpha")
	}
}

func TestRestoreSnapshotIfEmptyMissingDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	dataDir := filepath.Join(t.TempDir(), "missing-data")
	if err := RestoreSnapshotIfEmpty(snapshot, dataDir); err != nil {
		t.Fatalf("RestoreSnapshotIfEmpty failed for missing data dir: %v", err)
	}
	if got := string(readFile(t, filepath.Join(dataDir, "a.txt"))); got != "alpha" {
		t.Fatalf("restored a.txt = %q, want %q", got, "alpha")
	}
}

func TestRestoreSnapshotIfEmptyNonEmptyDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), []byte("alpha"))
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	dataDir := t.TempDir()
	writeFile(t, filepath.Join(dataDir, "existing"), []byte("keep"))
	if err := RestoreSnapshotIfEmpty(snapshot, dataDir); err == nil {
		t.Fatal("RestoreSnapshotIfEmpty should refuse non-empty data dir")
	}
	if got := string(readFile(t, filepath.Join(dataDir, "existing"))); got != "keep" {
		t.Fatalf("existing file was modified: %q", got)
	}
}

func TestRestoreSnapshotIfEmptyMissingSnapshot(t *testing.T) {
	if err := RestoreSnapshotIfEmpty(filepath.Join(t.TempDir(), "missing.tar.gz"), t.TempDir()); err == nil {
		t.Fatal("RestoreSnapshotIfEmpty should fail for missing snapshot")
	}
}

func writeTarEntry(t *testing.T, snapshot, name string, data []byte) {
	t.Helper()

	f, err := os.Create(snapshot)
	if err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("failed to write tar data: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip: %v", err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return data
}
