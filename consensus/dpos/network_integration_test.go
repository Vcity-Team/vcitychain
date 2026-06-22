//go:build integration

package dpos

import (
	"sync"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
)

func TestNetworkIntegration_SignatureRequestBroadcast(t *testing.T) {
	// 创建测试日志器
	logger := hclog.NewNullLogger()

	// 创建网络服务器配置
	config := &network.Config{
		NoDiscover: true, // 禁用发现以避免实际网络连接
	}

	// 创建网络服务器
	networkServer, err := network.NewServer(logger, config)
	if err != nil {
		t.Skipf("无法创建网络服务器，跳过测试: %v", err)
	}

	// 创建网络集成
	ni := NewNetworkIntegration(networkServer, logger)

	// 启动网络集成
	err = ni.Start()
	assert.NoError(t, err)
	defer ni.Stop()

	// 创建测试签名请求
	request := &SignatureRequest{
		BlockNumber:    100,
		BlockHash:      types.StringToHash("0x1234567890abcdef"),
		CheckpointHash: types.StringToHash("0xabcdef1234567890"),
		Round:          1,
		Proposer:       types.StringToAddress("0x1111111111111111111111111111111111111111"),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 测试广播签名请求
	err = ni.BroadcastSignatureRequest(request)
	assert.NoError(t, err)

	// 等待一段时间让消息处理
	time.Sleep(100 * time.Millisecond)
}

func TestDPoSRuntime_QueryPendingSignatureRequests(t *testing.T) {
	// 创建测试日志器
	logger := hclog.NewNullLogger()

	// 创建网络服务器配置
	config := &network.Config{
		NoDiscover: true, // 禁用发现以避免实际网络连接
	}

	// 创建网络服务器
	networkServer, err := network.NewServer(logger, config)
	if err != nil {
		t.Skipf("无法创建网络服务器，跳过测试: %v", err)
	}

	// 创建DPoS运行时
	runtime := &dposRuntime{
		logger:  logger.Named("test-runtime"),
		network: networkServer,
		closeCh: make(chan struct{}),
	}

	// 测试查询待处理签名请求（应该不会出错，即使没有连接的节点）
	runtime.queryPendingSignatureRequests()

	// 清理
	close(runtime.closeCh)
}

func TestDPoSRuntime_PeriodicPeerCheck(t *testing.T) {
	// 创建测试日志器
	logger := hclog.NewNullLogger()

	// 创建网络服务器配置
	config := &network.Config{
		NoDiscover: true, // 禁用发现以避免实际网络连接
	}

	// 创建网络服务器
	networkServer, err := network.NewServer(logger, config)
	if err != nil {
		t.Skipf("无法创建网络服务器，跳过测试: %v", err)
	}

	// 创建DPoS运行时
	runtime := &dposRuntime{
		logger:  logger.Named("test-runtime"),
		network: networkServer,
		closeCh: make(chan struct{}),
	}

	// 启动定期检查
	go runtime.periodicPeerCheck()

	// 等待一段时间让检查运行
	time.Sleep(100 * time.Millisecond)

	// 清理
	close(runtime.closeCh)
}

func TestDPoSRuntime_SignatureQueryMechanism(t *testing.T) {
	// 创建测试日志器
	logger := hclog.NewNullLogger()

	// 创建网络服务器配置
	config := &network.Config{
		NoDiscover: true, // 禁用发现以避免实际网络连接
	}

	// 创建网络服务器
	networkServer, err := network.NewServer(logger, config)
	if err != nil {
		t.Skipf("无法创建网络服务器，跳过测试: %v", err)
	}

	// 创建DPoS运行时
	runtime := &dposRuntime{
		logger:  logger.Named("test-runtime"),
		network: networkServer,
		closeCh: make(chan struct{}),
	}

	// 初始化签名请求存储
	runtime.pendingSignatureRequests = make(map[types.Hash]*SignatureRequest)
	runtime.signatureRequestMutex = sync.RWMutex{}

	// 添加一个测试签名请求
	testRequest := &SignatureRequest{
		BlockNumber:    100,
		BlockHash:      types.StringToHash("0x1234567890abcdef"),
		CheckpointHash: types.StringToHash("0xabcdef1234567890"),
		Round:          1,
		Proposer:       types.StringToAddress("0x1111111111111111111111111111111111111111"),
		Timestamp:      uint64(time.Now().Unix()),
	}

	runtime.signatureRequestMutex.Lock()
	runtime.pendingSignatureRequests[testRequest.CheckpointHash] = testRequest
	runtime.signatureRequestMutex.Unlock()

	// 测试调试方法
	runtime.debugPendingSignatureRequests()

	// 测试查询方法（应该不会出错）
	runtime.queryPendingSignatureRequests()

	// 清理
	close(runtime.closeCh)
}

func TestDPoSRuntime_RoundSynchronization(t *testing.T) {
	// 创建测试配置
	config := &runtimeConfig{
		blockchain: &blockchainMock{},
		network:    &networkMock{},
		key:        &walletMock{},
		logger:     hclog.NewNullLogger(),
	}

	// 创建dposRuntime实例
	runtime := &dposRuntime{
		config:  config,
		logger:  hclog.NewNullLogger(),
		lock:    sync.RWMutex{},
		closeCh: make(chan struct{}),
	}

	// 初始化受托人集合
	runtime.delegates = validator.AccountSet{
		{Address: types.StringToAddress("0x07Ed257e12E388DCE5f975B9cCf8D040e1251989")}, // 节点1
		{Address: types.StringToAddress("0x482efA39DcFd4911aD5217F2B5812dFE3cc86d7d")}, // 节点2
		{Address: types.StringToAddress("0x7eBFadB54b11266A71c5D04740c9c1f8a2d45eb8")}, // 节点3
		{Address: types.StringToAddress("0x514292Aea20b6a012109fDB2254f7aE7A71E0424")}, // 节点4
	}

	// 设置初始状态
	runtime.currentRound = 0
	runtime.currentDelegateIndex = 0

	// 测试节点1产生区块后的轮次更新
	t.Run("Node1 produces block", func(t *testing.T) {
		oldIndex := runtime.currentDelegateIndex
		oldRound := runtime.currentRound

		runtime.updateRound()

		// 验证轮次更新
		assert.Equal(t, oldRound, runtime.currentRound)           // 轮次应该不变
		assert.Equal(t, oldIndex+1, runtime.currentDelegateIndex) // 索引应该+1
		assert.Equal(t, uint64(1), runtime.currentDelegateIndex)
	})

	// 测试节点2接收到节点1的区块后也应该更新轮次
	t.Run("Node2 receives block from Node1", func(t *testing.T) {
		// 模拟节点2的runtime
		node2Runtime := &dposRuntime{
			config:  config,
			logger:  hclog.NewNullLogger(),
			lock:    sync.RWMutex{},
			closeCh: make(chan struct{}),
		}
		node2Runtime.delegates = runtime.delegates
		node2Runtime.currentRound = 0
		node2Runtime.currentDelegateIndex = 0

		// 节点2接收到节点1的区块，应该更新轮次
		node2Runtime.updateRound()

		// 验证节点2的轮次也更新了
		assert.Equal(t, uint64(1), node2Runtime.currentDelegateIndex)
		assert.Equal(t, uint64(0), node2Runtime.currentRound) // 轮次应该不变
	})

	// 测试轮次循环
	t.Run("Round cycle", func(t *testing.T) {
		testRuntime := &dposRuntime{
			config:  config,
			logger:  hclog.NewNullLogger(),
			lock:    sync.RWMutex{},
			closeCh: make(chan struct{}),
		}
		testRuntime.delegates = runtime.delegates
		testRuntime.currentRound = 0
		testRuntime.currentDelegateIndex = 3 // 最后一个受托人

		// 更新轮次，应该循环到第一个受托人并增加轮次
		testRuntime.updateRound()

		assert.Equal(t, uint64(0), testRuntime.currentDelegateIndex) // 重置为0
		assert.Equal(t, uint64(1), testRuntime.currentRound)         // 轮次+1
	})
}
