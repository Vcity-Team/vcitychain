package dpos

import (
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// dposRuntime 是DPoS共识的运行时状态管理器
type dposRuntime struct {
	config  *runtimeConfig
	backend dposBackend
	logger  hclog.Logger

	// 网络服务
	network *network.Server

	// 网络主题缓存
	signatureRequestTopic  *network.Topic
	signatureResponseTopic *network.Topic
	signatureQueryTopic    *network.Topic
	topicMutex             sync.RWMutex

	// 运行时状态
	currentRound            uint64
	lastBlockProductionTime time.Time // 记录上次本地出块的时间
	lastBlockNumber         uint64    // 记录上次出块的区块号
	delegates               validator.AccountSet
	voters                  map[types.Address]*VoterInfo
	pendingVotes            []*VoteMessage

	// 签名请求存储 - 用于新节点查询
	pendingSignatureRequests map[types.Hash]*SignatureRequest
	signatureRequestMutex    sync.RWMutex

	// 定时器
	voteTimer *time.Ticker

	// 🆕 网络健康监控
	networkHealthTimer *time.Ticker
	lastNetworkCheck   time.Time

	// 控制通道
	closeCh chan struct{}

	// 锁
	lock sync.RWMutex

	// 签名请求去重机制 - 避免日志刷屏
	processedSignatureRequests map[string]time.Time
	signatureRequestDedupMutex sync.RWMutex

	// 签名响应广播去重机制 - 避免日志刷屏
	processedSignatureResponses map[string]time.Time
	signatureResponseDedupMutex sync.RWMutex

	// 签名响应生成去重机制 - 避免日志刷屏
	processedSignatureGenerations map[string]time.Time
	signatureGenerationDedupMutex sync.RWMutex

	// 🆕 缓存第一次成功获取的4个验证者，确保整个区块生产过程中使用相同的验证者集合
	cachedProductionValidators validator.AccountSet

	// 私钥缓存 - 避免重复文件读取
	blsPrivateKeyCache      *bls.PrivateKey
	blsPrivateKeyCacheMutex sync.RWMutex
	blsPrivateKeyCacheTime  time.Time

	// 并发控制 - 限制同时处理的签名请求数量
	signatureRequestSemaphore chan struct{}
	maxConcurrentSignatures   int

	// 🆕 投票签名验证相关
	processedVotes map[string]bool // 防重放：已处理的投票nonce
	voteMutex      sync.RWMutex    // 保护processedVotes的并发访问

	// 网络集成管理器
	networkIntegration *NetworkIntegration

	// 资源监控
	resourceMonitor *ResourceMonitor

	// 🆕 防重复日志机制
	lastLogTime map[string]time.Time
	logMutex    sync.RWMutex

	// 🆕 下一个epoch的验证者集合（只在epoch边界区块时设置，用于写入ExtraData）
	nextEpochValidators validator.AccountSet
}

