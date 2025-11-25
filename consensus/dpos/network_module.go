package dpos

import (
	"fmt"

	networkmodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/network"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/types"
)

func (d *DPoS) initNetworkModule() {
	if d.network != nil {
		return
	}

	deps := d.buildNetworkModuleDependencies()
	d.network = networkmodule.NewManager(deps)
}

func (d *DPoS) buildNetworkModuleDependencies() networkmodule.Dependencies {
	return networkmodule.Dependencies{
		Logger: d.logger.Named("network_module"),
		PublishMessage: func(topic string, payload []byte) error {
			if d.runtime == nil || d.runtime.networkIntegration == nil {
				return fmt.Errorf("network integration not initialized")
			}

			if d.runtime.networkIntegration.topicManager == nil {
				return fmt.Errorf("topic manager not available")
			}

			t := d.runtime.networkIntegration.topicManager.GetTopic(topic)
			if t == nil {
				return fmt.Errorf("topic %s not available", topic)
			}

			return t.Publish(&dposProto.TransportMessage{
				Data: payload,
			})
		},
		RequestBLSKey: func(target types.Address) error {
			if d.runtime == nil || d.runtime.networkIntegration == nil {
				return fmt.Errorf("network integration not initialized")
			}

			var requester types.Address
			if d.key != nil {
				requester = types.Address(d.key.Address())
			}

			return d.runtime.networkIntegration.RequestBLSKey(target, requester)
		},
		GetValidatorConnectivity: func(address types.Address) (string, bool, bool) {
			if d.runtime == nil || d.runtime.networkIntegration == nil {
				return "", false, false
			}

			pID, hasMapping, isConnected := d.runtime.networkIntegration.GetValidatorConnectivity(address)
			return pID.String(), hasMapping, isConnected
		},
	}
}

