package dpos

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
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
	ProposalStore         *ProposalStore     // 🆕 新增提案存储
	RegistrationStore     *RegistrationStore // 🆕 新增受托人注册存储
	FreezeStore           *FreezeStore       // 🆕 新增冻结信息存储
}

// RegistrationStore 受托人注册存储
type RegistrationStore struct {
	db       *bolt.DB
	dbHelper *dbHelper
}

// NewRegistrationStore 创建新的受托人注册存储
func NewRegistrationStore(db *bolt.DB, logger hclog.Logger) *RegistrationStore {
	return &RegistrationStore{
		db:       db,
		dbHelper: newDBHelper(newLoggerWrapper(logger)),
	}
}

// SaveRegistration 保存注册信息
func (s *RegistrationStore) SaveRegistration(reg *DelegateRegistration) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return s.dbHelper.saveToBucket(tx, "delegate_registrations", reg.Address.Bytes(), reg)
	})
}

// GetRegistration 获取注册信息
func (s *RegistrationStore) GetRegistration(address types.Address) (*DelegateRegistration, error) {
	var reg *DelegateRegistration
	err := s.db.View(func(tx *bolt.Tx) error {
		reg = &DelegateRegistration{}
		return s.dbHelper.getFromBucketOptional(tx, "delegate_registrations", address.Bytes(), reg)
	})

	if err != nil {
		return nil, err
	}
	if reg.Address == (types.Address{}) {
		return nil, nil // 未找到
	}
	return reg, nil
}

// GetAllRegistrations 获取所有注册信息
func (s *RegistrationStore) GetAllRegistrations() ([]*DelegateRegistration, error) {
	var registrations []*DelegateRegistration
	err := s.db.View(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理遍历和反序列化
		return s.dbHelper.forEachInBucketWithUnmarshal(tx, "delegate_registrations", func() interface{} {
			return &DelegateRegistration{}
		}, func(key, item interface{}) error {
			reg := item.(*DelegateRegistration)
			registrations = append(registrations, reg)
			return nil
		})
	})

	return registrations, err
}

// UpdateRegistrationStatus 更新注册状态
func (s *RegistrationStore) UpdateRegistrationStatus(address types.Address, status RegStatus) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		reg := &DelegateRegistration{}
		if err := s.dbHelper.getFromBucket(tx, "delegate_registrations", address.Bytes(), reg); err != nil {
			return err
		}

		reg.Status = status
		switch status {
		case RegStatusActive:
			reg.IsActive = true
		case RegStatusWithdrawn:
			reg.IsActive = false
		}

		return s.dbHelper.saveToBucket(tx, "delegate_registrations", address.Bytes(), reg)
	})
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
	db     *bolt.DB
	logger *loggerWrapper
}

// RewardSummary 汇总奖励信息
type RewardSummary struct {
	Address              string                 `json:"address"`
	FromEpoch            uint64                 `json:"fromEpoch"`
	ToEpoch              uint64                 `json:"toEpoch"`
	TotalRewardWei       string                 `json:"totalRewardWei"`
	ValidatorRewardWei   string                 `json:"validatorRewardWei"`
	VoterRewardWei       string                 `json:"voterRewardWei"`
	ValidatorRecords     []RewardRecordExtended `json:"validatorRecords"`
	VoterRecords         []RewardRecordExtended `json:"voterRecords"`
	OtherRewardRecords   []RewardRecordExtended `json:"otherRewardRecords"`
	TotalRewardEther     string                 `json:"totalRewardEther,omitempty"`
	ValidatorRewardEther string                 `json:"validatorRewardEther,omitempty"`
	VoterRewardEther     string                 `json:"voterRewardEther,omitempty"`
	RecordCount          int                    `json:"recordCount"`
	ValidatorRecordCount int                    `json:"validatorRecordCount"`
	VoterRecordCount     int                    `json:"voterRecordCount"`
	OtherRecordCount     int                    `json:"otherRecordCount"`
}

// BlockTrackerStore 出块统计存储
type BlockTrackerStore struct {
	db       *bolt.DB
	dbHelper *dbHelper // 🆕 添加 dbHelper
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
	db       *bolt.DB
	dbHelper *dbHelper
}

// ProposalStore 提案存储
type ProposalStore struct {
	db       *bolt.DB
	logger   *loggerWrapper
	dbHelper *dbHelper // 🆕 添加 dbHelper
}

