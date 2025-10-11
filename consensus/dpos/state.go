package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/hashicorp/go-hclog"
	bolt "go.etcd.io/bbolt"
)

var (
	edgeEventsLastProcessedBlockBucket = []byte("EdgeEventsLastProcessedBlock")
	edgeEventsLastProcessedBlockKey    = []byte("EdgeEventsLastProcessedBlockKey")
)

// MessageSignature encapsulates sender identifier and its signature
type MessageSignature struct {
	// Signer of the vote
	From string
	// Signature of the message
	Signature []byte
}

// TransportMessage represents the payload which is gossiped across the network
type TransportMessage struct {
	// Hash is encoded data
	Hash []byte
	// Message signature
	Signature []byte
	// From is the address of the message signer
	From string
	// Number of epoch
	EpochNumber uint64
}

// State represents a persistence layer which persists consensus data off-chain
type State struct {
	db    *bolt.DB
	close chan struct{}

	StateSyncStore        *StateSyncStore
	CheckpointStore       *CheckpointStore
	EpochStore            *EpochStore
	ProposerSnapshotStore *ProposerSnapshotStore
	StakeStore            *StakeStore
	ValidatorStore        *ValidatorStore
	RewardStore           *RewardStore // 🆕 新增奖励记录存储
}

// RewardRecordExtended 扩展的奖励记录结构，用于数据库存储
type RewardRecordExtended struct {
	ID              uint64    `json:"id"`
	EpochNumber     uint64    `json:"epoch_number"`
	Recipient       string    `json:"recipient"`
	RewardType      string    `json:"reward_type"`      // "validator" 或 "voter"
	Amount          string    `json:"amount"`           // 奖励金额(Wei)
	BlockCount      uint64    `json:"block_count"`      // 出块数量
	VoteWeight      string    `json:"vote_weight"`      // 投票权重
	Timestamp       time.Time `json:"timestamp"`        // 发放时间
	TransactionHash string    `json:"transaction_hash"` // 相关交易哈希
	Status          string    `json:"status"`           // "completed"
}

// RewardStore 奖励记录存储
type RewardStore struct {
	db *bolt.DB
}

// newState creates new instance of State
func newState(path string, logger hclog.Logger, closeCh chan struct{}) (*State, error) {
	db, err := bolt.Open(path, 0666, nil)
	if err != nil {
		return nil, err
	}

	s := &State{
		db:                    db,
		close:                 closeCh,
		StateSyncStore:        &StateSyncStore{db: db},
		CheckpointStore:       &CheckpointStore{db: db},
		EpochStore:            &EpochStore{db: db},
		ProposerSnapshotStore: &ProposerSnapshotStore{db: db},
		StakeStore:            &StakeStore{db: db},
		ValidatorStore:        &ValidatorStore{db: db},
		RewardStore:           &RewardStore{db: db}, // 🆕 新增
	}

	if err = s.initStorages(); err != nil {
		return nil, err
	}

	return s, nil
}

// initStorages initializes data storages
func (s *State) initStorages() error {
	// init the buckets
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := s.StateSyncStore.initialize(tx); err != nil {
			return err
		}
		if err := s.CheckpointStore.initialize(tx); err != nil {
			return err
		}
		if err := s.EpochStore.initialize(tx); err != nil {
			return err
		}
		if err := s.ProposerSnapshotStore.initialize(tx); err != nil {
			return err
		}
		if err := s.StakeStore.initialize(tx); err != nil {
			return err
		}
		if err := s.ValidatorStore.initialize(tx); err != nil {
			return err
		}
		if err := s.RewardStore.initialize(tx); err != nil { // 🆕 新增
			return err
		}

		_, err := tx.CreateBucketIfNotExists(edgeEventsLastProcessedBlockBucket)
		if err != nil {
			return fmt.Errorf("failed to create bucket=%s: %w", string(edgeEventsLastProcessedBlockBucket), err)
		}

		lastProcessedBlock, err := s.getLastProcessedEventsBlock(tx)
		if err != nil {
			return fmt.Errorf("failed to get last processed block: %w", err)
		}

		if lastProcessedBlock == 0 {
			// we do this for already existing chains, which just updated their Edge binary,
			// to not get events from scratch, but start from what other stores have
			lastSaved, err := s.CheckpointStore.getLastSaved(tx)
			if err != nil {
				return fmt.Errorf("could not initialize last processed block bucket: %w", err)
			}

			if lastSaved > 0 {
				return s.insertLastProcessedEventsBlock(lastSaved, tx)
			}
		}

		return nil
	})
}

// insertLastProcessedEventsBlock inserts the last processed block for events on Edge
func (s *State) insertLastProcessedEventsBlock(block uint64, dbTx *bolt.Tx) error {
	insertFn := func(tx *bolt.Tx) error {
		return tx.Bucket(edgeEventsLastProcessedBlockBucket).Put(
			edgeEventsLastProcessedBlockKey, common.EncodeUint64ToBytes(block))
	}

	if dbTx == nil {
		return s.db.Update(func(tx *bolt.Tx) error {
			return insertFn(tx)
		})
	}

	return insertFn(dbTx)
}

