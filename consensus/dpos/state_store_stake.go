package dpos

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
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

type StakeStore struct {
	db *bolt.DB
}

// initialize creates necessary buckets in DB if they don't already exist
func (s *StakeStore) initialize(dbTx *bolt.Tx) error {
	// 创建必要的bucket
	buckets := []string{
		"StakingInfo",
		"DelegatesAtBlock",
		"VotingPowerAtBlock",
		"VoterInfo",
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
	bucket, err := dbTx.CreateBucketIfNotExists([]byte("DelegatesAtBlock"))
	if err != nil {
		return fmt.Errorf("failed to create delegates at block bucket: %w", err)
	}

	data, err := json.Marshal(delegates)
	if err != nil {
		return fmt.Errorf("failed to marshal delegates: %w", err)
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, blockNumber)

	if err := bucket.Put(key, data); err != nil {
		return fmt.Errorf("failed to save delegates: %w", err)
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
func (s *ValidatorStore) getDelegatesAtBlock(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error) {
	// TODO: 实现从数据库获取指定区块的受托人集合
	return validator.AccountSet{}, nil
}

// getVotingPowerAtBlock returns the voting power of a delegate at a specific block
func (s *ValidatorStore) getVotingPowerAtBlock(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// TODO: 实现从数据库获取指定区块的投票权重
	return big.NewInt(0), nil
}
