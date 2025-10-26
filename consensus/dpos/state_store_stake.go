package dpos

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
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

// getGlobalLogger 获取全局logger
func getGlobalLogger() hclog.Logger {
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		return dposInstance.logger
	}
	return nil
}

type StakeStore struct {
	db *bolt.DB
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
	}

	for _, bucketName := range buckets {
		if _, err := dbTx.CreateBucketIfNotExists([]byte(bucketName)); err != nil {
			return fmt.Errorf("failed to create bucket %s: %w", bucketName, err)
		}
	}

	return nil
}

// insertFullValidatorSet inserts full validator set to its bucket (or updates it if exists)
// If the passed tx is already open (not nil), it will use it to insert full validator set
// If the passed tx is not open (it is nil), it will open a new transaction on db and insert full validator set
func (s *StakeStore) insertFullValidatorSet(fullValidatorSet validatorSetState, dbTx *bolt.Tx) error {
	insertFn := func(tx *bolt.Tx) error {
		raw, err := fullValidatorSet.Marshal()
		if err != nil {
			return err
		}

		return tx.Bucket(validatorSetBucket).Put(fullValidatorSetKey, raw)
	}

	if dbTx == nil {
		return s.db.Update(func(tx *bolt.Tx) error {
			return insertFn(tx)
		})
	}

	return insertFn(dbTx)
}

// getFullValidatorSet returns full validator set from its bucket if exists
// If the passed tx is already open (not nil), it will use it to get full validator set
// If the passed tx is not open (it is nil), it will open a new transaction on db and get full validator set
func (s *StakeStore) getFullValidatorSet(dbTx *bolt.Tx) (validatorSetState, error) {
	var (
		fullValidatorSet validatorSetState
		err              error
	)

	getFn := func(tx *bolt.Tx) error {
		raw := tx.Bucket(validatorSetBucket).Get(fullValidatorSetKey)
		if raw == nil {
			return errNoFullValidatorSet
		}

		return fullValidatorSet.Unmarshal(raw)
	}

	if dbTx == nil {
		err = s.db.View(func(tx *bolt.Tx) error {
			return getFn(tx)
		})
	} else {
		err = getFn(dbTx)
	}

	return fullValidatorSet, err
}

// 🆕 新增：GetValidators方法，实现与命令一致的数据源
func (s *StakeStore) GetValidators() (validator.AccountSet, error) {
	return s.GetValidatorsWithFilter(true)
}

// 🆕 新增：GetValidatorsWithFilter方法，支持控制是否过滤
func (s *StakeStore) GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error) {
	var validators validator.AccountSet

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
				continue
			}

			// 应用过滤条件
			if filterZeroVotingPower && (delegateInfo.VotingPower == nil || delegateInfo.VotingPower.Cmp(big.NewInt(0)) == 0) {
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
		return nil, fmt.Errorf("failed to get validators from database: %w", err)
	}

	// 🆕 按权重倒序排序，确保权重高的验证者排在前面
	sort.Slice(validators, func(i, j int) bool {
		// 先按权重倒序排序
		weightCmp := validators[i].VotingPower.Cmp(validators[j].VotingPower)
		if weightCmp != 0 {
			return weightCmp > 0 // 权重高的排在前面
		}
		// 如果权重相同，按地址排序（确保排序稳定）
		return validators[i].Address.String() < validators[j].Address.String()
	})

	return validators, nil
}

// 🆕 新增：GetStakingInfo方法，直接从数据库读取，不做修改
func (s *StakeStore) GetStakingInfo() ([]*StakeInfo, error) {
	var stakingInfos []*StakeInfo

	err := s.db.View(func(tx *bolt.Tx) error {
		// 直接从StakingInfo bucket读取数据，不做任何修改
		stakingBucket := tx.Bucket([]byte("StakingInfo"))
		if stakingBucket != nil {
			cursor := stakingBucket.Cursor()
			for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
				var stakeInfo StakeInfo
				if err := json.Unmarshal(value, &stakeInfo); err != nil {
					continue // 跳过解析失败的数据
				}
				stakingInfos = append(stakingInfos, &stakeInfo)
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get staking info: %w", err)
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
		return nil, fmt.Errorf("failed to unmarshal staking info: %w", err)
	}

	return &info, nil
}

// setStakingInfo 保存质押信息到数据库
func (s *StakeStore) setStakingInfo(staker types.Address, info *StakeInfo, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("StakingInfo"))
	if err != nil {
		return fmt.Errorf("failed to create staking info bucket: %w", err)
	}

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal staking info: %w", err)
	}

	if err := bucket.Put(staker[:], data); err != nil {
		return fmt.Errorf("failed to save staking info: %w", err)
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
		return nil, fmt.Errorf("failed to unmarshal delegates: %w", err)
	}

	return delegates, nil
}

