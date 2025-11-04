package dpos

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
)

// VerifyHeader 验证区块头部
func (d *DPoS) VerifyHeader(header *types.Header) error {
	blockNumber := header.Number

	// 🆕 关键日志：追踪调用来源
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	stackTrace := string(buf[:n])
	lines := strings.Split(stackTrace, "\n")
	stackInfo := ""
	if len(lines) > 6 {
		stackInfo = strings.Join(lines[2:8], "\n")
	} else {
		stackInfo = stackTrace
	}

	d.logger.Debug("🔍 VerifyHeader被调用 - 追踪调用来源",
		"blockNumber", blockNumber,
		"blockHash", header.Hash.String()[:16],
		"timestamp", time.Now().Format("15:04:05.000"),
		"stackTrace", stackInfo)

	// 🆕 添加：检查是否是共识切换高度
	if d.config.ConsensusSwitchHeight > 0 && blockNumber == d.config.ConsensusSwitchHeight {
		d.logger.Info("🔄 共识切换高度区块，跳过DPoS验证", "blockNumber", blockNumber, "consensusSwitchHeight", d.config.ConsensusSwitchHeight)
		return nil
	}

	// 🆕 关键：在验证前等待BLS公钥加载完成
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

		// 🆕 父区块获取失败时立即退出程序
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

		// 🆕 区块头验证失败时立即退出程序
		d.logger.Error("💀 区块头验证失败，程序将立即退出")
		os.Exit(1)

		return err
	}
	d.logger.Debug("✅ DPoS VerifyHeader 验证完成", "blockNumber", blockNumber)
	return nil
}

