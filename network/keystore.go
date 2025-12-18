package network

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/libp2p/go-libp2p/core/crypto"
)

// ReadLibp2pKey reads the private networking key from the secrets manager
func ReadLibp2pKey(manager secrets.SecretsManager) (crypto.PrivKey, error) {
	libp2pKey, err := manager.GetSecret(secrets.NetworkKey)
	if err != nil {
		return nil, err
	}

	return ParseLibp2pKey(libp2pKey)
}

// GenerateAndEncodeLibp2pKey generates a new networking private key, and encodes it into hex
func GenerateAndEncodeLibp2pKey() (crypto.PrivKey, []byte, error) {
	priv, _, err := crypto.GenerateKeyPair(crypto.Secp256k1, 256)
	if err != nil {
		return nil, nil, err
	}

	buf, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}

	return priv, []byte(hex.EncodeToString(buf)), nil
}

// ParseLibp2pKey converts a byte array to a private key
func ParseLibp2pKey(key []byte) (crypto.PrivKey, error) {
	// Trim whitespace (including newlines) that might be present in the key file
	keyStr := strings.TrimSpace(string(key))
	if len(keyStr) == 0 {
		return nil, fmt.Errorf("empty key after trimming whitespace")
	}
	buf, err := hex.DecodeString(keyStr)
	if err != nil {
		previewLen := 20
		if len(keyStr) < previewLen {
			previewLen = len(keyStr)
		}
		return nil, fmt.Errorf("failed to decode hex key (length: %d, first %d chars: %q): %w", len(keyStr), previewLen, keyStr[:previewLen], err)
	}

	libp2pKey, err := crypto.UnmarshalPrivateKey(buf)
	if err != nil {
		return nil, err
	}

	return libp2pKey, nil
}
