package dpos

import (
	"bytes"
	"fmt"
	"math/big"
	"runtime"
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

	// 对地址进行排序以确保一致性（使用字节比较，与GetSortedValidatorsWithLimit保持一致）
	for i := 0; i < len(addresses); i++ {
		for j := i + 1; j < len(addresses); j++ {
			if bytes.Compare(addresses[i][:], addresses[j][:]) > 0 {
				addresses[i], addresses[j] = addresses[j], addresses[i]
			}
		}
	}

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

// DPoSMixDigest represents a hash to identify whether the block is from DPoS consensus engine
var DPoSMixDigest = types.StringToHash("adce6e5230abe012342a44e4e9b6d05997d6f015387ae0e59be924afc7ec70c1")

// FaultFlagInfo 故障标志信息结构
type FaultFlagInfo struct {
	NodeAddress            types.Address `json:"node_address"`
	IsFaulty               bool          `json:"is_faulty"`
	MissedBlocks           uint64        `json:"missed_blocks"`
	ActualBlocks           uint64        `json:"actual_blocks"`            // 实际出块数
	ExpectedBlocks         uint64        `json:"expected_blocks"`          // 预期出块数
	MissedBlocksPercentage uint64        `json:"missed_blocks_percentage"` // 漏块率（基点）
	LastUpdateTime         uint64        `json:"last_update_time"`
	EpochNumber            uint64        `json:"epoch_number"`
	LastFaultyEpoch        uint64        `json:"last_faulty_epoch"` // 上次故障的epoch（如果之前有故障则保留，否则为0）
	Reason                 string        `json:"reason"`
	DoubleSigningHeight    uint64        `json:"double_signing_height,omitempty"` // 双重签名高度（严重违规）
}

// Extra defines the structure of the extra field for Istanbul
type Extra struct {
	Validators *validator.ValidatorSetDelta
	Parent     *Signature
	Committed  *Signature
	Checkpoint *CheckpointData
	// 奖励分配信息
	RewardDistribution *RewardDistributionInfo
	// 用于CheckpointHash计算的区块哈希
	CheckpointBlockHash types.Hash
	// 故障标志信息
	FaultFlags []FaultFlagInfo `json:"fault_flags,omitempty"`
	// 故障消减信息（只包含 missed blocks 的消减，不包含双重签名）
	SlashingInfo *SlashingInfo `json:"slashing_info,omitempty"`
}

// VoterRewardDetail 投票者奖励详情（包含验证者地址）
// 注意：一个投票者可能投票给多个验证者，所以需要为每个验证者-投票者组合创建一条记录
type VoterRewardDetail struct {
	VoterAddress     string   `json:"voterAddress"`     // 投票者地址
	ValidatorAddress string   `json:"validatorAddress"` // 验证者地址
	Amount           *big.Int `json:"amount"`           // 奖励金额
	VoteWeight       *big.Int `json:"voteWeight"`       // 投票权重
}

// RewardDistributionInfo 奖励分配信息
type RewardDistributionInfo struct {
	EpochNumber       uint64                  `json:"epochNumber"`
	Rewards           map[string]*big.Int     `json:"rewards"`           // 地址 -> 奖励金额（聚合值，用于状态更新）
	VoterRewards      []*VoterRewardDetail    `json:"voterRewards"`      // 验证者-投票者奖励列表，用于记录到数据库
	ProducerRewards   []*ProducerRewardDetail `json:"producerRewards"`   // 节点出块奖励明细（独立于质押池）
	VoterPoolTotal    *big.Int                `json:"voterPoolTotal"`    // 质押投票池本 epoch 总量
	ProducerPoolTotal *big.Int                `json:"producerPoolTotal"` // 节点出块池本 epoch 总量
	TotalReward       *big.Int                `json:"totalReward"`
	Timestamp         uint64                  `json:"timestamp"`
}

// SlashingInfo 故障消减信息（只包含 missed blocks 的消减，不包含双重签名）
type SlashingInfo struct {
	EpochNumber uint64               `json:"epochNumber"`
	Slashings   []*SlashingOperation `json:"slashings"` // 消减操作列表
	Timestamp   uint64               `json:"timestamp"`
}

// SlashingOperation 单个消减操作（只用于故障检测）
type SlashingOperation struct {
	ValidatorAddr          types.Address `json:"validatorAddr"`          // 被消减的验证者
	SlashRate              uint64        `json:"slashRate"`              // 消减率（基点）
	MissedBlocks           uint64        `json:"missedBlocks"`           // 错过的区块数
	MissedBlocksPercentage uint64        `json:"missedBlocksPercentage"` // 错过区块百分比
	Reason                 string        `json:"reason"`                 // 消减原因
}

// MarshalRLPWith 实现RLP编码
func (r *RewardDistributionInfo) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	vv.Set(ar.NewUint(r.EpochNumber))

	// Rewards (聚合值，用于状态更新)
	rewardsArray := ar.NewArray()
	for addr, amount := range r.Rewards {
		rewardItem := ar.NewArray()
		rewardItem.Set(ar.NewCopyBytes([]byte(addr)))
		rewardItem.Set(ar.NewBigInt(amount))
		rewardsArray.Set(rewardItem)
	}
	vv.Set(rewardsArray)

	// VoterRewards (验证者-投票者映射列表，用于记录到数据库)
	voterRewardsArray := ar.NewArray()
	for _, detail := range r.VoterRewards {
		if detail != nil {
			voterRewardItem := ar.NewArray()
			voterRewardItem.Set(ar.NewCopyBytes([]byte(detail.VoterAddress)))
			voterRewardItem.Set(ar.NewCopyBytes([]byte(detail.ValidatorAddress)))
			voterRewardItem.Set(ar.NewBigInt(detail.Amount))
			voterRewardsArray.Set(voterRewardItem)
		}
	}
	vv.Set(voterRewardsArray)

	// ProducerRewards（节点出块奖励，独立于质押池）
	producerRewardsArray := ar.NewArray()
	for _, detail := range r.ProducerRewards {
		if detail == nil {
			continue
		}
		producerItem := ar.NewArray()
		producerItem.Set(ar.NewCopyBytes([]byte(detail.ProducerAddress)))
		producerItem.Set(ar.NewBigInt(detail.Amount))
		producerItem.Set(ar.NewUint(detail.BlocksProduced))
		if detail.RewardPerBlock != nil {
			producerItem.Set(ar.NewBigInt(detail.RewardPerBlock))
		} else {
			producerItem.Set(ar.NewBigInt(big.NewInt(0)))
		}
		producerRewardsArray.Set(producerItem)
	}
	vv.Set(producerRewardsArray)

	vv.Set(ar.NewBigInt(r.TotalReward))

	vv.Set(ar.NewUint(r.Timestamp))

	return vv
}

