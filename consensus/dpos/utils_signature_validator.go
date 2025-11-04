package dpos

import (
	"crypto/ecdsa"
	"math/big"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

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




