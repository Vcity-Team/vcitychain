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
	rewardDB *bolt.DB // 新增：奖励数据库
	close    chan struct{}

	StateSyncStore        *StateSyncStore
	CheckpointStore       *CheckpointStore
	EpochStore            *EpochStore
	ProposerSnapshotStore *ProposerSnapshotStore
	StakeStore            *StakeStore
	ValidatorStore        *ValidatorStore
	RewardStore           *RewardStore       // 新增奖励记录存储
	BlockTrackerStore     *BlockTrackerStore // 新增出块统计存储
	ParameterStore        *ParameterStore    // 新增参数存储
	ProposalStore         *ProposalStore     // 新增提案存储
	RegistrationStore     *RegistrationStore // 新增受托人注册存储
	FreezeStore           *FreezeStore       // 新增冻结信息存储
}

// RegistrationStore 受托人注册存储
type RegistrationStore struct {
	db *bolt.DB
}

// NewRegistrationStore 创建新的受托人注册存储
func NewRegistrationStore(db *bolt.DB) *RegistrationStore {
	return &RegistrationStore{db: db}
}

// SaveRegistration 保存注册信息
func (s *RegistrationStore) SaveRegistration(reg *DelegateRegistration) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("delegate_registrations"))
		if err != nil {
			return err
		}

		data, err := json.Marshal(reg)
		if err != nil {
			return err
		}

		return bucket.Put(reg.Address.Bytes(), data)
	})
}

// GetRegistration 获取注册信息
func (s *RegistrationStore) GetRegistration(address types.Address) (*DelegateRegistration, error) {
	var reg *DelegateRegistration
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("delegate_registrations"))
		if bucket == nil {
			return nil
		}

		data := bucket.Get(address.Bytes())
		if data == nil {
			return nil
		}

		reg = &DelegateRegistration{}
		return json.Unmarshal(data, reg)
	})

	return reg, err
}

// GetAllRegistrations 获取所有注册信息
func (s *RegistrationStore) GetAllRegistrations() ([]*DelegateRegistration, error) {
	var registrations []*DelegateRegistration
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("delegate_registrations"))
		if bucket == nil {
			return nil
		}

		return bucket.ForEach(func(key, value []byte) error {
			reg := &DelegateRegistration{}
			if err := json.Unmarshal(value, reg); err != nil {
				return err
			}
			registrations = append(registrations, reg)
			return nil
		})
	})

	return registrations, err
}

// UpdateRegistrationStatus 更新注册状态
func (s *RegistrationStore) UpdateRegistrationStatus(address types.Address, status RegStatus) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("delegate_registrations"))
		if bucket == nil {
			return fmt.Errorf("registrations bucket not found")
		}

		data := bucket.Get(address.Bytes())
		if data == nil {
			return fmt.Errorf("registration not found")
		}

		reg := &DelegateRegistration{}
		if err := json.Unmarshal(data, reg); err != nil {
			return err
		}

		reg.Status = status
		switch status {
		case RegStatusActive:
			reg.IsActive = true
		case RegStatusWithdrawn:
			reg.IsActive = false
		}

		updatedData, err := json.Marshal(reg)
		if err != nil {
			return err
		}

		return bucket.Put(address.Bytes(), updatedData)
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
	db *bolt.DB
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

// ProposalStore 提案存储
type ProposalStore struct {
	db     *bolt.DB
	logger hclog.Logger
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
	db *bolt.DB
}

// NewFreezeStore 创建新的冻结信息存储
func NewFreezeStore(db *bolt.DB) *FreezeStore {
	return &FreezeStore{db: db}
}

// SaveFreezeInfo 保存冻结信息
func (s *FreezeStore) SaveFreezeInfo(info *FreezeInfo) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("freeze_info"))
		if err != nil {
			return err
		}

		data, err := json.Marshal(info)
		if err != nil {
			return err
		}

		return bucket.Put(info.Address.Bytes(), data)
	})
}

// GetFreezeInfo 获取冻结信息
func (s *FreezeStore) GetFreezeInfo(address types.Address) (*FreezeInfo, error) {
	var info *FreezeInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("freeze_info"))
		if bucket == nil {
			return nil
		}

		data := bucket.Get(address.Bytes())
		if data == nil {
			return nil
		}

		info = &FreezeInfo{}
		return json.Unmarshal(data, info)
	})

	return info, err
}

