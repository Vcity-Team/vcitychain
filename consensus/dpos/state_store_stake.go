package dpos

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	hclog "github.com/hashicorp/go-hclog"
	bolt "go.etcd.io/bbolt"
)

var (
	// bucket to store full validator set
	validatorSetBucket = []byte("fullValidatorSetBucket")
	// key of the full validator set in bucket
	fullValidatorSetKey = []byte("fullValidatorSet")
	// error returned if full validator set does not exists in db
	errNoFullValidatorSet = errors.New("full validator set not in db")
)

// getGlobalLogger 获取全局logger（已废弃，使用 getGlobalLoggerWrapper 代替）
func getGlobalLogger() hclog.Logger {
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		return dposInstance.logger
	}
	return nil
}

// getGlobalLoggerWrapper 获取全局logger包装器
func getGlobalLoggerWrapper() *loggerWrapper {
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		return newLoggerWrapper(dposInstance.logger)
	}
	return newLoggerWrapper(nil)
}

type StakeStore struct {
	db     *bolt.DB
	logger *loggerWrapper
}

// setLogger 设置logger（用于初始化时设置）
func (s *StakeStore) setLogger(logger *loggerWrapper) {
	s.logger = logger
}

// withTransaction 统一处理数据库事务（写操作）
func (s *StakeStore) withTransaction(dbTx *bolt.Tx, fn func(*bolt.Tx) error) error {
	if dbTx == nil {
		return s.db.Update(fn)
	}
	return fn(dbTx)
}

// withReadTransaction 统一处理只读事务
func (s *StakeStore) withReadTransaction(dbTx *bolt.Tx, fn func(*bolt.Tx) error) error {
	if dbTx == nil {
		return s.db.View(fn)
	}
	return fn(dbTx)
}

// isGenesisValidator 检查给定的地址是否为创世验证者
func (s *StakeStore) isGenesisValidator(address types.Address) bool {
	// 从 DPoS 实例获取创世验证者映射
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		if dposInstance.genesisValidators != nil {
			return dposInstance.genesisValidators[address]
		}

		// 如果映射为空，尝试从创世文件解析
		if dposInstance.config != nil && dposInstance.config.Blockchain != nil {
			genesisHeader, exists := dposInstance.config.Blockchain.GetHeaderByNumber(0)
			if exists && len(genesisHeader.ExtraData) >= 32 {
				// 使用现有的解析函数从 ExtraData 解析创世验证者
				genesisValidators, err := dposInstance.parseValidatorsFromExtraData(genesisHeader.ExtraData)
				if err == nil && len(genesisValidators) > 0 {
					// 检查地址是否在解析出的创世验证者中
					for _, validator := range genesisValidators {
						if validator.Address == address {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// initialize creates necessary buckets in DB if they don't already exist
func (s *StakeStore) initialize(dbTx *bolt.Tx) error {
	// 创建必要的bucket
	buckets := []string{
		"StakingInfo",
		"DelegatesAtBlock",
		"VotingPowerAtBlock",
		"VoterInfo",
		"DelegateInfo", // 🆕 新增：受托人信息存储
		"RewardHistory",
		"EpochRewards",
		"SlashingHistory", // 🆕 新增：削减历史记录
		"DPoSState",       // 🆕 第一层保护：存储 currentEpoch 等状态
	}

	for _, bucketName := range buckets {
		if _, err := dbTx.CreateBucketIfNotExists([]byte(bucketName)); err != nil {
			return WrapErrorf("create bucket", "bucket=%s: %w", bucketName, err)
		}
	}

	return nil
}

// insertFullValidatorSet inserts full validator set to its bucket (or updates it if exists)
// If the passed tx is already open (not nil), it will use it to insert full validator set
// If the passed tx is not open (it is nil), it will open a new transaction on db and insert full validator set
func (s *StakeStore) insertFullValidatorSet(fullValidatorSet validatorSetState, dbTx *bolt.Tx) error {
	return s.withTransaction(dbTx, func(tx *bolt.Tx) error {
		raw, err := fullValidatorSet.Marshal()
		if err != nil {
			return err
		}

		return tx.Bucket(validatorSetBucket).Put(fullValidatorSetKey, raw)
	})
}

// getFullValidatorSet returns full validator set from its bucket if exists
// If the passed tx is already open (not nil), it will use it to get full validator set
// If the passed tx is not open (it is nil), it will open a new transaction on db and get full validator set
func (s *StakeStore) getFullValidatorSet(dbTx *bolt.Tx) (validatorSetState, error) {
	var (
		fullValidatorSet validatorSetState
		err              error
	)

	err = s.withReadTransaction(dbTx, func(tx *bolt.Tx) error {
		raw := tx.Bucket(validatorSetBucket).Get(fullValidatorSetKey)
		if raw == nil {
			return errNoFullValidatorSet
		}

		return fullValidatorSet.Unmarshal(raw)
	})

	return fullValidatorSet, err
}

// 🆕 新增：GetValidators方法，实现与命令一致的数据源
func (s *StakeStore) GetValidators() (validator.AccountSet, error) {
	return s.GetValidatorsWithFilter(true)
}

// 🆕 新增：GetValidatorsWithFilter方法，支持控制是否过滤
func (s *StakeStore) GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error) {
	var validators validator.AccountSet
	// 移除详细日志，减少刷屏，只在错误时输出

	err := s.db.View(func(tx *bolt.Tx) error {
		// 🆕 修改：直接从DelegateInfo表读取验证者信息，不做任何处理
		delegateBucket := tx.Bucket([]byte("DelegateInfo"))
		if delegateBucket == nil {
			return fmt.Errorf("DelegateInfo bucket not found")
		}

		cursor := delegateBucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var delegateInfo DelegateInfo
			if err := json.Unmarshal(value, &delegateInfo); err != nil {
				// 解析失败时静默跳过，不输出日志
				continue
			}

			// 应用过滤条件
			if filterZeroVotingPower && (delegateInfo.VotingPower == nil || delegateInfo.VotingPower.Cmp(big.NewInt(0)) == 0) {
				// 跳过零权重验证者，不输出日志
				continue
			}

			// 直接使用DelegateInfo中的VotingPower，不做任何处理
			validator := &validator.ValidatorMetadata{
				Address:     delegateInfo.Address,
				VotingPower: new(big.Int).Set(delegateInfo.VotingPower),
				IsActive:    delegateInfo.IsActive,
				BlsKey:      nil, // 初始为空
			}

			// 如果有BLS公钥，解析并设置
			if len(delegateInfo.BlsPublicKey) > 0 {
				if blsKey, err := bls.UnmarshalPublicKey(delegateInfo.BlsPublicKey); err == nil {
					validator.BlsKey = blsKey
				}
			}

			validators = append(validators, validator)
		}

		return nil
	})

	if err != nil {
		return nil, WrapError("get validators from database", err)
	}

	// 🆕 按权重倒序排序，确保权重高的验证者排在前面
	sortValidatorsByVotingPower(validators)

	return validators, nil
}

// SaveSlashingHistory 保存削减历史记录
func (s *StakeStore) SaveSlashingHistory(history *SlashingHistory) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("stake store not initialized")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("SlashingHistory"))
		if err != nil {
			return WrapError("create slashing history bucket", err)
		}

		// 使用复合 key: validatorAddr (20 bytes) + blockNumber (8 bytes) = 28 bytes
		key := make([]byte, 28)
		copy(key[0:20], history.ValidatorAddr[:])
		binary.BigEndian.PutUint64(key[20:28], history.BlockNumber)

		data, err := json.Marshal(history)
		if err != nil {
			return WrapError("marshal slashing history", err)
		}

		return bucket.Put(key, data)
	})
}

