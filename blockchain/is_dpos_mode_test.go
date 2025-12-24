package blockchain

import (
	"fmt"
	"testing"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
)

// TestIsDPoSMode_WithDPoSConsensus 测试当 consensus 类型名称包含 "DPoS" 时，isDPoSMode 返回 true
func TestIsDPoSMode_WithDPoSConsensus(t *testing.T) {
	b := NewTestBlockchain(t, nil)

	// 创建一个类型名称包含 "DPoS" 的 mock consensus
	mockDPoS := &DPoSConsensus{}
	b.consensus = mockDPoS

	// 打印 isDPoSMode 的返回值
	result := b.isDPoSMode()
	fmt.Printf("isDPoSMode() = %v (consensus type: %T)\n", result, b.consensus)

	// 测试 isDPoSMode 应该返回 true（因为类型名称包含 "DPoS"）
	assert.True(t, result, "当 consensus 类型名称包含 'DPoS' 时，isDPoSMode 应该返回 true")
}

// TestIsDPoSMode_WithDPoSConsensusLowercase 测试当 consensus 类型名称包含 "dpos"（小写）时
func TestIsDPoSMode_WithDPoSConsensusLowercase(t *testing.T) {
	b := NewTestBlockchain(t, nil)

	// 创建一个类型名称包含 "dpos"（小写）的 mock consensus
	mockDPoS := &dposConsensus{}
	b.consensus = mockDPoS

	// 打印 isDPoSMode 的返回值
	result := b.isDPoSMode()
	fmt.Printf("isDPoSMode() = %v (consensus type: %T)\n", result, b.consensus)

	// 测试 isDPoSMode 应该返回 true（因为类型名称包含 "dpos"）
	assert.True(t, result, "当 consensus 类型名称包含 'dpos' 时，isDPoSMode 应该返回 true")
}

// TestIsDPoSMode_WithIBFTConsensus 测试当 consensus 类型名称不包含 "dpos" 时，isDPoSMode 返回 false
func TestIsDPoSMode_WithIBFTConsensus(t *testing.T) {
	b := NewTestBlockchain(t, nil)

	// 创建一个类型名称不包含 "dpos" 的 mock consensus
	mockIBFT := &IBFTConsensus{}
	b.consensus = mockIBFT

	// 打印 isDPoSMode 的返回值
	result := b.isDPoSMode()
	fmt.Printf("isDPoSMode() = %v (consensus type: %T)\n", result, b.consensus)

	// 测试 isDPoSMode 应该返回 false
	assert.False(t, result, "当 consensus 类型名称不包含 'dpos' 时，isDPoSMode 应该返回 false")
}

// TestIsDPoSMode_WithNilConsensus 测试当 consensus 为 nil 时，isDPoSMode 返回 false
func TestIsDPoSMode_WithNilConsensus(t *testing.T) {
	b := NewTestBlockchain(t, nil)
	b.consensus = nil

	// 打印 isDPoSMode 的返回值
	result := b.isDPoSMode()
	fmt.Printf("isDPoSMode() = %v (consensus type: %T)\n", result, b.consensus)

	// 测试 isDPoSMode 应该返回 false
	assert.False(t, result, "当 consensus 为 nil 时，isDPoSMode 应该返回 false")
}

// baseVerifier 实现 Verifier 接口的基础类型
type baseVerifier struct{}

func (b *baseVerifier) VerifyHeader(header *types.Header) error {
	return nil
}

func (b *baseVerifier) ProcessHeaders(headers []*types.Header) error {
	return nil
}

func (b *baseVerifier) GetBlockCreator(header *types.Header) (types.Address, error) {
	return types.Address{}, nil
}

func (b *baseVerifier) PreCommitState(block *types.Block, txn *state.Transition) error {
	return nil
}

// DPoSConsensus 类型名称包含 "DPoS"，嵌入 baseVerifier 以实现 Verifier 接口
type DPoSConsensus struct {
	*baseVerifier
}

// dposConsensus 类型名称包含 "dpos"（小写），嵌入 baseVerifier 以实现 Verifier 接口
type dposConsensus struct {
	*baseVerifier
}

// IBFTConsensus 类型名称不包含 "dpos"，嵌入 baseVerifier 以实现 Verifier 接口
type IBFTConsensus struct {
	*baseVerifier
}
