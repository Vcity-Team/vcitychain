package dpos

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

// copyVoterInfo 深拷贝 VoterInfo，确保所有引用类型都被正确拷贝
func copyVoterInfo(voter *VoterInfo) *VoterInfo {
	if voter == nil {
		return nil
	}

	voterCopy := &VoterInfo{
		Address:        voter.Address,
		VotingPower:    new(big.Int).Set(voter.VotingPower),
		VotedDelegates: make([]types.Address, len(voter.VotedDelegates)),
		LastVoteTime:   voter.LastVoteTime,
		LockedUntil:    voter.LockedUntil,
		Nonce:          make(map[uint64]bool),
	}

	// 深拷贝 VotedDelegates slice
	copy(voterCopy.VotedDelegates, voter.VotedDelegates)

	// 深拷贝 Nonce map
	for k, v := range voter.Nonce {
		voterCopy.Nonce[k] = v
	}

	// 深拷贝 DelegateVotes map
	if voter.DelegateVotes != nil {
		voterCopy.DelegateVotes = make(map[types.Address]*big.Int)
		for k, v := range voter.DelegateVotes {
			voterCopy.DelegateVotes[k] = new(big.Int).Set(v)
		}
	}

	// 深拷贝 SlashingRecords map
	if voter.SlashingRecords != nil {
		voterCopy.SlashingRecords = make(map[types.Address][]*SlashingRecord)
		for k, records := range voter.SlashingRecords {
			voterCopy.SlashingRecords[k] = make([]*SlashingRecord, len(records))
			copy(voterCopy.SlashingRecords[k], records)
		}
	}

	return voterCopy
}