// GetSlashingHistory 获取验证者的削减历史记录
func (s *StakeStore) GetSlashingHistory(validatorAddr types.Address) ([]*SlashingHistory, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("stake store not initialized")
	}

	var histories []*SlashingHistory
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("SlashingHistory"))
		if bucket == nil {
			return nil // 没有历史记录
		}

		cursor := bucket.Cursor()
		prefix := validatorAddr[:]

		for k, v := cursor.Seek(prefix); k != nil && len(k) >= 20 && bytes.Equal(k[0:20], prefix); k, v = cursor.Next() {
			var history SlashingHistory
			if err := json.Unmarshal(v, &history); err != nil {
				continue // 跳过无效记录
			}
			histories = append(histories, &history)
		}

		return nil
	})

	return histories, err
}

// 🆕 检查是否已执行过指定区块的消减（幂等性检查）
func (s *StakeStore) HasSlashingHistory(validatorAddr types.Address, blockNumber uint64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("stake store not initialized")
	}

	var exists bool
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("SlashingHistory"))
		if bucket == nil {
			return nil // 没有历史记录，返回 false
		}

		// 使用复合 key: validatorAddr (20 bytes) + blockNumber (8 bytes) = 28 bytes
		key := make([]byte, 28)
		copy(key[0:20], validatorAddr[:])
		binary.BigEndian.PutUint64(key[20:28], blockNumber)

		data := bucket.Get(key)
		exists = data != nil
		return nil
	})

	return exists, err
}

// 🆕 第一层保护：保存当前已检测的epoch索引（防止重复检测）
func (s *StakeStore) SaveCurrentEpoch(epochIndex uint64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("stake store not initialized")
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("DPoSState"))
		if err != nil {
			return WrapError("create DPoSState bucket", err)
		}

		key := []byte("currentEpoch")
		value := make([]byte, 8)
		binary.BigEndian.PutUint64(value, epochIndex)

		return bucket.Put(key, value)
	})
}

