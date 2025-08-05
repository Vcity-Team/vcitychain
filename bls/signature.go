package bls

import (
	"fmt"
	"math/big"

	bn256 "github.com/umbracle/go-eth-bn256"
)

const (
	SignatureSize = 64
)

var (
	ErrInvalidSignatureSize = fmt.Errorf("signature must be %d bytes long", SignatureSize)
)

// Signature represents bls signature which is point on the curve
type Signature struct {
	g1 *bn256.G1
}

// Verify checks the BLS signature of the message against the public key of its signer
func (s *Signature) Verify(pub *PublicKey, message, domain []byte) bool {
	// fmt.Printf("DEBUG: BLS Verify - 开始验证单个签名\n")
	// fmt.Printf("DEBUG: 签名点: %x\n", s.g1.Marshal())
	// fmt.Printf("DEBUG: 公钥: %x\n", pub.Marshal())
	// fmt.Printf("DEBUG: 消息: %x\n", message)
	// fmt.Printf("DEBUG: 域: %x\n", domain)

	point, err := hashToPoint(message, domain)
	if err != nil {
		// fmt.Printf("DEBUG: BLS Verify - hashToPoint失败: %v\n", err)
		return false
	}

	// fmt.Printf("DEBUG: BLS Verify - hashToPoint成功: %x\n", point.Marshal())

	result := bn256.PairingCheck([]*bn256.G1{s.g1, point}, []*bn256.G2{negG2Point, pub.g2})
	// fmt.Printf("DEBUG: BLS Verify - 配对检查结果: %v\n", result)

	return result
}

// VerifyAggregated checks the BLS signature of the message against the aggregated public keys of its signers
func (s *Signature) VerifyAggregated(publicKeys []*PublicKey, msg, domain []byte) bool {
	// fmt.Printf("DEBUG: BLS VerifyAggregated - 开始验证聚合签名\n")
	// fmt.Printf("DEBUG: 消息长度: %d, 消息内容: %x\n", len(msg), msg)
	// fmt.Printf("DEBUG: 域长度: %d, 域内容: %x\n", len(domain), domain)
	// fmt.Printf("DEBUG: 公钥数量: %d\n", len(publicKeys))

	// 打印每个公钥的信息
	// for i, pubKey := range publicKeys {
	// 	if pubKey != nil {
	// 		fmt.Printf("DEBUG: 公钥 %d: %x\n", i, pubKey.Marshal())
	// 	} else {
	// 		fmt.Printf("DEBUG: 公钥 %d: nil\n", i)
	// 	}
	// }

	// 聚合公钥
	aggregatedPubKey := PublicKeys(publicKeys).Aggregate()
	// fmt.Printf("DEBUG: 聚合公钥: %x\n", aggregatedPubKey.Marshal())

	// 调用单个验证
	result := s.Verify(aggregatedPubKey, msg, domain)
	// fmt.Printf("DEBUG: BLS VerifyAggregated - 验证结果: %v\n", result)

	return result
}

// Marshal the signature to bytes.
func (s *Signature) Marshal() ([]byte, error) {
	return s.g1.Marshal(), nil
}

// ToBigInt marshalls signature (which is point) to 2 big ints - one for each coordinate
func (s Signature) ToBigInt() ([2]*big.Int, error) {
	sig, err := s.Marshal()
	if err != nil {
		return [2]*big.Int{}, err
	}

	return [2]*big.Int{
		new(big.Int).SetBytes(sig[0:32]),
		new(big.Int).SetBytes(sig[32:64]),
	}, nil
}

// UnmarshalSignature reads the signature from the given byte array
func UnmarshalSignature(raw []byte) (*Signature, error) {
	if len(raw) < SignatureSize {
		return nil, ErrInvalidSignatureSize
	}

	g1 := new(bn256.G1)
	if _, err := g1.Unmarshal(raw); err != nil {
		return nil, err
	}

	// check if it is the point at infinity
	if g1.IsInfinity() {
		return nil, errInfinityPoint
	}

	// check if not part of the subgroup
	if !g1.InCorrectSubgroup() {
		return nil, fmt.Errorf("incorrect subgroup")
	}

	return &Signature{g1: g1}, nil
}

// Signatures is a slice of signatures
type Signatures []*Signature

// Aggregate aggregates all signatures into one
func (sigs Signatures) Aggregate() *Signature {
	g1 := new(bn256.G1)

	for _, sig := range sigs {
		g1.Add(g1, sig.g1)
	}

	return &Signature{g1: g1}
}
