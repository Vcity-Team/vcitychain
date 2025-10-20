package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
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
	db       *bolt.DB
	rewardDB *bolt.DB // 🆕 新增：奖励数据库
	close    chan struct{}

	StateSyncStore        *StateSyncStore
	CheckpointStore       *CheckpointStore
	EpochStore            *EpochStore
	ProposerSnapshotStore *ProposerSnapshotStore
	StakeStore            *StakeStore
	ValidatorStore        *ValidatorStore
	RewardStore           *RewardStore       // 🆕 新增奖励记录存储
	BlockTrackerStore     *BlockTrackerStore // 🆕 新增出块统计存储
	ParameterStore        *ParameterStore    // 🆕 新增参数存储
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

// BlockTrackerStore 出块统计存储
type BlockTrackerStore struct {
	db *bolt.DB
}

// ParameterCurrentValue 参数当前值存储结构
type ParameterCurrentValue struct {
	ParameterName string      `json:"parameter_name"`
	CurrentValue  interface{} `json:"current_value"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Source        string      `json:"source"` // "config" 或 "proposal_xxx"
}

// ParameterStore 参数存储
type ParameterStore struct {
	db *bolt.DB
}

// initialize 初始化参数存储
func (ps *ParameterStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("parameters"))
	return err
}

// SaveParameterValue 保存参数值
func (ps *ParameterStore) SaveParameterValue(paramName string, value interface{}, source string) error {
	return ps.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("parameters"))
		if bucket == nil {
			return fmt.Errorf("parameters bucket not found")
		}

		paramValue := &ParameterCurrentValue{
			ParameterName: paramName,
			CurrentValue:  value,
			UpdatedAt:     time.Now(),
			Source:        source,
		}

		data, err := json.Marshal(paramValue)
		if err != nil {
			return err
		}

		return bucket.Put([]byte(paramName), data)
	})
}

// GetParameterValue 获取参数值
func (ps *ParameterStore) GetParameterValue(paramName string) (interface{}, error) {
	var paramValue ParameterCurrentValue

	err := ps.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("parameters"))
		if bucket == nil {
			return fmt.Errorf("parameters bucket not found")
		}

		data := bucket.Get([]byte(paramName))
		if data == nil {
			return fmt.Errorf("parameter not found")
		}

		return json.Unmarshal(data, &paramValue)
	})

	if err != nil {
		return nil, err
	}

	return paramValue.CurrentValue, nil
}

// initialize 初始化出块统计存储
func (bts *BlockTrackerStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("blockTracker"))
	return err
}

// SaveEpochBlocks 保存epoch出块统计
func (bts *BlockTrackerStore) SaveEpochBlocks(epochNumber uint64, blockCounts map[types.Address]uint64) error {
	return bts.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("blockTracker"))
		if bucket == nil {
			return fmt.Errorf("blockTracker bucket not found")
		}

		// 将map序列化为JSON
		data, err := json.Marshal(blockCounts)
		if err != nil {
			return fmt.Errorf("failed to marshal block counts: %w", err)
		}

		// 使用epochNumber作为key
		key := fmt.Sprintf("epoch_%d", epochNumber)
		return bucket.Put([]byte(key), data)
	})
}

// LoadEpochBlocks 加载epoch出块统计
func (bts *BlockTrackerStore) LoadEpochBlocks(epochNumber uint64) (map[types.Address]uint64, error) {
	var blockCounts map[types.Address]uint64

	err := bts.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("blockTracker"))
		if bucket == nil {
			return fmt.Errorf("blockTracker bucket not found")
		}

		key := fmt.Sprintf("epoch_%d", epochNumber)
		data := bucket.Get([]byte(key))
		if data == nil {
			// 没有找到数据，返回空map
			blockCounts = make(map[types.Address]uint64)
			return nil
		}

		// 反序列化JSON
		err := json.Unmarshal(data, &blockCounts)
		if err != nil {
			return fmt.Errorf("failed to unmarshal block counts: %w", err)
		}

		return nil
	})

	return blockCounts, err
}

// LoadAllEpochBlocks 加载所有epoch的出块统计
func (bts *BlockTrackerStore) LoadAllEpochBlocks() (map[uint64]map[types.Address]uint64, error) {
	allBlocks := make(map[uint64]map[types.Address]uint64)

	err := bts.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("blockTracker"))
		if bucket == nil {
			return fmt.Errorf("blockTracker bucket not found")
		}

		return bucket.ForEach(func(k, v []byte) error {
			key := string(k)
			if len(key) > 6 && key[:6] == "epoch_" {
				// 解析epochNumber
				var epochNumber uint64
				if _, err := fmt.Sscanf(key, "epoch_%d", &epochNumber); err != nil {
					return err
				}

				// 反序列化数据
				var blockCounts map[types.Address]uint64
				if err := json.Unmarshal(v, &blockCounts); err != nil {
					return err
				}

				allBlocks[epochNumber] = blockCounts
			}
			return nil
		})
	})

	return allBlocks, err
}

// newState creates new instance of State
func newState(path string, logger hclog.Logger, closeCh chan struct{}) (*State, error) {
	db, err := bolt.Open(path, 0666, nil)
	if err != nil {
		return nil, err
	}

	// 🆕 创建独立的奖励数据库
	rewardDBPath := path + ".rewards"
	rewardDB, err := bolt.Open(rewardDBPath, 0666, nil)
	if err != nil {
		db.Close() // 清理主数据库
		return nil, fmt.Errorf("failed to open reward database: %w", err)
	}

	s := &State{
		db:                    db,
		rewardDB:              rewardDB, // 🆕 新增
		close:                 closeCh,
		StateSyncStore:        &StateSyncStore{db: db},
		CheckpointStore:       &CheckpointStore{db: db},
		EpochStore:            &EpochStore{db: db},
		ProposerSnapshotStore: &ProposerSnapshotStore{db: db},
		StakeStore:            &StakeStore{db: db},
		ValidatorStore:        &ValidatorStore{db: db},
		RewardStore:           &RewardStore{db: rewardDB}, // 🆕 使用独立数据库
		BlockTrackerStore:     &BlockTrackerStore{db: db}, // 🆕 使用主数据库
		ParameterStore:        &ParameterStore{db: db},    // 🆕 使用主数据库
	}

	if err = s.initStorages(); err != nil {
		s.Close() // 清理数据库
		return nil, err
	}

	// 🆕 初始化奖励数据库
	if err = s.initRewardDatabase(); err != nil {
		s.Close() // 清理数据库
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
		if err := s.BlockTrackerStore.initialize(tx); err != nil {
			return err
		}
		if err := s.ParameterStore.initialize(tx); err != nil {
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

// initRewardDatabase 初始化奖励数据库
func (s *State) initRewardDatabase() error {
	return s.rewardDB.Update(func(tx *bolt.Tx) error {
		// 🆕 初始化奖励存储
		if err := s.RewardStore.initialize(tx); err != nil {
			return err
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

// RecordReward 记录奖励（简化版）
func (rs *RewardStore) RecordReward(record *RewardRecordExtended) error {
	// 获取全局日志器
	logger := getGlobalLogger()

	// 生成键：epochNumber_recipient_rewardType
	key := fmt.Sprintf("%d_%s_%s", record.EpochNumber, record.Recipient, record.RewardType)

	// 序列化记录
	data, err := json.Marshal(record)
	if err != nil {
		if logger != nil {
			logger.Error("❌ RecordReward: 序列化失败",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient,
				"error", err)
		}
		return err
	}

	// 检查数据库状态
	if rs.db == nil {
		if logger != nil {
			logger.Error("❌ RecordReward: 数据库为nil",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient)
		}
		return fmt.Errorf("database is nil")
	}

	err = rs.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			if logger != nil {
				logger.Error("❌ RecordReward: rewards bucket not found",
					"epoch", record.EpochNumber,
					"recipient", record.Recipient)
			}
			return fmt.Errorf("rewards bucket not found")
		}

		// 生成唯一ID
		id, _ := bucket.NextSequence()
		record.ID = id

		// 添加超时机制防止卡死
		putDone := make(chan error, 1)
		go func() {
			putDone <- bucket.Put([]byte(key), data)
		}()

		select {
		case err := <-putDone:
			if err != nil {
				if logger != nil {
					logger.Error("❌ RecordReward: 存储数据失败",
						"epoch", record.EpochNumber,
						"recipient", record.Recipient,
						"error", err)
				}
				return err
			}
		case <-time.After(5 * time.Second):
			if logger != nil {
				logger.Error("❌ RecordReward: 存储数据超时",
					"epoch", record.EpochNumber,
					"recipient", record.Recipient,
					"key", key)
			}
			return fmt.Errorf("bucket.Put timeout after 5 seconds")
		}

		return nil
	})

	if err != nil {
		if logger != nil {
			logger.Error("❌ RecordReward: 数据库更新失败",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient,
				"error", err)
		}
		return err
	}

	return nil
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

// Close 关闭数据库连接
func (s *State) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.rewardDB != nil {
		if closeErr := s.rewardDB.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
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
