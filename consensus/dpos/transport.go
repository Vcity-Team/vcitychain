package dpos

import (
	ibftProto "github.com/0xPolygon/go-ibft/messages/proto"
)

// BridgeTransport is an abstraction of network layer for a bridge
type BridgeTransport interface {
	Multicast(msg interface{})
}

// subscribeToIbftTopic subscribes to ibft topic
func (p *DPoS) subscribeToIbftTopic() error {
	// TODO: 实现 DPoS 的 IBFT 主题订阅
	// 临时禁用，等待 DPoS 运行时实现
	p.logger.Info("DPoS IBFT topic subscription disabled - not implemented yet")
	return nil
}

// createTopics create all topics for a DPoS instance
func (p *DPoS) createTopics() (err error) {
	// TODO: 实现 DPoS 的主题创建
	// 临时禁用，等待 DPoS 网络配置完善
	p.logger.Info("DPoS topics creation disabled - not implemented yet")
	return nil
}

// Multicast is implementation of core.Transport interface
func (p *DPoS) Multicast(msg *ibftProto.Message) {
	if err := p.consensusTopic.Publish(msg); err != nil {
		p.logger.Warn("failed to multicast consensus message", "error", err)
	}
}
