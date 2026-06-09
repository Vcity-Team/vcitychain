package dpos

import (
	"fmt"
	"math/big"
	"strconv"

	"github.com/Vcity-Team/vcitychain/types"
)

// ProducerRewardDetail 节点出块奖励明细（独立于投票者质押池）
type ProducerRewardDetail struct {
	ProducerAddress string   `json:"producerAddress"`
	Amount          *big.Int `json:"amount"`
	BlocksProduced  uint64   `json:"blocksProduced"`
	RewardPerBlock  *big.Int `json:"rewardPerBlock"`
}

func parseWeiParameterValue(value interface{}) (*big.Int, error) {
	switch v := value.(type) {
	case string:
		amount, ok := new(big.Int).SetString(v, 10)
		if !ok {
			return nil, fmt.Errorf("invalid wei string: %s", v)
		}
		return amount, nil
	case *big.Int:
		if v == nil {
			return big.NewInt(0), nil
		}
		return new(big.Int).Set(v), nil
	case uint64:
		return new(big.Int).SetUint64(v), nil
	case int:
		if v < 0 {
			return nil, fmt.Errorf("negative wei value: %d", v)
		}
		return big.NewInt(int64(v)), nil
	case int64:
		if v < 0 {
			return nil, fmt.Errorf("negative wei value: %d", v)
		}
		return big.NewInt(v), nil
	case float64:
		if v < 0 {
			return nil, fmt.Errorf("negative wei value: %v", v)
		}
		return big.NewInt(int64(v)), nil
	default:
		return nil, fmt.Errorf("unsupported wei value type: %T", value)
	}
}

// getEffectiveBlockProducerRewardPerBlock 返回每块节点奖励（wei）；0 表示未启用节点轨。
func (d *DPoS) getEffectiveBlockProducerRewardPerBlock() *big.Int {
	if v, err := d.getCurrentParameterValue("dpos_block_producer_reward_per_block"); err == nil {
		if amount, err := parseWeiParameterValue(v); err == nil {
			return amount
		}
	}
	if d.config != nil && d.config.BlockProducerRewardPerBlock != nil {
		return new(big.Int).Set(d.config.BlockProducerRewardPerBlock)
	}
	return big.NewInt(0)
}

// getEffectiveProducerRewardActivationEpoch 返回节点轨激活 epoch；0 表示配置 perBlock>0 后立即生效。
func (d *DPoS) getEffectiveProducerRewardActivationEpoch() uint64 {
	if v, err := d.getCurrentParameterValue("dpos_producer_reward_activation_epoch"); err == nil {
		switch t := v.(type) {
		case uint64:
			return t
		case int:
			if t >= 0 {
				return uint64(t)
			}
		case int64:
			if t >= 0 {
				return uint64(t)
			}
		case float64:
			if t >= 0 {
				return uint64(t)
			}
		case string:
			if u, err := strconv.ParseUint(t, 10, 64); err == nil {
				return u
			}
		}
	}
	if d.config != nil {
		return d.config.ProducerRewardActivationEpoch
	}
	return 0
}

// isProducerRewardActive 判断指定 epoch 是否发放节点出块奖励。
func (d *DPoS) isProducerRewardActive(epochNumber uint64) bool {
	perBlock := d.getEffectiveBlockProducerRewardPerBlock()
	if perBlock == nil || perBlock.Sign() <= 0 {
		return false
	}
	activation := d.getEffectiveProducerRewardActivationEpoch()
	if activation == 0 {
		return true
	}
	return epochNumber >= activation
}

// computeProducerRewardPool 计算本 epoch 节点轨总池（perBlock × 总出块数）。
func (d *DPoS) computeProducerRewardPool(totalBlocks uint64) *big.Int {
	if totalBlocks == 0 {
		return big.NewInt(0)
	}
	perBlock := d.getEffectiveBlockProducerRewardPerBlock()
	if perBlock == nil || perBlock.Sign() <= 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Mul(perBlock, new(big.Int).SetUint64(totalBlocks))
}

// calculateProducerRewards 按出块数分配节点轨奖励。
func (d *DPoS) calculateProducerRewards(
	blockCounts map[types.Address]uint64,
	perBlock *big.Int,
) (map[types.Address]*big.Int, []*ProducerRewardDetail) {
	rewards := make(map[types.Address]*big.Int)
	details := make([]*ProducerRewardDetail, 0)

	if perBlock == nil || perBlock.Sign() <= 0 || len(blockCounts) == 0 {
		return rewards, details
	}

	for addr, blocks := range blockCounts {
		if blocks == 0 {
			continue
		}
		amount := new(big.Int).Mul(perBlock, new(big.Int).SetUint64(blocks))
		if amount.Sign() <= 0 {
			continue
		}
		rewards[addr] = amount
		details = append(details, &ProducerRewardDetail{
			ProducerAddress: addr.String(),
			Amount:          new(big.Int).Set(amount),
			BlocksProduced:  blocks,
			RewardPerBlock:  new(big.Int).Set(perBlock),
		})
	}
	return rewards, details
}

func mergeRewardMaps(base map[types.Address]*big.Int, extra map[types.Address]*big.Int) {
	for addr, amount := range extra {
		if amount == nil || amount.Sign() == 0 {
			continue
		}
		if existing, ok := base[addr]; ok {
			existing.Add(existing, amount)
		} else {
			base[addr] = new(big.Int).Set(amount)
		}
	}
}

func cloneBigIntOrZero(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}

func producerAmountByAddress(details []*ProducerRewardDetail) map[string]*big.Int {
	out := make(map[string]*big.Int)
	for _, detail := range details {
		if detail == nil || detail.Amount == nil || detail.Amount.Sign() <= 0 {
			continue
		}
		out[detail.ProducerAddress] = new(big.Int).Set(detail.Amount)
	}
	return out
}
