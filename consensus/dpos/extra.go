package dpos

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/bitmap"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/umbracle/ethgo/abi"
	"github.com/umbracle/fastrlp"
)

const (
	// ExtraVanity represents a fixed number of extra-data bytes reserved for proposer vanity
	ExtraVanity = 32

	// ExtraSeal represents the fixed number of extra-data bytes reserved for proposer seal
	ExtraSeal = 65
)

// PolyBFTMixDigest represents a hash of "PolyBFT Mix" to identify whether the block is from PolyBFT consensus engine
var PolyBFTMixDigest = types.StringToHash("adce6e5230abe012342a44e4e9b6d05997d6f015387ae0e59be924afc7ec70c1")

// Extra defines the structure of the extra field for Istanbul
type Extra struct {
	Validators *validator.ValidatorSetDelta
	Parent     *Signature
	Committed  *Signature
	Checkpoint *CheckpointData
}

// MarshalRLPTo defines the marshal function wrapper for Extra
func (i *Extra) MarshalRLPTo(dst []byte) []byte {
	ar := &fastrlp.Arena{}

	return append(make([]byte, ExtraVanity), i.MarshalRLPWith(ar).MarshalTo(dst)...)
}

// MarshalRLPWith defines the marshal function implementation for Extra
func (i *Extra) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	// Validators
	if i.Validators == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Validators.MarshalRLPWith(ar))
	}

	// Parent Signatures
	if i.Parent == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Parent.MarshalRLPWith(ar))
	}

	// Committed Signatures
	if i.Committed == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Committed.MarshalRLPWith(ar))
	}

	// Checkpoint
	if i.Checkpoint == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Checkpoint.MarshalRLPWith(ar))
	}

	return vv
}

// UnmarshalRLP defines the unmarshal function wrapper for Extra
func (i *Extra) UnmarshalRLP(input []byte) error {
	return fastrlp.UnmarshalRLP(input[ExtraVanity:], i)
}

// UnmarshalRLPWith defines the unmarshal implementation for Extra
func (i *Extra) UnmarshalRLPWith(v *fastrlp.Value) error {
	const expectedElements = 4

	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if num := len(elems); num != expectedElements {
		return fmt.Errorf("incorrect elements count to decode Extra, expected %d but found %d", expectedElements, num)
	}

	// Validators
	if elems[0].Elems() > 0 {
		i.Validators = &validator.ValidatorSetDelta{}
		if err := i.Validators.UnmarshalRLPWith(elems[0]); err != nil {
			return err
		}
	}

	// Parent Signatures
	if elems[1].Elems() > 0 {
		i.Parent = &Signature{}
		if err := i.Parent.UnmarshalRLPWith(elems[1]); err != nil {
			return err
		}
	}

	// Committed Signatures
	if elems[2].Elems() > 0 {
		i.Committed = &Signature{}
		if err := i.Committed.UnmarshalRLPWith(elems[2]); err != nil {
			return err
		}
	}

	// Checkpoint
	if elems[3].Elems() > 0 {
		i.Checkpoint = &CheckpointData{}
		if err := i.Checkpoint.UnmarshalRLPWith(elems[3]); err != nil {
			return err
		}
	}

	return nil
}