// 🆕 第一层保护：加载当前已检测的epoch索引（防止重复检测）
func (s *StakeStore) LoadCurrentEpoch() (uint64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("stake store not initialized")
	}

	var epochIndex uint64

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("DPoSState"))
		if bucket == nil {
			return fmt.Errorf("DPoSState bucket not found")
		}

		value := bucket.Get([]byte("currentEpoch"))
		if value == nil {
			return fmt.Errorf("currentEpoch not found in database")
		}

		if len(value) != 8 {
			return fmt.Errorf("invalid currentEpoch value length: expected 8, got %d", len(value))
		}

		epochIndex = binary.BigEndian.Uint64(value)
		return nil
	})

	return epochIndex, err
}

// 🆕 新增：GetStakingInfo方法，直接从数据库读取，不做修改
func (s *StakeStore) GetStakingInfo() ([]*StakeInfo, error) {
	var stakingInfos []*StakeInfo

	if s.db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	err := s.db.View(func(tx *bolt.Tx) error {
		// 直接从StakingInfo bucket读取数据，不做任何修改
		stakingBucket := tx.Bucket([]byte("StakingInfo"))
		if stakingBucket == nil {
			// Bucket 不存在，返回空列表（这是正常的，如果还没有质押数据）
			return nil
		}

		cursor := stakingBucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var stakeInfo StakeInfo
			if err := json.Unmarshal(value, &stakeInfo); err != nil {
				// 跳过解析失败的数据
				continue
			}
			stakingInfos = append(stakingInfos, &stakeInfo)
		}

		return nil
	})

	if err != nil {
		return nil, WrapError("get staking info", err)
	}

	return stakingInfos, nil
}

// getStakingInfo 从数据库获取质押信息
func (s *StakeStore) getStakingInfo(staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error) {
	bucket := dbTx.Bucket([]byte("StakingInfo"))
	if bucket == nil {
		return nil, errors.New("staking info bucket not found")
	}

	data := bucket.Get(staker[:])
	if data == nil {
		return nil, errors.New("staking info not found")
	}

	var info StakeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, WrapError("unmarshal staking info", err)
	}

	return &info, nil
}

// setStakingInfo 保存质押信息到数据库
// 🆕 使用复合 key (staker + delegate + timestamp) 来支持历史记录模式
// 每次投票都会创建独立记录，不会覆盖历史记录
func (s *StakeStore) setStakingInfo(staker types.Address, info *StakeInfo, timestamp uint64, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("StakingInfo"))
	if err != nil {
		return WrapError("create staking info bucket", err)
	}

	data, err := json.Marshal(info)
	if err != nil {
		return WrapError("marshal staking info", err)
	}

	// 🆕 使用统一的复合键构建函数
	// 格式: staker (20 bytes) + delegate (20 bytes) + timestamp (8 bytes) = 48 bytes
	// 这样每次投票都有唯一 key，不会覆盖历史记录
	key := buildStakingCompositeKey(staker, info.Delegate, timestamp)

	if err := bucket.Put(key, data); err != nil {
		return WrapError("save staking info", err)
	}

	return nil
}

// getDelegatesAtBlock 从数据库获取指定区块的受托人集合
func (s *StakeStore) getDelegatesAtBlock(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error) {
	bucket := dbTx.Bucket([]byte("DelegatesAtBlock"))
	if bucket == nil {
		return nil, errors.New("delegates at block bucket not found")
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNumber)

	data := bucket.Get(key)
	if data == nil {
		return nil, errors.New("delegates not found for block")
	}

	var delegates validator.AccountSet
	if err := json.Unmarshal(data, &delegates); err != nil {
		return nil, WrapError("unmarshal delegates", err)
	}

	return delegates, nil
}