// setDelegatesAtBlock 保存指定区块的受托人集合到数据库
func (s *StakeStore) setDelegatesAtBlock(blockNumber uint64, delegates validator.AccountSet, dbTx *bolt.Tx) error {
	// 使用全局logger，通过DPoS实例获取
	logger := getGlobalLogger()
	if logger == nil {
		// 如果没有全局logger，使用默认输出
		fmt.Printf("🔍 setDelegatesAtBlock: 开始存储验证者集合 blockNumber=%d delegatesCount=%d\n", blockNumber, len(delegates))
	} else {
		logger.Debug("🔍 setDelegatesAtBlock: 开始存储验证者集合",
			"blockNumber", blockNumber,
			"delegatesCount", len(delegates))
	}

	// 检查参数
	if dbTx == nil {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 数据库事务为nil")
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 数据库事务为nil\n")
		}
		return fmt.Errorf("database transaction is nil")
	}

	if len(delegates) == 0 {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 验证者集合为空")
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 验证者集合为空\n")
		}
		return fmt.Errorf("delegates set is empty")
	}

	// 创建或获取桶
	if logger != nil {
		logger.Debug("🔍 setDelegatesAtBlock: 步骤1-创建桶", "blockNumber", blockNumber)
	} else {
		fmt.Printf("🔍 setDelegatesAtBlock: 步骤1-创建桶 blockNumber=%d\n", blockNumber)
	}
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("DelegatesAtBlock"))
	if err != nil {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 创建桶失败", "error", err)
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 创建桶失败 error=%v\n", err)
		}
		return fmt.Errorf("failed to create delegates at block bucket: %w", err)
	}
	if logger != nil {
		logger.Debug("✅ setDelegatesAtBlock: 桶创建成功", "blockNumber", blockNumber)
	} else {
		fmt.Printf("✅ setDelegatesAtBlock: 桶创建成功 blockNumber=%d\n", blockNumber)
	}

	// 序列化验证者集合
	if logger != nil {
		logger.Debug("🔍 setDelegatesAtBlock: 步骤2-序列化验证者集合", "blockNumber", blockNumber)
	} else {
		fmt.Printf("🔍 setDelegatesAtBlock: 步骤2-序列化验证者集合 blockNumber=%d\n", blockNumber)
	}
	data, err := json.Marshal(delegates)
	if err != nil {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 序列化失败", "error", err)
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 序列化失败 error=%v\n", err)
		}
		return fmt.Errorf("failed to marshal delegates: %w", err)
	}
	if logger != nil {
		logger.Debug("✅ setDelegatesAtBlock: 序列化成功",
			"blockNumber", blockNumber,
			"dataSize", len(data))
	} else {
		fmt.Printf("✅ setDelegatesAtBlock: 序列化成功 blockNumber=%d dataSize=%d\n", blockNumber, len(data))
	}

	// 创建键
	if logger != nil {
		logger.Debug("🔍 setDelegatesAtBlock: 步骤3-创建键", "blockNumber", blockNumber)
	} else {
		fmt.Printf("🔍 setDelegatesAtBlock: 步骤3-创建键 blockNumber=%d\n", blockNumber)
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNumber)
	if logger != nil {
		logger.Debug("✅ setDelegatesAtBlock: 键创建成功",
			"blockNumber", blockNumber,
			"key", fmt.Sprintf("%x", key))
	} else {
		fmt.Printf("✅ setDelegatesAtBlock: 键创建成功 blockNumber=%d key=%x\n", blockNumber, key)
	}

	// 存储数据
	if logger != nil {
		logger.Debug("🔍 setDelegatesAtBlock: 步骤4-存储数据", "blockNumber", blockNumber)
	} else {
		fmt.Printf("🔍 setDelegatesAtBlock: 步骤4-存储数据 blockNumber=%d\n", blockNumber)
	}
	if err := bucket.Put(key, data); err != nil {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 存储数据失败", "error", err)
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 存储数据失败 error=%v\n", err)
		}
		return fmt.Errorf("failed to save delegates: %w", err)
	}
	if logger != nil {
		logger.Debug("✅ setDelegatesAtBlock: 数据存储成功", "blockNumber", blockNumber)
	} else {
		fmt.Printf("✅ setDelegatesAtBlock: 数据存储成功 blockNumber=%d\n", blockNumber)
	}

	// 验证存储是否成功
	if logger != nil {
		logger.Debug("🔍 setDelegatesAtBlock: 步骤5-验证存储", "blockNumber", blockNumber)
	} else {
		fmt.Printf("🔍 setDelegatesAtBlock: 步骤5-验证存储 blockNumber=%d\n", blockNumber)
	}
	storedData := bucket.Get(key)
	if storedData == nil {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 存储后数据未找到")
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 存储后数据未找到\n")
		}
		return fmt.Errorf("stored data not found after save")
	}
	if len(storedData) != len(data) {
		if logger != nil {
			logger.Error("❌ setDelegatesAtBlock: 存储数据大小不匹配",
				"expectedSize", len(data),
				"actualSize", len(storedData))
		} else {
			fmt.Printf("❌ setDelegatesAtBlock: 存储数据大小不匹配 expectedSize=%d actualSize=%d\n", len(data), len(storedData))
		}
		return fmt.Errorf("stored data size mismatch")
	}
	if logger != nil {
		logger.Debug("✅ setDelegatesAtBlock: 存储验证成功",
			"blockNumber", blockNumber,
			"dataSize", len(storedData))
	} else {
		fmt.Printf("✅ setDelegatesAtBlock: 存储验证成功 blockNumber=%d dataSize=%d\n", blockNumber, len(storedData))
	}

	if logger != nil {
		logger.Debug("✅ setDelegatesAtBlock: 所有步骤完成", "blockNumber", blockNumber)
	} else {
		fmt.Printf("✅ setDelegatesAtBlock: 所有步骤完成 blockNumber=%d\n", blockNumber)
	}
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
		return nil, fmt.Errorf("failed to unmarshal voting power: %w", err)
	}

	return &votingPower, nil
}

