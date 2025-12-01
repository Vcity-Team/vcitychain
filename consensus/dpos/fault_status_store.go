package dpos

import (
	"bytes"
	"encoding/json"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// UpdateValidatorFaultStatus 更新验证者故障状态
func (s *StakeStore) UpdateValidatorFaultStatus(address types.Address, isFaulty bool, missedBlocks uint64, lastUpdateTime uint64, lastFaultyEpoch uint64, reason string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// 获取或创建故障状态bucket
		bucket, err := tx.CreateBucketIfNotExists([]byte("validatorFaultStatus"))
		if err != nil {
			return WrapError("create fault status bucket", err)
		}

		// 创建故障状态信息
		faultInfo := map[string]interface{}{
			"address":         address.String(),
			"isFaulty":        isFaulty,
			"missedBlocks":    missedBlocks,
			"lastUpdateTime":  lastUpdateTime,
			"lastFaultyEpoch": lastFaultyEpoch,
			"reason":          reason,
		}

		// 序列化并存储
		data, err := json.Marshal(faultInfo)
		if err != nil {
			return WrapError("marshal fault info", err)
		}

		return bucket.Put(address.Bytes(), data)
	})
}

// GetValidatorFaultStatus 获取验证者故障状态
func (s *StakeStore) GetValidatorFaultStatus(address types.Address) (map[string]interface{}, error) {
	var faultInfo map[string]interface{}
	key := address.Bytes()
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("validatorFaultStatus"))
		if bucket == nil {
			return nil // 没有故障记录
		}

		data := bucket.Get(key)
		if data == nil {
			// 🆕 调试：检查是否有其他key（遍历所有key）
			cursor := bucket.Cursor()
			for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
				if bytes.Equal(k, key) {
					// 找到了，但之前Get返回nil，可能是key格式问题
					if err := json.Unmarshal(v, &faultInfo); err == nil {
						return nil
					}
				}
			}
			return nil // 该验证者没有故障记录
		}

		return json.Unmarshal(data, &faultInfo)
	})

	return faultInfo, err
}