// FreezeInfo 冻结信息
type FreezeInfo struct {
	Address             types.Address `json:"address"`             // 账户地址
	FrozenAmount        *big.Int      `json:"frozenAmount"`        // 冻结金额
	FrozenAt            uint64        `json:"frozenAt"`            // 冻结时间
	UnfreezeAt          uint64        `json:"unfreezeAt"`          // 解冻时间（0表示未解冻）
	UnfreezeAvailableAt uint64        `json:"unfreezeAvailableAt"` // 资金可用时间（0表示未解冻）
	Status              string        `json:"status"`              // 状态：frozen, unfreezing, withdrawn
}

// FreezeStore 冻结信息存储
type FreezeStore struct {
	db       *bolt.DB
	dbHelper *dbHelper
}

// NewFreezeStore 创建新的冻结信息存储
func NewFreezeStore(db *bolt.DB, logger hclog.Logger) *FreezeStore {
	return &FreezeStore{
		db:       db,
		dbHelper: newDBHelper(newLoggerWrapper(logger)),
	}
}

// SaveFreezeInfo 保存冻结信息
func (s *FreezeStore) SaveFreezeInfo(info *FreezeInfo) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return s.dbHelper.saveToBucket(tx, "freeze_info", info.Address.Bytes(), info)
	})
}

// GetFreezeInfo 获取冻结信息
func (s *FreezeStore) GetFreezeInfo(address types.Address) (*FreezeInfo, error) {
	var info *FreezeInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		info = &FreezeInfo{}
		return s.dbHelper.getFromBucketOptional(tx, "freeze_info", address.Bytes(), info)
	})

	if err != nil {
		return nil, err
	}
	if info.Address == (types.Address{}) {
		return nil, nil // 未找到
	}
	return info, nil
}

// GetAllFreezeInfo 获取所有冻结信息
func (s *FreezeStore) GetAllFreezeInfo() ([]*FreezeInfo, error) {
	var infos []*FreezeInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理遍历和反序列化
		return s.dbHelper.forEachInBucketWithUnmarshal(tx, "freeze_info", func() interface{} {
			return &FreezeInfo{}
		}, func(key, item interface{}) error {
			info := item.(*FreezeInfo)
			infos = append(infos, info)
			return nil
		})
	})

	return infos, err
}

// DeleteFreezeInfo 删除冻结信息
func (s *FreezeStore) DeleteFreezeInfo(address types.Address) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("freeze_info"))
		if bucket == nil {
			return nil
		}

		return bucket.Delete(address.Bytes())
	})
}

// initialize 初始化参数存储
func (ps *ParameterStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("parameters"))
	return err
}

// SaveParameterValue 保存参数值
func (ps *ParameterStore) SaveParameterValue(paramName string, value interface{}, source string) error {
	return ps.db.Update(func(tx *bolt.Tx) error {
		paramValue := &ParameterCurrentValue{
			ParameterName: paramName,
			CurrentValue:  value,
			UpdatedAt:     time.Now(),
			Source:        source,
		}
		return ps.dbHelper.saveToBucket(tx, "parameters", []byte(paramName), paramValue)
	})
}

// GetParameterValue 获取参数值
func (ps *ParameterStore) GetParameterValue(paramName string) (interface{}, error) {
	var paramValue ParameterCurrentValue

	err := ps.db.View(func(tx *bolt.Tx) error {
		return ps.dbHelper.getFromBucket(tx, "parameters", []byte(paramName), &paramValue)
	})

	if err != nil {
		return nil, err
	}

	return paramValue.CurrentValue, nil
}

// initialize 初始化提案存储
func (ps *ProposalStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("proposals"))
	return err
}

// SaveProposal 保存提案
func (ps *ProposalStore) SaveProposal(proposal *ParameterProposal) error {
	ps.logger.Info("💾 [ProposalStore.SaveProposal] 开始保存提案", "proposalID", proposal.ID, "proposalType", proposal.ProposalType)
	err := ps.db.Update(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理
		if err := ps.dbHelper.saveToBucket(tx, "proposals", []byte(proposal.ID), proposal); err != nil {
			ps.logger.Error("❌ [ProposalStore.SaveProposal] 保存失败", "error", err, "proposalID", proposal.ID)
			return WrapError("save proposal", err)
		}

		ps.logger.Info("✅ [ProposalStore.SaveProposal] 提案已写入数据库", "proposalID", proposal.ID)
		return nil
	})
	if err != nil {
		ps.logger.Error("❌ [ProposalStore.SaveProposal] 数据库事务失败", "error", err, "proposalID", proposal.ID)
		return err
	}
	ps.logger.Info("✅ [ProposalStore.SaveProposal] 提案保存成功", "proposalID", proposal.ID)
	return nil
}

