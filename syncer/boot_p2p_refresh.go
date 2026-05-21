package syncer

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// trustedAheadNoServingBackoff 与出块间隔对齐，避免 500ms 空转刷屏。
	trustedAheadNoServingBackoff = 3 * time.Second
	bootP2PRefreshMinInterval    = 1 * time.Second
)

// setTrustedBootRPCHeight 记录各 boot 最近一次 eth_blockNumber（与 trusted tip 同源）。
func (s *syncer) setTrustedBootRPCHeight(id peer.ID, height uint64) {
	s.trustedBootRPCHeightMu.Lock()
	defer s.trustedBootRPCHeightMu.Unlock()
	if s.trustedBootRPCHeight == nil {
		s.trustedBootRPCHeight = make(map[peer.ID]uint64)
	}
	s.trustedBootRPCHeight[id] = height
}

func (s *syncer) getTrustedBootRPCHeight(id peer.ID) (uint64, bool) {
	s.trustedBootRPCHeightMu.RLock()
	defer s.trustedBootRPCHeightMu.RUnlock()
	h, ok := s.trustedBootRPCHeight[id]
	return h, ok
}

// refreshTrustedBootP2PStatus 对 RPC 已超前但 P2P gossip 滞后的 boot 主动 GetStatus，刷新 peerMap。
func (s *syncer) refreshTrustedBootP2PStatus(local uint64, force bool) int {
	if len(s.trustedBootnodeIDs) == 0 {
		return 0
	}
	s.lastBootP2PRefreshMu.Lock()
	if !force && time.Since(s.lastBootP2PRefreshAt) < bootP2PRefreshMinInterval {
		s.lastBootP2PRefreshMu.Unlock()
		return 0
	}
	s.lastBootP2PRefreshAt = time.Now()
	s.lastBootP2PRefreshMu.Unlock()

	refreshed := 0
	for id := range s.trustedBootnodeIDs {
		rpcH, ok := s.getTrustedBootRPCHeight(id)
		if !ok || rpcH <= local {
			continue
		}
		if v, exists := s.peerMap.Load(id.String()); exists {
			if p, _ := v.(*NoForkPeer); p != nil && p.Number > local {
				continue
			}
		}
		status, err := s.syncPeerClient.GetPeerStatus(id)
		if err != nil {
			s.logTrustedTipThrottled("boot-getstatus:"+id.String(), func() {
				s.logger.Debug("syncer: boot GetStatus refresh failed",
					"peer", id.String(),
					"rpcHeight", rpcH,
					"localLatest", local,
					"err", err)
			})
			continue
		}
		if status == nil {
			continue
		}
		before := uint64(0)
		if v, exists := s.peerMap.Load(id.String()); exists {
			if p, _ := v.(*NoForkPeer); p != nil {
				before = p.Number
			}
		}
		s.putToPeerMap(status)
		if status.Number > before {
			refreshed++
			s.logTrustedTipThrottled("boot-p2p-refresh:"+id.String(), func() {
				s.logger.Info("syncer: boot P2P height refreshed from GetStatus (RPC was ahead)",
					"peer", id.String(),
					"rpcHeight", rpcH,
					"p2pBefore", before,
					"p2pAfter", status.Number,
					"localLatest", local)
			})
		}
	}
	return refreshed
}