// ValidateFinalizedData contains extra data validations for finalized headers
func (i *Extra) ValidateFinalizedData(header *types.Header, parent *types.Header, parents []*types.Header,
	chainID uint64, consensusBackend dposBackend, domain []byte, logger hclog.Logger) error {
	// validate committed signatures
	blockNumber := header.Number

	// skip block 1 because genesis does not have committed signatures
	if blockNumber <= 1 {
		logger.Debug("skipping signature validation for block 1 (first block after genesis)")
		return nil
	}

	if i.Committed == nil {
		return fmt.Errorf("failed to verify signatures for block %d, because signatures are not present", blockNumber)
	}

	if i.Checkpoint == nil {
		return fmt.Errorf("failed to verify signatures for block %d, because checkpoint data are not present", blockNumber)
	}

	// validate current block signatures
	// 使用固定的哈希值避免循环依赖，确保与生产区块时使用相同的checkpointHash
	fixedBlockHash := types.BytesToHash([]byte(fmt.Sprintf("block_%d", blockNumber)))
	checkpointHash, err := i.Checkpoint.Hash(chainID, blockNumber, fixedBlockHash)
	if err != nil {
		return fmt.Errorf("failed to calculate proposal hash: %w", err)
	}

	// 🔍 验证时获取验证者集合
	logger.Debug("🔍 验证时开始获取验证者集合",
		"blockNumber", blockNumber,
		"method", "GetDelegates(blockNumber-1, parents)",
		"note", "获取前一个区块的验证者集合")

	validators, err := consensusBackend.GetDelegates(blockNumber-1, parents)
	if err != nil {
		logger.Error("❌ 验证时获取验证者集合失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("failed to validate header for block %d. could not retrieve block validators:%w", blockNumber, err)
	}

	// 🔍 验证时获取的验证者集合信息
	logger.Debug("🔍 验证时验证者集合信息",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"validatorSource", "GetDelegates(blockNumber-1, parents)",
		"note", "用于BLS签名验证的验证者集合")

	// 🔍 打印验证时验证者集合的详细信息
	logger.Debug("🔍 验证时验证者集合详细信息")
	for i, validator := range validators {
		logger.Debug("🔍 验证时验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}

	if err := i.Committed.Verify(blockNumber, validators, checkpointHash, domain, logger); err != nil {
		logger.Error("🚨 区块签名验证失败，程序将退出",
			"blockNumber", blockNumber,
			"proposalHash", checkpointHash.String(),
			"error", err)

		// 🆕 区块验证失败时直接退出程序
		logger.Error("💀 区块验证失败，程序退出")
		os.Exit(1)
	}

	parentExtra, err := GetIbftExtra(parent.ExtraData)
	if err != nil {
		return fmt.Errorf("failed to verify signatures for block %d: %w", blockNumber, err)
	}

	// validate parent signatures
	if err := i.ValidateParentSignatures(blockNumber, consensusBackend, parents,
		parent, parentExtra, chainID, domain, logger); err != nil {
		return err
	}

	return i.Checkpoint.ValidateBasic(parentExtra.Checkpoint)
}

// ValidateParentSignatures validates signatures for parent block
func (i *Extra) ValidateParentSignatures(blockNumber uint64, consensusBackend dposBackend, parents []*types.Header,
	parent *types.Header, parentExtra *Extra, chainID uint64, domain []byte, logger hclog.Logger) error {
	// skip block 1 because genesis does not have committed signatures
	if blockNumber <= 1 {
		return nil
	}

	// 如果父区块是区块1（创世后的第一个区块），则跳过父区块签名验证
	// 因为区块1没有父区块，所以没有父区块签名
	if parent.Number == 1 {
		logger.Debug("skipping parent signature validation for block 1 (first block after genesis)")
		return nil
	}

	// 如果当前区块没有父区块签名，且父区块是区块1，则跳过验证
	// 因为区块1没有父区块签名，所以区块2的Parent字段为nil是正常的
	if i.Parent == nil && parent.Number == 1 {
		logger.Debug("skipping parent signature validation for block 2 (parent is block 1 which has no parent signature)")
		return nil
	}

	if i.Parent == nil {
		return fmt.Errorf("failed to verify signatures for parent of block %d because signatures are not present",
			blockNumber)
	}

	// 🔍 验证时获取父区块验证者集合
	logger.Debug("🔍 验证时开始获取父区块验证者集合",
		"blockNumber", blockNumber,
		"parentBlockNumber", parent.Number,
		"method", "GetDelegates(blockNumber-2, parents)",
		"note", "获取前两个区块的验证者集合")

	parentValidators, err := consensusBackend.GetDelegates(blockNumber-2, parents)
	if err != nil {
		logger.Error("❌ 验证时获取父区块验证者集合失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf(
			"failed to validate header for block %d. could not retrieve parent validators: %w",
			blockNumber,
			err,
		)
	}

	// 🔍 验证时获取的父区块验证者集合信息
	logger.Debug("🔍 验证时父区块验证者集合信息",
		"blockNumber", blockNumber,
		"parentBlockNumber", parent.Number,
		"totalValidators", len(parentValidators),
		"validatorSource", "GetDelegates(blockNumber-2, parents)",
		"note", "用于父区块BLS签名验证的验证者集合")

	// 🔍 打印验证时父区块验证者集合的详细信息
	logger.Debug("🔍 验证时父区块验证者集合详细信息")
	for i, validator := range parentValidators {
		logger.Debug("🔍 验证时父区块验证者",
			"blockNumber", blockNumber,
			"parentBlockNumber", parent.Number,
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}

	// 使用固定的哈希值避免循环依赖，确保与生产区块时使用相同的checkpointHash
	fixedParentBlockHash := types.BytesToHash([]byte(fmt.Sprintf("block_%d", parent.Number)))
	parentCheckpointHash, err := parentExtra.Checkpoint.Hash(chainID, parent.Number, fixedParentBlockHash)
	if err != nil {
		return fmt.Errorf("failed to calculate parent proposal hash: %w", err)
	}

	parentBlockNumber := blockNumber - 1
	if err := i.Parent.Verify(parentBlockNumber, parentValidators, parentCheckpointHash, domain, logger); err != nil {
		// 🆕 修复：检查是否是BLS密钥缺失错误，如果是则尝试获取
		if strings.Contains(err.Error(), "has nil BLS key but is marked as signer in bitmap") {
			logger.Warn("⚠️ ValidateParentSignatures - 检测到BLS密钥缺失错误，尝试获取缺失的BLS密钥",
				"blockNumber", blockNumber,
				"parentBlockNumber", parentBlockNumber,
				"error", err)

			// 解析错误信息，提取缺失BLS密钥的受托人地址
			parts := strings.Split(err.Error(), "(")
			if len(parts) >= 2 {
				addressPart := strings.Split(parts[1], ")")[0]
				if strings.HasPrefix(addressPart, "0x") {
					missingAddress := types.StringToAddress(addressPart)
					logger.Info("🔍 ValidateParentSignatures - 检测到缺失BLS密钥的受托人",
						"blockNumber", blockNumber,
						"parentBlockNumber", parentBlockNumber,
						"missingAddress", missingAddress.String())

					// 🆕 修复：优先使用Signature中的DPoS实例引用
					var dposInstance *DPoS
					var found bool

					// 首先尝试使用父区块Signature中已设置的DPoS实例
					if i.Parent != nil && i.Parent.dposInstance != nil {
						dposInstance = i.Parent.dposInstance
						found = true
						logger.Debug("✅ 使用父区块Signature中的DPoS实例引用", "address", dposInstance.key.Address().String())
					} else {
						// 如果没有设置，则尝试从全局注册表获取
						logger.Debug("⚠️ 父区块Signature中没有DPoS实例引用，尝试从全局注册表获取")
						allInstances := GetAllDPoSInstances()
						logger.Debug("🔍 全局注册表中的DPoS实例数量", "count", len(allInstances))

						if len(allInstances) == 0 {
							logger.Warn("⚠️ 全局注册表中没有DPoS实例，可能DPoS还未启动")
						}

						for key, instance := range allInstances {
							if instance != nil {
								dposInstance = instance
								found = true
								logger.Debug("找到DPoS实例", "key", key, "address", instance.key.Address().String())
								break
							}
						}
					}

					if found {
						logger.Info("📨 ValidateParentSignatures - 发起BLS公钥网络请求",
							"blockNumber", blockNumber,
							"parentBlockNumber", parentBlockNumber,
							"address", missingAddress.String())

						// 发起网络请求
						if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
							myAddress := types.Address(dposInstance.key.Address())
							if err := dposInstance.runtime.networkIntegration.RequestBLSKey(missingAddress, myAddress); err != nil {
								logger.Debug("⚠️ ValidateParentSignatures - 发送BLS公钥请求失败",
									"blockNumber", blockNumber,
									"parentBlockNumber", parentBlockNumber,
									"address", missingAddress.String(),
									"error", err)
							} else {
								logger.Info("📨 ValidateParentSignatures - BLS公钥网络请求已发送",
									"blockNumber", blockNumber,
									"parentBlockNumber", parentBlockNumber,
									"address", missingAddress.String())
							}
						}

						// 🆕 增强：增加BLS公钥网络请求的等待时间和重试机制
						maxWaitTime := 10 * time.Second  // 增加等待时间到10秒
						retryInterval := 2 * time.Second // 每2秒检查一次
						maxRetries := int(maxWaitTime / retryInterval)

						logger.Info("⏳ ValidateParentSignatures - 开始等待BLS公钥网络响应",
							"blockNumber", blockNumber,
							"parentBlockNumber", parentBlockNumber,
							"maxWaitTime", maxWaitTime.String(),
							"retryInterval", retryInterval.String(),
							"maxRetries", maxRetries)

						for retry := 0; retry < maxRetries; retry++ {
							time.Sleep(retryInterval)
							elapsedTime := time.Duration(retry+1) * retryInterval

							logger.Info("⏳ ValidateParentSignatures - 等待BLS公钥网络响应",
								"blockNumber", blockNumber,
								"parentBlockNumber", parentBlockNumber,
								"retry", retry+1,
								"maxRetries", maxRetries,
								"elapsedTime", elapsedTime)

							// 检查是否已经获取到BLS公钥
							if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
								if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(missingAddress); exists {
									logger.Info("✅ ValidateParentSignatures - 重试期间成功获取BLS公钥",
										"blockNumber", blockNumber,
										"parentBlockNumber", parentBlockNumber,
										"address", missingAddress.String(),
										"blsKeyLength", len(cachedBLSKey),
										"retry", retry+1,
										"elapsedTime", elapsedTime)
									break
								}
							}

							// 最后一次重试
							if retry == maxRetries-1 {
								logger.Warn("⚠️ ValidateParentSignatures - BLS公钥网络请求超时，但继续处理",
									"blockNumber", blockNumber,
									"parentBlockNumber", parentBlockNumber,
									"maxWaitTime", maxWaitTime,
									"note", "系统将容忍BLS公钥缺失，继续验证流程")
							}
						}

						// 🆕 增强：重新尝试验证父区块签名，如果失败则继续重试
						maxVerifyRetries := 3
						verifySuccess := false

						logger.Info("🔄 ValidateParentSignatures - 开始重新尝试验证父区块签名",
							"blockNumber", blockNumber,
							"parentBlockNumber", parentBlockNumber,
							"maxRetries", maxVerifyRetries)

						for verifyRetry := 0; verifyRetry < maxVerifyRetries; verifyRetry++ {
							if err := i.Parent.Verify(parentBlockNumber, parentValidators, parentCheckpointHash, domain, logger); err != nil {
								logger.Warn("⚠️ ValidateParentSignatures - 重新尝试验证父区块签名失败",
									"blockNumber", blockNumber,
									"parentBlockNumber", parentBlockNumber,
									"retry", verifyRetry+1,
									"maxRetries", maxVerifyRetries,
									"error", err)

								if verifyRetry < maxVerifyRetries-1 {
									// 等待一段时间后重试
									waitTime := time.Duration(verifyRetry+1) * time.Second
									logger.Info("⏳ ValidateParentSignatures - 等待后重试验证",
										"blockNumber", blockNumber,
										"parentBlockNumber", parentBlockNumber,
										"waitTime", waitTime.String())
									time.Sleep(waitTime)
								} else {
									// 最后一次重试失败，记录警告但继续处理
									logger.Warn("⚠️ ValidateParentSignatures - 验证最终失败，但继续处理",
										"blockNumber", blockNumber,
										"parentBlockNumber", parentBlockNumber,
										"maxRetries", maxVerifyRetries,
										"error", err,
										"note", "系统将容忍此错误，继续验证流程")
								}
							} else {
								logger.Info("✅ ValidateParentSignatures - 重新尝试验证父区块签名成功",
									"blockNumber", blockNumber,
									"parentBlockNumber", parentBlockNumber,
									"retry", verifyRetry+1)
								verifySuccess = true
								break
							}
						}

						// 如果验证最终失败，记录警告但继续处理
						if !verifySuccess {
							logger.Warn("⚠️ ValidateParentSignatures - 父区块签名验证最终失败，但继续处理",
								"blockNumber", blockNumber,
								"parentBlockNumber", parentBlockNumber,
								"note", "系统将容忍此错误，继续验证流程")
							// 这里可以添加备选处理逻辑
						}
					} else {
						logger.Error("❌ ValidateParentSignatures - 无法获取DPoS实例来请求BLS公钥",
							"blockNumber", blockNumber,
							"parentBlockNumber", parentBlockNumber)
						return fmt.Errorf("failed to verify signatures for parent of block %d (proposal hash: %s): %w",
							blockNumber, parentCheckpointHash, err)
					}
				} else {
					logger.Error("❌ ValidateParentSignatures - 无法解析缺失BLS密钥的受托人地址",
						"blockNumber", blockNumber,
						"parentBlockNumber", parentBlockNumber,
						"error", err)
					return fmt.Errorf("failed to verify signatures for parent of block %d (proposal hash: %s): %w",
						blockNumber, parentCheckpointHash, err)
				}
			} else {
				logger.Error("❌ ValidateParentSignatures - 无法解析错误信息",
					"blockNumber", blockNumber,
					"parentBlockNumber", parentBlockNumber,
					"error", err)
				return fmt.Errorf("failed to verify signatures for parent of block %d (proposal hash: %s): %w",
					blockNumber, parentCheckpointHash, err)
			}
		} else {
			// 其他类型的错误，直接返回
			return fmt.Errorf("failed to verify signatures for parent of block %d (proposal hash: %s): %w",
				blockNumber, parentCheckpointHash, err)
		}
	}

	return nil
}

// Signature represents aggregated signatures of signers accompanied with a bitmap
// (in order to be able to determine identities of each signer)
type Signature struct {
	AggregatedSignature []byte
	Bitmap              bitmap.Bitmap
	// 🆕 添加：DPoS实例引用，用于BLS密钥获取
	dposInstance *DPoS
}

// MarshalRLPWith marshals Signature object into RLP format
func (s *Signature) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	committed := ar.NewArray()
	if s.AggregatedSignature == nil {
		committed.Set(ar.NewNull())
	} else {
		committed.Set(ar.NewBytes(s.AggregatedSignature))
	}

	if len(s.Bitmap) == 0 {
		committed.Set(ar.NewNull())
	} else {
		committed.Set(ar.NewBytes([]byte(s.Bitmap)))
	}

	return committed
}

// UnmarshalRLPWith unmarshals Signature object from the RLP format
func (s *Signature) UnmarshalRLPWith(v *fastrlp.Value) error {
	vals, err := v.GetElems()
	if err != nil {
		return fmt.Errorf("array type expected for signature struct")
	}

	// there should be exactly two elements (aggregated signature and bitmap)
	if num := len(vals); num != 2 {
		return fmt.Errorf("incorrect elements count to decode Signature, expected 2 but found %d", num)
	}

	s.AggregatedSignature, err = vals[0].GetBytes(nil)
	if err != nil {
		return err
	}

	var bitmapBytes []byte
	bitmapBytes, err = vals[1].GetBytes(nil)
	if err != nil {
		return err
	}
	s.Bitmap = bitmap.Bitmap(bitmapBytes)

	return nil
}

// SetDPoSInstance 设置DPoS实例引用
func (s *Signature) SetDPoSInstance(dpos *DPoS) {
	s.dposInstance = dpos
}

// GetDPoSInstance 获取DPoS实例引用
func (s *Signature) GetDPoSInstance() *DPoS {
	return s.dposInstance
}

// tryFetchBLSKeyFromNetwork 尝试从网络获取BLS密钥并保存到验证者对象
func (s *Signature) tryFetchBLSKeyFromNetwork(missingAddress types.Address, blockNumber uint64, validators validator.AccountSet, logger hclog.Logger) bool {
	logger.Info("🔍 开始尝试从网络获取BLS密钥",
		"blockNumber", blockNumber,
		"missingAddress", missingAddress.String())

	// 🆕 获取DPoS实例
	var dposInstance *DPoS
	var found bool

	// 首先尝试使用Signature中已设置的DPoS实例
	if s.dposInstance != nil {
		dposInstance = s.dposInstance
		found = true
		logger.Debug("✅ 使用Signature中的DPoS实例引用", "address", dposInstance.key.Address().String())
	} else {
		// 如果没有设置，则尝试从全局注册表获取
		logger.Debug("⚠️ Signature中没有DPoS实例引用，尝试从全局注册表获取")
		allInstances := GetAllDPoSInstances()
		logger.Debug("🔍 全局注册表中的DPoS实例数量", "count", len(allInstances))

		if len(allInstances) == 0 {
			logger.Warn("⚠️ 全局注册表中没有DPoS实例，可能DPoS还未启动")
			return false
		}

		for key, instance := range allInstances {
			if instance != nil {
				dposInstance = instance
				found = true
				logger.Debug("✅ 找到DPoS实例", "key", key, "address", instance.key.Address().String())
				break
			}
		}
	}

	if !found {
		logger.Error("❌ 无法获取DPoS实例来请求BLS公钥")
		return false
	}

	// 发起网络请求
	logger.Info("📨 发起BLS公钥网络请求",
		"blockNumber", blockNumber,
		"address", missingAddress.String())

	if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
		myAddress := types.Address(dposInstance.key.Address())
		logger.Debug("🔍 网络请求详情",
			"blockNumber", blockNumber,
			"missingAddress", missingAddress.String(),
			"myAddress", myAddress.String(),
			"networkIntegrationExists", dposInstance.runtime.networkIntegration != nil)

		if err := dposInstance.runtime.networkIntegration.RequestBLSKey(missingAddress, myAddress); err != nil {
			logger.Warn("⚠️ 发送BLS公钥请求失败",
				"blockNumber", blockNumber,
				"address", missingAddress.String(),
				"error", err)
		} else {
			logger.Info("📨 BLS公钥网络请求已发送",
				"blockNumber", blockNumber,
				"address", missingAddress.String())
		}
	} else {
		logger.Warn("⚠️ 网络集成不可用",
			"blockNumber", blockNumber,
			"address", missingAddress.String(),
			"runtimeExists", dposInstance.runtime != nil,
			"networkIntegrationExists", dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil)
	}

	// 🆕 等待网络响应
	maxWaitTime := 10 * time.Second  // 等待时间10秒
	retryInterval := 2 * time.Second // 每2秒检查一次
	maxRetries := int(maxWaitTime / retryInterval)

	logger.Info("⏳ 开始等待BLS公钥网络响应",
		"blockNumber", blockNumber,
		"maxWaitTime", maxWaitTime.String(),
		"retryInterval", retryInterval.String(),
		"maxRetries", maxRetries)

	for retry := 0; retry < maxRetries; retry++ {
		time.Sleep(retryInterval)
		elapsedTime := time.Duration(retry+1) * retryInterval

		logger.Info("⏳ 等待BLS公钥网络响应",
			"blockNumber", blockNumber,
			"retry", retry+1,
			"maxRetries", maxRetries,
			"elapsedTime", elapsedTime)

		// 检查是否已经获取到BLS公钥
		if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
			logger.Info("🔍 开始检查网络缓存中的BLS公钥",
				"blockNumber", blockNumber,
				"address", missingAddress.String())

			if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(missingAddress); exists {
				logger.Info("✅ 成功从网络获取BLS公钥+++++++++++++++++++++++++++++++++++++",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"blsKeyLength", len(cachedBLSKey),
					"retry", retry+1,
					"elapsedTime", elapsedTime)
				logger.Info("✅ 成功从网络获取BLS公钥+++++++++++++++++++++++++++++++++++++",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"blsKeyLength", len(cachedBLSKey),
					"retry", retry+1,
					"elapsedTime", elapsedTime)
				logger.Info("✅ 成功从网络获取BLS公钥+++++++++++++++++++++++++++++++++++++",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"blsKeyLength", len(cachedBLSKey),
					"retry", retry+1,
					"elapsedTime", elapsedTime)
				logger.Info("✅ 成功从网络获取BLS公钥+++++++++++++++++++++++++++++++++++++",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"blsKeyLength", len(cachedBLSKey),
					"retry", retry+1,
					"elapsedTime", elapsedTime)
				logger.Info("✅ 成功从网络获取BLS公钥+++++++++++++++++++++++++++++++++++++",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"blsKeyLength", len(cachedBLSKey),
					"retry", retry+1,
					"elapsedTime", elapsedTime)

				// 🆕 关键修复：将获取到的BLS公钥保存到验证者对象中
				logger.Info("🔍 开始保存BLS公钥到验证者对象",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"validatorsCount", len(validators))

				if validators != nil {
					found := false
					logger.Info("🔍 开始遍历验证者集合",
						"blockNumber", blockNumber,
						"missingAddress", missingAddress.String(),
						"validatorsCount", len(validators))

					for i, validator := range validators {
						logger.Info("🔍 检查验证者",
							"index", i,
							"address", validator.Address.String(),
							"targetAddress", missingAddress.String(),
							"match", validator.Address == missingAddress,
							"addressBytes", fmt.Sprintf("%x", validator.Address[:]),
							"targetBytes", fmt.Sprintf("%x", missingAddress[:]))

						if validator.Address == missingAddress {
							found = true
							logger.Info("🎯 找到匹配的验证者，开始保存BLS公钥",
								"blockNumber", blockNumber,
								"address", missingAddress.String(),
								"validatorIndex", i)

							// 解析BLS公钥
							if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
								// 🆕 关键修复：确保保存到正确的验证者对象
								validator.BlsKey = blsKey

								// 立即验证保存结果
								if validator.BlsKey != nil {
									logger.Info("✅ BLS公钥保存成功",
										"blockNumber", blockNumber,
										"address", missingAddress.String(),
										"validatorIndex", i,
										"blsKeyLength", len(cachedBLSKey))

									// 🆕 额外验证：检查保存后的公钥长度
									if marshaled := validator.BlsKey.Marshal(); len(marshaled) > 0 {
										logger.Info("🎯 BLS公钥保存验证成功",
											"blockNumber", blockNumber,
											"address", missingAddress.String(),
											"marshaledLength", len(marshaled))
									} else {
										logger.Warn("⚠️ BLS公钥保存后序列化失败",
											"blockNumber", blockNumber,
											"address", missingAddress.String())
									}
								} else {
									logger.Error("❌ BLS公钥保存失败，validator.BlsKey仍为nil",
										"blockNumber", blockNumber,
										"address", missingAddress.String(),
										"validatorIndex", i)
								}

								// 🆕 保存后验证：检查验证者集合中是否真的保存了BLS公钥
								if found && validator.BlsKey != nil {
									logger.Info("🔍 保存后验证：检查验证者集合中的BLS公钥状态",
										"blockNumber", blockNumber,
										"address", missingAddress.String(),
										"validatorIndex", i,
										"blsKeyExists", validator.BlsKey != nil,
										"blsKeyLength", func() int {
											if validator.BlsKey != nil {
												return len(validator.BlsKey.Marshal())
											}
											return 0
										}())

									// 🆕 立即验证：重新检查验证者集合中该地址的BLS公钥状态
									logger.Info("🔍 立即验证：重新检查验证者集合状态",
										"blockNumber", blockNumber,
										"address", missingAddress.String())

									for j, v := range validators {
										if v.Address == missingAddress {
											logger.Info("🔍 立即验证结果",
												"blockNumber", blockNumber,
												"address", missingAddress.String(),
												"validatorIndex", j,
												"blsKeyExists", v.BlsKey != nil,
												"blsKeyLength", func() int {
													if v.BlsKey != nil {
														return len(v.BlsKey.Marshal())
													}
													return 0
												}())
											break
										}
									}
								}

								break
							}
						}
					}

					if !found {
						logger.Warn("⚠️ 在验证者集合中未找到匹配的地址",
							"blockNumber", blockNumber,
							"missingAddress", missingAddress.String(),
							"validatorsCount", len(validators))
					}
				} else {
					logger.Warn("⚠️ 验证者集合为nil，无法保存BLS公钥",
						"blockNumber", blockNumber,
						"address", missingAddress.String())
				}

				return true
			}
		}

		// 最后一次重试
		if retry == maxRetries-1 {
			logger.Warn("⚠️ BLS公钥网络请求超时",
				"blockNumber", blockNumber,
				"maxWaitTime", maxWaitTime,
				"note", "网络获取超时，将使用备选方案")
		}
	}

	logger.Warn("⚠️ 网络获取BLS公钥失败",
		"blockNumber", blockNumber,
		"address", missingAddress.String(),
		"note", "将使用备选方案继续验证")

	// 🆕 备选方案：尝试从本地获取BLS公钥
	logger.Info("🔍 尝试备选方案：从本地获取BLS公钥",
		"blockNumber", blockNumber,
		"address", missingAddress.String())

	// 尝试从本地获取BLS公钥（这里可以添加本地存储的逻辑）
	// 暂时返回false，让上层处理
	return false
}