// setDelegatesAtBlock 保存指定区块的受托人集合到数据库
func (s *StakeStore) setDelegatesAtBlock(blockNumber uint64, delegates validator.AccountSet, dbTx *bolt.Tx) error {
	// 确保 logger 已初始化
	if s.logger == nil {
		s.logger = getGlobalLoggerWrapper()
	}

	s.logger.Debug("🔍 setDelegatesAtBlock: 开始存储验证者集合",
		"blockNumber", blockNumber,
		"delegatesCount", len(delegates))

	// 检查参数
	if dbTx == nil {
		s.logger.Error("❌ setDelegatesAtBlock: 数据库事务为nil")
		return fmt.Errorf("database transaction is nil")
	}

	if len(delegates) == 0 {
		s.logger.Error("❌ setDelegatesAtBlock: 验证者集合为空")
		return fmt.Errorf("delegates set is empty")
	}

	// 创建或获取桶
	s.logger.Debug("🔍 setDelegatesAtBlock: 步骤1-创建桶", "blockNumber", blockNumber)
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("DelegatesAtBlock"))
	if err != nil {
		s.logger.Error("❌ setDelegatesAtBlock: 创建桶失败", "error", err)
		return WrapError("create delegates at block bucket", err)
	}
	s.logger.Debug("✅ setDelegatesAtBlock: 桶创建成功", "blockNumber", blockNumber)

	// 序列化验证者集合
	s.logger.Debug("🔍 setDelegatesAtBlock: 步骤2-序列化验证者集合", "blockNumber", blockNumber)
	data, err := json.Marshal(delegates)
	if err != nil {
		s.logger.Error("❌ setDelegatesAtBlock: 序列化失败", "error", err)
		return WrapError("marshal delegates", err)
	}
	s.logger.Debug("✅ setDelegatesAtBlock: 序列化成功",
		"blockNumber", blockNumber,
		"dataSize", len(data))

	// 创建键
	s.logger.Debug("🔍 setDelegatesAtBlock: 步骤3-创建键", "blockNumber", blockNumber)
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNumber)
	s.logger.Debug("✅ setDelegatesAtBlock: 键创建成功",
		"blockNumber", blockNumber,
		"key", fmt.Sprintf("%x", key))

	// 存储数据
	s.logger.Debug("🔍 setDelegatesAtBlock: 步骤4-存储数据", "blockNumber", blockNumber)
	if err := bucket.Put(key, data); err != nil {
		s.logger.Error("❌ setDelegatesAtBlock: 存储数据失败", "error", err)
		return WrapError("save delegates", err)
	}
	s.logger.Debug("✅ setDelegatesAtBlock: 数据存储成功", "blockNumber", blockNumber)

	// 验证存储是否成功
	s.logger.Debug("🔍 setDelegatesAtBlock: 步骤5-验证存储", "blockNumber", blockNumber)
	storedData := bucket.Get(key)
	if storedData == nil {
		s.logger.Error("❌ setDelegatesAtBlock: 存储后数据未找到")
		return fmt.Errorf("stored data not found after save")
	}
	if len(storedData) != len(data) {
		s.logger.Error("❌ setDelegatesAtBlock: 存储数据大小不匹配",
			"expectedSize", len(data),
			"actualSize", len(storedData))
		return fmt.Errorf("stored data size mismatch")
	}
	s.logger.Debug("✅ setDelegatesAtBlock: 存储验证成功",
		"blockNumber", blockNumber,
		"dataSize", len(storedData))

	s.logger.Debug("✅ setDelegatesAtBlock: 所有步骤完成", "blockNumber", blockNumber)
	return nil
}

// getVotingPowerAtBlock 从数据库获取指定区块的投票权重
func (s *StakeStore) getVotingPowerAtBlock(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	bucket := dbTx.Bucket([]byte("VotingPowerAtBlock"))
	if bucket == nil {
		return nil, errors.New("voting power at block bucket not found")
	}

	key := make([]byte, 8+20) // blockNumber + address
	binary.BigEndian.PutUint64(key[:8], blockNumber)
	copy(key[8:], delegate[:])

	data := bucket.Get(key)
	if data == nil {
		return big.NewInt(0), nil
	}

	var votingPower big.Int
	if err := votingPower.UnmarshalJSON(data); err != nil {
		return nil, WrapError("unmarshal voting power", err)
	}

	return &votingPower, nil
}

// setVotingPowerAtBlock 保存指定区块的投票权重到数据库
func (s *StakeStore) setVotingPowerAtBlock(blockNumber uint64, delegate types.Address, votingPower *big.Int, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("VotingPowerAtBlock"))
	if err != nil {
		return WrapError("create voting power at block bucket", err)
	}

	data, err := votingPower.MarshalJSON()
	if err != nil {
		return WrapError("marshal voting power", err)
	}

	key := make([]byte, 8+20) // blockNumber + address
	binary.BigEndian.PutUint64(key[:8], blockNumber)
	copy(key[8:], delegate[:])

	if err := bucket.Put(key, data); err != nil {
		return WrapError("save voting power", err)
	}

	return nil
}

// getVoterInfo 从数据库获取投票者信息
func (s *StakeStore) getVoterInfo(voter types.Address, dbTx *bolt.Tx) (*VoterInfo, error) {
	bucket := dbTx.Bucket([]byte("VoterInfo"))
	if bucket == nil {
		return nil, errors.New("voter info bucket not found")
	}

	data := bucket.Get(voter[:])
	if data == nil {
		return nil, errors.New("voter info not found")
	}

	var info VoterInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, WrapError("unmarshal voter info", err)
	}

	return &info, nil
}

// setVoterInfo 保存投票者信息到数据库
func (s *StakeStore) setVoterInfo(voter types.Address, info *VoterInfo, dbTx *bolt.Tx) error {
	return s.withTransaction(dbTx, func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("VoterInfo"))
		if err != nil {
			return WrapError("create voter info bucket", err)
		}

		data, err := json.Marshal(info)
		if err != nil {
			return WrapError("marshal voter info", err)
		}

		if err := bucket.Put(voter[:], data); err != nil {
			return WrapError("save voter info", err)
		}

		return nil
	})
}

