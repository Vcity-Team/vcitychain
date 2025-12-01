package dpos

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	bolt "go.etcd.io/bbolt"
)

// ValidatorStore represents a store for validator-related data
type ValidatorStore struct {
	db       *bolt.DB
	logger   *loggerWrapper
	dbHelper *dbHelper // 🆕 添加 dbHelper
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

	// 使用 dbHelper 统一处理遍历和反序列化
	if s.dbHelper == nil {
		return validator.AccountSet{}, errors.New("dbHelper not initialized")
	}

	var delegates validator.AccountSet
	err := s.dbHelper.forEachInBucketWithUnmarshal(dbTx, "DelegateInfo", func() interface{} {
		return &DelegateInfo{}
	}, func(key, item interface{}) error {
		k := key.([]byte)
		if len(k) != 20 { // 地址长度应该是20字节
			return nil // 跳过
		}

		delegateInfo := item.(*DelegateInfo)

		// 🆕 修复：只检查投票权重，必须包含所有有BLS公钥的受托人
		// 这样可以确保与出块时的受托人顺序完全一致
		// 🆕 使用统一的零值检查函数
		if isNonPositive(delegateInfo.VotingPower) {
			s.logger.Debug("getDelegatesAtBlock: 跳过投票权重为0的受托人", "address", delegateInfo.Address.String(), "votingPower", delegateInfo.VotingPower.String())
			return nil // 跳过
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
		return nil
	})
	if err != nil {
		return validator.AccountSet{}, WrapError("get delegates from database", err)
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

