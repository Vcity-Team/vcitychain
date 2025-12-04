package dpos

import (
	"github.com/Vcity-Team/vcitychain/types"
)

// updateRoundState 更新轮次状态
func (d *DPoS) updateRoundState(header *types.Header) {
	// 检查这个区块是否是我们自己生产的
	blockMiner := types.BytesToAddress(header.Miner)
	keyAddr := types.Address(d.key.Address())

	// 更新轮次状态 - 只有接收其他节点的区块时才更新轮次
	// 如果是自己生产的区块，轮次已经在produceBlock中更新过了
	d.logger.Debug("🔍 检查区块生产者",
		"blockNumber", header.Number,
		"blockMiner", blockMiner.String(),
		"keyAddr", keyAddr.String(),
		"isOurBlock", blockMiner == keyAddr)

	if blockMiner != keyAddr {
		// 接收其他节点的区块，需要更新轮次
		d.logger.Debug("🔄 接收其他节点区块，准备更新轮次",
			"blockNumber", header.Number,
			"blockMiner", blockMiner.String(),
			"keyAddr", keyAddr.String())

		if d.runtime != nil {
			oldRound := d.runtime.currentRound
			d.runtime.updateRound(header.Number)

			d.logger.Debug("✅ 轮次状态更新完成",
				"blockNumber", header.Number,
				"blockMiner", blockMiner.String(),
				"keyAddr", keyAddr.String(),
				"oldRound", oldRound,
				"newRound", d.runtime.currentRound)
		} else {
			d.logger.Error("❌ runtime为nil，无法更新轮次状态",
				"blockNumber", header.Number)
		}
	} else {
		// 自己生产的区块，轮次已经在produceBlock中更新过了
		d.logger.Debug("ℹ️ 处理自己生产的区块，轮次已在produceBlock中更新",
			"blockNumber", header.Number,
			"currentRound", func() uint64 {
				if d.runtime != nil {
					return d.runtime.currentRound
				}
				return 0
			}())
	}
}
