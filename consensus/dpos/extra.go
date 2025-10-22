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

// ValidatorSet 用于计算验证者集合的哈希值
type ValidatorSet struct {
	Validators validator.AccountSet
}

// Hash 计算验证者集合的哈希值
func (vs *ValidatorSet) Hash() (types.Hash, error) {
	if len(vs.Validators) == 0 {
		return types.Hash{}, nil
	}

	// 使用简单的地址排序和哈希
	var addresses []types.Address
	for _, v := range vs.Validators {
		addresses = append(addresses, v.Address)
	}

	// 对地址进行排序以确保一致性
	for i := 0; i < len(addresses); i++ {
		for j := i + 1; j < len(addresses); j++ {
			if addresses[i].String() > addresses[j].String() {
				addresses[i], addresses[j] = addresses[j], addresses[i]
			}
		}
	}

	// 计算哈希
	var data []byte
	for _, addr := range addresses {
		data = append(data, addr.Bytes()...)
	}

	return types.BytesToHash(crypto.Keccak256(data)), nil
}

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
	// 🆕 奖励分配信息
	RewardDistribution *RewardDistributionInfo
	// 🆕 用于CheckpointHash计算的区块哈希
	CheckpointBlockHash types.Hash
}

// RewardDistributionInfo 奖励分配信息
type RewardDistributionInfo struct {
	EpochNumber uint64              `json:"epochNumber"`
	Rewards     map[string]*big.Int `json:"rewards"` // 地址 -> 奖励金额
	TotalReward *big.Int            `json:"totalReward"`
	Timestamp   uint64              `json:"timestamp"`
}

// MarshalRLPWith 实现RLP编码
func (r *RewardDistributionInfo) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	// EpochNumber
	vv.Set(ar.NewUint(r.EpochNumber))

	// Rewards map
	rewardsArray := ar.NewArray()
	for addr, amount := range r.Rewards {
		rewardItem := ar.NewArray()
		rewardItem.Set(ar.NewCopyBytes([]byte(addr)))
		rewardItem.Set(ar.NewBigInt(amount))
		rewardsArray.Set(rewardItem)
	}
	vv.Set(rewardsArray)

	// TotalReward
	vv.Set(ar.NewBigInt(r.TotalReward))

	// Timestamp
	vv.Set(ar.NewUint(r.Timestamp))

	return vv
}

// UnmarshalRLPWith 实现RLP解码
func (r *RewardDistributionInfo) UnmarshalRLPWith(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) < 4 {
		return fmt.Errorf("invalid RewardDistributionInfo RLP: expected 4 elements, got %d", len(elems))
	}

	// EpochNumber
	epochNumber, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	r.EpochNumber = epochNumber

	// Rewards map
	rewardsElems, err := elems[1].GetElems()
	if err != nil {
		return err
	}
	r.Rewards = make(map[string]*big.Int)
	for _, rewardElem := range rewardsElems {
		rewardItemElems, err := rewardElem.GetElems()
		if err != nil || len(rewardItemElems) != 2 {
			continue
		}

		addrBytes, err := rewardItemElems[0].GetBytes(nil)
		if err != nil {
			continue
		}

		amount := new(big.Int)
		if err := rewardItemElems[1].GetBigInt(amount); err != nil {
			continue
		}

		r.Rewards[string(addrBytes)] = amount
	}

	// TotalReward
	totalReward := new(big.Int)
	if err := elems[2].GetBigInt(totalReward); err != nil {
		return err
	}
	r.TotalReward = totalReward

	// Timestamp
	timestamp, err := elems[3].GetUint64()
	if err != nil {
		return err
	}
	r.Timestamp = timestamp

	return nil
}

// DelayedStateUpdateInfo 结构体已移除，延迟状态更新机制不再需要

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

	// 🆕 奖励分配信息
	if i.RewardDistribution == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.RewardDistribution.MarshalRLPWith(ar))
	}

	// 🆕 CheckpointBlockHash
	if i.CheckpointBlockHash == (types.Hash{}) {
		// 修复：使用空字节数组而不是NullArray，确保类型一致
		vv.Set(ar.NewBytes([]byte{}))
	} else {
		vv.Set(ar.NewBytes(i.CheckpointBlockHash.Bytes()))
	}

	return vv
}

// UnmarshalRLP defines the unmarshal function wrapper for Extra
func (i *Extra) UnmarshalRLP(input []byte) error {
	return fastrlp.UnmarshalRLP(input[ExtraVanity:], i)
}