// GetVoterInfo 公开方法：从数据库获取投票者信息（自动管理事务）
func (s *StakeStore) GetVoterInfo(voter types.Address) (*VoterInfo, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("stake store not initialized")
	}

	var voterInfo *VoterInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		info, err := s.getVoterInfo(voter, tx)
		if err != nil {
			return err
		}
		voterInfo = info
		return nil
	})

	if err != nil {
		// 如果未找到，返回 nil 而不是错误
		if err.Error() == "voter info not found" {
			return nil, nil
		}
		return nil, err
	}

	// 初始化 DelegateVotes 和 SlashingRecords（如果不存在）
	if voterInfo != nil {
		if voterInfo.DelegateVotes == nil {
			voterInfo.DelegateVotes = make(map[types.Address]*big.Int)
		}
		if voterInfo.SlashingRecords == nil {
			voterInfo.SlashingRecords = make(map[types.Address][]*SlashingRecord)
		}
	}

	return voterInfo, nil
}

// getRewardHistory 从数据库获取奖励历史
func (s *StakeStore) getRewardHistory(staker types.Address, dbTx *bolt.Tx) ([]*RewardRecord, error) {
	bucket := dbTx.Bucket([]byte("RewardHistory"))
	if bucket == nil {
		return nil, errors.New("reward history bucket not found")
	}

	data := bucket.Get(staker[:])
	if data == nil {
		return []*RewardRecord{}, nil
	}

	var records []*RewardRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, WrapError("unmarshal reward history", err)
	}

	return records, nil
}

// setRewardHistory 保存奖励历史到数据库
func (s *StakeStore) setRewardHistory(staker types.Address, records []*RewardRecord, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("RewardHistory"))
	if err != nil {
		return WrapError("create reward history bucket", err)
	}

	data, err := json.Marshal(records)
	if err != nil {
		return WrapError("marshal reward history", err)
	}

	if err := bucket.Put(staker[:], data); err != nil {
		return WrapError("save reward history", err)
	}

	return nil
}

// addRewardRecord 添加奖励记录
func (s *StakeStore) addRewardRecord(staker types.Address, record *RewardRecord, dbTx *bolt.Tx) error {
	records, err := s.getRewardHistory(staker, dbTx)
	if err != nil {
		return err
	}

	records = append(records, record)

	// 只保留最近100条记录
	if len(records) > 100 {
		records = records[len(records)-100:]
	}

	return s.setRewardHistory(staker, records, dbTx)
}

// getEpochRewards 从数据库获取周期奖励信息
func (s *StakeStore) getEpochRewards(epochNumber uint64, dbTx *bolt.Tx) (*EpochRewards, error) {
	bucket := dbTx.Bucket([]byte("EpochRewards"))
	if bucket == nil {
		return nil, errors.New("epoch rewards bucket not found")
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, epochNumber)

	data := bucket.Get(key)
	if data == nil {
		return nil, errors.New("epoch rewards not found")
	}

	var rewards EpochRewards
	if err := json.Unmarshal(data, &rewards); err != nil {
		return nil, WrapError("unmarshal epoch rewards", err)
	}

	return &rewards, nil
}

// setEpochRewards 保存周期奖励信息到数据库
func (s *StakeStore) setEpochRewards(epochNumber uint64, rewards *EpochRewards, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("EpochRewards"))
	if err != nil {
		return WrapError("create epoch rewards bucket", err)
	}

	data, err := json.Marshal(rewards)
	if err != nil {
		return WrapError("marshal epoch rewards", err)
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, epochNumber)

	if err := bucket.Put(key, data); err != nil {
		return WrapError("save epoch rewards", err)
	}

	return nil
}

// cleanup 清理过期数据
func (s *StakeStore) cleanup(currentBlock uint64, dbTx *bolt.Tx) error {
	// 清理超过1000个区块的投票权重数据
	if err := s.cleanupVotingPower(currentBlock, dbTx); err != nil {
		return WrapError("cleanup voting power", err)
	}

	// 清理超过100个周期的奖励数据
	if err := s.cleanupEpochRewards(currentBlock, dbTx); err != nil {
		return WrapError("cleanup epoch rewards", err)
	}

	return nil
}

// cleanupVotingPower 清理过期的投票权重数据
func (s *StakeStore) cleanupVotingPower(currentBlock uint64, dbTx *bolt.Tx) error {
	bucket := dbTx.Bucket([]byte("VotingPowerAtBlock"))
	if bucket == nil {
		return nil
	}

	cursor := bucket.Cursor()
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		if len(k) >= 8 {
			blockNumber := binary.BigEndian.Uint64(k[:8])
			if currentBlock-blockNumber > 1000 {
				if err := cursor.Delete(); err != nil {
					return WrapError("delete voting power record", err)
				}
			}
		}
	}

	return nil
}

