package dpos

import (
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
	buckets := []string{
		"StakingInfo",
		"DelegatesAtBlock",
		"VotingPowerAtBlock",
		"VoterInfo",
		"DelegateInfo",
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

func (s *StakeStore) GetValidators() (validator.AccountSet, error) {
	return s.GetValidatorsWithFilter(true)
}

func (s *StakeStore) GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error) {
	var validators validator.AccountSet

	err := s.db.View(func(tx *bolt.Tx) error {
		// 直接从DelegateInfo表读取验证者信息，不做任何处理
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
				BlsKey:      nil,
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

	// 按权重倒序排序，确保权重高的验证者排在前面
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

// ValidatorStore represents a store for validator-related data
type ValidatorStore struct {
	db *bolt.DB
}

// initialize creates necessary buckets in DB if they don't already exist
func (s *ValidatorStore) initialize(tx *bolt.Tx) error {
	// TODO: 实现验证者存储的初始化逻辑
	return nil
}

// 更新验证者故障状态
func (s *StakeStore) UpdateValidatorFaultStatus(address types.Address, isFaulty bool, missedBlocks uint64, lastUpdateTime uint64, lastFaultyEpoch uint64, reason string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// 获取或创建故障状态bucket
		bucket, err := tx.CreateBucketIfNotExists([]byte("validatorFaultStatus"))
		if err != nil {
			return fmt.Errorf("failed to create fault status bucket: %w", err)
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
			return fmt.Errorf("failed to marshal fault info: %w", err)
		}

		return bucket.Put(address.Bytes(), data)
	})
}

// 获取验证者故障状态
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

// 清除验证者故障标志（用于恢复提案执行）
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
			return fmt.Errorf("failed to unmarshal fault info: %w", err)
		}

		// 添加恢复信息
		faultInfo["isFaulty"] = false
		faultInfo["recoveredByProposal"] = proposalID
		faultInfo["recoveredAt"] = time.Now().Format(time.RFC3339)
		faultInfo["missedBlocks"] = 0
		// 清除lastFaultyEpoch（设为0表示已清除故障）
		faultInfo["lastFaultyEpoch"] = 0
		faultInfo["reason"] = fmt.Sprintf("故障已解除（提案ID: %s）", proposalID)
		faultInfo["lastUpdateTime"] = uint64(time.Now().Unix())

		// 保存更新后的记录
		updatedData, err := json.Marshal(faultInfo)
		if err != nil {
			return fmt.Errorf("failed to marshal updated fault info: %w", err)
		}

		return bucket.Put(address.Bytes(), updatedData)
	})
}

// 保存Epoch验证者集合
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

// 获取Epoch验证者集合
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