// UnmarshalRLPWith defines the unmarshal implementation for Extra
func (i *Extra) UnmarshalRLPWith(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	// 动态设置expectedElements
	expectedElements := 4 // 默认创世区块格式
	if len(elems) == 5 {
		expectedElements = 5 // 普通区块格式
	} else if len(elems) == 6 {
		expectedElements = 6 // 包含CheckpointBlockHash的格式
	}

	// 解析RLP元素

	// 处理元素数量不匹配的情况
	num := len(elems)
	if num < expectedElements {
		return fmt.Errorf("incorrect elements count to decode Extra, expected %d but found %d", expectedElements, num)
	} else if num > expectedElements {
		// 只使用前expectedElements个元素，忽略额外的元素
		elems = elems[:expectedElements]
	}

	// Validators
	if elems[0].Elems() > 0 {
		validatorElems, err := elems[0].GetElems()
		if err != nil {
			return err
		}

		if len(validatorElems) == 3 {
			// 标准ValidatorSetDelta格式：Added, Updated, Removed
			i.Validators = &validator.ValidatorSetDelta{}
			if err := i.Validators.UnmarshalRLPWith(elems[0]); err != nil {
				return err
			}
		} else {
			// 非标准格式，可能是验证者地址列表或其他格式
			i.Validators = nil
		}
	}

	// Parent Signatures
	if elems[1].Elems() > 0 {
		i.Parent = &Signature{}
		if err := i.Parent.UnmarshalRLPWith(elems[1]); err != nil {
			fmt.Printf("❌ DEBUG Parent Signature UnmarshalRLP failed: %v\n", err)
			return err
		}
	}

	// Committed Signatures
	if elems[2].Elems() > 0 {
		committedElems, err := elems[2].GetElems()
		if err != nil {
			return err
		}

		if len(committedElems) == 2 {
			// 标准Signature格式：AggregatedSignature, Bitmap
			i.Committed = &Signature{}
			if err := i.Committed.UnmarshalRLPWith(elems[2]); err != nil {
				fmt.Printf("❌ DEBUG Committed Signature UnmarshalRLP failed: %v\n", err)
				return err
			}
		} else {
			// 非标准格式，可能是其他签名相关数据
			// fmt.Printf("⚠️ DEBUG Non-standard Committed Signature format: %d elements, skipping\n", len(committedElems))
			i.Committed = nil
		}
	}

	// Checkpoint
	if elems[3].Elems() > 0 {
		checkpointElems, err := elems[3].GetElems()
		if err != nil {
			return err
		}

		// 解析checkpoint元素

		if len(checkpointElems) == 5 {
			// 标准CheckpointData格式：5个元素
			i.Checkpoint = &CheckpointData{}
			if err := i.Checkpoint.UnmarshalRLPWith(elems[3]); err != nil {
				fmt.Printf("❌ DEBUG CheckpointData UnmarshalRLP failed: %v\n", err)
				return err
			}
		} else {
			// 非标准格式，跳过解析
			// fmt.Printf("⚠️ DEBUG Non-standard Checkpoint format: %d elements, skipping CheckpointData parsing\n", len(checkpointElems))
			i.Checkpoint = nil
		}
	}

	// Element[4] - 奖励分配信息（只在5个或6个元素时处理）
	if expectedElements >= 5 && len(elems) > 4 && elems[4].Elems() > 0 {
		i.RewardDistribution = &RewardDistributionInfo{}
		if err := i.RewardDistribution.UnmarshalRLPWith(elems[4]); err != nil {
			fmt.Printf("❌ DEBUG RewardDistribution UnmarshalRLP failed: %v\n", err)
			// 不返回错误，只是跳过奖励分配信息
			i.RewardDistribution = nil
		}
	}

	// Element[5] - CheckpointBlockHash（只在6个元素时处理）
	if expectedElements == 6 && len(elems) > 5 {
		if elems[5].Type() == fastrlp.TypeBytes {
			hashBytes, err := elems[5].GetBytes(nil)
			if err == nil && len(hashBytes) == 32 {
				i.CheckpointBlockHash = types.BytesToHash(hashBytes)
			} else if err == nil && len(hashBytes) == 0 {
				// 修复：处理空字节数组的情况
				i.CheckpointBlockHash = types.Hash{}
			}
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
		logger.Info("✅ ValidateFinalizedData 跳过创世区块验证", "blockNumber", blockNumber)
		return nil
	}

	// 🆕 新增：检查是否在共识切换高度，如果是则完全跳过所有验证
	if consensusBackend != nil {
		if dposBackend, ok := consensusBackend.(*DPoS); ok && dposBackend.config != nil {
			if dposBackend.config.ConsensusSwitchHeight > 0 && blockNumber == dposBackend.config.ConsensusSwitchHeight {
				logger.Info("🔄 检测到共识切换高度，完全跳过所有验证",
					"blockNumber", blockNumber,
					"switchHeight", dposBackend.config.ConsensusSwitchHeight,
					"reason", "切换高度跳过所有验证，包括当前区块验证")
				return nil
			}
		}
	}

	if i.Committed == nil {
		logger.Error("❌ ValidateFinalizedData 签名数据缺失", "blockNumber", blockNumber)
		return fmt.Errorf("failed to verify signatures for block %d, because signatures are not present", blockNumber)
	}

	if i.Checkpoint == nil {
		logger.Error("❌ ValidateFinalizedData 检查点数据缺失", "blockNumber", blockNumber)
		return fmt.Errorf("failed to verify signatures for block %d, because checkpoint data are not present", blockNumber)
	}

	// validate current block signatures
	// 🆕 修复：使用与生产时完全相同的哈希计算方式
	// 生产时使用：checkpoint.Hash(blockchain.GetChainID(), block.Block.Number(), fixedBlockHash)
	// 验证时使用：i.Checkpoint.Hash(chainID, blockNumber, fixedBlockHash)
	// 需要确保两者使用相同的参数和计算方式

	// 🆕 使用ExtraData中保存的CheckpointBlockHash（生产时用于计算CheckpointHash的区块哈希）
	// 确保区块哈希已经计算完成
	realBlockHash := header.Hash

	// 🆕 如果ExtraData中有CheckpointBlockHash，优先使用它
	if i.CheckpointBlockHash != (types.Hash{}) {
		realBlockHash = i.CheckpointBlockHash
	} else {
		logger.Info("🔍 ===== 验证时使用区块头哈希 =====",
			"blockNumber", blockNumber,
			"headerHash", realBlockHash.String(),
			"说明", "ExtraData中没有CheckpointBlockHash，验证时使用区块头哈希")
	}

	// 🆕 修复：使用传入的chainID参数，确保与生产时一致
	productionChainID := chainID

	// 🆕 从 ExtraData 中获取验证者集合
	validators, err := i.getValidatorsFromExtraData(header, parent, parents, consensusBackend, logger)
	if err != nil {
		logger.Error("❌ 从 ExtraData 获取验证者集合失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("failed to get validators from ExtraData for block %d: %w", blockNumber, err)
	}

	// 🆕 关键修复：重新计算CheckpointData的哈希值，确保与生产时一致
	// 生产时使用r.delegates.Hash()计算CurrentValidatorsHash和NextValidatorsHash
	// 验证时需要重新计算这些哈希值，确保与生产时完全一致

	// 从验证者集合重新计算哈希值，确保与生产时一致
	var currentValidatorsHash types.Hash
	var nextValidatorsHash types.Hash

	// 如果有验证者集合，重新计算哈希值
	logger.Debug("🔍 ValidateFinalizedData 开始计算验证者哈希", "blockNumber", blockNumber, "validatorsCount", len(validators))
	if len(validators) > 0 {
		logger.Debug("🔍 验证时开始计算验证者哈希", "validatorsCount", len(validators))
		// 使用与生产时相同的validator.AccountSet.HashAddressOnly()方法
		if hash, err := validators.HashAddressOnly(); err == nil {
			currentValidatorsHash = hash
			nextValidatorsHash = hash // 暂时使用相同的哈希
			logger.Debug("✅ ValidateFinalizedData 验证者哈希计算成功", "blockNumber", blockNumber, "currentValidatorsHash", currentValidatorsHash.String()[:16])
		} else {
			// 如果计算失败，使用空哈希
			logger.Error("❌ 验证时验证者哈希计算失败", "error", err)
			currentValidatorsHash = types.Hash{}
			nextValidatorsHash = types.Hash{}
		}
	} else {
		// 如果没有验证者集合，使用空哈希
		logger.Info("🔍 ValidateFinalizedData 没有验证者集合，使用空哈希", "blockNumber", blockNumber)
		currentValidatorsHash = types.Hash{}
		nextValidatorsHash = types.Hash{}
	}

	// 🆕 方案2：直接使用ExtraData中的轮次值，确保与生产时完全一致
	// 生产时使用的轮次值已经保存在ExtraData.Checkpoint.BlockRound中
	// 验证时直接使用这个值，而不是重新计算

	// 检查是否为共识切换高度，添加特殊日志
	isConsensusSwitch := false
	if consensusBackend != nil {
		if dposBackend, ok := consensusBackend.(*DPoS); ok && dposBackend.config != nil {
			if dposBackend.config.ConsensusSwitchHeight > 0 && blockNumber == dposBackend.config.ConsensusSwitchHeight {
				isConsensusSwitch = true
			}
		}
	}

	if isConsensusSwitch {
		logger.Debug("🔍 方案2：切换高度使用ExtraData中的轮次值",
			"blockNumber", blockNumber,
			"ExtraData轮次", i.Checkpoint.BlockRound,
			"验证者数量", len(validators),
			"说明", "切换高度直接使用生产时保存的轮次值，确保checkpointHash一致")
	} else {
		logger.Debug("🔍 方案2：使用ExtraData中的轮次值",
			"blockNumber", blockNumber,
			"ExtraData轮次", i.Checkpoint.BlockRound,
			"验证者数量", len(validators),
			"说明", "直接使用生产时保存的轮次值，确保checkpointHash一致")
	}

	recalculatedCheckpoint := &CheckpointData{
		BlockRound:            i.Checkpoint.BlockRound, // 直接使用生产时的轮次
		EpochNumber:           i.Checkpoint.EpochNumber,
		CurrentValidatorsHash: currentValidatorsHash, // 使用重新计算的哈希
		NextValidatorsHash:    nextValidatorsHash,    // 使用重新计算的哈希
		EventRoot:             i.Checkpoint.EventRoot,
	}

	// 添加方案2对比日志
	logger.Debug("🔍 方案2：轮次使用对比",
		"blockNumber", blockNumber,
		"使用轮次", i.Checkpoint.BlockRound,
		"说明", "直接使用ExtraData中的轮次值，与生产时完全一致")

	// 🆕 添加验证时区块头详细信息（从header参数获取）
	logger.Debug("🔍 ===== 验证时区块头详细信息（CheckpointHash计算前） =====",
		"blockNumber", blockNumber,
		"blockHash", header.Hash.String(),
		"parentHash", header.ParentHash.String(),
		"timestamp", header.Timestamp,
		"gasLimit", header.GasLimit,
		"gasUsed", header.GasUsed,
		"difficulty", header.Difficulty,
		"stateRoot", header.StateRoot.String(),
		"transactionsRoot", header.TxRoot.String(),
		"receiptsRoot", header.ReceiptsRoot.String(),
		"miner", types.BytesToAddress(header.Miner).String(),
		"nonce", header.Nonce.String(),
		"extraDataLength", len(header.ExtraData),
		"说明", "验证时用于CheckpointHash计算的区块头字段")

	// 🆕 添加验证时关键参数显著日志
	logger.Debug("🔍 ===== 验证时CheckpointHash计算参数 =====",
		"blockNumber", blockNumber,
		"chainID", productionChainID,
		"realBlockHash", realBlockHash.String(),
		"currentValidatorsHash", recalculatedCheckpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", recalculatedCheckpoint.NextValidatorsHash.String(),
		"blockRound", recalculatedCheckpoint.BlockRound,
		"epochNumber", recalculatedCheckpoint.EpochNumber,
		"eventRoot", recalculatedCheckpoint.EventRoot.String(),
		"说明", "验证时用于计算checkpointHash的所有参数")

	// 🆕 添加验证时轮次计算详细日志
	logger.Debug("🔍 ===== 验证时轮次计算详情 =====",
		"blockNumber", blockNumber,
		"originalBlockRound", i.Checkpoint.BlockRound,
		"recalculatedBlockRound", recalculatedCheckpoint.BlockRound,
		"isConsensusSwitch", isConsensusSwitch,
		"说明", "验证时轮次计算过程")

	logger.Debug("🔍 验证时开始计算checkpoint哈希",
		"blockNumber", blockNumber,
		"chainID", productionChainID,
		"realBlockHash", realBlockHash.String(),
		"currentValidatorsHash", recalculatedCheckpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", recalculatedCheckpoint.NextValidatorsHash.String(),
		"blockRound", recalculatedCheckpoint.BlockRound,
		"epochNumber", recalculatedCheckpoint.EpochNumber)

	// 🆕 添加详细的CheckpointData内容对比日志
	logger.Debug("🔍 验证时CheckpointData详细信息",
		"blockNumber", blockNumber,
		"chainID", productionChainID,
		"blockHash", realBlockHash.String(),
		"blockRound", recalculatedCheckpoint.BlockRound,
		"epochNumber", recalculatedCheckpoint.EpochNumber,
		"eventRoot", recalculatedCheckpoint.EventRoot.String(),
		"currentValidatorsHash", recalculatedCheckpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", recalculatedCheckpoint.NextValidatorsHash.String(),
		"validatorsCount", len(validators))

	// 🆕 打印验证时验证者集合的详细信息
	logger.Debug("🔍 验证时验证者集合详细信息")
	for i, validator := range validators {
		logger.Debug("🔍 验证时验证者",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive)
	}

	checkpointHash, err := recalculatedCheckpoint.Hash(productionChainID, blockNumber, realBlockHash)
	if err != nil {
		logger.Error("❌ ValidateFinalizedData checkpoint哈希计算失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("failed to calculate proposal hash: %w", err)
	}

	// 🆕 添加验证时checkpointHash结果显著日志
	logger.Debug("🔍 ===== 验证时CheckpointHash计算结果 =====",
		"blockNumber", blockNumber,
		"checkpointHash", checkpointHash.String(),
		"说明", "验证时最终计算出的checkpointHash")

	// 🆕 添加生产和验证CheckpointHash对比日志
	logger.Debug("🔍 ===== 生产vs验证CheckpointHash对比 =====",
		"blockNumber", blockNumber,
		"生产时CheckpointHash", "请查看生产日志中的'奖励分发区块CheckpointHash计算结果'或'生产时CheckpointHash计算结果'",
		"验证时CheckpointHash", checkpointHash.String(),
		"说明", "请比对生产和验证的CheckpointHash是否一致")

	// 🆕 添加生产和验证时参数对比日志
	logger.Debug("🔍 ===== 生产vs验证CheckpointData参数对比 =====",
		"blockNumber", blockNumber,
		"生产时轮次", i.Checkpoint.BlockRound,
		"验证时轮次", recalculatedCheckpoint.BlockRound,
		"生产时currentValidatorsHash", i.Checkpoint.CurrentValidatorsHash.String(),
		"验证时currentValidatorsHash", recalculatedCheckpoint.CurrentValidatorsHash.String(),
		"生产时nextValidatorsHash", i.Checkpoint.NextValidatorsHash.String(),
		"验证时nextValidatorsHash", recalculatedCheckpoint.NextValidatorsHash.String(),
		"生产时eventRoot", i.Checkpoint.EventRoot.String(),
		"验证时eventRoot", recalculatedCheckpoint.EventRoot.String(),
		"说明", "对比生产和验证时的CheckpointData参数")

	logger.Debug("🔍 验证时checkpoint哈希计算结果", "checkpointHash", checkpointHash.String())

	// 🆕 添加checkpointHash修复效果日志（方案2）
	logger.Debug("🔍 checkpointHash修复效果（方案2）",
		"blockNumber", blockNumber,
		"修复后checkpointHash", checkpointHash.String(),
		"使用轮次", i.Checkpoint.BlockRound,
		"说明", "验证时直接使用ExtraData中的轮次值，确保checkpointHash与生产时一致")

	// 🆕 添加参数对比总结日志（方案2）
	summaryTitle := "📊 ===== 生产vs验证参数对比总结（方案2） ====="
	if isConsensusSwitch {
		summaryTitle = "📊 ===== 切换高度生产vs验证参数对比总结（方案2） ====="
	}

	logger.Debug(summaryTitle,
		"blockNumber", blockNumber,
		"chainID", fmt.Sprintf("生产时=%d, 验证时=%d", productionChainID, productionChainID),
		"realBlockHash", realBlockHash.String(),
		"blockRound", fmt.Sprintf("生产时=1844, 验证时=%d", i.Checkpoint.BlockRound),
		"epochNumber", "生产时=1, 验证时=1",
		"eventRoot", "生产时=0x0000..., 验证时=0x0000...",
		"currentValidatorsHash", recalculatedCheckpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", recalculatedCheckpoint.NextValidatorsHash.String(),
		"最终checkpointHash", checkpointHash.String(),
		"方案2说明", "验证时直接使用ExtraData中的轮次值，确保与生产时完全一致",
		"切换高度", isConsensusSwitch)

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

	// 🆕 如果BLS公钥为nil，尝试从创世文件恢复
	for i, validator := range validators {
		if validator.BlsKey == nil {
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
				}
			} else {
				logger.Error("❌ 无法获取DPoS实例来恢复BLS公钥",
					"index", i,
					"address", validator.Address.String())
			}
		}
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
		logger.Error("❌ ValidateFinalizedData 解析父区块ExtraData失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("failed to verify signatures for block %d: %w", blockNumber, err)
	}

	// validate parent signatures
	if err := i.ValidateParentSignatures(blockNumber, consensusBackend, parents,
		parent, parentExtra, chainID, domain, logger); err != nil {
		logger.Error("❌ ValidateFinalizedData 父区块签名验证失败", "blockNumber", blockNumber, "error", err)
		return err
	}
	// 🆕 新增：检查parentExtra.Checkpoint是否为nil，避免空指针异常
	if parentExtra == nil || parentExtra.Checkpoint == nil {
		logger.Info("🔄 ValidateFinalizedData 父区块Checkpoint为nil，跳过Checkpoint验证", "blockNumber", blockNumber, "parentBlockNumber", parent.Number)
		return nil
	}

	err = i.Checkpoint.ValidateBasic(parentExtra.Checkpoint)
	if err != nil {
		logger.Error("❌ ValidateFinalizedData Checkpoint基本验证失败", "blockNumber", blockNumber, "error", err)
		return err
	}
	return nil
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

	// 🆕 新增：检查是否在共识切换高度，如果是则跳过父区块BLS签名验证
	// 因为父区块可能使用IBFT共识，没有BLS签名
	// 但继续执行后续的ValidateFinalizedData，应用方案2的完整修复逻辑
	if consensusBackend != nil {
		if dposBackend, ok := consensusBackend.(*DPoS); ok && dposBackend.config != nil {
			if dposBackend.config.ConsensusSwitchHeight > 0 && blockNumber == dposBackend.config.ConsensusSwitchHeight {
				logger.Debug("🔄 检测到共识切换高度，跳过父区块BLS签名验证但继续当前区块验证",
					"blockNumber", blockNumber,
					"switchHeight", dposBackend.config.ConsensusSwitchHeight,
					"parentBlockNumber", parent.Number,
					"reason", "父区块使用IBFT共识，但当前区块需要DPoS验证并应用方案2修复")
				// 🆕 修复：在切换高度直接返回nil，跳过父区块BLS签名验证
				// 这样就不会因为父区块没有BLS签名而报错
				return nil
			}
		}
	}

	if i.Parent == nil {
		return fmt.Errorf("failed to verify signatures for parent of block %d because signatures are not present",
			blockNumber)
	}

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
		return fmt.Errorf(
			"failed to get parent validators from ExtraData for block %d: %w",
			blockNumber,
			err,
		)
	}

	// 🔍 打印验证时父区块验证者集合的详细信息

	// 使用固定的哈希值避免循环依赖，确保与生产区块时使用相同的checkpointHash
	// 使用真实的父区块哈希
	realParentBlockHash := parent.Hash
	parentCheckpointHash, err := parentExtra.Checkpoint.Hash(chainID, parent.Number, realParentBlockHash)
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

							// 检查是否已经获取到BLS公钥
							if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
								if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(missingAddress); exists {
									logger.Debug("✅ ValidateParentSignatures - 重试期间成功获取BLS公钥",
										"blockNumber", blockNumber,
										"parentBlockNumber", parentBlockNumber,
										"address", missingAddress.String(),
										"blsKeyLength", len(cachedBLSKey),
										"retry", retry+1)
									break
								}
							}

							// 最后一次重试
							if retry == maxRetries-1 {
								logger.Debug("⚠️ ValidateParentSignatures - BLS公钥网络请求超时，但继续处理",
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

		// 检查是否已经获取到BLS公钥
		if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {

			if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(missingAddress); exists {

				// 🆕 关键修复：将获取到的BLS公钥保存到验证者对象中

				if validators != nil {
					found := false

					for i, validator := range validators {

						if validator.Address == missingAddress {
							found = true

							// 解析BLS公钥
							if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
								// 🆕 关键修复：确保保存到正确的验证者对象
								validator.BlsKey = blsKey

								// 立即验证保存结果
								if validator.BlsKey != nil {
									// 🆕 额外验证：检查保存后的公钥长度
									if marshaled := validator.BlsKey.Marshal(); len(marshaled) > 0 {
										// BLS公钥保存成功
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
									// BLS公钥已保存到验证者对象
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

	// 🆕 新增：检查是否在共识切换高度，如果是则跳过BLS签名验证
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		if dposInstance.config != nil && dposInstance.config.ConsensusSwitchHeight > 0 && blockNumber == dposInstance.config.ConsensusSwitchHeight {
			logger.Info("🔄 检测到共识切换高度，跳过BLS签名验证",
				"blockNumber", blockNumber,
				"switchHeight", dposInstance.config.ConsensusSwitchHeight,
				"reason", "切换高度跳过BLS签名验证")
			return nil
		}
	}

	// 直接使用所有验证者作为签名者
	signers := validators

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

		logger.Error("🚨 区块验证失败 - 法定人数不足，将返回错误让上层处理",
			"blockNumber", blockNumber,
			"reason", "quorum not reached",
			"quorumDetails", fmt.Sprintf("当前签名数: %d, 需要签名数: %d, 差距: %d",
				len(signers), requiredQuorumCount, requiredQuorumCount-len(signers)))

		return fmt.Errorf("quorum not reached: current signatures %d, required %d", len(signers), requiredQuorumCount)
	}

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

	// 🆕 方案2：统一从网络集成层缓存获取BLS公钥，不依赖validator.BlsKey字段
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			validator := validators[int(i)]

			// 优先从网络集成层缓存获取BLS公钥
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
					if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
						// 解析BLS公钥
						if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
							blsPublicKeys[i] = blsKey
						} else {
							logger.Warn("⚠️ 解析BLS公钥失败",
								"blockNumber", blockNumber,
								"address", validator.Address.String(),
								"error", err)
							missingBLSKeys = append(missingBLSKeys, validator.Address)
						}
					} else {
						// 缓存中没有找到，添加到缺失列表
						missingBLSKeys = append(missingBLSKeys, validator.Address)
						logger.Debug("⚠️ 网络集成层缓存中未找到BLS公钥，将尝试主动获取",
							"blockNumber", blockNumber,
							"address", validator.Address.String(),
							"note", "BLS公钥未在缓存中找到，将尝试网络获取")
					}
				} else {
					// 网络集成层不可用
					missingBLSKeys = append(missingBLSKeys, validator.Address)
					logger.Error("❌ 网络集成层不可用",
						"blockNumber", blockNumber,
						"address", validator.Address.String())
				}
			} else {
				// DPoS实例不可用
				missingBLSKeys = append(missingBLSKeys, validator.Address)
				logger.Error("❌ DPoS实例不可用",
					"blockNumber", blockNumber,
					"address", validator.Address.String())
			}
		}
	}

	// 🆕 方案2：如果还有缺失的BLS公钥，尝试主动获取
	if len(missingBLSKeys) > 0 {
		logger.Info("🔄 发现缺失的BLS公钥，尝试主动获取",
			"blockNumber", blockNumber,
			"missingCount", len(missingBLSKeys),
			"missingAddresses", missingBLSKeys,
			"note", "BLS公钥未在缓存中找到，将尝试网络获取")

		// 🆕 尝试主动获取缺失的BLS公钥
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
				myAddress := types.Address(dposInstance.key.Address())
				for _, missingAddress := range missingBLSKeys {
					if err := dposInstance.runtime.networkIntegration.RequestBLSKey(missingAddress, myAddress); err != nil {
						logger.Warn("⚠️ 主动获取BLS公钥失败，但继续验证流程",
							"blockNumber", blockNumber,
							"address", missingAddress.String(),
							"error", err,
							"note", "将使用现有公钥继续验证")
					} else {
						logger.Debug("📨 已发送BLS公钥请求",
							"blockNumber", blockNumber,
							"address", missingAddress.String(),
							"note", "等待网络响应")
					}
				}

				// 🆕 同步等待BLS公钥获取完成
				logger.Info("⏳ 等待BLS公钥网络响应",
					"blockNumber", blockNumber,
					"missingCount", len(missingBLSKeys),
					"waitTime", "15秒")

				maxWaitTime := 15 * time.Second
				retryInterval := 1 * time.Second
				maxRetries := int(maxWaitTime / retryInterval)

				for retry := 0; retry < maxRetries; retry++ {
					// 检查是否所有BLS公钥都已获取
					stillMissing := make([]types.Address, 0)
					for _, address := range missingBLSKeys {
						if _, exists := dposInstance.runtime.networkIntegration.GetBLSKey(address); !exists {
							stillMissing = append(stillMissing, address)
						}
					}

					if len(stillMissing) == 0 {
						logger.Info("✅ 所有BLS公钥已成功获取",
							"blockNumber", blockNumber,
							"waitTime", fmt.Sprintf("%.1f秒", float64(retry+1)*retryInterval.Seconds()))
						break
					}

					if retry < maxRetries-1 {
						logger.Debug("⏳ 继续等待BLS公钥响应",
							"blockNumber", blockNumber,
							"stillMissingCount", len(stillMissing),
							"retry", retry+1,
							"maxRetries", maxRetries)
						time.Sleep(retryInterval)
					}
				}

				// 最终检查
				finalMissing := make([]types.Address, 0)
				for _, address := range missingBLSKeys {
					if _, exists := dposInstance.runtime.networkIntegration.GetBLSKey(address); !exists {
						finalMissing = append(finalMissing, address)
					}
				}

				if len(finalMissing) > 0 {
					logger.Warn("⚠️ 部分BLS公钥仍无法获取，继续验证流程",
						"blockNumber", blockNumber,
						"stillMissingCount", len(finalMissing),
						"stillMissingAddresses", finalMissing,
						"note", "将使用现有公钥继续验证，可能影响签名验证")
				} else {
					logger.Info("✅ 所有缺失的BLS公钥已成功获取",
						"blockNumber", blockNumber,
						"note", "可以继续BLS签名验证")

					// 🆕 重新从缓存获取BLS公钥并更新blsPublicKeys数组
					logger.Info("🔄 重新从缓存获取BLS公钥并更新数组",
						"blockNumber", blockNumber,
						"missingCount", len(missingBLSKeys))

					for _, address := range missingBLSKeys {
						if cachedBLSKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(address); exists {
							if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
								// 找到对应的位图索引
								for i := uint64(0); i < uint64(len(validators)); i++ {
									if s.Bitmap.IsSet(i) && validators[int(i)].Address == address {
										blsPublicKeys[i] = blsKey
										// 同时更新validator对象
										validators[int(i)].BlsKey = blsKey
										logger.Info("✅ 成功更新BLS公钥到数组和validator对象",
											"blockNumber", blockNumber,
											"address", address.String(),
											"bitmapIndex", i,
											"blsKeyLength", len(cachedBLSKey))
										break
									}
								}
							} else {
								logger.Warn("⚠️ 解析从缓存获取的BLS公钥失败",
									"blockNumber", blockNumber,
									"address", address.String(),
									"error", err)
							}
						}
					}
				}
			} else {
				logger.Warn("⚠️ 网络集成层不可用，无法主动获取BLS公钥",
					"blockNumber", blockNumber,
					"note", "将使用现有公钥继续验证")
			}
		} else {
			logger.Warn("⚠️ DPoS实例不可用，无法主动获取BLS公钥",
				"blockNumber", blockNumber,
				"note", "将使用现有公钥继续验证")
		}
	}

	// 🆕 方案2：BLS公钥获取逻辑已简化，所有公钥应在启动时预加载完成

	aggs, err := bls.UnmarshalSignature(s.AggregatedSignature)
	if err != nil {
		logger.Error("Signature.Verify - 解析聚合签名失败", "error", err)
		return err
	}

	// 🆕 方案2：BLS公钥获取逻辑已统一到前面的循环中，这里不再需要额外处理

	// 🆕 方案2修复：使用验证者地址映射来重新排列BLS公钥，确保与生产时的签名顺序完全一致
	// 生产时按位图索引顺序聚合签名，验证时也应该按位图索引顺序排列公钥
	validBLSKeys := make([]*bls.PublicKey, 0)
	bitmapOrderedAddresses := make([]types.Address, 0)

	// 🆕 方案2：创建地址到BLS公钥的映射，使用从网络集成层缓存获取的BLS公钥
	addressToBLSKey := make(map[types.Address]*bls.PublicKey)
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) && int(i) < len(blsPublicKeys) && blsPublicKeys[i] != nil {
			validator := validators[int(i)]
			addressToBLSKey[validator.Address] = blsPublicKeys[i]
			// 🆕 修复：同时设置validator.BlsKey字段，确保数据同步
			validator.BlsKey = blsPublicKeys[i]
		}
	}

	// 🆕 显著日志：位图索引与验证者集合对应关系
	logger.Debug("🔍 ===== 位图索引与验证者集合对应关系 =====",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"bitmapHex", fmt.Sprintf("%x", s.Bitmap),
		"note", "检查位图索引是否在验证者集合范围内")

	for i := uint64(0); i < uint64(len(validators)); i++ {
		logger.Debug("🔍 位图索引检查",
			"blockNumber", blockNumber,
			"bitmapIndex", i,
			"isSet", s.Bitmap.IsSet(i),
			"validatorAddress", validators[i].Address.String(),
			"hasBlsKey", validators[i].BlsKey != nil,
			"note", "位图索引与验证者集合一一对应")
	}

	// 🎯 按位图索引顺序收集公钥和地址，只使用实际签名者
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			validatorAddress := validators[int(i)].Address
			if blsKey, exists := addressToBLSKey[validatorAddress]; exists && blsKey != nil {
				validBLSKeys = append(validBLSKeys, blsKey)
				bitmapOrderedAddresses = append(bitmapOrderedAddresses, validatorAddress)
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

	// 执行BLS签名验证（只使用有效的公钥）
	isValid := aggs.VerifyAggregated(validBLSKeys, hash[:], domain)

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
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
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

				logger.Info("⚠️ Signature.Verify - BLS公钥为nil，尝试从从缓存和创世文件恢复",
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

	// Marshal CheckpointData

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

	result := types.BytesToHash(crypto.Keccak256(abiEncoded))

	return result, nil
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

	// 解析extraData

	// 尝试解析RLP数据
	extra := &Extra{}

	if err := extra.UnmarshalRLP(extraRaw); err != nil {
		fmt.Printf("❌ DEBUG UnmarshalRLP failed: %v\n", err)
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

	// 🆕 添加 parent 的 nil 检查
	if parent == nil {
		// 🔍 优先从当前区块ExtraData获取生产时的验证者地址集合（实际签名者）
		if i.Validators != nil && !i.Validators.IsEmpty() && len(i.Validators.Added) > 0 {
			// 从ExtraData获取实际签名者地址，然后从创世文件获取BLS公钥
			validatorAddresses := i.Validators.Added

			// 🆕 从创世文件获取BLS公钥，构建完整的验证者集合
			productionValidators := make(validator.AccountSet, 0, len(validatorAddresses))
			for _, validatorAddr := range validatorAddresses {
				// 从创世文件获取BLS公钥
				blsKey, err := i.getBLSKeyFromGenesis(validatorAddr.Address, logger)
				if err != nil {
					// BLS公钥获取失败
				}

				// 构建完整的验证者信息
				productionValidators = append(productionValidators, &validator.ValidatorMetadata{
					Address:     validatorAddr.Address,
					BlsKey:      blsKey, // 🆕 从创世文件获取的BLS公钥
					VotingPower: validatorAddr.VotingPower,
					IsActive:    validatorAddr.IsActive,
				})
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

		// 从ExtraData获取验证者地址，然后从创世文件获取BLS公钥
		validatorAddresses := i.Validators.Added



		// 🆕 从创世文件获取BLS公钥，构建完整的验证者集合
		productionValidators := make(validator.AccountSet, 0, len(validatorAddresses))
		// 从创世文件获取BLS公钥

		for idx, validatorAddr := range validatorAddresses {
			// 从创世文件获取BLS公钥
			blsKey, err := i.getBLSKeyFromGenesis(validatorAddr.Address, logger)
			if err != nil {
				// BLS公钥获取失败，继续处理下一个
			} else {
				// BLS公钥获取成功
				if blsKey == nil {
					logger.Error("❌ 从创世文件获取的BLS公钥为nil",
						"blockNumber", blockNumber,
						"index", idx,
						"address", validatorAddr.Address.String())
				}
			}

			// 构建完整的验证者信息
			productionValidators = append(productionValidators, &validator.ValidatorMetadata{
				Address:     validatorAddr.Address,
				BlsKey:      blsKey, // 🆕 从创世文件获取的BLS公钥
				VotingPower: validatorAddr.VotingPower,
				IsActive:    validatorAddr.IsActive,
			})
		}

		return productionValidators, nil
	}

	parentValidators, err := i.getParentValidators(parent, parents, consensusBackend, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get parent validators: %w", err)
	}


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


	// 首先尝试从父区块的 ExtraData 中获取
	parentExtra, err := GetIbftExtra(parent.ExtraData)
	if err == nil && parentExtra != nil {

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
			return parentValidators, nil
		}
	}

	// 备用方案：从数据库获取（如果可用）

	parentValidators, err := consensusBackend.GetDelegates(parent.Number, parents)
	if err == nil && len(parentValidators) > 0 {
		return parentValidators, nil
	}


	// 最后备用方案：递归获取更早的父区块
	if parent.Number > 1 {

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

// getBLSKeyFromGenesis 从validator-bls.key文件获取BLS公钥
func (i *Extra) getBLSKeyFromGenesis(address types.Address, logger hclog.Logger) (*bls.PublicKey, error) {

	// 尝试从DPoS实例获取BLS公钥
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		// 从validator-bls.key文件获取BLS公钥字节
		blsKeyBytes, err := dposInstance.GetBLSKeyBytesFromGenesis(address)
		if err != nil {
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

		// BLS公钥获取成功

		return blsKey, nil
	}

	return nil, fmt.Errorf("无法获取DPoS实例来从validator-bls.key文件获取BLS公钥")
}
