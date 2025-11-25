package dpos

import (
	"errors"
	"fmt"
)

var errProposalStoreUnavailable = errors.New("proposal store not available")

type governanceModule struct {
	d *DPoS
}

func newGovernanceModule(d *DPoS) *governanceModule {
	return &governanceModule{d: d}
}

func (d *DPoS) initGovernanceModule() {
	if d.governance == nil {
		d.governance = newGovernanceModule(d)
	}
}

func (d *DPoS) ensureGovernanceModule() *governanceModule {
	if d.governance == nil {
		d.governance = newGovernanceModule(d)
	}
	return d.governance
}

func (d *DPoS) governanceSaveProposal(proposal *ParameterProposal) error {
	return d.ensureGovernanceModule().saveProposal(proposal)
}

func (d *DPoS) governanceGetAllProposals() (map[string]*ParameterProposal, error) {
	return d.ensureGovernanceModule().getAllProposals()
}

func (d *DPoS) governanceRecordVote(proposal *ParameterProposal) error {
	return d.ensureGovernanceModule().recordVote(proposal)
}

func (d *DPoS) governanceLoadAllIntoMemory() error {
	return d.ensureGovernanceModule().loadAllIntoMemory()
}

func (d *DPoS) governanceHydrateProposal(proposalID string) (*ParameterProposal, error) {
	return d.ensureGovernanceModule().hydrateProposal(proposalID)
}

func (d *DPoS) governanceLoadScheduled(epoch uint64) []*ParameterProposal {
	return d.ensureGovernanceModule().loadScheduled(epoch)
}

func (d *DPoS) governanceMarkProposalApplied(proposalID string, appliedBlock uint64) error {
	return d.ensureGovernanceModule().markProposalApplied(proposalID, appliedBlock)
}

func (m *governanceModule) saveProposal(proposal *ParameterProposal) error {
	if proposal == nil {
		return errors.New("proposal is nil")
	}
	store := m.getProposalStore()
	if store == nil {
		return errProposalStoreUnavailable
	}
	return store.SaveProposal(proposal)
}

func (m *governanceModule) getProposal(id string) (*ParameterProposal, error) {
	if id == "" {
		return nil, errors.New("proposal id is empty")
	}
	store := m.getProposalStore()
	if store == nil {
		return nil, errProposalStoreUnavailable
	}
	return store.GetProposal(id)
}

func (m *governanceModule) getAllProposals() (map[string]*ParameterProposal, error) {
	store := m.getProposalStore()
	if store == nil {
		return nil, errProposalStoreUnavailable
	}
	return store.GetAllProposals()
}

func (m *governanceModule) listScheduledByEpoch(epoch uint64) ([]*ParameterProposal, error) {
	store := m.getProposalStore()
	if store == nil {
		return nil, errProposalStoreUnavailable
	}

	type schedLister interface {
		ListScheduledByEpoch(epoch uint64) ([]*ParameterProposal, error)
	}

	if l, ok := interface{}(store).(schedLister); ok {
		return l.ListScheduledByEpoch(epoch)
	}

	return nil, errors.New("proposal store does not support scheduled listing")
}

func (m *governanceModule) recordVote(proposal *ParameterProposal) error {
	return m.saveProposal(proposal)
}

func (m *governanceModule) loadProposalToMemory(proposalID string) (*ParameterProposal, error) {
	proposal, err := m.getProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if proposal != nil {
		m.d.parameterProposals[proposalID] = proposal
	}
	return proposal, nil
}

func (m *governanceModule) loadAllIntoMemory() error {
	all, err := m.getAllProposals()
	if err != nil {
		return err
	}

	for id, proposal := range all {
		m.d.parameterProposals[id] = proposal
		if proposal.Status == ProposalPending || proposal.Status == ProposalActive {
			m.d.activeProposals[id] = true
		}
	}
	return nil
}

func (m *governanceModule) listScheduledInMemory(epoch uint64) []*ParameterProposal {
	results := make([]*ParameterProposal, 0)
	for _, prop := range m.d.parameterProposals {
		if prop != nil &&
			prop.Schedule.Scheduled &&
			prop.Schedule.EffectiveEpoch == epoch &&
			!prop.Schedule.Applied {
			results = append(results, prop)
		}
	}
	return results
}

func (m *governanceModule) getProposalStore() *ProposalStore {
	if m.d.state == nil {
		return nil
	}
	return m.d.state.ProposalStore
}

func (m *governanceModule) getScheduledFromStore(epoch uint64) []*ParameterProposal {
	list, err := m.listScheduledByEpoch(epoch)
	if err != nil {
		return nil
	}
	return list
}

func (m *governanceModule) hydrateProposal(proposalID string) (*ParameterProposal, error) {
	if proposal, exists := m.d.parameterProposals[proposalID]; exists {
		return proposal, nil
	}
	return m.loadProposalToMemory(proposalID)
}

func (m *governanceModule) loadScheduled(epoch uint64) []*ParameterProposal {
	result := m.listScheduledInMemory(epoch)
	if len(result) > 0 {
		return result
	}
	if scheduled := m.getScheduledFromStore(epoch); len(scheduled) > 0 {
		return scheduled
	}
	return nil
}

func (m *governanceModule) markProposalApplied(proposalID string, appliedBlock uint64) error {
	if proposalID == "" {
		return fmt.Errorf("proposal id is empty")
	}

	proposal, err := m.hydrateProposal(proposalID)
	if err != nil {
		return err
	}
	if proposal == nil {
		return fmt.Errorf("proposal %s not found", proposalID)
	}

	proposal.Schedule.Applied = true
	proposal.Schedule.AppliedAtBlock = appliedBlock

	return m.saveProposal(proposal)
}
