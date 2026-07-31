package network

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// maxConnectionsPerIP limits the number of concurrent connections from a single IP.
	// This mitigates connection pool exhaustion attacks from a single source.
	maxConnectionsPerIP = 5
)

// ipConnectionGater restricts inbound connections per source IP.
type ipConnectionGater struct {
	mu     sync.RWMutex
	counts map[string]int
	limit  int
}

func newIPConnectionGater(limit int) *ipConnectionGater {
	if limit <= 0 {
		limit = maxConnectionsPerIP
	}

	return &ipConnectionGater{
		counts: make(map[string]int),
		limit:  limit,
	}
}

// InterceptPeerDial allows all outbound dials.
func (g *ipConnectionGater) InterceptPeerDial(peer.ID) bool {
	return true
}

// InterceptAddrDial allows all outbound address dials.
func (g *ipConnectionGater) InterceptAddrDial(peer.ID, ma.Multiaddr) bool {
	return true
}

// InterceptAccept rejects inbound connections when the source IP has reached the limit.
func (g *ipConnectionGater) InterceptAccept(conn network.ConnMultiaddrs) bool {
	ip := ipFromMultiaddr(conn.RemoteMultiaddr())
	if ip == "" {
		return true
	}

	// Loopback addresses are only reachable locally (tests/dev) and not an
	// external connection-pool exhaustion risk.
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}

	g.mu.RLock()
	count := g.counts[ip]
	g.mu.RUnlock()

	return count < g.limit
}

// InterceptSecured allows all authenticated connections (accept-time check is sufficient).
func (g *ipConnectionGater) InterceptSecured(network.Direction, peer.ID, network.ConnMultiaddrs) bool {
	return true
}

// InterceptUpgraded allows all fully upgraded connections.
func (g *ipConnectionGater) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

// onConnected increments the connection count for the given IP.
func (g *ipConnectionGater) onConnected(ip string) {
	if ip == "" {
		return
	}

	g.mu.Lock()
	g.counts[ip]++
	g.mu.Unlock()
}

// onDisconnected decrements the connection count for the given IP.
func (g *ipConnectionGater) onDisconnected(ip string) {
	if ip == "" {
		return
	}

	g.mu.Lock()
	if g.counts[ip] > 0 {
		g.counts[ip]--
	}
	g.mu.Unlock()
}

// ipFromMultiaddr extracts the IP string from a multiaddr.
func ipFromMultiaddr(addr ma.Multiaddr) string {
	if addr == nil {
		return ""
	}

	if ip, err := addr.ValueForProtocol(ma.P_IP4); err == nil {
		return ip
	}
	if ip, err := addr.ValueForProtocol(ma.P_IP6); err == nil {
		return ip
	}

	return ""
}
