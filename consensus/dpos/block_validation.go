package dpos

import (
	"fmt"
	"os"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/types"
)

// VerifyHeader 验证区块头部
func (d *DPoS) VerifyHeader(header *types.Header) error {
	blockNumber := header.Number

	if d.config.ConsensusSwitchHeight > 0 && blockNumber == d.config.ConsensusSwitchHeight {
		d.logger.Info("🔄 共识切换高度区块，跳过DPoS验证", "blockNumber", blockNumber, "consensusSwitchHeight", d.config.ConsensusSwitchHeight)
		return nil
	}

	if err := d.waitForBLSKeysLoaded(); err != nil {
		d.logger.Error("❌ 等待BLS公钥加载失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("BLS keys not loaded: %w", err)
	}

	// Short circuit if the header is known
	if _, ok := d.blockchain.GetHeaderByHash(header.Hash); ok {
		d.logger.Info("✅ DPoS VerifyHeader 区块已存在，跳过验证", "blockNumber", blockNumber)
		return nil
	}

	parent, ok := d.blockchain.GetHeaderByHash(header.ParentHash)
	if !ok {
		d.logger.Error("❌ 无法通过哈希获取父区块",
			"blockNumber", header.Number,
			"parentHash", header.ParentHash.String(),
			"parentHashHex", fmt.Sprintf("0x%x", header.ParentHash))

		// 父区块获取失败时立即退出程序
		d.logger.Error("💀 无法获取父区块，程序将立即退出")
		os.Exit(1)

		return fmt.Errorf(
			"unable to get parent header by hash for block number %d",
			header.Number,
		)
	}

	err := d.verifyHeaderImpl(parent, header, d.config.BlockTime.Duration, nil)
	if err != nil {
		d.logger.Error("❌ DPoS VerifyHeader verifyHeaderImpl失败", "blockNumber", blockNumber, "error", err)

		d.logger.Error("💀 区块头验证失败，程序将立即退出")
		os.Exit(1)

		return err
	}
	return nil
}

// verifyHeaderImpl 验证区块头部的实现
func (d *DPoS) verifyHeaderImpl(parent, header *types.Header, blockTimeDrift time.Duration, parents []*types.Header) error {
	// validate header fields
	if err := validateHeaderFields(parent, header, uint64(blockTimeDrift.Seconds())); err != nil {
		d.logger.Error("❌ 区块头部字段验证失败 - parent信息",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String(),
			"blockTimestamp", time.Unix(int64(header.Timestamp), 0).Format("15:04:05"),
			"parentNumber", parent.Number,
			"parentHash", parent.Hash.String(),
			"parentTimestamp", time.Unix(int64(parent.Timestamp), 0).Format("15:04:05"),
			"error", err)
		return fmt.Errorf("failed to validate header for block %d. error = %w", header.Number, err)
	}

	// decode the extra data
	extra, err := GetDposExtra(header.ExtraData)
	if err != nil {
		d.logger.Error("解析区块extraData失败",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String(),
			"error", err,
			"extraDataLength", len(header.ExtraData))
		return fmt.Errorf("failed to verify header for block %d. get extra error = %w", header.Number, err)
	}

	// validate extra data
	err = extra.ValidateFinalizedData(
		header, parent, parents, d.blockchain.GetChainID(), d, signer.DomainValidatorSet, d.logger)

	if err != nil {
		d.logger.Error("🚨 区块extraData验证失败，将返回错误让上层处理",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String(),
			"error", err)
		d.logger.Error("=== 验证区块头部失败 ===")
		return fmt.Errorf("block extraData validation failed: %w", err)
	}
	d.logger.Debug("区块extraData验证成功")
	return nil
}

// ProcessHeaders 处理区块头部列表
func (d *DPoS) ProcessHeaders(headers []*types.Header) error {
	// For DPoS, we need to update round state when receiving new blocks
	d.logger.Debug("🔄 DPoS ProcessHeaders被调用", "count", len(headers), "runtimeIsNil", d.runtime == nil)

	// Update round state for each new block
	for _, header := range headers {
		d.logger.Debug("🔄 DPoS处理区块头部", "blockNumber", header.Number, "blockHash", header.Hash.String()[:16])

		// 检查同步节点接收到的区块状态根
		d.logger.Debug("🔍 同步节点接收区块状态根检查",
			"blockNumber", header.Number,
			"stateRoot", header.StateRoot.String(),
			"blockHash", header.Hash.String()[:16])

		if err := d.processBlockVotesFromHeader(header); err != nil {
			d.logger.Error("failed to process block votes from header", "blockNumber", header.Number, "blockHash", header.Hash, "error", err)
		}
		// 同步更新轮次状态
		d.updateRoundState(header)
	}

	return nil
}
