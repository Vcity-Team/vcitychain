package dpos

import (
	"fmt"

	blsmodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/bls"
	"github.com/Vcity-Team/vcitychain/types"
)

func (d *DPoS) initBLSModule() {
	if d.bls != nil {
		return
	}

	deps := d.buildBLSModuleDependencies()
	d.bls = blsmodule.NewManager(deps)
}

func (d *DPoS) buildBLSModuleDependencies() blsmodule.Dependencies {
	return blsmodule.Dependencies{
		Logger: d.logger.Named("bls_module"),
		LoadKey: func(address types.Address) ([]byte, bool, error) {
			manager := d.getBLSKeyManager()
			if manager == nil {
				return nil, false, fmt.Errorf("BLS key manager not initialized")
			}
			bytes, exists := manager.GetBLSKey(address)
			return bytes, exists, nil
		},
		SaveKey: func(address types.Address, keyBytes []byte) error {
			manager := d.getBLSKeyManager()
			if manager == nil {
				return fmt.Errorf("BLS key manager not initialized")
			}
			return manager.SaveBLSKey(address, keyBytes)
		},
		BroadcastKey: func(address types.Address, keyBytes []byte, nodeType string) error {
			manager := d.getBLSKeyManager()
			if manager == nil {
				return fmt.Errorf("BLS key manager not initialized")
			}
			return manager.BroadcastBLSKey(address, keyBytes, nodeType)
		},
	}
}

func (d *DPoS) getBLSKeyManager() *BLSKeyManager {
	if d.runtime == nil || d.runtime.networkIntegration == nil {
		return nil
	}
	return d.runtime.networkIntegration.blsKeyManager
}

