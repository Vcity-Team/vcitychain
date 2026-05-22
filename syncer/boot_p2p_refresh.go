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
		}
	}
	return refreshed
}

func (s *syncer) invalidateTrustedBootHeightCache() {
	s.trustedBootHeightMu.Lock()
	s.trustedBootHeightCachedAt = time.Time{}
	s.trustedBootHeightMu.Unlock()
}

// refreshBootP2PWhenCaughtUp 在 best_peer_not_ahead（P2P 高度<=local）时刷新 genesis boot GetStatus。
// force 时对所有 P2P<=local 的 boot 拉状态；否则仅 RPC 已高于 local 的 boot（与 refreshTrustedBootP2PStatus 一致）。
func (s *syncer) refreshBootP2PWhenCaughtUp(local uint64, force bool) int {
	if len(s.trustedBootnodeIDs) == 0 {
		return 0
	}
	s.lastBootP2PRefreshMu.Lock()
	if time.Since(s.lastBootP2PRefreshAt) < bootP2PRefreshMinInterval {
		s.lastBootP2PRefreshMu.Unlock()
		return 0
	}
	s.lastBootP2PRefreshAt = time.Now()
	s.lastBootP2PRefreshMu.Unlock()

	refreshed := 0
	for id := range s.trustedBootnodeIDs {
		before := uint64(0)
		if v, exists := s.peerMap.Load(id.String()); exists {
			if p, _ := v.(*NoForkPeer); p != nil {
				if p.Number > local {
					continue
				}
				before = p.Number
			}
		}
		rpcH, hasRPC := s.getTrustedBootRPCHeight(id)
		if !force {
			if !hasRPC || rpcH <= local {
				continue
			}
		}
		status, err := s.syncPeerClient.GetPeerStatus(id)
		if err != nil {
			continue
		}
		if status == nil {
			continue
		}
		s.putToPeerMap(status)
		if status.Number > before || status.Number > local {
			refreshed++
		}
	}
	return refreshed
}

// tryAdvancePeerViewForNextBlock 追平后等待 local+1：刷新 boot RPC 缓存并对所有 P2P<=local 的 boot GetStatus。
func (s *syncer) tryAdvancePeerViewForNextBlock(local uint64) bool {
	s.invalidateTrustedBootHeightCache()
	s.collectTrustedBootHeights(local)
	return s.refreshBootP2PWhenCaughtUp(local, true) > 0
}

// prepareCatchUpRound P0-1：绕过 RPC 高度缓存，刷新 boot P2P，并返回最新 trusted meta。
func (s *syncer) prepareCatchUpRound(local uint64) trustedTipResult {
	s.invalidateTrustedBootHeightCache()
	meta := s.computeTrustedBootnodeTip(local)
	if s.trustedQuorumIndicatesNextBlock(local, meta) {
		s.refreshTrustedBootP2PStatus(local, true)
	}
	s.refreshBootP2PWhenCaughtUp(local, true)
	return meta
}
