package dpos

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
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
func (s *StakeStore) initialize(tx *bolt.Tx) error {
	if _, err := tx.CreateBucketIfNotExists(validatorSetBucket); err != nil {
		return fmt.Errorf("failed to create bucket=%s: %w", string(epochsBucket), err)
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

// getStakingInfo returns staking information for a given staker
func (s *StakeStore) getStakingInfo(staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error) {
	// TODO: 实现从数据库获取质押信息的逻辑
	// 这里需要根据实际的数据库结构来实现
	return &StakeInfo{
		Staker: staker,
		// 其他字段需要从数据库读取
	}, nil
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
