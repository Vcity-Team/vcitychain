package bls

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies defines the capabilities required by the BLS manager module.
type Dependencies struct {
	Logger hclog.Logger

	// LoadKey fetches the raw BLS public key bytes.
	LoadKey func(address types.Address) ([]byte, bool, error)

	// SaveKey persists the raw BLS public key bytes.
	SaveKey func(address types.Address, keyBytes []byte) error

	// BroadcastKey broadcasts the key bytes to the network with the provided node type.
	BroadcastKey func(address types.Address, keyBytes []byte, nodeType string) error
}

// Manager implements core.BLSManager using injected dependencies.
type Manager struct {
	deps Dependencies
}

// NewManager builds a BLS manager backed by the given dependencies.
func NewManager(deps Dependencies) core.BLSManager {
	if deps.Logger == nil {
		deps.Logger = hclog.NewNullLogger()
	}
	return &Manager{deps: deps}
}

// GetBLSKey returns the BLS public key for the given validator address.
func (m *Manager) GetBLSKey(address types.Address) (*bls.PublicKey, error) {
	if m.deps.LoadKey == nil {
		return nil, fmt.Errorf("load key dependency not configured")
	}

	bytes, exists, err := m.deps.LoadKey(address)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("BLS key not found for address %s", address.String())
	}

	key, err := bls.UnmarshalPublicKey(bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal BLS public key: %w", err)
	}
	return key, nil
}

// SaveBLSKey persists the provided BLS public key.
func (m *Manager) SaveBLSKey(address types.Address, key *bls.PublicKey) error {
	if m.deps.SaveKey == nil {
		return fmt.Errorf("save key dependency not configured")
	}
	if key == nil {
		return fmt.Errorf("key must not be nil")
	}

	return m.deps.SaveKey(address, key.Marshal())
}

// BroadcastBLSKey broadcasts the BLS public key to the network.
func (m *Manager) BroadcastBLSKey(address types.Address, key *bls.PublicKey) error {
	if m.deps.BroadcastKey == nil {
		return fmt.Errorf("broadcast key dependency not configured")
	}
	if key == nil {
		return fmt.Errorf("key must not be nil")
	}

	return m.deps.BroadcastKey(address, key.Marshal(), "validator")
}

