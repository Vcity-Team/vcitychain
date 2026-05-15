package blockchain

import (
	"errors"
	"fmt"

	"github.com/Vcity-Team/vcitychain/blockchain/storage"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
)

// HealCanonicalBlockState rewinds execution-layer state to the parent of block and replays block (B).
// Used when VerifyFinalizedBlock fails with state root mismatch but the parent header matches the network.
func (b *Blockchain) HealCanonicalBlockState(block *types.Block) error {
	if block == nil || block.Header == nil {
		return errors.New("heal: nil block")
	}
	if block.Number() == 0 {
		return errors.New("heal: genesis block")
	}

	b.writeLock.Lock()
	defer b.writeLock.Unlock()

	parentNumber := block.Number() - 1
	parentHash := block.ParentHash()

	parentHeader, ok := b.readHeader(parentHash)
	if !ok || parentHeader == nil {
		return fmt.Errorf("heal: parent header not found at %d", parentNumber)
	}
	if parentHeader.Number != parentNumber {
		return fmt.Errorf("heal: parent number mismatch")
	}

	currentHead := b.Header()
	if currentHead != nil && currentHead.Number >= block.Number() {
		if localHash, ok := b.db.ReadCanonicalHash(block.Number()); ok && localHash != (types.Hash{}) && localHash != block.Hash() {
			b.logger.Warn("⚠️ heal: same-height canonical fork detected, rolling back chain head",
				"height", block.Number(),
				"localHash", localHash.String()[:18],
				"targetHash", block.Hash().String()[:18],
			)
			if err := b.rollbackToHeightLocked(parentNumber); err != nil {
				return fmt.Errorf("heal: rollback head: %w", err)
			}
			if healer, ok := b.consensus.(StateHealer); ok {
				if err := healer.OnRewindToHeight(parentNumber); err != nil {
					return fmt.Errorf("heal: consensus rewind: %w", err)
				}
				if oldBlock, ok := b.GetBlockByHash(localHash, true); ok && oldBlock != nil {
					if err := healer.PrepareSameHeightForkReplay(oldBlock); err != nil {
						return fmt.Errorf("heal: prepare fork replay: %w", err)
					}
				}
			}
		}
	}

	if healer, ok := b.consensus.(StateHealer); ok {
		if err := healer.PrepareSameHeightForkReplay(block); err != nil {
			return fmt.Errorf("heal: prepare canonical replay: %w", err)
		}
	}

	blockResult, err := b.executeBlockTransactionsLocked(block, ExecutionCommit)
	if err != nil {
		return fmt.Errorf("heal: execute block: %w", err)
	}
	if err := blockResult.verifyBlockResult(block); err != nil {
		return fmt.Errorf("heal: verify after replay: %w", err)
	}

	b.receiptsCache.Add(block.Hash(), blockResult.Receipts)
	b.logger.Info("✅ heal: state replay succeeded",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:18],
		"stateRoot", blockResult.Root.String()[:18],
	)

	return nil
}

// rollbackToHeightLocked is RollbackToHeight without acquiring writeLock (caller holds lock).
func (b *Blockchain) rollbackToHeightLocked(targetHeight uint64) error {
	current := b.Header()
	if current == nil {
		return fmt.Errorf("rollback: current header is nil")
	}
	if targetHeight >= current.Number {
		return nil
	}

	targetHash, ok := b.db.ReadCanonicalHash(targetHeight)
	if !ok {
		return fmt.Errorf("rollback: canonical hash not found at height %d", targetHeight)
	}

	targetHeader, ok := b.GetHeaderByHash(targetHash)
	if !ok || targetHeader == nil {
		return fmt.Errorf("rollback: header not found at height %d", targetHeight)
	}

	targetTD, ok := b.GetTD(targetHash)
	if !ok || targetTD == nil {
		return fmt.Errorf("rollback: total difficulty not found at height %d", targetHeight)
	}

	batchWriter := storage.NewBatchWriter(b.db)
	batchWriter.PutHeadHash(targetHash)
	batchWriter.PutHeadNumber(targetHeight)
	batchWriter.PutCanonicalHash(targetHeight, targetHash)

	for h := targetHeight + 1; h <= current.Number; h++ {
		key := append(append([]byte{}, storage.CANONICAL...), common.EncodeUint64ToBytes(h)...)
		batchWriter.DeleteKey(key)
	}

	if err := batchWriter.WriteBatch(); err != nil {
		return fmt.Errorf("rollback: write batch failed: %w", err)
	}

	b.setCurrentHeader(targetHeader, targetTD)
	b.logger.Warn("⚠️ chain head rolled back (heal)", "from", current.Number, "to", targetHeight, "hash", targetHash.String())

	return nil
}
