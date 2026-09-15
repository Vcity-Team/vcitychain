package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotSignatureRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "chain.db"), []byte("blocks"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	keyFile := filepath.Join(t.TempDir(), "snapshot.key")
	publicKey, err := GenerateSnapshotKeypair(keyFile)
	if err != nil {
		t.Fatalf("GenerateSnapshotKeypair failed: %v", err)
	}
	if err := SignSnapshot(snapshot, keyFile); err != nil {
		t.Fatalf("SignSnapshot failed: %v", err)
	}
	if err := VerifySnapshotSignature(snapshot, publicKey); err != nil {
		t.Fatalf("VerifySnapshotSignature failed: %v", err)
	}
}

func TestSnapshotSignatureDetectsTamper(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "chain.db"), []byte("blocks"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	keyFile := filepath.Join(t.TempDir(), "snapshot.key")
	publicKey, err := GenerateSnapshotKeypair(keyFile)
	if err != nil {
		t.Fatalf("GenerateSnapshotKeypair failed: %v", err)
	}
	if err := SignSnapshot(snapshot, keyFile); err != nil {
		t.Fatalf("SignSnapshot failed: %v", err)
	}

	if err := os.WriteFile(snapshot, append(readFile(t, snapshot), 'x'), 0644); err != nil {
		t.Fatalf("failed to tamper snapshot: %v", err)
	}
	if err := VerifySnapshotSignature(snapshot, publicKey); err == nil {
		t.Fatal("tampered snapshot must fail signature verification")
	}
}

func TestSnapshotSignatureRejectsWrongKey(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "chain.db"), []byte("blocks"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	keyFile := filepath.Join(t.TempDir(), "snapshot.key")
	if _, err := GenerateSnapshotKeypair(keyFile); err != nil {
		t.Fatalf("GenerateSnapshotKeypair failed: %v", err)
	}
	if err := SignSnapshot(snapshot, keyFile); err != nil {
		t.Fatalf("SignSnapshot failed: %v", err)
	}

	otherKeyFile := filepath.Join(t.TempDir(), "other.key")
	otherPublicKey, err := GenerateSnapshotKeypair(otherKeyFile)
	if err != nil {
		t.Fatalf("GenerateSnapshotKeypair failed: %v", err)
	}
	if err := VerifySnapshotSignature(snapshot, otherPublicKey); err == nil {
		t.Fatal("signature verification must fail with a different public key")
	}
}

func TestSignSnapshotMissingKey(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "chain.db"), []byte("blocks"))

	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	if err := CreateSnapshot(src, snapshot); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	if err := SignSnapshot(snapshot, filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("SignSnapshot should fail when the key file is missing")
	}
}