// GetProposal 获取提案
func (ps *ProposalStore) GetProposal(proposalID string) (*ParameterProposal, error) {
	ps.logger.Info("🔍 [ProposalStore.GetProposal] 开始查询提案", "proposalID", proposalID)
	var proposal ParameterProposal

	err := ps.db.View(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理
		if err := ps.dbHelper.getFromBucket(tx, "proposals", []byte(proposalID), &proposal); err != nil {
			ps.logger.Info("❌ [ProposalStore.GetProposal] 提案不存在", "proposalID", proposalID, "error", err)
			return WrapError("get proposal", err)
		}

		ps.logger.Info("✅ [ProposalStore.GetProposal] 提案查询成功", "proposalID", proposalID, "proposalType", proposal.ProposalType)
		return nil
	})

	if err != nil {
		ps.logger.Error("❌ [ProposalStore.GetProposal] 查询失败", "error", err, "proposalID", proposalID)
		return nil, err
	}

	return &proposal, nil
}

// GetAllProposals 获取所有提案
func (ps *ProposalStore) GetAllProposals() (map[string]*ParameterProposal, error) {
	proposals := make(map[string]*ParameterProposal)

	err := ps.db.View(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理遍历
		count := 0
		err := ps.dbHelper.forEachInBucket(tx, "proposals", func(key, value []byte) error {
			var proposal ParameterProposal
			if err := json.Unmarshal(value, &proposal); err != nil {
				ps.logger.Error("❌ [ProposalStore.GetAllProposals] 反序列化提案失败", "proposalID", string(key), "error", err)
				return WrapError("unmarshal proposal", err)
			}
			proposals[string(key)] = &proposal
			count++
			ps.logger.Debug("📋 [ProposalStore.GetAllProposals] 找到提案", "proposalID", string(key), "proposalType", proposal.ProposalType, "index", count)
			return nil
		})

		if err != nil {
			return err
		}

		ps.logger.Debug("✅ [ProposalStore.GetAllProposals] 查询完成", "totalCount", count)
		return nil
	})

	if err != nil {
		ps.logger.Error("❌ [ProposalStore.GetAllProposals] 查询失败", "error", err)
		return nil, err
	}

	return proposals, nil
}

// DeleteProposal 删除提案
func (ps *ProposalStore) DeleteProposal(proposalID string) error {
	return ps.db.Update(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理
		if err := ps.dbHelper.deleteFromBucket(tx, "proposals", []byte(proposalID)); err != nil {
			ps.logger.Error("❌ [ProposalStore.DeleteProposal] 删除失败", "error", err, "proposalID", proposalID)
			return WrapError("delete proposal", err)
		}
		ps.logger.Info("✅ [ProposalStore.DeleteProposal] 提案删除成功", "proposalID", proposalID)
		return nil
	})
}

// initialize 初始化出块统计存储
func (bts *BlockTrackerStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("blockTracker"))
	return err
}

// SaveEpochBlocks 保存epoch出块统计
func (bts *BlockTrackerStore) SaveEpochBlocks(epochNumber uint64, blockCounts map[types.Address]uint64) error {
	return bts.db.Update(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理
		key := fmt.Sprintf("epoch_%d", epochNumber)
		if err := bts.dbHelper.saveToBucket(tx, "blockTracker", []byte(key), blockCounts); err != nil {
			return WrapError("save epoch blocks", err)
		}
		return nil
	})
}

// LoadEpochBlocks 加载epoch出块统计
func (bts *BlockTrackerStore) LoadEpochBlocks(epochNumber uint64) (map[types.Address]uint64, error) {
	var blockCounts map[types.Address]uint64

	err := bts.db.View(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理（可选读取）
		key := fmt.Sprintf("epoch_%d", epochNumber)
		if err := bts.dbHelper.getFromBucketOptional(tx, "blockTracker", []byte(key), &blockCounts); err != nil {
			return WrapError("load epoch blocks", err)
		}

		// 如果没有找到数据，返回空map
		if blockCounts == nil {
			blockCounts = make(map[types.Address]uint64)
		}

		return nil
	})

	return blockCounts, err
}