// verifyHeaderImpl 验证区块头部的实现
func (d *DPoS) verifyHeaderImpl(parent, header *types.Header, blockTimeDrift time.Duration, parents []*types.Header) error {
	blockNumber := header.Number
	d.logger.Debug("🔍 DPoS verifyHeaderImpl 开始验证", "blockNumber", blockNumber, "extraDataLength", len(header.ExtraData))

	// 添加详细的日志 - 节点3验证区块2头部
	d.logger.Debug("=== 验证区块头部开始 ===",
		"blockNumber", header.Number,
		"blockHash", header.Hash.String(),
		"parentNumber", parent.Number,
		"parentHash", parent.Hash.String(),
		"extraDataLength", len(header.ExtraData))

	// validate header fields
	if err := validateHeaderFields(parent, header, uint64(blockTimeDrift.Seconds())); err != nil {
		// 🆕 打印parent区块信息（Info级别）
		d.logger.Info("❌ 区块头部字段验证失败 - parent信息",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String(),
			"blockTimestamp", time.Unix(int64(header.Timestamp), 0).Format("15:04:05"),
			"parentNumber", parent.Number,
			"parentHash", parent.Hash.String(),
			"parentTimestamp", time.Unix(int64(parent.Timestamp), 0).Format("15:04:05"),
			"error", err)
		d.logger.Error("区块头部字段验证失败", "error", err)
		return fmt.Errorf("failed to validate header for block %d. error = %w", header.Number, err)
	}

	// decode the extra data
	extra, err := GetIbftExtra(header.ExtraData)
	if err != nil {
		d.logger.Error("解析区块extraData失败", "error", err)
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
	d.logger.Debug("=== 验证区块头部成功 ===")
	d.logger.Debug("✅ DPoS verifyHeaderImpl 验证完成", "blockNumber", blockNumber)
	return nil
}

// ProcessHeaders 处理区块头部列表
func (d *DPoS) ProcessHeaders(headers []*types.Header) error {
	// For DPoS, we need to update round state when receiving new blocks
	d.logger.Debug("🔄 DPoS ProcessHeaders被调用", "count", len(headers), "runtimeIsNil", d.runtime == nil)

	// Update round state for each new block
	for _, header := range headers {
		d.logger.Debug("🔄 DPoS处理区块头部", "blockNumber", header.Number, "blockHash", header.Hash.String()[:16])

		// 🆕 检查同步节点接收到的区块状态根
		d.logger.Debug("🔍 同步节点接收区块状态根检查",
			"blockNumber", header.Number,
			"stateRoot", header.StateRoot.String(),
			"blockHash", header.Hash.String()[:16])

		// 直接使用header数据
		if err := d.processBlockVotesFromHeader(header); err != nil {
			d.logger.Error("failed to process block votes from header", "blockNumber", header.Number, "blockHash", header.Hash, "error", err)
		}

		// 同步更新轮次状态（这部分必须同步执行，不能异步）
		d.updateRoundState(header)

		// 🆕 验证节点执行blockchain_wrapper.ProcessBlock来处理奖励分配
		if d.config.Blockchain != nil {
			// 获取完整区块信息
			block, exists := d.config.Blockchain.GetBlockByHash(header.Hash, true)

			if exists && block != nil {
				d.logger.Debug("✅ 验证节点成功获取完整区块信息，准备调用blockchain_wrapper.ProcessBlock",
					"blockNumber", header.Number,
					"blockHash", header.Hash.String()[:16],
					"blockExists", exists)

				// 获取父区块
				parent, exists := d.config.Blockchain.GetHeader(header.ParentHash, header.Number-1)
				d.logger.Debug("🔍 验证节点获取父区块结果",
					"blockNumber", header.Number,
					"parentHash", header.ParentHash.String()[:16],
					"parentExists", exists,
					"parentIsNil", parent == nil)

				if !exists {
					d.logger.Error("❌ 验证节点无法获取父区块", "blockNumber", header.Number, "parentHash", header.ParentHash.String()[:16])
				} else {
					// 调用blockchain_wrapper.ProcessBlock执行奖励分配
					d.logger.Debug("🚀 验证节点开始调用blockchain_wrapper.ProcessBlock执行奖励分配",
						"blockNumber", header.Number,
						"blockHash", header.Hash.String()[:16])

					if fullBlock, err := d.blockchain.ProcessBlock(parent, block); err != nil {
						d.logger.Error("❌ 验证节点blockchain_wrapper.ProcessBlock调用失败", "blockNumber", header.Number, "error", err)
						// 不返回错误，继续处理其他逻辑
					} else {
						d.logger.Debug("✅ 验证节点blockchain_wrapper.ProcessBlock调用成功",
							"blockNumber", header.Number,
							"blockHash", header.Hash.String()[:16],
							"receiptsCount", len(fullBlock.Receipts))
					}
				}
			} else {
				d.logger.Debug("❌ 验证节点无法获取完整区块信息",
					"blockNumber", header.Number,
					"blockHash", header.Hash.String()[:16],
					"exists", exists,
					"blockIsNil", block == nil)
			}
		} else {
			d.logger.Debug("❌ 验证节点blockchain为nil，跳过blockchain_wrapper.ProcessBlock调用",
				"blockNumber", header.Number,
				"blockHash", header.Hash.String()[:16])
		}

		if d.config.Blockchain != nil {
			if block, exists := d.config.Blockchain.GetBlockByHash(header.Hash, true); exists && block != nil {
				// 构造FullBlock
				fullBlock := &types.FullBlock{
					Block: block,
				}

				if err := d.processEconomicSystem(fullBlock); err != nil {
					d.logger.Error("❌ 同步时处理经济系统失败", "blockNumber", header.Number, "error", err)
					// 不返回错误，继续处理其他逻辑
				} else {
					d.logger.Debug("✅ processEconomicSystem调用成功",
						"blockNumber", header.Number,
						"blockHash", header.Hash.String()[:16])
				}
			} else {
				d.logger.Warn("⚠️ 无法获取完整区块信息，跳过经济系统处理",
					"blockNumber", header.Number,
					"blockHash", header.Hash.String()[:16],
					"blockExists", exists,
					"blockIsNil", block == nil)
			}
		} else {
			d.logger.Warn("⚠️ Blockchain配置为nil，跳过经济系统处理",
				"blockNumber", header.Number,
				"blockHash", header.Hash.String()[:16])
		}

		// 🆕 在区块同步时存储验证者集合到历史数据库
		if d.state != nil && d.state.StakeStore != nil {
			d.logger.Debug("🔍 区块同步时存储验证者集合到历史数据库",
				"blockNumber", header.Number,
				"blockHash", header.Hash.String()[:16])

			// 从区块ExtraData解析验证者集合
			validators, err := d.GetDelegates(header.Number, []*types.Header{header})
			if err != nil {
				d.logger.Warn("⚠️ 从ExtraData解析验证者失败", "blockNumber", header.Number, "error", err)
			} else if len(validators) > 0 {
				// 开始数据库事务
				dbTx, err := d.state.beginDBTransaction(true) // 写事务
				if err != nil {
					d.logger.Warn("⚠️ 无法开始数据库事务", "blockNumber", header.Number, "error", err)
				} else {
					defer dbTx.Rollback()

					// 临时注释掉setDelegatesAtBlock调用进行测试
					// 存储验证者集合
					// if err := d.state.StakeStore.setDelegatesAtBlock(header.Number, validators, dbTx); err != nil {
					// 	d.logger.Warn("⚠️ 存储验证者集合失败", "blockNumber", header.Number, "error", err)
					// } else {
					// 提交事务 - 添加超时机制
					// d.logger.Debug("🔍 区块同步时开始提交数据库事务", "blockNumber", header.Number)

					// 使用超时机制防止卡死
					// commitDone := make(chan error, 1)
					// go func() {
					// 	commitDone <- dbTx.Commit()
					// }()

					// select {
					// case err := <-commitDone:
					// 	if err != nil {
					// 		d.logger.Warn("⚠️ 区块同步时提交事务失败", "blockNumber", header.Number, "error", err)
					// 	} else {
					// 		d.logger.Debug("✅ 区块同步时验证者集合已存储到历史数据库",
					// 			"blockNumber", header.Number,
					// 			"count", len(validators))
					// 	}
					// case <-time.After(3 * time.Second):
					// 	d.logger.Error("❌ 区块同步时数据库事务提交超时，强制回滚", "blockNumber", header.Number)
					// 	dbTx.Rollback()
					// }
				}
			} else {
				d.logger.Warn("⚠️ 从ExtraData解析的验证者集合为空", "blockNumber", header.Number)
			}
		}
	}

	return nil
}