// setVotingPowerAtBlock 保存指定区块的投票权重到数据库
func (s *StakeStore) setVotingPowerAtBlock(blockNumber uint64, delegate types.Address, votingPower *big.Int, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("VotingPowerAtBlock"))
	if err != nil {
		return fmt.Errorf("failed to create voting power at block bucket: %w", err)
	}

	data, err := votingPower.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal voting power: %w", err)
	}

	key := make([]byte, 8+20) // blockNumber + address
	binary.BigEndian.PutUint64(key[:8], blockNumber)
	copy(key[8:], delegate[:])

	if err := bucket.Put(key, data); err != nil {
		return fmt.Errorf("failed to save voting power: %w", err)
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
		return nil, fmt.Errorf("failed to unmarshal voter info: %w", err)
	}

	return &info, nil
}

// setVoterInfo 保存投票者信息到数据库
func (s *StakeStore) setVoterInfo(voter types.Address, info *VoterInfo, dbTx *bolt.Tx) error {
	// 如果 dbTx 为 nil，使用自己的数据库连接
	if dbTx == nil {
		return s.db.Update(func(tx *bolt.Tx) error {
			return s.setVoterInfo(voter, info, tx)
		})
	}

	bucket, err := dbTx.CreateBucketIfNotExists([]byte("VoterInfo"))
	if err != nil {
		return fmt.Errorf("failed to create voter info bucket: %w", err)
	}

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal voter info: %w", err)
	}

	if err := bucket.Put(voter[:], data); err != nil {
		return fmt.Errorf("failed to save voter info: %w", err)
	}

	return nil
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
		return nil, fmt.Errorf("failed to unmarshal reward history: %w", err)
	}

	return records, nil
}

// setRewardHistory 保存奖励历史到数据库
func (s *StakeStore) setRewardHistory(staker types.Address, records []*RewardRecord, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("RewardHistory"))
	if err != nil {
		return fmt.Errorf("failed to create reward history bucket: %w", err)
	}

	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("failed to marshal reward history: %w", err)
	}

	if err := bucket.Put(staker[:], data); err != nil {
		return fmt.Errorf("failed to save reward history: %w", err)
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
		return nil, fmt.Errorf("failed to unmarshal epoch rewards: %w", err)
	}

	return &rewards, nil
}

// setEpochRewards 保存周期奖励信息到数据库
func (s *StakeStore) setEpochRewards(epochNumber uint64, rewards *EpochRewards, dbTx *bolt.Tx) error {
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("EpochRewards"))
	if err != nil {
		return fmt.Errorf("failed to create epoch rewards bucket: %w", err)
	}

	data, err := json.Marshal(rewards)
	if err != nil {
		return fmt.Errorf("failed to marshal epoch rewards: %w", err)
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, epochNumber)

	if err := bucket.Put(key, data); err != nil {
		return fmt.Errorf("failed to save epoch rewards: %w", err)
	}

	return nil
}