// GetAllFreezeInfo 获取所有冻结信息
func (s *FreezeStore) GetAllFreezeInfo() ([]*FreezeInfo, error) {
	var infos []*FreezeInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("freeze_info"))
		if bucket == nil {
			return nil
		}

		return bucket.ForEach(func(key, value []byte) error {
			info := &FreezeInfo{}
			if err := json.Unmarshal(value, info); err != nil {
				return err
			}
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

// initialize 初始化提案存储
func (ps *ProposalStore) initialize(tx *bolt.Tx) error {
	_, err := tx.CreateBucketIfNotExists([]byte("proposals"))
	return err
}

// SaveProposal 保存提案
func (ps *ProposalStore) SaveProposal(proposal *ParameterProposal) error {
	if ps.logger != nil {
		ps.logger.Info("💾 [ProposalStore.SaveProposal] 开始保存提案", "proposalID", proposal.ID, "proposalType", proposal.ProposalType)
	}
	err := ps.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("proposals"))
		if bucket == nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.SaveProposal] proposals bucket not found")
			}
			return fmt.Errorf("proposals bucket not found")
		}

		data, err := json.Marshal(proposal)
		if err != nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.SaveProposal] 序列化提案失败", "error", err, "proposalID", proposal.ID)
			}
			return fmt.Errorf("failed to marshal proposal: %w", err)
		}

		if err := bucket.Put([]byte(proposal.ID), data); err != nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.SaveProposal] 写入数据库失败", "error", err, "proposalID", proposal.ID)
			}
			return err
		}

		if ps.logger != nil {
			ps.logger.Info("✅ [ProposalStore.SaveProposal] 提案已写入数据库", "proposalID", proposal.ID, "dataSize", len(data))
		}
		return nil
	})
	if err != nil {
		if ps.logger != nil {
			ps.logger.Error("❌ [ProposalStore.SaveProposal] 数据库事务失败", "error", err, "proposalID", proposal.ID)
		}
		return err
	}
	if ps.logger != nil {
		ps.logger.Info("✅ [ProposalStore.SaveProposal] 提案保存成功", "proposalID", proposal.ID)
	}
	return nil
}

// GetProposal 获取提案
func (ps *ProposalStore) GetProposal(proposalID string) (*ParameterProposal, error) {
	if ps.logger != nil {
		ps.logger.Info("🔍 [ProposalStore.GetProposal] 开始查询提案", "proposalID", proposalID)
	}
	var proposal ParameterProposal

	err := ps.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("proposals"))
		if bucket == nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.GetProposal] proposals bucket not found")
			}
			return fmt.Errorf("proposals bucket not found")
		}

		data := bucket.Get([]byte(proposalID))
		if data == nil {
			if ps.logger != nil {
				ps.logger.Info("❌ [ProposalStore.GetProposal] 提案不存在", "proposalID", proposalID)
			}
			return fmt.Errorf("proposal not found")
		}

		if ps.logger != nil {
			ps.logger.Info("✅ [ProposalStore.GetProposal] 找到提案数据", "proposalID", proposalID, "dataSize", len(data))
		}

		if err := json.Unmarshal(data, &proposal); err != nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.GetProposal] 反序列化提案失败", "error", err, "proposalID", proposalID)
			}
			return err
		}

		if ps.logger != nil {
			ps.logger.Info("✅ [ProposalStore.GetProposal] 提案查询成功", "proposalID", proposalID, "proposalType", proposal.ProposalType)
		}
		return nil
	})

	if err != nil {
		if ps.logger != nil {
			ps.logger.Error("❌ [ProposalStore.GetProposal] 查询失败", "error", err, "proposalID", proposalID)
		}
		return nil, err
	}

	return &proposal, nil
}

