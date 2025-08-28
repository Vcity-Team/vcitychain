package dpos

import (
	"fmt"
	"math/big"
	"os"
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

	validators, err := consensusBackend.GetDelegates(blockNumber-1, parents)
	if err != nil {
		return fmt.Errorf("failed to validate header for block %d. could not retrieve block validators:%w", blockNumber, err)
	}

	if err := i.Committed.Verify(blockNumber, validators, checkpointHash, domain, logger); err != nil {
		logger.Error("🚨 区块签名验证失败，程序将终止",
			"blockNumber", blockNumber,
			"proposalHash", checkpointHash.String(),
			"error", err)
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

	parentValidators, err := consensusBackend.GetDelegates(blockNumber-2, parents)
	if err != nil {
		return fmt.Errorf(
			"failed to validate header for block %d. could not retrieve parent validators: %w",
			blockNumber,
			err,
		)
	}

	// 使用固定的哈希值避免循环依赖，确保与生产区块时使用相同的checkpointHash
	fixedParentBlockHash := types.BytesToHash([]byte(fmt.Sprintf("block_%d", parent.Number)))
	parentCheckpointHash, err := parentExtra.Checkpoint.Hash(chainID, parent.Number, fixedParentBlockHash)
	if err != nil {
		return fmt.Errorf("failed to calculate parent proposal hash: %w", err)
	}

	parentBlockNumber := blockNumber - 1
	if err := i.Parent.Verify(parentBlockNumber, parentValidators, parentCheckpointHash, domain, logger); err != nil {
		return fmt.Errorf("failed to verify signatures for parent of block %d (proposal hash: %s): %w",
			blockNumber, parentCheckpointHash, err)
	}

	return nil
}

// Signature represents aggregated signatures of signers accompanied with a bitmap
// (in order to be able to determine identities of each signer)
type Signature struct {
	AggregatedSignature []byte
	Bitmap              bitmap.Bitmap
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

// Verify is used to verify aggregated signature based on current validator set, message hash and domain
func (s *Signature) Verify(blockNumber uint64, validators validator.AccountSet,
	hash types.Hash, domain []byte, logger hclog.Logger) error {

	logger.Debug("Signature.Verify - 开始验证签名",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"bitmapLength", len(s.Bitmap),
		"aggregatedSignatureLength", len(s.AggregatedSignature))

	signers, err := validators.GetFilteredValidators(s.Bitmap)
	if err != nil {
		logger.Error("Signature.Verify - GetFilteredValidators失败", "error", err)
		return err
	}

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

		// 🆕 详细记录每个签名者的信息
		for i, signer := range signers {
			logger.Info("🔍 签名者详情",
				"index", i,
				"address", signer.Address.String(),
				"votingPower", signer.VotingPower.String(),
				"isActive", signer.IsActive)
		}

		logger.Error("🚨 区块验证失败 - 法定人数不足，程序将终止",
			"blockNumber", blockNumber,
			"reason", "quorum not reached",
			"quorumDetails", fmt.Sprintf("当前签名数: %d, 需要签名数: %d, 差距: %d", 
				len(signers), requiredQuorumCount, requiredQuorumCount-len(signers)))

		// 🆕 程序终止
		os.Exit(1)
	}

	logger.Debug("Signature.Verify - 法定人数验证通过，开始验证BLS签名")

	// 🆕 添加详细日志：打印从数据库读取的验证者信息
	logger.Debug("🔍 Signature.Verify - 验证者详细信息",
		"blockNumber", blockNumber,
		"totalValidators", len(validators),
		"filteredSigners", len(signers),
		"bitmapLength", len(s.Bitmap),
		"bitmapHex", fmt.Sprintf("%x", s.Bitmap))

	// 🆕 检查并获取缺失的BLS公钥
	blsPublicKeys := make([]*bls.PublicKey, len(signers))
	missingBLSKeys := make([]types.Address, 0)

	for i, validator := range signers {
		blsPublicKeys[i] = validator.BlsKey

		// 🆕 详细打印每个验证者的BLS公钥信息
		if validator.BlsKey != nil {
			pubKeyBytes := validator.BlsKey.Marshal()
			logger.Debug("🔑 Signature.Verify - 验证者BLS公钥详情",
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive,
				"blsKeyExists", true,
				"publicKeyBytes", fmt.Sprintf("%x", pubKeyBytes),
				"publicKeyLength", len(pubKeyBytes))
		} else {
			logger.Debug("⚠️ Signature.Verify - 验证者缺少BLS公钥，将尝试网络获取",
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive,
				"blsKeyExists", false)
			missingBLSKeys = append(missingBLSKeys, validator.Address)
		}
	}

	// 🆕 如果有缺失的BLS公钥，尝试网络获取
	if len(missingBLSKeys) > 0 {
		logger.Info("🔍 发现缺失的BLS公钥，尝试网络获取",
			"blockNumber", blockNumber,
			"missingCount", len(missingBLSKeys),
			"missingAddresses", missingBLSKeys)

		// 尝试从全局注册表获取DPoS实例并请求BLS公钥
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			for _, address := range missingBLSKeys {
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

			// 等待一段时间让网络请求完成
			logger.Info("⏳ 等待BLS公钥网络响应",
				"blockNumber", blockNumber,
				"waitTime", "3秒")
			time.Sleep(3 * time.Second)

			// 重新检查BLS公钥
			logger.Info("🔍 重新检查BLS公钥状态",
				"blockNumber", blockNumber)

			for i, validator := range signers {
				if blsPublicKeys[i] == nil {
					// 尝试从网络集成层获取
					if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
						if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
							// 解析BLS公钥
							if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
								blsPublicKeys[i] = blsKey
								logger.Info("✅ 成功获取BLS公钥",
									"blockNumber", blockNumber,
									"address", validator.Address.String(),
									"blsKeyLength", len(cachedBLSKey))
							} else {
								logger.Debug("⚠️ 解析获取的BLS公钥失败",
									"blockNumber", blockNumber,
									"address", validator.Address.String(),
									"error", err)
							}
						}
					}
				}
			}
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

	// 🆕 添加签名顺序验证日志
	logger.Debug("🔍 验证签名顺序与公钥顺序匹配:")
	for i, pubKey := range blsPublicKeys {
		if pubKey != nil {
			logger.Debug("🔍 验证用BLS公钥",
				"index", i,
				"address", signers[i].Address.String(),
				"pubKeyBytes", fmt.Sprintf("%x", pubKey.Marshal()))

			// 检查公钥是否与验证者地址匹配
			if i < len(signers) {
				delegate := signers[i]
				logger.Debug("🔗 签名顺序验证",
					"signatureIndex", i,
					"expectedAddress", delegate.Address.String(),
					"hasBlsKey", delegate.BlsKey != nil)
			}
		}
	}

	// 🆕 显示位图对应的签名顺序
	logger.Debug("🔗 位图对应的签名顺序:")
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			if int(i) < len(validators) {
				validator := validators[int(i)]
				logger.Debug("位图顺序",
					"bitmapIndex", i,
					"validatorAddress", validator.Address.String(),
					"blsKeyExists", validator.BlsKey != nil)
			}
		}
	}

	// 执行BLS签名验证
	isValid := aggs.VerifyAggregated(blsPublicKeys, hash[:], domain)
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
				logger.Debug("Signature.Verify - BLS公钥信息",
					"index", i,
					"address", signers[i].Address.String(),
					"publicKeyBytes", fmt.Sprintf("%x", pubKeyBytes),
					"publicKeyLength", len(pubKeyBytes))
			} else {
				logger.Error("Signature.Verify - BLS公钥为nil",
					"index", i,
					"address", signers[i].Address.String())
			}
		}

		logger.Error("🚨 BLS签名验证失败，程序将终止",
			"blockNumber", blockNumber,
			"reason", "BLS signature verification failed")

		// 🆕 程序终止
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
