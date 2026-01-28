package dpos

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 在文件顶部添加工具函数
func addr(n byte) types.Address {
	var a types.Address
	a[0] = n
	return a
}

// TestDPoSConfig 测试配置管理
func TestDPoSConfig(t *testing.T) {
	// 测试默认配置
	config := DefaultDPoSConfig()
	assert.NotNil(t, config)
	assert.Equal(t, uint64(21), config.DPoSValidatorsCount)
	assert.Equal(t, common.Duration{Duration: 15 * time.Second}, config.BlockTime)

	// 测试配置验证
	err := config.Validate()
	assert.NoError(t, err)

	// 测试无效配置
	invalidConfig := DefaultDPoSConfig()
	invalidConfig.BlockTime = common.Duration{Duration: 0}
	err = invalidConfig.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "block_time must be positive")

	// 测试配置摘要
	summary := config.GetConfigSummary()
	assert.NotNil(t, summary)
	assert.Equal(t, "15s", summary["block_time"])
	assert.Equal(t, uint64(21), summary["delegate_count"])
}

// TestVoteValidation 测试投票验证
func TestVoteValidation(t *testing.T) {
	dpos := createTestDPoS(t)

	// 创建有效投票
	validVote := &VoteMessage{
		Voter:     addr(1),
		Delegate:  addr(2),
		Amount:    big.NewInt(1000000000000000000), // 1 token
		Round:     1,
		Timestamp: uint64(time.Now().Unix()),
		Signature: []byte("test-signature"),
	}

	// 测试有效投票（完整验证）
	err := dpos.validateVote(validVote, false, false, false)
	assert.NoError(t, err)

	// 测试无效金额
	invalidAmountVote := &VoteMessage{
		Voter:     addr(1),
		Delegate:  addr(2),
		Amount:    big.NewInt(0),
		Round:     1,
		Timestamp: uint64(time.Now().Unix()),
		Signature: []byte("test-signature"),
	}
	err = dpos.validateVote(invalidAmountVote, false, false, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "vote amount cannot be zero")

	// 测试金额超限
	tooLargeVote := &VoteMessage{
		Voter:     addr(1),
		Delegate:  addr(2),
		Amount:    new(big.Int).Mul(big.NewInt(1000000), big.NewInt(1e18)), // 1M tokens
		Round:     1,
		Timestamp: uint64(time.Now().Unix()),
		Signature: []byte("test-signature"),
	}
	err = dpos.validateVote(tooLargeVote, false, false, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "vote amount exceeds maximum")
}

// TestVoteProcessing 测试投票处理
func TestVoteProcessing(t *testing.T) {
	dpos := createTestDPoS(t)

	// 创建投票
	vote := &VoteMessage{
		Voter:     addr(1),
		Delegate:  addr(2),
		Amount:    big.NewInt(1000000000000000000), // 1 token
		Round:     1,
		Timestamp: uint64(time.Now().Unix()),
		Signature: []byte("test-signature"),
	}

	// 处理投票
	err := dpos.processVote(vote)
	assert.NoError(t, err)

	// 验证投票者信息
	voter, exists := dpos.voters[vote.Voter]
	assert.True(t, exists)
	assert.Equal(t, vote.Amount, voter.VotingPower)
	assert.Contains(t, voter.VotedDelegates, vote.Delegate)

	// 验证受托人投票权重
	delegateFound := false
	for _, del := range dpos.delegates {
		if del.Address == vote.Delegate {
			assert.Equal(t, vote.Amount, del.VotingPower)
			delegateFound = true
			break
		}
	}
	assert.True(t, delegateFound)

	// 测试重复投票
	err = dpos.processVote(vote)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "vote nonce already used")
}

// TestDelegateUpdate 测试受托人更新
func TestDelegateUpdate(t *testing.T) {
	dpos := createTestDPoS(t)

	// 添加初始受托人
	initialDelegates := []*validator.ValidatorMetadata{
		{Address: addr(1), VotingPower: big.NewInt(1000), IsActive: true},
		{Address: addr(2), VotingPower: big.NewInt(2000), IsActive: true},
		{Address: addr(3), VotingPower: big.NewInt(3000), IsActive: true},
	}
	dpos.delegates = initialDelegates

	// 创建测试区块
	block := &types.FullBlock{
		Block: &types.Block{
			Header: &types.Header{
				Number: 100,
			},
		},
	}

	// 更新受托人
	err := dpos.updateDelegates(block)
	assert.NoError(t, err)

	// 验证受托人排序（按投票权重降序）
	assert.Len(t, dpos.delegates, 3)
	assert.Equal(t, addr(3), dpos.delegates[0].Address) // 最高投票权重
	assert.Equal(t, addr(2), dpos.delegates[1].Address)
	assert.Equal(t, addr(1), dpos.delegates[2].Address) // 最低投票权重
}

