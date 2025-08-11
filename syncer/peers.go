package syncer

import (
	"math/big"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerHealth 表示peer的健康状态
type PeerHealth struct {
	// 响应时间统计
	ResponseTime time.Duration
	// 成功率 (0.0 - 1.0)
	SuccessRate float64
	// 最后成功时间
	LastSuccessTime time.Time
	// 连续失败次数
	ConsecutiveFailures int
	// 最后更新时间
	LastUpdateTime time.Time
}

// NoForkPeer 表示无分叉的peer
type NoForkPeer struct {
	// identifier
	ID peer.ID
	// peer's latest block number
	Number uint64
	// peer's distance
	Distance *big.Int
	// peer的健康状态
	Health *PeerHealth
}

// IsBetter 改进的peer比较逻辑，考虑健康度和负载均衡
func (p *NoForkPeer) IsBetter(t *NoForkPeer) bool {
	if p.Number != t.Number {
		return p.Number > t.Number
	}

	// 如果区块高度相同，优先选择更健康的peer
	if p.Health != nil && t.Health != nil {
		// 计算综合健康分数 (0-100)
		pScore := p.calculateHealthScore()
		tScore := t.calculateHealthScore()

		if pScore != tScore {
			return pScore > tScore
		}
	}

	// 如果健康度相同，选择距离更近的
	if p.Distance != nil && t.Distance != nil {
		return p.Distance.Cmp(t.Distance) < 0
	}

	// 如果距离也相同，选择最后更新更近的
	if p.Health != nil && t.Health != nil {
		return p.Health.LastUpdateTime.After(t.Health.LastUpdateTime)
	}

	return false
}

// calculateHealthScore 计算peer的综合健康分数 (0-100)
func (p *NoForkPeer) calculateHealthScore() int {
	if p.Health == nil {
		return 50 // 默认中等分数
	}

	score := 100

	// 响应时间评分 (0-30分)
	if p.Health.ResponseTime > 0 {
		if p.Health.ResponseTime < 100*time.Millisecond {
			score += 30
		} else if p.Health.ResponseTime < 500*time.Millisecond {
			score += 20
		} else if p.Health.ResponseTime < 1*time.Second {
			score += 10
		} else {
			score -= 10
		}
	}

	// 成功率评分 (0-40分)
	score += int(p.Health.SuccessRate * 40)

	// 连续失败惩罚 (0-30分)
	if p.Health.ConsecutiveFailures > 0 {
		penalty := p.Health.ConsecutiveFailures * 5
		if penalty > 30 {
			penalty = 30
		}
		score -= penalty
	}

	// 确保分数在0-100范围内
	if score < 0 {
		score = 0
	} else if score > 100 {
		score = 100
	}

	return score
}

// UpdateHealth 更新peer健康状态
func (p *NoForkPeer) UpdateHealth(responseTime time.Duration, success bool) {
	if p.Health == nil {
		p.Health = &PeerHealth{}
	}

	p.Health.ResponseTime = responseTime
	p.Health.LastUpdateTime = time.Now()

	if success {
		p.Health.SuccessRate = 0.9*p.Health.SuccessRate + 0.1 // 指数移动平均
		p.Health.LastSuccessTime = time.Now()
		p.Health.ConsecutiveFailures = 0
	} else {
		p.Health.SuccessRate = 0.9 * p.Health.SuccessRate // 指数移动平均
		p.Health.ConsecutiveFailures++
	}

	// 确保成功率在合理范围内
	if p.Health.SuccessRate < 0.0 {
		p.Health.SuccessRate = 0.0
	} else if p.Health.SuccessRate > 1.0 {
		p.Health.SuccessRate = 1.0
	}
}

// IsHealthy 判断peer是否健康
func (p *NoForkPeer) IsHealthy() bool {
	if p.Health == nil {
		return true // 默认认为健康
	}

	// 连续失败过多认为不健康
	if p.Health.ConsecutiveFailures >= 5 {
		return false
	}

	// 成功率过低认为不健康
	if p.Health.SuccessRate < 0.3 {
		return false
	}

	// 响应时间过长认为不健康
	if p.Health.ResponseTime > 2*time.Second {
		return false
	}

	return true
}

type PeerMap struct {
	sync.Map
	// 负载均衡相关
	lastSelectedPeer peer.ID
	selectionCount   int
	mu               sync.RWMutex
}

func NewPeerMap(peers []*NoForkPeer) *PeerMap {
	peerMap := new(PeerMap)

	peerMap.Put(peers...)

	return peerMap
}

func (m *PeerMap) Put(peers ...*NoForkPeer) {
	for _, peer := range peers {
		m.Store(peer.ID.String(), peer)
	}
}

// Remove removes a peer from heap if it exists
func (m *PeerMap) Remove(peerID peer.ID) {
	m.Delete(peerID.String())
}

// BestPeer 改进的peer选择逻辑，增加负载均衡和健康度检查
func (m *PeerMap) BestPeer(skipMap map[peer.ID]bool) *NoForkPeer {
	var bestPeers []*NoForkPeer
	var bestScore int

	// 第一轮：找到最高分数的peers
	m.Range(func(key, value interface{}) bool {
		peer, _ := value.(*NoForkPeer)

		if skipMap != nil && skipMap[peer.ID] {
			return true
		}

		// 跳过不健康的peer
		if !peer.IsHealthy() {
			return true
		}

		score := peer.calculateHealthScore()
		if score > bestScore {
			bestScore = score
			bestPeers = []*NoForkPeer{peer}
		} else if score == bestScore {
			bestPeers = append(bestPeers, peer)
		}

		return true
	})

	if len(bestPeers) == 0 {
		return nil
	}

	if len(bestPeers) == 1 {
		return bestPeers[0]
	}

	// 多个peer分数相同时，使用负载均衡
	return m.selectWithLoadBalancing(bestPeers)
}

// selectWithLoadBalancing 使用负载均衡选择peer
func (m *PeerMap) selectWithLoadBalancing(peers []*NoForkPeer) *NoForkPeer {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 如果上次选择的peer还在候选列表中，优先选择其他peer
	var candidates []*NoForkPeer
	for _, p := range peers {
		if p.ID != m.lastSelectedPeer {
			candidates = append(candidates, p)
		}
	}

	// 如果没有其他候选，重置选择
	if len(candidates) == 0 {
		candidates = peers
		m.lastSelectedPeer = ""
		m.selectionCount = 0
	}

	// 选择候选列表中的第一个
	selected := candidates[0]
	m.lastSelectedPeer = selected.ID
	m.selectionCount++

	return selected
}

// GetHealthyPeers 获取所有健康的peers
func (m *PeerMap) GetHealthyPeers() []*NoForkPeer {
	var healthyPeers []*NoForkPeer

	m.Range(func(key, value interface{}) bool {
		peer, _ := value.(*NoForkPeer)
		if peer.IsHealthy() {
			healthyPeers = append(healthyPeers, peer)
		}
		return true
	})

	return healthyPeers
}

// GetPeerHealth 获取指定peer的健康状态
func (m *PeerMap) GetPeerHealth(peerID peer.ID) *PeerHealth {
	if value, ok := m.Load(peerID.String()); ok {
		if peer, ok := value.(*NoForkPeer); ok {
			return peer.Health
		}
	}
	return nil
}
