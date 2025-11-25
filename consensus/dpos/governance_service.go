package dpos

import (
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	governancemodule "github.com/Vcity-Team/vcitychain/consensus/dpos/modules/governance"
)

var errProposalStoreUnavailable = governancemodule.ErrProposalStoreUnavailable

func (d *DPoS) ensureGovernanceModule() core.GovernanceManager {
	if d.governance == nil {
		d.initGovernanceModule()
	}
	return d.governance
}

func (d *DPoS) governanceSaveProposal(proposal *ParameterProposal) error {
	mgr, err := d.requireGovernanceManager("save proposal")
	if err != nil {
		return err
	}
	return mgr.SaveProposal(proposal)
}

func (d *DPoS) governanceGetAllProposals() (map[string]*ParameterProposal, error) {
	mgr, err := d.requireGovernanceManager("get proposals")
	if err != nil {
		return nil, err
	}
	return mgr.GetAllProposals()
}

func (d *DPoS) governanceRecordVote(proposal *ParameterProposal) error {
	mgr, err := d.requireGovernanceManager("record vote")
	if err != nil {
		return err
	}
	return mgr.RecordVote(proposal)
}

func (d *DPoS) governanceLoadAllIntoMemory() error {
	mgr, err := d.requireGovernanceManager("load proposals")
	if err != nil {
		return err
	}
	return mgr.LoadAllIntoMemory()
}

func (d *DPoS) governanceHydrateProposal(proposalID string) (*ParameterProposal, error) {
	mgr, err := d.requireGovernanceManager("hydrate proposal")
	if err != nil {
		return nil, err
	}
	return mgr.HydrateProposal(proposalID)
}

func (d *DPoS) governanceLoadScheduled(epoch uint64) []*ParameterProposal {
	mgr := d.ensureGovernanceModule()
	if mgr == nil {
		return nil
	}
	return mgr.LoadScheduled(epoch)
}

func (d *DPoS) governanceMarkProposalApplied(proposalID string, appliedBlock uint64) error {
	mgr, err := d.requireGovernanceManager("mark proposal applied")
	if err != nil {
		return err
	}
	return mgr.MarkProposalApplied(proposalID, appliedBlock)
}

func (d *DPoS) requireGovernanceManager(action string) (core.GovernanceManager, error) {
	mgr := d.ensureGovernanceModule()
	if mgr == nil {
		return nil, fmt.Errorf("governance manager not initialized (%s)", action)
	}
	return mgr, nil
}