// TestRewardCalculation 测试奖励计算
func TestRewardCalculation(t *testing.T) {
	dpos := createTestDPoS(t)

	// 设置测试数据
	dpos.voters[addr(1)] = &VoterInfo{
		Address:     addr(1),
		VotingPower: big.NewInt(1000),
	}
	dpos.voters[addr(2)] = &VoterInfo{
		Address:     addr(2),
		VotingPower: big.NewInt(2000),
	}

	dpos.delegates = []*validator.ValidatorMetadata{
		{Address: addr(1), VotingPower: big.NewInt(1000), IsActive: true},
		{Address: addr(2), VotingPower: big.NewInt(2000), IsActive: true},
	}

	// 计算奖励
	reward1 := dpos.calculateReward(addr(1))
	reward2 := dpos.calculateReward(addr(2))

	// 验证奖励比例
	totalPower := big.NewInt(3000)
	expectedReward1 := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000))
	expectedReward1.Div(expectedReward1, totalPower)

	expectedReward2 := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(2000))
	expectedReward2.Div(expectedReward2, totalPower)

	assert.Equal(t, expectedReward1, reward1)
	assert.Equal(t, expectedReward2, reward2)

	// 验证总奖励不超过区块奖励
	totalReward := new(big.Int).Add(reward1, reward2)
	assert.True(t, totalReward.Cmp(big.NewInt(1e18)) <= 0)
}

// TestBatchProcessing 测试批量处理
func TestBatchProcessing(t *testing.T) {
	dpos := createTestDPoS(t)

	// 创建批量投票
	votes := []*VoteMessage{
		{
			Voter:     addr(1),
			Delegate:  addr(10),
			Amount:    big.NewInt(1000000000000000000),
			Round:     1,
			Timestamp: uint64(time.Now().Unix()),
			Signature: []byte("sig1"),
		},
		{
			Voter:     addr(2),
			Delegate:  addr(10),
			Amount:    big.NewInt(2000000000000000000),
			Round:     1,
			Timestamp: uint64(time.Now().Unix()),
			Signature: []byte("sig2"),
		},
		{
			Voter:     addr(3),
			Delegate:  addr(11),
			Amount:    big.NewInt(3000000000000000000),
			Round:     1,
			Timestamp: uint64(time.Now().Unix()),
			Signature: []byte("sig3"),
		},
	}

	// 批量处理投票
	dpos.processVoteBatch(votes)

	// 验证投票者信息
	assert.Equal(t, big.NewInt(1000000000000000000), dpos.voters[addr(1)].VotingPower)
	assert.Equal(t, big.NewInt(2000000000000000000), dpos.voters[addr(2)].VotingPower)
	assert.Equal(t, big.NewInt(3000000000000000000), dpos.voters[addr(3)].VotingPower)

	// 验证受托人投票权重
	delegate10Found := false
	delegate11Found := false
	for _, del := range dpos.delegates {
		var addr10 types.Address
		addr10[0] = 10
		var addr11 types.Address
		addr11[0] = 11
		if del.Address == addr10 {
			expectedPower := new(big.Int).Add(big.NewInt(1000000000000000000), big.NewInt(2000000000000000000))
			assert.Equal(t, expectedPower, del.VotingPower)
			delegate10Found = true
		}
		if del.Address == addr11 {
			assert.Equal(t, big.NewInt(3000000000000000000), del.VotingPower)
			delegate11Found = true
		}
	}
	assert.True(t, delegate10Found)
	assert.True(t, delegate11Found)
}

// TestMetrics 测试指标收集
func TestMetrics(t *testing.T) {
	collector := NewDPoSMetricsCollector(0) // 使用随机端口
	defer collector.Close()

	// 记录投票
	start := time.Now()
	collector.RecordVote(addr(1), addr(2), big.NewInt(1000), time.Since(start))

	// 记录受托人更新
	collector.RecordDelegateUpdate([]types.Address{addr(1), addr(2)}, 100*time.Millisecond)

	// 记录区块生产
	collector.RecordBlockProduction(100, common.Duration{Duration: 15 * time.Second}, big.NewInt(1000000))

	// 记录活跃投票者
	collector.RecordActiveVoters(50)

	// 获取投票权重统计
	stats := collector.GetVotingPowerStats()
	assert.NotNil(t, stats)
}