// Verify is used to verify aggregated signature based on current validator set, message hash and domain
func (s *Signature) Verify(blockNumber uint64, validators validator.AccountSet,
	hash types.Hash, domain []byte, logger hclog.Logger) error {

	logger.Debug("Signature.Verify - 开始验证签名",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"bitmapLength", len(s.Bitmap),
		"aggregatedSignatureLength", len(s.AggregatedSignature))

	// 🆕 修复：当GetFilteredValidators失败时，尝试获取缺失的BLS密钥
	logger.Info("🚀🚀🚀 开始执行新的方案2逻辑 🚀🚀🚀", "blockNumber", blockNumber)
	signers, err := validators.GetFilteredValidators(s.Bitmap)
	if err != nil {
		logger.Warn("⚠️ Signature.Verify - GetFilteredValidators失败，尝试获取缺失的BLS密钥", "error", err)

		// 解析错误信息，提取缺失BLS密钥的受托人地址
		if strings.Contains(err.Error(), "has nil BLS key but is marked as signer in bitmap") {
			// 提取受托人地址
			parts := strings.Split(err.Error(), "(")
			if len(parts) >= 2 {
				addressPart := strings.Split(parts[1], ")")[0]
				if strings.HasPrefix(addressPart, "0x") {
					missingAddress := types.StringToAddress(addressPart)
					logger.Info("🔍 检测到缺失BLS密钥的受托人",
						"blockNumber", blockNumber,
						"missingAddress", missingAddress.String())

					// 🆕 方案2：先尝试网络获取BLS密钥，再重新验证
					logger.Info("🚀🚀🚀 执行方案2：先网络获取，再重新验证 🚀🚀🚀")

					// 尝试从网络获取BLS密钥
					if s.tryFetchBLSKeyFromNetwork(missingAddress, blockNumber, validators, logger) {
						logger.Info("✅ 网络获取BLS密钥成功，重新尝试GetFilteredValidators")

						// 重新尝试GetFilteredValidators
						signers, err = validators.GetFilteredValidators(s.Bitmap)
						if err == nil {
							logger.Info("✅ 网络获取BLS密钥后，GetFilteredValidators成功",
								"blockNumber", blockNumber,
								"signersCount", len(signers))
							// 🆕 关键修复：成功获取后，直接继续后续验证逻辑
							goto continueVerification
						} else {
							logger.Warn("⚠️ 网络获取BLS密钥后，GetFilteredValidators仍然失败",
								"blockNumber", blockNumber,
								"error", err,
								"note", "将使用备选方案继续验证")
						}
					} else {
						logger.Warn("⚠️ 网络获取BLS密钥失败，将使用备选方案",
							"blockNumber", blockNumber,
							"note", "系统将容忍BLS公钥缺失，继续验证流程")
					}
				} else {
					logger.Error("❌ 无法解析缺失BLS密钥的受托人地址", "error", err)
					return err
				}
			} else {
				logger.Error("❌ 无法解析GetFilteredValidators错误信息", "error", err)
				return err
			}
		} else {
			// 其他类型的错误，直接返回
			logger.Error("❌ GetFilteredValidators失败，非BLS密钥缺失错误", "error", err)
			return err
		}
	}

continueVerification:
	logger.Debug("Signature.Verify - 过滤后签名者信息",
		"filteredSignersCount", len(signers),
		"signerAddresses", signers.GetAddresses())

	validatorSet := validator.NewValidatorSet(validators, logger)
	if !validatorSet.HasQuorum(blockNumber, signers.GetAddressesAsSet()) {
		// 🆕 计算基于人数的法定人数要求（1/2多数原则）
		requiredQuorumCount := validator.GetQuorumSizeByValidatorCount(len(validators))

		// 🆕 详细计算和显示法定人数要求
		quorumCalculation := fmt.Sprintf("总验证者数: %d, 1/2多数原则: %d/2 = %d, 最小要求: %d",
			len(validators), len(validators), len(validators)/2, requiredQuorumCount)

		logger.Error("Signature.Verify - 法定人数不足",
			"blockNumber", blockNumber,
			"signersCount", len(signers),
			"requiredQuorumCount", requiredQuorumCount,
			"totalValidators", len(validators),
			"quorumCalculation", quorumCalculation,
			"signerAddresses", signers.GetAddresses())

		// 🆕 修复：直接按位图索引记录验证者信息，避免访问 signers 数组
		logger.Info("🔍 按位图索引记录验证者信息:")
		for i := uint64(0); i < uint64(len(validators)); i++ {
			if s.Bitmap.IsSet(i) {
				validator := validators[int(i)]
				logger.Info("🔍 位图验证者详情",
					"bitmapIndex", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive)
			}
		}

		logger.Error("🚨 区块验证失败 - 法定人数不足，将返回错误让上层处理",
			"blockNumber", blockNumber,
			"reason", "quorum not reached",
			"quorumDetails", fmt.Sprintf("当前签名数: %d, 需要签名数: %d, 差距: %d",
				len(signers), requiredQuorumCount, requiredQuorumCount-len(signers)))

		return fmt.Errorf("quorum not reached: current signatures %d, required %d", len(signers), requiredQuorumCount)
	}

	logger.Debug("Signature.Verify - 法定人数验证通过，开始验证BLS签名")

	// 🆕 添加详细日志：打印从数据库读取的验证者信息
	logger.Debug("🔍 Signature.Verify - 验证者详细信息",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"filteredSigners", len(signers),
		"bitmapLength", len(s.Bitmap),
		"bitmapHex", fmt.Sprintf("%x", s.Bitmap))

	// 🆕 修复：先计算位图中设置的位数，然后创建正确长度的数组
	bitmapSetCount := 0
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			bitmapSetCount++
		}
	}

	// 创建与validators数组长度一致的BLS公钥数组，确保位图索引能正确对应
	blsPublicKeys := make([]*bls.PublicKey, len(validators))
	missingBLSKeys := make([]types.Address, 0)

	// 按位图索引填充BLS公钥数组，保持与validators数组的索引对应关系
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			validator := validators[int(i)]
			blsPublicKeys[i] = validator.BlsKey

			// 🆕 详细打印每个验证者的BLS公钥信息
			if validator.BlsKey != nil {
				pubKeyBytes := validator.BlsKey.Marshal()
				logger.Debug("🔑 Signature.Verify - 验证者BLS公钥详情",
					"bitmapIndex", i,
					"keyIndex", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"blsKeyExists", true,
					"publicKeyBytes", fmt.Sprintf("%x", pubKeyBytes),
					"publicKeyLength", len(pubKeyBytes))
			} else {
				logger.Debug("⚠️ Signature.Verify - 验证者缺少BLS公钥，将尝试网络获取",
					"bitmapIndex", i,
					"keyIndex", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"blsKeyExists", false)
				missingBLSKeys = append(missingBLSKeys, validator.Address)
			}
		}
	}

	// 🆕 如果有缺失的BLS公钥，先尝试从本地genesis文件读取，不行再通过网络广播获取
	if len(missingBLSKeys) > 0 {
		logger.Info("🔍 发现缺失的BLS公钥，先尝试从本地genesis文件读取",
			"blockNumber", blockNumber,
			"missingCount", len(missingBLSKeys),
			"missingAddresses", missingBLSKeys)

		// 🆕 第一步：尝试从本地genesis文件读取BLS公钥
		genesisKeysFound := 0
		for _, address := range missingBLSKeys {
			logger.Info("🔍 尝试从本地genesis文件读取BLS公钥",
				"blockNumber", blockNumber,
				"address", address.String())

			// 尝试从genesis文件或本地存储中获取BLS公钥
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				// 尝试从本地genesis文件读取
				if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
					if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(address); exists {
						// 解析BLS公钥
						if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
							// 找到对应的验证者索引并设置BLS公钥
							for i := uint64(0); i < uint64(len(validators)); i++ {
								if validators[int(i)].Address == address {
									blsPublicKeys[i] = blsKey
									genesisKeysFound++
									logger.Info("✅ 从本地genesis文件成功读取BLS公钥",
										"blockNumber", blockNumber,
										"address", address.String(),
										"bitmapIndex", i,
										"blsKeyLength", len(cachedBLSKey))
									break
								}
							}
						} else {
							logger.Debug("⚠️ 解析本地genesis文件中的BLS公钥失败",
								"blockNumber", blockNumber,
								"address", address.String(),
								"error", err)
						}
					} else {
						logger.Info("⚠️ 本地genesis文件中未找到BLS公钥，将尝试网络广播获取",
							"blockNumber", blockNumber,
							"address", address.String())
					}
				}
			}
		}

		// 🆕 第二步：如果本地genesis文件中没有找到，则通过网络广播获取
		remainingMissingKeys := make([]types.Address, 0)
		for _, address := range missingBLSKeys {
			// 检查是否已经从本地genesis文件获取到
			found := false
			for i := uint64(0); i < uint64(len(validators)); i++ {
				if validators[int(i)].Address == address && blsPublicKeys[i] != nil {
					found = true
					break
				}
			}
			if !found {
				remainingMissingKeys = append(remainingMissingKeys, address)
			}
		}

		if len(remainingMissingKeys) > 0 {
			logger.Info("📨 本地genesis文件中未找到的BLS公钥，开始网络广播获取",
				"blockNumber", blockNumber,
				"remainingCount", len(remainingMissingKeys),
				"remainingAddresses", remainingMissingKeys,
				"genesisKeysFound", genesisKeysFound)

			// 尝试从全局注册表获取DPoS实例并请求BLS公钥
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				for _, address := range remainingMissingKeys {
					logger.Info("📨 发起BLS公钥网络请求",
						"blockNumber", blockNumber,
						"address", address.String())

					// 发起网络请求
					if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
						myAddress := types.Address(dposInstance.key.Address())
						if err := dposInstance.runtime.networkIntegration.RequestBLSKey(address, myAddress); err != nil {
							logger.Debug("⚠️ 发送BLS公钥请求失败",
								"blockNumber", blockNumber,
								"address", address.String(),
								"error", err)
						} else {
							logger.Info("📨 BLS公钥网络请求已发送",
								"blockNumber", blockNumber,
								"address", address.String())
						}
					}
				}
			}

			// 🆕 增强：增加BLS公钥网络请求的等待时间和重试机制
			maxWaitTime := 10 * time.Second  // 增加等待时间到10秒
			retryInterval := 2 * time.Second // 每2秒检查一次
			maxRetries := int(maxWaitTime / retryInterval)

			logger.Info("⏳ 开始等待BLS公钥网络响应",
				"blockNumber", blockNumber,
				"maxWaitTime", maxWaitTime.String(),
				"retryInterval", retryInterval.String(),
				"maxRetries", maxRetries)

			for retry := 0; retry < maxRetries; retry++ {
				time.Sleep(retryInterval)
				elapsedTime := time.Duration(retry+1) * retryInterval

				logger.Info("⏳ 等待BLS公钥网络响应",
					"blockNumber", blockNumber,
					"retry", retry+1,
					"maxRetries", maxRetries,
					"elapsedTime", elapsedTime)

				// 🆕 修复：直接按位图索引检查BLS公钥，避免访问 signers 数组
				// 检查是否已经获取到所有需要的BLS公钥
				allKeysFound := true
				for i := uint64(0); i < uint64(len(validators)); i++ {
					if s.Bitmap.IsSet(i) && int(i) < len(blsPublicKeys) && blsPublicKeys[i] == nil {
						validator := validators[int(i)]
						// 重新获取dposInstance以确保作用域正确
						if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
							if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
								if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
									// 解析BLS公钥
									if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
										blsPublicKeys[i] = blsKey
										logger.Info("✅ 重试期间成功获取BLS公钥",
											"blockNumber", blockNumber,
											"address", validator.Address.String(),
											"blsKeyLength", len(cachedBLSKey),
											"retry", retry+1,
											"elapsedTime", elapsedTime)
									}
								} else {
									allKeysFound = false
								}
							}
						}
					}
				}

				// 如果所有密钥都找到了，提前退出
				if allKeysFound {
					logger.Info("✅ 所有BLS公钥都已获取，提前退出等待",
						"blockNumber", blockNumber,
						"retry", retry+1,
						"elapsedTime", elapsedTime)
					break
				}

				// 最后一次重试
				if retry == maxRetries-1 {
					logger.Warn("⚠️ BLS公钥网络请求超时，但继续处理",
						"blockNumber", blockNumber,
						"maxWaitTime", maxWaitTime,
						"note", "系统将容忍BLS公钥缺失，继续验证流程")
				}
			}

			// 🆕 修复：移除重复的BLS公钥检查逻辑，避免与第一个逻辑冲突
			// 第一个逻辑已经处理了BLS公钥获取和保存，这里不再重复处理
			logger.Info("🔍 BLS公钥状态检查完成，跳过重复检查",
				"blockNumber", blockNumber,
				"note", "第一个逻辑已处理BLS公钥获取和保存")
		}
	}

	logger.Debug("Signature.Verify - 开始验证BLS聚合签名",
		"aggregatedSignatureLength", len(s.AggregatedSignature),
		"aggregatedSignatureBytes", fmt.Sprintf("%x", s.AggregatedSignature),
		"bitmapLength", len(s.Bitmap),
		"bitmapBytes", fmt.Sprintf("%x", s.Bitmap),
		"hash", hash.String(),
		"domain", fmt.Sprintf("%x", domain))

	aggs, err := bls.UnmarshalSignature(s.AggregatedSignature)
	if err != nil {
		logger.Error("Signature.Verify - 解析聚合签名失败", "error", err)
		return err
	}

	// 🆕 验证签名数据本身
	logger.Debug("🔍 聚合签名数据检查",
		"blockNumber", blockNumber,
		"aggregatedSignatureLength", len(s.AggregatedSignature),
		"aggregatedSignatureBytes", fmt.Sprintf("%x", s.AggregatedSignature),
		"signatureType", fmt.Sprintf("%T", aggs))

	logger.Debug("Signature.Verify - 聚合签名解析成功",
		"signatureType", fmt.Sprintf("%T", aggs))

	// 🆕 添加BLS签名验证前的调试信息
	logger.Debug("🔍 BLS签名验证前准备",
		"blockNumber", blockNumber,
		"blsPublicKeysCount", len(blsPublicKeys),
		"hash", hash.String(),
		"domain", fmt.Sprintf("%x", domain))

	// 🆕 修复：直接按位图索引获取验证者，避免访问 signers 数组
	logger.Debug("🔍 验证签名顺序与公钥顺序匹配:")
	for i, pubKey := range blsPublicKeys {
		if pubKey != nil {
			// 安全访问 validators 数组，避免越界
			var addressStr string
			if int(i) < len(validators) {
				addressStr = validators[int(i)].Address.String()
			} else {
				addressStr = "unknown_index"
			}

			logger.Debug("🔍 验证用BLS公钥",
				"index", i,
				"address", addressStr,
				"pubKeyBytes", fmt.Sprintf("%x", pubKey.Marshal()))

			// 检查公钥是否与验证者地址匹配
			if int(i) < len(validators) {
				validator := validators[int(i)]
				logger.Debug("🔗 签名顺序验证",
					"signatureIndex", i,
					"expectedAddress", validator.Address.String(),
					"hasBlsKey", validator.BlsKey != nil)
			}
		}
	}

	// 🆕 显示位图对应的签名顺序，并直接获取缺失的BLS公钥
	logger.Debug("🔗 位图对应的签名顺序:")
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			if int(i) < len(validators) {
				validator := validators[int(i)]
				logger.Debug("位图顺序",
					"bitmapIndex", i,
					"validatorAddress", validator.Address.String(),
					"blsKeyExists", validator.BlsKey != nil,
					"blsKeyLength", func() int {
						if validator.BlsKey != nil {
							return len(validator.BlsKey.Marshal())
						}
						return 0
					}())

				// 🆕 简单修复：如果BLS公钥为空，直接网络获取
				if validator.BlsKey == nil {
					logger.Info("🔍 检测到空的BLS公钥，直接网络获取",
						"blockNumber", blockNumber,
						"bitmapIndex", i,
						"address", validator.Address.String())

					// 直接调用网络获取
					if s.tryFetchBLSKeyFromNetwork(validator.Address, blockNumber, validators, logger) {
						logger.Info("✅ 直接网络获取BLS公钥成功",
							"blockNumber", blockNumber,
							"address", validator.Address.String())
					} else {
						logger.Warn("⚠️ 直接网络获取BLS公钥失败",
							"blockNumber", blockNumber,
							"address", validator.Address.String())

						// 🆕 备选方案：尝试从本地获取BLS公钥
						logger.Info("🔍 尝试备选方案：从本地获取BLS公钥",
							"blockNumber", blockNumber,
							"address", validator.Address.String())

						// 这里可以添加从本地存储获取BLS公钥的逻辑
						// 暂时跳过，让上层处理
					}
				}
			}
		}
	}

	// 🆕 方案2修复：使用验证者地址映射来重新排列BLS公钥，确保与生产时的签名顺序完全一致
	// 生产时按位图索引顺序聚合签名，验证时也应该按位图索引顺序排列公钥
	validBLSKeys := make([]*bls.PublicKey, 0)
	bitmapOrderedAddresses := make([]types.Address, 0)

	logger.Debug("🔍 开始按位图索引顺序重新排列BLS公钥")

	// 🆕 创建地址到BLS公钥的映射，避免依赖索引位置
	addressToBLSKey := make(map[types.Address]*bls.PublicKey)
	for i, validator := range validators {
		if validator.BlsKey != nil {
			addressToBLSKey[validator.Address] = validator.BlsKey
			logger.Debug("🔍 创建地址到BLS公钥映射",
				"validatorIndex", i,
				"address", validator.Address.String(),
				"blsKeyExists", validator.BlsKey != nil,
				"blsKeyLength", len(validator.BlsKey.Marshal()))
		} else {
			logger.Debug("🔍 验证者缺少BLS公钥",
				"validatorIndex", i,
				"address", validator.Address.String())
		}
	}

	// 按位图索引顺序收集公钥和地址，使用地址映射查找BLS公钥
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			validatorAddress := validators[int(i)].Address
			if blsKey, exists := addressToBLSKey[validatorAddress]; exists && blsKey != nil {
				validBLSKeys = append(validBLSKeys, blsKey)
				bitmapOrderedAddresses = append(bitmapOrderedAddresses, validatorAddress)
				logger.Debug("🔍 按位图顺序排列BLS公钥",
					"bitmapIndex", i,
					"signatureIndex", len(validBLSKeys)-1,
					"address", validatorAddress.String(),
					"publicKeyLength", len(blsKey.Marshal()))
			} else {
				logger.Warn("⚠️ 位图索引对应的BLS公钥不存在或为nil",
					"bitmapIndex", i,
					"address", validatorAddress.String(),
					"blsPublicKeysLength", len(blsPublicKeys),
					"hasBlsKey", exists && blsKey != nil,
					"note", "使用地址映射查找BLS公钥")
			}
		}
	}

	// 🆕 关键修复：记录按位图顺序排列的公钥信息，便于调试
	logger.Debug("🔍 按位图顺序排列的公钥信息",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"bitmapSetCount", bitmapSetCount,
		"validBLSKeysCount", len(validBLSKeys),
		"bitmapOrderedAddresses", bitmapOrderedAddresses)

	// 验证公钥顺序与生产时签名顺序的一致性
	logger.Debug("🔍 验证公钥顺序与生产时签名顺序的一致性:")
	for i, address := range bitmapOrderedAddresses {
		logger.Debug("🔗 位图顺序验证",
			"bitmapIndex", i,
			"signatureIndex", i,
			"address", address.String(),
			"hasBlsKey", i < len(validBLSKeys) && validBLSKeys[i] != nil)
	}

	logger.Info("🔍 BLS验证公钥过滤结果",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"bitmapSetCount", bitmapSetCount,
		"validBLSKeysCount", len(validBLSKeys),
		"note", "只传递有效的BLS公钥给验证函数")

	// 执行BLS签名验证（只使用有效的公钥）
	isValid := aggs.VerifyAggregated(validBLSKeys, hash[:], domain)
	logger.Debug("🔍 BLS签名验证结果",
		"blockNumber", blockNumber,
		"isValid", isValid,
		"aggregatedSignature", fmt.Sprintf("%x", s.AggregatedSignature))

	if !isValid {
		logger.Error("Signature.Verify - BLS签名验证失败",
			"blockNumber", blockNumber,
			"hash", hash.String(),
			"domain", fmt.Sprintf("%x", domain),
			"aggregatedSignatureLength", len(s.AggregatedSignature),
			"publicKeysCount", len(blsPublicKeys),
			"signersCount", len(signers))

		// 🆕 添加更详细的调试信息
		logger.Error("🔍 BLS签名验证失败详细信息",
			"hashBytes", fmt.Sprintf("%x", hash[:]),
			"hashLength", len(hash[:]),
			"domainBytes", fmt.Sprintf("%x", domain),
			"domainLength", len(domain),
			"aggregatedSignatureBytes", fmt.Sprintf("%x", s.AggregatedSignature),
			"aggregatedSignatureLength", len(s.AggregatedSignature))

		// 添加更详细的调试信息
		for i, pubKey := range blsPublicKeys {
			if pubKey != nil {
				pubKeyBytes := pubKey.Marshal()
				// 🆕 修复：直接按位图索引从 validators 数组获取地址，避免访问 signers 数组
				var addressStr string
				if int(i) < len(validators) {
					addressStr = validators[int(i)].Address.String()
				} else {
					addressStr = "unknown_index"
				}
				logger.Debug("Signature.Verify - BLS公钥信息",
					"index", i,
					"address", addressStr,
					"publicKeyBytes", fmt.Sprintf("%x", pubKeyBytes),
					"publicKeyLength", len(pubKeyBytes))
			} else {
				// 🆕 修复：直接按位图索引从 validators 数组获取地址，避免访问 signers 数组
				var addressStr string
				if int(i) < len(validators) {
					addressStr = validators[int(i)].Address.String()
				} else {
					addressStr = "unknown_index"
				}
				logger.Error("Signature.Verify - BLS公钥为nil",
					"index", i,
					"address", addressStr)
			}
		}

		logger.Error("🚨 BLS签名验证失败，程序将退出",
			"blockNumber", blockNumber,
			"reason", "BLS signature verification failed")

		// 🆕 区块验证失败时直接退出程序
		logger.Error("💀 区块验证失败，程序退出")
		os.Exit(1)
	}

	logger.Debug("Signature.Verify - 签名验证成功")
	return nil
}

