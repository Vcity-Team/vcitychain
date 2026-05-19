package network

import "github.com/libp2p/go-libp2p/core/peer"

// BootnodePeerIDs returns peer IDs from genesis bootnodes configuration.
func (s *Server) BootnodePeerIDs() []peer.ID {
	if s.bootnodes == nil {
		return nil
	}
	nodes := s.bootnodes.getBootnodes()
	ids := make([]peer.ID, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			ids = append(ids, n.ID)
		}
	}
	return ids
}