// cleanup 清理过期数据
func (s *StakeStore) cleanup(currentBlock uint64, dbTx *bolt.Tx) error {
	// 清理超过1000个区块的投票权重数据
	if err := s.cleanupVotingPower(currentBlock, dbTx); err != nil {
		return fmt.Errorf("failed to cleanup voting power: %w", err)
	}

	// 清理超过100个周期的奖励数据
	if err := s.cleanupEpochRewards(currentBlock, dbTx); err != nil {
		return fmt.Errorf("failed to cleanup epoch rewards: %w", err)
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
					return fmt.Errorf("failed to delete voting power record: %w", err)
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
					return fmt.Errorf("failed to delete epoch rewards record: %w", err)
				}
			}
		}
	}

	return nil
}

// setDelegateInfo 保存受托人信息到数据库
func (s *StakeStore) setDelegateInfo(delegate types.Address, info *DelegateInfo, dbTx *bolt.Tx) error {
	// 如果 dbTx 为 nil，使用自己的数据库连接
	if dbTx == nil {
		return s.db.Update(func(tx *bolt.Tx) error {
			return s.setDelegateInfoInternal(delegate, info, tx)
		})
	}

	return s.setDelegateInfoInternal(delegate, info, dbTx)
}

// setDelegateInfoInternal 内部实现，避免递归调用
func (s *StakeStore) setDelegateInfoInternal(delegate types.Address, info *DelegateInfo, dbTx *bolt.Tx) error {
	// 添加调试日志
	// 使用debug级别记录调用信息
	fmt.Printf("🔍 setDelegateInfoInternal called: delegate=%s, dbTx=%v\n", delegate.String(), dbTx != nil)
	fmt.Printf("  - 受托人地址: %s\n", delegate.String())
	fmt.Printf("  - 传入的VotingPower: %s (0x%x)\n", info.VotingPower.String(), info.VotingPower.Bytes())
	fmt.Printf("  - 传入的TotalVotes: %s (0x%x)\n", info.TotalVotes.String(), info.TotalVotes.Bytes())
	fmt.Printf("  - 传入的IsActive: %v\n", info.IsActive)
	fmt.Printf("  - 传入的BlsPublicKey长度: %d\n", len(info.BlsPublicKey))

	bucket, err := dbTx.CreateBucketIfNotExists([]byte("DelegateInfo"))
	if err != nil {
		fmt.Printf("❌ Failed to create bucket: %v\n", err)
		return fmt.Errorf("failed to create delegate info bucket: %w", err)
	}

	data, err := json.Marshal(info)
	if err != nil {
		fmt.Printf("❌ Failed to marshal: %v\n", err)
		return fmt.Errorf("failed to marshal delegate info: %w", err)
	}

	if err := bucket.Put(delegate[:], data); err != nil {
		fmt.Printf("❌ Failed to put data: %v\n", err)
		return fmt.Errorf("failed to save delegate info: %w", err)
	}

	fmt.Printf("✅ setDelegateInfoInternal completed successfully\n")
	fmt.Printf("  - 保存到数据库的VotingPower: %s (0x%x)\n", info.VotingPower.String(), info.VotingPower.Bytes())
	fmt.Printf("  - 保存到数据库的TotalVotes: %s (0x%x)\n", info.TotalVotes.String(), info.TotalVotes.Bytes())
	// fmt.Printf("  - 保存到数据库的IsActive: %v\n", info.IsActive)
	// fmt.Printf("  - 保存到数据库的BlsPublicKey长度: %d\n", len(info.BlsPublicKey))
	return nil
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
		return nil, fmt.Errorf("failed to unmarshal delegate info: %w", err)
	}

	return &info, nil
}