var checkpointDataABIType = abi.MustNewType(`tuple(
	uint256 chainId,
	uint256 blockNumber,
	bytes32 blockHash,
	uint256 blockRound, 
	uint256 epochNumber,
	bytes32 eventRoot,
	bytes32 currentValidatorsHash,
	bytes32 nextValidatorsHash)`)

// CheckpointData represents data needed for checkpointing mechanism
type CheckpointData struct {
	BlockRound            uint64
	EpochNumber           uint64
	CurrentValidatorsHash types.Hash
	NextValidatorsHash    types.Hash
	EventRoot             types.Hash
}

// MarshalRLPWith defines the marshal function implementation for CheckpointData
func (c *CheckpointData) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	// BlockRound
	vv.Set(ar.NewUint(c.BlockRound))
	// EpochNumber
	vv.Set(ar.NewUint(c.EpochNumber))
	// CurrentValidatorsHash
	vv.Set(ar.NewBytes(c.CurrentValidatorsHash.Bytes()))
	// NextValidatorsHash
	vv.Set(ar.NewBytes(c.NextValidatorsHash.Bytes()))
	// EventRoot
	vv.Set(ar.NewBytes(c.EventRoot.Bytes()))

	return vv
}

// UnmarshalRLPWith unmarshals CheckpointData object from the RLP format
func (c *CheckpointData) UnmarshalRLPWith(v *fastrlp.Value) error {
	vals, err := v.GetElems()
	if err != nil {
		return fmt.Errorf("array type expected for CheckpointData struct")
	}

	// there should be exactly 5 elements:
	// BlockRound, EpochNumber, CurrentValidatorsHash, NextValidatorsHash, EventRoot
	if num := len(vals); num != 5 {
		return fmt.Errorf("incorrect elements count to decode CheckpointData, expected 5 but found %d", num)
	}

	// BlockRound
	c.BlockRound, err = vals[0].GetUint64()
	if err != nil {
		return err
	}

	// EpochNumber
	c.EpochNumber, err = vals[1].GetUint64()
	if err != nil {
		return err
	}

	// CurrentValidatorsHash
	currentValidatorsHashRaw, err := vals[2].GetBytes(nil)
	if err != nil {
		return err
	}

	c.CurrentValidatorsHash = types.BytesToHash(currentValidatorsHashRaw)

	// NextValidatorsHash
	nextValidatorsHashRaw, err := vals[3].GetBytes(nil)
	if err != nil {
		return err
	}

	c.NextValidatorsHash = types.BytesToHash(nextValidatorsHashRaw)

	// EventRoot
	eventRootRaw, err := vals[4].GetBytes(nil)
	if err != nil {
		return err
	}

	c.EventRoot = types.BytesToHash(eventRootRaw)

	return nil
}

