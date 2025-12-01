package dpos

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	bolt "go.etcd.io/bbolt"
)

// SaveEpochValidators 保存指定epoch的验证者集合
func (s *StakeStore) SaveEpochValidators(epochNumber uint64, validators validator.AccountSet) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// 获取或创建epoch验证者bucket
		bucket, err := tx.CreateBucketIfNotExists([]byte("epochValidators"))
		if err != nil {
			return WrapError("create epoch validators bucket", err)
		}

		// 序列化验证者集合
		data, err := json.Marshal(validators)
		if err != nil {
			return WrapError("marshal validators", err)
		}

		// 🆕 使用epoch号作为key（而不是时间戳）
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, epochNumber)

		return bucket.Put(key, data)
	})
}

// GetEpochValidatorsByEpoch 按epoch号获取Epoch验证者集合
func (s *StakeStore) GetEpochValidatorsByEpoch(epochNumber uint64) (validator.AccountSet, error) {
	var validators validator.AccountSet

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("epochValidators"))
		if bucket == nil {
			return fmt.Errorf("epoch validators bucket not found")
		}

		// 使用epoch号作为key
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, epochNumber)

		data := bucket.Get(key)
		if data == nil {
			return fmt.Errorf("no validators found for epoch %d", epochNumber)
		}

		// 反序列化
		return json.Unmarshal(data, &validators)
	})

	return validators, err
}

// GetEpochValidators 获取最新的Epoch验证者集合（向后兼容，保留用于获取当前epoch）
func (s *StakeStore) GetEpochValidators() (validator.AccountSet, error) {
	var validators validator.AccountSet

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("epochValidators"))
		if bucket == nil {
			return fmt.Errorf("epoch validators bucket not found")
		}

		// 获取最新的验证者集合（按时间戳倒序，兼容旧数据）
		cursor := bucket.Cursor()
		_, data := cursor.Last()
		if data == nil {
			return fmt.Errorf("no epoch validators found")
		}

		// 反序列化
		return json.Unmarshal(data, &validators)
	})

	return validators, err
}