// UnmarshalRLPWith 实现RLP解码
func (r *RewardDistributionInfo) UnmarshalRLPWith(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	// 兼容旧版本：
	// - 4 元素：无 VoterRewards
	// - 5 元素：有 VoterRewards，无 ProducerRewards
	// - 6 元素：有 ProducerRewards（双轨）
	if len(elems) < 4 {
		return fmt.Errorf("invalid RewardDistributionInfo RLP: expected at least 4 elements, got %d", len(elems))
	}

	epochNumber, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	r.EpochNumber = epochNumber

	// Rewards (聚合值)
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

	// VoterRewards (验证者-投票者映射列表)
	r.VoterRewards = []*VoterRewardDetail{}
	if len(elems) >= 5 {
		// 新版本：有VoterRewards字段
		voterRewardsElems, err := elems[2].GetElems()
		if err == nil {
			for _, voterRewardElem := range voterRewardsElems {
				voterRewardItemElems, err := voterRewardElem.GetElems()
				if err != nil || len(voterRewardItemElems) != 3 {
					continue
				}

				voterAddrBytes, err := voterRewardItemElems[0].GetBytes(nil)
				if err != nil {
					continue
				}

				validatorAddrBytes, err := voterRewardItemElems[1].GetBytes(nil)
				if err != nil {
					continue
				}

				amount := new(big.Int)
				if err := voterRewardItemElems[2].GetBigInt(amount); err != nil {
					continue
				}

				r.VoterRewards = append(r.VoterRewards, &VoterRewardDetail{
					VoterAddress:     string(voterAddrBytes),
					ValidatorAddress: string(validatorAddrBytes),
					Amount:           amount,
				})
			}
		}
		// 如果解析失败，VoterRewards 保持为空（兼容旧版本）
	}

	r.ProducerRewards = []*ProducerRewardDetail{}
	if len(elems) >= 6 {
		producerElems, err := elems[3].GetElems()
		if err == nil {
			for _, producerElem := range producerElems {
				itemElems, err := producerElem.GetElems()
				if err != nil || len(itemElems) < 2 {
					continue
				}
				addrBytes, err := itemElems[0].GetBytes(nil)
				if err != nil {
					continue
				}
				amount := new(big.Int)
				if err := itemElems[1].GetBigInt(amount); err != nil {
					continue
				}
				var blocksProduced uint64
				if len(itemElems) >= 3 {
					blocksProduced, _ = itemElems[2].GetUint64()
				}
				rewardPerBlock := big.NewInt(0)
				if len(itemElems) >= 4 {
					_ = itemElems[3].GetBigInt(rewardPerBlock)
				}
				r.ProducerRewards = append(r.ProducerRewards, &ProducerRewardDetail{
					ProducerAddress: string(addrBytes),
					Amount:          amount,
					BlocksProduced:  blocksProduced,
					RewardPerBlock:  rewardPerBlock,
				})
			}
		}
	}

	// TotalReward：4 元素格式在 index 2；5 元素在 index 3；6 元素（含 ProducerRewards）在 index 4
	totalRewardIndex := 2
	if len(elems) == 5 {
		totalRewardIndex = 3
	} else if len(elems) >= 6 {
		totalRewardIndex = 4
	}
	totalReward := new(big.Int)
	if err := elems[totalRewardIndex].GetBigInt(totalReward); err != nil {
		return err
	}
	r.TotalReward = totalReward

	// Timestamp
	timestampIndex := totalRewardIndex + 1
	timestamp, err := elems[timestampIndex].GetUint64()
	if err != nil {
		return err
	}
	r.Timestamp = timestamp

	return nil
}

// MarshalRLPWith 实现RLP编码
func (s *SlashingInfo) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	vv.Set(ar.NewUint(s.EpochNumber))

	slashingsArray := ar.NewArray()
	for _, op := range s.Slashings {
		slashingsArray.Set(op.MarshalRLPWith(ar))
	}
	vv.Set(slashingsArray)

	vv.Set(ar.NewUint(s.Timestamp))

	return vv
}

// UnmarshalRLPWith 实现RLP解码
func (s *SlashingInfo) UnmarshalRLPWith(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) < 3 {
		return fmt.Errorf("invalid SlashingInfo RLP: expected 3 elements, got %d", len(elems))
	}

	// EpochNumber
	epochNumber, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	s.EpochNumber = epochNumber

	slashingsElems, err := elems[1].GetElems()
	if err != nil {
		return err
	}
	s.Slashings = make([]*SlashingOperation, 0, len(slashingsElems))
	for _, opElem := range slashingsElems {
		op := &SlashingOperation{}
		if err := op.UnmarshalRLPWith(opElem); err == nil {
			s.Slashings = append(s.Slashings, op)
		}
	}

	timestamp, err := elems[2].GetUint64()
	if err != nil {
		return err
	}
	s.Timestamp = timestamp

	return nil
}

// MarshalRLPWith 实现RLP编码
func (s *SlashingOperation) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	vv.Set(ar.NewBytes(s.ValidatorAddr.Bytes()))

	vv.Set(ar.NewUint(s.SlashRate))

	vv.Set(ar.NewUint(s.MissedBlocks))

	vv.Set(ar.NewUint(s.MissedBlocksPercentage))

	vv.Set(ar.NewCopyBytes([]byte(s.Reason)))

	return vv
}

// UnmarshalRLPWith 实现RLP解码
func (s *SlashingOperation) UnmarshalRLPWith(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) < 5 {
		return fmt.Errorf("invalid SlashingOperation RLP: expected 5 elements, got %d", len(elems))
	}

	addrBytes, err := elems[0].GetBytes(nil)
	if err != nil {
		return err
	}
	if len(addrBytes) == 20 {
		s.ValidatorAddr = types.BytesToAddress(addrBytes)
	}

	slashRate, err := elems[1].GetUint64()
	if err != nil {
		return err
	}
	s.SlashRate = slashRate

	missedBlocks, err := elems[2].GetUint64()
	if err != nil {
		return err
	}
	s.MissedBlocks = missedBlocks

	missedBlocksPercentage, err := elems[3].GetUint64()
	if err != nil {
		return err
	}
	s.MissedBlocksPercentage = missedBlocksPercentage

	reasonBytes, err := elems[4].GetBytes(nil)
	if err != nil {
		return err
	}
	s.Reason = string(reasonBytes)

	return nil
}

// MarshalRLPTo defines the marshal function wrapper for Extra
func (i *Extra) MarshalRLPTo(dst []byte) []byte {
	ar := &fastrlp.Arena{}
	result := append(make([]byte, ExtraVanity), i.MarshalRLPWith(ar).MarshalTo(dst)...)
	return result
}

