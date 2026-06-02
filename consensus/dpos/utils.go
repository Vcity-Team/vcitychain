package dpos

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// ============================================================================
// 区块相关工具函数
// ============================================================================

// GetBlockCreator 获取区块创建者
func (d *DPoS) GetBlockCreator(header *types.Header) (types.Address, error) {
	return types.BytesToAddress(header.Miner), nil
}

// ============================================================================
// 提案解析工具函数
// ============================================================================

// ParseProposalInput 解析标准交易 input 中的提案载荷，返回类别：create / vote / execute
func ParseProposalInput(input []byte) (string, error) {
	if len(input) == 0 {
		return "", fmt.Errorf("empty input")
	}
	var generic map[string]any
	if err := json.Unmarshal(input, &generic); err != nil {
		return "", err
	}
	// 判断 create：包含 proposalType（parameter / validator_recovery）
	if v, ok := generic["proposalType"].(string); ok && v != "" {
		return "create", nil
	}
	// 判断 vote：包含 support 且包含 proposalId
	if _, ok := generic["support"]; ok {
		if id, ok2 := generic["proposalId"].(string); ok2 && id != "" {
			return "vote", nil
		}
	}
	// 判断 execute：仅包含 proposalId
	if id, ok := generic["proposalId"].(string); ok && id != "" {
		return "execute", nil
	}
	return "", fmt.Errorf("not a proposal payload")
}

// ============================================================================
// 签名验证工具函数
// ============================================================================

// verifyAddressMatchesPublicKey 验证地址与公钥匹配
func verifyAddressMatchesPublicKey(address types.Address, publicKey *ecdsa.PublicKey) bool {
	// 从公钥计算地址
	computedAddress := crypto.PubKeyToAddress(publicKey)
	return computedAddress == address
}

// verifyECDSASignature 验证ECDSA签名
func verifyECDSASignature(publicKey *ecdsa.PublicKey, hash []byte, signature []byte) bool {
	// 检查签名长度
	if len(signature) != 65 {
		return false
	}

	// 提取r和s值（前64字节）
	rValue := new(big.Int).SetBytes(signature[:32])
	sValue := new(big.Int).SetBytes(signature[32:64])

	// 使用ECDSA验证签名
	return ecdsa.Verify(publicKey, hash, rValue, sValue)
}

// ============================================================================
// 哈希工具函数
// ============================================================================

var setupHeaderHashFuncOnce sync.Once

// getLegacyPolyBFTExtraClean strips seals from polybft/dpos-style extra while keeping validators.
// Pre-DPoS blocks may use this layout instead of the post-switch DPoS clean format.
func getLegacyPolyBFTExtraClean(extraRaw []byte) ([]byte, error) {
	extra, err := GetDposExtra(extraRaw)
	if err != nil {
		return nil, err
	}

	clean := &Extra{
		Parent:     extra.Parent,
		Validators: extra.Validators,
		Checkpoint: extra.Checkpoint,
		Committed:  &Signature{},
	}

	return clean.MarshalRLPTo(nil), nil
}

// polyBFTHeaderHash defines the custom implementation for getting the header hash,
// because of the extraData field
func setupHeaderHashFunc() {
	setupHeaderHashFuncOnce.Do(func() {
		originalHeaderHash := types.HeaderHash

		types.HeaderHash = func(h *types.Header) types.Hash {
			// when hashing the block for signing we have to remove from
			// the extra field the seal and committed seal items
			extra, err := GetDposExtraClean(h.ExtraData)
			if err != nil {
				return types.ZeroHash
			}

			hh := h.Copy()
			hh.ExtraData = extra

			return originalHeaderHash(hh)
		}
	})
}