// cleanupEpochRewards 清理过期的周期奖励数据
func (s *StakeStore) cleanupEpochRewards(currentBlock uint64, dbTx *bolt.Tx) error {
	bucket := dbTx.Bucket([]byte("EpochRewards"))
	if bucket == nil {
		return nil
	}

	currentEpoch := currentBlock / 100 // 假设每100个区块为一个周期

	cursor := bucket.Cursor()
	for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
		if len(k) >= 8 {
			epochNumber := binary.BigEndian.Uint64(k)
			if currentEpoch-epochNumber > 100 {
				if err := cursor.Delete(); err != nil {
					return WrapError("delete epoch rewards record", err)
				}
			}
		}
	}

	return nil
}

// setDelegateInfo 保存受托人信息到数据库
func (s *StakeStore) setDelegateInfo(delegate types.Address, info *DelegateInfo, dbTx *bolt.Tx) error {
	return s.withTransaction(dbTx, func(tx *bolt.Tx) error {
		return s.setDelegateInfoInternal(delegate, info, tx)
	})
}

// setDelegateInfoInternal 内部实现，避免递归调用
// 🆕 修复：保存前自动合并现有数据，防止统计字段丢失
func (s *StakeStore) setDelegateInfoInternal(delegate types.Address, info *DelegateInfo, dbTx *bolt.Tx) error {
	// 确保 logger 已初始化
	if s.logger == nil {
		s.logger = getGlobalLoggerWrapper()
	}

	s.logger.Debug("setDelegateInfoInternal called",
		"delegate", delegate.String(),
		"dbTx", dbTx != nil,
		"votingPower", info.VotingPower.String(),
		"totalVotes", info.TotalVotes.String(),
		"isActive", info.IsActive,
		"blsKeyLength", len(info.BlsPublicKey))

	bucket, err := dbTx.CreateBucketIfNotExists([]byte("DelegateInfo"))
	if err != nil {
		s.logger.Error("Failed to create bucket", "error", err)
		return WrapError("create delegate info bucket", err)
	}

	// 🆕 修复：先尝试读取现有记录，合并更新以保留统计字段
	existingInfo, err := s.getDelegateInfo(delegate, dbTx)
	if err == nil && existingInfo != nil {
		// 合并更新：保留统计字段和其他重要字段
		// 1. 保留统计字段（如果新数据为0，使用现有值）
		if info.ProducedBlocks == 0 && existingInfo.ProducedBlocks > 0 {
			info.ProducedBlocks = existingInfo.ProducedBlocks
			s.logger.Debug("保留现有 ProducedBlocks", "value", info.ProducedBlocks)
		}
		if info.MissedBlocks == 0 && existingInfo.MissedBlocks > 0 {
			info.MissedBlocks = existingInfo.MissedBlocks
			s.logger.Debug("保留现有 MissedBlocks", "value", info.MissedBlocks)
		}
		if info.LastBlockTime == 0 && existingInfo.LastBlockTime > 0 {
			info.LastBlockTime = existingInfo.LastBlockTime
			s.logger.Debug("保留现有 LastBlockTime", "value", info.LastBlockTime)
		}

		// 2. 保留注册信息（如果新数据未设置，使用现有值）
		if !info.IsRegistered && existingInfo.IsRegistered {
			info.IsRegistered = existingInfo.IsRegistered
		}
		if info.RegistrationInfo == nil && existingInfo.RegistrationInfo != nil {
			info.RegistrationInfo = existingInfo.RegistrationInfo
		}

		// 3. 保留BLS公钥（如果新数据为空，使用现有值）
		if len(info.BlsPublicKey) == 0 && len(existingInfo.BlsPublicKey) > 0 {
			info.BlsPublicKey = existingInfo.BlsPublicKey
			s.logger.Debug("保留现有 BlsPublicKey", "length", len(info.BlsPublicKey))
		}

		// 4. 保留佣金信息（如果新数据为0，使用现有值）
		if info.CommissionRate == 0 && existingInfo.CommissionRate > 0 {
			info.CommissionRate = existingInfo.CommissionRate
		}
		if info.PendingCommissionRate == 0 && existingInfo.PendingCommissionRate > 0 {
			info.PendingCommissionRate = existingInfo.PendingCommissionRate
		}
		if info.CommissionUpdateTime == 0 && existingInfo.CommissionUpdateTime > 0 {
			info.CommissionUpdateTime = existingInfo.CommissionUpdateTime
		}

		s.logger.Debug("合并现有数据完成",
			"producedBlocks", info.ProducedBlocks,
			"missedBlocks", info.MissedBlocks,
			"lastBlockTime", info.LastBlockTime)
	} else if err != nil {
		// 读取失败但记录不存在是正常的（新创建记录），只记录非"not found"错误
		if err.Error() != "delegate info not found" {
			s.logger.Debug("读取现有记录失败（可能是新记录）", "error", err)
		}
	}

	data, err := json.Marshal(info)
	if err != nil {
		s.logger.Error("Failed to marshal", "error", err)
		return WrapError("marshal delegate info", err)
	}

	if err := bucket.Put(delegate[:], data); err != nil {
		s.logger.Error("Failed to put data", "error", err)
		return WrapError("save delegate info", err)
	}

	s.logger.Debug("setDelegateInfoInternal completed successfully",
		"delegate", delegate.String(),
		"votingPower", info.VotingPower.String(),
		"totalVotes", info.TotalVotes.String(),
		"producedBlocks", info.ProducedBlocks,
		"missedBlocks", info.MissedBlocks)
	return nil
}