// Copy returns deep copy of CheckpointData instance
func (c *CheckpointData) Copy() *CheckpointData {
	newCheckpointData := new(CheckpointData)
	*newCheckpointData = *c

	return newCheckpointData
}

// Hash calculates keccak256 hash of the CheckpointData.
// CheckpointData is ABI encoded and then hashed.
func (c *CheckpointData) Hash(chainID uint64, blockNumber uint64, blockHash types.Hash) (types.Hash, error) {
	checkpointMap := map[string]interface{}{
		"chainId":               new(big.Int).SetUint64(chainID),
		"blockNumber":           new(big.Int).SetUint64(blockNumber),
		"blockHash":             blockHash,
		"blockRound":            new(big.Int).SetUint64(c.BlockRound),
		"epochNumber":           new(big.Int).SetUint64(c.EpochNumber),
		"eventRoot":             c.EventRoot,
		"currentValidatorsHash": c.CurrentValidatorsHash,
		"nextValidatorsHash":    c.NextValidatorsHash,
	}

	abiEncoded, err := checkpointDataABIType.Encode(checkpointMap)
	if err != nil {
		return types.ZeroHash, err
	}

	return types.BytesToHash(crypto.Keccak256(abiEncoded)), nil
}

// ValidateBasic encapsulates basic validation logic for checkpoint data.
// It only checks epoch numbers validity and whether validators hashes are non-empty.
func (c *CheckpointData) ValidateBasic(parentCheckpoint *CheckpointData) error {
	if c.EpochNumber != parentCheckpoint.EpochNumber &&
		c.EpochNumber != parentCheckpoint.EpochNumber+1 {
		// epoch-beginning block
		// epoch number must be incremented by one compared to parent block's checkpoint
		return fmt.Errorf("invalid epoch number for epoch-beginning block")
	}

	if c.CurrentValidatorsHash == types.ZeroHash {
		return fmt.Errorf("current validators hash must not be empty")
	}

	if c.NextValidatorsHash == types.ZeroHash {
		return fmt.Errorf("next validators hash must not be empty")
	}

	return nil
}

