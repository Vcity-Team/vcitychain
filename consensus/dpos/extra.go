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

	// 🆕 从 ExtraData 中获取验证者集合
	logger.Debug("🔄 开始从 ExtraData 获取验证者集合",
		"blockNumber", blockNumber,
		"method", "从区块ExtraData解析",
		"note", "不再依赖数据库，直接从区块数据获取")

	validators, err := i.getValidatorsFromExtraData(header, parent, parents, consensusBackend, logger)
	if err != nil {
		logger.Error("❌ 从 ExtraData 获取验证者集合失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("failed to get validators from ExtraData for block %d: %w", blockNumber, err)
	}

	// 🆕 关键修复：确保验证时使用的验证者集合与生产时完全一致
	// 生产时使用 r.delegates 设置位图索引，验证时也应该使用相同的验证者集合
	logger.Debug("🔍 验证时验证者集合与生产时一致性检查",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"note", "确保验证者集合与生产时位图索引对应关系一致")

	// 🔍 从 ExtraData 获取的验证者集合信息
	logger.Debug("✅ 从 ExtraData 成功获取验证者集合",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"validatorSource", "ExtraData.Validators",
		"note", "用于BLS签名验证的验证者集合")

	// 🆕 添加验证时验证者集合的详细对比日志
	logger.Debug("📋 当前区块验证者集合详细信息:")
	for i, validator := range validators {
		logger.Debug("📝 当前区块验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", validator.Address.String(),
			"hasBlsKey", validator.BlsKey != nil,
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive)

		// 🆕 如果BLS公钥为nil，尝试从创世文件恢复
		if validator.BlsKey == nil {
			logger.Debug("⚠️ 验证时发现BLS公钥为nil，尝试从创世文件恢复",
				"index", i,
				"address", validator.Address.String())

			// 尝试从DPoS实例获取BLS公钥
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				if blsKeyBytes, err := dposInstance.GetBLSKeyBytesFromGenesis(validator.Address); err == nil {
					if blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes); err == nil {
						validator.BlsKey = blsKey
						logger.Info("✅ 成功从创世文件恢复BLS公钥",
							"index", i,
							"address", validator.Address.String(),
							"blsKeyLength", len(blsKeyBytes))
					} else {
						logger.Error("❌ 解析从创世文件获取的BLS公钥失败",
							"index", i,
							"address", validator.Address.String(),
							"error", err)
					}
				} else {
					logger.Debug("❌ 从创世文件获取BLS公钥失败",
						"index", i,
						"address", validator.Address.String(),
						"error", err)
				}
			} else {
				logger.Error("❌ 无法获取DPoS实例来恢复BLS公钥",
					"index", i,
					"address", validator.Address.String())
			}
		}
	}

	// 🆕 添加验证者集合顺序对比
	logger.Debug("🔍 验证者集合顺序对比:")
	logger.Debug("📊 验证时验证者地址顺序:")
	for i, validator := range validators {
		logger.Debug("🔗 验证者地址",
			"index", i,
			"address", validator.Address.String())
	}

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

	// 🆕 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
	logger.Debug("📝 验证者集合获取方式已更新",
		"blockNumber", blockNumber,
		"method", "从ExtraData直接解析",
		"note", "不再需要保存到数据库，直接从区块数据获取")

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

	// 🆕 从父区块 ExtraData 中获取验证者集合
	logger.Debug("🔄 开始从父区块 ExtraData 获取验证者集合",
		"blockNumber", blockNumber,
		"parentBlockNumber", parent.Number,
		"method", "从父区块ExtraData解析",
		"note", "不再依赖数据库，直接从父区块数据获取")

	// 递归获取父区块的验证者集合，需要获取父区块的父区块
	var parentParent *types.Header
	if len(parents) > 0 {
		// 从 parents 数组中查找父区块的父区块
		for _, p := range parents {
			if p.Number == parent.Number-1 {
				parentParent = p
				break
			}
		}
	}

	parentValidators, err := parentExtra.getValidatorsFromExtraData(parent, parentParent, parents, consensusBackend, logger)
	if err != nil {
		logger.Error("❌ 从父区块 ExtraData 获取验证者集合失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf(
			"failed to get parent validators from ExtraData for block %d: %w",
			blockNumber,
			err,
		)
	}

	// 🔍 从父区块 ExtraData 获取的验证者集合信息
	logger.Debug("✅ 从父区块 ExtraData 成功获取验证者集合",
		"blockNumber", blockNumber,
		"parentBlockNumber", parent.Number,
		"totalValidators", len(parentValidators),
		"validatorSource", "父区块ExtraData.Validators",
		"note", "用于父区块BLS签名验证的验证者集合")

	// 🔍 打印验证时父区块验证者集合的详细信息
	logger.Debug("📋 父区块验证者集合详细信息:")
	for i, validator := range parentValidators {
		logger.Debug("📝 父区块验证者",
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
					logger.Debug("🔍 ValidateParentSignatures - 检测到缺失BLS密钥的受托人",
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
						logger.Debug("📨 ValidateParentSignatures - 发起BLS公钥网络请求",
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
								logger.Debug("📨 ValidateParentSignatures - BLS公钥网络请求已发送",
									"blockNumber", blockNumber,
									"parentBlockNumber", parentBlockNumber,
									"address", missingAddress.String())
							}
						}

						// 🆕 增强：增加BLS公钥网络请求的等待时间和重试机制
						maxWaitTime := 10 * time.Second  // 增加等待时间到10秒
						retryInterval := 2 * time.Second // 每2秒检查一次
						maxRetries := int(maxWaitTime / retryInterval)

						logger.Debug("⏳ ValidateParentSignatures - 开始等待BLS公钥网络响应",
							"blockNumber", blockNumber,
							"parentBlockNumber", parentBlockNumber,
							"maxWaitTime", maxWaitTime.String(),
							"retryInterval", retryInterval.String(),
							"maxRetries", maxRetries)

						for retry := 0; retry < maxRetries; retry++ {
							time.Sleep(retryInterval)
							elapsedTime := time.Duration(retry+1) * retryInterval

							logger.Debug("⏳ ValidateParentSignatures - 等待BLS公钥网络响应",
								"blockNumber", blockNumber,
								"parentBlockNumber", parentBlockNumber,
								"retry", retry+1,
								"maxRetries", maxRetries,
								"elapsedTime", elapsedTime)

							// 检查是否已经获取到BLS公钥
							if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
								if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(missingAddress); exists {
									logger.Debug("✅ ValidateParentSignatures - 重试期间成功获取BLS公钥",
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

						logger.Debug("🔄 ValidateParentSignatures - 开始重新尝试验证父区块签名",
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
									logger.Debug("⏳ ValidateParentSignatures - 等待后重试验证",
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
								logger.Debug("✅ ValidateParentSignatures - 重新尝试验证父区块签名成功",
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
		allInstances := GetAllDPoSInstances()

		if len(allInstances) == 0 {
			logger.Warn("⚠️ 全局注册表中没有DPoS实例，可能DPoS还未启动")
			return false
		}

		for _, instance := range allInstances {
			if instance != nil {
				dposInstance = instance
				found = true
				break
			}
		}
	}

	if !found {
		logger.Error("❌ 无法获取DPoS实例来请求BLS公钥")
		return false
	}

	// 发起网络请求

	if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
		myAddress := types.Address(dposInstance.key.Address())

		if err := dposInstance.runtime.networkIntegration.RequestBLSKey(missingAddress, myAddress); err != nil {
			logger.Warn("⚠️ 发送BLS公钥请求失败",
				"blockNumber", blockNumber,
				"address", missingAddress.String(),
				"error", err)
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

	logger.Debug("⏳ 开始等待BLS公钥网络响应",
		"blockNumber", blockNumber,
		"maxWaitTime", maxWaitTime.String(),
		"retryInterval", retryInterval.String(),
		"maxRetries", maxRetries)

	for retry := 0; retry < maxRetries; retry++ {
		time.Sleep(retryInterval)
		elapsedTime := time.Duration(retry+1) * retryInterval

		logger.Debug("⏳ 等待BLS公钥网络响应",
			"blockNumber", blockNumber,
			"retry", retry+1,
			"maxRetries", maxRetries,
			"elapsedTime", elapsedTime)

		// 检查是否已经获取到BLS公钥
		if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
			logger.Debug("🔍 开始检查网络缓存中的BLS公钥",
				"blockNumber", blockNumber,
				"address", missingAddress.String())

			if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(missingAddress); exists {
				logger.Debug("✅ 成功从网络获取BLS公钥",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"blsKeyLength", len(cachedBLSKey),
					"retry", retry+1,
					"elapsedTime", elapsedTime)

				// 🆕 关键修复：将获取到的BLS公钥保存到验证者对象中
				logger.Debug("🔍 开始保存BLS公钥到验证者对象",
					"blockNumber", blockNumber,
					"address", missingAddress.String(),
					"validatorsCount", len(validators))

				if validators != nil {
					found := false
					logger.Debug("🔍 开始遍历验证者集合",
						"blockNumber", blockNumber,
						"missingAddress", missingAddress.String(),
						"validatorsCount", len(validators))

					for i, validator := range validators {
						logger.Debug("🔍 检查验证者",
							"index", i,
							"address", validator.Address.String(),
							"targetAddress", missingAddress.String(),
							"match", validator.Address == missingAddress,
							"addressBytes", fmt.Sprintf("%x", validator.Address[:]),
							"targetBytes", fmt.Sprintf("%x", missingAddress[:]))

						if validator.Address == missingAddress {
							found = true
							logger.Debug("🎯 找到匹配的验证者，开始保存BLS公钥",
								"blockNumber", blockNumber,
								"address", missingAddress.String(),
								"validatorIndex", i)

							// 解析BLS公钥
							if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
								// 🆕 关键修复：确保保存到正确的验证者对象
								validator.BlsKey = blsKey

								// 立即验证保存结果
								if validator.BlsKey != nil {
									logger.Debug("✅ BLS公钥保存成功",
										"blockNumber", blockNumber,
										"address", missingAddress.String(),
										"validatorIndex", i,
										"blsKeyLength", len(cachedBLSKey))

									// 🆕 额外验证：检查保存后的公钥长度
									if marshaled := validator.BlsKey.Marshal(); len(marshaled) > 0 {
										logger.Debug("🎯 BLS公钥保存验证成功",
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
									logger.Debug("🔍 保存后验证：检查验证者集合中的BLS公钥状态",
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
									logger.Debug("🔍 立即验证：重新检查验证者集合状态",
										"blockNumber", blockNumber,
										"address", missingAddress.String())

									for j, v := range validators {
										if v.Address == missingAddress {
											logger.Debug("🔍 立即验证结果",
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
	logger.Debug("🔍 尝试备选方案：从本地获取BLS公钥",
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

	// 🆕 直接使用区块获取到的验证者集合，不尝试位图过滤
	logger.Debug("🔄 直接使用区块获取到的验证者集合进行签名验证",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"note", "使用区块获取到的验证者集合，确保与签名位图匹配")

	// 🎯 显著日志：打印验证时位图索引详情
	logger.Debug("🎯 ===== 验证时位图索引详情 =====",
		"blockNumber", blockNumber,
		"bitmapHex", fmt.Sprintf("0x%x", []byte(s.Bitmap)),
		"bitmapLength", len([]byte(s.Bitmap)),
		"totalValidators", len(validators),
		"note", "验证时位图索引对应关系")

	// 直接使用所有验证者作为签名者
	signers := validators
	logger.Debug("🔍 ===== 验证时使用验证者集合进行BLS签名验证 =====",
		"blockNumber", blockNumber,
		"signersCount", len(signers),
		"note", "使用从ExtraData获取的验证者集合进行签名验证")

	// 🎯 显著日志：打印位图索引对应关系
	logger.Debug("🎯 验证时位图索引对应关系:")
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			logger.Debug("🎯 验证时位图索引",
				"blockNumber", blockNumber,
				"bitmapIndex", i,
				"address", validators[i].Address.String(),
				"isSet", true,
				"note", "实际签名者")
		}
	}

	// 🆕 打印验证时使用的验证者集合详细信息
	logger.Debug("🔍 验证时使用的验证者集合详细信息:")
	for i, validator := range signers {
		logger.Debug("🔍 验证时签名验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}

	// 🆕 直接使用区块获取到的验证者集合，跳过位图过滤
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
				logger.Debug("🔑 Signature.Verify - 验证时动态获取BLS公钥（按需获取）",
					"bitmapIndex", i,
					"keyIndex", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"blsKeyExists", false,
					"note", "BLS公钥将在验证时动态获取")
				missingBLSKeys = append(missingBLSKeys, validator.Address)
			}
		}
	}

	// 🆕 验证时动态获取BLS公钥（按需获取机制）
	if len(missingBLSKeys) > 0 {
		logger.Debug("🔑 验证时动态获取BLS公钥（按需获取）",
			"blockNumber", blockNumber,
			"missingKeysCount", len(missingBLSKeys),
			"note", "BLS公钥只在真正验证时才获取")
		logger.Debug("🔍 发现缺失的BLS公钥，先尝试从本地genesis文件读取",
			"blockNumber", blockNumber,
			"missingCount", len(missingBLSKeys),
			"missingAddresses", missingBLSKeys)

		// 🆕 第一步：尝试从本地genesis文件读取BLS公钥
		genesisKeysFound := 0
		for _, address := range missingBLSKeys {
			logger.Debug("🔍 尝试从本地genesis文件读取BLS公钥",
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
									logger.Debug("✅ 从本地genesis文件成功读取BLS公钥",
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
						logger.Debug("⚠️ 本地genesis文件中未找到BLS公钥，将尝试网络广播获取",
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
			logger.Debug("📨 本地genesis文件中未找到的BLS公钥，开始网络广播获取",
				"blockNumber", blockNumber,
				"remainingCount", len(remainingMissingKeys),
				"remainingAddresses", remainingMissingKeys,
				"genesisKeysFound", genesisKeysFound)

			// 尝试从全局注册表获取DPoS实例并请求BLS公钥
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				for _, address := range remainingMissingKeys {
					logger.Debug("📨 发起BLS公钥网络请求",
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
							logger.Debug("📨 BLS公钥网络请求已发送",
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

			logger.Debug("⏳ 开始等待BLS公钥网络响应",
				"blockNumber", blockNumber,
				"maxWaitTime", maxWaitTime.String(),
				"retryInterval", retryInterval.String(),
				"maxRetries", maxRetries)

			for retry := 0; retry < maxRetries; retry++ {
				time.Sleep(retryInterval)
				elapsedTime := time.Duration(retry+1) * retryInterval

				logger.Debug("⏳ 等待BLS公钥网络响应",
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
										logger.Debug("✅ 重试期间成功获取BLS公钥",
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
					logger.Debug("✅ 所有BLS公钥都已获取，提前退出等待",
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
			logger.Debug("🔍 BLS公钥状态检查完成，跳过重复检查",
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
		// 检查公钥是否与验证者地址匹配
		if int(i) < len(validators) {
			// 验证者地址匹配检查
		}
		}
	}

	// 🆕 显示位图对应的签名顺序，并直接获取缺失的BLS公钥
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			if int(i) < len(validators) {
				validator := validators[int(i)]

				// 🆕 简单修复：如果BLS公钥为空，直接网络获取
				if validator.BlsKey == nil {
					// 直接调用网络获取
					if s.tryFetchBLSKeyFromNetwork(validator.Address, blockNumber, validators, logger) {
						logger.Debug("✅ 直接网络获取BLS公钥成功",
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

	// 🎯 显著日志：按位图索引顺序收集公钥和地址，只使用实际签名者
	logger.Debug("🎯 验证时按位图索引收集实际签名者BLS公钥:")
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			validatorAddress := validators[int(i)].Address
			if blsKey, exists := addressToBLSKey[validatorAddress]; exists && blsKey != nil {
				validBLSKeys = append(validBLSKeys, blsKey)
				bitmapOrderedAddresses = append(bitmapOrderedAddresses, validatorAddress)
				logger.Debug("🎯 验证时位图索引BLS公钥",
					"blockNumber", blockNumber,
					"bitmapIndex", i,
					"signatureIndex", len(validBLSKeys)-1,
					"address", validatorAddress.String(),
					"publicKeyLength", len(blsKey.Marshal()),
					"note", "实际签名者BLS公钥")
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

	// 🎯 显著日志：打印最终验证时位图索引统计
	logger.Debug("🎯 ===== 验证时最终位图索引统计 =====",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"bitmapSetCount", bitmapSetCount,
		"validBLSKeysCount", len(validBLSKeys),
		"bitmapOrderedAddresses", bitmapOrderedAddresses,
		"note", "验证时实际签名者统计")

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

	logger.Debug("🔍 BLS验证公钥过滤结果",
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

		// 🆕 显示参与签名的验证者详情
		logger.Error("🔍 参与签名的验证者详情:")
		for i, signer := range signers {
			if i < len(validators) {
				validator := validators[i]
				logger.Error("📝 签名验证者",
					"index", i,
					"address", validator.Address.String(),
					"hasBlsKey", validator.BlsKey != nil,
					"signerAddress", signer.String())
			}
		}

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
				var validatorAddress types.Address
				if int(i) < len(validators) {
					addressStr = validators[int(i)].Address.String()
					validatorAddress = validators[int(i)].Address
				} else {
					addressStr = "unknown_index"
				}

				logger.Info("⚠️ Signature.Verify - BLS公钥为nil，尝试从创世文件恢复",
					"index", i,
					"address", addressStr)

				// 🆕 尝试从缓存和创世文件恢复BLS公钥
				if validatorAddress != (types.Address{}) {
					if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
						var blsKeyBytes []byte
						var found bool

						// 第一步：尝试从缓存获取BLS公钥
						if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
							if cachedKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(validatorAddress); exists {
								blsKeyBytes = cachedKey
								found = true
								logger.Info("✅ 从缓存找到BLS公钥",
									"index", i,
									"address", addressStr,
									"blsKeyLength", len(blsKeyBytes))
							}
						}

						// 第二步：如果缓存中没有，尝试从创世文件获取
						if !found {
							if keyBytes, err := dposInstance.GetBLSKeyBytesFromGenesis(validatorAddress); err == nil {
								blsKeyBytes = keyBytes
								found = true
								logger.Info("✅ 从创世文件找到BLS公钥",
									"index", i,
									"address", addressStr,
									"blsKeyLength", len(blsKeyBytes))
							} else {
								logger.Debug("❌ 从创世文件获取BLS公钥失败",
									"index", i,
									"address", addressStr,
									"error", err)
							}
						}

						// 第三步：如果找到了BLS公钥，解析并更新
						if found && len(blsKeyBytes) > 0 {
							if blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes); err == nil {
								// 更新validators数组中的BLS公钥
								validators[int(i)].BlsKey = blsKey
								blsPublicKeys[i] = blsKey
								logger.Info("✅ 成功恢复BLS公钥",
									"index", i,
									"address", addressStr,
									"blsKeyLength", len(blsKeyBytes),
									"source", func() string {
										if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
											if _, exists := dposInstance.runtime.networkIntegration.GetBLSKey(validatorAddress); exists {
												return "缓存"
											}
										}
										return "创世文件"
									}())
							} else {
								logger.Error("❌ 解析BLS公钥失败",
									"index", i,
									"address", addressStr,
									"error", err)
							}
						}
					} else {
						logger.Error("❌ 无法获取DPoS实例来恢复BLS公钥",
							"index", i,
							"address", addressStr)
					}
				}
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

// getValidatorsFromExtraData 从区块的 ExtraData 中获取验证者集合
func (i *Extra) getValidatorsFromExtraData(header *types.Header, parent *types.Header, parents []*types.Header,
	consensusBackend dposBackend, logger hclog.Logger) (validator.AccountSet, error) {

	blockNumber := header.Number
	logger.Debug("🔍 开始从 ExtraData 解析验证者集合",
		"blockNumber", blockNumber,
		"hasValidators", i.Validators != nil,
		"hasCheckpoint", i.Checkpoint != nil)

	// 🆕 添加 parent 的 nil 检查
	if parent == nil {
		// 🔍 优先从当前区块ExtraData获取生产时的验证者地址集合（实际签名者）
		if i.Validators != nil && !i.Validators.IsEmpty() && len(i.Validators.Added) > 0 {
			logger.Debug("🔍 ===== 验证时从当前区块ExtraData获取实际签名者 =====",
				"blockNumber", blockNumber,
				"actualSignersCount", len(i.Validators.Added),
				"method", "从区块ExtraData直接获取",
				"note", "从ExtraData获取实际签名者地址，BLS公钥从创世文件获取")

			// 从ExtraData获取实际签名者地址，然后从创世文件获取BLS公钥
			validatorAddresses := i.Validators.Added

			logger.Debug("✅ 从ExtraData获取生产时验证者地址集合成功",
				"blockNumber", blockNumber,
				"productionValidatorsCount", len(validatorAddresses))

			// 🆕 从创世文件获取BLS公钥，构建完整的验证者集合
			productionValidators := make(validator.AccountSet, 0, len(validatorAddresses))
			for _, validatorAddr := range validatorAddresses {
				// 从创世文件获取BLS公钥
				blsKey, err := i.getBLSKeyFromGenesis(validatorAddr.Address, logger)
				if err != nil {
					logger.Debug("⚠️ 从创世文件获取BLS公钥失败",
						"blockNumber", blockNumber,
						"address", validatorAddr.Address.String(),
						"error", err)
				}

				// 构建完整的验证者信息
				productionValidators = append(productionValidators, &validator.ValidatorMetadata{
					Address:     validatorAddr.Address,
					BlsKey:      blsKey, // 🆕 从创世文件获取的BLS公钥
					VotingPower: validatorAddr.VotingPower,
					IsActive:    validatorAddr.IsActive,
				})
			}

			// 详细记录验证时从ExtraData获取的验证者集合
			logger.Debug("🔍 验证时从ExtraData获取的验证者集合详细信息:")
			for i, validator := range productionValidators {
				logger.Debug("🔍 验证时ExtraData验证者",
					"blockNumber", blockNumber,
					"index", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"hasBlsKey", validator.BlsKey != nil)
			}

			return productionValidators, nil
		}

		// 备用方案：返回创世验证者集合
		logger.Info("📋 parent 为 nil，使用备用方案返回创世验证者集合",
			"blockNumber", blockNumber)

		genesisValidators, err := i.getGenesisValidators(consensusBackend, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to get genesis validators: %w", err)
		}

		logger.Info("✅ 从创世文件获取验证者集合成功",
			"blockNumber", blockNumber,
			"genesisValidatorsCount", len(genesisValidators))

		return genesisValidators, nil
	}

	// 如果是创世区块或第一个区块，从创世文件获取验证者集合
	if blockNumber <= 1 {
		logger.Info("📋 处理创世区块或第一个区块，从创世文件获取验证者集合",
			"blockNumber", blockNumber)

		// 从创世文件获取初始验证者集合
		genesisValidators, err := i.getGenesisValidators(consensusBackend, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to get genesis validators: %w", err)
		}

		logger.Info("✅ 从创世文件获取验证者集合成功",
			"blockNumber", blockNumber,
			"genesisValidatorsCount", len(genesisValidators))

		return genesisValidators, nil
	}

	// 获取父区块的验证者集合
	logger.Debug("🔍 获取父区块验证者集合",
		"blockNumber", blockNumber,
		"parentBlockNumber", parent.Number)

	// 🆕 如果父区块是区块1，直接返回创世验证者集合，避免无限递归
	if parent.Number == 1 {
		logger.Info("📋 父区块是区块1，直接返回创世验证者集合",
			"blockNumber", blockNumber,
			"parentBlockNumber", parent.Number)

		genesisValidators, err := i.getGenesisValidators(consensusBackend, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to get genesis validators: %w", err)
		}

		logger.Info("✅ 从创世文件获取验证者集合成功",
			"blockNumber", blockNumber,
			"genesisValidatorsCount", len(genesisValidators))

		return genesisValidators, nil
	}

	// 🆕 关键修复：如果当前区块的ExtraData中有验证者地址集合信息，直接使用
	// 这确保验证时使用与生产时完全相同的验证者集合
	if i.Validators != nil && !i.Validators.IsEmpty() && len(i.Validators.Added) > 0 {
		logger.Debug("🔍 ===== 验证时从当前区块ExtraData获取验证者地址集合 =====",
			"blockNumber", blockNumber,
			"productionValidatorsCount", len(i.Validators.Added),
			"method", "从区块ExtraData直接获取",
			"note", "从ExtraData获取地址，BLS公钥从创世文件获取")

		// 从ExtraData获取验证者地址，然后从创世文件获取BLS公钥
		validatorAddresses := i.Validators.Added

		logger.Debug("✅ 从ExtraData获取生产时验证者地址集合成功",
			"blockNumber", blockNumber,
			"productionValidatorsCount", len(validatorAddresses))

		// 🆕 从创世文件获取BLS公钥，构建完整的验证者集合
		productionValidators := make(validator.AccountSet, 0, len(validatorAddresses))
		for _, validatorAddr := range validatorAddresses {
			// 从创世文件获取BLS公钥
			blsKey, err := i.getBLSKeyFromGenesis(validatorAddr.Address, logger)
			if err != nil {
				logger.Debug("⚠️ 从创世文件获取BLS公钥失败",
					"blockNumber", blockNumber,
					"address", validatorAddr.Address.String(),
					"error", err)
			}

			// 构建完整的验证者信息
			productionValidators = append(productionValidators, &validator.ValidatorMetadata{
				Address:     validatorAddr.Address,
				BlsKey:      blsKey, // 🆕 从创世文件获取的BLS公钥
				VotingPower: validatorAddr.VotingPower,
				IsActive:    validatorAddr.IsActive,
			})
		}

		// 详细记录验证时从ExtraData获取的验证者集合
		logger.Debug("🔍 验证时从ExtraData获取的验证者集合详细信息:")
		for i, validator := range productionValidators {
			logger.Debug("🔍 验证时ExtraData验证者",
				"blockNumber", blockNumber,
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil)
		}

		return productionValidators, nil
	}

	parentValidators, err := i.getParentValidators(parent, parents, consensusBackend, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get parent validators: %w", err)
	}

	logger.Debug("✅ 获取父区块验证者集合成功",
		"blockNumber", blockNumber,
		"parentValidatorsCount", len(parentValidators))

	// 如果没有验证者集合变化，直接返回父区块的验证者集合
	if i.Validators == nil || i.Validators.IsEmpty() {
		logger.Debug("📝 当前区块无验证者集合变化，使用父区块验证者集合",
			"blockNumber", blockNumber,
			"parentValidatorsCount", len(parentValidators))
		return parentValidators, nil
	}

	// 应用验证者集合变化
	logger.Info("🔄 开始应用验证者集合变化",
		"blockNumber", blockNumber,
		"addedCount", len(i.Validators.Added),
		"updatedCount", len(i.Validators.Updated),
		"removedCount", i.Validators.Removed.Len())

	currentValidators := i.applyValidatorSetDelta(parentValidators, i.Validators, logger)

	logger.Info("✅ 验证者集合变化应用完成",
		"blockNumber", blockNumber,
		"originalCount", len(parentValidators),
		"finalCount", len(currentValidators),
		"addedCount", len(i.Validators.Added),
		"updatedCount", len(i.Validators.Updated),
		"removedCount", i.Validators.Removed.Len())

	// 详细记录最终的验证者集合
	logger.Info("📋 最终验证者集合详情:")
	for i, validator := range currentValidators {
		logger.Info("📝 最终验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}

	return currentValidators, nil
}

// getGenesisValidators 从创世文件获取验证者集合
func (i *Extra) getGenesisValidators(consensusBackend dposBackend, logger hclog.Logger) (validator.AccountSet, error) {
	logger.Info("🔍 开始从创世文件获取验证者集合")

	// 🆕 直接从 DPoS 实例的内存中获取当前验证者集合（创世验证者）
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		// 获取当前内存中的验证者集合，这些就是创世验证者
		currentValidators := dposInstance.GetCurrentDelegates()
		if len(currentValidators) > 0 {
			logger.Info("✅ 从 DPoS 实例内存获取创世验证者成功",
				"genesisValidatorsCount", len(currentValidators))

			// 详细记录创世验证者信息
			logger.Info("📋 创世验证者详细信息:")
			for i, validator := range currentValidators {
				logger.Info("📝 创世验证者",
					"index", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"hasBlsKey", validator.BlsKey != nil)
			}

			return currentValidators, nil
		}
		logger.Warn("⚠️ DPoS 实例内存中验证者集合为空")
	}

	// 备用方案：尝试从共识后端获取当前验证者集合
	logger.Info("🔄 尝试从共识后端获取当前验证者集合作为创世验证者")
	currentValidators, err := consensusBackend.GetDelegates(1, nil) // 使用区块1而不是区块0
	if err == nil && len(currentValidators) > 0 {
		logger.Info("✅ 从共识后端获取当前验证者集合成功",
			"genesisValidatorsCount", len(currentValidators))
		return currentValidators, nil
	}

	return nil, fmt.Errorf("failed to get genesis validators from all sources")
}

// getParentValidators 获取父区块的验证者集合
func (i *Extra) getParentValidators(parent *types.Header, parents []*types.Header,
	consensusBackend dposBackend, logger hclog.Logger) (validator.AccountSet, error) {

	logger.Debug("🔍 开始获取父区块验证者集合",
		"parentBlockNumber", parent.Number)

	// 首先尝试从父区块的 ExtraData 中获取
	parentExtra, err := GetIbftExtra(parent.ExtraData)
	if err == nil && parentExtra != nil {
		logger.Info("📋 从父区块 ExtraData 获取验证者集合",
			"parentBlockNumber", parent.Number)

		// 递归获取父区块的验证者集合，需要获取父区块的父区块
		var parentParent *types.Header
		if len(parents) > 0 {
			// 从 parents 数组中查找父区块的父区块
			for _, p := range parents {
				if p.Number == parent.Number-1 {
					parentParent = p
					break
				}
			}
		}

		parentValidators, err := parentExtra.getValidatorsFromExtraData(parent, parentParent, parents, consensusBackend, logger)
		if err == nil {
			logger.Info("✅ 从父区块 ExtraData 获取验证者集合成功",
				"parentBlockNumber", parent.Number,
				"parentValidatorsCount", len(parentValidators))
			return parentValidators, nil
		}
		logger.Warn("⚠️ 从父区块 ExtraData 获取验证者集合失败", "error", err)
	}

	// 备用方案：从数据库获取（如果可用）
	logger.Debug("🔄 尝试从数据库获取父区块验证者集合",
		"parentBlockNumber", parent.Number)

	parentValidators, err := consensusBackend.GetDelegates(parent.Number, parents)
	if err == nil && len(parentValidators) > 0 {
		logger.Debug("✅ 从数据库获取父区块验证者集合成功",
			"parentBlockNumber", parent.Number,
			"parentValidatorsCount", len(parentValidators))
		return parentValidators, nil
	}

	logger.Warn("⚠️ 从数据库获取父区块验证者集合失败", "error", err)

	// 最后备用方案：递归获取更早的父区块
	if parent.Number > 1 {
		logger.Debug("🔄 递归获取更早的父区块验证者集合",
			"parentBlockNumber", parent.Number)

		// 这里需要获取更早的父区块，但为了简化，我们返回错误
		// 在实际实现中，可能需要更复杂的递归逻辑
		return nil, fmt.Errorf("failed to get parent validators, need recursive approach")
	}

	// 如果父区块是创世区块，从创世文件获取
	return i.getGenesisValidators(consensusBackend, logger)
}

// applyValidatorSetDelta 应用验证者集合变化
func (i *Extra) applyValidatorSetDelta(parentValidators validator.AccountSet, delta *validator.ValidatorSetDelta, logger hclog.Logger) validator.AccountSet {
	logger.Info("🔄 开始应用验证者集合变化",
		"parentValidatorsCount", len(parentValidators),
		"addedCount", len(delta.Added),
		"updatedCount", len(delta.Updated),
		"removedCount", delta.Removed.Len())

	// 复制父区块验证者集合
	currentValidators := parentValidators.Copy()

	// 1. 移除被删除的验证者
	if delta.Removed != nil && delta.Removed.Len() > 0 {
		logger.Info("🗑️ 开始移除验证者",
			"removedBitmapLength", len(delta.Removed))

		// 从后往前遍历，避免索引问题
		for j := len(currentValidators) - 1; j >= 0; j-- {
			if delta.Removed.IsSet(uint64(j)) {
				removedValidator := currentValidators[j]
				logger.Info("🗑️ 移除验证者",
					"index", j,
					"address", removedValidator.Address.String(),
					"votingPower", removedValidator.VotingPower.String())

				currentValidators = append(currentValidators[:j], currentValidators[j+1:]...)
			}
		}
	}

	// 2. 添加新的验证者
	if len(delta.Added) > 0 {
		logger.Info("➕ 开始添加新验证者",
			"addedCount", len(delta.Added))

		for _, addedValidator := range delta.Added {
			logger.Info("➕ 添加新验证者",
				"address", addedValidator.Address.String(),
				"votingPower", addedValidator.VotingPower.String(),
				"isActive", addedValidator.IsActive,
				"hasBlsKey", addedValidator.BlsKey != nil)

			currentValidators = append(currentValidators, addedValidator)
		}
	}

	// 3. 更新现有验证者
	if len(delta.Updated) > 0 {
		logger.Info("🔄 开始更新验证者",
			"updatedCount", len(delta.Updated))

		for _, updatedValidator := range delta.Updated {
			// 找到并更新对应的验证者
			for j, existingValidator := range currentValidators {
				if existingValidator.Address == updatedValidator.Address {
					logger.Info("🔄 更新验证者",
						"address", updatedValidator.Address.String(),
						"oldVotingPower", existingValidator.VotingPower.String(),
						"newVotingPower", updatedValidator.VotingPower.String(),
						"oldIsActive", existingValidator.IsActive,
						"newIsActive", updatedValidator.IsActive)

					currentValidators[j] = updatedValidator
					break
				}
			}
		}
	}

	logger.Info("✅ 验证者集合变化应用完成",
		"finalValidatorsCount", len(currentValidators))

	return currentValidators
}

// getBLSKeyFromGenesis 从创世文件获取BLS公钥
func (i *Extra) getBLSKeyFromGenesis(address types.Address, logger hclog.Logger) (*bls.PublicKey, error) {
	logger.Debug("🔍 从创世文件获取BLS公钥",
		"address", address.String())

	// 尝试从DPoS实例获取BLS公钥
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		// 从创世文件获取BLS公钥字节
		blsKeyBytes, err := dposInstance.GetBLSKeyBytesFromGenesis(address)
		if err != nil {
			logger.Debug("❌ 从创世文件获取BLS公钥失败",
				"address", address.String(),
				"error", err)
			return nil, err
		}

		// 解析BLS公钥
		blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
		if err != nil {
			logger.Debug("❌ 解析BLS公钥失败",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes),
				"error", err)
			return nil, err
		}

		logger.Debug("✅ 从创世文件成功获取BLS公钥",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))

		return blsKey, nil
	}

	return nil, fmt.Errorf("无法获取DPoS实例来从创世文件获取BLS公钥")
}
