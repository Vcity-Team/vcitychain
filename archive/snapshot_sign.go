package archive

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const snapshotSignatureSuffix = ".sig"

// GenerateSnapshotKeypair creates an Ed25519 keypair used to sign snapshots.
// The private key is written to keyFile as hex (0600), the public key to
// keyFile+".pub", and the public key hex is returned.
func GenerateSnapshotKeypair(keyFile string) (string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(privateKey)), 0600); err != nil {
		return "", err
	}

	publicKeyHex := hex.EncodeToString(publicKey)
	if err := os.WriteFile(keyFile+".pub", []byte(publicKeyHex+"\n"), 0644); err != nil {
		return "", err
	}

	return publicKeyHex, nil
}

// SignSnapshot signs the SHA-256 digest of snapshotFile with the Ed25519
// private key in keyFile and writes snapshotFile+".sig".
func SignSnapshot(snapshotFile, keyFile string) error {
	keyHex, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	privateKey, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil {
		return err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid snapshot signing key length %d", len(privateKey))
	}

	digest, err := sha256File(snapshotFile)
	if err != nil {
		return err
	}
	signature := ed25519.Sign(ed25519.PrivateKey(privateKey), digest[:])

	return os.WriteFile(snapshotFile+snapshotSignatureSuffix, []byte(hex.EncodeToString(signature)), 0644)
}

// VerifySnapshotSignature verifies snapshotFile+".sig" with publicKeyHex.
func VerifySnapshotSignature(snapshotFile, publicKeyHex string) error {
	signatureHex, err := os.ReadFile(snapshotFile + snapshotSignatureSuffix)
	if err != nil {
		return err
	}
	signature, err := hex.DecodeString(strings.TrimSpace(string(signatureHex)))
	if err != nil {
		return err
	}

	publicKey, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
	if err != nil {
		return err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid snapshot public key length %d", len(publicKey))
	}

	digest, err := sha256File(snapshotFile)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), digest[:], signature) {
		return errors.New("snapshot signature verification failed")
	}

	return nil
}
