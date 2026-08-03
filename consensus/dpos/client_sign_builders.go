package dpos

import (
	"encoding/binary"
	"math/big"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// BuildDelegateRegistrationCalldata builds DPOS+REG wire bytes (no private key).
func BuildDelegateRegistrationCalldata(registrant types.Address, name, website, description string) []byte {
	data := make([]byte, 0, 128)
	data = append(data, []byte("DPOS")...)
	data = append(data, []byte("REG")...)
	data = append(data, 0x00)
	data = append(data, registrant.Bytes()...)

	appendLenPrefixed := func(dst []byte, s string) []byte {
		b := []byte(s)
		n := uint32(len(b))
		dst = append(dst, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		return append(dst, b...)
	}
	data = appendLenPrefixed(data, name)
	data = appendLenPrefixed(data, website)
	data = appendLenPrefixed(data, description)
	return data
}

// BuildCommissionUpdateCalldata builds DPOS+COM+uint16(rate) wire bytes.
func BuildCommissionUpdateCalldata(commissionRate uint16) []byte {
	out := make([]byte, 0, 9)
	out = append(out, []byte("DPOSCOM")...)
	out = append(out, byte(commissionRate>>8), byte(commissionRate&0xFF))
	return out
}

// BuildProposalDomainHash returns Keccak256 digest for proposal domain ECDSA (same as SignProposalForTx).
// Client signs this 32-byte hash (secp256k1, 65-byte sig) and embeds it as ProposalCreateTxData.proposerSignature (JSON base64).
func BuildProposalDomainHash(chainID uint64, proposal *ParameterProposal) []byte {
	if proposal == nil {
		return nil
	}
	data := make([]byte, 0, 64)
	data = append(data, proposal.Proposer.Bytes()...)

	proposalTypeBytes := []byte(proposal.ProposalType)
	proposalTypeLen := make([]byte, 4)
	binary.BigEndian.PutUint32(proposalTypeLen, uint32(len(proposalTypeBytes)))
	data = append(data, proposalTypeLen...)
	data = append(data, proposalTypeBytes...)

	if proposal.ProposalType == "validator_recovery" && proposal.ValidatorAddress != (types.Address{}) {
		data = append(data, proposal.ValidatorAddress.Bytes()...)
	} else {
		parameterBytes := []byte(proposal.Parameter)
		parameterLen := make([]byte, 4)
		binary.BigEndian.PutUint32(parameterLen, uint32(len(parameterBytes)))
		data = append(data, parameterLen...)
		data = append(data, parameterBytes...)
	}

	timestampBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(timestampBytes, proposal.CreatedAt)
	data = append(data, timestampBytes...)

	chainIDBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(chainIDBytes, chainID)
	data = append(data, chainIDBytes...)

	return crypto.Keccak256(data)
}

// BuildVoteDomainHash returns Keccak256 digest for governance vote domain ECDSA (same as SignVoteForTx).
func BuildVoteDomainHash(chainID uint64, vote *ParameterVote) []byte {
	if vote == nil {
		return nil
	}
	data := make([]byte, 0, 64)
	data = append(data, vote.Voter.Bytes()...)

	proposalIDBytes := []byte(vote.ProposalID)
	proposalIDLen := make([]byte, 4)
	binary.BigEndian.PutUint32(proposalIDLen, uint32(len(proposalIDBytes)))
	data = append(data, proposalIDLen...)
	data = append(data, proposalIDBytes...)

	if vote.Support {
		data = append(data, byte(0x01))
	} else {
		data = append(data, byte(0x00))
	}

	chainIDBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(chainIDBytes, chainID)
	data = append(data, chainIDBytes...)

	return crypto.Keccak256(data)
}

// GetDelegateDepositAmount returns the configured delegate registration deposit.
func (d *DPoS) GetDelegateDepositAmount() *big.Int {
	return d.getDelegateDepositAmount()
}