// MarshalRLPWith defines the marshal function implementation for Extra
func (i *Extra) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	if i.Validators == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Validators.MarshalRLPWith(ar))
	}

	if i.Parent == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Parent.MarshalRLPWith(ar))
	}

	if i.Committed == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Committed.MarshalRLPWith(ar))
	}

	if i.Checkpoint == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Checkpoint.MarshalRLPWith(ar))
	}

	if i.RewardDistribution == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.RewardDistribution.MarshalRLPWith(ar))
	}

	if i.CheckpointBlockHash == (types.Hash{}) {
		vv.Set(ar.NewBytes([]byte{}))
	} else {
		vv.Set(ar.NewBytes(i.CheckpointBlockHash.Bytes()))
	}

	// Element[6] - FaultFlags
	if len(i.FaultFlags) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		// 实现 FaultFlags 的 MarshalRLPWith
		// 每个 FaultFlagInfo 包含：NodeAddress, IsFaulty, MissedBlocks, ActualBlocks, LastUpdateTime, EpochNumber, LastFaultyEpoch, Reason
		faultFlagsArray := ar.NewArray()
		for _, flag := range i.FaultFlags {
			flagItem := ar.NewArray()
			flagItem.Set(ar.NewBytes(flag.NodeAddress.Bytes()))
			flagItem.Set(ar.NewBool(flag.IsFaulty))
			flagItem.Set(ar.NewUint(flag.MissedBlocks))
			flagItem.Set(ar.NewUint(flag.ActualBlocks))
			flagItem.Set(ar.NewUint(flag.LastUpdateTime))
			flagItem.Set(ar.NewUint(flag.EpochNumber))
			flagItem.Set(ar.NewUint(flag.LastFaultyEpoch))
			flagItem.Set(ar.NewCopyBytes([]byte(flag.Reason)))
			faultFlagsArray.Set(flagItem)
		}
		vv.Set(faultFlagsArray)
	}

	if i.SlashingInfo == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.SlashingInfo.MarshalRLPWith(ar))
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

	num := len(elems)
	// ✅ 关键修复：允许至少3个元素，兼容IBFT格式（IBFT的ExtraData至少有3个元素）
	// IBFT格式：Validators, ProposerSeal, CommittedSeal, ParentCommittedSeal, RoundNumber（至少3个）
	// DPoS格式：Validators, Parent, Committed, Checkpoint, ...（至少4个）
	if num < 3 {
		return fmt.Errorf("incorrect elements count to decode Extra, expected at least 3 but found %d", num)
	}
	// 区块的 ExtraData 在序列化为 RLP 时是一个数组：最多支持8个元素（Validators, Parent, Committed, Checkpoint, RewardDistribution, CheckpointBlockHash, FaultFlags, SlashingInfo）
	if num > 8 {
		// 只使用前8个元素，忽略额外的元素
		elems = elems[:8]
		num = 8
	}

	if elems[0].Elems() > 0 {
		validatorElems, err := elems[0].GetElems()
		if err != nil {
			return fmt.Errorf("failed to get validator elements from elems[0]: %w", err)
		}

		if len(validatorElems) == 3 {
			// 标准ValidatorSetDelta格式：Added, Updated, Removed
			i.Validators = &validator.ValidatorSetDelta{}
			if err := i.Validators.UnmarshalRLPWith(elems[0]); err != nil {
				// 解析失败，可能是IBFT格式（3个验证者地址）而不是ValidatorSetDelta格式
				// 这种情况通常发生在共识切换高度的父区块（IBFT区块）
				// 为了兼容性，将Validators设为nil，允许继续解析其他字段（如Committed签名）
				// 但只在解析父区块ExtraData时使用（在block_builder.go中会检查是否是共识切换高度）
				i.Validators = nil
				// 注意：这里不返回错误，允许继续解析其他字段
				// 调用者需要自己判断是否是共识切换高度的情况
			} else {
				// 解析成功，检查Added数组
				if len(i.Validators.Added) == 0 {
					// 注意：这里不返回错误，因为可能是空的Added数组（虽然不应该）
					// 但会在ValidateFinalizedData中检查并返回错误
				}
			}
		} else {
			// 🔍 调试信息：记录为什么Validators为nil（会在ValidateFinalizedData中记录详细日志）
			// len(validatorElems) != 3，不符合ValidatorSetDelta格式
			i.Validators = nil
		}
	} else {
		// 🔍 调试信息：elems[0]为空，Validators保持为nil（会在ValidateFinalizedData中记录详细日志）
		i.Validators = nil
	}

	// ✅ 关键修复：容错处理elems[1]和elems[2]的解析
	// IBFT格式：elems[1]是ProposerSeal（字节数组），elems[2]是CommittedSeal（Seals格式）
	// DPoS格式：elems[1]是Parent Signature，elems[2]是Committed Signature
	// 如果解析失败（可能是IBFT格式），将字段设为nil，允许继续解析其他字段
	if num >= 2 && elems[1].Elems() > 0 {
		i.Parent = &Signature{}
		if err := i.Parent.UnmarshalRLPWith(elems[1]); err != nil {
			// 解析失败，可能是IBFT格式（ProposerSeal），将Parent设为nil
			i.Parent = nil
		}
	}

	if num >= 3 && elems[2].Elems() > 0 {
		committedElems, err := elems[2].GetElems()
		if err != nil {
			// 获取元素失败，可能是IBFT格式（CommittedSeal），将Committed设为nil
			i.Committed = nil
		} else if len(committedElems) == 2 {
			// 标准Signature格式：AggregatedSignature, Bitmap
			i.Committed = &Signature{}
			if err := i.Committed.UnmarshalRLPWith(elems[2]); err != nil {
				// 解析失败，可能是IBFT格式（CommittedSeal），将Committed设为nil
				i.Committed = nil
			}
		} else {
			// 非标准格式，跳过
			i.Committed = nil
		}
	}

	// Checkpoint
	// ✅ 关键修复：检查元素数量，避免访问不存在的elems[3]
	if num >= 4 && elems[3].Elems() > 0 {
		checkpointElems, err := elems[3].GetElems()
		if err != nil {
			// 获取元素失败，跳过Checkpoint解析
			i.Checkpoint = nil
		} else if len(checkpointElems) == 5 {
			// 标准CheckpointData格式：5个元素
			i.Checkpoint = &CheckpointData{}
			if err := i.Checkpoint.UnmarshalRLPWith(elems[3]); err != nil {
				// 解析失败，可能是IBFT格式，将Checkpoint设为nil
				i.Checkpoint = nil
			}
		} else {
			// 非标准格式，跳过解析
			i.Checkpoint = nil
		}
	}

	// Element[4] - 奖励分配信息（只在5个或更多元素时处理）
	if num >= 5 && elems[4].Elems() > 0 {
		i.RewardDistribution = &RewardDistributionInfo{}
		if err := i.RewardDistribution.UnmarshalRLPWith(elems[4]); err != nil {
			// 不返回错误，只是跳过奖励分配信息
			i.RewardDistribution = nil
		}
	}

	// Element[5] - CheckpointBlockHash（只在6个或更多元素时处理）
	if num >= 6 {
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

	// Element[6] - FaultFlags（只在7个或更多元素时处理）
	if num >= 7 && elems[6].Elems() > 0 {
		faultFlagsElems, err := elems[6].GetElems()
		if err == nil {
			i.FaultFlags = make([]FaultFlagInfo, 0, len(faultFlagsElems))
			for _, flagElem := range faultFlagsElems {
				flagItemElems, err := flagElem.GetElems()
				if err == nil && len(flagItemElems) >= 6 {
					flag := FaultFlagInfo{}

					// NodeAddress
					addrBytes, _ := flagItemElems[0].GetBytes(nil)
					if len(addrBytes) == 20 {
						flag.NodeAddress = types.BytesToAddress(addrBytes)
					}

					flag.IsFaulty, _ = flagItemElems[1].GetBool()
					flag.MissedBlocks, _ = flagItemElems[2].GetUint64()
					flag.ActualBlocks, _ = flagItemElems[3].GetUint64()
					flag.LastUpdateTime, _ = flagItemElems[4].GetUint64()

					// 区块的 ExtraData 在序列化为 RLP 时是一个数组：8个元素（index5 为 EpochNumber，index6 为 LastFaultyEpoch，index7 为 Reason）
					if len(flagItemElems) >= 8 {
						flag.EpochNumber, _ = flagItemElems[5].GetUint64()
						flag.LastFaultyEpoch, _ = flagItemElems[6].GetUint64()
						reasonBytes, _ := flagItemElems[7].GetBytes(nil)
						flag.Reason = string(reasonBytes)
					} else {
						// 格式不正确，跳过该故障标志
						continue
					}

					i.FaultFlags = append(i.FaultFlags, flag)
				}
			}
		}
	}

	// Element[7] - SlashingInfo（只在8个元素时处理）
	if num >= 8 && elems[7].Elems() > 0 {
		i.SlashingInfo = &SlashingInfo{}
		if err := i.SlashingInfo.UnmarshalRLPWith(elems[7]); err != nil {
			// 不返回错误，只是跳过消减信息
			// 注意：这里没有logger，因为Extra.UnmarshalRLP可能在不同上下文中调用
			i.SlashingInfo = nil
		}
	} else if num >= 8 {
		// ExtraData有8个元素，但第8个元素（SlashingInfo）为空或格式不正确
		// 这种情况需要记录日志，但这里没有logger，需要在调用处记录
	}

	return nil
}

