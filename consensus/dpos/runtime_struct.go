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
	lastProducedSlot        int       // 记录上次出块的 slot，防止一个 slot 内出多个区块
	lastBlockProductionTime time.Time // 记录上次本地出块的时间
	lastBlockNumber         uint64    // 记录上次出块的区块号
	currentBuildStartSlot   int       // 🔧 修复：记录当前构建开始时的 slot，用于检查 slot 是否已变化（-1 表示未设置）
	decisionSlot            int       // 保存 ShouldProduceBlockNow 判断时的 slot，用于严格比对（-1 表示未设置）
	decisionLocalTip        uint64    // 与 decisionSlot 同时保存的 localTip，用于 sync/produce 竞态复核
	delegates               validator.AccountSet
	voters                  map[types.Address]*VoterInfo
	pendingVotes            []*VoteMessage

	// 签名请求存储 - 用于新节点查询
	pendingSignatureRequests map[types.Hash]*SignatureRequest
	signatureRequestMutex    sync.RWMutex

	// 定时器
	voteTimer *time.Ticker

	// 网络健康监控
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

	// 缓存第一次成功获取的4个验证者，确保整个区块生产过程中使用相同的验证者集合
	cachedProductionValidators validator.AccountSet

	// 私钥缓存 - 避免重复文件读取
	blsPrivateKeyCache      *bls.PrivateKey
	blsPrivateKeyCacheMutex sync.RWMutex
	blsPrivateKeyCacheTime  time.Time

	// 并发控制 - 限制同时处理的签名请求数量
	signatureRequestSemaphore chan struct{}
	maxConcurrentSignatures   int

	// 同一 (proposer, checkpointHash) 仅允许一个签名任务在飞，避免 Gossip 重复投递占满并发槽
	signatureRequestInFlight      map[string]struct{}
	signatureRequestInFlightMutex sync.Mutex

	// 投票签名验证相关
	processedVotes map[string]bool // 防重放：已处理的投票nonce
	voteMutex      sync.RWMutex    // 保护processedVotes的并发访问

	// 网络集成管理器
	networkIntegration *NetworkIntegration

	// 资源监控
	resourceMonitor *ResourceMonitor

	// 防重复日志机制
	lastLogTime map[string]time.Time
	logMutex    sync.RWMutex

	// getNetworkLatestBlockNumber：当 syncer 短暂拿不到 best（GetBestPeerNumber==0）时，保留近期观测到的 peer 宣称高度，避免门禁误判「已追平」。
	networkHeadHintMu        sync.Mutex
	lastPeerAdvertisedHead   uint64
	lastPeerAdvertisedHeadAt time.Time

	// dpos_bootstrap_rpc：eth_blockNumber 缓存，用作 canonical 链尖上限（短 TTL，避免热路径打爆 RPC）
	bootstrapRPCMu       sync.Mutex
	bootstrapRPCCached   uint64
	bootstrapRPCCachedAt time.Time
}

// getBLSCommittee 返回用于 BLS 签名的验证者委员会
// 当前实现：只使用创世验证者集合（Genesis Validator Set）
// 如果获取失败，则回退到当前 delegates 集合作为兜底
func (r *dposRuntime) getBLSCommittee() validator.AccountSet {
	// 优先从 DPoS 实例获取创世验证者集合
	if r.config != nil && r.config.dposBackend != nil {
		if dposInstance, ok := r.config.dposBackend.(*DPoS); ok && dposInstance != nil {
			genesisSet := dposInstance.GetGenesisValidatorSet()
			if len(genesisSet) > 0 {
				r.logger.Debug("✅ getBLSCommittee: 使用创世验证者集合作为 BLS 委员会",
					"count", len(genesisSet))
				return genesisSet
			}
			r.logger.Debug("⚠️ getBLSCommittee: 创世验证者集合为空，回退到 delegates")
		}
	}

	// 兜底：使用当前 delegates（保持兼容性，避免 panic）
	r.lock.RLock()
	defer r.lock.RUnlock()

	if len(r.delegates) == 0 {
		return nil
	}

	r.logger.Debug("⚠️ getBLSCommittee: 使用 delegates 作为 BLS 委员会",
		"count", len(r.delegates))

	return r.delegates.Copy()
}