// GetAllProposals 获取所有提案
func (ps *ProposalStore) GetAllProposals() (map[string]*ParameterProposal, error) {
	proposals := make(map[string]*ParameterProposal)

	err := ps.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("proposals"))
		if bucket == nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.GetAllProposals] proposals bucket not found")
			}
			return fmt.Errorf("proposals bucket not found")
		}

		count := 0
		err := bucket.ForEach(func(key, value []byte) error {
			var proposal ParameterProposal
			if err := json.Unmarshal(value, &proposal); err != nil {
				if ps.logger != nil {
					ps.logger.Error("❌ [ProposalStore.GetAllProposals] 反序列化提案失败", "proposalID", string(key), "error", err)
				}
				return err
			}
			proposals[string(key)] = &proposal
			count++
			if ps.logger != nil {
				ps.logger.Debug("📋 [ProposalStore.GetAllProposals] 找到提案", "proposalID", string(key), "proposalType", proposal.ProposalType, "index", count)
			}
			return nil
		})

		if ps.logger != nil {
			ps.logger.Debug("✅ [ProposalStore.GetAllProposals] 查询完成", "totalCount", count)
		}
		return err
	})

	if err != nil {
		if ps.logger != nil {
			ps.logger.Error("❌ [ProposalStore.GetAllProposals] 查询失败", "error", err)
		}
		return nil, err
	}

	return proposals, nil
}

// DeleteProposal 删除提案
func (ps *ProposalStore) DeleteProposal(proposalID string) error {
	return ps.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("proposals"))
		if bucket == nil {
			return fmt.Errorf("proposals bucket not found")
		}

		return bucket.Delete([]byte(proposalID))
	})
}

