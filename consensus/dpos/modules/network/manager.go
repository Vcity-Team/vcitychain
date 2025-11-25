package network

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// Dependencies defines the capabilities the network module relies on.
type Dependencies struct {
	Logger hclog.Logger

	// PublishMessage broadcasts raw payloads to the given topic.
	PublishMessage func(topic string, payload []byte) error

	// RequestBLSKey triggers a BLS key request to a target validator.
	RequestBLSKey func(target types.Address) error

	// GetValidatorConnectivity returns the current connectivity information.
	GetValidatorConnectivity func(address types.Address) (peerID string, hasMapping bool, isConnected bool)
}

// Manager implements core.NetworkManager using injected dependencies.
type Manager struct {
	deps Dependencies
}

// NewManager wires a network manager backed by the provided dependencies.
func NewManager(deps Dependencies) core.NetworkManager {
	if deps.Logger == nil {
		deps.Logger = hclog.NewNullLogger()
	}
	return &Manager{
		deps: deps,
	}
}

// BroadcastMessage publishes the payload on the requested topic.
func (m *Manager) BroadcastMessage(topic string, message []byte) error {
	if m.deps.PublishMessage == nil {
		return fmt.Errorf("publish dependency not configured")
	}
	return m.deps.PublishMessage(topic, message)
}

// RequestBLSKey asks another validator to share its BLS key.
func (m *Manager) RequestBLSKey(targetAddress types.Address) error {
	if m.deps.RequestBLSKey == nil {
		return fmt.Errorf("request BLS key dependency not configured")
	}
	return m.deps.RequestBLSKey(targetAddress)
}

// GetValidatorConnectivity exposes the current connectivity metadata.
func (m *Manager) GetValidatorConnectivity(address types.Address) (peerID string, hasMapping bool, isConnected bool) {
	if m.deps.GetValidatorConnectivity == nil {
		return "", false, false
	}
	return m.deps.GetValidatorConnectivity(address)
}