// getLastProcessedEventsBlock gets the last processed block for events on Edge
func (s *State) getLastProcessedEventsBlock(dbTx *bolt.Tx) (uint64, error) {
	var (
		lastProcessed uint64
		err           error
	)

	getFn := func(tx *bolt.Tx) {
		value := tx.Bucket(edgeEventsLastProcessedBlockBucket).Get(edgeEventsLastProcessedBlockKey)
		if value != nil {
			lastProcessed = common.EncodeBytesToUint64(value)
		}
	}

	if dbTx == nil {
		err = s.db.View(func(tx *bolt.Tx) error {
			getFn(tx)

			return nil
		})
	} else {
		getFn(dbTx)
	}

	return lastProcessed, err
}

// beginDBTransaction creates and begins a transaction on BoltDB
// Note that transaction needs to be manually rollback or committed
func (s *State) beginDBTransaction(isWriteTx bool) (*bolt.Tx, error) {
	return s.db.Begin(isWriteTx)
}

// bucketStats returns stats for the given bucket in db
func bucketStats(bucketName []byte, db *bolt.DB) (*bolt.BucketStats, error) {
	var stats *bolt.BucketStats

	err := db.View(func(tx *bolt.Tx) error {
		s := tx.Bucket(bucketName).Stats()
		stats = &s

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("cannot check bucket stats. Bucket name=%s: %w", string(bucketName), err)
	}

	return stats, nil
}

// ==================== RewardStore 方法实现 ====================

// initialize 初始化RewardStore
func (rs *RewardStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("rewards"))
	return err
}

// RecordReward 记录奖励
func (rs *RewardStore) RecordReward(record *RewardRecordExtended) error {
	return rs.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			return fmt.Errorf("rewards bucket not found")
		}

		// 生成唯一ID
		id, _ := bucket.NextSequence()
		record.ID = id

		// 生成键：epochNumber_recipient_rewardType
		key := fmt.Sprintf("%d_%s_%s", record.EpochNumber, record.Recipient, record.RewardType)

		// 序列化记录
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}

		return bucket.Put([]byte(key), data)
	})
}

// GetValidatorRewardHistory 查询验证者奖励历史
func (rs *RewardStore) GetValidatorRewardHistory(validatorAddress string, fromEpoch, toEpoch uint64) ([]RewardRecordExtended, error) {
	var records []RewardRecordExtended

	err := rs.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			return fmt.Errorf("rewards bucket not found")
		}

		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			var record RewardRecordExtended
			if err := json.Unmarshal(v, &record); err != nil {
				continue
			}

			// 过滤条件
			if record.Recipient == validatorAddress &&
				record.RewardType == "validator" &&
				record.EpochNumber >= fromEpoch &&
				record.EpochNumber <= toEpoch {
				records = append(records, record)
			}
		}

		return nil
	})

	// 按epoch倒序排列
	sort.Slice(records, func(i, j int) bool {
		return records[i].EpochNumber > records[j].EpochNumber
	})

	return records, err
}

// GetVoterRewardHistory 查询投票者奖励历史
func (rs *RewardStore) GetVoterRewardHistory(voterAddress string, fromEpoch, toEpoch uint64) ([]RewardRecordExtended, error) {
	var records []RewardRecordExtended

	err := rs.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			return fmt.Errorf("rewards bucket not found")
		}

		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			var record RewardRecordExtended
			if err := json.Unmarshal(v, &record); err != nil {
				continue
			}

			// 过滤条件
			if record.Recipient == voterAddress &&
				record.RewardType == "voter" &&
				record.EpochNumber >= fromEpoch &&
				record.EpochNumber <= toEpoch {
				records = append(records, record)
			}
		}

		return nil
	})

	// 按epoch倒序排列
	sort.Slice(records, func(i, j int) bool {
		return records[i].EpochNumber > records[j].EpochNumber
	})

	return records, err
}

// GetEpochRewardDetails 查询指定epoch的奖励详情
func (rs *RewardStore) GetEpochRewardDetails(epochNumber uint64) ([]RewardRecordExtended, error) {
	var records []RewardRecordExtended

	err := rs.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			return fmt.Errorf("rewards bucket not found")
		}

		cursor := bucket.Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			var record RewardRecordExtended
			if err := json.Unmarshal(v, &record); err != nil {
				continue
			}

			if record.EpochNumber == epochNumber {
				records = append(records, record)
			}
		}

		return nil
	})

	// 按金额降序排列
	sort.Slice(records, func(i, j int) bool {
		amountI, _ := new(big.Int).SetString(records[i].Amount, 10)
		amountJ, _ := new(big.Int).SetString(records[j].Amount, 10)
		return amountI.Cmp(amountJ) > 0
	})

	return records, err
}
