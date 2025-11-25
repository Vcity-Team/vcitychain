package core

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

// ParameterProposal defines the structure for governance parameter proposals.
type ParameterProposal struct {
	ID                string                          `json:"id"`
	ProposalType      string                          `json:"proposalType"`
	Parameter         string                          `json:"parameter"`
	OldValue          interface{}                     `json:"oldValue"`
	NewValue          interface{}                     `json:"newValue"`
	Proposer          types.Address                   `json:"proposer"`
	StartBlock        uint64                          `json:"startBlock"`
	EndBlock          uint64                          `json:"endBlock"`
	ValidEndBlock     uint64                          `json:"validEndBlock"`
	Status            ProposalStatus                  `json:"status"`
	Votes             map[types.Address]ParameterVote `json:"votes"`
	Threshold         uint64                          `json:"threshold"`
	Description       string                          `json:"description"`
	CreatedAt         uint64                          `json:"createdAt"`
	ValidatorAddress  types.Address                   `json:"validatorAddress,omitempty"`
	RecoveryReason    string                          `json:"recoveryReason,omitempty"`
	ExecutedAt        uint64                          `json:"executedAt,omitempty"`
	ExecutedBy        string                          `json:"executedBy,omitempty"`
	ProposalSignature []byte                          `json:"proposalSignature,omitempty"`
	Schedule          ProposalScheduleMeta            `json:"schedule,omitempty"`
}

// ProposalStatus enumerates possible governance proposal states.
type ProposalStatus int

const (
	ProposalPending ProposalStatus = iota
	ProposalActive
	ProposalPassed
	ProposalRejected
	ProposalExecuted
)

// String returns the human-readable proposal status.
func (ps ProposalStatus) String() string {
	switch ps {
	case ProposalPending:
		return "pending"
	case ProposalActive:
		return "active"
	case ProposalPassed:
		return "passed"
	case ProposalRejected:
		return "rejected"
	case ProposalExecuted:
		return "executed"
	default:
		return "unknown"
	}
}

// ParameterVote captures an individual governance vote.
type ParameterVote struct {
	Voter      types.Address `json:"voter"`
	ProposalID string        `json:"proposalId"`
	Support    bool          `json:"support"`
	Weight     *big.Int      `json:"weight"`
	Timestamp  uint64        `json:"timestamp"`
	Signature  []byte        `json:"signature"`
}

// ProposalScheduleMeta stores scheduling metadata for proposals.
type ProposalScheduleMeta struct {
	Scheduled      bool   `json:"scheduled"`
	EffectiveEpoch uint64 `json:"effectiveEpoch"`
	Applied        bool   `json:"applied"`
	AppliedAtBlock uint64 `json:"appliedAtBlock"`
}

// ProposalCreateTxData is the payload structure for proposal creation.
type ProposalCreateTxData struct {
	ProposalType      string      `json:"proposalType"`
	Parameter         string      `json:"parameter"`
	NewValue          interface{} `json:"newValue"`
	Description       string      `json:"description"`
	RecoveryReason    string      `json:"recoveryReason,omitempty"`
	ProposerSignature []byte      `json:"proposerSignature"`
	CreatedAt         uint64      `json:"createdAt"`
}

// ProposalVoteTxData is the payload for voting transactions.
type ProposalVoteTxData struct {
	ProposalID    string `json:"proposalId"`
	Support       bool   `json:"support"`
	VoteSignature []byte `json:"voteSignature"`
}

// ProposalExecuteTxData is the payload for execution transactions.
type ProposalExecuteTxData struct {
	ProposalID string `json:"proposalId"`
}

// ParameterUpdate records applied parameter changes.
type ParameterUpdate struct {
	Parameter  string      `json:"parameter"`
	OldValue   interface{} `json:"oldValue"`
	NewValue   interface{} `json:"newValue"`
	BlockNum   uint64      `json:"blockNum"`
	Executed   bool        `json:"executed"`
	ProposalID string      `json:"proposalId"`
	ExecutedAt uint64      `json:"executedAt"`
}
