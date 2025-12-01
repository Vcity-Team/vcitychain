package dpos

import (
	"encoding/binary"

	"github.com/Vcity-Team/vcitychain/types"
)

// buildStakingCompositeKey 构建质押信息的复合键
// 格式: staker (20 bytes) + delegate (20 bytes) + timestamp (8 bytes) = 48 bytes
func buildStakingCompositeKey(staker types.Address, delegate types.Address, timestamp uint64) []byte {
	key := make([]byte, 48)
	copy(key[0:20], staker[:])
	copy(key[20:40], delegate[:])
	binary.BigEndian.PutUint64(key[40:48], timestamp)
	return key
}
