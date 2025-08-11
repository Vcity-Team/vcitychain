package ibft

import (
	"google.golang.org/protobuf/proto"

	ibftMessages "github.com/0xPolygon/go-ibft/messages/proto"
)

func (i *backendIBFT) signMessage(msg *ibftMessages.Message) *ibftMessages.Message {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return nil
	}

	if msg.Signature, err = i.currentSigner.SignIBFTMessage(raw); err != nil {
		return nil
	}

	return msg
}

func (i *backendIBFT) BuildPrePrepareMessage(
	rawProposal []byte,
	certificate *ibftMessages.RoundChangeCertificate,
	view *ibftMessages.View,
) *ibftMessages.Message {
	proposedBlock := &ibftMessages.Proposal{
		RawProposal: rawProposal,
		Round:       view.Round,
	}

	// hash calculation begins
	proposalHash, err := i.calculateProposalHashFromBlockBytes(rawProposal, &view.Round)
	if err != nil {
		return nil
	}

	msg := &ibftMessages.Message{
		View: view,
		From: i.ID(),
		Type: ibftMessages.MessageType_PREPREPARE,
		Payload: &ibftMessages.Message_PreprepareData{
			PreprepareData: &ibftMessages.PrePrepareMessage{
				Proposal:     proposedBlock,
				ProposalHash: proposalHash.Bytes(),
				Certificate:  certificate,
			},
		},
	}

	return i.signMessage(msg)
}

func (i *backendIBFT) BuildPrepareMessage(proposalHash []byte, view *ibftMessages.View) *ibftMessages.Message {
	msg := &ibftMessages.Message{
		View: view,
		From: i.ID(),
		Type: ibftMessages.MessageType_PREPARE,
		Payload: &ibftMessages.Message_PrepareData{
			PrepareData: &ibftMessages.PrepareMessage{
				ProposalHash: proposalHash,
			},
		},
	}

	return i.signMessage(msg)
}

func (i *backendIBFT) BuildCommitMessage(proposalHash []byte, view *ibftMessages.View) *ibftMessages.Message {
	committedSeal, err := i.currentSigner.CreateCommittedSeal(proposalHash)
	if err != nil {
		i.logger.Error("Unable to build commit message, %v", err)

		return nil
	}

	msg := &ibftMessages.Message{
		View: view,
		From: i.ID(),
		Type: ibftMessages.MessageType_COMMIT,
		Payload: &ibftMessages.Message_CommitData{
			CommitData: &ibftMessages.CommitMessage{
				ProposalHash:  proposalHash,
				CommittedSeal: committedSeal,
			},
		},
	}

	return i.signMessage(msg)
}

func (i *backendIBFT) BuildRoundChangeMessage(
	proposal *ibftMessages.Proposal,
	certificate *ibftMessages.PreparedCertificate,
	view *ibftMessages.View,
) *ibftMessages.Message {
	msg := &ibftMessages.Message{
		View: view,
		From: i.ID(),
		Type: ibftMessages.MessageType_ROUND_CHANGE,
		Payload: &ibftMessages.Message_RoundChangeData{RoundChangeData: &ibftMessages.RoundChangeMessage{
			LastPreparedProposal:      proposal,
			LatestPreparedCertificate: certificate,
		}},
	}

	return i.signMessage(msg)
}
