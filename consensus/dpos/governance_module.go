package dpos

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	governancemodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/governance"
)

func (d *DPoS) initGovernanceModule() {
	if d.governance != nil {
		return
	}

	deps := d.buildGovernanceModuleDependencies()
	d.governance = governancemodule.NewManager(deps)
}

func (d *DPoS) buildGovernanceModuleDependencies() governancemodule.Dependencies {
	return governancemodule.Dependencies{
		Logger: d.logger.Named("governance_module"),
		SaveProposal: func(proposal *core.ParameterProposal) error {
			store := d.getProposalStore()
			if store == nil {
				return governancemodule.ErrProposalStoreUnavailable
			}
			return store.SaveProposal(proposal)
		},
		GetProposal: func(id string) (*core.ParameterProposal, error) {
			store := d.getProposalStore()
			if store == nil {
				return nil, governancemodule.ErrProposalStoreUnavailable
			}
			return store.GetProposal(id)
		},
		GetAll: func() (map[string]*core.ParameterProposal, error) {
			store := d.getProposalStore()
			if store == nil {
				return nil, governancemodule.ErrProposalStoreUnavailable
			}
			return store.GetAllProposals()
		},
		ListScheduled: func(epoch uint64) ([]*core.ParameterProposal, error) {
			store := d.getProposalStore()
			if store == nil {
				return nil, governancemodule.ErrProposalStoreUnavailable
			}
			type scheduler interface {
				ListScheduledByEpoch(uint64) ([]*core.ParameterProposal, error)
			}
			if l, ok := interface{}(store).(scheduler); ok {
				return l.ListScheduledByEpoch(epoch)
			}
			return nil, fmt.Errorf("proposal store does not support scheduled listing")
		},
		CacheGet: func(id string) (*core.ParameterProposal, bool) {
			if d.parameterProposals == nil {
				return nil, false
			}
			prop, ok := d.parameterProposals[id]
			return prop, ok
		},
		CacheSet: func(id string, proposal *core.ParameterProposal) {
			if d.parameterProposals == nil {
				d.parameterProposals = make(map[string]*core.ParameterProposal)
			}
			d.parameterProposals[id] = proposal
		},
		CacheRange: func(iter func(string, *core.ParameterProposal)) {
			if d.parameterProposals == nil || iter == nil {
				return
			}
			for id, proposal := range d.parameterProposals {
				iter(id, proposal)
			}
		},
		SetActive: func(id string, active bool) {
			if d.activeProposals == nil {
				d.activeProposals = make(map[string]bool)
			}
			if active {
				d.activeProposals[id] = true
			} else {
				delete(d.activeProposals, id)
			}
		},
	}
}

func (d *DPoS) getProposalStore() *ProposalStore {
	if d.state == nil {
		return nil
	}
	return d.state.ProposalStore
}