// ValidatorStore represents a store for validator-related data
type ValidatorStore struct {
	db *bolt.DB
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
			fmt.Printf("⚠️ 解析受托人信息失败: %v\n", err)
			continue
		}

		// 🆕 修复：只检查投票权重，必须包含所有有BLS公钥的受托人
		// 这样可以确保与出块时的受托人顺序完全一致
		if delegateInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
			fmt.Printf("🔍 getDelegatesAtBlock: 跳过投票权重为0的受托人 - 地址=%s, votingPower=%s\n",
				delegateInfo.Address.String(), delegateInfo.VotingPower.String())
			continue
		}

		// 恢复BLS公钥
		var blsPublicKey *bls.PublicKey
		if len(delegateInfo.BlsPublicKey) > 0 {
			var err error
			blsPublicKey, err = bls.UnmarshalPublicKey(delegateInfo.BlsPublicKey)
			if err != nil {
				fmt.Printf("⚠️ ValidatorStore: BLS公钥解析失败 - 地址=%s, 错误=%v\n", delegateInfo.Address.String(), err)
				// 即使解析失败也创建对象，但BlsKey为nil
				blsPublicKey = nil
			} else {
				fmt.Printf("✅ ValidatorStore: BLS公钥恢复成功 - 地址=%s, 公钥长度=%d\n", delegateInfo.Address.String(), len(delegateInfo.BlsPublicKey))
			}
		} else {
			fmt.Printf("⚠️ ValidatorStore: 缺少BLS公钥数据 - 地址=%s, 公钥长度=%d\n", delegateInfo.Address.String(), len(delegateInfo.BlsPublicKey))
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
	sort.Slice(delegates, func(i, j int) bool {
		if delegates[i].VotingPower.Cmp(delegates[j].VotingPower) == 0 {
			// 票数相同，按地址排序（字节比较）
			return bytes.Compare(delegates[i].Address[:], delegates[j].Address[:]) < 0
		}
		return delegates[i].VotingPower.Cmp(delegates[j].VotingPower) > 0
	})

	// 🆕 关键修复：应用与出块时相同的DelegateCount限制
	// 确保验证时使用的受托人数量与出块时完全一致
	if delegateCount > 0 {
		maxDelegates := int(delegateCount)
		if len(delegates) > maxDelegates {
			delegates = delegates[:maxDelegates]
			fmt.Printf("🔍 getDelegatesAtBlock: 应用DelegateCount限制 - 原始数量=%d, 限制数量=%d\n",
				len(delegates)+len(delegates[maxDelegates:]), maxDelegates)
		}
	}

	// 🆕 添加详细日志：显示从数据库读取的BLS公钥
	fmt.Printf("🔍 ValidatorStore.getDelegatesAtBlock: 从数据库读取到 %d 个受托人 (区块 %d)\n", len(delegates), blockNumber)
	fmt.Printf("🔍 这是验证区块 %d 时使用的完整受托人集合:\n", blockNumber)
	for i, delegate := range delegates {
		fmt.Printf("  验证索引=%d, 地址=%s, 票数=%s, 有BLS密钥=%v\n",
			i, delegate.Address.String(), delegate.VotingPower.String(), delegate.BlsKey != nil)
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
		return nil, fmt.Errorf("failed to unmarshal delegate info: %w", err)
	}

	// 返回投票权重
	return new(big.Int).Set(delegateInfo.VotingPower), nil
}

// 🆕 新增：更新验证者故障状态
func (s *StakeStore) UpdateValidatorFaultStatus(address types.Address, isFaulty bool, missedBlocks uint64, lastUpdateTime uint64, reason string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// 获取或创建故障状态bucket
		bucket, err := tx.CreateBucketIfNotExists([]byte("validatorFaultStatus"))
		if err != nil {
			return fmt.Errorf("failed to create fault status bucket: %w", err)
		}

		// 创建故障状态信息
		faultInfo := map[string]interface{}{
			"address":        address.String(),
			"isFaulty":       isFaulty,
			"missedBlocks":   missedBlocks,
			"lastUpdateTime": lastUpdateTime,
			"reason":         reason,
		}

		// 序列化并存储
		data, err := json.Marshal(faultInfo)
		if err != nil {
			return fmt.Errorf("failed to marshal fault info: %w", err)
		}

		return bucket.Put(address.Bytes(), data)
	})
}

// 🆕 新增：获取验证者故障状态
func (s *StakeStore) GetValidatorFaultStatus(address types.Address) (map[string]interface{}, error) {
	var faultInfo map[string]interface{}
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("validatorFaultStatus"))
		if bucket == nil {
			return nil // 没有故障记录
		}
		
		data := bucket.Get(address.Bytes())
		if data == nil {
			return nil // 该验证者没有故障记录
		}
		
		return json.Unmarshal(data, &faultInfo)
	})
	
	return faultInfo, err
}

// 🆕 新增：保存Epoch验证者集合
func (s *StakeStore) SaveEpochValidators(validators validator.AccountSet) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// 获取或创建epoch验证者bucket
		bucket, err := tx.CreateBucketIfNotExists([]byte("epochValidators"))
		if err != nil {
			return fmt.Errorf("failed to create epoch validators bucket: %w", err)
		}

		// 序列化验证者集合
		data, err := json.Marshal(validators)
		if err != nil {
			return fmt.Errorf("failed to marshal validators: %w", err)
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
