package governance

import (
	"errors"
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/hashicorp/go-hclog"
)

// ErrProposalStoreUnavailable indicates that the persistent store cannot be accessed.
var ErrProposalStoreUnavailable = errors.New("proposal store not available")

// Dependencies defines the external capabilities required by the governance module.
type Dependencies struct {
	Logger hclog.Logger

	SaveProposal  func(*core.ParameterProposal) error
	GetProposal   func(string) (*core.ParameterProposal, error)
	GetAll        func() (map[string]*core.ParameterProposal, error)
	ListScheduled func(epoch uint64) ([]*core.ParameterProposal, error)

	CacheGet   func(string) (*core.ParameterProposal, bool)
	CacheSet   func(string, *core.ParameterProposal)
	CacheRange func(func(string, *core.ParameterProposal))
	SetActive  func(string, bool)
}

// Manager implements core.GovernanceManager backed by injected dependencies.
type Manager struct {
	deps   Dependencies
	logger hclog.Logger
}

// NewManager creates a governance manager.
func NewManager(deps Dependencies) core.GovernanceManager {
	if deps.Logger == nil {
		deps.Logger = hclog.NewNullLogger()
	}
	return &Manager{
		deps:   deps,
		logger: deps.Logger,
	}
}

// SaveProposal persists the proposal via the configured dependency.
func (m *Manager) SaveProposal(proposal *core.ParameterProposal) error {
	if proposal == nil {
		return fmt.Errorf("proposal is nil")
	}
	if m.deps.SaveProposal == nil {
		return ErrProposalStoreUnavailable
	}
	return m.deps.SaveProposal(proposal)
}

// GetAllProposals loads all proposals from the store.
func (m *Manager) GetAllProposals() (map[string]*core.ParameterProposal, error) {
	if m.deps.GetAll == nil {
		return nil, ErrProposalStoreUnavailable
	}
	return m.deps.GetAll()
}

// RecordVote reuses SaveProposal logic for persisting vote changes.
func (m *Manager) RecordVote(proposal *core.ParameterProposal) error {
	return m.SaveProposal(proposal)
}

// LoadAllIntoMemory hydrates the in-memory caches using the persistent store.
func (m *Manager) LoadAllIntoMemory() error {
	all, err := m.GetAllProposals()
	if err != nil {
		return err
	}

	for id, proposal := range all {
		if m.deps.CacheSet != nil {
			m.deps.CacheSet(id, proposal)
		}
		if m.deps.SetActive != nil && proposal != nil {
			active := proposal.Status == core.ProposalPending || proposal.Status == core.ProposalActive
			if active {
				m.deps.SetActive(id, true)
			}
		}
	}
	return nil
}

// HydrateProposal loads a single proposal into memory if absent.
func (m *Manager) HydrateProposal(proposalID string) (*core.ParameterProposal, error) {
	if proposalID == "" {
		return nil, fmt.Errorf("proposal id is empty")
	}
	if m.deps.CacheGet != nil {
		if proposal, ok := m.deps.CacheGet(proposalID); ok {
			return proposal, nil
		}
	}
	if m.deps.GetProposal == nil {
		return nil, ErrProposalStoreUnavailable
	}
	proposal, err := m.deps.GetProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if proposal != nil && m.deps.CacheSet != nil {
		m.deps.CacheSet(proposalID, proposal)
	}
	return proposal, nil
}

// LoadScheduled returns proposals scheduled for the given epoch.
func (m *Manager) LoadScheduled(epochNumber uint64) []*core.ParameterProposal {
	// 首先尝试从缓存获取
	if scheduled := m.listScheduledFromCache(epochNumber); len(scheduled) > 0 {
		return scheduled
	}
	
	// 缓存中没有，从数据库查询
	if m.deps.ListScheduled == nil {
		return nil
	}
	result, err := m.deps.ListScheduled(epochNumber)
	if err != nil {
		m.logger.Error("❌ [Manager.LoadScheduled] 查询失败", "epoch", epochNumber, "error", err)
		return nil
	}
	return result
}

// MarkProposalApplied updates scheduling metadata and persists the proposal.
func (m *Manager) MarkProposalApplied(proposalID string, appliedBlock uint64) error {
	proposal, err := m.HydrateProposal(proposalID)
	if err != nil {
		return err
	}
	if proposal == nil {
		return fmt.Errorf("proposal %s not found", proposalID)
	}

	proposal.Schedule.Applied = true
	proposal.Schedule.AppliedAtBlock = appliedBlock

	return m.SaveProposal(proposal)
}

func (m *Manager) listScheduledFromCache(epoch uint64) []*core.ParameterProposal {
	if m.deps.CacheRange == nil {
		return nil
	}

	results := make([]*core.ParameterProposal, 0)
	m.deps.CacheRange(func(id string, proposal *core.ParameterProposal) {
		if proposal == nil {
			return
		}
		schedule := proposal.Schedule
		if schedule.Scheduled && !schedule.Applied && schedule.EffectiveEpoch == epoch {
			results = append(results, proposal)
		}
	})
	return results
}
