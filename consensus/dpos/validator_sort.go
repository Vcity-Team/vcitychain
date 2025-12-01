package dpos

import (
	"bytes"
	"sort"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
)

// sortValidatorsByVotingPower 按投票权重降序排序验证者（权重相同时按地址升序）
func sortValidatorsByVotingPower(validators validator.AccountSet) {
	sort.Slice(validators, func(i, j int) bool {
		// 1. 首先按票数降序排序
		votingPowerCmp := validators[i].VotingPower.Cmp(validators[j].VotingPower)
		if votingPowerCmp != 0 {
			return votingPowerCmp > 0
		}
		// 2. 票数相同，按地址升序排序（确保完全一致）
		return bytes.Compare(validators[i].Address[:], validators[j].Address[:]) < 0
	})
}