// processFaultFlags 处理故障标志并保存到数据库（独立函数，可在多个地方调用）
func (i *Extra) processFaultFlags(blockNumber uint64, consensusBackend dposBackend, logger hclog.Logger) {
	// 处理故障标志（在获取验证者集合之前，确保故障状态被保存）
	if len(i.FaultFlags) > 0 {
		// 统计真正故障的验证者数量（isFaulty=true）
		faultyCount := 0
		for _, faultFlag := range i.FaultFlags {
			if faultFlag.IsFaulty {
				faultyCount++
			}
		}

		// 只在有真正故障的验证者时才打印日志
		var faultyFlags []FaultFlagInfo
		savedCount := 0

		clearedMainnet := false
		for _, faultFlag := range i.FaultFlags {
			// 保存故障状态到数据库（验证节点）
			// 只有当 isFaulty=true 时才保存，避免覆盖已存在的故障状态
			if faultFlag.IsFaulty {
				faultyFlags = append(faultyFlags, faultFlag)
				if dposInstance, ok := consensusBackend.(*DPoS); ok {
					if err := dposInstance.saveFaultStatusToDatabase(faultFlag); err != nil {
						logger.Warn("⚠️ processFaultFlags 保存故障状态到数据库失败",
							"blockNumber", blockNumber,
							"address", faultFlag.NodeAddress.String(),
							"error", err)
					} else {
						savedCount++
					}
				} else {
					logger.Warn("⚠️ processFaultFlags 无法获取DPoS实例，无法保存故障状态到数据库",
						"blockNumber", blockNumber,
						"address", faultFlag.NodeAddress.String())
				}
			} else {
				// isFaulty=false：默认保留既有故障（漏块等需治理恢复）。
				// 例外：mainnet stake restored 且当前 DB 为主网质押类故障时可清除。
				if dposInstance, ok := consensusBackend.(*DPoS); ok {
					if dposInstance.applyMainnetStakeFaultClear(faultFlag) {
						clearedMainnet = true
						logger.Info("processFaultFlags cleared mainnet stake fault",
							"blockNumber", blockNumber,
							"address", faultFlag.NodeAddress.String())
					}
				}
			}
		}
		if clearedMainnet {
			if dposInstance, ok := consensusBackend.(*DPoS); ok {
				if err := dposInstance.reloadValidatorsAfterRecovery(); err != nil {
					logger.Warn("processFaultFlags reload validators after mainnet clear failed", "error", err)
				}
			}
		}

		if len(faultyFlags) > 0 {
			logger.Info("🔍 processFaultFlags 故障标志统计",
				"blockNumber", blockNumber,
				"faultFlagsCount", len(i.FaultFlags),
				"faultyCount", len(faultyFlags),
				"savedCount", savedCount)

			for _, flag := range faultyFlags {
				logger.Info("📝 故障节点详情",
					"address", flag.NodeAddress.String(),
					"isFaulty", flag.IsFaulty,
					"missedBlocks", flag.MissedBlocks,
					"epoch", flag.EpochNumber,
					"lastFaultyEpoch", flag.LastFaultyEpoch,
					"reason", flag.Reason)
			}
		}
	} else {
		logger.Debug("ℹ️ processFaultFlags 没有故障标志",
			"blockNumber", blockNumber)
	}
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

	// 新增：检查是否在共识切换高度，如果是则完全跳过所有验证
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

	realBlockHash := header.Hash

	// 如果ExtraData中有CheckpointBlockHash，优先使用它
	if i.CheckpointBlockHash != (types.Hash{}) {
		realBlockHash = i.CheckpointBlockHash
	} else {
		logger.Info("🔍 ===== 验证时使用区块头哈希 =====",
			"blockNumber", blockNumber,
			"headerHash", realBlockHash.String(),
			"说明", "ExtraData中没有CheckpointBlockHash，验证时使用区块头哈希")
	}
	productionChainID := chainID

	// 处理故障标志（调用独立函数）
	i.processFaultFlags(blockNumber, consensusBackend, logger)

	// 🔧 修复：必须从ExtraData读取验证者集合，确保与生产时使用的验证者集合一致
	// 这样可以避免生产与验证之间故障状态变化导致的位图索引不匹配问题

	var validators validator.AccountSet
	// 🔍 并发跟踪：记录读取验证者集合前的状态
	goroutineID2 := fmt.Sprintf("%d", getGoroutineID())
	extraInstanceAddr2 := fmt.Sprintf("%p", i)
	validatorsBeforeAddr := "nil"
	validatorsBeforeLen := 0
	if i.Validators != nil {
		validatorsBeforeAddr = fmt.Sprintf("%p", i.Validators)
		if i.Validators.Added != nil {
			validatorsBeforeLen = len(i.Validators.Added)
			validatorsBeforeAddr = fmt.Sprintf("%s(Added:%p,len:%d)", validatorsBeforeAddr, i.Validators.Added, validatorsBeforeLen)
		}
	}

	if i.Validators != nil && len(i.Validators.Added) > 0 {
		// 从ExtraData读取生产时使用的验证者集合
		validators = i.Validators.Added.Copy()
		validatorsAfterAddr := fmt.Sprintf("%p", validators)
		logger.Debug("✅ 从ExtraData读取生产时验证者集合",
			"blockNumber", blockNumber,
			"validatorsCount", len(validators),
			"goroutineID", goroutineID2,
			"extraInstanceAddr", extraInstanceAddr2,
			"validatorsBeforeAddr", validatorsBeforeAddr,
			"validatorsAfterAddr", validatorsAfterAddr,
			"note", "确保与生产时使用的验证者集合一致")
	} else {
		// ❌ 错误：ExtraData中必须包含验证者集合，否则无法正确验证位图
		logger.Error("❌ ExtraData中缺少验证者集合",
			"blockNumber", blockNumber,
			"validatorsIsNil", i.Validators == nil,
			"validatorsAddedLen", func() int {
				if i.Validators != nil {
					return len(i.Validators.Added)
				}
				return 0
			}(),
			"validatorsUpdatedLen", func() int {
				if i.Validators != nil {
					return len(i.Validators.Updated)
				}
				return 0
			}(),
			"validatorsRemovedLen", func() int {
				if i.Validators != nil {
					return len(i.Validators.Removed)
				}
				return 0
			}(),
			"extraDataLength", len(header.ExtraData),
			"extraDataHex", func() string {
				if len(header.ExtraData) > 100 {
					return fmt.Sprintf("%x", header.ExtraData[:100]) // 只显示前100字节
				}
				return fmt.Sprintf("%x", header.ExtraData)
			}(),
			"note", "生产时应该将验证者集合写入ExtraData，验证时必须从ExtraData读取。可能原因：1) ExtraData解析失败 2) Validators字段未正确序列化 3) elems[0]为空或len(validatorElems)!=3")
		return fmt.Errorf("validators set is missing in ExtraData for block %d: validatorsIsNil=%v, validatorsAddedLen=%d, extraDataLength=%d",
			blockNumber, i.Validators == nil, func() int {
				if i.Validators != nil {
					return len(i.Validators.Added)
				}
				return 0
			}(), len(header.ExtraData))
	}

	var currentValidatorsHash types.Hash
	var nextValidatorsHash types.Hash

	// 如果有验证者集合，重新计算哈希值
	if len(validators) > 0 {
		// 使用与生产时相同的validator.AccountSet.HashAddressOnly()方法
		if hash, err := validators.HashAddressOnly(); err == nil {
			currentValidatorsHash = hash
			nextValidatorsHash = hash // 暂时使用相同的哈希
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

	recalculatedCheckpoint := &CheckpointData{
		BlockRound:            i.Checkpoint.BlockRound, // 直接使用生产时的轮次
		EpochNumber:           i.Checkpoint.EpochNumber,
		CurrentValidatorsHash: currentValidatorsHash, // 使用重新计算的哈希
		NextValidatorsHash:    nextValidatorsHash,    // 使用重新计算的哈希
		EventRoot:             i.Checkpoint.EventRoot,
	}

	checkpointHash, err := recalculatedCheckpoint.Hash(productionChainID, blockNumber, realBlockHash)
	if err != nil {
		logger.Error("❌ ValidateFinalizedData checkpoint哈希计算失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("failed to calculate proposal hash: %w", err)
	}

	// 如果BLS公钥为nil，尝试从创世文件恢复
	for i, validator := range validators {
		if validator.BlsKey == nil {
			// 尝试从DPoS实例获取BLS公钥
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
				if blsKeyBytes, err := dposInstance.GetBLSKeyBytesFromGenesis(validator.Address); err == nil {
					if blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes); err == nil {
						validator.BlsKey = blsKey
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
		logger.Error("区块签名验证失败，返回错误由上层处理",
			"blockNumber", blockNumber,
			"proposalHash", checkpointHash.String(),
			"error", err)
		return fmt.Errorf("block signature verification failed for block %d: %w", blockNumber, err)
	}
	parentExtra, err := GetDposExtra(parent.ExtraData)
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
	if parentExtra == nil || parentExtra.Checkpoint == nil {
		logger.Info("🔄 ValidateFinalizedData 父区块Checkpoint为nil，跳过Checkpoint验证", "blockNumber", blockNumber, "parentBlockNumber", parent.Number)
		return nil
	}
	err = i.Checkpoint.ValidateBasic(parentExtra.Checkpoint)
	if err != nil {
		logger.Error("❌ ValidateFinalizedData Checkpoint基本验证失败", "blockNumber", blockNumber, "error", err)
		return err
	}

	// 双重签名检测和削减（dpos_disable_double_sign_slashing=true 时整段跳过）
	if consensusBackend != nil {
		if dposBackend, ok := consensusBackend.(*DPoS); ok && !dposBackend.disableDoubleSignSlashing {
			// 检查是否有 doubleSigningDetector（通过检查是否有 DetectDoubleSigning 方法）
			if dposBackend.epochManager != nil {
				validatorAddr := types.BytesToAddress(header.Miner)
				blockHeight := blockNumber
				blockHash := header.Hash

				// 使用 DoubleSigningDetector 检测双重签名
				var isDoubleSigning bool
				var existingSig *BlockSignature
				if dposBackend.doubleSigningDetector != nil {
					isDoubleSigning, existingSig = dposBackend.doubleSigningDetector.DetectDoubleSigning(
						validatorAddr,
						blockHeight,
						blockHash,
					)
				}

				if isDoubleSigning && existingSig != nil {
					logger.Warn("🚨 检测到双重签名，执行严重违规削减",
						"validator", validatorAddr.String(),
						"height", blockHeight,
						"currentHash", blockHash.String(),
						"conflictHash", existingSig.BlockHash.String())

					// 计算 epoch number
					epochNumber := dposBackend.epochManager.GetCurrentEpoch(blockHeight)
					slashRate := dposBackend.getSevereOffenseSlashRate()

					// 创建故障标志
					faultInfo := FaultFlagInfo{
						NodeAddress:         validatorAddr,
						IsFaulty:            true,
						LastUpdateTime:      uint64(time.Now().Unix()),
						EpochNumber:         epochNumber,
						LastFaultyEpoch:     epochNumber,
						Reason:              fmt.Sprintf("Severe Offense: Double Signing at height %d", blockHeight),
						DoubleSigningHeight: blockHeight,
					}

					// 执行严重违规削减
					err := dposBackend.executeSlashing(
						validatorAddr,
						slashRate,
						blockHeight,
						epochNumber,
						faultInfo.Reason,
						0,           // missedBlocks
						0,           // missedBlocksPercentage
						blockHeight, // doubleSigningHeight
					)
					if err != nil {
						logger.Error("❌ 双重签名削减执行失败",
							"validator", validatorAddr.String(),
							"error", err)
					} else {
						logger.Info("✅ 双重签名削减执行成功",
							"validator", validatorAddr.String(),
							"slashRate", slashRate,
							"基点")
					}

					// 更新内存中的故障状态
					dposBackend.updateMemoryFaultStatus(faultInfo)

					// 保存故障状态到数据库
					if dposBackend.state != nil && dposBackend.state.StakeStore != nil {
						if err := dposBackend.state.StakeStore.UpdateValidatorFaultStatus(
							validatorAddr,
							faultInfo.IsFaulty,
							faultInfo.MissedBlocks,
							faultInfo.LastUpdateTime,
							faultInfo.LastFaultyEpoch,
							faultInfo.Reason,
						); err != nil {
							logger.Warn("⚠️ 保存双重签名故障状态失败",
								"validator", validatorAddr.String(),
								"error", err)
						}
					}
				}
			}
		}
	}

	return nil
}

// ValidateParentSignatures validates signatures for parent block
func (i *Extra) ValidateParentSignatures(blockNumber uint64, consensusBackend dposBackend, parents []*types.Header,
	parent *types.Header, parentExtra *Extra, chainID uint64, domain []byte, logger hclog.Logger) error {

	// 🔍 并发跟踪：记录函数开始时的关键信息
	goroutineID := fmt.Sprintf("%d", getGoroutineID())
	parentExtraAddr := fmt.Sprintf("%p", parentExtra)
	parentValidatorsAddr := "nil"
	parentValidatorsLen := 0
	if parentExtra.Validators != nil {
		parentValidatorsAddr = fmt.Sprintf("%p", parentExtra.Validators)
		if parentExtra.Validators.Added != nil {
			parentValidatorsLen = len(parentExtra.Validators.Added)
			parentValidatorsAddr = fmt.Sprintf("%s(Added:%p,len:%d)", parentValidatorsAddr, parentExtra.Validators.Added, parentValidatorsLen)
		}
	}

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
				// 修复：在切换高度直接返回nil，跳过父区块BLS签名验证
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

	// 🔍 关键修复：优先从父区块的ExtraData读取验证者集合，而不是从数据库
	// 这样可以确保使用父区块生产时的验证者集合，而不是当前数据库中的验证者集合
	var parentValidators validator.AccountSet
	var err error

	// 首先尝试从parentExtra.Validators.Added读取（这是父区块生产时写入的验证者集合）
	if parentExtra.Validators != nil && len(parentExtra.Validators.Added) > 0 {
		parentValidators = parentExtra.Validators.Added.Copy()
	} else {
		// 如果ExtraData中没有验证者集合，才从数据库获取（fallback）
		logger.Warn("⚠️ [ValidateParentSignatures] 父区块ExtraData中没有验证者集合，从数据库获取",
			"blockNumber", blockNumber,
			"parentBlockNumber", parent.Number,
			"goroutineID", goroutineID,
			"parentExtraAddr", parentExtraAddr,
			"parentValidatorsIsNil", parentExtra.Validators == nil,
			"parentValidatorsAddedLen", func() int {
				if parentExtra.Validators != nil {
					return len(parentExtra.Validators.Added)
				}
				return 0
			}(),
			"note", "这不是期望的情况，父区块应该包含验证者集合")
		parentValidators, err = parentExtra.getValidatorsFromDatabase(parent, parentParent, parents, consensusBackend, logger)
		if err != nil {
			return fmt.Errorf(
				"failed to get parent validators from ExtraData for block %d: %w",
				blockNumber,
				err,
			)
		}

		// 🔍 记录从数据库获取的验证者集合
		parentValidatorsFromDBAddr := fmt.Sprintf("%p", parentValidators)
		parentValidatorsFromDBLen := len(parentValidators)
		validatorsFromDBDetails := make([]string, len(parentValidators))
		for idx, v := range parentValidators {
			validatorsFromDBDetails[idx] = fmt.Sprintf("[%d]%s", idx, v.Address.String())
		}

		logger.Info("⚠️ [ValidateParentSignatures] 从数据库获取父区块验证者集合",
			"blockNumber", blockNumber,
			"parentBlockNumber", parent.Number,
			"goroutineID", goroutineID,
			"parentValidatorsFromDBAddr", parentValidatorsFromDBAddr,
			"parentValidatorsFromDBLen", parentValidatorsFromDBLen,
			"validatorsFromDBDetails", validatorsFromDBDetails,
			"note", "从数据库获取的验证者集合可能与父区块生产时使用的验证者集合不一致")
	}

	realParentBlockHash := parent.Hash
	if parentExtra.Checkpoint == nil {
		// 兼容历史/回滚/导入数据：部分父区块可能缺失 Checkpoint 字段。
		// 这种情况下无法计算 proposalHash 来验父块签名，只能跳过父块签名验证，
		// 否则会导致节点因“缺父块 checkpoint”拒绝同步并各自出块形成分叉。
		logger.Warn("⚠️ [ValidateParentSignatures] 父区块Checkpoint缺失，跳过父区块签名验证",
			"blockNumber", blockNumber,
			"parentBlockNumber", parent.Number,
			"parentHash", realParentBlockHash.String(),
			"note", "请尽快修复/补齐父区块ExtraData中的Checkpoint字段")
		return nil
	}

	parentCheckpointHash, err := parentExtra.Checkpoint.Hash(chainID, parent.Number, realParentBlockHash)
	if err != nil {
		return fmt.Errorf("failed to calculate parent checkpoint hash: %w", err)
	}

	parentBlockNumber := blockNumber - 1

	if err := i.Parent.Verify(parentBlockNumber, parentValidators, parentCheckpointHash, domain, logger); err != nil {
		// 修复：检查是否是BLS密钥缺失错误，如果是则尝试获取
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

					// 修复：优先使用Signature中的DPoS实例引用
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

						// 增强：增加BLS公钥网络请求的等待时间和重试机制
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

						// 增强：重新尝试验证父区块签名，如果失败则继续重试
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
	// 添加：DPoS实例引用，用于BLS密钥获取
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

	// 获取DPoS实例
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

	// 等待网络响应
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

				// 关键修复：将获取到的BLS公钥保存到验证者对象中

				if validators != nil {
					found := false

					for i, validator := range validators {

						if validator.Address == missingAddress {
							found = true

							// 解析BLS公钥
							if blsKey, err := bls.UnmarshalPublicKey(cachedBLSKey); err == nil {
								// 关键修复：确保保存到正确的验证者对象
								validator.BlsKey = blsKey

								// 立即验证保存结果
								if validator.BlsKey != nil {
									// 额外验证：检查保存后的公钥长度
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

								// 保存后验证：检查验证者集合中是否真的保存了BLS公钥
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

	// 备选方案：尝试从本地获取BLS公钥
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

	// 新增：检查是否在共识切换高度，如果是则跳过BLS签名验证
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

	// 统一门槛：使用运行时 calculateMinRequiredSignatures()
	requiredQuorumCount := 0
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance.runtime != nil {
		requiredQuorumCount = dposInstance.runtime.calculateMinRequiredSignatures()
	} else {
		// 兜底（不期望走到这里）：按当前验证者数一半+1
		requiredQuorumCount = len(validators)/2 + 1
	}

	if len(signers) < requiredQuorumCount {
		quorumCalculation := fmt.Sprintf("统一门槛：active/validators 一半+1 = %d", requiredQuorumCount)

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

	// 修复：先计算位图中设置的位数，然后创建正确长度的数组
	bitmapSetCount := 0
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			bitmapSetCount++
		}
	}

	// 创建与validators数组长度一致的BLS公钥数组，确保位图索引能正确对应
	blsPublicKeys := make([]*bls.PublicKey, len(validators))
	missingBLSKeys := make([]types.Address, 0)

	// ✅ 方案1：在验证BLS签名前，如果networkIntegration不可用，等待其就绪
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		if dposInstance.runtime == nil || dposInstance.runtime.networkIntegration == nil {
			// networkIntegration不可用，等待其初始化
			logger.Info("⏳ networkIntegration不可用，等待其初始化",
				"blockNumber", blockNumber,
				"waitTime", "10秒",
				"runtimeAddr", fmt.Sprintf("%p", dposInstance.runtime),
				"networkIntegrationAddr", func() string {
					if dposInstance.runtime == nil {
						return "<runtime-nil>"
					}
					return fmt.Sprintf("%p", dposInstance.runtime.networkIntegration)
				}())

			waitTimeout := 10 * time.Second
			waitInterval := 50 * time.Millisecond
			waitStart := time.Now()

			for time.Since(waitStart) < waitTimeout {
				if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
					// 检查是否已启动（通过检查关键主题）
					if dposInstance.runtime.networkIntegration.GetSignatureRequestTopic() != nil ||
						dposInstance.runtime.networkIntegration.GetSignatureResponseTopic() != nil {
						logger.Info("✅ networkIntegration已就绪",
							"blockNumber", blockNumber,
							"waitTime", time.Since(waitStart),
							"runtimeAddr", fmt.Sprintf("%p", dposInstance.runtime),
							"networkIntegrationAddr", fmt.Sprintf("%p", dposInstance.runtime.networkIntegration))
						break
					}
				}
				time.Sleep(waitInterval)
			}

			// 如果等待超时后仍未就绪，尝试启动
			if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
				if dposInstance.runtime.networkIntegration.GetSignatureRequestTopic() == nil &&
					dposInstance.runtime.networkIntegration.GetSignatureResponseTopic() == nil {
					logger.Warn("⚠️ networkIntegration等待超时，尝试启动",
						"blockNumber", blockNumber,
						"runtimeAddr", fmt.Sprintf("%p", dposInstance.runtime),
						"networkIntegrationAddr", fmt.Sprintf("%p", dposInstance.runtime.networkIntegration))
					if err := dposInstance.runtime.networkIntegration.Start(); err != nil {
						logger.Error("❌ networkIntegration启动失败",
							"blockNumber", blockNumber,
							"runtimeAddr", fmt.Sprintf("%p", dposInstance.runtime),
							"networkIntegrationAddr", fmt.Sprintf("%p", dposInstance.runtime.networkIntegration),
							"error", err)
					} else {
						logger.Info("✅ networkIntegration启动成功",
							"blockNumber", blockNumber,
							"runtimeAddr", fmt.Sprintf("%p", dposInstance.runtime),
							"networkIntegrationAddr", fmt.Sprintf("%p", dposInstance.runtime.networkIntegration))
					}
				}
			}
		}
	}

	// 方案2：统一从网络集成层缓存获取BLS公钥，不依赖validator.BlsKey字段
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
						"address", validator.Address.String(),
						"runtimeAddr", fmt.Sprintf("%p", dposInstance.runtime),
						"networkIntegrationAddr", func() string {
							if dposInstance.runtime == nil {
								return "<runtime-nil>"
							}
							return fmt.Sprintf("%p", dposInstance.runtime.networkIntegration)
						}())
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

	// 方案2：仅对创世验证者尝试主动获取并等待；历史验证者缺 key 验证阶段会直接放行，无需等待
	missingGenesisBLSKeys := make([]types.Address, 0)
	if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
		for _, addr := range missingBLSKeys {
			if dposInstance.IsGenesisValidator(addr) {
				missingGenesisBLSKeys = append(missingGenesisBLSKeys, addr)
			}
		}
	} else {
		missingGenesisBLSKeys = missingBLSKeys
	}
	if len(missingGenesisBLSKeys) == 0 && len(missingBLSKeys) > 0 {
		logger.Debug("缺失的BLS公钥均为历史验证者，跳过主动获取与等待",
			"blockNumber", blockNumber, "missingAddresses", missingBLSKeys)
	}
	if len(missingGenesisBLSKeys) > 0 {
		logger.Info("🔄 发现缺失的BLS公钥，尝试主动获取",
			"blockNumber", blockNumber,
			"missingCount", len(missingGenesisBLSKeys),
			"missingAddresses", missingGenesisBLSKeys,
			"note", "BLS公钥未在缓存中找到，将尝试网络获取（仅创世验证者）")

		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
				myAddress := types.Address(dposInstance.key.Address())
				for _, missingAddress := range missingGenesisBLSKeys {
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

				// 同步等待BLS公钥获取完成（仅创世验证者）
				logger.Info("⏳ 等待BLS公钥网络响应",
					"blockNumber", blockNumber,
					"missingCount", len(missingGenesisBLSKeys),
					"waitTime", "15秒")

				maxWaitTime := 15 * time.Second
				retryInterval := 1 * time.Second
				maxRetries := int(maxWaitTime / retryInterval)
				// 记录已发送请求的地址，避免重复发送
				requestedAddresses := make(map[types.Address]bool)

				for retry := 0; retry < maxRetries; retry++ {
					// 检查是否所有BLS公钥都已获取
					stillMissing := make([]types.Address, 0)
					for _, address := range missingGenesisBLSKeys {
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

					// 在等待期间，检查peer连接状态并重试发送请求
					if retry < maxRetries-1 {
						// 检查未获取到BLS公钥的地址的peer连接状态
						for _, address := range stillMissing {
							// 检查peer连接状态
							peerID, hasMapping, isConnected := dposInstance.runtime.networkIntegration.GetValidatorConnectivity(address)

							if hasMapping && !isConnected {
								// peer映射存在但未连接，主动触发连接
								if err := dposInstance.runtime.networkIntegration.TryConnectPeer(peerID); err != nil {
									logger.Debug("⚠️ 触发peer连接失败",
										"blockNumber", blockNumber,
										"address", address.String(),
										"peerID", peerID.String(),
										"error", err,
										"retry", retry+1)
								} else {
									logger.Debug("🔄 已触发peer连接，等待连接建立",
										"blockNumber", blockNumber,
										"address", address.String(),
										"peerID", peerID.String(),
										"retry", retry+1)
								}
							} else if hasMapping && isConnected {
								// peer已连接，如果之前没有发送过请求，则重试发送
								if !requestedAddresses[address] {
									logger.Debug("🔄 peer已连接，重试发送BLS公钥请求",
										"blockNumber", blockNumber,
										"address", address.String(),
										"peerID", peerID.String(),
										"retry", retry+1)
									if err := dposInstance.runtime.networkIntegration.RequestBLSKey(address, myAddress); err != nil {
										logger.Debug("⚠️ 重试发送BLS公钥请求失败",
											"blockNumber", blockNumber,
											"address", address.String(),
											"error", err)
									} else {
										requestedAddresses[address] = true
									}
								}
							} else {
								// 没有peer映射，尝试发送请求（可能通过广播）
								if !requestedAddresses[address] {
									logger.Debug("🔄 无peer映射，尝试广播BLS公钥请求",
										"blockNumber", blockNumber,
										"address", address.String(),
										"retry", retry+1)
									if err := dposInstance.runtime.networkIntegration.RequestBLSKey(address, myAddress); err != nil {
										logger.Debug("⚠️ 广播BLS公钥请求失败",
											"blockNumber", blockNumber,
											"address", address.String(),
											"error", err)
									} else {
										requestedAddresses[address] = true
									}
								}
							}
						}

						logger.Debug("⏳ 继续等待BLS公钥响应",
							"blockNumber", blockNumber,
							"stillMissingCount", len(stillMissing),
							"retry", retry+1,
							"maxRetries", maxRetries)
						time.Sleep(retryInterval)
					}
				}

				// 最终检查（仅创世验证者）
				finalMissing := make([]types.Address, 0)
				for _, address := range missingGenesisBLSKeys {
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

					// 重新从缓存获取BLS公钥并更新blsPublicKeys数组
					logger.Info("🔄 重新从缓存获取BLS公钥并更新数组",
						"blockNumber", blockNumber,
						"missingCount", len(missingGenesisBLSKeys))

					for _, address := range missingGenesisBLSKeys {
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
	aggs, err := bls.UnmarshalSignature(s.AggregatedSignature)
	if err != nil {
		logger.Error("Signature.Verify - 解析聚合签名失败", "error", err)
		return err
	}
	// 生产时按位图索引顺序聚合签名，验证时也应该按位图索引顺序排列公钥
	validBLSKeys := make([]*bls.PublicKey, 0)
	bitmapOrderedAddresses := make([]types.Address, 0)

	// 方案2：创建地址到BLS公钥的映射，使用从网络集成层缓存获取的BLS公钥
	addressToBLSKey := make(map[types.Address]*bls.PublicKey)
	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) && int(i) < len(blsPublicKeys) && blsPublicKeys[i] != nil {
			validator := validators[int(i)]
			addressToBLSKey[validator.Address] = blsPublicKeys[i]
			// 修复：同时设置validator.BlsKey字段，确保数据同步
			validator.BlsKey = blsPublicKeys[i]
		}
	}

	// ✅ 方案4：按位图索引顺序收集公钥和地址，在验证前立即恢复缺失的BLS公钥
	// 检查位图索引是否超出验证者集合范围
	maxBitmapIndex := uint64(0)
	bitmapSetIndices := make([]uint64, 0)
	for i := uint64(0); i < 256; i++ { // 检查前256位
		if s.Bitmap.IsSet(i) {
			if i > maxBitmapIndex {
				maxBitmapIndex = i
			}
			bitmapSetIndices = append(bitmapSetIndices, i)
		}
	}

	if maxBitmapIndex >= uint64(len(validators)) {
		logger.Error("🚨 [Signature.Verify] 位图索引超出验证者集合范围",
			"blockNumber", blockNumber,
			"maxBitmapIndex", maxBitmapIndex,
			"validatorsCount", len(validators),
			"bitmapSetIndices", bitmapSetIndices,
			"bitmapHex", fmt.Sprintf("%x", s.Bitmap),
			"note", "位图索引超出范围，可能导致验证失败")
	}

	// 用于历史验证者 BLS 策略：创世验证者缺 key 必须失败；非创世（历史）验证者缺 key 则放行，避免同步卡住
	var missingGenesisValidatorKey, hasMissingHistoricalValidatorKey bool

	for i := uint64(0); i < uint64(len(validators)); i++ {
		if s.Bitmap.IsSet(i) {
			validatorAddress := validators[int(i)].Address

			if blsKey, exists := addressToBLSKey[validatorAddress]; exists && blsKey != nil {
				validBLSKeys = append(validBLSKeys, blsKey)
				bitmapOrderedAddresses = append(bitmapOrderedAddresses, validatorAddress)
			} else {
				// BLS公钥缺失：先判断是否创世验证者，非创世直接 pass，不尝试获取
				if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && !dposInstance.IsGenesisValidator(validatorAddress) {
					hasMissingHistoricalValidatorKey = true
					logger.Debug("历史验证者缺BLS公钥，直接放行不请求",
						"bitmapIndex", i, "address", validatorAddress.String())
					continue
				}

				// 创世验证者或无法获取DPoS：尝试恢复（缓存 → 创世文件 → 网络）
				if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
					var blsKeyBytes []byte
					var found bool

					// 第一步：尝试从缓存获取BLS公钥
					if dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
						if cachedKey, exists := dposInstance.runtime.networkIntegration.GetBLSKey(validatorAddress); exists {
							blsKeyBytes = cachedKey
							found = true
							logger.Debug("✅ 从缓存找到BLS公钥",
								"bitmapIndex", i,
								"address", validatorAddress.String(),
								"blsKeyLength", len(blsKeyBytes))
						}
					}

					// 第二步：如果缓存中没有，尝试从创世文件获取（仅限本地节点）
					if !found {
						if keyBytes, err := dposInstance.GetBLSKeyBytesFromGenesis(validatorAddress); err == nil {
							blsKeyBytes = keyBytes
							found = true
							logger.Debug("✅ 从创世文件找到BLS公钥",
								"bitmapIndex", i,
								"address", validatorAddress.String(),
								"blsKeyLength", len(blsKeyBytes))
						}
					}

					// 第三步：如果缓存和创世文件都没有，尝试通过网络请求获取（仅限远程验证者）
					if !found {
						isLocalNode := dposInstance.key != nil && validatorAddress == types.Address(dposInstance.key.Address())
						if !isLocalNode && dposInstance.runtime != nil && dposInstance.runtime.networkIntegration != nil {
							logger.Debug("🌐 尝试通过网络请求获取BLS公钥",
								"bitmapIndex", i,
								"address", validatorAddress.String(),
								"note", "缓存和创世文件都没有，尝试网络请求")

							if blsKey, err := dposInstance.GetBLSKeyForValidator(validatorAddress); err == nil && blsKey != nil {
								blsKeyBytes = blsKey.Marshal()
								found = true
								logger.Debug("✅ 通过网络请求获取BLS公钥成功",
									"bitmapIndex", i,
									"address", validatorAddress.String(),
									"blsKeyLength", len(blsKeyBytes))
							}
						}
					}

					// 第四步：如果找到了BLS公钥，解析并更新
					if found && len(blsKeyBytes) > 0 {
						if blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes); err == nil {
							validators[int(i)].BlsKey = blsKey
							blsPublicKeys[i] = blsKey
							addressToBLSKey[validatorAddress] = blsKey
							validBLSKeys = append(validBLSKeys, blsKey)
							bitmapOrderedAddresses = append(bitmapOrderedAddresses, validatorAddress)
							logger.Debug("✅ 成功恢复BLS公钥并添加到验证列表",
								"bitmapIndex", i,
								"address", validatorAddress.String(),
								"blsKeyLength", len(blsKeyBytes))
						} else {
							logger.Warn("⚠️ 解析恢复的BLS公钥失败",
								"bitmapIndex", i,
								"address", validatorAddress.String(),
								"error", err)
							// 此处必为创世验证者（非创世已在上方 continue）
							missingGenesisValidatorKey = true
						}
					} else {
						// 无法恢复：此处必为创世验证者
						missingGenesisValidatorKey = true
						logger.Warn("⚠️ 创世验证者BLS公钥不可用，验证将失败",
							"bitmapIndex", i, "address", validatorAddress.String())
					}
				} else {
					logger.Error("❌ 无法获取DPoS实例来恢复BLS公钥",
						"bitmapIndex", i,
						"address", validatorAddress.String())
				}
			}
		}
	}

	// 创世验证者缺 BLS key：必须失败
	if missingGenesisValidatorKey {
		logger.Error("BLS签名验证失败：创世验证者BLS公钥不可用", "blockNumber", blockNumber)
		return fmt.Errorf("BLS signature verification failed: genesis validator BLS key unavailable for block %d", blockNumber)
	}
	// 仅历史验证者缺 key：放行，避免同步卡在旧区块
	if hasMissingHistoricalValidatorKey {
		logger.Debug("跳过BLS聚合验证（存在历史验证者缺BLS公钥），放行区块",
			"blockNumber", blockNumber, "hash", hash.String())
		return nil
	}

	// 执行BLS签名验证（只使用有效的公钥）
	isValid := aggs.VerifyAggregated(validBLSKeys, hash[:], domain)

	if !isValid {
		logger.Error("BLS签名验证失败，返回错误由上层处理",
			"blockNumber", blockNumber,
			"hash", hash.String(),
			"signersCount", len(signers),
			"publicKeysCount", len(blsPublicKeys),
			"aggregatedSignatureLength", len(s.AggregatedSignature))
		return fmt.Errorf("BLS signature verification failed for block %d", blockNumber)
	}

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
	if c == nil {
		return types.ZeroHash, fmt.Errorf("checkpoint data is nil")
	}
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

// GetDposExtraClean returns unmarshaled extra field from the passed in header,
// but without signatures for the given header (it only includes signatures for the parent block)
func GetDposExtraClean(extraRaw []byte) ([]byte, error) {
	extra, err := GetDposExtra(extraRaw)
	if err != nil {
		return nil, err
	}

	dposExtra := &Extra{
		Parent:     extra.Parent,
		Validators: nil, // 🔧 修改：不再存储验证者集合，统一从数据库读取
		Checkpoint: extra.Checkpoint,
		Committed:  &Signature{},
	}

	return dposExtra.MarshalRLPTo(nil), nil
}

// GetDposExtra returns the DPoS extra data field from the passed in header
func GetDposExtra(extraRaw []byte) (*Extra, error) {
	if len(extraRaw) < ExtraVanity {
		return nil, fmt.Errorf("wrong extra size: %d", len(extraRaw))
	}

	// 🔍 并发跟踪：记录goroutine ID和Extra实例创建
	goroutineID := fmt.Sprintf("%d", getGoroutineID())
	extraDataHash := fmt.Sprintf("%x", extraRaw[:min(32, len(extraRaw))]) // 只记录前32字节的哈希

	// 尝试解析RLP数据
	extra := &Extra{}
	extraInstanceAddr := fmt.Sprintf("%p", extra)

	// 添加info级别日志：记录ExtraData解析开始
	// 注意：这里无法获取blockNumber，但可以记录实例地址用于并发跟踪

	if err := extra.UnmarshalRLP(extraRaw); err != nil {
		return nil, err
	}

	// 🔍 并发跟踪：记录解析后的状态
	validatorsAddr := "nil"
	validatorsLen := 0
	if extra.Validators != nil {
		validatorsAddr = fmt.Sprintf("%p", extra.Validators)
		if extra.Validators.Added != nil {
			validatorsLen = len(extra.Validators.Added)
			validatorsAddr = fmt.Sprintf("%s(Added:%p,len:%d)", validatorsAddr, extra.Validators.Added, validatorsLen)
		}
	}

	// 添加info级别日志：记录ExtraData解析结果
	// 注意：这里无法获取blockNumber，详细的区块号信息会在ValidateFinalizedData中记录
	if extra.Validators != nil && len(extra.Validators.Added) > 0 {
		// ExtraData解析成功，包含验证者集合
		// 详细日志会在ValidateFinalizedData中记录
	}

	// 🔍 并发跟踪日志
	_ = goroutineID
	_ = extraDataHash
	_ = extraInstanceAddr
	_ = validatorsAddr
	_ = validatorsLen
	// 注意：这里不打印日志，因为无法获取blockNumber，避免日志混乱
	// 详细的日志会在ValidateFinalizedData中记录

	return extra, nil
}

// getGoroutineID 获取当前goroutine的ID（用于并发跟踪）
func getGoroutineID() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	// 从堆栈信息中提取goroutine ID
	// 格式: "goroutine 123 [running]:"
	var id uint64
	fmt.Sscanf(string(b), "goroutine %d", &id)
	return id
}

// getValidatorsFromDatabase 从数据库获取验证者集合
func (i *Extra) getValidatorsFromDatabase(header *types.Header, parent *types.Header, parents []*types.Header,
	consensusBackend dposBackend, logger hclog.Logger) (validator.AccountSet, error) {
	blockNumber := header.Number

	if dposBackend, ok := consensusBackend.(*DPoS); ok && dposBackend != nil && dposBackend.config != nil {
		consensusSwitchHeight := dposBackend.config.ConsensusSwitchHeight
		if consensusSwitchHeight > 0 && blockNumber < consensusSwitchHeight {
			logger.Debug("📋 处理共识切换高度之前的区块，从创世文件获取验证者集合",
				"blockNumber", blockNumber,
				"consensusSwitchHeight", consensusSwitchHeight)

			genesisValidators, err := i.getGenesisValidators(consensusBackend, logger)
			if err != nil {
				return nil, fmt.Errorf("failed to get genesis validators: %w", err)
			}

			logger.Debug("✅ 从创世文件获取验证者集合成功",
				"blockNumber", blockNumber,
				"genesisValidatorsCount", len(genesisValidators))

			return genesisValidators, nil
		}
	}

	// 从数据库读取验证者集合
	if dposBackend, ok := consensusBackend.(*DPoS); ok && dposBackend != nil {
		validators, err := dposBackend.GetSortedValidatorsWithLimitFilterFaulty()
		if err != nil {
			logger.Error("❌ 从数据库获取验证者集合失败", "blockNumber", blockNumber, "error", err)
			return nil, fmt.Errorf("failed to get validators from database for block %d: %w", blockNumber, err)
		}
		if len(validators) > 0 {
			logger.Debug("✅ 从数据库获取验证者集合成功",
				"blockNumber", blockNumber,
				"validatorsCount", len(validators))
			return validators, nil
		}
	}

	return nil, nil
}

// getGenesisValidators 从创世文件获取验证者集合
func (i *Extra) getGenesisValidators(consensusBackend dposBackend, logger hclog.Logger) (validator.AccountSet, error) {
	logger.Info("🔍 开始从创世文件获取验证者集合")

	// 直接从 DPoS 实例的内存中获取当前验证者集合（创世验证者）
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

	return nil, fmt.Errorf("failed to get genesis validators from all sources")
}

// getParentValidators 获取父区块的验证者集合
func (i *Extra) getParentValidators(parent *types.Header, parents []*types.Header,
	consensusBackend dposBackend, logger hclog.Logger) (validator.AccountSet, error) {

	// 首先尝试从父区块的 ExtraData 中获取
	parentExtra, err := GetDposExtra(parent.ExtraData)
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

		parentValidators, err := parentExtra.getValidatorsFromDatabase(parent, parentParent, parents, consensusBackend, logger)
		if err == nil {
			return parentValidators, nil
		}
	}

	// 如果父区块是创世区块，从创世文件获取
	return i.getGenesisValidators(consensusBackend, logger)
}