// ListScheduledByEpoch 根据 EffectiveEpoch 查询待应用的提案
func (ps *ProposalStore) ListScheduledByEpoch(epochNumber uint64) ([]*ParameterProposal, error) {
	// 添加详细日志，跟踪传入的epoch参数
	if ps.logger != nil {
		ps.logger.Debug("🔍🔍🔍 [ProposalStore.ListScheduledByEpoch] 开始查询提案",
			"epochNumber", epochNumber,
			"说明", "查询effectiveEpoch等于此值的待应用提案")
	}

	var scheduledProposals []*ParameterProposal

	err := ps.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("proposals"))
		if bucket == nil {
			if ps.logger != nil {
				ps.logger.Error("❌ [ProposalStore.ListScheduledByEpoch] proposals bucket not found")
			}
			return fmt.Errorf("proposals bucket not found")
		}

		count := 0
		err := bucket.ForEach(func(key, value []byte) error {
			var proposal ParameterProposal
			if err := json.Unmarshal(value, &proposal); err != nil {
				if ps.logger != nil {
					ps.logger.Warn("❌ [ProposalStore.ListScheduledByEpoch] 反序列化提案失败", "proposalID", string(key), "error", err)
				}
				// 跳过损坏的提案，继续处理其他提案
				return nil
			}

			// 检查是否符合条件：Scheduled=true, Applied=false, EffectiveEpoch=epochNumber
			// 添加详细日志，跟踪每个提案的检查过程
			if ps.logger != nil && (proposal.Schedule.Scheduled || proposal.Schedule.EffectiveEpoch > 0) {
				ps.logger.Info("🔍 [ProposalStore.ListScheduledByEpoch] 检查提案",
					"proposalID", proposal.ID,
					"proposalType", proposal.ProposalType,
					"scheduled", proposal.Schedule.Scheduled,
					"applied", proposal.Schedule.Applied,
					"effectiveEpoch", proposal.Schedule.EffectiveEpoch,
					"queryEpoch", epochNumber,
					"match", proposal.Schedule.Scheduled && !proposal.Schedule.Applied && proposal.Schedule.EffectiveEpoch == epochNumber)
			}

			if proposal.Schedule.Scheduled &&
				!proposal.Schedule.Applied &&
				proposal.Schedule.EffectiveEpoch == epochNumber {
				scheduledProposals = append(scheduledProposals, &proposal)
				count++
				if ps.logger != nil {
					ps.logger.Info("✅ [ProposalStore.ListScheduledByEpoch] 找到待应用提案",
						"proposalID", proposal.ID,
						"proposalType", proposal.ProposalType,
						"effectiveEpoch", proposal.Schedule.EffectiveEpoch,
						"queryEpoch", epochNumber,
						"index", count)
				}
			}

			return nil
		})

		if ps.logger != nil {
			ps.logger.Debug("✅ [ProposalStore.ListScheduledByEpoch] 查询完成",
				"epochNumber", epochNumber,
				"totalCount", count)
		}

		return err
	})

	if err != nil {
		if ps.logger != nil {
			ps.logger.Error("❌ [ProposalStore.ListScheduledByEpoch] 查询失败", "error", err, "epochNumber", epochNumber)
		}
		return nil, err
	}

	return scheduledProposals, nil
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

	// 创建独立的奖励数据库
	rewardDBPath := path + ".rewards"
	rewardDB, err := bolt.Open(rewardDBPath, 0666, nil)
	if err != nil {
		db.Close() // 清理主数据库
		return nil, fmt.Errorf("failed to open reward database: %w", err)
	}

	s := &State{
		db:                    db,
		rewardDB:              rewardDB, // 新增
		close:                 closeCh,
		StateSyncStore:        &StateSyncStore{db: db},
		CheckpointStore:       &CheckpointStore{db: db},
		EpochStore:            &EpochStore{db: db},
		ProposerSnapshotStore: &ProposerSnapshotStore{db: db},
		StakeStore:            &StakeStore{db: db},
		ValidatorStore:        &ValidatorStore{db: db},
		RewardStore:           &RewardStore{db: rewardDB},             // 使用独立数据库
		BlockTrackerStore:     &BlockTrackerStore{db: db},             // 使用主数据库
		ParameterStore:        &ParameterStore{db: db},                // 使用主数据库
		ProposalStore:         &ProposalStore{db: db, logger: logger}, // 使用主数据库
		RegistrationStore:     &RegistrationStore{db: db},             // 使用主数据库
		FreezeStore:           NewFreezeStore(db),                     // 使用主数据库
	}

	if err = s.initStorages(); err != nil {
		s.Close() // 清理数据库
		return nil, err
	}

	// 初始化奖励数据库
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
			return fmt.Errorf("failed to open reward database: %w", err)
		}
		s.rewardDB = rewardDB
	}

	if s.RewardStore == nil {
		s.RewardStore = &RewardStore{db: s.rewardDB}
	}

	if err := s.initRewardDatabase(); err != nil {
		// Roll back reward database initialization on failure
		if s.rewardDB != nil {
			_ = s.rewardDB.Close()
		}
		s.rewardDB = nil
		s.RewardStore = nil
		return fmt.Errorf("failed to initialize reward database: %w", err)
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
		// 初始化冻结信息存储
		if _, err := tx.CreateBucketIfNotExists([]byte("freeze_info")); err != nil {
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
		// 初始化奖励存储
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

		// 检查是否已存在记录（用于跟踪覆盖）
		existingData := bucket.Get([]byte(key))
		isOverwrite := existingData != nil
		if isOverwrite && logger != nil {
			var existingRecord RewardRecordExtended
			if err := json.Unmarshal(existingData, &existingRecord); err == nil {
				// 🔧 保护：如果旧记录的 BlockCount > 0，而新记录的 BlockCount == 0，则保留旧值
				if existingRecord.BlockCount > 0 && record.BlockCount == 0 {
					logger.Info("🛡️ RecordReward: 检测到覆盖风险，保留旧BlockCount",
						"epoch", record.EpochNumber,
						"recipient", record.Recipient,
						"rewardType", record.RewardType,
						"key", key,
						"oldBlockCount", existingRecord.BlockCount,
						"newBlockCount", record.BlockCount,
						"oldAmount", existingRecord.Amount,
						"newAmount", record.Amount,
						"action", "保留旧BlockCount，避免被0覆盖")
					// 保留旧的 BlockCount
					record.BlockCount = existingRecord.BlockCount
				} else {
					logger.Info("⚠️ RecordReward: 检测到覆盖已有记录",
						"epoch", record.EpochNumber,
						"recipient", record.Recipient,
						"rewardType", record.RewardType,
						"key", key,
						"oldBlockCount", existingRecord.BlockCount,
						"newBlockCount", record.BlockCount,
						"oldAmount", existingRecord.Amount,
						"newAmount", record.Amount)
				}
			}
		}

		// 生成唯一ID
		id, _ := bucket.NextSequence()
		record.ID = id

		// 记录写入信息
		if logger != nil {
			logger.Info("📝 RecordReward: 写入奖励记录",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient,
				"rewardType", record.RewardType,
				"blockCount", record.BlockCount,
				"amount", record.Amount,
				"key", key,
				"isOverwrite", isOverwrite)
		}

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
	logger := getGlobalLogger()
	if logger != nil {
		logger.Info("🔍 GetValidatorRewardHistory: 开始查询奖励历史",
			"validatorAddress", validatorAddress,
			"fromEpoch", fromEpoch,
			"toEpoch", toEpoch)
	}

	summary, err := rs.GetRewardSummary(validatorAddress, fromEpoch, toEpoch)
	if err != nil {
		if logger != nil {
			logger.Error("❌ GetValidatorRewardHistory: 查询失败",
				"validatorAddress", validatorAddress,
				"error", err)
		}
		return nil, err
	}

	records := summary.ValidatorRecords
	sort.Slice(records, func(i, j int) bool {
		return records[i].EpochNumber > records[j].EpochNumber
	})

	if logger != nil {
		logger.Info("✅ GetValidatorRewardHistory: 查询完成",
			"validatorAddress", validatorAddress,
			"recordsCount", len(records))
		// 详细记录每个 epoch 的 BlockCount
		for _, record := range records {
			logger.Info("📊 GetValidatorRewardHistory: 奖励记录详情",
				"epoch", record.EpochNumber,
				"recipient", record.Recipient,
				"rewardType", record.RewardType,
				"blockCount", record.BlockCount,
				"amount", record.Amount)
		}
	}

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

	// 添加日志：开始查询
	logger := getGlobalLogger()
	if logger != nil {
		logger.Debug("🔍 [RewardStore.GetEpochRewardDetails] 开始查询", "epochNumber", epochNumber)
	}

	err := rs.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("rewards"))
		if bucket == nil {
			if logger != nil {
				logger.Warn("⚠️ [RewardStore.GetEpochRewardDetails] rewards bucket not found")
			}
			return fmt.Errorf("rewards bucket not found")
		}

		if logger != nil {
			logger.Debug("✅ [RewardStore.GetEpochRewardDetails] rewards bucket found, 开始遍历")
		}

		cursor := bucket.Cursor()
		processedCount := 0
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			processedCount++
			var record RewardRecordExtended
			if err := json.Unmarshal(v, &record); err != nil {
				if logger != nil && processedCount%1000 == 0 {
					logger.Debug("⚠️ [RewardStore.GetEpochRewardDetails] 反序列化失败", "key", string(k), "error", err, "processedCount", processedCount)
				}
				continue
			}

			if record.EpochNumber == epochNumber {
				records = append(records, record)
			}

			// 每处理1000条记录打印一次日志（避免日志过多）
			if logger != nil && processedCount%1000 == 0 {
				logger.Debug("🔄 [RewardStore.GetEpochRewardDetails] 处理中", "processedCount", processedCount, "matchedCount", len(records))
			}
		}

		if logger != nil {
			logger.Info("✅ [RewardStore.GetEpochRewardDetails] 遍历完成", "epochNumber", epochNumber, "totalProcessed", processedCount, "matchedCount", len(records))
		}

		return nil
	})

	if err != nil {
		if logger != nil {
			logger.Error("❌ [RewardStore.GetEpochRewardDetails] 数据库查询失败", "epochNumber", epochNumber, "error", err)
		}
		return nil, err
	}

	// 按金额降序排列
	if logger != nil {
		logger.Debug("🔄 [RewardStore.GetEpochRewardDetails] 开始排序", "recordsCount", len(records))
	}
	sort.Slice(records, func(i, j int) bool {
		amountI, _ := new(big.Int).SetString(records[i].Amount, 10)
		amountJ, _ := new(big.Int).SetString(records[j].Amount, 10)
		return amountI.Cmp(amountJ) > 0
	})

	if logger != nil {
		logger.Debug("✅ [RewardStore.GetEpochRewardDetails] 查询完成", "epochNumber", epochNumber, "recordsCount", len(records))
	}

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

			// 记录读取到的奖励记录信息（特别是 BlockCount）
			logger := getGlobalLogger()
			if logger != nil {
				logger.Info("📖 GetRewardSummary: 读取奖励记录",
					"epoch", record.EpochNumber,
					"recipient", record.Recipient,
					"rewardType", record.RewardType,
					"blockCount", record.BlockCount,
					"amount", record.Amount,
					"key", string(k))
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
