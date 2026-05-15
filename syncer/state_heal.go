package syncer

import (
	"errors"
	"strings"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/types"
)

func isStateRootVerifyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, blockchain.ErrInvalidStateRoot) {
		return true
	}
	return strings.Contains(err.Error(), "invalid block state root")
}

// tryHealStateRootMismatch attempts B-path heal when verify fails due to state root mismatch.
func (s *syncer) tryHealStateRootMismatch(block *types.Block, verifyErr error) (*types.FullBlock, error) {
	if !isStateRootVerifyError(verifyErr) {
		return nil, verifyErr
	}
	healer, ok := s.blockchain.(interface {
		HealCanonicalBlockState(block *types.Block) error
	})
	if !ok {
		return nil, verifyErr
	}
	s.logger.Warn("⚠️ StateRoot 验证失败，尝试自动回滚并重放区块状态",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:18],
	)
	if err := healer.HealCanonicalBlockState(block); err != nil {
		s.logger.Error("自动状态修复失败", "blockNumber", block.Number(), "error", err)
		return nil, verifyErr
	}
	fullBlock, err := s.blockchain.VerifyFinalizedBlock(block)
	if err != nil {
		s.logger.Error("状态修复后仍无法验证区块", "blockNumber", block.Number(), "error", err)
		return nil, err
	}
	s.logger.Info("✅ 自动状态修复成功，区块验证通过",
		"blockNumber", block.Number(),
		"blockHash", block.Hash().String()[:18],
	)
	return fullBlock, nil
}