// GetDelegateInfo 对外提供获取受托人信息的便捷方法（自动管理事务）
func (s *StakeStore) GetDelegateInfo(delegate types.Address) (*DelegateInfo, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("stake store not initialized")
	}

	var result *DelegateInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("DelegateInfo"))
		if bucket == nil {
			return nil
		}

		data := bucket.Get(delegate[:])
		if data == nil {
			return nil
		}

		var info DelegateInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return WrapError("unmarshal delegate info", err)
		}

		result = &info
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

// getDelegateInfo 从数据库获取受托人信息
func (s *StakeStore) getDelegateInfo(delegate types.Address, dbTx *bolt.Tx) (*DelegateInfo, error) {
	bucket := dbTx.Bucket([]byte("DelegateInfo"))
	if bucket == nil {
		return nil, errors.New("delegate info bucket not found")
	}

	data := bucket.Get(delegate[:])
	if data == nil {
		return nil, errors.New("delegate info not found")
	}

	var info DelegateInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, WrapError("unmarshal delegate info", err)
	}

	return &info, nil
}

// ValidatorStore represents a store for validator-related data
type ValidatorStore struct {
	db     *bolt.DB
	logger *loggerWrapper
}

// initialize creates necessary buckets in DB if they don't already exist
func (s *ValidatorStore) initialize(tx *bolt.Tx) error {
	// TODO: 实现验证者存储的初始化逻辑
	return nil
}

// getDelegatesAtBlock returns the delegate set at a specific block
func (s *ValidatorStore) getDelegatesAtBlock(blockNumber uint64, dbTx *bolt.Tx, delegateCount uint64) (validator.AccountSet, error) {
	// 🆕 实现从数据库获取指定区块的受托人集合
	if dbTx == nil {
		return validator.AccountSet{}, fmt.Errorf("database transaction is required")
	}

	// 从 DelegateInfo bucket 获取所有受托人信息
	delegateBucket := dbTx.Bucket([]byte("DelegateInfo"))
	if delegateBucket == nil {
		// 如果 bucket 不存在，返回空集合
		return validator.AccountSet{}, nil
	}

	var delegates validator.AccountSet
	cursor := delegateBucket.Cursor()

	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		if len(k) != 20 { // 地址长度应该是20字节
			continue
		}

		var delegateInfo DelegateInfo
		if err := json.Unmarshal(v, &delegateInfo); err != nil {
			// 记录错误但继续处理其他受托人
			s.logger.Warn("解析受托人信息失败", "error", err)
			continue
		}

		// 🆕 修复：只检查投票权重，必须包含所有有BLS公钥的受托人
		// 这样可以确保与出块时的受托人顺序完全一致
		if delegateInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
			s.logger.Debug("getDelegatesAtBlock: 跳过投票权重为0的受托人", "address", delegateInfo.Address.String(), "votingPower", delegateInfo.VotingPower.String())
			continue
		}

		// 恢复BLS公钥
		var blsPublicKey *bls.PublicKey
		if len(delegateInfo.BlsPublicKey) > 0 {
			var err error
			blsPublicKey, err = bls.UnmarshalPublicKey(delegateInfo.BlsPublicKey)
			if err != nil {
				s.logger.Warn("ValidatorStore: BLS公钥解析失败", "address", delegateInfo.Address.String(), "error", err)
				// 即使解析失败也创建对象，但BlsKey为nil
				blsPublicKey = nil
			} else {
				s.logger.Debug("ValidatorStore: BLS公钥恢复成功", "address", delegateInfo.Address.String(), "keyLength", len(delegateInfo.BlsPublicKey))
			}
		} else {
			s.logger.Warn("ValidatorStore: 缺少BLS公钥数据", "address", delegateInfo.Address.String(), "keyLength", len(delegateInfo.BlsPublicKey))
		}

		// 🆕 修复：总是创建验证者元数据，保持索引一致性
		validatorMeta := &validator.ValidatorMetadata{
			Address:     delegateInfo.Address,
			VotingPower: new(big.Int).Set(delegateInfo.VotingPower),
			IsActive:    delegateInfo.IsActive,
			BlsKey:      blsPublicKey, // 可能为nil
		}

		// 验证者元数据创建完成

		delegates = append(delegates, validatorMeta)
	}

	// 🆕 修复：按票数降序排序，如果票数相同则按地址排序，确保与出块时的顺序完全一致
	// 这样可以避免BLS公钥顺序不一致导致的签名验证失败
	sortValidatorsByVotingPower(delegates)

	// 🆕 关键修复：应用与出块时相同的DelegateCount限制
	// 确保验证时使用的受托人数量与出块时完全一致
	if delegateCount > 0 {
		maxDelegates := int(delegateCount)
		if len(delegates) > maxDelegates {
			delegates = delegates[:maxDelegates]
			s.logger.Debug("getDelegatesAtBlock: 应用DelegateCount限制", "originalCount", len(delegates)+len(delegates[maxDelegates:]), "maxDelegates", maxDelegates)
		}
	}

	// 🆕 添加详细日志：显示从数据库读取的BLS公钥
	s.logger.Debug("ValidatorStore.getDelegatesAtBlock: 从数据库读取到受托人", "count", len(delegates), "blockNumber", blockNumber)
	s.logger.Debug("这是验证区块时使用的完整受托人集合", "blockNumber", blockNumber)
	for i, delegate := range delegates {
		s.logger.Debug("验证者详情", "index", i, "address", delegate.Address.String(), "votingPower", delegate.VotingPower.String(), "hasBlsKey", delegate.BlsKey != nil)
	}

	// 排序完成

	return delegates, nil
}