// BenchmarkVoteProcessing 投票处理性能测试
func BenchmarkVoteProcessing(b *testing.B) {
	dpos := createTestDPoS(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vote := &VoteMessage{
			Voter:     addr(byte(i % 100)),
			Delegate:  addr(byte(i % 50)),
			Amount:    big.NewInt(1000000000000000000),
			Round:     uint64(i),
			Timestamp: uint64(time.Now().Unix()),
			Signature: []byte("test-signature"),
		}
		dpos.processVote(vote)
	}
}

// BenchmarkBatchProcessing 批量处理性能测试
func BenchmarkBatchProcessing(b *testing.B) {
	dpos := createTestDPoS(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		votes := make([]*VoteMessage, 100)
		for j := 0; j < 100; j++ {
			votes[j] = &VoteMessage{
				Voter:     addr(byte((i + j) % 100)),
				Delegate:  addr(byte((i + j) % 50)),
				Amount:    big.NewInt(1000000000000000000),
				Round:     uint64(i),
				Timestamp: uint64(time.Now().Unix()),
				Signature: []byte("test-signature"),
			}
		}
		dpos.processVoteBatch(votes)
	}
}

// 创建测试DPoS实例
func createTestDPoS(t interface{}) *DPoS {
	config := DefaultDPoSConfig()
	config.DPoSValidatorsCount = 10
	config.BlockTime = common.Duration{Duration: 1 * time.Second}

	dpos := &DPoS{
		config:    config,
		voters:    make(map[types.Address]*VoterInfo),
		delegates: make([]*validator.ValidatorMetadata, 0),
		lock:      sync.RWMutex{},
	}

	// 初始化受托人
	dpos.delegates = []*validator.ValidatorMetadata{
		{Address: addr(10), VotingPower: big.NewInt(0), IsActive: true},
		{Address: addr(11), VotingPower: big.NewInt(0), IsActive: true},
	}

	return dpos
}

// TestIntegration 集成测试
func TestIntegration(t *testing.T) {
	dpos := createTestDPoS(t)

	// 1. 处理多个投票
	votes := []*VoteMessage{
		{Voter: addr(1), Delegate: addr(10), Amount: big.NewInt(1000), Round: 1, Timestamp: uint64(time.Now().Unix()), Signature: []byte("sig1")},
		{Voter: addr(2), Delegate: addr(10), Amount: big.NewInt(2000), Round: 1, Timestamp: uint64(time.Now().Unix()), Signature: []byte("sig2")},
		{Voter: addr(3), Delegate: addr(11), Amount: big.NewInt(3000), Round: 1, Timestamp: uint64(time.Now().Unix()), Signature: []byte("sig3")},
	}

	for _, vote := range votes {
		err := dpos.processVote(vote)
		require.NoError(t, err)
	}

	// 2. 验证投票者状态
	assert.Equal(t, big.NewInt(1000), dpos.voters[addr(1)].VotingPower)
	assert.Equal(t, big.NewInt(2000), dpos.voters[addr(2)].VotingPower)
	assert.Equal(t, big.NewInt(3000), dpos.voters[addr(3)].VotingPower)

	// 3. 验证受托人投票权重
	delegate10Found := false
	delegate11Found := false
	for _, del := range dpos.delegates {
		if del.Address == addr(10) {
			assert.Equal(t, big.NewInt(3000), del.VotingPower) // 1000 + 2000
			delegate10Found = true
		}
		if del.Address == addr(11) {
			assert.Equal(t, big.NewInt(3000), del.VotingPower)
			delegate11Found = true
		}
	}
	assert.True(t, delegate10Found)
	assert.True(t, delegate11Found)

	// 4. 更新受托人集合
	block := &types.FullBlock{
		Block: &types.Block{
			Header: &types.Header{Number: 100},
		},
	}
	err := dpos.updateDelegates(block)
	require.NoError(t, err)

	// 5. 计算奖励
	reward1 := dpos.calculateReward(addr(1))
	reward2 := dpos.calculateReward(addr(2))
	reward3 := dpos.calculateReward(addr(3))

	// 验证奖励总和不超过区块奖励
	totalReward := new(big.Int).Add(reward1, reward2)
	totalReward.Add(totalReward, reward3)
	assert.True(t, totalReward.Cmp(big.NewInt(1e18)) <= 0)
}