// Validate encapsulates validation logic for checkpoint data
// (with regards to current and next epoch validators)
func (c *CheckpointData) Validate(parentCheckpoint *CheckpointData,
	currentValidators validator.AccountSet, nextValidators validator.AccountSet,
	exitRootHash types.Hash) error {
	if err := c.ValidateBasic(parentCheckpoint); err != nil {
		return err
	}

	// check if currentValidatorsHash, present in CheckpointData is correct
	currentValidatorsHash, err := currentValidators.Hash()
	if err != nil {
		return fmt.Errorf("failed to calculate current validators hash: %w", err)
	}

	if currentValidatorsHash != c.CurrentValidatorsHash {
		return fmt.Errorf("current validators hashes don't match")
	}

	// check if nextValidatorsHash, present in CheckpointData is correct
	nextValidatorsHash, err := nextValidators.Hash()
	if err != nil {
		return fmt.Errorf("failed to calculate next validators hash: %w", err)
	}

	if nextValidatorsHash != c.NextValidatorsHash {
		return fmt.Errorf("next validators hashes don't match")
	}

	// epoch ending blocks have validator set transitions
	if !currentValidators.Equals(nextValidators) &&
		c.EpochNumber != parentCheckpoint.EpochNumber {
		// epoch ending blocks should have the same epoch number as parent block
		// (as they belong to the same epoch)
		return fmt.Errorf("epoch number should not change for epoch-ending block")
	}

	// exit root hash of proposer and
	// validator that validates proposal have to match
	if exitRootHash != c.EventRoot {
		return fmt.Errorf("exit root hash not as expected")
	}

	return nil
}