// getVotingPowerAtBlock returns the voting power of a delegate at a specific block
func (s *ValidatorStore) getVotingPowerAtBlock(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// 🆕 实现从数据库获取指定区块的投票权重
	if dbTx == nil {
		return nil, fmt.Errorf("database transaction is required")
	}

	// 从 DelegateInfo bucket 获取受托人信息
	delegateBucket := dbTx.Bucket([]byte("DelegateInfo"))
	if delegateBucket == nil {
		return nil, fmt.Errorf("delegate info bucket not found")
	}

	// 获取受托人信息
	data := delegateBucket.Get(delegate[:])
	if data == nil {
		return nil, fmt.Errorf("delegate info not found for address %s", delegate.String())
	}

	var delegateInfo DelegateInfo
	if err := json.Unmarshal(data, &delegateInfo); err != nil {
		return nil, WrapError("unmarshal delegate info", err)
	}

	// 返回投票权重
	return new(big.Int).Set(delegateInfo.VotingPower), nil
}

// 🆕 新增：更新验证者故障状态
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

// 🆕 新增：获取验证者故障状态
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

// 🆕 清除验证者故障标志（用于恢复提案执行）
func (s *StakeStore) ClearValidatorFaultStatus(address types.Address, proposalID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("validatorFaultStatus"))
		if bucket == nil {
			return nil // 没有故障记录，直接返回成功
		}

		// 读取现有记录
		data := bucket.Get(address.Bytes())
		if data == nil {
			return nil // 该验证者没有故障记录，直接返回成功
		}

		var faultInfo map[string]interface{}
		if err := json.Unmarshal(data, &faultInfo); err != nil {
			return WrapError("unmarshal fault info", err)
		}

		// 🆕 添加恢复信息
		faultInfo["isFaulty"] = false
		faultInfo["recoveredByProposal"] = proposalID
		faultInfo["recoveredAt"] = time.Now().Format(time.RFC3339)
		faultInfo["missedBlocks"] = 0
		// 清除lastFaultyEpoch（设为0表示已清除故障）
		faultInfo["lastFaultyEpoch"] = 0
		faultInfo["reason"] = fmt.Sprintf("Fault cleared (Proposal ID: %s)", proposalID)
		faultInfo["lastUpdateTime"] = uint64(time.Now().Unix())

		// 保存更新后的记录
		updatedData, err := json.Marshal(faultInfo)
		if err != nil {
			return WrapError("marshal updated fault info", err)
		}

		return bucket.Put(address.Bytes(), updatedData)
	})
}

// 🆕 新增：保存Epoch验证者集合
func (s *StakeStore) SaveEpochValidators(validators validator.AccountSet) error {
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

		// 使用当前时间戳作为key
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, uint64(time.Now().Unix()))

		return bucket.Put(key, data)
	})
}

// 🆕 新增：获取Epoch验证者集合
func (s *StakeStore) GetEpochValidators() (validator.AccountSet, error) {
	var validators validator.AccountSet

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("epochValidators"))
		if bucket == nil {
			return fmt.Errorf("epoch validators bucket not found")
		}

		// 获取最新的验证者集合（按时间戳倒序）
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