// LoadAllEpochBlocks 加载所有epoch的出块统计
func (bts *BlockTrackerStore) LoadAllEpochBlocks() (map[uint64]map[types.Address]uint64, error) {
	allBlocks := make(map[uint64]map[types.Address]uint64)

	err := bts.db.View(func(tx *bolt.Tx) error {
		// 🆕 使用 dbHelper 统一处理遍历和反序列化
		return bts.dbHelper.forEachInBucketWithUnmarshal(tx, "blockTracker", func() interface{} {
			return make(map[types.Address]uint64)
		}, func(key, item interface{}) error {
			keyStr := string(key.([]byte))
			if len(keyStr) > 6 && keyStr[:6] == "epoch_" {
				// 解析epochNumber
				var epochNumber uint64
				if _, err := fmt.Sscanf(keyStr, "epoch_%d", &epochNumber); err != nil {
					return err
				}

				blockCounts := item.(map[types.Address]uint64)
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
		return nil, WrapError("open reward database", err)
	}

	s := &State{
		db:                    db,
		rewardDB:              rewardDB, // 🆕 新增
		close:                 closeCh,
		StateSyncStore:        &StateSyncStore{db: db},
		CheckpointStore:       &CheckpointStore{db: db},
		EpochStore:            &EpochStore{db: db},
		ProposerSnapshotStore: &ProposerSnapshotStore{db: db},
		StakeStore: func() *StakeStore {
			store := &StakeStore{db: db}
			// 初始化 logger wrapper
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				store.setLogger(newLoggerWrapper(dposInstance.logger))
			} else {
				store.setLogger(newLoggerWrapper(nil))
			}
			return store
		}(),
		ValidatorStore: func() *ValidatorStore {
			vs := &ValidatorStore{db: db, logger: newLoggerWrapper(logger)}
			vs.dbHelper = newDBHelper(newLoggerWrapper(logger)) // 🆕 添加 dbHelper
			return vs
		}(),
		RewardStore:       &RewardStore{db: rewardDB, logger: newLoggerWrapper(logger)},                                              // 🆕 使用独立数据库，使用 logger wrapper
		BlockTrackerStore: &BlockTrackerStore{db: db},                                                                                // 🆕 使用主数据库
		ParameterStore:    &ParameterStore{db: db, dbHelper: newDBHelper(newLoggerWrapper(logger))},                                  // 🆕 使用主数据库，使用 dbHelper
		ProposalStore:     &ProposalStore{db: db, logger: newLoggerWrapper(logger), dbHelper: newDBHelper(newLoggerWrapper(logger))}, // 🆕 使用主数据库，使用 logger wrapper 和 dbHelper
		RegistrationStore: NewRegistrationStore(db, logger),                                                                          // 🆕 使用主数据库，使用 dbHelper
		FreezeStore:       NewFreezeStore(db, logger),                                                                                // 🆕 使用主数据库，使用 dbHelper
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

// EnsureRewardStore ensures that the reward store is initialized and ready for use
func (s *State) EnsureRewardStore(logger hclog.Logger) error {
	if s == nil {
		return fmt.Errorf("state is nil")
	}

	if s.rewardDB != nil && s.RewardStore != nil {
		return nil
	}

	if s.db == nil {
		return fmt.Errorf("state database not initialized")
	}

	dbPath := s.db.Path()
	if dbPath == "" {
		return fmt.Errorf("state database path unavailable")
	}

	// Open reward DB if needed
	if s.rewardDB == nil {
		rewardDBPath := dbPath + ".rewards"
		rewardDB, err := bolt.Open(rewardDBPath, 0666, nil)
		if err != nil {
			return WrapError("open reward database", err)
		}
		s.rewardDB = rewardDB
	}

	if s.RewardStore == nil {
		s.RewardStore = &RewardStore{db: s.rewardDB, logger: newLoggerWrapper(logger)}
	}

	if err := s.initRewardDatabase(); err != nil {
		// Roll back reward database initialization on failure
		if s.rewardDB != nil {
			_ = s.rewardDB.Close()
		}
		s.rewardDB = nil
		s.RewardStore = nil
		return WrapError("initialize reward database", err)
	}

	if logger != nil {
		logger.Info("Reward store initialized", "path", s.rewardDB.Path())
	}

	return nil
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
		if err := s.ProposalStore.initialize(tx); err != nil {
			return err
		}
		// 🆕 初始化冻结信息存储
		if _, err := tx.CreateBucketIfNotExists([]byte("freeze_info")); err != nil {
			return err
		}

		_, err := tx.CreateBucketIfNotExists(edgeEventsLastProcessedBlockBucket)
		if err != nil {
			return WrapErrorf("create bucket", "bucket=%s: %w", string(edgeEventsLastProcessedBlockBucket), err)
		}

		lastProcessedBlock, err := s.getLastProcessedEventsBlock(tx)
		if err != nil {
			return WrapError("get last processed block", err)
		}

		if lastProcessedBlock == 0 {
			// we do this for already existing chains, which just updated their Edge binary,
			// to not get events from scratch, but start from what other stores have
			lastSaved, err := s.CheckpointStore.getLastSaved(tx)
			if err != nil {
				return WrapError("initialize last processed block bucket", err)
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
	return s.withTransaction(dbTx, func(tx *bolt.Tx) error {
		return tx.Bucket(edgeEventsLastProcessedBlockBucket).Put(
			edgeEventsLastProcessedBlockKey, common.EncodeUint64ToBytes(block))
	})
}

// getLastProcessedEventsBlock gets the last processed block for events on Edge
func (s *State) getLastProcessedEventsBlock(dbTx *bolt.Tx) (uint64, error) {
	var lastProcessed uint64

	err := s.withReadTransaction(dbTx, func(tx *bolt.Tx) error {
		value := tx.Bucket(edgeEventsLastProcessedBlockBucket).Get(edgeEventsLastProcessedBlockKey)
		if value != nil {
			lastProcessed = common.EncodeBytesToUint64(value)
		}
		return nil
	})

	return lastProcessed, err
}

// beginDBTransaction creates and begins a transaction on BoltDB
// Note that transaction needs to be manually rollback or committed
func (s *State) beginDBTransaction(isWriteTx bool) (*bolt.Tx, error) {
	return s.db.Begin(isWriteTx)
}

// withTransaction 统一处理数据库事务（写操作）
func (s *State) withTransaction(dbTx *bolt.Tx, fn func(*bolt.Tx) error) error {
	if dbTx == nil {
		return s.db.Update(fn)
	}
	return fn(dbTx)
}

// withReadTransaction 统一处理只读事务
func (s *State) withReadTransaction(dbTx *bolt.Tx, fn func(*bolt.Tx) error) error {
	if dbTx == nil {
		return s.db.View(fn)
	}
	return fn(dbTx)
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
		return nil, WrapErrorf("check bucket stats", "bucket name=%s: %w", string(bucketName), err)
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
	// 确保 logger 已初始化
	if rs.logger == nil {
		rs.logger = getGlobalLoggerWrapper()
	}

	// 生成键：epochNumber_recipient_rewardType
	key := fmt.Sprintf("%d_%s_%s", record.EpochNumber, record.Recipient, record.RewardType)

	// 序列化记录
	data, err := json.Marshal(record)
	if err != nil {
		rs.logger.Error("❌ RecordReward: 序列化失败",
			"epoch", record.EpochNumber,
			"recipient", record.Recipient,
			"error", err)
		return err
	}

	// 检查数据库状态
	if rs.db == nil {
		rs.logger.Error("❌ RecordReward: 数据库为nil",
			"epoch", record.EpochNumber,
			"recipient", record.Recipient)
		return fmt.Errorf("database is nil")
	}

	err = rs.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			rs.logger.Error("❌ RecordReward: rewards bucket not found",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient)
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
				rs.logger.Error("❌ RecordReward: 存储数据失败",
					"epoch", record.EpochNumber,
					"recipient", record.Recipient,
					"error", err)
				return err
			}
		case <-time.After(5 * time.Second):
			rs.logger.Error("❌ RecordReward: 存储数据超时",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient,
				"key", key)
			return fmt.Errorf("bucket.Put timeout after 5 seconds")
		}

		return nil
	})

	if err != nil {
		rs.logger.Error("❌ RecordReward: 数据库更新失败",
			"epoch", record.EpochNumber,
			"recipient", record.Recipient,
			"error", err)
		return err
	}

	return nil
}

// GetValidatorRewardHistory 查询验证者奖励历史
func (rs *RewardStore) GetValidatorRewardHistory(validatorAddress string, fromEpoch, toEpoch uint64) ([]RewardRecordExtended, error) {
	summary, err := rs.GetRewardSummary(validatorAddress, fromEpoch, toEpoch)
	if err != nil {
		return nil, err
	}

	records := summary.ValidatorRecords
	sort.Slice(records, func(i, j int) bool {
		return records[i].EpochNumber > records[j].EpochNumber
	})

	return records, nil
}

// GetVoterRewardHistory 查询投票者奖励历史
func (rs *RewardStore) GetVoterRewardHistory(voterAddress string, fromEpoch, toEpoch uint64) ([]RewardRecordExtended, error) {
	summary, err := rs.GetRewardSummary(voterAddress, fromEpoch, toEpoch)
	if err != nil {
		return nil, err
	}

	records := summary.VoterRecords
	sort.Slice(records, func(i, j int) bool {
		return records[i].EpochNumber > records[j].EpochNumber
	})

	return records, nil
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

// GetRewardSummary 汇总指定地址在一定epoch范围内的奖励详情
func (rs *RewardStore) GetRewardSummary(address string, fromEpoch, toEpoch uint64) (*RewardSummary, error) {
	sum := &RewardSummary{
		Address:            address,
		FromEpoch:          fromEpoch,
		ToEpoch:            toEpoch,
		ValidatorRecords:   []RewardRecordExtended{},
		VoterRecords:       []RewardRecordExtended{},
		OtherRewardRecords: []RewardRecordExtended{},
	}

	total := big.NewInt(0)
	validatorTotal := big.NewInt(0)
	voterTotal := big.NewInt(0)

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

			if record.Recipient != address {
				continue
			}
			if record.EpochNumber < fromEpoch || record.EpochNumber > toEpoch {
				continue
			}

			amount, ok := new(big.Int).SetString(record.Amount, 10)
			if !ok {
				continue
			}

			total.Add(total, amount)
			sum.RecordCount++

			rewardType := strings.ToLower(record.RewardType)
			isValidator := strings.Contains(rewardType, "validator")
			isVoter := strings.Contains(rewardType, "voter")

			if isValidator {
				sum.ValidatorRecords = append(sum.ValidatorRecords, record)
				validatorTotal.Add(validatorTotal, amount)
				sum.ValidatorRecordCount++
			}

			if isVoter {
				sum.VoterRecords = append(sum.VoterRecords, record)
				voterTotal.Add(voterTotal, amount)
				sum.VoterRecordCount++
			}

			if !isValidator && !isVoter {
				sum.OtherRewardRecords = append(sum.OtherRewardRecords, record)
				sum.OtherRecordCount++
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// 排序，按epoch倒序
	sort.Slice(sum.ValidatorRecords, func(i, j int) bool {
		return sum.ValidatorRecords[i].EpochNumber > sum.ValidatorRecords[j].EpochNumber
	})
	sort.Slice(sum.VoterRecords, func(i, j int) bool {
		return sum.VoterRecords[i].EpochNumber > sum.VoterRecords[j].EpochNumber
	})
	sort.Slice(sum.OtherRewardRecords, func(i, j int) bool {
		return sum.OtherRewardRecords[i].EpochNumber > sum.OtherRewardRecords[j].EpochNumber
	})

	sum.TotalRewardWei = total.String()
	sum.ValidatorRewardWei = validatorTotal.String()
	sum.VoterRewardWei = voterTotal.String()

	sum.TotalRewardEther = formatWeiToEther(total)
	sum.ValidatorRewardEther = formatWeiToEther(validatorTotal)
	sum.VoterRewardEther = formatWeiToEther(voterTotal)

	return sum, nil
}

func formatWeiToEther(amount *big.Int) string {
	if amount == nil || amount.Sign() == 0 {
		return "0"
	}

	weiPerEther := new(big.Float).SetFloat64(1e18)
	value := new(big.Float).SetInt(amount)
	value.Quo(value, weiPerEther)
	return value.Text('f', 6)
}