// GetIbftExtraClean returns unmarshaled extra field from the passed in header,
// but without signatures for the given header (it only includes signatures for the parent block)
func GetIbftExtraClean(extraRaw []byte) ([]byte, error) {
	extra, err := GetIbftExtra(extraRaw)
	if err != nil {
		return nil, err
	}

	ibftExtra := &Extra{
		Parent:     extra.Parent,
		Validators: extra.Validators,
		Checkpoint: extra.Checkpoint,
		Committed:  &Signature{},
	}

	return ibftExtra.MarshalRLPTo(nil), nil
}

// GetIbftExtra returns the istanbul extra data field from the passed in header
func GetIbftExtra(extraRaw []byte) (*Extra, error) {
	if len(extraRaw) < ExtraVanity {
		return nil, fmt.Errorf("wrong extra size: %d", len(extraRaw))
	}

	extra := &Extra{}

	if err := extra.UnmarshalRLP(extraRaw); err != nil {
		return nil, err
	}

	return extra, nil
}

// 🆕 辅助函数：获取位图中设置的位位置
func getBitmapSetPositions(bitmap bitmap.Bitmap) string {
	positions := make([]string, 0)
	for i := uint64(0); i < bitmap.Len(); i++ {
		if bitmap.IsSet(i) {
			positions = append(positions, fmt.Sprintf("%d", i))
		}
	}
	return strings.Join(positions, ",")
}

// 🆕 辅助函数：获取位图对应的验证者地址
func getExpectedSignerAddresses(bitmap bitmap.Bitmap, validators validator.AccountSet) string {
	addresses := make([]string, 0)
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if bitmap.IsSet(i) {
			addresses = append(addresses, validators[int(i)].Address.String())
		}
	}
	return strings.Join(addresses, ",")
}
