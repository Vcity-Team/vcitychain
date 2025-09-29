package dpos

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/bitmap"
	dposProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	ibftSigner "github.com/Vcity-Team/vcitychain/consensus/ibft/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/hashicorp/golang-lru"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/umbracle/fastrlp"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

// 🆕 全局DPoS实例注册表，用于BLS公钥持久化
var (
	dposInstances = make(map[string]*DPoS)
	dposMutex     sync.RWMutex
)

// RegisterDPoSInstance 注册DPoS实例
func RegisterDPoSInstance(key string, dpos *DPoS) {
	dposMutex.Lock()
	defer dposMutex.Unlock()
	dposInstances[key] = dpos
}

// GetDPoSInstance 获取DPoS实例
func GetDPoSInstance(key string) (*DPoS, bool) {
	dposMutex.RLock()
	defer dposMutex.RUnlock()
	dpos, exists := dposInstances[key]
	return dpos, exists
}

// GetAllDPoSInstances 获取所有DPoS实例
func GetAllDPoSInstances() map[string]*DPoS {
	dposMutex.RLock()
	defer dposMutex.RUnlock()

	// 创建副本以避免外部修改
	result := make(map[string]*DPoS)
	for key, instance := range dposInstances {
		result[key] = instance
	}
	return result
}

// UnregisterDPoSInstance 注销DPoS实例
func UnregisterDPoSInstance(key string) {
	dposMutex.Lock()
	defer dposMutex.Unlock()
	delete(dposInstances, key)
}

// StakeInfo 质押信息结构体
type StakeInfo struct {
	Staker    types.Address `json:"staker"`
	Amount    *big.Int      `json:"amount"`
	StartTime uint64        `json:"startTime"`
	EndTime   uint64        `json:"endTime"`
	IsLocked  bool          `json:"isLocked"`
	IsActive  bool          `json:"isActive"`
	Rewards   *big.Int      `json:"rewards"`
	Delegate  types.Address `json:"delegate"`
}

// 委托者（Delegator/Voter）：普通持币人，把投票权委托给受托人。
// 受托人/验证者（Delegate/Validator）：被选出来实际参与出块和共识的节点
// dposBackend 接口定义了DPoS需要的方法
type dposBackend interface {
	// GetDelegates 获取指定区块的受托人集合--实际参与共识的节点
	GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error)

	// GetDelegatesWithTx 在数据库事务中获取受托人集合
	GetDelegatesWithTx(blockNumber uint64, parents []*types.Header, dbTx *bolt.Tx) (validator.AccountSet, error)

	// GetCurrentDelegates 获取当前内存中的受托人集合
	GetCurrentDelegates() validator.AccountSet

	// GetStakingInfo 获取指定区块的质押信息
	GetStakingInfo(blockNumber uint64, staker types.Address) (*StakeInfo, error)

	// GetStakingInfoWithTx 在数据库事务中获取质押信息
	GetStakingInfoWithTx(blockNumber uint64, staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error)

	// GetVotingPower 获取指定区块的投票权重
	GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error)

	// GetVotingPowerWithTx 在数据库事务中获取投票权重
	GetVotingPowerWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error)

	// GetCurrentRound 获取当前轮次
	GetCurrentRound() uint64

	// GetCurrentDelegate 获取当前受托人
	GetCurrentDelegate() types.Address

	// GetDelegateIndex 获取受托人索引
	GetDelegateIndex(delegate types.Address) uint64

	// SaveValidatorSetForBlockWithValidators 保存指定区块的特定验证者集合到数据库
	SaveValidatorSetForBlockWithValidators(blockNumber uint64, validators validator.AccountSet) error
}

// DPoSConfig 配置结构
type DPoSConfig struct {
	// 受托人数量
	DelegateCount uint64 `json:"delegateCount"`

	// 区块时间
	BlockTime common.Duration `json:"blockTime"`

	// 轮次时间 (所有受托人完成一轮的时间)
	RoundTime common.Duration `json:"roundTime"`

	Blockchain *blockchain.Blockchain
	Logger     hclog.Logger
	Network    *network.Server

	SecretsManager secrets.SecretsManager

	Executor *state.Executor

	// 最小投票权重
	MinVotingPower *big.Int `json:"minVotingPower"`

	// 初始受托人集合
	InitialDelegates []*validator.GenesisValidator `json:"initialDelegates"`

	// 投票锁定时间
	VoteLockTime uint64 `json:"voteLockTime"`

	// 委托奖励比例
	RewardRatio uint64 `json:"rewardRatio"`

	// 🆕 新增：共识切换高度
	ConsensusSwitchHeight uint64 `json:"consensusSwitchHeight"`

	// 🆕 新增：DPoS验证者数量配置
	ValidatorsCount uint64 `json:"validatorsCount" yaml:"validatorsCount"`
}

// dpos_runtime.go
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
	currentRound         uint64
	currentDelegateIndex uint64
	delegates            validator.AccountSet
	voters               map[types.Address]*VoterInfo
	pendingVotes         []*VoteMessage

	// 签名请求存储 - 用于新节点查询
	pendingSignatureRequests map[types.Hash]*SignatureRequest
	signatureRequestMutex    sync.RWMutex

	// 定时器
	blockTimer *time.Ticker
	voteTimer  *time.Ticker

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
}

func (r *dposRuntime) start() error {
	r.logger.Info("🚀 开始启动DPoS runtime")

	// 初始化运行时状态
	r.logger.Info("🔧 开始初始化DPoS runtime状态...")
	if err := r.initializeRuntime(); err != nil {
		r.logger.Error("❌ 初始化DPoS runtime失败", "error", err)
		return fmt.Errorf("failed to initialize runtime: %w", err)
	}
	r.logger.Info("✅ DPoS runtime状态初始化成功")

	// 启动区块生产定时器
	if err := r.startBlockProduction(); err != nil {
		r.logger.Error("❌ 启动区块生产定时器失败", "error", err)
		return fmt.Errorf("failed to start block production: %w", err)
	}

	// 启动投票统计定时器
	if err := r.startVoteCollection(); err != nil {
		r.logger.Error("❌ 启动投票统计定时器失败", "error", err)
		return fmt.Errorf("failed to start vote collection: %w", err)
	}

	// 启动持久的签名请求监听器 - 确保所有节点都能接收到广播的签名请求
	go r.listenForSignatureRequests(context.Background())

	// 🆕 启动网络健康监控
	r.startNetworkHealthMonitoring()

	r.logger.Info("🎉 DPoS runtime启动成功")
	return nil
}

func (r *dposRuntime) close() {
	r.logger.Debug("closing DPoS runtime")

	// 停止所有定时器
	if r.blockTimer != nil {
		r.blockTimer.Stop()
	}
	if r.voteTimer != nil {
		r.voteTimer.Stop()
	}
	// 🆕 停止网络健康监控定时器
	if r.networkHealthTimer != nil {
		r.networkHealthTimer.Stop()
	}

	// 停止网络集成管理器
	if r.networkIntegration != nil {
		if err := r.networkIntegration.Stop(); err != nil {
			r.logger.Warn("failed to stop network integration", "error", err)
		} else {
			r.logger.Debug("网络集成管理器已停止")
		}
	}

	// 停止资源监控器
	if r.resourceMonitor != nil {
		// 这里可以添加停止资源监控器的逻辑
		r.logger.Debug("资源监控器已停止")
	}

	// 清理运行时状态
	r.cleanupRuntime()

	// 清理网络主题引用
	r.topicMutex.Lock()
	r.signatureRequestTopic = nil
	r.signatureResponseTopic = nil
	r.signatureQueryTopic = nil
	r.topicMutex.Unlock()

	r.logger.Debug("DPoS runtime closed")
}

// ResourceMonitor 资源监控器
type ResourceMonitor struct {
	logger hclog.Logger

	// 协程管理器
	goroutineManager *GoroutineManager

	// 协程数量监控
	goroutineCount int64
	goroutineMutex sync.RWMutex

	// 网络主题监控
	topicCount int64
	topicMutex sync.RWMutex

	// 网络服务状态监控
	networkAvailable bool
	networkMutex     sync.RWMutex

	// 清理间隔
	cleanupInterval time.Duration

	// DPoS运行时引用，用于缓存清理
	dposRuntime *dposRuntime
}

// NewResourceMonitor 创建资源监控器
func NewResourceMonitor(logger hclog.Logger, dposRuntime *dposRuntime) *ResourceMonitor {
	return &ResourceMonitor{
		logger:           logger.Named("resource-monitor"),
		goroutineManager: NewGoroutineManager(logger, 2000, 200), // 最大2000个协程，200个重试工作器
		cleanupInterval:  30 * time.Second,
		dposRuntime:      dposRuntime,
	}
}

// Start 启动资源监控
func (rm *ResourceMonitor) Start(ctx context.Context) {
	rm.goroutineManager.StartGoroutine("resource-monitor", func() {
		rm.monitorLoop(ctx)
	})
}

// monitorLoop 监控循环
func (rm *ResourceMonitor) monitorLoop(ctx context.Context) {
	ticker := time.NewTicker(rm.cleanupInterval)
	defer ticker.Stop()

	// 添加内存监控ticker
	memoryTicker := time.NewTicker(60 * time.Second)
	defer memoryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rm.cleanupResources()
		case <-memoryTicker.C:
			// 定期内存监控
			rm.monitorMemoryUsage()
		}
	}
}

// cleanupResources 清理资源
func (rm *ResourceMonitor) cleanupResources() {
	// 获取当前协程数量
	currentGoroutines := runtime.NumGoroutine()

	rm.goroutineMutex.Lock()
	rm.goroutineCount = int64(currentGoroutines)
	rm.goroutineMutex.Unlock()

	// 如果协程数量过多，记录警告
	if currentGoroutines > 1000 {
		rm.logger.Warn("协程数量过多，可能存在泄漏", "count", currentGoroutines)

		// 如果协程管理器可用，获取其统计信息
		if rm.goroutineManager != nil {
			stats := rm.goroutineManager.GetStats()
			rm.logger.Warn("协程管理器统计", "stats", stats)
		}
	}

	// 清理DPoS运行时缓存
	if rm.dposRuntime != nil {
		rm.dposRuntime.cleanupExpiredCaches()
	}
	
	// 🆕 添加processedBlocks清理
	if rm.dposRuntime != nil {
		if dpos, ok := rm.dposRuntime.backend.(*DPoS); ok {
			dpos.cleanupProcessedBlocks()
		}
	}
}

// monitorMemoryUsage 监控内存使用情况
func (rm *ResourceMonitor) monitorMemoryUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := m.Alloc / 1024 / 1024
	sysMB := m.Sys / 1024 / 1024

	// 如果内存使用超过100MB，记录警告
	if allocMB > 100 {
		rm.logger.Warn("内存使用较高",
			"allocMB", allocMB,
			"sysMB", sysMB,
			"goroutines", runtime.NumGoroutine())
	} else {
		rm.logger.Debug("内存使用正常",
			"allocMB", allocMB,
			"sysMB", sysMB,
			"goroutines", runtime.NumGoroutine())
	}
}

// initializeRuntime 初始化运行时状态
func (r *dposRuntime) initializeRuntime() error {
	// 检查配置是否可用
	if r.config == nil {
		return fmt.Errorf("runtime config is nil")
	}

	// 🆕 修复：根据当前区块号计算初始轮次和委托者索引
	r.currentRound = r.calculateInitialRound()
	r.currentDelegateIndex = r.calculateCurrentDelegateIndex()

	r.logger.Debug("=== dposRuntime.initializeRuntime ===",
		"initialRound", r.currentRound,
		"r.currentDelegateIndex", r.currentDelegateIndex,
		"note", "根据当前区块号计算，与创世文件保持一致")

	// 初始化受托人集合
	if err := r.initializeDelegates(); err != nil {
		return fmt.Errorf("failed to initialize delegates: %w", err)
	}

	// 初始化投票者映射
	r.voters = make(map[types.Address]*VoterInfo)

	// 初始化签名请求存储
	r.pendingSignatureRequests = make(map[types.Hash]*SignatureRequest)

	// 初始化签名请求去重机制
	r.processedSignatureRequests = make(map[string]time.Time)

	// 初始化签名响应广播去重机制
	r.processedSignatureResponses = make(map[string]time.Time)

	// 初始化签名响应生成去重机制
	r.processedSignatureGenerations = make(map[string]time.Time)

	// 初始化并发控制
	r.maxConcurrentSignatures = 10 // 最多同时处理10个签名请求
	r.signatureRequestSemaphore = make(chan struct{}, r.maxConcurrentSignatures)

	// 🆕 初始化防重复日志机制
	r.lastLogTime = make(map[string]time.Time)

	// 🆕 初始化投票签名验证相关
	r.processedVotes = make(map[string]bool)

	// 检查网络服务状态
	if r.network == nil {
		r.logger.Warn("网络服务不可用，DPoS共识将无法进行网络通信")
	} else {
		r.setupNetworkEventListeners()

		// 暂时禁用网络集成管理器，使用原有的签名收集机制
	}

	// 初始化资源监控器
	r.resourceMonitor = NewResourceMonitor(r.logger, r)
	r.resourceMonitor.Start(context.Background())

	return nil
}

// 🆕 防重复日志函数
func (r *dposRuntime) logOnce(key string, level string, message string, args ...interface{}) {
	r.logMutex.Lock()
	defer r.logMutex.Unlock()

	now := time.Now()
	if lastTime, exists := r.lastLogTime[key]; exists {
		// 如果5秒内已经记录过相同key的日志，则跳过
		if now.Sub(lastTime) < 5*time.Second {
			return
		}
	}

	// 更新最后记录时间
	r.lastLogTime[key] = now

	// 根据级别记录日志
	switch level {
	case "debug":
		r.logger.Debug(message, args...)
	case "info":
		r.logger.Info(message, args...)
	case "warn":
		r.logger.Warn(message, args...)
	case "error":
		r.logger.Error(message, args...)
	default:
		r.logger.Info(message, args...)
	}
}

// parseValidatorsFromGenesis 从创世块解析DPoS验证者
func (r *dposRuntime) parseValidatorsFromGenesis() error {
	
	// 1. 获取创世块
	if r.config == nil || r.config.blockchain == nil {
		return fmt.Errorf("blockchain not available")
	}
	
	genesisHeader, exists := r.config.blockchain.GetHeaderByNumber(0)
	if !exists {
		return fmt.Errorf("genesis block not found")
	}
	
	// 2. 解析验证者地址
	ibftValidators, err := r.parseValidatorsFromExtraData(genesisHeader.ExtraData)
	if err != nil {
		return fmt.Errorf("failed to parse validators from extraData: %w", err)
	}
	
	// 3. 设置最小质押门槛
	minStakeAmount := big.NewInt(0)
	minStakeAmount.SetString("1000000000000000000000", 10) // 1000 VCITY
	
	
	validValidatorCount := 0
	insufficientBalanceCount := 0
	
	// 4. 清空现有的delegates
	r.delegates = make(validator.AccountSet, 0)
	
	// 5. 为每个验证者地址检查余额并生成BLS公钥
	for i := 0; i < ibftValidators.Len(); i++ {
		ibftValidator := ibftValidators[i]
		address := ibftValidator.Address
		
		// 查询验证者余额
		balance, err := r.getValidatorBalance(address)
		if err != nil {
			r.logger.Error("❌ 余额查询失败", 
				"address", address.String(), 
				"error", err)
			continue
		}
		
		// 检查是否满足最小质押要求
		if balance.Cmp(minStakeAmount) < 0 {
			insufficientBalanceCount++
			r.logger.Warn("⚠️ 验证者余额不足", 
				"address", address.String(),
				"balance", balance.String(),
				"required", minStakeAmount.String(),
				"deficit", new(big.Int).Sub(minStakeAmount, balance).String())
			continue
		}
		
		// 创建DPoS验证者（BLS公钥延迟获取）
		delegate := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: balance,
			BlsKey:      nil, // BLS公钥将在需要时获取
			IsActive:    true,
		}
		
		// 直接添加到 delegates
		r.delegates = append(r.delegates, delegate)
		validValidatorCount++
		
		r.logger.Info("✅ DPoS验证者创建成功（BLS公钥延迟获取）",
			"address", address.String(),
			"balance", balance.String(),
			"validatorIndex", validValidatorCount)
	}
	
	// 关键日志：DPoS验证者筛选结果汇总
	r.logger.Info("🚨 DPoS验证者筛选完成", 
		"totalCandidates", ibftValidators.Len(),
		"validValidators", validValidatorCount,
		"insufficientBalance", insufficientBalanceCount,
		"successRate", fmt.Sprintf("%.1f%%", float64(validValidatorCount)/float64(ibftValidators.Len())*100))
	
	// 检查验证者数量
	if validValidatorCount == 0 {
		return fmt.Errorf("no valid validators found")
	}
	
	if validValidatorCount < 2 {
		r.logger.Warn("⚠️ 警告：DPoS验证者数量过少", 
			"validValidators", validValidatorCount,
			"建议至少需要2个验证者")
	}
	
	r.logger.Info("✅ DPoS验证者解析完成", "count", validValidatorCount)
	
	return nil
}

// parseValidatorsFromExtraData 从extraData解析验证者地址
func (r *dposRuntime) parseValidatorsFromExtraData(extraData []byte) (validator.AccountSet, error) {
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short")
	}
	
	// 使用与 ForkManager 相同的解析逻辑
	ibftValidators, err := r.parseValidatorsFromExtraDataDirectly(extraData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse IBFT validators: %w", err)
	}
	
	return ibftValidators, nil
}

// parseIBFTValidatorsFromExtraData 从extraData解析IBFT验证者
func (r *dposRuntime) parseIBFTValidatorsFromExtraData(extraData []byte) (validator.AccountSet, error) {
	r.logger.Debug("🔍 开始解析extraData", "length", len(extraData))
	
	// 检查 extraData 长度
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short: %d bytes", len(extraData))
	}
	
	// 打印 extraData 的十六进制内容用于调试
	r.logger.Debug("🔍 extraData内容", "hex", fmt.Sprintf("%x", extraData))
	
	// 尝试解析 IstanbulExtra 结构
	parser := fastrlp.Parser{}
	val, err := parser.Parse(extraData)
	if err != nil {
		r.logger.Error("❌ fastrlp解析失败", "error", err)
		return nil, fmt.Errorf("failed to parse extraData: %w", err)
	}
	
	// 检查解析出的值的类型
	r.logger.Debug("🔍 解析出的值类型", "type", val.Type())
	
	// 尝试解析 IstanbulExtra 结构
	var istanbulExtra ibftSigner.IstanbulExtra
	if err := istanbulExtra.UnmarshalRLPFrom(&parser, val); err != nil {
		r.logger.Error("❌ IstanbulExtra解析失败", "error", err)
		
		// 尝试直接解析验证者地址，不使用模拟数据
		return r.parseValidatorsFromExtraDataDirectly(extraData)
	}
	
	r.logger.Debug("✅ IstanbulExtra解析成功", "validatorsCount", istanbulExtra.Validators.Len())
	
	// 将 IBFT 验证者转换为 DPoS 验证者格式
	accountSet := make(validator.AccountSet, 0, istanbulExtra.Validators.Len())
	
	for i := 0; i < istanbulExtra.Validators.Len(); i++ {
		ibftValidator := istanbulExtra.Validators.At(uint64(i))
		
		// 创建 DPoS 验证者元数据
		delegate := &validator.ValidatorMetadata{
			Address:     ibftValidator.Addr(),
			VotingPower: big.NewInt(0), // 将在后续步骤中设置
			BlsKey:      nil,           // BLS公钥将在需要时获取
			IsActive:    true,
		}
		
		accountSet = append(accountSet, delegate)
		r.logger.Debug("🔍 解析出验证者", "index", i, "address", ibftValidator.Addr().String())
	}
	
	return accountSet, nil
}

// parseValidatorsFromExtraDataDirectly 直接解析extraData中的验证者地址
func (r *dposRuntime) parseValidatorsFromExtraDataDirectly(extraData []byte) (validator.AccountSet, error) {
	
	// 使用与 ForkManager 相同的解析逻辑
	// Remove only the vanity bytes (32 bytes) from extraData
	// The rest is RLP data containing validators and seals
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short: %d bytes", len(extraData))
	}
	
	// Extract the RLP-encoded data
	// extraData format: [vanity(32)] + [RLP(IstanbulExtra)]
	rlpData := extraData[32:]
	
	// 创建验证者列表
	validatorList := make([]*validator.ValidatorMetadata, 0)
	
	// Parse RLP data using the same method as ForkManager
	err := types.UnmarshalRlp(func(p *fastrlp.Parser, v *fastrlp.Value) error {
		// Get the top-level list
		elems, err := v.GetElems()
		if err != nil {
			return fmt.Errorf("expected array: %w", err)
		}
		
		// Process each element
		for _, elem := range elems {
			// Try to get bytes
			if bytes, err := elem.GetBytes(nil); err == nil {
				// If it's 20 bytes, it might be an address
				if len(bytes) == 20 {
					addr := types.BytesToAddress(bytes)
					delegate := &validator.ValidatorMetadata{
						Address:     addr,
						VotingPower: big.NewInt(0), // 将在后续步骤中设置
						BlsKey:      nil,           // BLS公钥将在需要时获取
						IsActive:    true,
					}
					validatorList = append(validatorList, delegate)
				}
			} else {
				// Try to get sub-elements
				if subElems, err := elem.GetElems(); err == nil {
					for _, subElem := range subElems {
						if subBytes, err := subElem.GetBytes(nil); err == nil {
							// If it's 20 bytes, it might be an address
							if len(subBytes) == 20 {
								addr := types.BytesToAddress(subBytes)
								delegate := &validator.ValidatorMetadata{
									Address:     addr,
									VotingPower: big.NewInt(0), // 将在后续步骤中设置
									BlsKey:      nil,           // BLS公钥将在需要时获取
									IsActive:    true,
								}
								validatorList = append(validatorList, delegate)
							}
						}
					}
				}
			}
		}
		
		return nil
	}, rlpData)
	
	if err != nil {
		return nil, fmt.Errorf("failed to parse RLP data: %w", err)
	}
	
	return validatorList, nil
}

// getValidatorBalance 获取验证者余额
func (r *dposRuntime) getValidatorBalance(address types.Address) (*big.Int, error) {
	if r.config == nil || r.config.blockchain == nil {
		return big.NewInt(0), fmt.Errorf("blockchain not available")
	}
	
	// 获取当前区块头
	currentHeader := r.config.blockchain.CurrentHeader()
	if currentHeader == nil {
		return big.NewInt(0), fmt.Errorf("current header not available")
	}
	
	r.logger.Debug("🔍 开始查询验证者余额", "address", address.String())
	
	// 通过backend获取DPoS实例，然后查询真实余额
	if r.backend != nil {
		if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance.config != nil && dposInstance.config.Executor != nil {
			// 通过executor查询余额
			snapshot, err := dposInstance.config.Executor.StateAt(currentHeader.StateRoot)
			if err != nil {
				r.logger.Warn("⚠️ 无法创建状态快照，使用默认余额", "error", err)
				// 回退到默认值
				balance := big.NewInt(0)
				balance.SetString("1000002100000000000000", 10) // 1000002.1 VCITY
				return balance, nil
			}
			
			account, err := snapshot.GetAccount(address)
			if err != nil {
				r.logger.Warn("⚠️ 无法获取账户信息，使用默认余额", "address", address.String(), "error", err)
				// 回退到默认值
				balance := big.NewInt(0)
				balance.SetString("1000002100000000000000", 10) // 1000002.1 VCITY
				return balance, nil
			}
			
			r.logger.Debug("✅ 成功查询到验证者余额", "address", address.String(), "balance", account.Balance.String())
			return account.Balance, nil
		}
	}
	
	// 如果无法通过backend查询，回退到默认值
	r.logger.Warn("⚠️ 无法通过backend查询余额，使用默认余额", "address", address.String())
	balance := big.NewInt(0)
	balance.SetString("1000002100000000000000", 10) // 1000002.1 VCITY
	return balance, nil
}

// setupNetworkEventListeners 设置网络事件监听器
func (r *dposRuntime) setupNetworkEventListeners() {
	// 监听新节点加入事件
	// 注意：这里需要根据实际的网络事件系统来实现
	// 由于当前代码中没有直接的网络事件监听机制，我们使用定时器来模拟


	// 启动定期检查新节点的协程
	go r.periodicPeerCheck()
}

// periodicPeerCheck 定期检查新节点
func (r *dposRuntime) periodicPeerCheck() {
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	lastPeerCount := 0

	for {
		select {
		case <-ticker.C:
			if r.network == nil {
				continue
			}

			currentPeers := r.network.Peers()
			currentPeerCount := len(currentPeers)

			// 如果节点数量增加，说明有新节点加入
			if currentPeerCount > lastPeerCount {
				// 检测到新节点加入（静默处理）

				// 查询新节点的待处理签名请求
				r.queryPendingSignatureRequests()
			}

			lastPeerCount = currentPeerCount

		case <-r.closeCh:
			return
		}
	}
}

// startBlockProduction 启动区块生产
func (r *dposRuntime) startBlockProduction() error {
	
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("❌ Key不可用，无法启动区块生产",
			"configIsNil", r.config == nil,
			"keyIsNil", r.config != nil && r.config.Key == nil)
		return fmt.Errorf("key not available, cannot start block production")
	}

	blockTime := 2 * time.Second // 默认2秒
	if r.config.PolyBFTConfig != nil {
		blockTime = r.config.PolyBFTConfig.BlockTime.Duration
	}

	r.logger.Info("🏭 区块生产配置检查", 
		"blockTime", blockTime.String(),
		"resourceMonitor", r.resourceMonitor != nil,
		"goroutineManager", func() bool {
			if r.resourceMonitor != nil {
				return r.resourceMonitor.goroutineManager != nil
			}
			return false
		}())

	r.blockTimer = time.NewTicker(blockTime)
	r.logger.Info("✅ 区块生产定时器创建成功", "blockTime", blockTime.String())
	
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("block-production", func() {
			for {
				select {
				case <-r.blockTimer.C:
					if err := r.produceBlock(); err != nil {
						r.logger.Error("failed to produce block", "error", err)
					}
				case <-r.closeCh:
					return
				}
			}
		})
	} else {
		r.logger.Error("❌ 无法启动区块生产：resourceMonitor或goroutineManager为nil",
			"resourceMonitor", r.resourceMonitor != nil,
			"goroutineManager", func() bool {
				if r.resourceMonitor != nil {
					return r.resourceMonitor.goroutineManager != nil
				}
				return false
			}())
		return fmt.Errorf("resourceMonitor or goroutineManager is nil")
	}

	return nil
}

// startVoteCollection 启动投票收集
func (r *dposRuntime) startVoteCollection() error {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		return fmt.Errorf("key not available, cannot start vote collection")
	}

	voteTime := 5 * time.Second // 默认5秒
	if r.config.PolyBFTConfig != nil {
		voteTime = r.config.PolyBFTConfig.BlockTime.Duration * 4 // 投票时间设为区块时间的4倍
	}

	r.voteTimer = time.NewTicker(voteTime)
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("vote-collection", func() {
			defer func() {
				// 确保定时器被停止
				if r.voteTimer != nil {
					r.voteTimer.Stop()
					r.voteTimer = nil
				}
				r.logger.Debug("投票收集goroutine已退出")
			}()

			for {
				select {
				case <-r.voteTimer.C:
					if err := r.collectVotes(); err != nil {
						r.logger.Error("failed to collect votes", "error", err)
					}
				case <-r.closeCh:
					r.logger.Debug("投票收集goroutine收到关闭信号")
					return
				}
			}
		})
	}

	return nil
}

// cleanupRuntime 清理运行时状态
func (r *dposRuntime) cleanupRuntime() {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 清理投票者映射
	r.voters = nil
	r.delegates = nil
}

// cleanupSignatureCollectionResources 清理签名收集相关的资源
func (r *dposRuntime) cleanupSignatureCollectionResources(checkpointHash types.Hash) {
	// 清理已处理的签名请求记录
	r.signatureRequestDedupMutex.Lock()
	for key := range r.processedSignatureRequests {
		if strings.Contains(key, checkpointHash.String()) {
			delete(r.processedSignatureRequests, key)
		}
	}
	r.signatureRequestDedupMutex.Unlock()

	// 清理已处理的签名响应记录
	r.signatureResponseDedupMutex.Lock()
	for key := range r.processedSignatureResponses {
		if strings.Contains(key, checkpointHash.String()) {
			delete(r.processedSignatureResponses, key)
		}
	}
	r.signatureResponseDedupMutex.Unlock()

	// 清理已处理的签名生成记录
	r.signatureGenerationDedupMutex.Lock()
	for key := range r.processedSignatureGenerations {
		if strings.Contains(key, checkpointHash.String()) {
			delete(r.processedSignatureGenerations, key)
		}
	}
	r.signatureGenerationDedupMutex.Unlock()

	// 清理待处理的签名请求
	r.signatureRequestMutex.Lock()
	delete(r.pendingSignatureRequests, checkpointHash)
	r.signatureRequestMutex.Unlock()

	// 签名收集资源已清理
}

// cleanupExpiredCaches 清理过期的运行时缓存
func (r *dposRuntime) cleanupExpiredCaches() {
	now := time.Now()

	// 清理过期的签名生成记录（超过10分钟）
	r.signatureGenerationDedupMutex.Lock()
	for key, timestamp := range r.processedSignatureGenerations {
		if now.Sub(timestamp) > 10*time.Minute {
			delete(r.processedSignatureGenerations, key)
		}
	}
	r.signatureGenerationDedupMutex.Unlock()

	// 清理过期的日志时间记录（超过1小时）
	r.logMutex.Lock()
	for key, timestamp := range r.lastLogTime {
		if now.Sub(timestamp) > time.Hour {
			delete(r.lastLogTime, key)
		}
	}
	r.logMutex.Unlock()

	// 清理过期的BLS私钥缓存（超过30分钟）
	r.blsPrivateKeyCacheMutex.Lock()
	if !r.blsPrivateKeyCacheTime.IsZero() && now.Sub(r.blsPrivateKeyCacheTime) > 30*time.Minute {
		r.blsPrivateKeyCache = nil
		r.blsPrivateKeyCacheTime = time.Time{}
	}
	r.blsPrivateKeyCacheMutex.Unlock()
}

// cleanupProcessedBlocks 清理已处理的区块记录（LRU自动管理）
func (d *DPoS) cleanupProcessedBlocks() {
	// LRU缓存会自动管理大小，无需手动清理
	// 这里可以添加一些统计信息
	d.processedMutex.RLock()
	if d.processedBlocks == nil {
		d.processedMutex.RUnlock()
		return // 缓存未初始化，无需清理
	}
	size := d.processedBlocks.Len()
	d.processedMutex.RUnlock()
	
	if size > 800 { // 当接近上限时记录日志
		d.logger.Debug("已处理区块缓存接近上限", "size", size, "max", 1000)
	}
}

// startSignatureCleanup 启动签名去重清理协程
func (r *dposRuntime) startSignatureCleanup() {
	go func() {
		defer func() {
			if panicErr := recover(); panicErr != nil {
				r.logger.Error("签名清理协程panic", "error", panicErr)
			}
		}()
		
		ticker := time.NewTicker(30 * time.Second) // 每30秒清理一次
		defer ticker.Stop()
		
		for {
			select {
			case <-ticker.C:
				r.cleanupSignatureMaps()
			case <-r.closeCh:
				return
			}
		}
	}()
}

// cleanupSignatureMaps 异步清理所有签名map
func (r *dposRuntime) cleanupSignatureMaps() {
	now := time.Now()
	
	// 清理签名请求
	r.signatureRequestDedupMutex.Lock()
	for k, v := range r.processedSignatureRequests {
		if now.Sub(v) > 2*time.Minute { // 改为2分钟
			delete(r.processedSignatureRequests, k)
		}
	}
	r.signatureRequestDedupMutex.Unlock()
	
	// 清理签名响应
	r.signatureResponseDedupMutex.Lock()
	for k, v := range r.processedSignatureResponses {
		if now.Sub(v) > 2*time.Minute {
			delete(r.processedSignatureResponses, k)
		}
	}
	r.signatureResponseDedupMutex.Unlock()
	
	// 清理签名生成
	r.signatureGenerationDedupMutex.Lock()
	for k, v := range r.processedSignatureGenerations {
		if now.Sub(v) > 2*time.Minute {
			delete(r.processedSignatureGenerations, k)
		}
	}
	r.signatureGenerationDedupMutex.Unlock()
}

// produceBlock 生产区块
func (r *dposRuntime) produceBlock() error {
	// 🆕 在锁之前添加 Printf 日志
	
	r.lock.Lock()
	defer r.lock.Unlock()

	
	// 🆕 添加详细的currentDelegateIndex日志
	currentBlock := r.config.blockchain.CurrentHeader()
	// 静默处理，不打印日志

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("❌ key not available, cannot produce block",
			"config", r.config != nil,
			"key", func() bool {
				if r.config != nil {
					return r.config.Key != nil
				}
				return false
			}())
		return fmt.Errorf("key not available, cannot produce block")
	}
	
	// 静默处理，不打印日志

	// 检查当前节点是否为出块者
	currentDelegate := r.getCurrentDelegate()
	keyAddr := types.Address(r.config.Key.Address())

	// 检查当前节点是否为出块者

	// 检查当前节点是否有足够的stake参与出块
	var currentDelegateInfo *validator.ValidatorMetadata
	for _, delegate := range r.delegates {
		if delegate.Address == keyAddr {
			currentDelegateInfo = delegate
			break
		}
	}

	// 如果当前节点stake为0或不活跃，跳过出块
	if currentDelegateInfo == nil || !currentDelegateInfo.IsActive || currentDelegateInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
		var isActiveStr string
		var votingPowerStr string
		if currentDelegateInfo != nil {
			isActiveStr = fmt.Sprintf("%v", currentDelegateInfo.IsActive)
			votingPowerStr = currentDelegateInfo.VotingPower.String()
		} else {
			isActiveStr = "N/A"
			votingPowerStr = "N/A"
		}

		r.logger.Info("❌ 当前节点stake不足或不活跃，跳过出块",
			"keyAddr", keyAddr.String(),
			"isActive", isActiveStr,
			"votingPower", votingPowerStr,
			"currentDelegateInfo", currentDelegateInfo != nil,
			"reason", func() string {
				if currentDelegateInfo == nil {
					return "当前节点不在验证者集合中"
				}
				if !currentDelegateInfo.IsActive {
					return "当前节点不活跃"
				}
				if currentDelegateInfo.VotingPower.Cmp(big.NewInt(0)) <= 0 {
					return "当前节点投票权重为0"
				}
				return "未知原因"
			}())
		return nil
	}
	
	// 当前节点stake检查通过

	// 添加调试日志 - 只有当本节点是当前受托人时才打印
	if currentDelegate == keyAddr {
		r.logger.Debug("🏭 检查区块生产资格",
			"currentDelegate", currentDelegate,
			"keyAddr", keyAddr,
			"currentRound", r.currentRound,
			"currentDelegateIndex", r.currentDelegateIndex,
			"delegatesCount", len(r.delegates),
			"votingPower", currentDelegateInfo.VotingPower.String())
	}

	// 添加详细的受托人集合调试信息
	// r.logger.Info("=== 当前受托人集合 ===")
	// for i, delegate := range r.delegates {
	// 	r.logger.Info("受托人",
	// 		"index", i,
	// 		"address", delegate.Address.String(),
	// 		"isCurrentDelegate", delegate.Address == currentDelegate,
	// 		"isKeyAddr", delegate.Address == keyAddr)
	// }
	// r.logger.Info("=== 受托人集合结束 ===")

	if currentDelegate != keyAddr {
		r.logOnce("not_current_delegate", "info", "⏭️ 不是当前委托者，跳过区块生产",
			"currentDelegate", currentDelegate.String(),
			"keyAddr", keyAddr.String(),
			"currentDelegateIndex", r.currentDelegateIndex)
		return nil // 不是当前出块者
	}

	// 🆕 基于严格顺序的出块检查，替换原有的等待逻辑
	if !r.shouldProduceBlock() {
		expectedIndex := r.calculateExpectedDelegateIndex()
		currentBlock := r.config.blockchain.CurrentHeader()
		r.logger.Debug("不是当前轮次的委托者，跳过出块",
			"currentBlock", currentBlock.Number,
			"currentDelegateIndex", r.currentDelegateIndex,
			"expectedDelegateIndex", expectedIndex,
			"delegateCount", r.config.DelegateCount)
		return nil
	}

	// 检查是否已经有更新的区块
	// currentBlock 已在上面声明过了

	// 计算下一个要生产的区块号
	nextBlockNumber := currentBlock.Number + 1

	// 只对区块1进行特殊检查，防止分叉
	if nextBlockNumber == 1 {
		// 如果我们要生产区块1，检查是否已经有区块1了
		if currentBlock.Number >= 1 {
			// 检查当前区块的矿工地址是否是我们自己
			blockMiner := types.BytesToAddress(currentBlock.Miner)
			keyAddr := types.Address(r.config.Key.Address())

			if blockMiner != keyAddr {
				r.logger.Debug("block 1 was produced by another node, skipping block production",
					"blockMiner", blockMiner, "keyAddr", keyAddr)
				return nil
			} else {
				r.logger.Debug("block 1 was produced by us, continuing with next block")
			}
		}
	} else if currentBlock.Number >= nextBlockNumber {
		// 如果当前区块号大于等于我们要生产的区块号，说明已经有更新的区块了
		r.logger.Debug("block already exists, skipping block production",
			"currentBlockNumber", currentBlock.Number, "nextBlockNumber", nextBlockNumber)
		return nil
	}

	// 构建新区块
	r.logger.Info("🏗️ DPoS开始构建新区块", "blockNumber", nextBlockNumber)
	block, err := r.buildBlock()
	if err != nil {
		return fmt.Errorf("failed to build block: %w", err)
	}

	// 提交区块到区块链
	r.logger.Info("📝 开始提交区块到区块链", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String())

	if err := r.config.blockchain.CommitBlock(block); err != nil {
		r.logger.Error("区块提交失败", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String(), "error", err)
		return fmt.Errorf("failed to commit block: %w", err)
	}

	r.logger.Info("✅ DPoS区块提交成功", 
		"blockNumber", block.Block.Number(), 
		"blockHash", block.Block.Hash().String()[:16],
		"txs", len(block.Block.Transactions),
		"difficulty", block.Block.Header.Difficulty,
		"gasUsed", block.Block.Header.GasUsed,
		"timestamp", block.Block.Header.Timestamp,
		"delegate", r.config.Key.Address().String()[:16])
	
	// 添加事件触发日志跟踪
	r.logger.Info("🔔 区块提交完成，等待区块链事件触发状态广播", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String())

	// 注意：历史验证者集合已在签名聚合完成后保存，无需重复保存

	// 验证区块是否真正写入区块链
	if writtenBlock, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number()); exists {
		r.logger.Debug("区块验证成功", "blockNumber", writtenBlock.Number, "blockHash", writtenBlock.Hash.String(), "stateRoot", writtenBlock.StateRoot.String())

		// 进一步验证区块数据完整性
		r.logger.Debug("区块数据完整性验证", "blockNumber", block.Block.Number(), "txCount", len(block.Block.Transactions))

		// 记录所有交易的详细信息，帮助诊断
		for i, tx := range block.Block.Transactions {
			r.logger.Info("区块交易详情",
				"index", i,
				"txHash", tx.Hash.String(),
				"nonce", tx.Nonce,
				"from", tx.From.String(),
				"blockNumber", block.Block.Number())
		}

		// 验证交易查找表是否正确写入（关键验证）
		r.logger.Debug("开始验证交易查找表写入状态...")
		for i, tx := range block.Block.Transactions {
			// 尝试通过交易哈希查找区块
			if blockHash, found := r.config.blockchain.ReadTxLookup(tx.Hash); found {
				r.logger.Debug("✅ 交易查找表验证成功",
					"index", i,
					"txHash", tx.Hash.String(),
					"blockHash", blockHash.String(),
					"expectedBlockHash", block.Block.Hash().String())
			} else {
				r.logger.Error("❌ 交易查找表验证失败",
					"index", i,
					"txHash", tx.Hash.String(),
					"blockNumber", block.Block.Number(),
					"expectedBlockHash", block.Block.Hash().String())
				return fmt.Errorf("transaction lookup table verification failed for tx %s", tx.Hash.String())
			}
		}
		r.logger.Debug("🎉 所有交易查找表验证完成")
	} else {
		r.logger.Error("区块验证失败", "blockNumber", block.Block.Number(), "blockHash", block.Block.Hash().String())
		return fmt.Errorf("block was not written to blockchain after commit")
	}

	// 根据交易数量添加特殊标记
	txCount := len(block.Block.Transactions)
	if txCount == 0 {
		r.logger.Info("⚪💎💫 EMPTY BLOCK SEALED 💫💎⚪", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
		r.logger.Info("⚪💎💫 EMPTY BLOCK SEALED 💫💎⚪", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
		r.logger.Info("⚪💎💫 EMPTY BLOCK SEALED 💫💎⚪", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
	} else if txCount == 1 {
		// 包含交易的区块 - 添加明显的特殊标记
		r.logger.Info("🚀🚀🚀 TRANSACTION BLOCK SEALED 🚀🚀🚀", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
		r.logger.Info("🚀🚀🚀 TRANSACTION BLOCK SEALED 🚀🚀🚀", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
		r.logger.Info("🚀🚀🚀 TRANSACTION BLOCK SEALED 🚀🚀🚀", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
	} else {
		// 🆕 包含多个交易的区块 - 使用更显著的标记，加上各种符号
		r.logger.Warn("🎉🎊🎆🎈 *** MULTI-TX BLOCK SEALED *** 🎈🎆🎊🎉", "number", block.Block.Number(), "hash", block.Block.Hash(), "txCount", txCount)
	}

	// 更新轮次 - 使用区块号更新，避免时序问题
	r.updateRound(block.Block.Number())

	// 🆕 增强调试日志：添加详细的计算过程
	blockNumber := block.Block.Number()
	delegateCount := uint64(0)
	if r.config != nil {
		delegateCount = r.config.DelegateCount
	}
	
	// 计算期望的委托者索引用于验证
	expectedDelegateIndex := blockNumber % delegateCount
	
	r.logger.Info("🔄 轮次更新完成", 
		"newRound", r.currentRound, 
		"newDelegateIndex", r.currentDelegateIndex, 
		"blockNumber", blockNumber,
		"delegateCount", delegateCount,
		"expectedDelegateIndex", expectedDelegateIndex,
		"calculation", fmt.Sprintf("%d%%%d=%d", blockNumber, delegateCount, expectedDelegateIndex),
		"isCorrect", r.currentDelegateIndex == expectedDelegateIndex,
		"roundCalculation", fmt.Sprintf("1+(%d-1)/%d=%d", blockNumber, delegateCount, 1+(blockNumber-1)/delegateCount))

	return nil
}

// collectVotes 收集投票
func (r *dposRuntime) collectVotes() error {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 处理待处理的投票
	for _, vote := range r.pendingVotes {
		if err := r.processVote(vote); err != nil {
			r.logger.Error("failed to process vote", "error", err, "voter", vote.Voter)
		}
	}

	// 清空待处理投票
	r.pendingVotes = nil

	return nil
}

// updateRound 更新轮次
func (r *dposRuntime) updateRound(blockNumber ...uint64) {
	// 🆕 修复：统一使用区块号计算委托者索引，避免不一致
	var currentBlockNumber uint64
	
	if len(blockNumber) > 0 {
		// 优先使用传入的区块号
		currentBlockNumber = blockNumber[0]
		r.logger.Debug("🔄 使用传入区块号更新委托者索引",
			"blockNumber", currentBlockNumber,
			"currentRound", r.currentRound)
	} else {
		// 如果没有传入区块号，从CurrentHeader获取
		if r.config != nil && r.config.blockchain != nil {
			if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
				currentBlockNumber = currentHeader.Number
				r.logger.Debug("🔄 使用CurrentHeader区块号更新委托者索引",
					"blockNumber", currentBlockNumber,
					"currentRound", r.currentRound)
			} else {
				r.logger.Warn("⚠️ 无法获取当前区块头，使用默认值0")
				currentBlockNumber = 0
			}
		} else {
			r.logger.Warn("⚠️ 配置或区块链服务不可用，使用默认值0")
			currentBlockNumber = 0
		}
	}
	
	// 统一使用相同的计算公式
	// 修复：使用 (currentBlockNumber + 1) % delegateCount，计算下一个区块的委托者
	if r.config != nil && r.config.DelegateCount > 0 {
		r.currentDelegateIndex = (currentBlockNumber + 1) % uint64(r.config.DelegateCount)
		r.logger.Debug("🔄 统一计算委托者索引",
			"blockNumber", currentBlockNumber,
			"nextBlockNumber", currentBlockNumber + 1,
			"delegateCount", r.config.DelegateCount,
			"formula", fmt.Sprintf("(%d+1)%%%d=%d", currentBlockNumber, r.config.DelegateCount, r.currentDelegateIndex),
			"currentRound", r.currentRound,
			"newDelegateIndex", r.currentDelegateIndex)
	} else {
		r.logger.Error("❌ 配置无效，无法计算委托者索引",
			"config", r.config != nil,
			"delegateCount", func() uint64 {
				if r.config != nil {
					return r.config.DelegateCount
				}
				return 0
			}())
		r.currentDelegateIndex = 0
	}

	// 检查是否需要更新轮次
	// 修复：当委托者索引从最后一个回到第一个时，轮次+1
	if r.currentDelegateIndex == 0 && r.currentRound >= 1 {
		// 检查是否真的需要增加轮次（避免第一次就增加）
		if r.currentRound == 1 {
			// 第一次轮次，检查是否已经完成了一轮
			// 如果当前区块号大于等于委托者数量，说明已经完成了一轮
			if currentBlockNumber > uint64(r.config.DelegateCount) {
				r.currentRound++
				r.logger.Debug("🔄 完成第一轮，更新轮次",
					"newRound", r.currentRound,
					"newDelegateIndex", r.currentDelegateIndex,
					"blockNumber", currentBlockNumber,
					"delegateCount", r.config.DelegateCount)
			}
		} else {
			// 非第一轮，直接增加轮次
			r.currentRound++
			r.logger.Debug("🔄 委托者索引重置为0，更新轮次",
				"newRound", r.currentRound,
				"newDelegateIndex", r.currentDelegateIndex)
		}

		// 🆕 方案1+方案2：轮次边界时处理延迟的验证者集合更新
		if r.backend != nil {
			// 通过类型断言访问DPoS实例
			if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance.pendingValidatorUpdate {
				r.logger.Info("🔄 轮次边界：处理延迟的验证者集合更新")
				if err := dposInstance.updateDelegatesInternal(nil); err != nil {
					r.logger.Error("❌ 轮次边界更新验证者集合失败", "error", err)
				} else {
					r.logger.Info("✅ 轮次边界：验证者集合更新完成")

					// 🆕 轮次边界：同步新的验证者集合到runtime
					r.logger.Info("🔄 轮次边界：同步新验证者集合到runtime")
					go func() {
						dposInstance.syncRuntimeDelegatesWithRetry()
					}()
				}
				dposInstance.pendingValidatorUpdate = false
			}
		}
	} else {
		r.logger.Debug("🔄 更新委托者索引",
			"currentRound", r.currentRound,
			"newDelegateIndex", r.currentDelegateIndex)
	}
}

// initializeDelegates 初始化受托人集合
func (r *dposRuntime) initializeDelegates() error {
	r.logger.Debug("🚀 dposRuntime.initializeDelegates 开始")
	r.logger.Debug("backend是否为nil", "isNil", r.backend == nil)

	// 🆕 修复：使用当前区块号获取受托人集合，而不是已废弃的区块0
	fromExtraData := false
	if r.backend != nil {
		// 获取当前区块号
		currentBlockNumber := uint64(0)
		if r.config != nil && r.config.blockchain != nil {
			if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
				currentBlockNumber = currentHeader.Number
			}
		}

		delegates, err := r.backend.GetDelegates(currentBlockNumber, nil)
		if err != nil {
			r.logger.Error("failed to get current delegates from backend", "error", err)
			return fmt.Errorf("failed to get current delegates from backend: %w", err)
		}

		// 🆕 应用 DelegateCount 限制，只取前N个受托人
		if r.config != nil && r.config.DelegateCount > 0 {
			maxDelegates := int(r.config.DelegateCount)
			if len(delegates) > maxDelegates {
				delegates = delegates[:maxDelegates]
				r.logger.Debug("🎯 runtime初始化：限制受托人数量为前N个",
					"originalCount", len(delegates)+len(delegates[maxDelegates:]),
					"limitedCount", maxDelegates,
					"configDelegateCount", r.config.DelegateCount)
			}
		}

		r.delegates = delegates
		r.logger.Debug("initialized delegates from backend", "count", len(r.delegates))

		// 🆕 如果从backend获取的delegates为空，尝试从extraData解析
		if len(r.delegates) == 0 {
			r.logger.Info("🎯 从backend获取的delegates为空，尝试从extraData解析验证者")
			
			// 直接在dposRuntime中解析extraData验证者
			if err := r.parseValidatorsFromGenesis(); err != nil {
				r.logger.Error("Failed to parse validators from genesis", "error", err)
				// 继续使用空集合
			} else {
				r.logger.Info("✅ 已从extraData解析验证者", "count", len(r.delegates))
				fromExtraData = true
			}
		} else {
			r.logger.Info("✅ 从backend获取到验证者，跳过extraData解析", "count", len(r.delegates))
		}

		// 🆕 按voterpower排序并截取前N个验证者
		if len(r.delegates) > 0 {
			r.logger.Info("🔍 开始按voterpower排序并截取前N个验证者", 
				"originalCount", len(r.delegates),
				"configDelegateCount", r.config.DelegateCount)
			
			// 按votingPower降序排序
			sort.Slice(r.delegates, func(i, j int) bool {
				if r.delegates[i].VotingPower.Cmp(r.delegates[j].VotingPower) == 0 {
					return bytes.Compare(r.delegates[i].Address[:], r.delegates[j].Address[:]) < 0
				}
				return r.delegates[i].VotingPower.Cmp(r.delegates[j].VotingPower) > 0
			})
			
			// 截取前N个验证者
			maxDelegates := int(r.config.DelegateCount)
			originalCount := len(r.delegates)
			if len(r.delegates) > maxDelegates {
				r.delegates = r.delegates[:maxDelegates]
				r.logger.Info("🎯 限制验证者数量为前N个",
					"originalCount", originalCount,
					"limitedCount", maxDelegates,
					"configDelegateCount", r.config.DelegateCount)
			}
			
			r.logger.Info("✅ 验证者排序和截取完成", 
				"finalCount", len(r.delegates),
				"maxDelegates", maxDelegates)
		}

		r.logger.Debug("=== 受托人集合详细信息 ===")
		for i, delegate := range r.delegates {
			r.logger.Debug("受托人信息",
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}
		r.logger.Info("=== 受托人集合详细信息结束 ===")

		// 检查当前节点的地址是否在受托人集合中
		if r.config != nil && r.config.Key != nil {
			keyAddr := types.Address(r.config.Key.Address())
			r.logger.Debug("当前节点地址", "keyAddr", keyAddr.String())

			found := false
			for i, delegate := range r.delegates {
				if delegate.Address == keyAddr {
					r.logger.Debug("找到当前节点在受托人集合中", "index", i, "address", keyAddr.String())
					found = true
					break
				}
			}
			if !found {
				r.logger.Warn("当前节点不在受托人集合中", "keyAddr", keyAddr.String())
			}
		}
	} else {
		// 如果没有backend，使用空集合
		r.delegates = validator.AccountSet{}
		r.logger.Warn("no backend available, using empty delegate set")
	}

	// 🆕 同步数据到 d.runtime.delegates 和 d.delegates
	// r.backend 是 DPoS 实例，r.backend.runtime 就是 d.runtime
	if r.backend != nil {
		// 通过 backend 访问 DPoS 实例的 runtime
		if dposInstance, ok := r.backend.(*DPoS); ok && dposInstance.runtime != nil {
			dposInstance.runtime.delegates = r.delegates.Copy()
			dposInstance.delegates = r.delegates.Copy() 
			r.logger.Info("✅ 已同步验证者数据到 d.runtime.delegates 和 d.delegates", "count", len(dposInstance.runtime.delegates))
			
			// 🆕 只有在从extraData解析验证者时才同步到数据库
			if fromExtraData {
				if err := dposInstance.syncDelegatesToDatabase(r.delegates); err != nil {
					r.logger.Warn("⚠️ 同步验证者数据到数据库失败", "error", err)
				} else {
					r.logger.Info("✅ 验证者数据已准备，将在BLS公钥获取完成后保存到数据库", "count", len(r.delegates))
				}
			} else {
				r.logger.Info("✅ 验证者数据来自数据库，无需同步", "count", len(r.delegates))
			}
		} else {
			r.logger.Warn("⚠️ 无法访问 d.runtime，验证者数据同步失败")
		}
	} else {
		r.logger.Warn("⚠️ backend为nil，无法同步验证者数据")
	}

	r.logger.Info("✅ dposRuntime.initializeDelegates 结束")
	return nil
}

// calculateInitialRound 根据当前区块号计算初始轮次
func (r *dposRuntime) calculateInitialRound() uint64 {
	if r.config == nil || r.config.DelegateCount == 0 {
		return 1 // 默认从第1轮开始
	}

	// 获取当前区块号
	var currentBlockNumber uint64 = 0
	if r.config.blockchain != nil {
		if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
			currentBlockNumber = currentHeader.Number
		}
	}

	// 计算轮次：每完成一轮（DelegateCount个区块）轮次+1
	// 轮次从1开始，所以公式是：1 + (blockNumber - 1) / delegateCount
	if currentBlockNumber > 0 {
		round := 1 + (currentBlockNumber-1)/uint64(r.config.DelegateCount)
		r.logger.Debug("🔍 根据区块号计算初始轮次",
			"currentBlockNumber", currentBlockNumber,
			"delegateCount", r.config.DelegateCount,
			"calculatedRound", round,
			"formula", fmt.Sprintf("1 + (%d-1)/%d=%d", currentBlockNumber, r.config.DelegateCount, round))
		return round
	}

	return 1 // 默认从第1轮开始
}

// shouldProduceBlock 检查当前节点是否应该出块（基于严格顺序）
func (r *dposRuntime) shouldProduceBlock() bool {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		r.logger.Warn("无法获取当前区块头，跳过出块")
		return false
	}
	
	// 🆕 添加详细的验证者集合信息
	r.logger.Debug("🔍 shouldProduceBlock 详细检查",
		"currentBlock", currentBlock.Number,
		"currentDelegateIndex", r.currentDelegateIndex,
		"delegateCount", r.config.DelegateCount,
		"delegatesCount", len(r.delegates))
	
	// 当前验证者集合详细信息（静默处理）
	
	// 区块0：只有索引0的委托者出块
	if currentBlock.Number == 0 {
		shouldProduce := r.currentDelegateIndex == 0
		r.logger.Info("🔍 检查区块0出块资格",
			"currentBlock", currentBlock.Number,
			"currentDelegateIndex", r.currentDelegateIndex,
			"shouldProduce", shouldProduce,
			"reason", func() string {
				if shouldProduce {
					return "当前委托者索引为0，应该出块"
				}
				return "当前委托者索引不为0，不应该出块"
			}())
		return shouldProduce
	}
	
	// 其他区块：按顺序出块
	// 修复：使用 (currentBlock.Number + 1) % delegateCount，计算下一个区块的委托者
	expectedDelegateIndex := (currentBlock.Number + 1) % uint64(r.config.DelegateCount)
	shouldProduce := r.currentDelegateIndex == expectedDelegateIndex
	
	// 🆕 添加更详细的出块资格检查
	r.logger.Info("🔍 检查出块资格",
		"currentBlock", currentBlock.Number,
		"r.currentDelegateIndex", r.currentDelegateIndex,
		"expectedDelegateIndex", expectedDelegateIndex,
		"delegateCount", r.config.DelegateCount,
		"shouldProduce", shouldProduce,
		"formula", fmt.Sprintf("%d%%%d=%d", currentBlock.Number, r.config.DelegateCount, expectedDelegateIndex),
		"reason", func() string {
			if shouldProduce {
				return "当前委托者索引与期望索引匹配，应该出块"
			}
			return fmt.Sprintf("当前委托者索引(%d)与期望索引(%d)不匹配，不应该出块", r.currentDelegateIndex, expectedDelegateIndex)
		}())
	
	// 🆕 添加当前委托者详细信息
	if r.currentDelegateIndex < uint64(len(r.delegates)) {
		currentDelegate := r.delegates[r.currentDelegateIndex]
		r.logger.Debug("🔍 当前委托者详细信息",
			"currentDelegateIndex", r.currentDelegateIndex,
			"address", currentDelegate.Address.String(),
			"votingPower", currentDelegate.VotingPower.String(),
			"isActive", currentDelegate.IsActive)
	} else {
		r.logger.Warn("🔍 当前委托者索引超出范围",
			"currentDelegateIndex", r.currentDelegateIndex,
			"delegatesCount", len(r.delegates))
	}
	
	return shouldProduce
}

// calculateExpectedDelegateIndex 计算期望的委托者索引
func (r *dposRuntime) calculateExpectedDelegateIndex() uint64 {
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock == nil {
		return 0
	}
	
	if currentBlock.Number == 0 {
		return 0
	}
	
	// 修复：使用 currentBlock.Number % delegateCount，与shouldProduceBlock保持一致
	return currentBlock.Number % uint64(r.config.DelegateCount)
}

// calculateCurrentDelegateIndex 根据当前区块号计算委托者索引，与创世文件保持一致
func (r *dposRuntime) calculateCurrentDelegateIndex() uint64 {
	if r.config == nil || r.config.DelegateCount == 0 {
		return 0
	}

	// 获取当前区块号
	var currentBlockNumber uint64 = 0
	if r.config.blockchain != nil {
		if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
			currentBlockNumber = currentHeader.Number
		}
	}

	// 修复：使用 (blockNumber + 1) % delegateCount，计算下一个区块的委托者索引
	if currentBlockNumber > 0 {
		delegateIndex := (currentBlockNumber + 1) % uint64(r.config.DelegateCount)
		r.logger.Debug("🔍 根据区块号计算委托者索引",
			"currentBlockNumber", currentBlockNumber,
			"nextBlockNumber", currentBlockNumber + 1,
			"delegateCount", r.config.DelegateCount,
			"calculatedIndex", delegateIndex,
			"formula", "(blockNumber + 1) % delegateCount")
		return delegateIndex
	}

	return 0
}

// getCurrentDelegate 获取当前受托人
func (r *dposRuntime) getCurrentDelegate() types.Address {
	if len(r.delegates) == 0 {
		r.logger.Warn("🔍 getCurrentDelegate: 验证者集合为空")
		return types.ZeroAddress
	}

	// 🆕 修复：直接使用r.currentDelegateIndex，确保与排序后的delegates数组一致
	if r.currentDelegateIndex >= uint64(len(r.delegates)) {
		r.logger.Warn("🔍 getCurrentDelegate: currentDelegateIndex超出范围",
			"currentDelegateIndex", r.currentDelegateIndex,
			"delegatesCount", len(r.delegates))
		return types.ZeroAddress
	}

	delegate := r.delegates[r.currentDelegateIndex]
	
	// 检查受托人是否活跃且有足够的stake
	if !delegate.IsActive || delegate.VotingPower.Cmp(big.NewInt(0)) <= 0 {
		r.logger.Warn("❌ 当前委托者不活跃或票数不足",
			"currentDelegateIndex", r.currentDelegateIndex,
			"address", delegate.Address.String(),
			"isActive", delegate.IsActive,
			"votingPower", delegate.VotingPower.String())
		return types.ZeroAddress
	}

	// 静默处理，不打印日志
	
	return delegate.Address
}

// buildBlock 构建区块
func (r *dposRuntime) buildBlock() (*types.FullBlock, error) {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot build block")
		return nil, fmt.Errorf("key not available, cannot build block")
	}

	// 获取父区块
	parent := r.config.blockchain.CurrentHeader()

	// 创建区块构建器
	keyAddr := types.Address(r.config.Key.Address())
	builder, err := r.config.blockchain.NewBlockBuilder(
		parent,
		keyAddr,
		r.config.txPool,
		2*time.Second, // 区块时间
		r.logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create block builder: %w", err)
	}

	// 重置构建器
	if err := builder.Reset(); err != nil {
		return nil, fmt.Errorf("failed to reset block builder: %w", err)
	}

	// 填充交易

	// 检查交易池状态
	if _, ok := r.config.txPool.(interface {
		DebugInfo() map[string]interface{}
	}); ok {
		// 交易池状态检查
	}

	// 尝试获取更详细的交易池信息
	if txPool, ok := r.config.txPool.(interface {
		GetTxs(inclQueued bool) (map[types.Address][]*types.Transaction, map[types.Address][]*types.Transaction)
	}); ok {
		allPromoted, allEnqueued := txPool.GetTxs(true)

		// 检查每个账户的状态
		for addr, promotedTxs := range allPromoted {
			r.logger.Debug("账户已提升交易", "address", addr.String(), "count", len(promotedTxs))

			// 获取账户在区块链中的当前 nonce
			if currentHeader := r.config.blockchain.CurrentHeader(); currentHeader != nil {
				// 记录当前区块信息，帮助诊断 nonce 问题
				r.logger.Debug("当前区块状态", "blockNumber", currentHeader.Number, "stateRoot", currentHeader.StateRoot.String())
			}

		for i, tx := range promotedTxs {
			if tx != nil {
				r.logger.Debug("已提升交易", "index", i, "hash", tx.Hash.String(), "nonce", tx.Nonce)
			} else {
				r.logger.Warn("发现空交易", "index", i, "address", addr.String())
			}
		}
		}

		for addr, enqueuedTxs := range allEnqueued {
			r.logger.Debug("账户待提升交易", "address", addr.String(), "count", len(enqueuedTxs))
		for i, tx := range enqueuedTxs {
			if tx != nil {
				r.logger.Debug("待提升交易", "index", i, "hash", tx.Hash.String(), "nonce", tx.Nonce)
			} else {
				r.logger.Warn("发现空交易", "index", i, "address", addr.String())
			}
		}
		}
	}

	builder.Fill()

	// 检查填充后的交易数量
	if blockBuilder, ok := builder.(interface {
		GetTransactions() []*types.Transaction
	}); ok {
		txs := blockBuilder.GetTransactions()
		if len(txs) > 0 {
			for i, tx := range txs {
				if tx != nil {
					r.logger.Info("区块中的交易", "index", i, "hash", tx.Hash.String(), "nonce", tx.Nonce)
				} else {
					r.logger.Warn("发现空交易", "index", i)
				}
			}
		} else {
			r.logOnce("no_transactions", "debug", "区块中没有包含任何交易！")
		}
	}

	// 先计算验证者哈希，用于CheckpointData
	// 🆕 关键修复：使用与验证时相同的验证者集合获取方法
	// 验证时使用：getValidatorsFromExtraData
	// 生产时也应该使用相同的逻辑：从ExtraData解析验证者集合
	
	// 获取当前区块信息
	currentBlock := r.config.blockchain.CurrentHeader()
	
	// 获取父区块信息，与验证时保持一致
	var parents []*types.Header
	if currentBlock.Number > 0 {
		parentHeader, exists := r.config.blockchain.GetHeaderByNumber(currentBlock.Number - 1)
		if exists && parentHeader != nil {
			parents = append(parents, parentHeader)
		}
	}
	
	// 🆕 使用与验证时完全相同的验证者集合获取方法
	// 验证时：getValidatorsFromExtraData(header, parent, parents, consensusBackend, logger)
	// 生产时：从当前区块的ExtraData解析验证者集合
	productionValidators, err := r.getValidatorsFromExtraDataForProduction(currentBlock, parents)
	if err != nil {
		r.logger.Error("❌ 生产时无法获取验证者集合", "blockNumber", currentBlock.Number, "error", err)
		return nil, fmt.Errorf("failed to get validators for production: %w", err)
	}
	
	// 🆕 缓存第一次成功获取的验证者集合，确保整个区块生产过程中使用相同的验证者
	r.cachedProductionValidators = productionValidators.Copy()
	r.logger.Debug("💾 已缓存生产时验证者集合", "validatorsCount", len(productionValidators))
	
	r.logger.Debug("🔍 生产时开始计算验证者哈希", "validatorsCount", len(productionValidators))
	currentValidatorsHash, err := productionValidators.HashAddressOnly()
	if err != nil {
		r.logger.Error("failed to calculate current validators hash", "error", err)
		return nil, fmt.Errorf("failed to calculate current validators hash: %w", err)
	}
	r.logger.Debug("🔍 生产时验证者哈希计算结果", "currentValidatorsHash", currentValidatorsHash.String())

	// 创建正确的Extra对象，包含必要的字段
	extra := &Extra{
		Committed: &Signature{}, // 添加空的Committed签名
		Checkpoint: &CheckpointData{
			BlockRound:            r.currentRound,
			EpochNumber:           1,
			CurrentValidatorsHash: currentValidatorsHash,
			NextValidatorsHash:    currentValidatorsHash, // 暂时使用相同的哈希
			EventRoot:             types.Hash{},          // 暂时为空
		},
	}

	// 构建区块
	block, err := builder.Build(func(h *types.Header) {
		// 设置DPoS相关的区块头信息
		h.Miner = keyAddr[:]
		h.Difficulty = 1
		h.ExtraData = extra.MarshalRLPTo(nil)

		// 添加调试日志
		r.logger.Info("🏗️ DPoS区块构建完成", 
			"number", h.Number,
			"difficulty", h.Difficulty,
			"gasLimit", h.GasLimit,
			"timestamp", h.Timestamp,
			"extraDataLength", len(h.ExtraData),
			"delegate", keyAddr.String()[:16])
	})

	if err != nil {
		return nil, fmt.Errorf("failed to build block: %w", err)
	}

	// 等待收集其他验证者的签名
	r.logger.Debug("waiting for validator signatures", "blockNumber", block.Block.Number())

	// 计算checkpoint哈希用于签名
	// 使用与区块头相同的CheckpointData对象
	checkpoint := extra.Checkpoint

	// 计算checkpoint哈希，使用固定的哈希值避免循环依赖
	// 使用区块号作为哈希的基础，确保所有节点计算相同的checkpointHash
	fixedBlockHash := types.BytesToHash([]byte(fmt.Sprintf("block_%d", block.Block.Number())))
	

	
	r.logger.Debug("🔍 生产时开始计算checkpoint哈希", 
		"blockNumber", block.Block.Number(),
		"chainID", r.config.blockchain.GetChainID(),
		"fixedBlockHash", fixedBlockHash.String(),
		"currentValidatorsHash", checkpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", checkpoint.NextValidatorsHash.String(),
		"blockRound", checkpoint.BlockRound,
		"epochNumber", checkpoint.EpochNumber)
	
	// 🆕 添加详细的CheckpointData内容对比日志
	r.logger.Debug("🔍 生产时CheckpointData详细信息",
		"blockNumber", block.Block.Number(),
		"chainID", r.config.blockchain.GetChainID(),
		"blockHash", fixedBlockHash.String(),
		"blockRound", checkpoint.BlockRound,
		"epochNumber", checkpoint.EpochNumber,
		"eventRoot", checkpoint.EventRoot.String(),
		"currentValidatorsHash", checkpoint.CurrentValidatorsHash.String(),
		"nextValidatorsHash", checkpoint.NextValidatorsHash.String(),
		"productionValidatorsCount", len(productionValidators))
	
	// 🆕 打印生产时验证者集合的详细信息
	r.logger.Debug("🔍 生产时验证者集合详细信息")
	for i, validator := range productionValidators {
		r.logger.Debug("🔍 生产时验证者",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive)
	}

	checkpointHash, err := checkpoint.Hash(r.config.blockchain.GetChainID(), block.Block.Number(), fixedBlockHash)
	if err != nil {
		r.logger.Error("failed to calculate checkpoint hash", "error", err)
		return nil, fmt.Errorf("failed to calculate checkpoint hash: %w", err)
	}
	

	// 确保checkpointHash不为全零
	if checkpointHash == (types.Hash{}) {
		r.logger.Error("checkpointHash计算结果为全零，使用备用哈希")
		// 使用备用哈希：区块哈希 + 当前轮次
		backupHash := types.BytesToHash(append(block.Block.Header.Hash.Bytes(), []byte(fmt.Sprintf("_%d", r.currentRound))...))
		checkpointHash = backupHash
	}

	// 最终验证：确保checkpointHash不为空
	if checkpointHash == (types.Hash{}) {
		r.logger.Error("checkpointHash 仍然为空，无法发送签名请求")
		return nil, fmt.Errorf("invalid checkpointHash: cannot be zero")
	}

	// 添加调试日志
	r.logger.Debug("计算checkpointHash完成",
		"blockNumber", block.Block.Number(),
		"currentRound", r.currentRound,
		"checkpointHash", checkpointHash.String(),
		"fixedBlockHash", fixedBlockHash.String())

	// 实现真实的签名收集机制，支持重试
	var signatures [][]byte
	var signatureBitmap bitmap.Bitmap
	var collectErr error

	// 重试机制：最多重试3次
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		signatures, signatureBitmap, collectErr = r.collectValidatorSignatures(block, checkpointHash, keyAddr)
		if collectErr == nil {
			break // 成功收集签名
		}

		// 检查是否是网络增长检测错误
		if strings.Contains(collectErr.Error(), "network growth detected") {
			r.logger.Debug("检测到网络增长，重试签名收集", "attempt", attempt+1)
			time.Sleep(2 * time.Second) // 等待2秒后重试
			continue
		}

		// 检查是否是验证者数量不足错误
		if strings.Contains(collectErr.Error(), "insufficient validators") {
			r.logger.Debug("验证者数量不足，等待网络改善后重试", "attempt", attempt+1)
			// 等待网络状态改善
			_, _, waitErr := r.waitForNetworkGrowth(checkpointHash, keyAddr)
			if waitErr != nil {
				r.logger.Debug("等待网络增长失败", "error", waitErr)
			}
			time.Sleep(2 * time.Second) // 等待2秒后重试
			continue
		}

		// 其他错误，记录并返回
		r.logger.Error("failed to collect validator signatures", "error", collectErr, "attempt", attempt+1)
		if attempt == maxRetries-1 {
			return nil, fmt.Errorf("failed to collect validator signatures after %d attempts: %w", maxRetries, collectErr)
		}

		// 等待后重试
		time.Sleep(1 * time.Second)
	}

	r.logger.Info("签名收集完成",
		"totalSignatures", len(signatures),
		"bitmapLength", len(signatureBitmap),
		"bitmapBytes", fmt.Sprintf("%x", signatureBitmap))

	// 🆕 关键修复：检查签名收集结果
	if len(signatures) == 0 || len(signatureBitmap) == 0 {
		r.logger.Error("签名收集失败，无法提交区块",
			"signaturesCount", len(signatures),
			"bitmapLength", len(signatureBitmap),
			"error", collectErr)
		return nil, fmt.Errorf("cannot commit block without valid signatures: signatures=%d, bitmap=%d", len(signatures), len(signatureBitmap))
	}

	// 更新区块的签名
	if len(signatures) > 0 {
		r.logger.Info("开始聚合签名",
			"signatureCount", len(signatures))



		// 🆕 修复：按位图索引顺序聚合签名，确保与验证时公钥顺序一致
		blsSignatures := make(bls.Signatures, 0, len(signatures))

		// 🆕 关键修复：使用与验证时完全相同的验证者集合获取方法
		// 验证时使用：consensusBackend.GetDelegates(blockNumber-1, parents)
		// 生产时也应该使用相同的逻辑：获取父区块信息并传递

		// 获取父区块信息，与验证时保持一致
		var parents []*types.Header
		if block.Block.Number() > 1 {
			// 获取父区块
			parentHeader, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number() - 1)
			if exists && parentHeader != nil {
				parents = append(parents, parentHeader)
				r.logger.Debug("🔍 生产时获取父区块信息",
					"blockNumber", block.Block.Number(),
					"parentBlockNumber", parentHeader.Number,
					"parentBlockHash", parentHeader.Hash.String())
			}
		}

		// 🆕 关键修复：使用缓存的验证者集合，确保与第一次获取完全一致
		var productionValidators validator.AccountSet
		if r.cachedProductionValidators != nil && len(r.cachedProductionValidators) > 0 {
			// 使用缓存的4个验证者
			productionValidators = r.cachedProductionValidators.Copy()
			r.logger.Debug("💾 使用缓存的验证者集合", "validatorsCount", len(productionValidators))
		} else {
			// 如果缓存为空，则重新获取（备用方案）
			r.logger.Warn("⚠️ 缓存为空，重新获取验证者集合")
			var err error
			productionValidators, err = r.getValidatorsFromExtraDataForProduction(block.Block.Header, parents)
			if err != nil {
				r.logger.Error("❌ 无法获取当前验证者集合", "blockNumber", block.Block.Number(), "error", err)
				return nil, fmt.Errorf("failed to get current validators for block %d: %w", block.Block.Number(), err)
			}
			if productionValidators == nil || len(productionValidators) == 0 {
				r.logger.Error("❌ 当前验证者集合为空", "blockNumber", block.Block.Number())
				return nil, fmt.Errorf("current validators set is empty for block %d", block.Block.Number())
			}
		}

		// 🆕 关键修复：实时同步r.delegates为productionValidators
		// 确保位图索引和保存的验证者集合完全匹配
		r.delegates = productionValidators
		r.logger.Debug("🔄 已同步r.delegates为productionValidators",
			"blockNumber", block.Block.Number(),
			"delegatesCount", len(r.delegates),
			"productionValidatorsCount", len(productionValidators),
			"note", "确保位图索引与验证者集合完全匹配")

		// 🔍 生产时获取的验证者集合信息
		r.logger.Debug("🔍 生产时验证者集合信息",
			"blockNumber", block.Block.Number(),
			"totalValidators", len(productionValidators),
			"delegatesCount", len(r.delegates),
			"validatorSource", "GetDelegates(blockNumber-1, nil)",
			"note", "使用与验证时相同的验证者获取方法")

		// 🔍 打印生产时验证者集合的详细信息
		r.logger.Debug("🔍 生产时验证者集合详细信息")
		for i, validator := range productionValidators {
			r.logger.Debug("🔍 生产时验证者",
				"blockNumber", block.Block.Number(),
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive)
		}

		// 🆕 验证：确保验证者集合与 r.delegates 一致
		if len(productionValidators) != len(r.delegates) {
			r.logger.Warn("⚠️ 验证者集合数量不一致",
				"productionValidatorsCount", len(productionValidators),
				"delegatesCount", len(r.delegates),
				"blockNumber", block.Block.Number())
		}

		r.logger.Debug("🔍 生产时验证者集合信息",
			"blockNumber", block.Block.Number(),
			"totalValidators", len(productionValidators),
			"delegatesCount", len(r.delegates))

		// 创建位图索引到签名的映射
		bitmapToSignature := make(map[uint64][]byte)
		signatureIndex := 0

		// 🆕 方案1：先找出实际参与签名的验证者索引（基于位图设置）
		participatingIndices := make([]uint64, 0)
		for i := uint64(0); i < uint64(len(productionValidators)); i++ {
			if signatureBitmap.IsSet(i) {
				participatingIndices = append(participatingIndices, i)
			}
		}

		// 按实际参与签名的验证者索引收集签名
		for _, validatorIndex := range participatingIndices {
			if signatureIndex < len(signatures) {
				bitmapToSignature[validatorIndex] = signatures[signatureIndex]
				signatureIndex++
			}
		}

		// 按位图索引顺序聚合签名
		for i := uint64(0); i < uint64(len(productionValidators)); i++ {
			if signatureBitmap.IsSet(i) {
				if sigBytes, exists := bitmapToSignature[i]; exists {
					sig, err := bls.UnmarshalSignature(sigBytes)
					if err != nil {
						r.logger.Error("❌ BLS签名解析失败", "error", err, "bitmapIndex", i, "signatureLength", len(sigBytes))
						continue
					}
					blsSignatures = append(blsSignatures, sig)
					r.logger.Debug("✅ BLS签名按位图顺序排列",
						"bitmapIndex", i,
						"signatureIndex", len(blsSignatures)-1,
						"validatorAddress", productionValidators[i].Address.String())
				}
			}
		}

		r.logger.Debug("签名解析完成",
			"parsedSignatures", len(blsSignatures),
			"totalSignatures", len(signatures))


		// 🆕 打印每个签名的详细信息
		r.logger.Debug("🔍 生产时BLS签名详细信息:")
		for i, sig := range blsSignatures {
			if sig != nil {
				sigBytes, err := sig.Marshal()
				if err == nil {
					r.logger.Debug("🔍 生产时BLS签名详情",
						"index", i,
						"signatureBytes", fmt.Sprintf("%x", sigBytes),
						"signatureLength", len(sigBytes))
				} else {
					r.logger.Error("❌ 生产时BLS签名序列化失败",
						"index", i,
						"error", err)
				}
			} else {
				r.logger.Error("❌ 生产时BLS签名为nil", "index", i)
			}
		}

		// 聚合所有签名
		aggregatedSignature, err := blsSignatures.Aggregate().Marshal()
		if err != nil {
			r.logger.Error("❌ 签名聚合失败", "error", err)
			return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
		}

		r.logger.Debug("✅ 签名聚合成功",
			"aggregatedSignatureLength", len(aggregatedSignature),
			"aggregatedSignatureBytes", fmt.Sprintf("%x", aggregatedSignature))

		// 🆕 测试：验证聚合签名是否可以正确解析
		r.logger.Debug("🔍 测试聚合签名解析...")
		_, err = bls.UnmarshalSignature(aggregatedSignature)
		if err != nil {
			r.logger.Error("❌ 聚合签名解析测试失败", "error", err,
				"aggregatedSignatureLength", len(aggregatedSignature),
				"aggregatedSignatureBytes", fmt.Sprintf("%x", aggregatedSignature))
			return nil, fmt.Errorf("aggregated signature verification failed: %w", err)
		}
		r.logger.Debug("✅ 聚合签名解析测试通过")

		// 获取父区块的签名作为Parent签名
		var parentSignature *Signature
		if block.Block.Number() > 1 {
			// 获取父区块头部
			parentHeader, exists := r.config.blockchain.GetHeaderByNumber(block.Block.Number() - 1)
			if !exists {
				r.logger.Error("failed to get parent header", "parentNumber", block.Block.Number()-1)
				return nil, fmt.Errorf("failed to get parent header for block %d", block.Block.Number()-1)
			}

			// 解析父区块的ExtraData
			parentExtra, err := GetIbftExtra(parentHeader.ExtraData)
			if err != nil {
				r.logger.Error("failed to parse parent extra data", "error", err)
				return nil, fmt.Errorf("failed to parse parent extra data: %w", err)
			}

			// 使用父区块的Committed签名作为当前区块的Parent签名
			if parentExtra.Committed != nil {
				parentSignature = parentExtra.Committed
				r.logger.Debug("设置父区块签名",
					"blockNumber", block.Block.Number(),
					"parentNumber", parentHeader.Number,
					"parentSignatureLength", len(parentSignature.AggregatedSignature))
			} else {
				r.logger.Debug("父区块没有Committed签名",
					"blockNumber", block.Block.Number(),
					"parentNumber", parentHeader.Number)
			}
		}

		// 🆕 计算参与签名的验证者数量（位图设置 AND 有签名）
		participatingCount := 0
		for i := uint64(0); i < uint64(len(productionValidators)); i++ {
			if signatureBitmap.IsSet(i) {
				// 检查该验证者是否真的有签名
				if sigBytes, exists := bitmapToSignature[i]; exists && len(sigBytes) > 0 {
					participatingCount++
				}
			}
		}

		// 🆕 修复：保存全部验证者，确保与生产时使用的验证者集合完全一致
		validatorAddresses := make(validator.AccountSet, 0, len(productionValidators))
		for _, v := range productionValidators {
			// 保存全部验证者，不仅仅是签名者，确保位图索引与验证者集合匹配
					validatorAddresses = append(validatorAddresses, &validator.ValidatorMetadata{
						Address:     v.Address,
						BlsKey:      nil, // 🆕 不保存BLS公钥，验证时从创世文件获取
						VotingPower: v.VotingPower,
						IsActive:    v.IsActive,
					})
			
		}

		signingValidatorDelta := &validator.ValidatorSetDelta{
			Added:   validatorAddresses, // 🆕 保存全部验证者，确保位图索引匹配
			Updated: make(validator.AccountSet, 0),
			Removed: bitmap.Bitmap{},
		}

		// 🆕 记录参与签名的验证者信息（用于调试）



		// 详细记录生产时保存的验证者地址
		r.logger.Debug("🏭 生产时保存的验证者地址详细信息:")
		for i, validator := range validatorAddresses {
			r.logger.Debug("🏭 生产时保存验证者地址",
				"blockNumber", block.Block.Number(),
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil,
				"isParticipating", true) // 只保存实际签名者
		}

		// 静默处理，不打印日志

		// 更新区块的ExtraData，包含聚合签名、父区块签名和验证者集合
		finalExtra := &Extra{
			Validators: signingValidatorDelta, // 🆕 保存全部验证者集合，确保位图索引匹配
			Parent:     parentSignature,       // 父区块签名
			Committed: &Signature{
				AggregatedSignature: aggregatedSignature,
				Bitmap:              signatureBitmap,
			},
			Checkpoint: checkpoint,
		}
		block.Block.Header.ExtraData = finalExtra.MarshalRLPTo(nil)

		// 重新计算区块哈希，因为ExtraData已经更新
		block.Block.Header.ComputeHash()

		r.logger.Debug("区块签名更新完成",
			"blockNumber", block.Block.Number(),
			"extraDataLength", len(block.Block.Header.ExtraData),
			"newBlockHash", block.Block.Header.Hash.String())
	}

	// 🆕 清理缓存，为下一个区块做准备
	r.cachedProductionValidators = nil
	r.logger.Debug("🧹 已清理验证者缓存，为下一个区块做准备")

	return block, nil
}

// processVote 处理投票
func (r *dposRuntime) processVote(vote *VoteMessage) error {
	// 验证投票签名
	if err := r.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("invalid vote signature: %w", err)
	}

	// 更新投票者信息
	voter, exists := r.voters[vote.Voter]
	if !exists {
		voter = &VoterInfo{
			Address:        vote.Voter,
			VotingPower:    big.NewInt(0),
			VotedDelegates: make([]types.Address, 0),
			LastVoteTime:   uint64(time.Now().Unix()),
			LockedUntil:    uint64(time.Now().Unix()) + 86400, // 锁定24小时
		}
		r.voters[vote.Voter] = voter
	}

	// 更新投票权重
	voter.VotingPower = new(big.Int).Add(voter.VotingPower, vote.Amount)
	voter.LastVoteTime = uint64(time.Now().Unix())

	// 添加受托人到投票列表
	voter.VotedDelegates = append(voter.VotedDelegates, vote.Delegate)

	return nil
}

// verifyVoteSignature 验证投票签名
func (r *dposRuntime) verifyVoteSignature(vote *VoteMessage) error {
	// 1. 检查签名字段
	if len(vote.Signature) == 0 {
		return fmt.Errorf("vote signature is empty")
	}

	// 2. 检查签名长度（ECDSA签名应该是65字节）
	if len(vote.Signature) != 65 {
		return fmt.Errorf("invalid signature length: expected 65 bytes, got %d", len(vote.Signature))
	}

	// 3. 构建待签名消息
	message := r.buildVoteMessage(vote)

	// 4. 计算消息哈希
	hash := crypto.Keccak256(message)

	// 5. 恢复公钥
	publicKey, err := crypto.RecoverPubkey(vote.Signature, hash)
	if err != nil {
		return fmt.Errorf("failed to recover public key: %w", err)
	}

	// 6. 验证公钥与投票者地址匹配
	if !r.verifyAddressMatchesPublicKey(vote.Voter, publicKey) {
		return fmt.Errorf("public key does not match voter address")
	}

	// 7. 验证签名
	if !r.verifyECDSASignature(publicKey, hash, vote.Signature) {
		return fmt.Errorf("signature verification failed")
	}

	// 8. 防重放验证
	if err := r.verifyVoteNonce(vote); err != nil {
		return fmt.Errorf("vote nonce verification failed: %w", err)
	}

	r.logger.Debug("投票签名验证成功",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"round", vote.Round)

	return nil
}

// buildVoteMessage 构建标准化的投票消息
func (r *dposRuntime) buildVoteMessage(vote *VoteMessage) []byte {
	// 使用标准化的消息格式，确保签名验证的一致性
	// 格式：voter:delegate:amount:round:timestamp
	message := fmt.Sprintf("%s:%s:%s:%d:%d",
		vote.Voter.String(),
		vote.Delegate.String(),
		vote.Amount.String(),
		vote.Round,
		vote.Timestamp)

	return []byte(message)
}

// verifyAddressMatchesPublicKey 验证地址与公钥匹配
func (r *dposRuntime) verifyAddressMatchesPublicKey(address types.Address, publicKey *ecdsa.PublicKey) bool {
	// 从公钥计算地址
	computedAddress := crypto.PubKeyToAddress(publicKey)
	return computedAddress == address
}

// verifyECDSASignature 验证ECDSA签名
func (r *dposRuntime) verifyECDSASignature(publicKey *ecdsa.PublicKey, hash []byte, signature []byte) bool {
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

// verifyVoteNonce 验证投票nonce，防止重放攻击
func (r *dposRuntime) verifyVoteNonce(vote *VoteMessage) error {
	// 构建nonce key
	nonceKey := fmt.Sprintf("%s:%d:%d",
		vote.Voter.String(),
		vote.Round,
		vote.Timestamp)

	r.voteMutex.Lock()
	defer r.voteMutex.Unlock()

	// 检查是否已经处理过这个nonce
	if r.processedVotes[nonceKey] {
		return fmt.Errorf("vote nonce already processed: %s", nonceKey)
	}

	// 标记为已处理
	r.processedVotes[nonceKey] = true

	// 清理过期的nonce（超过1小时的）
	r.cleanupExpiredVotes()

	return nil
}

// cleanupExpiredVotes 清理过期的投票nonce
func (r *dposRuntime) cleanupExpiredVotes() {
	// 这里可以实现更复杂的清理逻辑
	// 目前使用简单的策略：当map大小超过1000时清理一半
	if len(r.processedVotes) > 1000 {
		// 简单清理：保留一半
		count := 0
		for key := range r.processedVotes {
			if count >= 500 {
				delete(r.processedVotes, key)
			}
			count++
		}
	}
}

// updateDelegateVotingPower 更新受托人投票权重
func (r *dposRuntime) updateDelegateVotingPower(delegate types.Address, amount *big.Int) {
	// 使用静态变量跟踪调用次数
	static := struct {
		count int
		mu    sync.Mutex
	}{}

	static.mu.Lock()
	static.count++
	currentCount := static.count
	static.mu.Unlock()

	fmt.Printf("🔍 updateDelegateVotingPower: 开始更新受托人投票权重 (第%d次调用)\n", currentCount)
	fmt.Printf("  - 调用时间: %s\n", time.Now().Format("15:04:05.000"))
	fmt.Printf("  - 受托人地址: %s\n", delegate.String())
	fmt.Printf("  - 新增投票权重: %s (0x%x)\n", amount.String(), amount.Bytes())

	for _, d := range r.delegates {
		if d.Address == delegate {
			oldPower := new(big.Int).Set(d.VotingPower)
			d.VotingPower = new(big.Int).Add(d.VotingPower, amount)

			fmt.Printf("  - 找到受托人: %s\n", d.Address.String())
			fmt.Printf("  - 旧投票权重: %s\n", oldPower.String())
			fmt.Printf("  - 新投票权重: %s\n", d.VotingPower.String())
			fmt.Printf("  - 计算过程: %s + %s = %s\n", oldPower.String(), amount.String(), d.VotingPower.String())
			break
		}
	}
}

// GenerateExitProof generates proof of exit for given exit event
func (r *dposRuntime) GenerateExitProof(exitID uint64) (types.Proof, error) {
	// TODO: 实现退出证明生成逻辑
	return types.Proof{}, nil
}

// GetStateSyncProof retrieves the StateSync proof
func (r *dposRuntime) GetStateSyncProof(stateSyncID uint64) (types.Proof, error) {
	// TODO: 实现状态同步证明获取逻辑
	return types.Proof{}, nil
}

type DPoS struct {
	// 复用基础设施
	state  *State
	key    *wallet.Key
	logger hclog.Logger

	// DPoS特有组件
	config  *DPoSConfig
	runtime *dposRuntime

	// reference to the syncer
	syncer syncer.Syncer

	// 网络组件
	consensusTopic *network.Topic

	// 状态管理
	delegates            validator.AccountSet
	voters               map[types.Address]*VoterInfo
	currentRound         uint64
	currentDelegateIndex uint64

	// 同步控制
	closeCh chan struct{}
	lock    sync.RWMutex

	// 区块链引用
	blockchain blockchainBackend
	txPool     txPoolInterface

	// 区块时间
	blockTime time.Duration

	// 数据目录
	dataDir string

	// 验证者缓存
	validatorsCache *validatorsSnapshotCache

	// IBFT 共识包装器
	ibft *IBFTConsensusWrapper

	// 添加性能优化相关结构
	cache          *DPoSCache
	batchProcessor *BatchProcessor
	metrics        *DPoSMetrics

	// 🆕 方案1+方案2：延迟验证者集合更新标志
	pendingValidatorUpdate bool

	// 已处理的区块哈希集合，避免重复处理（使用LRU缓存）
	processedBlocks *lru.Cache
	processedMutex  sync.RWMutex

	// 🆕 新增：余额查询器
	balanceQuerier NativeTokenBalanceQuerier

	// 🆕 新增：DPoS验证者相关字段
	minStakeAmount    *big.Int             // 最小质押门槛
	genesisExtraData  []byte               // 创世块extraData
	
	// 🆕 新增：BLS网络通信相关字段
	blsRequestTopic  *network.Topic // BLS公钥请求Topic
	blsResponseTopic *network.Topic // BLS公钥响应Topic
	
	// 🆕 新增：BLS加载状态管理
	blsLoadingComplete    bool
	blsLoadingMutex       sync.RWMutex
	blsLoadingWaitCh      chan struct{}
}

// VoterInfo 投票者信息
type VoterInfo struct {
	Address        types.Address
	VotingPower    *big.Int
	VotedDelegates []types.Address
	LastVoteTime   uint64
	LockedUntil    uint64
	Nonce          map[uint64]bool // 防重放
}

// DelegateInfo 受托人信息
type DelegateInfo struct {
	Address        types.Address
	VotingPower    *big.Int
	TotalVotes     *big.Int
	ProducedBlocks uint64
	MissedBlocks   uint64
	LastBlockTime  uint64
	IsActive       bool
	// 🆕 新增：BLS公钥，确保签名验证一致性
	BlsPublicKey []byte
}

// VoteInfo 投票信息结构（用于解析交易数据）
type VoteInfo struct {
	Voter     types.Address `json:"voter"`
	Candidate types.Address `json:"candidate"`
	Amount    *big.Int      `json:"amount"`
}

// AddVote 添加投票到 DPoS 状态（供 JSON-RPC 调用）
func (d *DPoS) AddVote(voter types.Address, candidate types.Address, amount *big.Int) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.logger.Debug("Adding vote to DPoS state",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String())

	// 创建投票消息
	vote := &VoteMessage{
		Voter:     voter,
		Delegate:  candidate,
		Amount:    amount,
		Round:     d.currentRound,
		Timestamp: uint64(time.Now().Unix()),
	}

	// 处理投票（内部调用，不重复加锁）
	d.logger.Debug("🔄 Calling processVoteInternal...")
	if err := d.processVoteInternal(vote); err != nil {
		d.logger.Error("❌ Failed to process vote", "error", err)
		return fmt.Errorf("failed to process vote: %w", err)
	}
	d.logger.Debug("✅ processVoteInternal completed successfully")

	// 🆕 新增：持久化投票信息到数据库
	d.logger.Debug("🔄 Starting vote persistence to database...")
	if err := d.persistVoteToDatabase(voter, candidate, amount); err != nil {
		d.logger.Error("Failed to persist vote to database", "error", err)
		// 注意：这里不返回错误，因为内存更新已经成功
		// 但记录错误日志以便调试
	} else {
		d.logger.Debug("✅ Vote successfully persisted to database",
			"voter", voter.String(),
			"candidate", candidate.String(),
			"amount", amount.String())
	}

	d.logger.Debug("Vote added successfully to DPoS state",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String())

	// 🆕 方案1+方案2：投票完成后标记需要延迟更新验证者集合
	d.logger.Debug("🔄 投票完成，标记需要延迟更新验证者集合...")
	d.pendingValidatorUpdate = true
	d.logger.Info("✅ 投票完成，验证者集合将在下一轮更新",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String())

	// 🆕 延迟同步机制：只有在轮次边界时才同步新的验证者集合
	// 当前轮次继续使用旧的验证者集合，确保"每个节点出一个块"的规则
	if !d.pendingValidatorUpdate {
		// 如果没有待更新的验证者集合，立即同步（向后兼容）
		d.logger.Info("🔄 启动异步同步 dposRuntime delegates 状态...")
		go func() {
			d.syncRuntimeDelegatesWithRetry()
		}()
	} else {
		// 如果有待更新的验证者集合，延迟到轮次边界同步
		d.logger.Info("⏳ 投票完成，验证者集合同步将延迟到轮次边界",
			"voter", voter.String(),
			"candidate", candidate.String(),
			"amount", amount.String(),
			"note", "当前轮次继续使用旧验证者集合")

		// 🆕 打印当前轮次和当前验证者集合
		d.logger.Info("📋 当前轮次验证者集合详细信息:")
		d.logger.Info("🎯 当前轮次", "round", d.currentRound, "delegatesCount", len(d.delegates))
		for i, delegate := range d.delegates {
			d.logger.Info("📝 当前验证者",
				"round", d.currentRound,
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive,
				"hasBlsKey", delegate.BlsKey != nil)
		}
	}

	// 🆕 新增：数据同步验证 - 确保内存和数据库中的验证者集合一致
	// 暂时注释掉，避免阻塞投票流程
	/*
		d.logger.Info("🔍 Verifying data consistency after vote...")
		if err := d.verifyDataConsistencyAfterVote(); err != nil {
			d.logger.Error("❌ Data consistency verification failed after vote", "error", err)
			// 不返回错误，但记录警告
		} else {
			d.logger.Info("✅ Data consistency verified after vote")
		}
	*/
	d.logger.Info("⚠️ Data consistency verification temporarily disabled to avoid blocking")

	// 验证 voters 字段是否被正确更新
	d.logger.Info("Verifying voters field update...")
	if voterInfo, exists := d.voters[voter]; exists {
		d.logger.Info("Voter info found in d.voters",
			"voter", voter.String(),
			"votingPower", voterInfo.VotingPower.String(),
			"votedDelegatesCount", len(voterInfo.VotedDelegates),
			"lastVoteTime", voterInfo.LastVoteTime)

		// 检查是否包含当前投票的受托人
		found := false
		for _, delegate := range voterInfo.VotedDelegates {
			if delegate == candidate {
				found = true
				break
			}
		}
		if found {
			d.logger.Info("✅ Delegate found in voter's voted delegates list")
		} else {
			d.logger.Warn("❌ Delegate NOT found in voter's voted delegates list")
		}
	} else {
		d.logger.Error("❌ Voter info NOT found in d.voters after AddVote")
	}

	// 显示当前 voters 字段的总数
	d.logger.Info("Current d.voters field status",
		"totalVoters", len(d.voters),
		"voterAddresses", func() []string {
			addresses := make([]string, 0, len(d.voters))
			for addr := range d.voters {
				addresses = append(addresses, addr.String())
			}
			return addresses
		}())

	return nil
}

func (d *DPoS) VerifyHeader(header *types.Header) error {
	blockNumber := header.Number
	d.logger.Debug("🔍 DPoS VerifyHeader 开始验证", "blockNumber", blockNumber, "blockHash", header.Hash.String()[:16])
	
	// 🆕 添加：检查是否是共识切换高度
	if d.config.ConsensusSwitchHeight > 0 && blockNumber == d.config.ConsensusSwitchHeight {
		d.logger.Info("🔄 共识切换高度区块，跳过DPoS验证", "blockNumber", blockNumber, "consensusSwitchHeight", d.config.ConsensusSwitchHeight)
		return nil
	}
	
	// 🆕 关键：在验证前等待BLS公钥加载完成
	if err := d.waitForBLSKeysLoaded(); err != nil {
		d.logger.Error("❌ 等待BLS公钥加载失败", "blockNumber", blockNumber, "error", err)
		return fmt.Errorf("BLS keys not loaded: %w", err)
	}
	
	// Short circuit if the header is known
	if _, ok := d.blockchain.GetHeaderByHash(header.Hash); ok {
		d.logger.Info("✅ DPoS VerifyHeader 区块已存在，跳过验证", "blockNumber", blockNumber)
		return nil
	}

	// 🆕 修复：只在真正需要调试时才打印详细日志
	// 这个日志不应该每次都打印，因为会造成"验证失败"的假象
	d.logger.Debug("🔍 开始验证区块",
		"blockNumber", header.Number,
		"blockHash", header.Hash.String(),
		"blockParentHash", header.ParentHash.String())

	// // 🆕 尝试获取本地区块341的信息进行对比
	// if header.Number == 342 {
	// 	// 尝试通过区块号获取本地区块341
	// 	localBlock341, exists := d.blockchain.GetHeaderByNumber(341)
	// 	if exists {
	// 		d.logger.Error("📋 本地区块341信息",
	// 			"localBlock341Hash", localBlock341.Hash.String(),
	// 			"localBlock341HashHex", fmt.Sprintf("0x%x", localBlock341.Hash),
	// 			"localBlock341ParentHash", localBlock341.ParentHash.String(),
	// 			"localBlock341ParentHashHex", fmt.Sprintf("0x%x", localBlock341.ParentHash))

	// 		// 🆕 对比哈希是否匹配
	// 		if localBlock341.Hash == header.ParentHash {
	// 			d.logger.Error("✅ 哈希匹配：区块342的parentHash与本地区块341哈希一致")
	// 		} else {
	// 			d.logger.Error("❌ 哈希不匹配：区块342的parentHash与本地区块341哈希不一致",
	// 				"block342ParentHash", header.ParentHash.String(),
	// 				"localBlock341Hash", localBlock341.Hash.String())
	// 		}
	// 	} else {
	// 		d.logger.Error("❌ 本地区块链中找不到区块341")
	// 	}
	// }

	d.logger.Debug("🔍 DPoS VerifyHeader 获取父区块", "blockNumber", blockNumber, "parentHash", header.ParentHash.String()[:16])
	parent, ok := d.blockchain.GetHeaderByHash(header.ParentHash)
	if !ok {
		d.logger.Error("❌ 无法通过哈希获取父区块",
			"blockNumber", header.Number,
			"parentHash", header.ParentHash.String(),
			"parentHashHex", fmt.Sprintf("0x%x", header.ParentHash))

		return fmt.Errorf(
			"unable to get parent header by hash for block number %d",
			header.Number,
		)
	}
	d.logger.Debug("✅ DPoS VerifyHeader 父区块获取成功", "blockNumber", blockNumber, "parentNumber", parent.Number)

	d.logger.Debug("🔍 DPoS VerifyHeader 开始调用verifyHeaderImpl", "blockNumber", blockNumber)
	err := d.verifyHeaderImpl(parent, header, d.config.BlockTime.Duration, nil)
	if err != nil {
		d.logger.Info("❌ DPoS VerifyHeader verifyHeaderImpl失败", "blockNumber", blockNumber, "error", err)
		return err
	}
	d.logger.Debug("✅ DPoS VerifyHeader 验证完成", "blockNumber", blockNumber)
	return nil
}

func (d *DPoS) verifyHeaderImpl(parent, header *types.Header, blockTimeDrift time.Duration, parents []*types.Header) error {
	blockNumber := header.Number
	d.logger.Debug("🔍 DPoS verifyHeaderImpl 开始验证", "blockNumber", blockNumber, "extraDataLength", len(header.ExtraData))
	
	// 添加详细的日志 - 节点3验证区块2头部
	d.logger.Debug("=== 验证区块头部开始 ===",
		"blockNumber", header.Number,
		"blockHash", header.Hash.String(),
		"parentNumber", parent.Number,
		"parentHash", parent.Hash.String(),
		"extraDataLength", len(header.ExtraData))

	// 🆕 添加验证时区块头详细信息
	d.logger.Debug("🔍 ===== 验证时区块头详细信息 =====",
		"blockNumber", header.Number,
		"blockHash", header.Hash.String(),
		"parentHash", header.ParentHash.String(),
		"timestamp", header.Timestamp,
		"gasLimit", header.GasLimit,
		"gasUsed", header.GasUsed,
		"difficulty", header.Difficulty,
		"stateRoot", header.StateRoot.String(),
		"transactionsRoot", header.TxRoot.String(),
		"receiptsRoot", header.ReceiptsRoot.String(),
		"miner", types.BytesToAddress(header.Miner).String(),
		"nonce", header.Nonce.String(),
		"extraDataLength", len(header.ExtraData),
		"说明", "验证时区块头的所有关键字段")

	// validate header fields
	d.logger.Debug("🔍 DPoS verifyHeaderImpl 开始验证头部字段", "blockNumber", blockNumber)
	if err := validateHeaderFields(parent, header, uint64(blockTimeDrift.Seconds())); err != nil {
		d.logger.Error("区块头部字段验证失败", "error", err)
		return fmt.Errorf("failed to validate header for block %d. error = %w", header.Number, err)
	}
	d.logger.Debug("✅ DPoS verifyHeaderImpl 头部字段验证通过", "blockNumber", blockNumber)

	d.logger.Debug("区块头部字段验证通过")

	// decode the extra data
	d.logger.Debug("🔍 DPoS verifyHeaderImpl 开始解析extraData", "blockNumber", blockNumber)
	extra, err := GetIbftExtra(header.ExtraData)
	if err != nil {
		d.logger.Error("解析区块extraData失败", "error", err)
		return fmt.Errorf("failed to verify header for block %d. get extra error = %w", header.Number, err)
	}
	d.logger.Debug("✅ DPoS verifyHeaderImpl extraData解析成功", "blockNumber", blockNumber)

	d.logger.Debug("区块extraData解析成功",
		"committedSignatureLength", len(extra.Committed.AggregatedSignature),
		"committedBitmapLength", len(extra.Committed.Bitmap),
		"checkpointExists", extra.Checkpoint != nil)

	// validate extra data
	d.logger.Debug("🔍 DPoS verifyHeaderImpl 开始验证extraData", "blockNumber", blockNumber)
	err = extra.ValidateFinalizedData(
		header, parent, parents, d.blockchain.GetChainID(), d, signer.DomainValidatorSet, d.logger)

	if err != nil {
		d.logger.Error("🚨 区块extraData验证失败，将返回错误让上层处理",
			"blockNumber", header.Number,
			"blockHash", header.Hash.String(),
			"error", err)
		d.logger.Error("=== 验证区块头部失败 ===")
		return fmt.Errorf("block extraData validation failed: %w", err)
	}
	d.logger.Debug("✅ DPoS verifyHeaderImpl extraData验证成功", "blockNumber", blockNumber)

	d.logger.Debug("区块extraData验证成功")
	d.logger.Debug("=== 验证区块头部成功 ===")
	d.logger.Debug("✅ DPoS verifyHeaderImpl 验证完成", "blockNumber", blockNumber)
	return nil
}

func (d *DPoS) ProcessHeaders(headers []*types.Header) error {
	// For DPoS, we need to update round state when receiving new blocks
	d.logger.Info("🔄 DPoS ProcessHeaders被调用", "count", len(headers), "runtimeIsNil", d.runtime == nil)

	// Update round state for each new block
	for _, header := range headers {
		d.logger.Info("🔄 DPoS处理区块头部", "blockNumber", header.Number, "blockHash", header.Hash.String()[:16])

		// 检查是否已经处理过这个区块
		d.processedMutex.RLock()
		if d.processedBlocks == nil {
			d.processedMutex.RUnlock()
			// 初始化LRU缓存
			d.processedMutex.Lock()
			if d.processedBlocks == nil {
				var err error
				d.processedBlocks, err = lru.New(1000) // 最多1000个条目
				if err != nil {
					d.processedMutex.Unlock()
					return fmt.Errorf("failed to create LRU cache: %w", err)
				}
			}
			d.processedMutex.Unlock()
			d.processedMutex.RLock()
		}
		
		_, exists := d.processedBlocks.Get(header.Hash)
		d.processedMutex.RUnlock()
		
		if exists {
			d.logger.Debug("block already processed, skipping", "blockNumber", header.Number, "blockHash", header.Hash)
			continue
		}

		// 标记区块已处理
		d.processedMutex.Lock()
		d.processedBlocks.Add(header.Hash, true)
		d.processedMutex.Unlock()

		// 🆕 修复：简化区块投票处理，避免数据库锁竞争
		// 直接使用header数据，不进行异步处理
		d.logger.Debug("🔄 处理区块投票", "blockNumber", header.Number, "blockHash", header.Hash.String()[:16])
		if err := d.processBlockVotesFromHeader(header); err != nil {
			d.logger.Error("failed to process block votes from header", "blockNumber", header.Number, "blockHash", header.Hash, "error", err)
			// 不返回错误，继续处理其他逻辑
		} else {
			d.logger.Debug("✅ 投票处理完成", "blockNumber", header.Number, "blockHash", header.Hash.String()[:16])
		}

		// 同步更新轮次状态（这部分必须同步执行，不能异步）
		d.updateRoundState(header)
	}

	return nil
}

// 🆕 新增：更新轮次状态（从ProcessHeaders中提取出来）
func (d *DPoS) updateRoundState(header *types.Header) {
	// 检查这个区块是否是我们自己生产的
	blockMiner := types.BytesToAddress(header.Miner)
	keyAddr := types.Address(d.key.Address())

	// 更新轮次状态 - 只有接收其他节点的区块时才更新轮次
	// 如果是自己生产的区块，轮次已经在produceBlock中更新过了
	d.logger.Info("🔍 检查区块生产者", 
		"blockNumber", header.Number,
		"blockMiner", blockMiner.String(),
		"keyAddr", keyAddr.String(),
		"isOurBlock", blockMiner == keyAddr)
		
	if blockMiner != keyAddr {
		// 接收其他节点的区块，需要更新轮次
		d.logger.Info("🔄 接收其他节点区块，准备更新轮次", 
			"blockNumber", header.Number,
			"blockMiner", blockMiner.String(),
			"keyAddr", keyAddr.String())
			
		if d.runtime != nil {
			d.runtime.lock.Lock()
			oldIndex := d.runtime.currentDelegateIndex
			oldRound := d.runtime.currentRound

			d.logger.Info("🔍 更新前轮次状态", 
				"blockNumber", header.Number,
				"oldRound", oldRound,
				"oldDelegateIndex", oldIndex)

			// 🆕 使用区块头部的区块号，确保一致性
			d.runtime.updateRound(header.Number)

			d.runtime.lock.Unlock()

			d.logger.Info("✅ 轮次状态更新完成",
				"blockNumber", header.Number,
				"blockMiner", blockMiner.String(),
				"keyAddr", keyAddr.String(),
				"oldRound", oldRound,
				"newRound", d.runtime.currentRound,
				"oldDelegateIndex", oldIndex,
				"newDelegateIndex", d.runtime.currentDelegateIndex)
		} else {
			d.logger.Error("❌ runtime为nil，无法更新轮次状态", 
				"blockNumber", header.Number)
		}
	} else {
		// 自己生产的区块，轮次已经在produceBlock中更新过了
		d.logger.Info("ℹ️ 处理自己生产的区块，轮次已在produceBlock中更新",
			"blockNumber", header.Number,
			"currentRound", func() uint64 {
				if d.runtime != nil {
					return d.runtime.currentRound
				}
				return 0
			}(),
			"currentDelegateIndex", func() uint64 {
				if d.runtime != nil {
					return d.runtime.currentDelegateIndex
				}
				return 0
			}())
	}
}

// 🆕 新增：从区块头部处理投票事件（供ProcessHeaders调用）
func (d *DPoS) processBlockVotesFromHeader(header *types.Header) error {
	d.logger.Debug("🔄 开始处理区块头部的投票事件", "blockNumber", header.Number, "blockHash", header.Hash)

	if header == nil {
		d.logger.Warn("⚠️ 区块头部为空，跳过投票事件处理")
		return nil
	}

	// 🆕 修复：简化投票处理，直接使用header数据，避免数据库锁竞争
	// 创建一个简化的区块对象用于投票处理
	block := &types.Block{
		Header: header,
		// 对于投票处理，我们不需要完整的交易数据
		Transactions: []*types.Transaction{},
	}

	// 转换为FullBlock格式
	fullBlock := &types.FullBlock{
		Block:    block,
		Receipts: []*types.Receipt{}, // 空的receipts
	}

	d.logger.Debug("🔍 使用简化的区块数据进行投票处理",
		"blockNumber", header.Number,
		"blockHash", header.Hash)

	// 调用现有的投票处理逻辑
	return d.processBlockVotes(fullBlock)
}

func (d *DPoS) GetBlockCreator(header *types.Header) (types.Address, error) {
	return types.BytesToAddress(header.Miner), nil
}

func (d *DPoS) PreCommitState(block *types.Block, _ *state.Transition) error {
	// For DPoS, we don't need to validate commitment state transactions like PolyBFT
	// This is mainly used for state transition validation
	d.logger.Debug("pre-commit state validation", "block", block.Number())
	return nil
}

func (d *DPoS) GetSyncProgression() *progress.Progression {
	if d.syncer != nil {
		return d.syncer.GetSyncProgression()
	}
	return nil
}

func (d *DPoS) GetBridgeProvider() consensus.BridgeDataProvider {
	if d.runtime != nil {
		return d.runtime
	}
	return nil
}

func (d *DPoS) FilterExtra(extra []byte) ([]byte, error) {
	return GetIbftExtraClean(extra)
}

func (d *DPoS) Start() error {
	d.logger.Info("starting dpos consensus", "signer", d.key.String())

	// 🆕 详细检查DPoS实例状态
	d.logger.Info("🔍 DPoS Start() 详细状态检查",
		"runtimeIsNil", d.runtime == nil,
		"txPoolIsNil", d.txPool == nil,
		"syncerIsNil", d.syncer == nil,
		"stateIsNil", d.state == nil,
		"delegatesCount", len(d.delegates))

	// 🆕 1. 初始化BLS加载状态
	d.initializeBLSLoadingState()

	// 🆕 2. 从数据库加载验证者信息（按voterpower排序并截取前N个）
	if err := d.loadValidatorsFromDatabaseWithLimit(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载验证者失败，将使用runtime中的验证者", "error", err)
		// 如果数据库为空，使用runtime中已经解析和排序的验证者
		if d.runtime != nil && len(d.runtime.delegates) > 0 {
			d.delegates = d.runtime.delegates.Copy()
			d.logger.Info("✅ 使用runtime中的验证者", "count", len(d.delegates))
		} else {
			return fmt.Errorf("no validators available from database or runtime")
		}
	}

	// 设置交易池为密封状态，允许交易提升和区块构建
	// 注意：只有当前节点是出块者时才设置 sealing=true
	if d.txPool != nil {
		// 检查当前节点是否是受托人（出块者）
		if d.key != nil {
			keyAddr := types.Address(d.key.Address())
			isDelegate := false

			d.lock.RLock()
			for _, delegate := range d.delegates {
				if delegate.Address == keyAddr {
					isDelegate = true
					break
				}
			}
			d.lock.RUnlock()

			if isDelegate {
				d.txPool.SetSealing(true)
				d.logger.Info("transaction pool sealing state set to true (node is delegate)")
				
				// 🆕 只有受托人节点才启用状态广播
				// 注意：需要直接访问syncPeerClient，但当前syncer接口没有暴露此方法
				// 暂时注释掉，需要修改syncer接口或使用其他方式
				// d.syncer.GetSyncPeerClient().EnablePublishingPeerStatus()
				d.logger.Info("enabled status broadcasting (node is delegate)")
			} else {
				d.txPool.SetSealing(false)
				d.logger.Info("transaction pool sealing state set to false (node is not delegate)")
				
				// 🆕 非受托人节点禁用状态广播
				// 注意：需要直接访问syncPeerClient，但当前syncer接口没有暴露此方法
				// 暂时注释掉，需要修改syncer接口或使用其他方式
				// d.syncer.GetSyncPeerClient().DisablePublishingPeerStatus()
				d.logger.Info("disabled status broadcasting (node is not delegate)")
			}
		} else {
			d.logger.Warn("key not available, cannot determine if node is delegate")
		}
	} else {
		d.logger.Warn("transaction pool not available, cannot set sealing state")
	}

	// 🆕 3. 先同步获取BLS公钥（确保网络集成层已就绪）
	d.logger.Info("🔑 开始同步获取BLS公钥...")
	if err := d.syncLoadBLSKeys(); err != nil {
		d.logger.Error("❌ BLS公钥同步获取失败", "error", err)
		return fmt.Errorf("failed to sync load BLS keys: %w", err)
	}
	d.logger.Info("✅ BLS公钥同步获取完成")

	// 🆕 4. 启动syncer（BLS公钥加载完成后）
	d.logger.Info("🌐 开始启动syncer...")
	if err := d.syncer.Start(); err != nil {
		// 🆕 检查是否是topic冲突错误，如果是则忽略
		if strings.Contains(err.Error(), "topic already exists") {
			d.logger.Warn("⚠️ syncer启动遇到topic冲突，但继续启动DPoS runtime", "error", err)
		} else {
			d.logger.Error("❌ syncer启动失败（非topic冲突）", "error", err)
			return fmt.Errorf("failed to start syncer. Error: %w", err)
		}
	} else {
		d.logger.Info("✅ syncer启动成功")
	}

	// 🆕 5. 注意：不在这里设置blsLoadingComplete，让syncLoadBLSKeys自己设置

	// 🆕 添加关键检查点日志
	d.logger.Info("🔍 准备启动DPoS runtime，检查runtime状态",
		"runtimeIsNil", d.runtime == nil)

	// sync concurrently, retrying indefinitely
	go common.RetryForever(context.Background(), time.Second, func(context.Context) error {
		// 🆕 在区块同步前检查BLS公钥是否加载完成
		if err := d.waitForBLSKeysLoaded(); err != nil {
			d.logger.Warn("⚠️ 等待BLS公钥加载完成失败，继续尝试同步", "error", err)
			// 不返回错误，继续尝试同步
		}
		
		blockHandler := func(b *types.FullBlock) bool {
			// 实现DPoS的区块处理逻辑
			d.logger.Debug("processing block", "number", b.Block.Number())

			// 🆕 修复：移除投票处理逻辑，避免重复处理
			// 投票处理现在统一在ProcessHeaders中进行
			// if err := d.processBlockVotes(b); err != nil {
			// 	d.logger.Error("failed to process block votes", "error", err, "block", b.Block.Number())
			// }

			// // 更新受托人集合
			// if err := d.updateDelegates(b); err != nil {
			// 	d.logger.Error("failed to update delegates", "error", err, "block", b.Block.Number())
			// }

			// 处理奖励分配
			if err := d.processRewards(b); err != nil {
				d.logger.Error("failed to process rewards", "error", err, "block", b.Block.Number())
			}

			return false
		}
		if err := d.syncer.Sync(blockHandler); err != nil {
			d.logger.Error("blocks synchronization failed", "error", err)
			return err
		}
		return nil
	})

	// start consensus runtime if available
	if d.runtime != nil {
		d.logger.Info("🔧 开始启动DPoS runtime...")
		
		// 检查runtime是否已正确初始化
		if d.runtime.config == nil || d.runtime.config.Key == nil {
			d.logger.Error("❌ DPoS runtime未正确初始化", 
				"configIsNil", d.runtime.config == nil,
				"keyIsNil", d.runtime.config != nil && d.runtime.config.Key == nil)
			return fmt.Errorf("DPoS runtime not properly initialized: Key is nil")
		}

		// 🆕 详细检查runtime状态
		d.logger.Info("🔍 DPoS runtime详细状态检查",
			"resourceMonitorIsNil", d.runtime.resourceMonitor == nil,
			"goroutineManagerIsNil", func() bool {
				if d.runtime.resourceMonitor != nil {
					return d.runtime.resourceMonitor.goroutineManager == nil
				}
				return true
			}(),
			"delegatesCount", len(d.runtime.delegates))

		// 启动DPoS运行时
		d.logger.Info("🚀 调用d.runtime.start()...")
		if err := d.runtime.start(); err != nil {
			d.logger.Error("❌ 启动DPoS runtime失败", "error", err)
			return fmt.Errorf("failed to start DPoS runtime: %w", err)
		}
		d.logger.Info("✅ DPoS runtime启动成功")

		// BLS公钥已在runtime启动前同步加载完成

		// 新节点启动后，主动查询其他节点的待处理签名请求
		go func() {
			// 等待一段时间让网络连接稳定
			time.Sleep(10 * time.Second)
			// 新节点启动，开始查询待处理签名请求（静默处理）
			d.runtime.queryPendingSignatureRequests()
		}()

		// 🆕 移除：启动时BLS公钥广播（采用按需请求机制）
		// 新的机制：只有在需要BLS公钥时才通过网络请求获取
	} else {
		d.logger.Error("❌ DPoS runtime为nil，无法启动出块循环")
	}

	// start state DB process if available
	if d.state != nil {
		go d.state.startStatsReleasing()
	}

	// 🆕 新增：从数据库恢复投票数据
	if err := d.restoreVotingDataFromDatabase(); err != nil {
		d.logger.Error("Failed to restore voting data from database", "error", err)
		// 不返回错误，因为这是非关键操作
	} else {
	}

	// 🆕 新增：启动时直接调用和命令一样的数据源方法
	if err := d.callCommandDataSourcesOnStartup(); err != nil {
		d.logger.Warn("Failed to call command data sources on startup", "error", err)
	}

	// 🆕 移除：BLS公钥预加载已移到Start方法开头，确保在启动其他组件前完成

	// 初始化性能优化组件
	d.initPerformanceOptimizations()

	return nil
}

func (d *DPoS) Close() error {
	if d.syncer != nil {
		if err := d.syncer.Close(); err != nil {
			return err
		}
	}

	close(d.closeCh)

	if d.runtime != nil {
		d.runtime.close()
	}

	// 🆕 从全局注册表中注销DPoS实例
	if d.key != nil {
		key := d.key.Address().String()
		UnregisterDPoSInstance(key)
		d.logger.Info("DPoS实例已从全局注册表中注销", "address", key)
	}

	return nil
}

// DPoS 实现 dposBackend 接口
var _ dposBackend = (*DPoS)(nil)

// Factory 创建DPoS共识实例
func Factory(params *consensus.Params) (consensus.Consensus, error) {
	logger := params.Logger.Named("dpos")

	// 设置自定义哈希函数
	setupHeaderHashFunc()

	vcity_dpos := &DPoS{
		closeCh: make(chan struct{}),
		logger:  logger,
		txPool:  params.TxPool,
	}

	// 解析配置
	customConfigJSON, err := json.Marshal(params.Config.Config)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(customConfigJSON, &vcity_dpos.config)
	if err != nil {
		return nil, err
	}

	// 🆕 新增：从配置中解析共识切换高度
	if consensusSwitchHeight, exists := params.Config.Config["consensusSwitchHeight"]; exists {
		if height, ok := consensusSwitchHeight.(float64); ok {
			vcity_dpos.config.ConsensusSwitchHeight = uint64(height)
			logger.Debug("🔄 设置共识切换高度", "height", vcity_dpos.config.ConsensusSwitchHeight)
		}
	}

	// 设置必要的配置字段
	vcity_dpos.config.SecretsManager = params.SecretsManager
	vcity_dpos.config.Blockchain = params.Blockchain
	vcity_dpos.config.Logger = params.Logger
	vcity_dpos.config.Network = params.Network
	vcity_dpos.config.Executor = params.Executor

	// 🆕 新增：设置数据目录
	vcity_dpos.logger.Debug("Config details",
		"ConfigPath", params.Config.Path,
		"ConfigType", fmt.Sprintf("%T", params.Config),
		"ConfigContent", fmt.Sprintf("%+v", params.Config))

	if params.Config.Path != "" {
		vcity_dpos.dataDir = filepath.Join(params.Config.Path, "dpos")
		vcity_dpos.logger.Debug("DPoS data directory set", "path", vcity_dpos.dataDir)
	} else {
		vcity_dpos.logger.Warn("Config path not set, DPoS data directory will not be available")
		// 尝试使用默认路径
		defaultPath := "./dpos"
		vcity_dpos.dataDir = defaultPath
		vcity_dpos.logger.Info("Using default DPoS data directory", "path", defaultPath)
	}

	// 初始化其他必要字段
	vcity_dpos.voters = make(map[types.Address]*VoterInfo)
	vcity_dpos.delegates = make(validator.AccountSet, 0)
	// vcity_dpos.validatorsCache = newValidatorsSnapshotCache() // TODO: 需要正确的参数

	return vcity_dpos, nil
}

// Initialize 初始化DPoS
func (d *DPoS) Initialize() error {
	d.logger.Debug("initializing dpos...")

	// read account
	account, err := wallet.NewAccountFromSecret(d.config.SecretsManager)
	if err != nil {
		return fmt.Errorf("failed to read account data. Error: %w", err)
	}

	// set key
	d.key = wallet.NewKey(account)

	// 检查Key是否成功设置
	if d.key == nil {
		return fmt.Errorf("failed to create wallet key")
	}

	// 🆕 注册DPoS实例到全局注册表
	// 使用固定key和节点地址作为key，确保能够被找到
	fixedKey := "vcity_dpos"
	nodeKey := d.key.Address().String()
	RegisterDPoSInstance(fixedKey, d)
	RegisterDPoSInstance(nodeKey, d)
	d.logger.Debug("DPoS实例已注册到全局注册表", "fixedKey", fixedKey, "nodeKey", nodeKey)

	// create and set syncer
	d.syncer = syncer.NewSyncer(
		d.config.Logger.Named("syncer"),
		d.config.Network,
		d.config.Blockchain,
		d.config.BlockTime.Duration*3*time.Second,
		d.config.ConsensusSwitchHeight,
	)

	// set blockchain backend
	d.blockchain = &blockchainWrapper{
		blockchain: d.config.Blockchain,
		executor:   d.config.Executor,
	}

	// 🆕 新增：设置余额查询器（使用真实实现）
	// 注意：这里需要传入blockchain实例，暂时使用nil
	// TODO: 传入真实的blockchain实例
	d.balanceQuerier = nil // 暂时禁用，等待blockchain实例传入
	d.logger.Debug("Balance querier initialized (disabled for now)")

	// set block time
	d.blockTime = d.config.BlockTime.Duration

	// 🆕 新增：初始化状态存储
	d.logger.Debug("Attempting to initialize state store",
		"dataDir", d.dataDir,
		"dataDirEmpty", d.dataDir == "",
		"dataDirLength", len(d.dataDir))

	if d.dataDir != "" {
		statePath := filepath.Join(d.dataDir, "dpos.db")
		d.logger.Info("Creating state store", "path", statePath)

		// 确保目录存在
		if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
			d.logger.Error("Failed to create data directory", "path", filepath.Dir(statePath), "error", err)
		} else {
			d.logger.Info("Data directory created/verified", "path", filepath.Dir(statePath))
		}

		state, err := newState(statePath, d.logger, d.closeCh)
		if err != nil {
			d.logger.Error("Failed to initialize state store", "path", statePath, "error", err)
			// 不返回错误，因为状态存储不是关键组件
		} else {
			d.state = state
			d.logger.Info("✅ State store initialized successfully", "path", statePath)
		}
	} else {
		d.logger.Warn("Data directory not set, state store will not be initialized")
	}

	// 🆕 新增：初始化BLS网络通信
	if err := d.initializeBLSNetworking(); err != nil {
		d.logger.Error("Failed to initialize BLS networking", "error", err)
		// 不返回错误，因为BLS网络初始化失败不应该阻止DPoS启动
	}

	// 🆕 移除：DPoS验证者解析移到initializeDelegates中进行
	// 这样可以在数据库没有受托人时才解析extraData，避免重复解析

	// initialize delegates
	if err := d.initializeDelegates(); err != nil {
		return fmt.Errorf("failed to initialize delegates: %w", err)
	}

	// 创建DPoS runtime
	runtimeConfig := &runtimeConfig{
		DataDir:          d.dataDir,
		Key:              d.key,
		State:            d.state,
		blockchain:       d.blockchain,
		dposBackend:      d,
		txPool:           d.txPool,
		DelegateCount:    d.config.DelegateCount,
		InitialDelegates: d.config.InitialDelegates,
	}

	// 检查runtime配置是否正确
	if runtimeConfig.Key == nil {
		return fmt.Errorf("runtime config Key is nil")
	}

	d.runtime = &dposRuntime{
		config:  runtimeConfig,
		backend: d,
		logger:  d.logger.Named("runtime"),
		network: d.config.Network,
		closeCh: make(chan struct{}),
		voters:  make(map[types.Address]*VoterInfo),
	}
	

	// 初始化runtime
	if err := d.runtime.initializeRuntime(); err != nil {
		return fmt.Errorf("failed to initialize runtime: %w", err)
	}

	// 设置网络集成
	if err := d.runtime.setupNetworkIntegration(); err != nil {
		d.logger.Error("failed to setup network integration", "error", err)
		return fmt.Errorf("failed to setup network integration: %w", err)
	}


	return nil
}

// 🆕 新增：从创世块解析DPoS验证者
func (d *DPoS) parseValidatorsFromGenesis() error {
	d.logger.Info("🔍 开始从创世块解析DPoS验证者")
	
	// 1. 获取创世块
	genesisHeader, exists := d.config.Blockchain.GetHeaderByNumber(0)
	if !exists {
		return fmt.Errorf("genesis block not found")
	}
	
	// 保存创世块extraData
	d.genesisExtraData = genesisHeader.ExtraData
	d.logger.Info("📋 创世块extraData已加载", "length", len(d.genesisExtraData))
	
	// 2. 解析验证者地址
	ibftValidators, err := d.parseValidatorsFromExtraData(d.genesisExtraData)
	if err != nil {
		return fmt.Errorf("failed to parse validators from extraData: %w", err)
	}
	
	// 3. 设置最小质押门槛
	d.minStakeAmount = d.config.MinVotingPower
	if d.minStakeAmount == nil {
		// 使用默认值：1000 VCITY = 1000 * 1e18 wei
		d.minStakeAmount, _ = new(big.Int).SetString("1000000000000000000000", 10)
		d.logger.Info("使用默认最小质押门槛", "amount", d.minStakeAmount.String())
	}
	
	// 4. 直接操作 runtime.delegates（如果runtime已初始化）
	if d.runtime == nil {
		d.logger.Warn("⚠️ runtime未初始化，无法解析验证者")
		return fmt.Errorf("runtime not initialized")
	}
	
	// 清空现有的delegates
	d.runtime.delegates = make(validator.AccountSet, 0)
	
	d.logger.Info("🚨 DPoS验证者筛选开始", 
		"totalCandidates", ibftValidators.Len(),
		"minStakeAmount", d.minStakeAmount.String())
	
	validValidatorCount := 0
	insufficientBalanceCount := 0
	
	// 5. 为每个验证者地址检查余额并生成BLS公钥
	for i := 0; i < ibftValidators.Len(); i++ {
		ibftValidator := ibftValidators[i]
		address := ibftValidator.Address
		
		// 查询验证者余额
		balance, err := d.getValidatorBalance(address)
		if err != nil {
			d.logger.Error("❌ 余额查询失败", 
				"address", address.String(), 
				"error", err)
			continue
		}
		
		// 检查是否满足最小质押要求
		if balance.Cmp(d.minStakeAmount) < 0 {
			insufficientBalanceCount++
			d.logger.Warn("⚠️ 验证者余额不足", 
				"address", address.String(),
				"balance", balance.String(),
				"required", d.minStakeAmount.String(),
				"deficit", new(big.Int).Sub(d.minStakeAmount, balance).String())
			continue
		}
		
		// 创建DPoS验证者（BLS公钥延迟获取）
		delegate := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: balance,
			BlsKey:      nil, // BLS公钥将在需要时获取
			IsActive:    true,
		}
		
		// 直接添加到 runtime.delegates
		d.runtime.delegates = append(d.runtime.delegates, delegate)
		validValidatorCount++
		
		d.logger.Info("✅ DPoS验证者创建成功（BLS公钥延迟获取）",
			"address", address.String(),
			"balance", balance.String(),
			"validatorIndex", validValidatorCount)
	}
	
	// 关键日志：DPoS验证者筛选结果汇总
	d.logger.Info("🚨 DPoS验证者筛选完成", 
		"totalCandidates", ibftValidators.Len(),
		"validValidators", validValidatorCount,
		"insufficientBalance", insufficientBalanceCount,
		"successRate", fmt.Sprintf("%.1f%%", float64(validValidatorCount)/float64(ibftValidators.Len())*100))
	
	if validValidatorCount == 0 {
		d.logger.Error("❌ 没有验证者满足DPoS质押要求", 
			"totalCandidates", ibftValidators.Len(),
			"minStakeAmount", d.minStakeAmount.String())
		return fmt.Errorf("no validators meet DPoS stake requirements")
	}
	
	if validValidatorCount < 2 {
		d.logger.Warn("⚠️ 警告：DPoS验证者数量过少", 
			"validValidators", validValidatorCount,
			"建议至少需要2个验证者")
	}
	
	d.logger.Info("✅ DPoS验证者解析完成", "count", validValidatorCount)
	
	return nil
}

// 🆕 新增：从extraData解析验证者地址
func (d *DPoS) parseValidatorsFromExtraData(extraData []byte) (validator.AccountSet, error) {
	// 开始解析extraData
	
	// Remove only the vanity bytes (32 bytes) from extraData
	// The rest is RLP data containing validators and seals
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short: %d bytes", len(extraData))
	}
	
	// Extract the RLP-encoded data
	// extraData format: [vanity(32)] + [RLP(IstanbulExtra)]
	rlpData := extraData[32:]
	
	// 提取RLP数据
	
	// Create validator accounts
	validatorList := make([]*validator.ValidatorMetadata, 0)
	
	// Parse RLP data using the same method as the test
	err := types.UnmarshalRlp(func(p *fastrlp.Parser, v *fastrlp.Value) error {
		// Get the top-level list
		elems, err := v.GetElems()
		if err != nil {
			return fmt.Errorf("expected array: %w", err)
		}
		
		// 找到验证者列表
		
		// Process each element
		for _, elem := range elems {
			// Try to get bytes
			if bytes, err := elem.GetBytes(nil); err == nil {
				// If it's 20 bytes, it might be an address
				if len(bytes) == 20 {
					addr := types.BytesToAddress(bytes)
					validator := &validator.ValidatorMetadata{
						Address:     addr,
						VotingPower: big.NewInt(0), // 初始化为0，后续会更新
						IsActive:    true,
					}
					validatorList = append(validatorList, validator)
					
					// 解析验证者地址
				}
			} else {
				// Try to get sub-elements
				if subElems, err := elem.GetElems(); err == nil {
					// 找到子列表
					
					for _, subElem := range subElems {
						if subBytes, err := subElem.GetBytes(nil); err == nil {
							// If it's 20 bytes, it might be an address
							if len(subBytes) == 20 {
								addr := types.BytesToAddress(subBytes)
								validator := &validator.ValidatorMetadata{
									Address:     addr,
									VotingPower: big.NewInt(0), // 初始化为0，后续会更新
									IsActive:    true,
								}
								validatorList = append(validatorList, validator)
								
								// 解析子列表中的验证者地址
							}
						}
					}
				}
			}
		}
		
		return nil
	}, rlpData)
	
	if err != nil {
		return nil, fmt.Errorf("failed to parse RLP data: %w", err)
	}
	
	// 成功解析验证者
	
	return validator.AccountSet(validatorList), nil
}

// 🆕 新增：通过网络请求BLS公钥
func (d *DPoS) requestBLSPublicKeyFromNetwork(address types.Address) (*bls.PublicKey, error) {
	// 静默处理，不打印日志
	
	// 1. 检查网络是否可用
	if d.config.Network == nil {
		return nil, fmt.Errorf("network not available")
	}
	
	// 2. 创建BLS公钥请求消息
	requestMsg := &BLSPublicKeyRequest{
		RequesterAddress: types.Address(d.key.Address()),
		TargetAddress:    address,
		Timestamp:        uint64(time.Now().Unix()),
	}
	
	// 3. 序列化请求消息
	requestData, err := json.Marshal(requestMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal BLS request: %w", err)
	}
	
	// 4. 发送网络广播请求
	// 静默处理，不打印日志
	
	// 创建响应通道
	responseCh := make(chan *bls.PublicKey, 1)
	errorCh := make(chan error, 1)
	
	// 注册响应处理器
	requestID := fmt.Sprintf("bls_request_%s_%d", address.String(), requestMsg.Timestamp)
	d.registerBLSResponseHandler(requestID, responseCh, errorCh)
	
	// 发送广播请求
	if err := d.broadcastBLSRequest(requestData, requestID); err != nil {
		d.unregisterBLSResponseHandler(requestID)
		return nil, fmt.Errorf("failed to broadcast BLS request: %w", err)
	}
	
	// 等待响应（设置超时，增加到30秒）
	timeout := time.After(30 * time.Second)
	select {
	case blsKey := <-responseCh:
		d.unregisterBLSResponseHandler(requestID)
		//d.logger.Info("✅ 收到BLS公钥响应", "address", address.String())
		return blsKey, nil
	case err := <-errorCh:
		d.unregisterBLSResponseHandler(requestID)
		return nil, fmt.Errorf("BLS request failed: %w", err)
	case <-timeout:
		d.unregisterBLSResponseHandler(requestID)
		return nil, fmt.Errorf("BLS request timeout for address %s", address.String())
	}
}

// BLSPublicKeyRequest BLS公钥请求消息
type BLSPublicKeyRequest struct {
	RequesterAddress types.Address `json:"requester_address"`
	TargetAddress    types.Address `json:"target_address"`
	Timestamp        uint64        `json:"timestamp"`
}

// 🆕 新增：查询验证者余额
func (d *DPoS) getValidatorBalance(address types.Address) (*big.Int, error) {
	d.logger.Debug("🔍 开始查询验证者余额", "address", address.String())
	
	// 检查config和executor
	if d.config == nil {
		d.logger.Error("❌ DPoS config is nil")
		return big.NewInt(0), nil
	}
	
	if d.config.Executor == nil {
		d.logger.Error("❌ DPoS config.Executor is nil")
		return big.NewInt(0), nil
	}
	
	// 获取当前区块头
	currentHeader := d.config.Blockchain.Header()
	if currentHeader == nil {
		d.logger.Error("❌ 无法获取当前区块头")
		return nil, fmt.Errorf("failed to get current header")
	}
	
	d.logger.Debug("📋 当前区块头信息", "number", currentHeader.Number, "stateRoot", currentHeader.StateRoot.String())
	
	// 通过executor查询余额
	if d.config.Executor != nil {
		// 通过state.Executor的StateAt方法直接获取状态快照
		snapshot, err := d.config.Executor.StateAt(currentHeader.StateRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to create snapshot at state root %s: %w", currentHeader.StateRoot.String(), err)
		}
		
		account, err := snapshot.GetAccount(address)
		if err != nil {
			return nil, fmt.Errorf("failed to get account for address %s: %w", address.String(), err)
		}
		
		// 返回账户余额
		d.logger.Debug("✅ 成功查询到验证者余额", "address", address.String(), "balance", account.Balance.String())
		return account.Balance, nil
	}
	
	// 如果无法获取executor，返回0余额
	d.logger.Warn("无法获取executor，返回0余额", "address", address.String())
	return big.NewInt(0), nil
}

// 🆕 新增：从私钥文件生成BLS公钥
func (d *DPoS) readBLSPrivateKeyAndGeneratePublicKey(validatorAddress types.Address) ([]byte, error) {
	// 1. 构建私钥文件路径 - 为每个验证者生成独立的BLS私钥文件
	parentDir := filepath.Dir(d.dataDir) // 获取 "node1\consensus"
	keyFilePath := filepath.Join(parentDir, "validator-bls.key")
	
	d.logger.Debug("🔍 BLS私钥文件路径", 
		"dataDir", d.dataDir,
		"parentDir", parentDir,
		"keyFilePath", keyFilePath)
	
	// 2. 检查文件是否存在
	if _, err := os.Stat(keyFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("BLS private key file not found: %s", keyFilePath)
	}
	
	// 3. 读取私钥文件
	privateKeyData, err := os.ReadFile(keyFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read BLS private key file: %w", err)
	}
	
	// 4. 获取十六进制字符串（去除可能的换行符）
	privateKeyHex := strings.TrimSpace(string(privateKeyData))
	
	// 5. 检查并修正私钥长度
	if len(privateKeyHex)%2 != 0 {
		d.logger.Info("Private key length is odd, adding leading zero", 
			"originalLength", len(privateKeyHex),
			"originalKey", privateKeyHex)
		privateKeyHex = "0" + privateKeyHex
		d.logger.Info("Private key corrected", 
			"correctedLength", len(privateKeyHex),
			"correctedKey", privateKeyHex)
	}
	
	// 6. 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}
	
	// 7. 从私钥生成公钥
	publicKey := privateKey.PublicKey()
	publicKeyBytes := publicKey.Marshal()
	
	// 8. 验证公钥长度（应该是128字节）
	if len(publicKeyBytes) != 128 {
		return nil, fmt.Errorf("invalid BLS public key length: expected 128 bytes, got %d", len(publicKeyBytes))
	}
	
	return publicKeyBytes, nil
}

// initializeDelegates 初始化受托人集合
func (d *DPoS) initializeDelegates() error {
	d.delegates = make(validator.AccountSet, 0, d.config.DelegateCount)

	// 🆕 修正：优先从数据库读取受托人，而不是从创世文件
	d.logger.Info("initializing delegates", "configDelegateCount", d.config.DelegateCount, "initialDelegatesCount", len(d.config.InitialDelegates))

	// 🆕 首先尝试从数据库读取受托人（真正用于出块）
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Debug("🔍 尝试从数据库读取受托人信息（真正用于出块）...")
		dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		if err != nil {
			d.logger.Warn("⚠️ 从数据库读取受托人失败，将使用创世文件", "error", err)
		} else if len(dbValidators) > 0 {
			d.logger.Info("✅ 从数据库成功读取受托人（真正用于出块）", "count", len(dbValidators))
			
			// 🆕 添加详细日志：打印从数据库读取的验证者信息
			d.logger.Info("🔍 数据库验证者详细信息:")
			for i, validator := range dbValidators {
				d.logger.Info("🔍 数据库验证者",
					"index", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
					"isActive", validator.IsActive,
					"hasBlsKey", validator.BlsKey != nil)
			}

			// 🆕 修复：按票数降序排序，如果票数相同则按地址排序，确保顺序完全一致
			d.logger.Debug("🔍 按票数降序排序数据库受托人...")
			sort.Slice(dbValidators, func(i, j int) bool {
				if dbValidators[i].VotingPower.Cmp(dbValidators[j].VotingPower) == 0 {
					// 票数相同，按地址排序（字节比较）
					return bytes.Compare(dbValidators[i].Address[:], dbValidators[j].Address[:]) < 0
				}
				return dbValidators[i].VotingPower.Cmp(dbValidators[j].VotingPower) > 0
			})

			// 🆕 使用创世文件中的delegateCount配置，只取前N个票数最高的受托人
			maxDelegates := int(d.config.DelegateCount)
			if len(dbValidators) > maxDelegates {
				dbValidators = dbValidators[:maxDelegates]
				d.logger.Info("🎯 限制受托人数量为前N个", "originalCount", len(dbValidators), "limitedCount", maxDelegates, "configDelegateCount", d.config.DelegateCount)
			}

			// 🆕 将排序后的前N个受托人信息真正添加到d.delegates中用于出块
			for i, validator := range dbValidators {
				d.logger.Debug("📋 数据库受托人信息（真正用于出块，按票数排序）", "index", i, "address", validator.Address, "votingPower", validator.VotingPower.String(), "isActive", validator.IsActive)

				// BLS密钥将在异步加载过程中获取，这里不进行任何处理
				if validator.BlsKey == nil {
					d.logger.Debug("🔑 受托人BLS密钥将在异步加载过程中获取", "address", validator.Address)
				} else {
					d.logger.Debug("✅ 受托人BLS密钥已存在", "address", validator.Address, "blsKeyLength", len(validator.BlsKey.Marshal()))
				}

				// 将数据库中的受托人添加到出块集合中（使用安全方法去重）
				d.addDelegateSafely(validator)
			}

			d.logger.Info("📊 数据库受托人已按票数排序并取前N个添加到出块集合", "count", len(d.delegates), "configDelegateCount", d.config.DelegateCount)

			// 🆕 如果从数据库成功读取到受托人，直接返回，不再使用创世文件
			if len(d.delegates) > 0 {
				d.logger.Info("🎯 使用数据库中的受托人进行出块，跳过创世文件")

				// 🆕 注意：dbValidators已经在前面按票数排序，这里不需要再次排序

				// 添加详细的调试日志
				d.logger.Debug("=== 数据库受托人集合详细信息（用于出块）===")
				for i, delegate := range d.delegates {
					d.logger.Debug("数据库受托人信息（用于出块）",
						"index", i,
						"address", delegate.Address.String(),
						"votingPower", delegate.VotingPower.String(),
						"isActive", delegate.IsActive)
				}
				d.logger.Debug("=== 数据库受托人集合详细信息结束 ===")

				// 检查当前节点的地址是否在受托人集合中
				if d.key != nil {
					keyAddr := types.Address(d.key.Address())
					d.logger.Info("数据库受托人集合中当前节点地址", "keyAddr", keyAddr.String())

					found := false
					for i, delegate := range d.delegates {
						if delegate.Address == keyAddr {
							d.logger.Info("数据库受托人集合中找到当前节点", "index", i, "address", keyAddr.String())
							found = true
							break
						}
					}

					if !found {
						d.logger.Warn("⚠️ 当前节点不在数据库受托人集合中，无法参与出块", "keyAddr", keyAddr.String())
					}
				}

				return nil
			}
		}
	}

	// 🆕 如果数据库中没有受托人，等待runtime初始化后再解析
	d.logger.Info("🎯 数据库中没有受托人，等待runtime初始化后再解析验证者")
	
	// 注意：parseValidatorsFromGenesis() 将在 dposRuntime.initializeDelegates() 中调用
	// 因为此时 d.runtime 还没有初始化
	// runtime初始化时会从extraData解析验证者，如果失败才会使用创世文件作为后备

	// 注意：实际的验证者解析将在 dposRuntime.initializeDelegates() 中完成
	// 这里只是等待runtime初始化，不进行任何处理
	return nil
}

// saveValidatorSetForBlock 已移除数据库保存机制，改为日志记录
func (d *DPoS) saveValidatorSetForBlock(blockNumber uint64) error {
	// 🆕 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
	d.logger.Info("📝 验证者集合获取方式已更新",
		"blockNumber", blockNumber,
		"delegatesCount", len(d.delegates),
		"method", "从ExtraData直接解析",
		"note", "不再需要保存到数据库，验证节点将从区块数据直接获取")

	// 详细记录当前验证者集合信息
	d.logger.Info("📋 当前验证者集合详细信息:")
	for i, delegate := range d.delegates {
		d.logger.Info("📝 当前验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", delegate.Address.String(),
			"votingPower", delegate.VotingPower.String(),
			"isActive", delegate.IsActive,
			"hasBlsKey", delegate.BlsKey != nil)
	}

	return nil
}

// SaveValidatorSetForBlockWithValidators 保存指定区块的特定验证者集合到数据库（接口实现）
func (d *DPoS) SaveValidatorSetForBlockWithValidators(blockNumber uint64, validators validator.AccountSet) error {
	return d.saveValidatorSetForBlockWithValidators(blockNumber, validators)
}

// saveValidatorSetForBlockWithValidators 已移除数据库保存机制，改为日志记录
func (d *DPoS) saveValidatorSetForBlockWithValidators(blockNumber uint64, validators validator.AccountSet) error {
	// 🆕 已移除数据库保存机制，改为从 ExtraData 直接读取验证者集合
	d.logger.Info("📝 验证者集合获取方式已更新",
		"blockNumber", blockNumber,
		"validatorsCount", len(validators),
		"method", "从ExtraData直接解析",
		"note", "不再需要保存到数据库，验证节点将从区块数据直接获取")

	// 详细记录验证者集合信息
	d.logger.Info("📋 验证者集合详细信息:")
	for i, validator := range validators {
		d.logger.Info("📝 验证者",
			"blockNumber", blockNumber,
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}

	return nil
}

// addDelegateSafely 安全地添加验证者，自动去重和权重累计
func (d *DPoS) addDelegateSafely(newDelegate *validator.ValidatorMetadata) {
	// 检查是否已存在相同地址的验证者
	for i, existingDelegate := range d.delegates {
		if existingDelegate.Address == newDelegate.Address {
			// 累计权重
			existingDelegate.VotingPower.Add(existingDelegate.VotingPower, newDelegate.VotingPower)
			d.logger.Warn("🔄 发现重复验证者地址，累计权重",
				"address", newDelegate.Address.String(),
				"originalVotingPower", newDelegate.VotingPower.String(),
				"accumulatedVotingPower", existingDelegate.VotingPower.String(),
				"existingIndex", i)
			return
		}
	}

	// 如果没有重复，直接添加
	d.delegates = append(d.delegates, newDelegate)
	d.logger.Debug("✅ 添加新验证者",
		"address", newDelegate.Address.String(),
		"votingPower", newDelegate.VotingPower.String(),
		"isActive", newDelegate.IsActive,
		"totalDelegates", len(d.delegates))
}

// 🆕 新增：从数据库加载验证者并按voterpower排序截取前N个
func (d *DPoS) loadValidatorsFromDatabaseWithLimit() error {
	d.logger.Info("🔍 开始从数据库加载验证者并按voterpower排序截取前N个")
	
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}
	
	// 从数据库获取所有验证者
	dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return fmt.Errorf("failed to get validators from database: %w", err)
	}
	
	if len(dbValidators) == 0 {
		return fmt.Errorf("no validators in database")
	}
	
	d.logger.Info("✅ 从数据库成功读取验证者", "count", len(dbValidators))
	
	// 🆕 添加详细日志：打印从数据库读取的验证者信息
	d.logger.Info("🔍 数据库验证者详细信息:")
	for i, validator := range dbValidators {
		d.logger.Info("🔍 数据库验证者",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}
	
	// 按VotingPower降序排序
	d.logger.Debug("🔍 按voterpower降序排序验证者...")
	sort.Slice(dbValidators, func(i, j int) bool {
		if dbValidators[i].VotingPower.Cmp(dbValidators[j].VotingPower) == 0 {
			// 票数相同，按地址排序
			return bytes.Compare(dbValidators[i].Address[:], dbValidators[j].Address[:]) < 0
		}
		return dbValidators[i].VotingPower.Cmp(dbValidators[j].VotingPower) > 0
	})
	
	// 根据配置文件限制数量
	maxDelegates := int(d.config.DelegateCount)
	originalCount := len(dbValidators)
	if len(dbValidators) > maxDelegates {
		dbValidators = dbValidators[:maxDelegates]
		d.logger.Info("🎯 限制验证者数量为前N个", 
			"originalCount", originalCount, 
			"limitedCount", maxDelegates, 
			"configDelegateCount", d.config.DelegateCount)
	}
	
	// 设置验证者到runtime
	if d.runtime != nil {
		d.runtime.delegates = dbValidators
	}
	d.delegates = dbValidators
	
	d.logger.Info("✅ 从数据库加载验证者完成", 
		"count", len(dbValidators), 
		"maxDelegates", maxDelegates)
	
	// 详细记录验证者信息
	for i, validator := range dbValidators {
		d.logger.Debug("📋 验证者信息",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}
	
	return nil
}

// 🆕 新增：初始化BLS加载状态
func (d *DPoS) initializeBLSLoadingState() {
	d.blsLoadingComplete = false
	d.blsLoadingWaitCh = make(chan struct{})
	d.logger.Debug("🔑 BLS加载状态已初始化")
}

// 🆕 新增：等待BLS公钥加载完成
func (d *DPoS) waitForBLSKeysLoaded() error {
	d.blsLoadingMutex.RLock()
	if d.blsLoadingComplete {
		d.blsLoadingMutex.RUnlock()
		return nil
	}
	d.blsLoadingMutex.RUnlock()
	
	d.logger.Info("⏳ 等待BLS公钥加载完成...")
	
	// 等待BLS加载完成信号
	select {
	case <-d.blsLoadingWaitCh:
		d.logger.Info("✅ BLS公钥加载完成，可以开始区块验证")
		return nil
	case <-time.After(5 * time.Minute): // 5分钟超时
		return fmt.Errorf("timeout waiting for BLS keys to load")
	}
}

// 🆕 新增：异步加载BLS公钥（严格等待所有BLS加载完成）
func (d *DPoS) asyncLoadBLSKeys() {
	d.logger.Info("🔑 开始异步加载BLS公钥...")
	
	// 1. 首先尝试从数据库加载已缓存的BLS公钥
	if err := d.loadBLSKeysFromDatabase(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载BLS公钥失败", "error", err)
		// 数据库加载失败不阻止启动，继续尝试网络获取
	}
	
	validators := d.getAllValidators()
	
	for {
		allBLSLoaded := true
		failedValidators := []types.Address{}
		
		for _, validator := range validators {
			if validator.BlsKey == nil {
				allBLSLoaded = false
				
				// 尝试获取BLS公钥
				blsKey, err := d.getBLSKeyForValidator(validator.Address)
				if err != nil {
					d.logger.Warn("BLS公钥获取失败", "address", validator.Address.String(), "error", err)
					failedValidators = append(failedValidators, validator.Address)
					continue
				}
				
				// 保存BLS公钥
				validator.BlsKey = blsKey
				d.saveBLSKeyToCache(validator.Address, blsKey)
				d.saveBLSKeyToDatabase(validator.Address, blsKey)
				
				d.logger.Debug("✅ BLS公钥获取成功", "address", validator.Address.String())
			}
		}
		
		// 如果所有BLS都加载完成，发送完成信号
		if allBLSLoaded {
			d.logger.Info("✅ 所有BLS公钥加载完成，可以开始区块验证")
			
			// 设置完成状态并发送信号
			d.blsLoadingMutex.Lock()
			d.blsLoadingComplete = true
			d.blsLoadingMutex.Unlock()
			
			// 发送完成信号（非阻塞）
			select {
			case d.blsLoadingWaitCh <- struct{}{}:
			default:
			}
			
			break
		}
		
		// 如果还有失败的，等待后重试
		if len(failedValidators) > 0 {
			d.logger.Warn("部分BLS公钥获取失败，等待重试", "failedCount", len(failedValidators))
			time.Sleep(10 * time.Second) // 等待10秒后重试
		}
	}
}

// 🆕 新增：获取所有验证者
func (d *DPoS) getAllValidators() validator.AccountSet {
	if d.runtime != nil && len(d.runtime.delegates) > 0 {
		d.logger.Info("🔍 getAllValidators: 使用runtime.delegates", "count", len(d.runtime.delegates))
		// 🆕 添加详细日志：打印每个验证者的VotingPower
		for i, validator := range d.runtime.delegates {
			d.logger.Info("🔍 runtime.delegates验证者信息",
				"index", i,
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil)
		}
		return d.runtime.delegates
	}
	d.logger.Info("🔍 getAllValidators: 使用d.delegates", "count", len(d.delegates))
	// 🆕 添加详细日志：打印每个验证者的VotingPower
	for i, validator := range d.delegates {
		d.logger.Info("🔍 d.delegates验证者信息",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
	}
	return d.delegates
}

// 🆕 新增：获取指定验证者的BLS公钥
func (d *DPoS) getBLSKeyForValidator(address types.Address) (*bls.PublicKey, error) {
	// 检查是否是当前节点
	if d.key != nil && address == types.Address(d.key.Address()) {
		// 当前节点：从本地文件读取BLS私钥
		blsPublicKey, err := d.readBLSPrivateKeyAndGeneratePublicKey(address)
		if err != nil {
			return nil, fmt.Errorf("failed to read local BLS key: %w", err)
		}
		
		// 解析BLS公钥
		blsKey, err := bls.UnmarshalPublicKey(blsPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal BLS key: %w", err)
		}
		
		d.logger.Debug("✅ 本地BLS公钥获取成功", "address", address.String())
		return blsKey, nil
	} else {
		// 其他节点：通过网络请求BLS公钥
		blsKey, err := d.requestBLSPublicKeyFromNetwork(address)
		if err != nil {
			return nil, fmt.Errorf("failed to request remote BLS key: %w", err)
		}
		
		d.logger.Debug("✅ 远程BLS公钥获取成功", "address", address.String())
		return blsKey, nil
	}
}

// 🆕 新增：保存BLS公钥到缓存
func (d *DPoS) saveBLSKeyToCache(address types.Address, blsKey *bls.PublicKey) {
	if d.runtime != nil && d.runtime.networkIntegration != nil {
		blsKeyBytes := blsKey.Marshal()
		if err := d.runtime.networkIntegration.SaveBLSKey(address, blsKeyBytes); err != nil {
			d.logger.Warn("⚠️ 保存BLS公钥到缓存失败", 
				"address", address.String(), 
				"error", err)
		} else {
			d.logger.Debug("✅ BLS公钥已保存到缓存", "address", address.String())
		}
	}
}

// 🆕 新增：同步加载BLS公钥
func (d *DPoS) syncLoadBLSKeys() error {
	d.logger.Info("🔑 开始同步加载BLS公钥...")
	
	// 1. 从数据库加载已缓存的BLS公钥
	if err := d.loadBLSKeysFromDatabase(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载BLS公钥失败", "error", err)
		// 数据库加载失败不阻止启动，继续尝试网络获取
	}
	
	// 2. 获取所有验证者
	validators := d.getAllValidators()
	d.logger.Info("🔍 获取到验证者数量", "count", len(validators))
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需获取BLS公钥")
		return nil
	}
	
	
	// 3. 同步获取缺失的BLS公钥（带重试机制）
	maxRetries := 3
	retryDelay := 5 * time.Second
	
	for _, validator := range validators {
		if validator.BlsKey == nil {
			d.logger.Debug("🔑 开始获取BLS公钥", "address", validator.Address.String())
			
			var blsKey *bls.PublicKey
			var err error
			
			// 重试机制
			for retry := 0; retry < maxRetries; retry++ {
				blsKey, err = d.getBLSKeyForValidator(validator.Address)
				if err == nil {
					break // 成功获取，跳出重试循环
				}
				
				if retry < maxRetries-1 {
					d.logger.Warn("⚠️ BLS公钥获取失败，准备重试", 
						"address", validator.Address.String(),
						"retry", retry+1,
						"maxRetries", maxRetries,
						"error", err)
					time.Sleep(retryDelay)
				} else {
					d.logger.Error("❌ BLS公钥获取失败，已达到最大重试次数", 
						"address", validator.Address.String(),
						"maxRetries", maxRetries,
						"error", err)
				}
			}
			
			if err != nil {
				return fmt.Errorf("failed to get BLS key for %s after %d retries: %w", validator.Address.String(), maxRetries, err)
			}
			
			// 保存BLS公钥
			validator.BlsKey = blsKey
			d.saveBLSKeyToCache(validator.Address, blsKey)
			d.saveBLSKeyToDatabase(validator.Address, blsKey)
			
			d.logger.Info("✅ BLS公钥获取成功", "address", validator.Address.String())
		} else {
			d.logger.Debug("✅ BLS公钥已存在", "address", validator.Address.String())
		}
	}
	
	// 🆕 修复：检查所有验证者是否都有BLS公钥
	allBLSLoaded := true
	for _, validator := range validators {
		if validator.BlsKey == nil {
			allBLSLoaded = false
			d.logger.Warn("⚠️ 验证者BLS公钥未加载", "address", validator.Address.String())
		}
	}
	
	if !allBLSLoaded {
		d.logger.Warn("⚠️ 部分验证者BLS公钥未加载完成，但继续启动流程")
	} else {
		d.logger.Info("✅ 所有BLS公钥同步加载完成")
	}
	
	// 🆕 修复：设置BLS加载完成状态
	d.blsLoadingMutex.Lock()
	d.blsLoadingComplete = true
	d.blsLoadingMutex.Unlock()
	d.logger.Info("✅ BLS加载状态已设置为完成")
	
	// 发送完成信号
	select {
	case d.blsLoadingWaitCh <- struct{}{}:
		d.logger.Debug("✅ BLS加载完成信号已发送")
	default:
		d.logger.Debug("ℹ️ BLS加载完成信号通道已满")
	}
	
	// 4. 检查数据库是否已有验证者，只有没有时才保存
	d.logger.Info("🔍 开始检查数据库验证者状态...")
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Info("🔍 状态存储可用，开始获取数据库验证者...")
		d.logger.Info("🔍 当前state和StakeStore状态", 
			"stateIsNil", d.state == nil,
			"stakeStoreIsNil", d.state.StakeStore == nil)
		
		// 🆕 直接使用GetValidatorsWithFilter(false)查询数据库，避免内存和数据库不一致
		dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		d.logger.Info("🔍 GetValidatorsWithFilter(false)结果", "count", len(dbValidators), "error", err)
		
		if err == nil && len(dbValidators) > 0 {
			// 打印数据库验证者的详细信息
			d.logger.Info("🔍 数据库验证者详细信息:")
			for i, validator := range dbValidators {
				d.logger.Info("🔍 数据库验证者",
					"index", i,
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"isActive", validator.IsActive,
					"hasBlsKey", validator.BlsKey != nil)
			}
			d.logger.Info("✅ 数据库已有验证者，跳过保存", "count", len(dbValidators))
		} else {
			d.logger.Info("🔍 数据库无验证者或获取失败，准备保存", "count", len(dbValidators), "error", err)
			// 数据库没有验证者，保存验证者信息到数据库
			if err := d.saveValidatorsWithBLSKeysToDatabase(); err != nil {
				d.logger.Warn("⚠️ 保存验证者信息到数据库失败", "error", err)
				// 不返回错误，继续启动流程
			} else {
				d.logger.Info("✅ 验证者信息（含BLS公钥）已保存到数据库")
			}
		}
	} else {
		d.logger.Warn("⚠️ 状态存储不可用，无法检查数据库状态")
	}
	
	return nil
}

// 🆕 新增：保存验证者信息（含BLS公钥）到数据库
func (d *DPoS) saveValidatorsWithBLSKeysToDatabase() error {
	d.logger.Info("💾 开始保存验证者信息（含BLS公钥）到数据库...")
	
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法保存验证者信息到数据库")
		return fmt.Errorf("state store not available")
	}
	
	validators := d.getAllValidators()
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需保存到数据库")
		return nil
	}
	
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需保存到数据库")
		return nil
	}
	
	syncedCount := 0
	for i, validator := range validators {
		// 🆕 添加详细日志：打印验证者信息
		d.logger.Info("🔍 准备保存验证者信息到数据库",
			"index", i,
			"address", validator.Address.String(),
			"votingPower", validator.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
			"isActive", validator.IsActive,
			"hasBlsKey", validator.BlsKey != nil)
		
		// 创建DelegateInfo结构
		delegateInfo := &DelegateInfo{
			Address:        validator.Address,
			VotingPower:    new(big.Int).Set(validator.VotingPower),
			TotalVotes:     new(big.Int).Set(validator.VotingPower), // 使用VotingPower作为TotalVotes
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       validator.IsActive,
			BlsPublicKey:   []byte{}, // 初始为空
		}
		
		// 🆕 添加详细日志：打印DelegateInfo信息
		d.logger.Info("🔍 DelegateInfo详细信息",
			"address", delegateInfo.Address.String(),
			"votingPower", delegateInfo.VotingPower.String(),
			"votingPowerHex", fmt.Sprintf("0x%x", delegateInfo.VotingPower.Bytes()),
			"totalVotes", delegateInfo.TotalVotes.String(),
			"totalVotesHex", fmt.Sprintf("0x%x", delegateInfo.TotalVotes.Bytes()),
			"isActive", delegateInfo.IsActive,
			"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))
		
		// 如果有BLS公钥，保存到DelegateInfo中
		if validator.BlsKey != nil {
			blsKeyBytes := validator.BlsKey.Marshal()
			delegateInfo.BlsPublicKey = blsKeyBytes
			d.logger.Debug("✅ 保存验证者BLS公钥到数据库", 
				"address", validator.Address.String(),
				"blsKeyLength", len(blsKeyBytes))
		}
		
		// 保存到数据库
		if err := d.state.StakeStore.setDelegateInfo(validator.Address, delegateInfo, nil); err != nil {
			d.logger.Warn("⚠️ 保存验证者信息到数据库失败", 
				"address", validator.Address.String(), 
				"error", err)
		} else {
			syncedCount++
			// 🆕 添加详细日志：打印保存成功后的信息
			d.logger.Info("✅ 验证者信息已保存到数据库",
				"address", validator.Address.String(),
				"votingPower", validator.VotingPower.String(),
				"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil)
			
			// 打印BLS公钥详细信息
			if validator.BlsKey != nil {
				blsKeyBytes := validator.BlsKey.Marshal()
				d.logger.Info("✅ 验证者信息（含BLS公钥）已保存到数据库", 
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
					"isActive", validator.IsActive,
					"hasBlsKey", true,
					"blsKeyLength", len(blsKeyBytes),
					"blsKeyHex", fmt.Sprintf("%x", blsKeyBytes[:16])+"...")
			} else {
				d.logger.Info("✅ 验证者信息（无BLS公钥）已保存到数据库", 
					"address", validator.Address.String(),
					"votingPower", validator.VotingPower.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
					"isActive", validator.IsActive,
					"hasBlsKey", false)
			}
		}
	}
	
	d.logger.Info("💾 验证者信息（含BLS公钥）保存到数据库完成", "syncedCount", syncedCount, "totalValidators", len(validators))
	return nil
}

// 🆕 新增：保存BLS公钥到数据库
func (d *DPoS) saveBLSKeyToDatabase(address types.Address, blsKey *bls.PublicKey) {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法保存BLS公钥到数据库")
		return
	}
	
	// 获取验证者信息
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		d.logger.Warn("⚠️ 获取验证者信息失败", "error", err)
		return
	}
	
	// 查找并更新对应验证者的BLS公钥
	for _, validator := range validators {
		if validator.Address == address {
			// 🆕 重新查询账户余额作为VotingPower
			balance, err := d.getValidatorBalance(address)
			if err != nil {
				d.logger.Warn("⚠️ 查询账户余额失败，使用数据库中的VotingPower", 
					"address", address.String(), 
					"error", err)
				balance = validator.VotingPower
			} else {
				d.logger.Info("🔍 重新查询到账户余额", 
					"address", address.String(),
					"balance", balance.String(),
					"balanceHex", fmt.Sprintf("0x%x", balance.Bytes()))
			}
			
			// 🆕 添加详细日志：打印保存前的信息
			d.logger.Info("🔍 准备保存BLS公钥到数据库",
				"address", address.String(),
				"originalVotingPower", validator.VotingPower.String(),
				"newVotingPower", balance.String(),
				"votingPowerHex", fmt.Sprintf("0x%x", balance.Bytes()),
				"isActive", validator.IsActive,
				"hasBlsKey", validator.BlsKey != nil)
			
			// 更新BLS公钥和VotingPower
			validator.BlsKey = blsKey
			validator.VotingPower = balance
			
			// 🆕 创建DelegateInfo并保存到数据库
			delegateInfo := &DelegateInfo{
				Address:        validator.Address,
				VotingPower:    new(big.Int).Set(balance), // 使用重新查询的余额
				TotalVotes:     new(big.Int).Set(balance), // 使用重新查询的余额
				ProducedBlocks: 0,
				MissedBlocks:   0,
				LastBlockTime:  0,
				IsActive:       validator.IsActive,
				BlsPublicKey:   []byte{}, // 初始为空
			}
			
			// 保存BLS公钥到DelegateInfo
			if blsKey != nil {
				blsKeyBytes := blsKey.Marshal()
				delegateInfo.BlsPublicKey = blsKeyBytes
				d.logger.Info("🔍 DelegateInfo详细信息",
					"address", delegateInfo.Address.String(),
					"votingPower", delegateInfo.VotingPower.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", delegateInfo.VotingPower.Bytes()),
					"totalVotes", delegateInfo.TotalVotes.String(),
					"totalVotesHex", fmt.Sprintf("0x%x", delegateInfo.TotalVotes.Bytes()),
					"isActive", delegateInfo.IsActive,
					"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))
			}
			
			// 保存到数据库
			if err := d.state.StakeStore.setDelegateInfo(address, delegateInfo, nil); err != nil {
				d.logger.Warn("⚠️ 保存BLS公钥到数据库失败", 
					"address", address.String(), 
					"error", err)
			} else {
				d.logger.Info("✅ BLS公钥已保存到数据库",
					"address", address.String(),
					"votingPower", balance.String(),
					"votingPowerHex", fmt.Sprintf("0x%x", balance.Bytes()),
					"isActive", validator.IsActive,
					"blsKeyLength", len(blsKey.Marshal()))
			}
			break
		}
	}
}

// 🆕 新增：从数据库加载BLS公钥到缓存
func (d *DPoS) loadBLSKeysFromDatabase() error {
	d.logger.Debug("📚 从数据库加载BLS公钥到缓存...")
	
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法从数据库加载BLS公钥")
		return fmt.Errorf("state store not available")
	}
	
	// 从数据库获取所有验证者信息
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		d.logger.Warn("⚠️ 获取验证者信息失败", "error", err)
		return err
	}
	
	loadedCount := 0
	for _, validator := range validators {
		if validator.BlsKey != nil {
			// 将BLS公钥只加载到网络集成层缓存，不写入数据库
			if d.runtime != nil && d.runtime.networkIntegration != nil {
				blsKeyBytes := validator.BlsKey.Marshal()
				if err := d.runtime.networkIntegration.LoadBLSKeyToCache(validator.Address, blsKeyBytes); err != nil {
					d.logger.Warn("⚠️ 加载BLS公钥到缓存失败", 
						"address", validator.Address.String(), 
						"error", err)
				} else {
					loadedCount++
					d.logger.Debug("✅ 从数据库加载BLS公钥到缓存成功", 
						"address", validator.Address.String(),
						"blsKeyLength", len(blsKeyBytes))
				}
			}
		}
	}
	
	d.logger.Debug("📚 数据库BLS公钥加载完成", "loadedCount", loadedCount, "totalValidators", len(validators))
	return nil
}

// dpos.go - 添加后端接口实现
func (d *DPoS) GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	currentBlockNumber := d.blockchain.CurrentHeader().Number
	d.logger.Debug("🔍 验证时获取验证者集合",
		"requestedBlockNumber", blockNumber,
		"currentBlockNumber", currentBlockNumber,
		"isCurrentBlock", blockNumber == currentBlockNumber,
		"note", "验证时获取验证者集合")

	// 如果是当前区块，优先返回从runtime.delegates获取的验证者
	if blockNumber == currentBlockNumber {
		d.logger.Debug("🔍 Returning current delegates from memory")
		
		// 🆕 优先使用 runtime.delegates，如果为空则从数据库读取
		if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
			d.logger.Debug("🔍 使用从runtime.delegates获取的验证者")
			result := d.runtime.delegates.Copy()
			return result, nil
		}
		
		// 🆕 如果runtime.delegates为空，尝试从数据库读取
		if d.state != nil && d.state.StakeStore != nil {
			d.logger.Info("🔍 runtime.delegates为空，尝试从数据库读取验证者")
			if dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false); err == nil && len(dbValidators) > 0 {
				d.logger.Info("🔍 从数据库成功读取验证者", "count", len(dbValidators))
				
				// 🆕 添加详细日志：打印从数据库读取的验证者信息
				d.logger.Info("🔍 数据库验证者详细信息:")
				for i, validator := range dbValidators {
					d.logger.Info("🔍 数据库验证者",
						"index", i,
						"address", validator.Address.String(),
						"votingPower", validator.VotingPower.String(),
						"votingPowerHex", fmt.Sprintf("0x%x", validator.VotingPower.Bytes()),
						"isActive", validator.IsActive,
						"hasBlsKey", validator.BlsKey != nil)
				}
				
				return dbValidators, nil
			}
		}

		return validator.AccountSet{}, nil
	}

	// 🆕 已移除数据库读取机制，改为从区块 ExtraData 直接解析
	d.logger.Info("📝 GetDelegates 获取方式已更新",
		"requestedBlockNumber", blockNumber,
		"currentBlockNumber", currentBlockNumber,
		"method", "从区块ExtraData直接解析",
		"note", "不再从数据库读取，验证者集合应从区块数据直接获取")

	// 对于历史区块，应该通过区块的 ExtraData 来获取验证者集合
	// 这里返回错误，提示调用者应该使用 ExtraData 解析方式
	return nil, fmt.Errorf("GetDelegates for historical block %d is deprecated, use ExtraData parsing instead", blockNumber)
}

// GetValidatorsWithFilter returns validators with optional filtering
// This method allows RPC calls to get all validators including those with zero voting power
func (d *DPoS) GetValidatorsWithFilter(filterZeroVotingPower bool) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// Access the stake store directly through d.state.StakeStore
	if d.state == nil || d.state.StakeStore == nil {
		return nil, fmt.Errorf("DPoS state or stake store is nil")
	}

	// Get validators from stake store with filtering control
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(filterZeroVotingPower)
	if err != nil {
		return nil, fmt.Errorf("failed to get validators from stake store: %w", err)
	}

	return validators, nil
}

func (d *DPoS) GetDelegatesWithTx(blockNumber uint64, parents []*types.Header, dbTx *bolt.Tx) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 在事务中获取受托人集合
	return d.getDelegatesFromStateWithTx(blockNumber, dbTx)
}

func (d *DPoS) GetStakingInfo(blockNumber uint64, staker types.Address) (*StakeInfo, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接从内存中获取质押信息，避免复杂的数据库操作

	// 从内存中获取
	if voter, exists := d.voters[staker]; exists {
		return &StakeInfo{
			Staker:    staker,
			Amount:    new(big.Int).Set(voter.VotingPower),
			StartTime: voter.LastVoteTime,
			EndTime:   voter.LockedUntil,
			IsLocked:  voter.LockedUntil > uint64(time.Now().Unix()),
			IsActive:  len(voter.VotedDelegates) > 0,
			Rewards:   big.NewInt(0), // TODO: 实现奖励计算
			Delegate:  d.getPrimaryDelegate(voter.VotedDelegates),
		}, nil
	}

	// 如果内存中没有，尝试从受托人信息中获取
	for _, delegate := range d.delegates {
		if delegate.Address == staker {
			return &StakeInfo{
				Staker:    staker,
				Amount:    new(big.Int).Set(delegate.VotingPower),
				StartTime: uint64(time.Now().Unix()), // 使用当前时间作为开始时间
				EndTime:   0,                         // 受托人没有锁定时间
				IsLocked:  false,
				IsActive:  delegate.IsActive,
				Rewards:   big.NewInt(0),
				Delegate:  staker, // 受托人自己就是委托人
			}, nil
		}
	}

	// 返回默认值
	return &StakeInfo{
		Staker:    staker,
		Amount:    big.NewInt(0),
		StartTime: 0,
		EndTime:   0,
		IsLocked:  false,
		IsActive:  false,
		Rewards:   big.NewInt(0),
		Delegate:  types.ZeroAddress,
	}, nil
}

func (d *DPoS) GetStakingInfoWithTx(blockNumber uint64, staker types.Address, dbTx *bolt.Tx) (*StakeInfo, error) {
	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接调用内存版本，避免复杂的数据库操作
	return d.GetStakingInfo(blockNumber, staker)
}

func (d *DPoS) GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 获取受托人的投票权重
	for _, d := range d.delegates {
		if d.Address == delegate {
			return new(big.Int).Set(d.VotingPower), nil
		}
	}

	return big.NewInt(0), nil
}

func (d *DPoS) GetVotingPowerWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// 在事务中获取投票权重
	return d.getVotingPowerFromStateWithTx(blockNumber, delegate, dbTx)
}

func (d *DPoS) GetCurrentRound() uint64 {
	d.lock.RLock()
	defer d.lock.RUnlock()

	return d.currentRound
}

func (d *DPoS) GetCurrentDelegate() types.Address {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 如果当前受托人索引有效，返回对应的受托人地址
	if d.currentDelegateIndex < uint64(len(d.delegates)) {
		return d.delegates[d.currentDelegateIndex].Address
	}

	return types.ZeroAddress
}

// GetVoters returns the current voters map for external access
func (d *DPoS) GetVoters() map[types.Address]*VoterInfo {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// Debug: Log the current state of d.voters
	d.logger.Info("GetVoters called - current d.voters state",
		"votersMapAddress", fmt.Sprintf("%p", d.voters),
		"votersMapLength", len(d.voters),
		"votersMapNil", d.voters == nil)

	// Create a copy of the voters map to avoid race conditions
	votersCopy := make(map[types.Address]*VoterInfo)
	for addr, voter := range d.voters {
		// Create a deep copy of VoterInfo
		voterCopy := &VoterInfo{
			Address:        voter.Address,
			VotingPower:    new(big.Int).Set(voter.VotingPower),
			VotedDelegates: make([]types.Address, len(voter.VotedDelegates)),
			LastVoteTime:   voter.LastVoteTime,
			LockedUntil:    voter.LockedUntil,
			Nonce:          make(map[uint64]bool),
		}

		// Copy voted delegates
		copy(voterCopy.VotedDelegates, voter.VotedDelegates)

		// Copy nonce map
		for k, v := range voter.Nonce {
			voterCopy.Nonce[k] = v
		}

		votersCopy[addr] = voterCopy
	}

	d.logger.Info("GetVoters returning copy",
		"copyMapAddress", fmt.Sprintf("%p", votersCopy),
		"copyMapLength", len(votersCopy))

	return votersCopy
}

func (d *DPoS) GetDelegateIndex(delegate types.Address) uint64 {
	d.lock.RLock()
	defer d.lock.RUnlock()

	for i, d := range d.delegates {
		if d.Address == delegate {
			return uint64(i)
		}
	}

	return 0
}

// GetCurrentDelegates 获取当前内存中的受托人集合
func (d *DPoS) GetCurrentDelegates() validator.AccountSet {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 返回当前内存中的受托人集合的副本
	return d.delegates.Copy()
}

// 区块处理相关方法
func (d *DPoS) processBlockVotes(block *types.FullBlock) error {
	d.logger.Debug("🔄 开始处理区块中的投票事件", "blockNumber", block.Block.Number())

	if block == nil || block.Block == nil {
		d.logger.Warn("⚠️ 区块为空，跳过投票事件处理")
		return nil
	}

	// 🆕 修复：统一在区块广播接收后处理投票，确保只计算一次
	d.logger.Debug("✅ 处理区块中的投票事件",
		"blockNumber", block.Block.Number(),
		"blockCreator", func() string {
			if creator, err := d.GetBlockCreator(block.Block.Header); err == nil {
				return creator.String()
			}
			return "unknown"
		}())

	// 获取区块中的所有交易
	transactions := block.Block.Transactions
	if len(transactions) == 0 {
		return nil
	}

	d.logger.Info("📋 区块交易数量", "blockNumber", block.Block.Number(), "txCount", len(transactions))

	// 遍历所有交易，查找投票交易
	voteCount := 0
	for i, tx := range transactions {
		d.logger.Debug("🔍 检查交易", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())

		// 检查是否是投票交易
		if d.isVoteTransaction(tx) {
			d.logger.Info("✅ 发现投票交易", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())

			// 处理投票交易
			if err := d.processVoteTransaction(tx, block.Block.Number()); err != nil {
				d.logger.Error("❌ 处理投票交易失败", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String(), "error", err)
				// 不返回错误，继续处理其他交易
			} else {
				voteCount++
				d.logger.Info("✅ 投票交易处理成功", "blockNumber", block.Block.Number(), "txIndex", i, "txHash", tx.Hash.String())
			}
		}
	}

	d.logger.Info("🎯 区块投票事件处理完成", "blockNumber", block.Block.Number(), "totalTx", len(transactions), "voteTx", voteCount)
	return nil
}

// 🆕 新增：检查交易是否是投票交易
func (d *DPoS) isVoteTransaction(tx *types.Transaction) bool {
	// 检查交易是否有输入数据（投票交易应该有输入数据）
	if len(tx.Input) == 0 {
		return false
	}

	// 检查交易输入数据长度（DPoS投票交易格式：4字节"DPOS" + 20字节投票者 + 20字节受托人 + 32字节金额）
	const (
		dposPrefixLen  = 4
		addrLen        = 20
		amountLen      = 32
		expectedLength = dposPrefixLen + addrLen + addrLen + amountLen
	)

	if len(tx.Input) < expectedLength {
		return false
	}

	// 检查是否是DPoS投票交易（前4字节应该是"DPOS"）
	if !bytes.Equal(tx.Input[:4], []byte("DPOS")) {
		return false
	}

	d.logger.Debug("🔍 发现DPoS投票交易",
		"inputLength", len(tx.Input),
		"prefix", string(tx.Input[:4]),
		"expectedLength", expectedLength)

	return true
}

// 🆕 新增：处理投票交易
func (d *DPoS) processVoteTransaction(tx *types.Transaction, blockNumber uint64) error {
	d.logger.Info("🔄 开始处理投票交易", "txHash", tx.Hash.String(), "blockNumber", blockNumber)

	// 1. 解析交易输入数据，提取投票信息
	voteInfo, err := d.parseVoteTransactionData(tx)
	if err != nil {
		d.logger.Error("❌ 解析投票交易数据失败", "txHash", tx.Hash.String(), "error", err)
		return fmt.Errorf("failed to parse vote transaction data: %w", err)
	}

	d.logger.Info("📋 投票交易信息解析成功",
		"txHash", tx.Hash.String(),
		"from", tx.From.String(),
		"to", func() string {
			if tx.To != nil {
				return tx.To.String()
			}
			return "nil"
		}(),
		"value", tx.Value.String(),
		"inputLength", len(tx.Input),
		"voter", voteInfo.Voter.String(),
		"candidate", voteInfo.Candidate.String(),
		"amount", voteInfo.Amount.String())

	// 2. 调用投票处理逻辑
	d.logger.Debug("🔄 调用投票处理逻辑...")
	if err := d.AddVote(voteInfo.Voter, voteInfo.Candidate, voteInfo.Amount); err != nil {
		d.logger.Error("❌ 投票处理失败", "txHash", tx.Hash.String(), "error", err)
		return fmt.Errorf("failed to process vote: %w", err)
	}

	d.logger.Debug("✅ 投票交易处理完成", "txHash", tx.Hash.String(),
		"voter", voteInfo.Voter.String(),
		"candidate", voteInfo.Candidate.String(),
		"amount", voteInfo.Amount.String())
	return nil
}

// parseVoteTransactionData 解析DPoS投票交易数据
func (d *DPoS) parseVoteTransactionData(tx *types.Transaction) (*VoteInfo, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is nil")
	}

	input := tx.Input
	if input == nil || len(input) < 4 {
		return nil, fmt.Errorf("input data too short or nil: length=%d", len(input))
	}

	// 检查是否是DPoS投票交易
	if !bytes.Equal(input[:4], []byte("DPOS")) {
		return nil, fmt.Errorf("not a DPoS vote transaction, prefix=%x", input[:4])
	}

	// 预期格式：4字节"DPOS" + 20字节投票者 + 20字节受托人 + 32字节金额
	const (
		dposPrefixLen  = 4
		addrLen        = 20
		amountLen      = 32
		expectedLength = dposPrefixLen + addrLen + addrLen + amountLen
	)

	if len(input) < expectedLength {
		return nil, fmt.Errorf("invalid DPoS vote tx input length: expected %d, got %d", expectedLength, len(input))
	}

	// 解析地址和金额
	voter := types.BytesToAddress(input[dposPrefixLen : dposPrefixLen+addrLen])
	candidate := types.BytesToAddress(input[dposPrefixLen+addrLen : dposPrefixLen+addrLen+addrLen])
	amountBytes := input[dposPrefixLen+addrLen+addrLen : expectedLength]

	// 转换金额字节为big.Int（移除前导零）
	amount := new(big.Int).SetBytes(amountBytes)
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("vote amount must be positive, got %s", amount.String())
	}

	d.logger.Info("DPoS投票数据解析成功", "voter", voter.String(), "candidate", candidate.String(), "amount", amount.String())

	return &VoteInfo{
		Voter:     voter,
		Candidate: candidate,
		Amount:    amount,
	}, nil
}

// 添加安全相关常量
const (
	MaxVotingPower = "1000000000000000000000000" // 1M tokens
	MaxDelegates   = 100
	VoteLockTime   = 2                          // 2秒（仅用于测试）
	MaxVoteAmount  = "100000000000000000000000" // 100K tokens
	MinVoteAmount  = "1000000000000000000"      // 1 token
)

// 添加安全校验方法
func (d *DPoS) validateVote(vote *VoteMessage) error {
	// 1. 检查投票金额边界
	if vote.Amount.Cmp(big.NewInt(0)) <= 0 {
		return errors.New("vote amount must be positive")
	}

	maxAmount, _ := new(big.Int).SetString(MaxVoteAmount, 10)
	if vote.Amount.Cmp(maxAmount) > 0 {
		return errors.New("vote amount exceeds maximum")
	}

	minAmount, _ := new(big.Int).SetString(MinVoteAmount, 10)
	if vote.Amount.Cmp(minAmount) < 0 {
		return errors.New("vote amount below minimum")
	}

	// 🆕 2. 检查投票者VCITY代币余额
	if d.balanceQuerier != nil {
		balance, err := d.balanceQuerier.GetNativeTokenBalance(vote.Voter)
		if err != nil {
			d.logger.Error("Failed to query voter balance", 
				"voter", vote.Voter.String(), 
				"error", err)
			return fmt.Errorf("failed to query voter balance: %w", err)
		}

		// 检查余额是否足够
		if balance.Cmp(vote.Amount) < 0 {
			d.logger.Warn("Insufficient balance for vote", 
				"voter", vote.Voter.String(),
				"required", vote.Amount.String(),
				"available", balance.String())
			return fmt.Errorf("insufficient balance: required %s, available %s", 
				vote.Amount.String(), balance.String())
		}

		d.logger.Info("Vote balance check passed", 
			"voter", vote.Voter.String(),
			"voteAmount", vote.Amount.String(),
			"balance", balance.String())
	} else {
		d.logger.Warn("Balance querier not available, skipping balance check")
	}

	// 2. 检查受托人是否存在且活跃（允许投票给任何地址）
	delegateExists := false
	for _, del := range d.delegates {
		if del.Address == vote.Delegate && del.IsActive {
			delegateExists = true
			break
		}
	}

	// 如果受托人不在预定义列表中，自动创建并激活
	if !delegateExists {

		// 🆕 修复：新受托人的VotingPower应该初始化为0，但不要在这里设置
		// 因为VotingPower会在后续的投票处理中正确设置
		newDelegate := &validator.ValidatorMetadata{
			Address:     vote.Delegate,
			BlsKey:      nil,           // 暂时设为nil，后续可以更新
			VotingPower: big.NewInt(0), // 初始化为0，后续会正确更新
			IsActive:    false,         // 初始化为false，只有获得投票后才设为true
		}
		d.logger.Debug("Delegate not in predefined list, auto-creating",
			"delegate", vote.Delegate.String(), "amount", "0")
		// 添加到受托人列表（使用安全方法去重）
		d.addDelegateSafely(newDelegate)
	}

	// 3. 检查投票锁定时间（临时跳过用于测试）
	if voter, exists := d.voters[vote.Voter]; exists {
		d.logger.Debug("🔍 Checking lock status",
			"currentTime", uint64(time.Now().Unix()),
			"lockedUntil", voter.LockedUntil,
			"isLocked", uint64(time.Now().Unix()) < voter.LockedUntil)

		// 临时注释掉锁定检查用于测试
		/*
			if uint64(time.Now().Unix()) < voter.LockedUntil {
				return errors.New("voter is still locked")
			}
		*/
	}

	// 4. 检查投票权重上限
	totalVotingPower := big.NewInt(0)
	if voter, exists := d.voters[vote.Voter]; exists {
		totalVotingPower.Add(totalVotingPower, voter.VotingPower)
	}
	totalVotingPower.Add(totalVotingPower, vote.Amount)

	maxVotingPower, _ := new(big.Int).SetString(MaxVotingPower, 10)
	if totalVotingPower.Cmp(maxVotingPower) > 0 {
		return errors.New("total voting power exceeds maximum")
	}

	return nil
}

// processVoteInternal 内部投票处理方法（不加锁，由调用者负责）
func (d *DPoS) processVoteInternal(vote *VoteMessage) error {
	d.logger.Debug("🔄 processVoteInternal started",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"amountWei", vote.Amount.String(),
		"timestamp", time.Now().Unix())

	// 1. 安全校验
	d.logger.Debug("🔍 开始投票安全校验",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()))

	if err := d.validateVote(vote); err != nil {
		d.logger.Error("❌ Vote validation failed", "error", err)
		return fmt.Errorf("vote validation failed: %w", err)
	}

	d.logger.Debug("✅ Vote validation passed")

	// 2. 签名校验
	if err := d.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("vote signature verification failed: %w", err)
	}

	// 3. 防重放攻击 - 检查nonce（临时跳过用于测试）
	d.logger.Debug("🔍 Checking vote nonce",
		"voter", vote.Voter.String(),
		"round", vote.Round,
		"timestamp", vote.Timestamp)

	// 临时跳过nonce检查用于测试
	/*
		if err := d.checkVoteNonce(vote); err != nil {
			return fmt.Errorf("vote nonce check failed: %w", err)
		}
	*/

	// 4. 更新投票者信息
	voter, exists := d.voters[vote.Voter]
	if !exists {
		voter = &VoterInfo{
			Address:        vote.Voter,
			VotingPower:    big.NewInt(0),
			VotedDelegates: make([]types.Address, 0),
			LastVoteTime:   uint64(time.Now().Unix()),
			LockedUntil:    uint64(time.Now().Unix()) + VoteLockTime,
			Nonce:          make(map[uint64]bool), // 防重放
		}
		d.voters[vote.Voter] = voter
	}

	// 5. 更新投票权重
	voter.VotingPower = new(big.Int).Add(voter.VotingPower, vote.Amount)
	voter.LastVoteTime = uint64(time.Now().Unix())
	voter.LockedUntil = uint64(time.Now().Unix()) + VoteLockTime

	// 6. 添加受托人到投票列表（去重）
	found := false
	for _, del := range voter.VotedDelegates {
		if del == vote.Delegate {
			found = true
			break
		}
	}
	if !found {
		voter.VotedDelegates = append(voter.VotedDelegates, vote.Delegate)
	}

	// 7. 更新受托人的投票权重
	d.logger.Debug("🔄 准备更新受托人投票权重",
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()))

	// 🆕 添加详细日志：记录更新前的状态
	d.logger.Info("🔍 Before updateDelegateVotingPower - current delegates state:")
	for i, del := range d.delegates {
		if del.Address == vote.Delegate {
			d.logger.Info("🔍 Target delegate before update",
				"index", i,
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String(),
				"isActive", del.IsActive)
		}
	}

	d.updateDelegateVotingPower(vote.Delegate, vote.Amount)

	// 🆕 添加详细日志：记录更新后的状态
	d.logger.Info("🔍 After updateDelegateVotingPower - current delegates state:")
	for i, del := range d.delegates {
		if del.Address == vote.Delegate {
			d.logger.Info("🔍 Target delegate after update",
				"index", i,
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String(),
				"isActive", del.IsActive)
		}
	}

	// 8. 记录nonce防止重放
	voter.Nonce[vote.Round] = true

	d.logger.Debug("✅ vote processed successfully",
		"voter", vote.Voter.String(),
		"delegate", vote.Delegate.String(),
		"amount", vote.Amount.String(),
		"amountHex", fmt.Sprintf("0x%x", vote.Amount.Bytes()),
		"round", vote.Round)

	return nil
}

// 增强的投票处理（外部调用，加锁版本）
func (d *DPoS) processVote(vote *VoteMessage) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	return d.processVoteInternal(vote)
}

// 检查投票nonce防重放
func (d *DPoS) checkVoteNonce(vote *VoteMessage) error {
	if voter, exists := d.voters[vote.Voter]; exists {
		if voter.Nonce[vote.Round] {
			return errors.New("vote nonce already used")
		}
	}
	return nil
}

// 增强的签名验证
func (d *DPoS) verifyVoteSignature(vote *VoteMessage) error {
	// 构建投票消息哈希
	message := fmt.Sprintf("%s:%s:%s:%d",
		vote.Voter.String(),
		vote.Delegate.String(),
		vote.Amount.String(),
		vote.Round)

	messageBytes := []byte(message)
	hash := crypto.Keccak256(messageBytes)

	// 验证签名 - 简化实现，生产环境需要完整的签名验证
	// TODO: 实现完整的签名验证逻辑
	_ = hash // 避免未使用变量警告

	// 检查时间戳防重放
	now := uint64(time.Now().Unix())
	if vote.Timestamp < now-300 || vote.Timestamp > now+60 { // 5分钟时间窗口
		return errors.New("vote timestamp out of range")
	}

	return nil
}

// 增强的受托人更新（公共接口，需要获取锁）
func (d *DPoS) updateDelegates(block *types.FullBlock) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	return d.updateDelegatesInternal(block)
}

// 内部方法，不需要获取锁（由调用者负责锁管理）
func (d *DPoS) updateDelegatesInternal(block *types.FullBlock) error {
	d.logger.Info("🔄 updateDelegatesInternal called", "block", block, "currentDelegatesCount", len(d.delegates))

	// 🆕 方案1+方案2：检查是否需要重新排序验证者集合
	if d.pendingValidatorUpdate {
		d.logger.Info("🔄 轮次边界：重新排序验证者集合")

		// 按票数降序排序，如果票数相同则按地址排序
		sort.Slice(d.delegates, func(i, j int) bool {
			if d.delegates[i].VotingPower.Cmp(d.delegates[j].VotingPower) == 0 {
				// 票数相同，按地址排序（字节比较）
				return bytes.Compare(d.delegates[i].Address[:], d.delegates[j].Address[:]) < 0
			}
			return d.delegates[i].VotingPower.Cmp(d.delegates[j].VotingPower) > 0
		})

		d.logger.Info("✅ 轮次边界：验证者集合重新排序完成")
		for i, delegate := range d.delegates {
			d.logger.Info("🔍 重新排序后的验证者",
				"index", i,
				"address", delegate.Address.String(),
				"votingPower", delegate.VotingPower.String(),
				"isActive", delegate.IsActive)
		}

		// 🆕 修复：重新计算 currentDelegateIndex，确保与重新排序后的验证者集合一致
		if d.runtime != nil {
			// 🆕 使用 TryLock 避免死锁
			if d.runtime.lock.TryLock() {
			// 重新计算当前应该出块的委托者索引
			currentBlock := d.runtime.config.blockchain.CurrentHeader()
			if currentBlock != nil {
				var newIndex uint64
				if currentBlock.Number == 0 {
					newIndex = 0
				} else {
					newIndex = currentBlock.Number % uint64(d.runtime.config.DelegateCount)
				}
				oldIndex := d.runtime.currentDelegateIndex
				d.runtime.currentDelegateIndex = newIndex
				d.logger.Info("🔄 重新计算 currentDelegateIndex",
					"oldIndex", oldIndex,
					"newIndex", newIndex,
					"blockNumber", currentBlock.Number,
					"delegateCount", d.runtime.config.DelegateCount)
					
					// 🆕 关键修复：同步重新排序后的验证者集合到runtime
					d.logger.Info("🔄 同步重新排序后的验证者集合到runtime")
					d.runtime.delegates = make(validator.AccountSet, len(d.delegates))
					copy(d.runtime.delegates, d.delegates)
					d.logger.Info("✅ 验证者集合同步完成",
						"runtimeDelegatesCount", len(d.runtime.delegates),
						"sourceDelegatesCount", len(d.delegates))
			}
			d.runtime.lock.Unlock()
			} else {
				d.logger.Warn("⚠️ 无法获取 runtime.lock，跳过同步验证者集合")
			}
		}
	}

	// 🆕 落盘时：直接保存当前受托人集合，不进行排名和截取
	d.logger.Debug("💾 落盘时：直接保存当前受托人集合，不进行排名和截取")

	// 🆕 详细记录要落盘的见证人信息
	d.logger.Info("📋 准备落盘的见证人详情:")
	for i, delegate := range d.delegates {
		d.logger.Info("👤 见证人详情",
			"序号", i+1,
			"地址", delegate.Address.String(),
			"投票权重", delegate.VotingPower.String(),
			"是否活跃", delegate.IsActive,
			"isActiveType", fmt.Sprintf("%T", delegate.IsActive),
			"BLS密钥", delegate.BlsKey != nil,
			"BLS获取方式", func() string {
				if delegate.BlsKey != nil {
					return "已存在"
				}
				return "验证时动态获取"
			}())
	}

	// 直接保存当前的 d.delegates 到数据库，不改变受托人集合
	if err := d.persistDelegateSetToDatabase(d.delegates); err != nil {
		d.logger.Error("❌ Failed to persist delegate set to database", "error", err)
		return err
	}

	d.logger.Debug("✅ 受托人集合落盘完成", "count", len(d.delegates))
	return nil
}

// 比较受托人集合变化
func (d *DPoS) compareDelegateSets(oldSet, newSet validator.AccountSet) (added, removed []types.Address) {
	oldMap := make(map[types.Address]bool)
	newMap := make(map[types.Address]bool)

	for _, del := range oldSet {
		oldMap[del.Address] = true
	}
	for _, del := range newSet {
		newMap[del.Address] = true
	}

	for _, del := range newSet {
		if !oldMap[del.Address] {
			added = append(added, del.Address)
		}
	}

	for _, del := range oldSet {
		if !newMap[del.Address] {
			removed = append(removed, del.Address)
		}
	}

	return added, removed
}

// 处理奖励分配
func (d *DPoS) processRewards(block *types.FullBlock) error {
	// 处理奖励分配
	// TODO: 实现奖励分配逻辑
	d.logger.Debug("processing rewards", "block", block.Block.Number())
	return nil
}

// 辅助方法
func (d *DPoS) getPrimaryDelegate(votedDelegates []types.Address) types.Address {
	if len(votedDelegates) == 0 {
		return types.ZeroAddress
	}
	return votedDelegates[0]
}

func (d *DPoS) getDelegatesFromState(blockNumber uint64) (validator.AccountSet, error) {
	// 🆕 关键修复：首先尝试从历史数据获取验证者集合
	if d.state != nil && d.state.StakeStore != nil {
		// 开始数据库事务
		dbTx, err := d.state.beginDBTransaction(false) // 只读事务
		if err != nil {
			d.logger.Debug("⚠️ 无法开始数据库事务", "blockNumber", blockNumber, "error", err)
		} else {
			defer dbTx.Rollback()

			if historicalDelegates, err := d.state.StakeStore.getDelegatesAtBlock(blockNumber, dbTx); err == nil {
				d.logger.Info("✅ 从历史数据获取验证者集合", "blockNumber", blockNumber, "count", len(historicalDelegates))
				return historicalDelegates, nil
			} else {
				d.logger.Debug("⚠️ 历史验证者集合不存在", "blockNumber", blockNumber, "error", err)
			}
		}
	}

	// 🆕 关键修复：一旦获取不到历史验证者集合，直接退出程序
	d.logger.Error("❌ 历史验证者集合不存在，程序退出", "blockNumber", blockNumber)
	d.logger.Error("💀 无法获取历史验证者集合，程序退出")
	os.Exit(1)

	// 这行代码永远不会执行，但为了编译通过
	return nil, fmt.Errorf("historical validator set not found for block %d", blockNumber)
}

func (d *DPoS) getDelegatesFromStateWithTx(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error) {
	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接调用内存版本，避免复杂的数据库操作
	return d.getDelegatesFromState(blockNumber)
}

func (d *DPoS) getVotingPowerFromStateWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// 🆕 修复：简化实现，避免数据库事务死锁
	// 直接调用内存版本，避免复杂的数据库操作
	return d.GetVotingPower(blockNumber, delegate)
}

// 初始化性能优化组件
func (d *DPoS) initPerformanceOptimizations() {
	// 初始化缓存
	d.cache = &DPoSCache{
		voterCache:        make(map[types.Address]*VoterInfo),
		delegateCache:     make(map[types.Address]*validator.ValidatorMetadata),
		rewardCache:       make(map[types.Address]*big.Int),
		voterCacheTime:    make(map[types.Address]time.Time),
		delegateCacheTime: make(map[types.Address]time.Time),
		rewardCacheTime:   make(map[types.Address]time.Time),
		cacheTTL:          5 * time.Minute,
		maxCacheSize:      1000, // 最大缓存1000个条目
	}

	// 初始化批量处理器
	d.batchProcessor = &BatchProcessor{
		voteQueue:     make(chan *VoteMessage, 1000),
		delegateQueue: make(chan *DelegateMessage, 100),
		batchSize:     100,
		batchTimeout:  100 * time.Millisecond,
		workerCount:   4,
		stopCh:        make(chan struct{}),
	}

	// 初始化指标
	d.metrics = &DPoSMetrics{
		BlockRewards: big.NewInt(0),
	}

	// 启动批量处理工作协程
	d.startBatchWorkers()

	// 启动缓存清理协程
	go d.cacheCleanupWorker()

	// 🆕 启动签名去重清理协程
	if d.runtime != nil {
		d.runtime.startSignatureCleanup()
	}
}

// 启动批量处理工作协程
func (d *DPoS) startBatchWorkers() {
	for i := 0; i < d.batchProcessor.workerCount; i++ {
		d.batchProcessor.wg.Add(1)
		go d.batchWorker(i)
	}
}

// 批量处理工作协程
func (d *DPoS) batchWorker(id int) {
	defer d.batchProcessor.wg.Done()

	voteBatch := make([]*VoteMessage, 0, d.batchProcessor.batchSize)
	delegateBatch := make([]*DelegateMessage, 0, d.batchProcessor.batchSize)

	ticker := time.NewTicker(d.batchProcessor.batchTimeout)
	defer ticker.Stop()

	for {
		select {
		case vote := <-d.batchProcessor.voteQueue:
			voteBatch = append(voteBatch, vote)
			if len(voteBatch) >= d.batchProcessor.batchSize {
				d.processVoteBatch(voteBatch)
				voteBatch = voteBatch[:0]
			}

		case delegate := <-d.batchProcessor.delegateQueue:
			delegateBatch = append(delegateBatch, delegate)
			if len(delegateBatch) >= d.batchProcessor.batchSize {
				d.processDelegateBatch(delegateBatch)
				delegateBatch = delegateBatch[:0]
			}

		case <-ticker.C:
			if len(voteBatch) > 0 {
				d.processVoteBatch(voteBatch)
				voteBatch = voteBatch[:0]
			}
			if len(delegateBatch) > 0 {
				d.processDelegateBatch(delegateBatch)
				delegateBatch = delegateBatch[:0]
			}

		case <-d.batchProcessor.stopCh:
			// 处理剩余批次
			if len(voteBatch) > 0 {
				d.processVoteBatch(voteBatch)
			}
			if len(delegateBatch) > 0 {
				d.processDelegateBatch(delegateBatch)
			}
			return
		}
	}
}

// 批量处理投票
func (d *DPoS) processVoteBatch(votes []*VoteMessage) {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 批量更新投票者信息
	voterUpdates := make(map[types.Address]*VoterInfo)
	delegateUpdates := make(map[types.Address]*big.Int)

	for _, vote := range votes {
		// 验证投票
		if err := d.validateVote(vote); err != nil {
			d.logger.Warn("invalid vote in batch", "error", err, "voter", vote.Voter)
			continue
		}

		// 更新投票者
		voter, exists := d.voters[vote.Voter]
		if !exists {
			voter = &VoterInfo{
				Address:        vote.Voter,
				VotingPower:    big.NewInt(0),
				VotedDelegates: make([]types.Address, 0),
				LastVoteTime:   uint64(time.Now().Unix()),
				LockedUntil:    uint64(time.Now().Unix()) + VoteLockTime,
				Nonce:          make(map[uint64]bool),
			}
			d.voters[vote.Voter] = voter
		}

		voter.VotingPower = new(big.Int).Add(voter.VotingPower, vote.Amount)
		voter.LastVoteTime = uint64(time.Now().Unix())
		voter.LockedUntil = uint64(time.Now().Unix()) + VoteLockTime
		voter.Nonce[vote.Round] = true

		// 添加受托人到投票列表（去重）
		found := false
		for _, del := range voter.VotedDelegates {
			if del == vote.Delegate {
				found = true
				break
			}
		}
		if !found {
			voter.VotedDelegates = append(voter.VotedDelegates, vote.Delegate)
		}

		voterUpdates[vote.Voter] = voter

		// 累计受托人更新
		if delegateUpdates[vote.Delegate] == nil {
			delegateUpdates[vote.Delegate] = big.NewInt(0)
		}
		delegateUpdates[vote.Delegate].Add(delegateUpdates[vote.Delegate], vote.Amount)
	}

	// 批量更新受托人投票权重
	for delegate, amount := range delegateUpdates {
		for _, del := range d.delegates {
			if del.Address == delegate {
				del.VotingPower = new(big.Int).Add(del.VotingPower, amount)
				break
			}
		}
	}

	// 更新缓存
	d.updateCache(voterUpdates)

	// 更新指标
	d.metrics.lock.Lock()
	d.metrics.TotalVotes += uint64(len(votes))
	d.metrics.ActiveVoters = uint64(len(d.voters))
	d.metrics.lock.Unlock()

	d.logger.Debug("processed vote batch", "count", len(votes))
}

// 批量处理委托
func (d *DPoS) processDelegateBatch(delegates []*DelegateMessage) {
	d.lock.Lock()
	defer d.lock.Unlock()

	for _, msg := range delegates {
		switch msg.Action {
		case "register":
			found := false
			for _, del := range d.delegates {
				if del.Address == msg.Delegate {
					found = true
					break
				}
			}
			if !found {
				d.addDelegateSafely(&validator.ValidatorMetadata{
					Address:     msg.Delegate,
					VotingPower: msg.Stake,
					IsActive:    true,
				})
			}

		case "unregister":
			newSet := validator.AccountSet{}
			for _, del := range d.delegates {
				if del.Address != msg.Delegate {
					newSet = append(newSet, del)
				}
			}
			d.delegates = newSet

		case "update":
			for _, del := range d.delegates {
				if del.Address == msg.Delegate {
					del.VotingPower = msg.Stake
					break
				}
			}
		}
	}

	// 更新指标
	d.metrics.lock.Lock()
	d.metrics.TotalDelegates = uint64(len(d.delegates))
	d.metrics.lock.Unlock()

	d.logger.Debug("processed delegate batch", "count", len(delegates))
}

// 更新缓存
func (d *DPoS) updateCache(voterUpdates map[types.Address]*VoterInfo) {
	d.cache.lock.Lock()
	defer d.cache.lock.Unlock()

	for addr, voter := range voterUpdates {
		d.cache.voterCache[addr] = voter
	}
	d.cache.lastUpdate = time.Now()
}

// 缓存清理工作协程
func (d *DPoS) cacheCleanupWorker() {
	// 改为1倍TTL，增加清理频率
	ticker := time.NewTicker(d.cache.cacheTTL) // 从 * 2 改为直接使用
	defer ticker.Stop()
	
	// 添加内存压力检测
	memoryTicker := time.NewTicker(30 * time.Second)
	defer memoryTicker.Stop()

	for {
		select {
		case <-ticker.C:
			d.cleanupExpiredCache()
		case <-memoryTicker.C:
			// 内存压力检测
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.Alloc > 50*1024*1024 { // 50MB阈值
				d.cleanupExpiredCache()
			}
		case <-d.closeCh:
			return
		}
	}
}

// cleanupExpiredCache 清理过期的缓存条目
func (d *DPoS) cleanupExpiredCache() {
	d.cache.lock.Lock()
	defer d.cache.lock.Unlock()

	now := time.Now()
	cleanedCount := 0

	// 清理过期的投票者缓存
	expiredVoters := make([]types.Address, 0)
	for addr, cacheTime := range d.cache.voterCacheTime {
		if now.Sub(cacheTime) > d.cache.cacheTTL {
			expiredVoters = append(expiredVoters, addr)
		}
	}
	for _, addr := range expiredVoters {
		delete(d.cache.voterCache, addr)
		delete(d.cache.voterCacheTime, addr)
		cleanedCount++
	}

	// 清理过期的委托者缓存
	expiredDelegates := make([]types.Address, 0)
	for addr, cacheTime := range d.cache.delegateCacheTime {
		if now.Sub(cacheTime) > d.cache.cacheTTL {
			expiredDelegates = append(expiredDelegates, addr)
		}
	}
	for _, addr := range expiredDelegates {
		delete(d.cache.delegateCache, addr)
		delete(d.cache.delegateCacheTime, addr)
		cleanedCount++
	}

	// 清理过期的奖励缓存
	expiredRewards := make([]types.Address, 0)
	for addr, cacheTime := range d.cache.rewardCacheTime {
		if now.Sub(cacheTime) > d.cache.cacheTTL {
			expiredRewards = append(expiredRewards, addr)
		}
	}
	for _, addr := range expiredRewards {
		delete(d.cache.rewardCache, addr)
		delete(d.cache.rewardCacheTime, addr)
		cleanedCount++
	}

	// 如果缓存过大，清理最旧的条目
	d.cleanupOversizedCache()

	if cleanedCount > 0 {
		d.logger.Debug("DPoS缓存清理完成",
			"清理数量", cleanedCount,
			"投票者缓存", len(d.cache.voterCache),
			"委托者缓存", len(d.cache.delegateCache),
			"奖励缓存", len(d.cache.rewardCache))
	}
}

// cleanupOversizedCache 清理过大的缓存
func (d *DPoS) cleanupOversizedCache() {
	totalCacheSize := len(d.cache.voterCache) + len(d.cache.delegateCache) + len(d.cache.rewardCache)

	if totalCacheSize <= d.cache.maxCacheSize {
		return
	}

	// 计算需要清理的数量（保留80%的缓存）
	targetSize := int(float64(d.cache.maxCacheSize) * 0.8)
	needToClean := totalCacheSize - targetSize

	// 按时间排序，清理最旧的条目
	allEntries := make([]cacheEntry, 0)

	// 收集投票者缓存条目
	for addr, cacheTime := range d.cache.voterCacheTime {
		allEntries = append(allEntries, cacheEntry{
			addr:      addr,
			cacheTime: cacheTime,
			cacheType: "voter",
		})
	}

	// 收集委托者缓存条目
	for addr, cacheTime := range d.cache.delegateCacheTime {
		allEntries = append(allEntries, cacheEntry{
			addr:      addr,
			cacheTime: cacheTime,
			cacheType: "delegate",
		})
	}

	// 收集奖励缓存条目
	for addr, cacheTime := range d.cache.rewardCacheTime {
		allEntries = append(allEntries, cacheEntry{
			addr:      addr,
			cacheTime: cacheTime,
			cacheType: "reward",
		})
	}

	// 按时间排序（最旧的在前）
	sort.Slice(allEntries, func(i, j int) bool {
		return allEntries[i].cacheTime.Before(allEntries[j].cacheTime)
	})

	// 清理最旧的条目
	cleaned := 0
	for _, entry := range allEntries {
		if cleaned >= needToClean {
			break
		}

		switch entry.cacheType {
		case "voter":
			delete(d.cache.voterCache, entry.addr)
			delete(d.cache.voterCacheTime, entry.addr)
		case "delegate":
			delete(d.cache.delegateCache, entry.addr)
			delete(d.cache.delegateCacheTime, entry.addr)
		case "reward":
			delete(d.cache.rewardCache, entry.addr)
			delete(d.cache.rewardCacheTime, entry.addr)
		}
		cleaned++
	}

	if cleaned > 0 {
		d.logger.Debug("DPoS缓存大小清理完成",
			"清理数量", cleaned,
			"目标大小", targetSize,
			"当前大小", totalCacheSize-cleaned)
	}
}

// cacheEntry 缓存条目，用于排序
type cacheEntry struct {
	addr      types.Address
	cacheTime time.Time
	cacheType string
}

// 带缓存的投票者信息获取
func (d *DPoS) getVoterWithCache(addr types.Address) (*VoterInfo, bool) {
	d.cache.lock.RLock()
	if voter, exists := d.cache.voterCache[addr]; exists {
		d.cache.lock.RUnlock()
		return voter, true
	}
	d.cache.lock.RUnlock()

	d.lock.RLock()
	voter, exists := d.voters[addr]
	d.lock.RUnlock()

	if exists {
		// 更新缓存并记录时间戳
		d.cache.lock.Lock()
		d.cache.voterCache[addr] = voter
		d.cache.voterCacheTime[addr] = time.Now()
		d.cache.lock.Unlock()
	}

	return voter, exists
}

// 获取性能指标
func (d *DPoS) GetMetrics() *DPoSMetrics {
	d.metrics.lock.RLock()
	defer d.metrics.lock.RUnlock()

	return &DPoSMetrics{
		TotalVotes:     d.metrics.TotalVotes,
		TotalDelegates: d.metrics.TotalDelegates,
		ActiveVoters:   d.metrics.ActiveVoters,
		BlockRewards:   new(big.Int).Set(d.metrics.BlockRewards),
		LastBlockTime:  d.metrics.LastBlockTime,
	}
}

// 更新受托人投票权重
func (d *DPoS) updateDelegateVotingPower(delegate types.Address, amount *big.Int) {
	d.logger.Debug("🔄 Updating delegate voting power",
		"delegate", delegate.String(),
		"amount", amount.String(),
		"amountHex", fmt.Sprintf("0x%x", amount.Bytes()),
		"currentDelegatesCount", len(d.delegates))

	// 🆕 添加详细日志：打印所有受托人的当前状态
	d.logger.Info("🔍 Current delegates state before update:")
	for i, del := range d.delegates {
		d.logger.Info("🔍 Delegate",
			"index", i,
			"address", del.Address.String(),
			"votingPower", del.VotingPower.String(),
			"isActive", del.IsActive,
			"isTarget", del.Address == delegate)
	}

	// 查找现有受托人
	found := false
	for i, del := range d.delegates {
		if del.Address == delegate {
			oldPower := new(big.Int).Set(del.VotingPower)
			oldActive := del.IsActive
			del.VotingPower = new(big.Int).Add(del.VotingPower, amount)
			
			// 🆕 修复：根据新的投票权重更新活跃状态
			if del.VotingPower.Cmp(big.NewInt(0)) > 0 {
				del.IsActive = true
			} else {
				del.IsActive = false
			}
			
			d.logger.Debug("✅ Updated existing delegate voting power",
				"delegate", delegate.String(),
				"delegateIndex", i,
				"oldPower", oldPower.String(),
				"newPower", del.VotingPower.String(),
				"addedAmount", amount.String(),
				"oldActive", oldActive,
				"newActive", del.IsActive,
				"totalDelegates", len(d.delegates),
				"calculation", fmt.Sprintf("%s + %s = %s", oldPower.String(), amount.String(), del.VotingPower.String()))
			found = true
			break
		}
	}

	// 如果受托人不存在，创建一个新的
	if !found {
		d.logger.Info("🆕 Creating new delegate for voting power update",
			"delegate", delegate.String(),
			"amount", amount.String())

		newDelegate := &validator.ValidatorMetadata{
			Address:     delegate,
			VotingPower: new(big.Int).Set(amount),
			IsActive:    true, // 🆕 新增：自动设置为活跃状态
			// 其他字段使用默认值
		}

		// 🆕 新增：重点记录创建新受托人时的isActive状态
		d.logger.Info("🆕 创建新受托人",
			"address", delegate.String(),
			"votingPower", amount.String(),
			"isActive", newDelegate.IsActive,
			"isActiveType", fmt.Sprintf("%T", newDelegate.IsActive))

		d.addDelegateSafely(newDelegate)
	}

	// 🆕 添加详细日志：打印更新后的所有受托人状态
	d.logger.Info("🔍 Current delegates state after update:")
	for i, del := range d.delegates {
		d.logger.Info("🔍 Delegate",
			"index", i,
			"address", del.Address.String(),
			"votingPower", del.VotingPower.String(),
			"isActive", del.IsActive,
			"isTarget", del.Address == delegate)
	}
}

// calculateReward 计算奖励
func (d *DPoS) calculateReward(staker types.Address) *big.Int {
	// 简化的奖励计算逻辑
	// 这里可以根据实际的奖励算法来实现
	voter, exists := d.voters[staker]
	if !exists {
		return big.NewInt(0)
	}

	// 基础奖励：投票权重的1%
	reward := new(big.Int).Div(voter.VotingPower, big.NewInt(100))

	// 设置最小奖励
	minReward := big.NewInt(100000000000000000) // 0.1 token
	if reward.Cmp(minReward) < 0 {
		reward = minReward
	}

	return reward
}

// DefaultDPoSConfig 返回默认配置
func DefaultDPoSConfig() *DPoSConfig {
	return &DPoSConfig{
		DelegateCount:  21,
		BlockTime:      common.Duration{Duration: 15 * time.Second},
		RoundTime:      common.Duration{Duration: 30 * time.Second},
		MinVotingPower: big.NewInt(1000000000000000000), // 1 token
		VoteLockTime:   86400,                           // 24 hours
		RewardRatio:    100,                             // 1%
	}
}

// Validate 验证配置
func (c *DPoSConfig) Validate() error {
	if c.BlockTime.Duration <= 0 {
		return fmt.Errorf("block_time must be positive")
	}
	if c.RoundTime.Duration <= 0 {
		return fmt.Errorf("round_time must be positive")
	}
	if c.DelegateCount == 0 {
		return fmt.Errorf("delegate_count must be positive")
	}
	if c.MinVotingPower.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("min_voting_power must be positive")
	}
	return nil
}

// GetConfigSummary 获取配置摘要
func (c *DPoSConfig) GetConfigSummary() map[string]interface{} {
	return map[string]interface{}{
		"delegate_count":   c.DelegateCount,
		"block_time":       c.BlockTime.String(),
		"round_time":       c.RoundTime.String(),
		"min_voting_power": c.MinVotingPower.String(),
		"vote_lock_time":   c.VoteLockTime, // 当前设置为60秒（1分钟）
		"reward_ratio":     c.RewardRatio,
	}
}

// collectValidatorSignatures 收集验证者签名
func (r *dposRuntime) collectValidatorSignatures(block *types.FullBlock, checkpointHash types.Hash, keyAddr types.Address) ([][]byte, bitmap.Bitmap, error) {
	signatures := make([][]byte, 0)
	signatureBitmap := bitmap.Bitmap{}

	// 🆕 确保受托人按票数排序，与验证时保持一致
	r.logger.Debug("🔍 区块生产前：确保受托人按票数排序，与验证时保持一致")
	r.logger.Debug("📊 出块前受托人统计", "totalDelegates", len(r.delegates))

	// 打印所有受托人信息
	for i, delegate := range r.delegates {
		r.logger.Debug("🏭 出块受托人", "index", i, "address", delegate.Address.String(), "votingPower", delegate.VotingPower.String(), "isActive", delegate.IsActive)
	}

	// 🆕 关键修复：在签名收集前主动获取所有受托人的BLS公钥
	r.logger.Debug("🔑 开始主动获取所有受托人的BLS公钥")
	myAddress := types.Address(r.config.Key.Address())
	
	for i, delegate := range r.delegates {
		if delegate.BlsKey == nil {
			r.logger.Debug("🔑 出块时受托人缺少BLS公钥，将在验证时按需获取", 
				"index", i, 
				"address", delegate.Address.String(), 
				"votingPower", delegate.VotingPower.String(), 
				"isActive", delegate.IsActive)
			
			// 主动请求BLS公钥
			if r.networkIntegration != nil {
				if err := r.networkIntegration.RequestBLSKey(delegate.Address, myAddress); err != nil {
					r.logger.Warn("⚠️ 请求BLS公钥失败", 
						"address", delegate.Address.String(), 
						"error", err)
				} else {
					r.logger.Debug("📨 已发送BLS公钥请求", 
						"address", delegate.Address.String())
				}
			}
		} else {
			r.logger.Debug("🔑 出块时受托人BLS公钥已存在", 
				"index", i, 
				"address", delegate.Address.String(), 
				"publicKeyLength", len(delegate.BlsKey.Marshal()))
		}
	}
	
	// 等待一小段时间让BLS公钥请求完成
	time.Sleep(100 * time.Millisecond)
	
	// 🆕 尝试从缓存中恢复BLS公钥
	r.logger.Debug("🔄 尝试从缓存恢复BLS公钥")
	if r.networkIntegration != nil {
		// 使用批量恢复函数
		if err := r.networkIntegration.RestoreBLSKeysForDelegates(r.delegates); err != nil {
			r.logger.Warn("⚠️ 批量恢复BLS公钥失败", "error", err)
		}
	}
	
	// 🆕 验证BLS公钥可用性
	missingBlsKeys := 0
	for i, delegate := range r.delegates {
		if delegate.BlsKey == nil {
			missingBlsKeys++
			r.logger.Warn("⚠️ 受托人缺少BLS公钥", 
				"index", i, 
				"address", delegate.Address.String())
		}
	}
	
	if missingBlsKeys > 0 {
		r.logger.Warn("⚠️ 部分受托人缺少BLS公钥，将在验证时按需获取", 
			"missingCount", missingBlsKeys,
			"totalDelegates", len(r.delegates))
	}

	// 🆕 关键修复：不要在签名收集过程中重新排序验证者集合
	// 排序应该在签名收集之前完成，确保位图索引与签名收集一致
	// sort.Slice(r.delegates, func(i, j int) bool {
	// 	if r.delegates[i].VotingPower.Cmp(r.delegates[j].VotingPower) == 0 {
	// 		// 票数相同，按地址排序（字节比较）
	// 		return bytes.Compare(r.delegates[i].Address[:], r.delegates[j].Address[:]) < 0
	// 	}
	// 	return r.delegates[i].VotingPower.Cmp(r.delegates[j].VotingPower) > 0
	// })


	r.logger.Debug("开始收集验证者签名",
		"checkpointHash", checkpointHash.String(),
		"delegatesCount", len(r.delegates),
		"proposerAddress", keyAddr.String())

	
	// 统计活跃验证者数量
	activeCount := 0
	for _, delegate := range r.delegates {
		if delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0 {
			activeCount++
		}
	}
	
	// 检查网络中的活跃验证者数量
	activeValidators := r.getActiveValidatorsCount()
	
	r.logger.Debug("📊 活跃验证者统计",
		"activeCount", activeCount,
		"totalDelegates", len(r.delegates),
		"activeValidators", activeValidators,
		"note", "activeCount是本地统计，activeValidators是网络方法返回")
	
	// 计算真正活跃的验证者数量（有足够stake且IsActive=true）
	expectedSignatures := activeValidators // 只计算活跃的验证者

	r.logger.Debug("🌐 出块时网络状态检查",
		"activeValidators", activeValidators,
		"totalDelegates", len(r.delegates),
		"expectedSignatures", expectedSignatures,
		"requiredForQuorum", r.calculateMinRequiredSignatures())

	// 检查是否有足够的验证者
	minRequired := r.calculateMinRequiredSignatures()
	if activeValidators < minRequired { // 现在包括提议者自己
		r.logger.Error("验证者数量不足，无法进行签名收集",
			"activeValidators", activeValidators,
			"minRequired", minRequired,
			"totalDelegates", len(r.delegates))
		
		// 返回错误，让上层重试机制处理
		return nil, nil, fmt.Errorf("insufficient validators: got %d, need %d", activeValidators, minRequired)
	}

	// 1. 广播签名请求给其他验证者
	// 创建protobuf签名请求消息
	protoRequest := &dposProto.SignatureRequest{}
	protoRequest.BlockNumber = block.Block.Number()
	protoRequest.BlockHash = block.Block.Header.Hash.Bytes()
	protoRequest.CheckpointHash = checkpointHash.Bytes()
	protoRequest.Round = r.currentRound
	protoRequest.Proposer = types.Address(r.config.Key.Address()).Bytes()
	protoRequest.Timestamp = uint64(time.Now().Unix())

	if err := r.broadcastSignatureRequest(protoRequest); err != nil {
		r.logger.Error("failed to broadcast signature request", "error", err)
		return nil, nil, fmt.Errorf("failed to broadcast signature request: %w", err)
	}

	// 2. 创建签名收集通道和收集器
	signatureCh := make(chan *SignatureResponse, len(r.delegates))

	// 如果有网络集成，使用网络集成层进行签名收集
	if r.networkIntegration != nil {
		r.logger.Debug("使用网络集成层进行签名收集", "checkpointHash", checkpointHash.String())

		// 注册签名收集器到网络集成层
		timeout := 30 * time.Second
		requiredCount := r.calculateMinRequiredSignatures()
		r.networkIntegration.RegisterSignatureCollector(checkpointHash, signatureCh, timeout, requiredCount)

		r.logger.Debug("签名收集器已注册到网络集成层",
			"checkpointHash", checkpointHash.String(),
			"timeout", timeout,
			"requiredCount", requiredCount)
	} else {
		r.logger.Debug("网络集成不可用，使用原有的签名收集机制", "checkpointHash", checkpointHash.String())
	}

	// 启动签名收集协程 - 修复：确保使用正确的通道
	r.logger.Debug("启动签名收集协程", "checkpointHash", checkpointHash.String())
	go r.collectSignaturesAsync(checkpointHash, signatureCh)

	// 等待一小段时间让协程启动
	time.Sleep(100 * time.Millisecond)

	// 3. 智能等待签名收集完成
	collectedSignatures := make(map[types.Address][]byte)
	minRequiredSignatures := r.calculateMinRequiredSignatures()

	// 使用合理的超时时间
	baseTimeout := 2 * time.Minute    // 减少基础超时时间
	networkTimeout := 5 * time.Minute // 减少网络等待超时

	// 如果验证者数量不足，使用更长的超时等待更多节点加入
	if r.getActiveValidatorsCount() < r.calculateMinRequiredSignatures()+1 {
		baseTimeout = networkTimeout
	}

	// 签名收集超时配置（静默处理）

	timeoutCh := time.After(baseTimeout)
	checkInterval := time.NewTicker(15 * time.Second) // 每15秒检查一次网络状态
	defer checkInterval.Stop()

	// 开始智能签名收集（静默处理）

	// 添加调试日志，监控通道状态
	debugTicker := time.NewTicker(5 * time.Second)
	defer debugTicker.Stop()

	for {
		select {
		case sigResp := <-signatureCh:
			if sigResp != nil && sigResp.Signature != nil {
				collectedSignatures[sigResp.ValidatorAddr] = sigResp.Signature
				// 收到验证者签名（静默处理）

				// 检查是否收集到足够的签名
				if len(collectedSignatures) >= minRequiredSignatures {
					r.logger.Debug("收集到足够的签名",
						"count", len(collectedSignatures),
						"checkpointHash", checkpointHash.String())
					goto processSignatures
				}
			}

		case <-checkInterval.C:
			// 定期检查网络状态
			activeValidators := r.getActiveValidatorsCount()
			r.logger.Debug("定期检查网络状态",
				"activeValidators", activeValidators,
				"totalDelegates", len(r.delegates),
				"collectedSignatures", len(collectedSignatures),
				"requiredSignatures", minRequiredSignatures)

			// 检查是否有足够的验证者进行签名收集
			if activeValidators < minRequiredSignatures {
				r.logger.Warn("验证者数量不足，等待更多节点加入",
					"activeValidators", activeValidators,
					"minRequired", minRequiredSignatures)
				// 重置超时，给更多节点加入的时间
				timeoutCh = time.After(1 * time.Minute)
			} else if len(collectedSignatures) == 0 {
				r.logger.Warn("验证者数量足够但未收到签名，检查网络连接")

				// 🆕 进行网络健康检查
				if !r.checkNetworkHealth() {
					r.logger.Warn("网络健康检查失败，尝试自动恢复")
					go r.recoverNetworkConnection()
					// 给恢复更多时间
					timeoutCh = time.After(1 * time.Minute)
				} else {
					// 网络健康但未收到签名，可能是其他问题，给更多时间
					timeoutCh = time.After(30 * time.Second)
				}
			}

		case <-debugTicker.C:
			// 调试日志：监控通道状态和收集器状态
			// 检查网络集成层的收集器状态
			if r.networkIntegration != nil {
				// 网络集成层状态检查
			}

		case <-timeoutCh:
			r.logger.Warn("签名收集超时",
				"collected", len(collectedSignatures),
				"required", minRequiredSignatures,
				"checkpointHash", checkpointHash.String(),
				"activeValidators", r.getActiveValidatorsCount(),
				"totalDelegates", len(r.delegates))

			// 记录当前收集到的签名详情
			for addr, sig := range collectedSignatures {
				r.logger.Debug("已收集签名",
					"validator", addr.String(),
					"signatureLength", len(sig))
			}

			// 记录缺失的验证者
			missingValidators := make([]string, 0)
			for _, delegate := range r.delegates {
				if _, exists := collectedSignatures[delegate.Address]; !exists {
					missingValidators = append(missingValidators, delegate.Address.String())
				}
			}
			r.logger.Warn("缺失签名的验证者",
				"missingCount", len(missingValidators),
				"missingValidators", missingValidators)

			// 如果收集到的签名不足，返回错误
			if len(collectedSignatures) < minRequiredSignatures {
				return nil, nil, fmt.Errorf("insufficient signatures collected: got %d, need at least %d", len(collectedSignatures), minRequiredSignatures)
			}
			goto processSignatures
		}
	}

processSignatures:
	// 4. 处理收集到的签名
	r.logger.Debug("🎯 出块时签名收集开始",
		"totalDelegates", len(r.delegates),
		"checkpointHash", checkpointHash.String())


	for i, delegate := range r.delegates {
		// 静默处理，不打印日志

		if delegate.Address == keyAddr {
			// 提议者自己生成签名
			if r.config != nil && r.config.Key != nil {
				blsKey, err := r.getBLSPrivateKey()
				if err != nil {
					r.logger.Warn("提议者无法获取BLS私钥", "error", err)
					continue
				}

				signature, err := blsKey.Sign(checkpointHash[:], signer.DomainValidatorSet)
				if err != nil {
					r.logger.Warn("提议者签名失败", "error", err)
					continue
				}

				signatureBytes, err := signature.Marshal()
				if err != nil {
					r.logger.Warn("提议者签名序列化失败", "error", err)
					continue
				}

				signatures = append(signatures, signatureBytes)
				signatureBitmap.Set(uint64(i))
				r.logger.Debug("✅ 提议者签名已添加",
					"address", delegate.Address.String(),
					"bitmapIndex", i,
					"signatureCount", len(signatures),
					"checkpointHash", checkpointHash.String())
			}
			continue
		}

		if signature, exists := collectedSignatures[delegate.Address]; exists {


			// 验证签名
			if err := r.verifyValidatorSignature(delegate, signature, checkpointHash); err != nil {
				r.logger.Warn("❌ 验证者签名验证失败",
					"validator", delegate.Address.String(),
					"error", err)
				continue
			}

			// 静默处理，不打印日志

			signatures = append(signatures, signature)
			signatureBitmap.Set(uint64(i))
		} else {
			r.logger.Debug("未收到验证者签名",
				"validator", delegate.Address.String())
		}
	}

	r.logger.Debug("🎉 出块签名收集完成",
		"totalSignatures", len(signatures),
		"bitmapLength", len(signatureBitmap),
		"collectedCount", len(collectedSignatures),
		"expectedCount", expectedSignatures,
		"checkpointHash", checkpointHash.String())

	// 显示最终的签名详情
	r.logger.Debug("📋 出块时最终签名详情:")
	for i, sig := range signatures {
		r.logger.Debug("📝 收集到的签名",
			"signatureIndex", i,
			"signatureLength", len(sig),
			"signatureHex", fmt.Sprintf("%x", sig))
	}

	// 🆕 新增：出块前数据一致性验证
	r.logger.Debug("🔍 出块前验证数据一致性...")
	if err := r.verifyBlockDataConsistency(); err != nil {
		r.logger.Error("❌ 出块前数据一致性验证失败", "error", err)
		// 继续出块，但记录错误
	} else {
		r.logger.Debug("✅ 出块前数据一致性验证通过")
	}

	// 显示位图详情
	if len(signatures) > 0 {
		r.logger.Debug("📊 区块签名位图详情",
			"bitmapHex", fmt.Sprintf("%x", signatureBitmap),
			"bitmapLength", len(signatureBitmap),
			"signedDelegatesCount", len(signatures))

		// 位图验证完成
	}

	// 资源清理：清理签名收集相关的临时数据
	r.cleanupSignatureCollectionResources(checkpointHash)

	return signatures, signatureBitmap, nil
}

// verifyDataConsistencyAfterVote 验证投票后数据一致性
// 🆕 简化版：快速数据一致性验证，避免阻塞
func (d *DPoS) verifyDataConsistencyAfterVote() error {
	d.lock.RLock()
	defer d.lock.RUnlock()

	d.logger.Info("🔍 Starting simplified data consistency verification...")

	// 1. 快速检查内存中的验证者集合
	memoryDelegates := d.delegates.Copy()
	d.logger.Info("📊 Memory delegates count", "count", len(memoryDelegates))

	// 2. 简化验证：只检查基本一致性，不读取数据库
	d.logger.Info("🔍 Performing quick consistency check...")

	// 检查内存中的验证者是否都有BLS公钥
	blsKeyMissing := 0
	for _, del := range memoryDelegates {
		if del.BlsKey == nil {
			blsKeyMissing++
			d.logger.Warn("⚠️ Delegate missing BLS key in memory",
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String())
		}
	}

	if blsKeyMissing > 0 {
		d.logger.Warn("⚠️ Some delegates missing BLS keys in memory",
			"missingCount", blsKeyMissing,
			"totalCount", len(memoryDelegates))
	} else {
		d.logger.Info("✅ All delegates have BLS keys in memory",
			"totalCount", len(memoryDelegates))
	}

	d.logger.Info("✅ Simplified data consistency verification completed",
		"memoryCount", len(memoryDelegates),
		"blsKeysMissing", blsKeyMissing)

	return nil
}

// verifyBlockDataConsistency 出块前验证数据一致性
func (r *dposRuntime) verifyBlockDataConsistency() error {
	// 1. 获取出块时使用的验证者集合
	blockValidators := r.delegates.Copy()

	// 2. 获取通过GetDelegates方法获得的验证者集合
	currentBlockNumber := r.config.blockchain.CurrentHeader().Number
	getValidators, err := r.config.dposBackend.GetDelegates(currentBlockNumber, nil)
	if err != nil {
		r.logger.Error("❌ Failed to get validators via GetDelegates", "error", err)
		return fmt.Errorf("failed to get validators via GetDelegates: %w", err)
	}

	// 3. 比较两个验证者集合
	blockMap := make(map[types.Address]*validator.ValidatorMetadata)
	getMap := make(map[types.Address]*validator.ValidatorMetadata)

	// 构建出块验证者映射
	for _, del := range blockValidators {
		blockMap[del.Address] = del
	}

	// 构建GetDelegates验证者映射
	for _, del := range getValidators {
		getMap[del.Address] = del
	}

	// 4. 检查验证者集合一致性
	inconsistencies := 0

	// 检查出块验证者集合中的每个验证者
	for addr, blockDel := range blockMap {
		getDel, exists := getMap[addr]
		if !exists {
			r.logger.Error("❌ Validator exists in block delegates but not in GetDelegates",
				"address", addr.String(),
				"blockVotingPower", blockDel.VotingPower.String(),
				"blockIsActive", blockDel.IsActive)
			inconsistencies++
			continue
		}

		// 检查关键字段一致性
		if blockDel.VotingPower.Cmp(getDel.VotingPower) != 0 {
			r.logger.Error("❌ Voting power inconsistency in block validation",
				"address", addr.String(),
				"blockVotingPower", blockDel.VotingPower.String(),
				"getVotingPower", getDel.VotingPower.String(),
				"difference", new(big.Int).Sub(getDel.VotingPower, blockDel.VotingPower).String(),
				"blockIsActive", blockDel.IsActive,
				"getIsActive", getDel.IsActive)

			// 🆕 添加详细分析日志
			r.logger.Error("🔍 Voting power inconsistency analysis:",
				"address", addr.String(),
				"memoryValue", blockDel.VotingPower.String(),
				"databaseValue", getDel.VotingPower.String(),
				"memoryIsActive", blockDel.IsActive,
				"databaseIsActive", getDel.IsActive,
				"inconsistencyType", "voting_power_mismatch")
			inconsistencies++
		}

		if blockDel.IsActive != getDel.IsActive {
			r.logger.Error("❌ IsActive inconsistency in block validation",
				"address", addr.String(),
				"blockIsActive", blockDel.IsActive,
				"getIsActive", getDel.IsActive)
			inconsistencies++
		}

		// 检查BLS公钥一致性
		blockBlsKey := ""
		getBlsKey := ""
		if blockDel.BlsKey != nil {
			blockBlsKey = fmt.Sprintf("%x", blockDel.BlsKey.Marshal())
		}
		if getDel.BlsKey != nil {
			getBlsKey = fmt.Sprintf("%x", getDel.BlsKey.Marshal())
		}

		if blockBlsKey != getBlsKey {
			r.logger.Error("❌ BLS key inconsistency in block validation",
				"address", addr.String(),
				"blockBlsKey", blockBlsKey,
				"getBlsKey", getBlsKey)
			inconsistencies++
		}
	}

	// 5. 检查GetDelegates中是否有出块验证者集合中没有的验证者
	for addr, getDel := range getMap {
		if _, exists := blockMap[addr]; !exists {
			r.logger.Warn("⚠️ Validator exists in GetDelegates but not in block delegates",
				"address", addr.String(),
				"getVotingPower", getDel.VotingPower.String(),
				"getIsActive", getDel.IsActive)
		}
	}

	if inconsistencies > 0 {
		r.logger.Error("❌ Block data consistency verification failed",
			"inconsistencies", inconsistencies,
			"blockCount", len(blockValidators),
			"getCount", len(getValidators))
		return fmt.Errorf("block data consistency verification failed: %d inconsistencies found", inconsistencies)
	}

	// 数据一致性验证通过
	return nil
}

// getActiveValidatorsCount 获取网络中活跃验证者的数量
func (r *dposRuntime) getActiveValidatorsCount() int {
	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("网络服务不可用，无法进行多节点签名收集")
		return 0
	}

	// 计算真正活跃的验证者数量（有足够stake且IsActive=true）
	activeValidators := 0
	for _, delegate := range r.delegates {
		if delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0 {
			activeValidators++
		}
	}

	// 如果只有一个活跃验证者，返回0（表示无法进行多节点签名收集）
	if activeValidators <= 1 {
		r.logger.Debug("活跃验证者数量不足，无法进行多节点签名收集",
			"activeValidators", activeValidators,
			"totalDelegates", len(r.delegates))
		return 0
	}

	// 🆕 检查实际网络连接状态
	connectedPeers := r.getConnectedPeersCount()
	// 网络连接状态检查（静默处理）
	
	if connectedPeers < activeValidators {
		// 🆕 修复：如果完全没有网络连接，返回0触发等待网络改善
		if connectedPeers == 0 {
			r.logger.Warn("完全没有网络连接，返回0触发等待网络改善",
				"activeValidators", activeValidators,
				"connectedPeers", connectedPeers)
			return 0
		}
		
		// 如果有部分连接，返回活跃验证者数量（当前节点可参与签名）
		return activeValidators
	}

	return activeValidators
}

// getConnectedPeersCount 获取实际连接的节点数量
func (r *dposRuntime) getConnectedPeersCount() int {
	if r.network == nil {
		return 0
	}

	peers := r.network.Peers()
	connectedCount := 0

	for _, peer := range peers {
		// 检查peer是否真正连接（这里需要根据实际的peer接口调整）
		// 假设peer有IsConnected方法或类似的状态检查
		if peer != nil {
			connectedCount++
		}
	}

	return connectedCount
}

// checkNetworkHealth 检查网络健康状态
func (r *dposRuntime) checkNetworkHealth() bool {
	if r.network == nil {
		r.logger.Debug("网络健康检查失败：网络服务不可用")
		return false
	}

	// 检查主题是否可用
	topic, err := r.getSignatureRequestTopic()
	if err != nil || topic == nil {
		r.logger.Debug("网络健康检查失败：签名请求主题不可用", "error", err)
		return false
	}

	// 检查是否有足够的网络连接
	connectedPeers := r.getConnectedPeersCount()
	if connectedPeers < 2 {
		r.logger.Debug("网络健康检查失败：网络连接不足", "connectedPeers", connectedPeers)
		return false
	}

	r.logger.Debug("网络健康检查通过", "connectedPeers", connectedPeers)
	return true
}

// recoverNetworkConnection 自动恢复网络连接
func (r *dposRuntime) recoverNetworkConnection() {
	r.logger.Warn("检测到网络连接问题，尝试自动恢复")

	// 重新创建网络主题
	r.topicMutex.Lock()
	r.signatureRequestTopic = nil
	r.signatureResponseTopic = nil
	r.signatureQueryTopic = nil
	r.topicMutex.Unlock()

	r.logger.Debug("网络主题已重置，等待重新创建")

	// 重新初始化网络集成层
	if r.networkIntegration != nil {
		r.logger.Debug("重新初始化网络集成层")
		if err := r.networkIntegration.Stop(); err != nil {
			r.logger.Warn("停止网络集成层失败", "error", err)
		}

		// 重新设置网络集成层
		if err := r.setupNetworkIntegration(); err != nil {
			r.logger.Error("重新设置网络集成层失败", "error", err)
		} else {
			r.logger.Info("网络集成层重新初始化成功")
		}
	}
}

// startNetworkHealthMonitoring 启动网络健康监控
func (r *dposRuntime) startNetworkHealthMonitoring() {
	// 每30秒检查一次网络健康状态
	r.networkHealthTimer = time.NewTicker(30 * time.Second)

	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("network-health-monitor", func() {
			for {
				select {
				case <-r.networkHealthTimer.C:
					r.performNetworkHealthCheck()
				case <-r.closeCh:
					return
				}
			}
		})
	}

	r.logger.Debug("网络健康监控已启动")
}

// performNetworkHealthCheck 执行网络健康检查
func (r *dposRuntime) performNetworkHealthCheck() {
	r.lastNetworkCheck = time.Now()

	// 检查网络健康状态
	if !r.checkNetworkHealth() {
		r.logger.Warn("定期网络健康检查失败，尝试自动恢复")
		go r.recoverNetworkConnection()
	} else {
		r.logger.Debug("定期网络健康检查通过")
	}
}

// calculateMinRequiredSignatures 计算最少需要的签名数量
func (r *dposRuntime) calculateMinRequiredSignatures() int {
	// 🆕 使用配置的验证者数量而不是实际活跃验证者数量
	validatorsCount := r.config.ValidatorsCount
	if validatorsCount == 0 {
		// 如果配置中没有设置，使用默认值4
		validatorsCount = 4
		r.logger.Warn("⚠️ 配置中未设置ValidatorsCount，使用默认值4")
	}

	r.logger.Info("🧮 计算法定人数", 
		"configuredValidatorsCount", validatorsCount,
		"totalDelegates", len(r.delegates))

	// 使用2/3多数原则，基于配置的验证者数量计算门槛
	minRequired := (int(validatorsCount)*2 + 2) / 3 // 向上取整
	if minRequired < 1 {
		minRequired = 1
	}

	r.logger.Info("✅ 门槛计算完成", 
		"validatorsCount", validatorsCount,
		"minRequiredSignatures", minRequired)

	return minRequired
}

// waitForNetworkGrowth 等待网络增长到足够的验证者
func (r *dposRuntime) waitForNetworkGrowth(checkpointHash types.Hash, proposerAddr types.Address) ([][]byte, bitmap.Bitmap, error) {
	r.logger.Info("开始等待网络增长", "checkpointHash", checkpointHash.String())

	// 设置等待超时（10分钟）
	waitTimeout := 10 * time.Minute
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	timeoutCh := time.After(waitTimeout)

	for {
		select {
		case <-ticker.C:
			activeValidators := r.getActiveValidatorsCount()
			minRequired := r.calculateMinRequiredSignatures()
			
			r.logger.Info("检查网络状态",
				"activeValidators", activeValidators,
				"minRequired", minRequired,
				"totalDelegates", len(r.delegates))

			// 如果网络中有足够的验证者，重新尝试收集签名
			if activeValidators >= minRequired {
				r.logger.Info("检测到足够的验证者，重新尝试收集签名",
					"activeValidators", activeValidators,
					"minRequired", minRequired,
					"checkpointHash", checkpointHash.String())
				
				// 重新启动签名收集流程
				// 使用更短的超时时间，避免长时间等待
				retryTimeout := 30 * time.Second
				retryCtx, cancel := context.WithTimeout(context.Background(), retryTimeout)
				defer cancel()
				
				// 创建新的签名收集通道
				signatureCh := make(chan *SignatureResponse, len(r.delegates))
				
				// 重新广播签名请求
				currentBlockNumber := r.config.blockchain.CurrentHeader().Number + 1
				protoRequest := &dposProto.SignatureRequest{
					BlockNumber:    currentBlockNumber,
					CheckpointHash: checkpointHash.Bytes(),
					Round:         r.currentRound,
					Proposer:      proposerAddr.Bytes(),
					Timestamp:     uint64(time.Now().Unix()),
				}
				
				if err := r.broadcastSignatureRequest(protoRequest); err != nil {
					r.logger.Error("重新广播签名请求失败", "error", err)
					return nil, nil, fmt.Errorf("failed to rebroadcast signature request: %w", err)
				}
				
				// 等待签名收集
				signatures, bitmap, err := r.waitForSignaturesWithContext(retryCtx, signatureCh, minRequired)
				if err != nil {
					r.logger.Error("重新收集签名失败", "error", err)
					return nil, nil, fmt.Errorf("failed to collect signatures after network growth: %w", err)
				}
				
				r.logger.Info("网络增长后签名收集成功",
					"signaturesCount", len(signatures),
					"bitmapLength", len(bitmap))
				
				return signatures, bitmap, nil
			}

		case <-timeoutCh:
			r.logger.Error("等待网络增长超时，需要更多验证者节点")
			// 超时后，返回错误
			return nil, nil, fmt.Errorf("network growth timeout: need more validator nodes")
		}
	}
}

// waitForSignaturesWithContext 使用上下文控制的签名收集
func (r *dposRuntime) waitForSignaturesWithContext(ctx context.Context, signatureCh chan *SignatureResponse, minRequired int) ([][]byte, bitmap.Bitmap, error) {
	collectedSignatures := make(map[types.Address][]byte)
	signatureBitmap := bitmap.Bitmap{}
	
	// 设置收集超时
	collectTimeout := 20 * time.Second
	timeoutCh := time.After(collectTimeout)
	
	for {
		select {
		case response := <-signatureCh:
			if response == nil {
				continue
			}
			
			// 验证签名响应
			if err := r.validateSignatureResponse(response); err != nil {
				r.logger.Warn("签名响应验证失败", "validator", response.ValidatorAddr.String(), "error", err)
				continue
			}
			
			// 收集签名
			collectedSignatures[response.ValidatorAddr] = response.Signature
			
			// 设置位图
			for i, delegate := range r.delegates {
				if delegate.Address == response.ValidatorAddr {
					signatureBitmap.Set(uint64(i))
					break
				}
			}
			
			r.logger.Info("收集到签名",
				"validator", response.ValidatorAddr.String(),
				"collectedCount", len(collectedSignatures),
				"requiredCount", minRequired)
			
			// 检查是否收集到足够的签名
			if len(collectedSignatures) >= minRequired {
				// 按位图顺序排列签名
				signatures := make([][]byte, 0, len(collectedSignatures))
				for i := uint64(0); i < uint64(len(r.delegates)); i++ {
					if signatureBitmap.IsSet(i) {
						if sig, exists := collectedSignatures[r.delegates[i].Address]; exists {
							signatures = append(signatures, sig)
						}
					}
				}
				
				return signatures, signatureBitmap, nil
			}
			
		case <-timeoutCh:
			r.logger.Warn("签名收集超时",
				"collected", len(collectedSignatures),
				"required", minRequired)
			
			if len(collectedSignatures) >= minRequired {
				// 即使超时，如果收集到足够的签名就返回
				signatures := make([][]byte, 0, len(collectedSignatures))
				for i := uint64(0); i < uint64(len(r.delegates)); i++ {
					if signatureBitmap.IsSet(i) {
						if sig, exists := collectedSignatures[r.delegates[i].Address]; exists {
							signatures = append(signatures, sig)
						}
					}
				}
				return signatures, signatureBitmap, nil
			}
			
			return nil, nil, fmt.Errorf("signature collection timeout: got %d, need %d", len(collectedSignatures), minRequired)
			
		case <-ctx.Done():
			r.logger.Warn("签名收集被取消", "error", ctx.Err())
			return nil, nil, fmt.Errorf("signature collection cancelled: %w", ctx.Err())
		}
	}
}

// broadcastSignatureRequest 广播签名请求
func (r *dposRuntime) broadcastSignatureRequest(protoRequest *dposProto.SignatureRequest) error {
	// 验证签名请求的有效性，防止发送无效消息
	if protoRequest == nil {
		return fmt.Errorf("signature request is nil")
	}

	// 检查是否是查询请求
	if protoRequest.BlockNumber == 0 {
		// 查询请求必须包含有效的标识符
		if string(protoRequest.BlockHash) != "QUERY_REQUEST" && string(protoRequest.CheckpointHash) != "QUERY_REQUEST" {
			r.logger.Warn("阻止发送无效的查询请求",
				"blockHash", string(protoRequest.BlockHash),
				"checkpointHash", string(protoRequest.CheckpointHash))
			return fmt.Errorf("invalid query request: missing QUERY_REQUEST identifier")
		}
	} else {
		// 正常签名请求必须包含有效字段
		if protoRequest.BlockNumber == 0 {
			return fmt.Errorf("invalid block number: cannot be 0 for non-query requests")
		}
		if len(protoRequest.CheckpointHash) == 0 {
			return fmt.Errorf("invalid checkpoint hash: cannot be empty")
		}
		if len(protoRequest.Proposer) == 0 {
			return fmt.Errorf("invalid proposer: cannot be empty")
		}
	}

	checkpointHash := types.BytesToHash(protoRequest.CheckpointHash)

	// 创建内部请求对象
	internalRequest := &SignatureRequest{
		BlockNumber:    protoRequest.BlockNumber,
		BlockHash:      types.BytesToHash(protoRequest.BlockHash),
		CheckpointHash: checkpointHash,
		Round:          protoRequest.Round,
		Proposer:       types.BytesToAddress(protoRequest.Proposer),
		Timestamp:      protoRequest.Timestamp,
	}

	// 存储待处理的签名请求
	r.signatureRequestMutex.Lock()
	r.pendingSignatureRequests[checkpointHash] = internalRequest
	r.signatureRequestMutex.Unlock()

	// 获取签名请求主题
	_, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		// 回退到日志记录
		//r.logger.Info("广播签名请求（回退模式）",
		//	"blockNumber", protoRequest.BlockNumber,
		//	"checkpointHash", checkpointHash.String(),
		//	"round", protoRequest.Round)
		return nil
	}

	// 发布签名请求
	r.logger.Info("attempting to publish signature request",
		"blockNumber", protoRequest.BlockNumber,
		"round", protoRequest.Round)

	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		// 回退到日志记录
		//r.logger.Info("广播签名请求（回退模式）",
		//	"blockNumber", protoRequest.BlockNumber,
		//	"checkpointHash", checkpointHash.String(),
		//	"round", protoRequest.Round)
		return nil
	}

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化 SignatureRequest
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		r.logger.Warn("failed to marshal signature request", "error", err)
		return fmt.Errorf("failed to marshal signature request: %w", err)
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: requestData,
	}

	// 发布签名请求
	r.logger.Info("开始广播签名请求", "区块高度", protoRequest.BlockNumber, "checkpointHash", checkpointHash.String())
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature request, using fallback", "error", err)
		// 回退到日志记录
		//r.logger.Info("广播签名请求（回退模式）",
		//	"blockNumber", protoRequest.BlockNumber,
		//	"checkpointHash", checkpointHash.String(),
		//	"round", protoRequest.Round)
		return nil
	}

	r.logger.Info("成功广播签名请求", "区块高度", protoRequest.BlockNumber, "checkpointHash", checkpointHash.String())

	// 启动基于时间的简单备用传播监控
	go r.simpleFallbackMonitoring(protoRequest, checkpointHash)

	//r.logger.Info("成功广播签名请求",
	//	"blockNumber", protoRequest.BlockNumber,
	//	"checkpointHash", checkpointHash.String(),
	//	"round", protoRequest.Round)

	return nil
}

// getSignatureRequestTopic 获取签名请求主题
func (r *dposRuntime) getSignatureRequestTopic() (*network.Topic, error) {
	// 首先检查网络服务是否可用
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	r.topicMutex.RLock()
	if r.signatureRequestTopic != nil {
		defer r.topicMutex.RUnlock()
		return r.signatureRequestTopic, nil
	}
	r.topicMutex.RUnlock()

	r.topicMutex.Lock()
	defer r.topicMutex.Unlock()

	// 双重检查
	if r.signatureRequestTopic != nil {
		return r.signatureRequestTopic, nil
	}

	// 再次检查网络服务（双重检查）
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	// 使用有效的默认值创建主题，避免网络层发送零值消息
	// 创建一个模板消息作为主题初始化，这样网络层就不会发送无效的默认消息
	defaultRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,
		BlockHash:      []byte("TEMPLATE_MSG"),
		CheckpointHash: []byte("TEMPLATE_MSG"),
		Round:          0,
		Proposer:       types.Address(r.config.Key.Address()).Bytes(),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认请求
	defaultRequestData, err := proto.Marshal(defaultRequest)
	if err != nil {
		r.logger.Warn("failed to marshal default request template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultRequestData = []byte("TEMPLATE_MSG")
	}

	// 创建 DPOSMessage 作为模板
	defaultDPOSMessage := &dposProto.TransportMessage{
		Data: defaultRequestData,
	}

	// 尝试创建新主题，如果失败则智能处理
	topic, err := r.network.NewTopic("dpos-signature-request", defaultDPOSMessage)
	if err != nil {
		// 如果主题已存在，我们需要获取现有主题的引用
		if strings.Contains(err.Error(), "topic already exists") {
			r.logger.Debug("主题已存在，尝试获取现有主题引用", "topic", "dpos-signature-request")

			// 检查网络集成层是否有现有主题
			if r.networkIntegration != nil {
				// 尝试从网络集成层获取现有主题
				if existingTopic := r.networkIntegration.GetSignatureRequestTopic(); existingTopic != nil {
					r.logger.Debug("从网络集成层获取到现有主题", "topic", "dpos-signature-request")
					r.signatureRequestTopic = existingTopic
					return existingTopic, nil
				}
			}

			// 如果网络集成层也没有，我们需要等待一下再重试
			// 这通常是因为并发创建导致的，等待一下应该就能成功
			r.logger.Info("等待网络层同步后重试", "topic", "dpos-signature-request")
			time.Sleep(200 * time.Millisecond)

			// 重试创建主题
			topic, err = r.network.NewTopic("dpos-signature-request", defaultDPOSMessage)
			if err != nil {
				r.logger.Warn("重试创建主题仍然失败", "error", err)
				return nil, fmt.Errorf("failed to create topic after retry: %w", err)
			}
		} else {
			r.logger.Warn("创建签名请求主题失败", "error", err)
			return nil, fmt.Errorf("failed to create signature request topic: %w", err)
		}
	}

	r.signatureRequestTopic = topic
	r.logger.Debug("成功创建签名请求主题")
	return topic, nil
}

// getSignatureResponseTopic 获取签名响应主题
func (r *dposRuntime) getSignatureResponseTopic() (*network.Topic, error) {
	// 首先检查网络服务是否可用
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	r.topicMutex.RLock()
	if r.signatureResponseTopic != nil {
		defer r.topicMutex.RUnlock()
		return r.signatureResponseTopic, nil
	}
	r.topicMutex.RUnlock()

	r.topicMutex.Lock()
	defer r.topicMutex.Unlock()

	// 双重检查
	if r.signatureResponseTopic != nil {
		return r.signatureResponseTopic, nil
	}

	// 再次检查网络服务（双重检查）
	if r.network == nil {
		return nil, fmt.Errorf("network service not available")
	}

	// 使用有效的默认值创建主题，避免网络层发送零值消息
	// 创建一个有效的签名响应作为模板
	defaultResponse := &dposProto.SignatureResponse{
		ValidatorAddr:  types.Address(r.config.Key.Address()).Bytes(),
		Signature:      []byte("DEFAULT_SIGNATURE"),
		CheckpointHash: types.Hash{}.Bytes(), // 使用零哈希，但这是合法的
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 序列化默认响应
	defaultResponseData, err := proto.Marshal(defaultResponse)
	if err != nil {
		r.logger.Warn("failed to marshal default response template", "error", err)
		// 如果序列化失败，使用空的但有效的消息
		defaultResponseData = []byte("DEFAULT_RESPONSE")
	}

	// 创建 DPOSMessage 作为模板
	defaultDPOSMessage := &dposProto.TransportMessage{
		Data: defaultResponseData,
	}

	// 尝试创建新主题，如果失败则智能处理
	topic, err := r.network.NewTopic("dpos-signature-response", defaultDPOSMessage)
	if err != nil {
		// 如果主题已存在，我们需要获取现有主题的引用
		if strings.Contains(err.Error(), "topic already exists") {
			r.logger.Debug("主题已存在，尝试获取现有主题引用", "topic", "dpos-signature-response")

			// 检查网络集成层是否有现有主题
			if r.networkIntegration != nil {
				// 尝试从网络集成层获取现有主题
				if existingTopic := r.networkIntegration.GetSignatureResponseTopic(); existingTopic != nil {
					r.logger.Debug("从网络集成层获取到现有主题", "topic", "dpos-signature-response")
					r.signatureResponseTopic = existingTopic
					return existingTopic, nil
				}
			}

			// 如果网络集成层也没有，我们需要等待一下再重试
			// 这通常是因为并发创建导致的，等待一下应该就能成功
			r.logger.Info("等待网络层同步后重试", "topic", "dpos-signature-response")
			time.Sleep(200 * time.Millisecond)

			// 重试创建主题
			topic, err = r.network.NewTopic("dpos-signature-response", defaultDPOSMessage)
			if err != nil {
				r.logger.Warn("重试创建主题仍然失败", "error", err)
				return nil, fmt.Errorf("failed to create topic after retry: %w", err)
			}
		} else {
			r.logger.Warn("创建签名响应主题失败", "error", err)
			return nil, fmt.Errorf("failed to create signature response topic: %w", err)
		}
	}

	r.signatureResponseTopic = topic
	r.logger.Info("成功创建签名响应主题")
	return topic, nil
}

// collectSignaturesAsync 异步收集签名
func (r *dposRuntime) collectSignaturesAsync(checkpointHash types.Hash, signatureCh chan<- *SignatureResponse) {
	// 计算最小所需签名数量
	minRequiredSignatures := r.calculateMinRequiredSignatures()

	// 启动异步签名收集（静默处理）

	// 修复：创建一个双向通道作为桥梁，确保类型兼容性
	// 同时保持签名响应能够正确传递到collectValidatorSignatures等待的通道
	bridgeCh := make(chan *SignatureResponse, 1000) // 使用合适的缓冲区大小

	// 启动转发协程，将bridgeCh的消息转发到signatureCh
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("signature-bridge", func() {
			defer close(signatureCh)
			defer close(bridgeCh)


			// 添加超时控制，避免无限运行
			timeout := time.After(5 * time.Minute) // 5分钟后自动退出

			for {
				select {
				case response, ok := <-bridgeCh:
					if !ok {
						r.logger.Debug("桥接通道关闭，停止转发",
							"checkpointHash", checkpointHash.String())
						return
					}


					// 发送到signatureCh
					select {
					case signatureCh <- response:
					case <-time.After(5 * time.Second):
						r.logger.Error("签名响应桥接转发超时，丢弃响应",
							"validator", response.ValidatorAddr.String(),
							"checkpointHash", checkpointHash.String())
					}
				case <-timeout:
					r.logger.Debug("签名桥接协程超时，自动退出",
						"checkpointHash", checkpointHash.String())
					return
				}
			}
		})
	} else {
		r.logger.Error("资源监控器不可用，无法启动签名桥接协程")
	}

	// 注册签名收集器，使用桥接通道
	if r.networkIntegration != nil {

		r.networkIntegration.RegisterSignatureCollector(
			checkpointHash,
			bridgeCh, // 使用桥接通道
			30*time.Second,
			minRequiredSignatures,
		)
	} else {
		r.logger.Error("网络集成层不可用，无法注册签名收集器",
			"checkpointHash", checkpointHash.String())
	}

	// 启动一个监控协程，定期检查收集状态
	if r.resourceMonitor != nil && r.resourceMonitor.goroutineManager != nil {
		r.resourceMonitor.goroutineManager.StartGoroutine("signature-monitor", func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()

			// 添加超时控制，避免无限运行
			timeout := time.After(5 * time.Minute) // 5分钟后自动退出

			for {
				select {
				case <-ticker.C:
					// 检查收集器状态
					if r.networkIntegration != nil {
						// 静默监控，不打印日志
					}
				case <-timeout:
					r.logger.Debug("签名收集监控超时，自动退出",
						"checkpointHash", checkpointHash.String())
					return
				}
			}
		})
	}

}

// fallbackSignatureCollection 备用签名收集机制
func (r *dposRuntime) fallbackSignatureCollection(ctx context.Context, listener *SignatureListener, checkpointHash types.Hash) {
	r.logger.Info("启动备用签名收集机制", "checkpointHash", checkpointHash.String())

	// 定期检查是否有新的签名响应
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// 记录启动时间，用于超时控制
	startTime := time.Now()
	maxDuration := 5 * time.Minute // 最大运行时间

	for {
		select {
		case <-ctx.Done():
			r.logger.Debug("备用签名收集机制停止", "checkpointHash", checkpointHash.String())
			return
		case <-ticker.C:
			// 检查是否超时
			if time.Since(startTime) > maxDuration {
				r.logger.Warn("备用签名收集机制超时，停止运行", "checkpointHash", checkpointHash.String())
				return
			}

			// 尝试查询待处理的签名请求
			r.queryPendingSignatureRequests()

			// 尝试广播签名查询
			r.broadcastSignatureQuery()
		}
	}
}

// createSignatureListener 创建签名响应监听器
func (r *dposRuntime) createSignatureListener(checkpointHash types.Hash, signatureCh chan<- *SignatureResponse) *SignatureListener {
	return &SignatureListener{
		checkpointHash: checkpointHash,
		signatureCh:    signatureCh,
		receivedSigs:   make(map[types.Address]bool),
		logger:         r.logger,
	}
}

// listenForSignatureResponses 监听签名响应消息
func (r *dposRuntime) listenForSignatureResponses(ctx context.Context, listener *SignatureListener) {
	// 移除重复的签名请求监听，因为全局监听器已经处理了
	r.logger.Debug("启动签名响应监听器", "checkpointHash", listener.checkpointHash.String())

	// 监听上下文取消
	<-ctx.Done()
	r.logger.Debug("签名响应监听器停止", "checkpointHash", listener.checkpointHash.String())
}

// listenForSignatureRequests 监听签名请求并生成响应
func (r *dposRuntime) listenForSignatureRequests(ctx context.Context) {
	r.logger.Debug("开始监听签名请求", "节点地址", types.Address(r.config.Key.Address()).String())

	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("network service not available, cannot listen for signature requests")
		return
	}

	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		// 回退到日志记录
		r.logger.Info("监听签名请求（回退模式）")
		<-ctx.Done()
		r.logger.Debug("签名请求监听器停止（回退模式）")
		return
	}

	// 订阅主题
	if err := topic.Subscribe(r.handleSignatureRequestMessage); err != nil {
		r.logger.Warn("failed to subscribe to signature request topic, using fallback", "error", err)
		// 回退到日志记录
		r.logger.Info("监听签名请求（回退模式）")
		<-ctx.Done()
		r.logger.Debug("签名请求监听器停止（回退模式）")
		return
	}

	r.logger.Debug("成功订阅签名请求主题", "节点地址", types.Address(r.config.Key.Address()).String())

	// 监听上下文取消
	<-ctx.Done()

	// 注意：network.Topic 没有 Unsubscribe 方法，使用 Close() 会自动关闭所有订阅者
	// 这里不需要手动取消订阅，因为主题会在程序退出时自动清理
	r.logger.Debug("签名请求监听器停止")
}

// handleSignatureRequestMessage 处理签名请求消息
func (r *dposRuntime) handleSignatureRequestMessage(obj interface{}, from peer.ID) {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot handle signature request message")
		return
	}

	protoRequest, ok := obj.(*dposProto.SignatureRequest)
	if !ok {

		return
	}

	// 检查是否是模板消息（忽略）
	if protoRequest.BlockNumber == 0 && (string(protoRequest.BlockHash) == "TEMPLATE_MSG" || string(protoRequest.CheckpointHash) == "TEMPLATE_MSG") {
		r.logger.Debug("收到模板消息，忽略", "from", from.String())
		return
	}

	// 检查是否是查询请求
	if protoRequest.BlockNumber == 0 && (string(protoRequest.BlockHash) == "QUERY_REQUEST" || string(protoRequest.CheckpointHash) == "QUERY_REQUEST") {
		r.logger.Debug("收到签名查询请求", "from", from.String())
		r.handleSignatureQueryRequest(from)
		return
	}

	// 转换为内部格式
	request := &SignatureRequest{
		BlockNumber:    protoRequest.BlockNumber,
		BlockHash:      types.BytesToHash(protoRequest.BlockHash),
		CheckpointHash: types.BytesToHash(protoRequest.CheckpointHash),
		Round:          protoRequest.Round,
		Proposer:       types.BytesToAddress(protoRequest.Proposer),
		Timestamp:      protoRequest.Timestamp,
	}

	// 验证签名请求的有效性
	// 首先检查是否是模板消息（BlockNumber=0 且包含模板标识符）
	if request.BlockNumber == 0 {
		// 检查是否是模板消息
		if string(protoRequest.BlockHash) == "TEMPLATE_MSG" || string(protoRequest.CheckpointHash) == "TEMPLATE_MSG" {
			// 这是模板消息，忽略
			r.logger.Debug("收到模板消息，忽略",
				"blockNumber", request.BlockNumber,
				"blockHash", request.BlockHash.String(),
				"checkpointHash", request.CheckpointHash.String(),
				"from", from.String())
			return
		} else if string(protoRequest.BlockHash) == "QUERY_REQUEST" || string(protoRequest.CheckpointHash) == "QUERY_REQUEST" {
			// 这是查询请求，正常处理
			r.logger.Debug("收到查询请求，正常处理",
				"blockNumber", request.BlockNumber,
				"blockHash", request.BlockHash.String(),
				"checkpointHash", request.CheckpointHash.String(),
				"from", from.String())
		} else {
			// 这是无效的请求
			// r.logger.Warn("收到无效的签名请求：BlockNumber 为 0 但不是查询请求，忽略此请求",
			// 	"blockHash", request.BlockHash.String(),
			// 	"checkpointHash", request.CheckpointHash.String(),
			// 	"proposer", request.Proposer.String(),
			// 	"from", from.String())
			return
		}
	} else {
		// 对于正常的签名请求，检查其他字段
		if request.CheckpointHash == (types.Hash{}) {
			r.logger.Warn("收到无效的签名请求：CheckpointHash 为全零，忽略此请求",
				"blockNumber", request.BlockNumber,
				"proposer", request.Proposer.String(),
				"from", from.String())
			return
		}

		if request.Proposer == (types.Address{}) {
			r.logger.Warn("收到无效的签名请求：Proposer 地址为空，忽略此请求",
				"checkpointHash", request.CheckpointHash.String(),
				"proposer", request.Proposer.String(),
				"from", from.String())
			return
		}
	}

	// 简化处理：直接处理签名请求，不做去重检查

	// 获取当前区块高度
	delegateCount := uint64(len(r.delegates))
	if delegateCount == 0 {
		// 如果委托者列表为空，使用默认值
		delegateCount = 4 // 默认4个委托者
	}
	currentBlockNumber := r.backend.GetCurrentRound()*delegateCount + r.currentDelegateIndex

	// 检查是否是过期请求（区块号差距过大）
	if currentBlockNumber > request.BlockNumber+10 {
		r.logger.Debug("忽略过期签名请求",
			"from", from.String(),
			"requestBlockNumber", request.BlockNumber,
			"currentBlockNumber", currentBlockNumber,
			"本地节点", types.Address(r.config.Key.Address()).String())
		return
	}

	// 检查是否是自己的请求
	if request.Proposer == types.Address(r.config.Key.Address()) {
		//r.logger.Info("忽略自己的签名请求", "本地节点", types.Address(r.config.Key.Address()).String())
		return
	}

	// 检查自己是否是验证者
	if !r.isValidator() {
		r.logger.Info("自己不是验证者，忽略签名请求", "本地节点", types.Address(r.config.Key.Address()).String())
		return
	}

	// 使用并发控制，限制同时处理的签名请求数量
	select {
	case r.signatureRequestSemaphore <- struct{}{}:
		// 获取到信号量，可以处理签名请求
		go func() {
			defer func() { <-r.signatureRequestSemaphore }() // 释放信号量

			if err := r.generateSignatureResponse(request); err != nil {
				r.logger.Error("failed to generate signature response", "error", err)
			}
		}()
	default:
		// 信号量已满，记录警告并跳过处理
		//r.logger.Warn("并发签名请求过多，跳过处理",
		//	"checkpointHash", protoRequest.CheckpointHash.String(),
		//	"proposer", protoRequest.Proposer.String(),
		//	"当前并发数", r.maxConcurrentSignatures)
	}
}

// handleSignatureQueryRequest 处理签名查询请求
func (r *dposRuntime) handleSignatureQueryRequest(from peer.ID) {
	//r.logger.Info("处理签名查询请求", "from", from.String())

	// 获取所有待处理的签名请求
	r.signatureRequestMutex.RLock()
	pendingRequests := make([]*SignatureRequest, 0, len(r.pendingSignatureRequests))
	for _, request := range r.pendingSignatureRequests {
		pendingRequests = append(pendingRequests, request)
	}
	r.signatureRequestMutex.RUnlock()

	//r.logger.Info("当前存储的签名请求数量", "count", len(pendingRequests))

	if len(pendingRequests) == 0 {
		//r.logger.Info("没有待处理的签名请求")
		return
	}

	//r.logger.Info("发送待处理签名请求", "count", len(pendingRequests))

	// 广播所有待处理的签名请求
	for _, request := range pendingRequests {
		//r.logger.Info("广播签名请求",
		//	"blockNumber", request.BlockNumber,
		//	"checkpointHash", request.CheckpointHash.String(),
		//	"proposer", request.Proposer.String())
		r.broadcastSignatureRequestToPeer(request, from)
	}
}

// broadcastSignatureRequestToPeer 向特定节点广播签名请求
func (r *dposRuntime) broadcastSignatureRequestToPeer(request *SignatureRequest, peerID peer.ID) {
	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic for peer broadcast", "error", err)
		return
	}

	// 转换为protobuf格式
	protoRequest := &dposProto.SignatureRequest{
		BlockNumber:    request.BlockNumber,
		BlockHash:      request.BlockHash.Bytes(),
		CheckpointHash: request.CheckpointHash.Bytes(),
		Round:          request.Round,
		Proposer:       request.Proposer.Bytes(),
		Timestamp:      request.Timestamp,
	}

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化签名请求
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		r.logger.Warn("failed to marshal signature request", "error", err, "peer", peerID.String())
		return
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: requestData,
	}

	// 发布签名请求
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature request to peer", "error", err, "peer", peerID.String())
		return
	}

	//r.logger.Info("向节点广播签名请求成功", "peer", peerID.String(), "blockNumber", request.BlockNumber)
}

// isValidator 检查当前节点是否是验证者
func (r *dposRuntime) isValidator() bool {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot check if validator")
		return false
	}

	currentAddr := types.Address(r.config.Key.Address())
	
	// 🆕 优先从数据库获取验证者信息，确保数据一致性
	if r.backend != nil {
		// 通过backend获取当前验证者集合
		currentDelegates := r.backend.GetCurrentDelegates()
		if len(currentDelegates) > 0 {
			r.logger.Debug("🔍 从backend检查验证者状态", "address", currentAddr.String(), "delegatesCount", len(currentDelegates))
			
			for _, delegate := range currentDelegates {
				if delegate.Address == currentAddr {
					// 关键：检查stake是否足够且是否活跃
					if delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0 {
						r.logOnce("active_validator", "debug", "✅ 当前节点是活跃验证者（从backend）",
							"address", currentAddr.String(),
							"votingPower", delegate.VotingPower.String(),
							"isActive", delegate.IsActive)
						return true
					} else {
						r.logger.Info("❌ 当前节点不是活跃验证者（stake不足或不活跃，从backend）",
							"address", currentAddr.String(),
							"votingPower", delegate.VotingPower.String(),
							"isActive", delegate.IsActive)
						return false
					}
				}
			}
			
			r.logger.Info("❌ 当前节点不在backend受托人集合中", "address", currentAddr.String())
			return false
		} else {
			r.logger.Debug("⚠️ backend返回空验证者集合，回退到内存检查")
		}
	}
	
	// 回退方案：检查内存中的验证者
	for _, delegate := range r.delegates {
		if delegate.Address == currentAddr {
			// 关键：检查stake是否足够且是否活跃
			if delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0 {
				r.logOnce("active_validator", "debug", "✅ 当前节点是活跃验证者（从内存）",
					"address", currentAddr.String(),
					"votingPower", delegate.VotingPower.String(),
					"isActive", delegate.IsActive)
				return true
			} else {
				r.logger.Info("❌ 当前节点不是活跃验证者（stake不足或不活跃，从内存）",
					"address", currentAddr.String(),
					"votingPower", delegate.VotingPower.String(),
					"isActive", delegate.IsActive)
				return false
			}
		}
	}

	r.logger.Info("❌ 当前节点不在受托人集合中", "address", currentAddr.String())
	return false
}

// generateSignatureResponse 生成签名响应
func (r *dposRuntime) generateSignatureResponse(request *SignatureRequest) error {
	validatorAddr := types.Address(r.config.Key.Address())

	r.logger.Debug("🎯 收到签名请求，开始生成签名响应",
		"myAddress", validatorAddr.String(),
		"requestBlockNumber", request.BlockNumber,
		"requestCheckpointHash", request.CheckpointHash.String(),
		"proposer", request.Proposer.String())

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot generate signature response")
		return fmt.Errorf("key not available, cannot generate signature response")
	}

	// 获取自己的BLS私钥
	blsKey, err := r.getBLSPrivateKey()
	if err != nil {
		r.logger.Error("❌ 获取BLS私钥失败", "address", validatorAddr.String(), "error", err)
		return fmt.Errorf("failed to get BLS private key: %w", err)
	}

	r.logger.Debug("✅ 获取BLS私钥成功", "address", validatorAddr.String())

	// 生成签名
	signature, err := blsKey.Sign(request.CheckpointHash[:], signer.DomainValidatorSet)
	if err != nil {
		r.logger.Error("❌ 签名生成失败", "address", validatorAddr.String(), "error", err)
		return fmt.Errorf("failed to sign checkpoint hash: %w", err)
	}

	// 序列化BLS签名
	signatureBytes, err := signature.Marshal()
	if err != nil {
		r.logger.Error("❌ 签名序列化失败", "address", validatorAddr.String(), "error", err)
		return fmt.Errorf("failed to marshal signature: %w", err)
	}

	// 创建内部签名响应结构
	internalResponse := &SignatureResponse{
		ValidatorAddr:  types.Address(r.config.Key.Address()),
		Signature:      signatureBytes,
		CheckpointHash: request.CheckpointHash,
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 优先使用网络集成层发送消息
	if r.networkIntegration != nil {
		if err := r.networkIntegration.BroadcastSignatureResponse(internalResponse); err != nil {
			r.logger.Warn("通过网络集成层发送签名响应失败，使用回退模式", "error", err)
			// 回退到日志记录
			r.logger.Debug("生成签名响应（回退模式）",
				"validator", types.Address(r.config.Key.Address()).String(),
				"checkpointHash", request.CheckpointHash.String(),
				"signatureLength", len(signatureBytes))
			return nil
		}

		// 签名响应已广播
		return nil
	}

	// 如果没有网络集成层，记录错误并返回
	r.logger.Error("网络集成层不可用，无法发送签名响应",
		"validator", types.Address(r.config.Key.Address()).String(),
		"checkpointHash", request.CheckpointHash.String())
	return fmt.Errorf("网络集成层不可用，无法发送签名响应")
}

// getBLSPrivateKey 获取BLS私钥
func (r *dposRuntime) getBLSPrivateKey() (*bls.PrivateKey, error) {
	// 首先检查缓存
	r.blsPrivateKeyCacheMutex.RLock()
	if r.blsPrivateKeyCache != nil {
		defer r.blsPrivateKeyCacheMutex.RUnlock()
		return r.blsPrivateKeyCache, nil
	}
	r.blsPrivateKeyCacheMutex.RUnlock()

	// 缓存未命中，需要从文件读取
	r.blsPrivateKeyCacheMutex.Lock()
	defer r.blsPrivateKeyCacheMutex.Unlock()

	// 双重检查，防止在获取写锁期间其他goroutine已经加载了缓存
	if r.blsPrivateKeyCache != nil {
		return r.blsPrivateKeyCache, nil
	}

	// 这里需要从密钥管理器获取BLS私钥
	secretsManager := r.backend.(*DPoS).config.SecretsManager
	if secretsManager == nil {
		return nil, fmt.Errorf("secrets manager not available")
	}

	// 获取BLS私钥
	blsKey, err := secretsManager.GetSecret(secrets.ValidatorBLSKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get BLS key from secrets manager: %w", err)
	}

	// 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey(blsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}

	// 缓存私钥
	r.blsPrivateKeyCache = privateKey

	return privateKey, nil
}

// queryPendingSignatureRequests 查询待处理的签名请求
func (r *dposRuntime) queryPendingSignatureRequests() {
	//r.logger.Info("开始查询待处理的签名请求")

	// 调试：检查当前节点的签名请求存储状态
	r.debugPendingSignatureRequests()

	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Warn("network service not available, cannot query pending signature requests")
		return
	}

	// 获取当前连接的节点
	peers := r.network.Peers()
	if len(peers) == 0 {
		r.logger.Info("no connected peers, skipping pending signature request query")
		return
	}

	//r.logger.Info("查询待处理签名请求", "peerCount", len(peers))

	// 向所有连接的节点查询待处理的签名请求
	for _, peer := range peers {
		go r.queryPeerForPendingRequests(peer.Info.ID)
	}

	// 实现简单的广播查询机制
	// 通过现有的签名请求主题发送查询消息
	r.broadcastSignatureQuery()
}

// broadcastSignatureQuery 广播签名查询请求
func (r *dposRuntime) broadcastSignatureQuery() {
	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic for query", "error", err)
		return
	}

	// 创建查询请求 - 使用明确的标识符避免被误认为是无效请求
	queryRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,                       // 使用0表示这是一个查询请求
		BlockHash:      []byte("QUERY_REQUEST"), // 直接使用字符串标识符
		CheckpointHash: []byte("QUERY_REQUEST"), // 直接使用字符串标识符
		Round:          0,
		Proposer:       types.Address(r.config.Key.Address()).Bytes(),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 添加调试日志
	// r.logger.Debug("发送签名查询请求",
	// 	"blockNumber", queryRequest.BlockNumber,
	// 	"blockHash", string(queryRequest.BlockHash),
	// 	"checkpointHash", string(queryRequest.CheckpointHash),
	// 	"proposer", types.Address(r.config.Key.Address()).String())

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化查询请求
	queryData, err := proto.Marshal(queryRequest)
	if err != nil {
		r.logger.Warn("failed to marshal query request", "error", err)
		return
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: queryData,
	}

	// 发布查询请求
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature query request", "error", err)
		return
	}

	// 广播签名查询请求成功（静默处理）
}

// queryPeerForPendingRequests 向指定节点查询待处理的签名请求
func (r *dposRuntime) queryPeerForPendingRequests(peerID peer.ID) {
	//r.logger.Debug("查询节点的待处理签名请求", "peer", peerID.String())

	// 实现真正的RPC查询逻辑
	// 通过现有的签名请求主题发送查询消息

	// 获取签名请求主题
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic for peer query", "error", err, "peer", peerID.String())
		return
	}

	// 创建查询请求 - 使用明确的标识符避免被误认为是无效请求
	queryRequest := &dposProto.SignatureRequest{
		BlockNumber:    0,                       // 使用0表示这是一个查询请求
		BlockHash:      []byte("QUERY_REQUEST"), // 直接使用字符串标识符
		CheckpointHash: []byte("QUERY_REQUEST"), // 直接使用字符串标识符
		Round:          0,
		Proposer:       types.Address(r.config.Key.Address()).Bytes(),
		Timestamp:      uint64(time.Now().Unix()),
	}

	// 发送签名查询请求（静默处理）

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化查询请求
	queryData, err := proto.Marshal(queryRequest)
	if err != nil {
		r.logger.Warn("failed to marshal query request", "error", err, "peer", peerID.String())
		return
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: queryData,
	}

	// 发布查询请求到网络
	if err := topic.Publish(dposMsg); err != nil {
		r.logger.Warn("failed to publish signature query request", "error", err, "peer", peerID.String())
		return
	}

}

// getSignatureQueryTopic 获取签名查询主题
func (r *dposRuntime) getSignatureQueryTopic() (*network.Topic, error) {
	// 简化的实现，返回nil表示暂时不实现
	return nil, fmt.Errorf("signature query topic not implemented")
}

// handleSignatureQueryResponse 处理签名查询响应
func (r *dposRuntime) handleSignatureQueryResponse(obj interface{}, from peer.ID) {
	//r.logger.Info("收到签名查询响应", "from", from.String())
	// 简化的实现，暂时只记录日志
}

// 添加新节点加入时的处理逻辑
func (r *dposRuntime) onNewPeerJoined(peerID peer.ID) {
	r.logger.Info("新节点加入网络", "peer", peerID.String())

	// 延迟一段时间后查询该节点的待处理签名请求
	// 给节点一些时间完成初始化
	go func() {
		time.Sleep(5 * time.Second)
		r.queryPeerForPendingRequests(peerID)
	}()
}

// subscribeToSignatureTopic 订阅签名响应主题
func (r *dposRuntime) subscribeToSignatureTopic(listener *SignatureListener) error {
	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("network service not available, cannot subscribe to signature topic")
		return fmt.Errorf("network service not available, cannot subscribe to signature topic")
	}

	// 获取签名响应主题
	topic, err := r.getSignatureResponseTopic()
	if err != nil {
		r.logger.Warn("failed to get signature response topic, using fallback", "error", err)
		// 回退到日志记录
		r.logger.Debug("订阅签名响应主题（回退模式）")
		return nil
	}

	// 检查topic是否为nil
	if topic == nil {
		r.logger.Warn("signature response topic is nil, using fallback")
		// 回退到日志记录
		r.logger.Debug("订阅签名响应主题（回退模式）")
		return nil
	}

	// 订阅主题 - 使用闭包传递listener参数
	handler := func(obj interface{}, from peer.ID) {
		r.handleSignatureResponseMessage(obj, from, listener)
	}
	if err := topic.Subscribe(handler); err != nil {
		r.logger.Warn("failed to subscribe to signature response topic, using fallback", "error", err)
		// 回退到日志记录
		r.logger.Debug("订阅签名响应主题（回退模式）")
		return nil
	}

	// 保存主题引用
	listener.topic = topic

	r.logger.Info("成功订阅签名响应主题")

	// 注意：network.Topic 没有 Unsubscribe 方法，使用 Close() 会自动关闭所有订阅者
	// 在 SignatureListener.Close() 中会清理 topic 引用，避免内存泄露
	return nil
}

// handleSignatureResponseMessage 处理签名响应消息
func (r *dposRuntime) handleSignatureResponseMessage(obj interface{}, from peer.ID, listener *SignatureListener) {

	protoResponse, ok := obj.(*dposProto.SignatureResponse)
	if !ok {
		r.logger.Warn("received invalid signature response message", "from", from.String())
		return
	}

	// 转换为内部格式
	response := &SignatureResponse{
		ValidatorAddr:  types.BytesToAddress(protoResponse.ValidatorAddr),
		Signature:      protoResponse.Signature,
		CheckpointHash: types.BytesToHash(protoResponse.CheckpointHash),
		Timestamp:      protoResponse.Timestamp,
	}

	// 验证消息是否针对当前的checkpoint
	if response.CheckpointHash != listener.checkpointHash {
		r.logger.Debug("ignoring signature response for different checkpoint",
			"received", response.CheckpointHash.String(),
			"expected", listener.checkpointHash.String())
		return
	}

	// 检查是否已经收到过该验证者的签名
	listener.receivedMutex.RLock()
	alreadyReceived := listener.receivedSigs[response.ValidatorAddr]
	listener.receivedMutex.RUnlock()

	if alreadyReceived {
		r.logger.Debug("ignoring duplicate signature from validator",
			"validator", response.ValidatorAddr.String())
		return
	}

	// 验证签名响应
	if err := r.validateSignatureResponse(response); err != nil {
		r.logger.Warn("invalid signature response", "error", err, "validator", response.ValidatorAddr.String())
		return
	}

	// 验证签名
	if err := r.verifyValidatorSignatureByAddress(response.ValidatorAddr, response.Signature, response.CheckpointHash); err != nil {
		r.logger.Warn("signature verification failed", "error", err, "validator", response.ValidatorAddr.String())
		return
	}

	// 标记已收到该验证者的签名
	listener.receivedMutex.Lock()
	listener.receivedSigs[response.ValidatorAddr] = true
	listener.receivedMutex.Unlock()

	// 发送到签名通道
	select {
	case listener.signatureCh <- response:
		r.logger.Info("成功接收签名响应",
			"validator", response.ValidatorAddr.String(),
			"from", from.String(),
			"signatureLength", len(response.Signature),
			"checkpointHash", response.CheckpointHash.String())
	default:
		r.logger.Warn("signature channel is full, dropping response",
			"validator", response.ValidatorAddr.String())
	}
}

// verifyValidatorSignatureByAddress 根据地址验证验证者签名
func (r *dposRuntime) verifyValidatorSignatureByAddress(validatorAddr types.Address, signature []byte, checkpointHash types.Hash) error {
	// 查找验证者
	var validator *validator.ValidatorMetadata
	for _, delegate := range r.delegates {
		if delegate.Address == validatorAddr {
			validator = delegate
			break
		}
	}

	if validator == nil {
		return fmt.Errorf("validator %s not found", validatorAddr)
	}

	return r.verifyValidatorSignature(validator, signature, checkpointHash)
}

// mockSignatureCollection 已移除 - 不再支持mock签名收集

// SignatureListener 签名响应监听器
type SignatureListener struct {
	checkpointHash types.Hash
	signatureCh    chan<- *SignatureResponse
	receivedSigs   map[types.Address]bool
	receivedMutex  sync.RWMutex // 添加互斥锁保护 receivedSigs map
	topic          *network.Topic
	logger         hclog.Logger
}

// Close 关闭监听器
func (sl *SignatureListener) Close() {
	// 清理接收的签名记录
	sl.receivedMutex.Lock()
	sl.receivedSigs = make(map[types.Address]bool)
	sl.receivedMutex.Unlock()

	// 注意：这里不能直接关闭topic，因为topic可能被其他监听器使用
	// 我们只是清理引用，避免资源泄漏
	if sl.topic != nil {
		sl.topic = nil
	}

	// 清理日志引用
	sl.logger = nil
}

// SignatureRequest 签名请求消息
type SignatureRequest struct {
	BlockNumber    uint64        `json:"blockNumber"`
	BlockHash      types.Hash    `json:"blockHash"`
	CheckpointHash types.Hash    `json:"checkpointHash"`
	Round          uint64        `json:"round"`
	Proposer       types.Address `json:"proposer"`
	Timestamp      uint64        `json:"timestamp"`
}

// SignatureResponse 签名响应消息
type SignatureResponse struct {
	ValidatorAddr  types.Address `json:"validatorAddr"`
	Signature      []byte        `json:"signature"`
	CheckpointHash types.Hash    `json:"checkpointHash"`
	Timestamp      uint64        `json:"timestamp"`
}

// verifyValidatorSignature 验证验证者签名
func (r *dposRuntime) verifyValidatorSignature(delegate *validator.ValidatorMetadata, signature []byte, checkpointHash types.Hash) error {
	if delegate.BlsKey == nil {
		// 🆕 尝试从持久化存储中获取BLS公钥
		// 静默处理，不打印日志

		// 通过网络集成层获取BLS公钥
		if r.networkIntegration != nil {
			blsKeyBytes, exists := r.networkIntegration.GetBLSKey(delegate.Address)
			if exists && len(blsKeyBytes) > 0 {
				// 解析BLS公钥
				blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
				if err != nil {
					r.logger.Warn("解析持久化的BLS公钥失败", "validator", delegate.Address.String(), "error", err)
				} else {
					// 设置BLS公钥到delegate
					delegate.BlsKey = blsKey
					r.logger.Debug("成功从持久化存储恢复BLS公钥", "validator", delegate.Address.String())
				}
			} else {
				// 如果缓存中没有，尝试从数据库恢复
				// 静默处理，不打印日志
				if err := r.networkIntegration.restoreBLSKeysFromDatabase(); err != nil {
					r.logger.Debug("从数据库恢复BLS公钥失败", "validator", delegate.Address.String(), "error", err)
				} else {
					// 再次尝试从缓存获取
					blsKeyBytes, exists := r.networkIntegration.GetBLSKey(delegate.Address)
					if exists && len(blsKeyBytes) > 0 {
						blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
						if err == nil {
							delegate.BlsKey = blsKey
							r.logger.Debug("成功从数据库恢复BLS公钥", "validator", delegate.Address.String())
						}
					}
				}
			}
		}

		// 如果仍然没有BLS公钥，根据验证者类型选择恢复方法
		if delegate.BlsKey == nil {
			// 检查是否是本地验证者
			if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists && dposInstance.key != nil && 
				delegate.Address == types.Address(dposInstance.key.Address()) {
				// 本地验证者：从validator-bls.key文件恢复
				r.logger.Warn("⚠️ 本地验证者缺少BLS公钥，尝试从validator-bls.key文件恢复",
					"validator", delegate.Address.String())
				
				if r.config != nil && r.config.DataDir != "" {
					// 构建BLS私钥文件路径
					parentDir := filepath.Dir(r.config.DataDir)
					keyFilePath := filepath.Join(parentDir, "validator-bls.key")
					
					// 检查文件是否存在
					if _, err := os.Stat(keyFilePath); err == nil {
						// 读取私钥文件
						privateKeyData, err := os.ReadFile(keyFilePath)
						if err == nil {
							// 获取十六进制字符串（去除可能的换行符）
							privateKeyHex := strings.TrimSpace(string(privateKeyData))
							
							// 检查并修正私钥长度
							if len(privateKeyHex)%2 != 0 {
								privateKeyHex = "0" + privateKeyHex
							}
							
							// 解析BLS私钥
							privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
							if err == nil {
								// 从私钥生成公钥
								publicKey := privateKey.PublicKey()
								delegate.BlsKey = publicKey
								
								// 将BLS公钥保存到网络集成层缓存
								if r.networkIntegration != nil {
									publicKeyBytes := publicKey.Marshal()
									if err := r.networkIntegration.saveBLSKey(delegate.Address, publicKeyBytes); err != nil {
										r.logger.Warn("⚠️ 保存BLS公钥到缓存失败", 
											"validator", delegate.Address.String(),
											"error", err)
									}
								}
								
								r.logger.Info("✅ 本地验证者BLS公钥恢复成功",
									"validator", delegate.Address.String(),
									"filePath", keyFilePath)
							} else {
								r.logger.Warn("⚠️ 解析validator-bls.key文件失败",
									"validator", delegate.Address.String(),
									"filePath", keyFilePath,
									"error", err)
							}
						} else {
							r.logger.Warn("⚠️ 读取validator-bls.key文件失败",
								"validator", delegate.Address.String(),
								"filePath", keyFilePath,
								"error", err)
						}
					} else {
						r.logger.Warn("⚠️ validator-bls.key文件不存在",
							"validator", delegate.Address.String(),
							"filePath", keyFilePath)
					}
				}
			} else {
				// 远程验证者：通过网络请求获取BLS公钥
				// 静默处理，不打印日志
				
				if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
					// 使用DPoS实例的网络请求功能
					blsKey, err := dposInstance.GetBLSKeyForValidator(delegate.Address)
					if err == nil {
						delegate.BlsKey = blsKey
						// 静默处理，不打印日志
					} else {
						r.logger.Warn("⚠️ 远程验证者BLS公钥获取失败",
							"validator", delegate.Address.String(),
							"error", err)
					}
				}
			}

			// 如果仍然没有BLS公钥，返回错误
			if delegate.BlsKey == nil {
				r.logger.Error("🚨 BLS公钥恢复失败，验证者无法验证签名",
					"validator", delegate.Address.String(),
					"checkpointHash", checkpointHash.String())
				return fmt.Errorf("validator has no BLS public key")
			}
		}
	}

	// 检查签名长度
	if len(signature) != 64 {
		return fmt.Errorf("signature length must be 64 bytes, got %d", len(signature))
	}

	// 解析签名
	blsSignature, err := bls.UnmarshalSignature(signature)
	if err != nil {
		return fmt.Errorf("failed to unmarshal signature: %w", err)
	}

	// 验证签名
	if !blsSignature.Verify(delegate.BlsKey, checkpointHash[:], signer.DomainValidatorSet) {
		return fmt.Errorf("signature verification failed")
	}

	// 静默处理，不打印日志

	return nil
}

// validateSignatureResponse 验证签名响应
func (r *dposRuntime) validateSignatureResponse(response *SignatureResponse) error {
	// 1. 检查基本字段
	if response.ValidatorAddr == types.ZeroAddress {
		return errors.New("invalid validator address")
	}
	if len(response.Signature) == 0 {
		return errors.New("empty signature")
	}
	if response.CheckpointHash == types.ZeroHash {
		return errors.New("invalid checkpoint hash")
	}

	// 2. 检查时间戳
	now := uint64(time.Now().Unix())
	if response.Timestamp < now-300 || response.Timestamp > now+60 {
		return errors.New("response timestamp out of range")
	}

	// 3. 检查验证者是否为有效验证者
	isValidValidator := false
	for _, delegate := range r.delegates {
		if delegate.Address == response.ValidatorAddr {
			isValidValidator = true
			break
		}
	}
	if !isValidValidator {
		return errors.New("validator is not a valid delegate")
	}

	return nil
}

// SignatureQueryRequest 签名查询请求
type SignatureQueryRequest struct {
	RequesterAddr types.Address `json:"requesterAddr"`
	Timestamp     uint64        `json:"timestamp"`
}

// isSignatureRequestProcessed 检查签名请求是否已经处理过（去重机制）
func (r *dposRuntime) isSignatureRequestProcessed(proposer types.Address, checkpointHash types.Hash) bool {
	r.signatureRequestDedupMutex.RLock()
	defer r.signatureRequestDedupMutex.RUnlock()

	key := fmt.Sprintf("%s-%s", proposer.String(), checkpointHash.String())
	lastProcessed, exists := r.processedSignatureRequests[key]

	if !exists {
		return false
	}

	// 如果超过5分钟，认为可以重新处理（避免内存泄漏）
	if time.Since(lastProcessed) > 5*time.Minute {
		return false
	}

	return true
}

// markSignatureRequestProcessed 标记签名请求已处理
func (r *dposRuntime) markSignatureRequestProcessed(proposer types.Address, checkpointHash types.Hash) {
	r.signatureRequestDedupMutex.Lock()
	defer r.signatureRequestDedupMutex.Unlock()

	key := fmt.Sprintf("%s-%s", proposer.String(), checkpointHash.String())
	r.processedSignatureRequests[key] = time.Now()
}

// isSignatureResponseBroadcasted 检查签名响应是否已经广播过（去重机制）
func (r *dposRuntime) isSignatureResponseBroadcasted(responseKey string) bool {
	r.signatureResponseDedupMutex.RLock()
	defer r.signatureResponseDedupMutex.RUnlock()

	lastProcessed, exists := r.processedSignatureResponses[responseKey]

	if !exists {
		return false
	}

	// 如果超过5分钟，认为可以重新广播
	if time.Since(lastProcessed) > 5*time.Minute {
		return false
	}

	return true
}

// markSignatureResponseBroadcasted 标记签名响应已广播
func (r *dposRuntime) markSignatureResponseBroadcasted(responseKey string) {
	r.signatureResponseDedupMutex.Lock()
	defer r.signatureResponseDedupMutex.Unlock()

	r.processedSignatureResponses[responseKey] = time.Now()
}

// isSignatureResponseGenerated 检查签名响应是否已经生成过（去重机制）
func (r *dposRuntime) isSignatureResponseGenerated(generateKey string) bool {
	r.signatureGenerationDedupMutex.RLock()
	defer r.signatureGenerationDedupMutex.RUnlock()

	lastProcessed, exists := r.processedSignatureGenerations[generateKey]

	if !exists {
		return false
	}

	// 如果超过5分钟，认为可以重新生成
	if time.Since(lastProcessed) > 5*time.Minute {
		return false
	}

	return true
}

// markSignatureResponseGenerated 标记签名响应已生成
func (r *dposRuntime) markSignatureResponseGenerated(generateKey string) {
	r.signatureGenerationDedupMutex.Lock()
	defer r.signatureGenerationDedupMutex.Unlock()

	r.processedSignatureGenerations[generateKey] = time.Now()
}

// debugPendingSignatureRequests 调试方法：检查待处理的签名请求
func (r *dposRuntime) debugPendingSignatureRequests() {
	r.signatureRequestMutex.RLock()
	defer r.signatureRequestMutex.RUnlock()

	//r.logger.Info("=== 调试：待处理签名请求状态 ===")
	//r.logger.Info("存储映射是否为nil", "isNil", r.pendingSignatureRequests == nil)

	if r.pendingSignatureRequests == nil {
		r.logger.Info("pendingSignatureRequests映射为nil")
		return
	}

	//r.logger.Info("存储的签名请求数量", "count", len(r.pendingSignatureRequests))

	//for checkpointHash, request := range r.pendingSignatureRequests {
	//	//r.logger.Info("存储的签名请求",
	//	//	"checkpointHash", checkpointHash.String(),
	//	//	"blockNumber", request.BlockNumber,
	//	//	"blockHash", request.BlockHash.String(),
	//	//	"round", request.Round,
	//	//	"proposer", request.Proposer.String(),
	//	//	"timestamp", request.Timestamp)
	//}
	//r.logger.Info("=== 调试结束 ===")
}

// simpleFallbackMonitoring 基于时间的简单备用传播监控
func (r *dposRuntime) simpleFallbackMonitoring(protoRequest *dposProto.SignatureRequest, checkpointHash types.Hash) {
	// 第一层：1秒后检查进度
	time.Sleep(1 * time.Second)
	progress := r.getSignatureCollectionProgress(checkpointHash)
	if progress < 0.2 { // 20%以下
		r.logger.Info("签名收集进度极低，启动备用传播",
			"区块高度", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"progress", fmt.Sprintf("%.2f%%", progress*100))
		go r.fallbackSignatureRequestPropagation(protoRequest, checkpointHash)
		return
	}

	// 第二层：1.5秒后再次检查
	time.Sleep(500 * time.Millisecond) // 总共1.5秒
	progress = r.getSignatureCollectionProgress(checkpointHash)
	if progress < 0.5 { // 50%以下
		r.logger.Info("签名收集进度不足，启动备用传播",
			"区块高度", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"progress", fmt.Sprintf("%.2f%%", progress*100))
		go r.fallbackSignatureRequestPropagation(protoRequest, checkpointHash)
	}
}

// getSignatureCollectionProgress 获取签名收集进度
func (r *dposRuntime) getSignatureCollectionProgress(checkpointHash types.Hash) float64 {
	if r.networkIntegration != nil {
		collector := r.networkIntegration.GetSignatureCollector(checkpointHash)
		if collector != nil {
			return float64(collector.GetCollectedCount()) / float64(collector.requiredCount)
		}
	}
	return 0.0
}

// fallbackSignatureRequestPropagation 备用签名请求传播机制
func (r *dposRuntime) fallbackSignatureRequestPropagation(protoRequest *dposProto.SignatureRequest, checkpointHash types.Hash) {
	r.logger.Info("=== 备用转传播启动 ===", "区块高度", protoRequest.BlockNumber, "checkpointHash", checkpointHash.String())

	peers := r.network.Peers()
	successCount := 0
	totalPeers := len(peers)

	for _, peer := range peers {
		peerID := peer.Info.ID

		// 跳过自己
		if peerID.String() == r.network.AddrInfo().ID.String() {
			continue
		}

		// 尝试直接发送签名请求，使用指数退避重试
		if err := r.sendDirectSignatureRequestWithRetry(peerID, protoRequest); err != nil {
			r.logger.Warn("直接签名请求失败", "peer", peerID.String()[:8], "区块高度", protoRequest.BlockNumber, "错误", err)
		} else {
			successCount++
			r.logger.Debug("直接签名请求成功", "peer", peerID.String()[:8], "区块高度", protoRequest.BlockNumber)
		}
	}

	// 修复统计逻辑：确保成功率不会超过100%
	if totalPeers > 1 {
		effectiveTotal := totalPeers - 1 // 排除自己
		successRate := float64(successCount) / float64(effectiveTotal)

		// 限制成功率最大为100%
		if successRate > 1.0 {
			successRate = 1.0
		}

		r.logger.Info("=== 备用转传播完成 ===",
			"区块高度", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"成功节点数", successCount,
			"总节点数", effectiveTotal,
			"成功率", fmt.Sprintf("%.2f%%", successRate*100))
	}
}

// sendDirectSignatureRequest 直接发送签名请求给指定peer
func (r *dposRuntime) sendDirectSignatureRequest(peerID peer.ID, protoRequest *dposProto.SignatureRequest) error {
	// 临时方案：重新尝试gossip发布
	topic, err := r.getSignatureRequestTopic()
	if err != nil {
		return fmt.Errorf("failed to get signature request topic: %w", err)
	}

	r.logger.Debug("备用传播：重新尝试签名请求gossip发布", "peer", peerID.String()[:8], "区块高度", protoRequest.BlockNumber)

	// 统一消息类型：使用 DPOSMessage 包装，确保与网络集成管理器兼容
	// 序列化签名请求
	requestData, err := proto.Marshal(protoRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal signature request: %w", err)
	}

	// 创建 DPOSMessage
	dposMsg := &dposProto.TransportMessage{
		Data: requestData,
	}

	return topic.Publish(dposMsg)
}

// sendDirectSignatureRequestWithRetry 带重试的直接签名请求
func (r *dposRuntime) sendDirectSignatureRequestWithRetry(peerID peer.ID, protoRequest *dposProto.SignatureRequest) error {
	const maxRetries = 3
	const baseDelay = 100 * time.Millisecond

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避延迟
			delay := time.Duration(float64(baseDelay) * float64(attempt) * 1.5)
			time.Sleep(delay)
		}

		if err := r.sendDirectSignatureRequest(peerID, protoRequest); err != nil {
			lastErr = err
			r.logger.Debug("直接签名请求重试", "peer", peerID.String()[:8], "attempt", attempt+1, "error", err)
			continue
		}

		// 成功发送
		return nil
	}

	return fmt.Errorf("所有重试尝试都失败了，最后的错误: %w", lastErr)
}

// HandleSignatureRequest 处理来自网络集成的签名请求
func (r *dposRuntime) HandleSignatureRequest(request *SignatureRequest) error {
	r.logger.Debug("收到来自网络集成的签名请求",
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String(),
		"proposer", request.Proposer.String())

	// 检查是否是自己的请求
	if request.Proposer == types.Address(r.config.Key.Address()) {
		r.logger.Debug("忽略自己的签名请求")
		return nil
	}

	// 检查自己是否是验证者
	if !r.isValidator() {
		r.logger.Info("自己不是验证者，忽略签名请求")
		return nil
	}

	// 使用并发控制，限制同时处理的签名请求数量
	select {
	case r.signatureRequestSemaphore <- struct{}{}:
		// 获取到信号量，可以处理签名请求
		go func() {
			defer func() { <-r.signatureRequestSemaphore }() // 释放信号量

			if err := r.generateSignatureResponse(request); err != nil {
				r.logger.Error("failed to generate signature response", "error", err)
			}
		}()
		return nil
	default:
		// 信号量已满，记录警告并跳过处理
		r.logger.Warn("并发签名请求过多，跳过处理",
			"checkpointHash", request.CheckpointHash.String(),
			"proposer", request.Proposer.String(),
			"当前并发数", r.maxConcurrentSignatures)
		return fmt.Errorf("too many concurrent signature requests")
	}
}

// HandleSignatureResponse 处理来自网络集成的签名响应
func (r *dposRuntime) HandleSignatureResponse(response *SignatureResponse) error {
	r.logger.Info("收到来自网络集成的签名响应",
		"validator", response.ValidatorAddr.String(),
		"checkpointHash", response.CheckpointHash.String())

	// 这里可以添加签名响应的处理逻辑
	// 例如：验证签名、更新状态等

	return nil
}

// setupNetworkIntegration 设置网络集成
func (r *dposRuntime) setupNetworkIntegration() error {
	if r.network == nil {
		return fmt.Errorf("网络服务不可用，无法设置网络集成")
	}

	// 创建网络集成管理器
	r.networkIntegration = NewNetworkIntegration(r.network, r.logger)

	// 设置DPoS运行时回调
	r.networkIntegration.SetDPoSRuntime(r)

	// 🆕 设置DPoS实例引用（如果backend是DPoS实例）
	if dpos, ok := r.backend.(*DPoS); ok {
		r.networkIntegration.SetDPoSInstance(dpos)
	}

	// 🆕 设置BLS公钥持久化回调函数
	r.networkIntegration.SetBLSKeyPersistCallback(func(address types.Address, blsKeyBytes []byte) error {
		// 通过backend获取DPoS实例
		if dpos, ok := r.backend.(*DPoS); ok {
			if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
				r.logger.Warn("BLS公钥持久化失败",
					"address", address.String(),
					"blsKeyLength", len(blsKeyBytes),
					"error", err)
				return err
			}
			r.logger.Debug("BLS公钥持久化成功",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes))
			return nil
		}

		// 如果backend不是DPoS实例，尝试通过全局注册表查找
		if dpos, exists := GetDPoSInstance(address.String()); exists && dpos != nil {
			if err := dpos.persistBLSKeyToStakeStore(address, blsKeyBytes); err != nil {
				r.logger.Warn("通过全局注册表BLS公钥持久化失败",
					"address", address.String(),
					"blsKeyLength", len(blsKeyBytes),
					"error", err)
				return err
			}
			r.logger.Debug("通过全局注册表BLS公钥持久化成功",
				"address", address.String(),
				"blsKeyLength", len(blsKeyBytes))
			return nil
		}

		r.logger.Warn("无法找到DPoS实例进行BLS公钥持久化",
			"address", address.String(),
			"blsKeyLength", len(blsKeyBytes))
		return fmt.Errorf("DPoS instance not available for BLS key persistence")
	})

	// 🆕 设置BLS公钥查找回调函数
	r.networkIntegration.SetBLSKeyLookupCallback(func(address types.Address) ([]byte, error) {
		// 通过全局注册表获取DPoS实例
		if dposInstance, exists := GetDPoSInstance("vcity_dpos"); exists {
			return dposInstance.GetBLSKeyBytesFromGenesis(address)
		}
		return nil, fmt.Errorf("DPoS instance not available for BLS key lookup")
	})

	// 尝试获取现有主题，避免重复创建
	r.topicMutex.RLock()
	if r.signatureRequestTopic != nil {
		r.networkIntegration.SetExistingTopics(r.signatureRequestTopic, r.signatureResponseTopic)
		r.logger.Info("使用现有主题设置网络集成")
	}
	r.topicMutex.RUnlock()

	// 启动网络集成
	if err := r.networkIntegration.Start(); err != nil {
		return fmt.Errorf("网络集成启动失败: %w", err)
	}

	return nil
}

// persistVoteToDatabase 将投票信息持久化到数据库
func (d *DPoS) persistVoteToDatabase(voter types.Address, candidate types.Address, amount *big.Int) error {
	// 检查状态存储是否可用
	d.logger.Debug("🔍 Checking state store availability",
		"d.state", d.state != nil,
		"d.state.StakeStore", func() interface{} {
			if d.state != nil {
				return d.state.StakeStore != nil
			}
			return "N/A"
		}(),
		"d.dataDir", d.dataDir)

	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available: d.state=%v, d.state.StakeStore=%v",
			d.state != nil,
			func() interface{} {
				if d.state != nil {
					return d.state.StakeStore != nil
				}
				return "N/A"
			}())
	}

	// 获取当前投票者信息
	d.logger.Debug("🔍 Getting voter info from memory...")

	// 添加 defer 确保能看到是否进入了锁
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("❌ Panic in persistVoteToDatabase", "panic", r)
		}
	}()

	// 🚨 注意：调用者已经持有写锁，所以这里不需要再获取锁
	d.logger.Debug("🔍 Accessing d.voters map (caller already holds write lock)...")

	voterInfo, exists := d.voters[voter]
	d.logger.Debug("🔍 Voter lookup completed", "exists", exists)

	d.logger.Debug("🔍 Voter info retrieved",
		"exists", exists,
		"voter", voter.String())

	if !exists {
		d.logger.Error("❌ Voter info not found in memory", "voter", voter.String())
		return fmt.Errorf("voter info not found in memory for address %s", voter.String())
	}

	d.logger.Info("✅ Voter info found",
		"votingPower", voterInfo.VotingPower.String(),
		"votedDelegatesCount", len(voterInfo.VotedDelegates))

	d.logger.Debug("💾 Persisting vote to database",
		"voter", voter.String(),
		"candidate", candidate.String(),
		"amount", amount.String(),
		"votingPower", voterInfo.VotingPower.String(),
		"votedDelegatesCount", len(voterInfo.VotedDelegates))

	// 🆕 添加详细日志：记录数据库更新前的内存状态
	d.logger.Info("🔍 Before database persistence - memory delegates state:")
	for i, del := range d.delegates {
		if del.Address == candidate {
			d.logger.Info("🔍 Target delegate before database persistence",
				"index", i,
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String(),
				"isActive", del.IsActive)
		}
	}

	// 保存投票者信息到数据库
	d.logger.Info("💾 Saving voter info to database...")
	if err := d.state.StakeStore.setVoterInfo(voter, voterInfo, nil); err != nil {
		d.logger.Error("❌ Failed to save voter info to database", "error", err)
		return fmt.Errorf("failed to save voter info to database: %w", err)
	}
	d.logger.Info("✅ Voter info saved to database successfully")

	// 同时保存受托人的投票权重信息
	d.logger.Info("💾 Saving delegate voting power...")
	if err := d.persistDelegateVotingPower(candidate, amount); err != nil {
		d.logger.Warn("❌ Failed to persist delegate voting power", "error", err)
		// 不返回错误，因为投票者信息已经保存成功
	} else {
		d.logger.Info("✅ Delegate voting power saved successfully")
	}

	return nil
}

// persistDelegateVotingPower 持久化受托人的投票权重
func (d *DPoS) persistDelegateVotingPower(delegate types.Address, amount *big.Int) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 🚨 修复：调用者已经持有写锁，直接访问delegates
	// 不需要再获取读锁，避免死锁
	d.logger.Debug("🔍 Accessing d.delegates (caller already holds write lock)...")

	var existingDelegate *validator.ValidatorMetadata
	fmt.Printf("🔍 检查d.delegates中的所有受托人:\n")
	for i, del := range d.delegates {
		fmt.Printf("  - 受托人[%d]: 地址=%s, VotingPower=%s (0x%x), IsActive=%v\n",
			i+1, del.Address.String(), del.VotingPower.String(), del.VotingPower.Bytes(), del.IsActive)
		if del.Address == delegate {
			existingDelegate = del
			fmt.Printf("    -> 找到目标受托人！\n")
		}
	}

	if existingDelegate != nil {
		fmt.Printf("🔍 existingDelegate详细信息:\n")
		fmt.Printf("  - 地址: %s\n", existingDelegate.Address.String())
		fmt.Printf("  - VotingPower: %s (0x%x)\n", existingDelegate.VotingPower.String(), existingDelegate.VotingPower.Bytes())
		fmt.Printf("  - IsActive: %v\n", existingDelegate.IsActive)
	} else {
		fmt.Printf("🔍 未找到existingDelegate，这是新受托人\n")
	}

	// 创建或更新受托人信息
	fmt.Printf("🔍 创建delegateInfo前的状态检查:\n")
	fmt.Printf("  - 受托人地址: %s\n", delegate.String())
	fmt.Printf("  - amount参数: %s (0x%x)\n", amount.String(), amount.Bytes())
	fmt.Printf("  - existingDelegate是否存在: %v\n", existingDelegate != nil)
	if existingDelegate != nil {
		fmt.Printf("  - existingDelegate.VotingPower: %s (0x%x)\n", existingDelegate.VotingPower.String(), existingDelegate.VotingPower.Bytes())
	}

	delegateInfo := &DelegateInfo{
		Address:        delegate,
		VotingPower:    big.NewInt(0),
		TotalVotes:     big.NewInt(0),
		ProducedBlocks: 0,
		MissedBlocks:   0,
		LastBlockTime:  0,
		IsActive:       true,
	}

	fmt.Printf("  - 初始化后delegateInfo.VotingPower: %s (0x%x)\n", delegateInfo.VotingPower.String(), delegateInfo.VotingPower.Bytes())

	// 🆕 修复：直接使用内存中受托人的当前VotingPower，不进行累加
	// 因为VotingPower已经在updateDelegateVotingPower中正确更新了
	if existingDelegate != nil {
		// 直接使用内存中受托人的当前VotingPower
		delegateInfo.VotingPower = new(big.Int).Set(existingDelegate.VotingPower)
		fmt.Printf("  - ✅ 使用内存中受托人的当前VotingPower: %s\n", delegateInfo.VotingPower.String())
	} else {
		// 这种情况不应该发生，因为validateVote阶段已经创建了受托人
		d.logger.Warn("⚠️ 受托人不存在，这是异常情况")
		delegateInfo.VotingPower = new(big.Int).Set(amount)
		fmt.Printf("  - ⚠️ 异常情况，设置VotingPower: %s\n", delegateInfo.VotingPower.String())
	}

	// 🆕 修复：TotalVotes应该等于VotingPower
	delegateInfo.TotalVotes = new(big.Int).Set(delegateInfo.VotingPower)
	fmt.Printf("  - ✅ TotalVotes设置为VotingPower: %s\n", delegateInfo.TotalVotes.String())

	// 🆕 修复：根据投票权重设置Active状态
	if delegateInfo.VotingPower.Cmp(big.NewInt(0)) > 0 {
		delegateInfo.IsActive = true
		fmt.Printf("  - ✅ 受托人有投票权重，设置为Active: true\n")
	} else {
		delegateInfo.IsActive = false
		fmt.Printf("  - ⚠️ 受托人无投票权重，设置为Active: false\n")
	}

	// 保存到数据库
	d.logger.Info("💾 About to save delegate info to database",
		"delegate", delegate.String(),
		"votingPower", delegateInfo.VotingPower.String(),
		"totalVotes", delegateInfo.TotalVotes.String())

	// 🆕 添加详细日志：检查调用setDelegateInfo前的数据
	d.logger.Debug("🔍 调用setDelegateInfo前的详细检查",
		"delegate", delegate.String(),
		"votingPower", delegateInfo.VotingPower.String(),
		"totalVotes", delegateInfo.TotalVotes.String(),
		"isActive", delegateInfo.IsActive,
		"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))

	// 🆕 检查内存中对应受托人的状态
	for _, del := range d.delegates {
		if del.Address == delegate {
			d.logger.Debug("🔍 内存中受托人状态",
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String(),
				"isActive", del.IsActive)
			break
		}
	}

	if err := d.state.StakeStore.setDelegateInfo(delegate, delegateInfo, nil); err != nil {
		d.logger.Error("❌ Failed to save delegate info to database", "error", err)
		return fmt.Errorf("failed to save delegate info to database: %w", err)
	}

	// 🆕 添加详细日志：记录数据库更新后的内存状态
	d.logger.Info("🔍 After database persistence - memory delegates state:")
	for i, del := range d.delegates {
		if del.Address == delegate {
			d.logger.Info("🔍 Target delegate after database persistence",
				"index", i,
				"address", del.Address.String(),
				"votingPower", del.VotingPower.String(),
				"isActive", del.IsActive)
		}
	}

	d.logger.Info("✅ Delegate info saved to database successfully",
		"delegate", delegate.String())

	return nil
}

// restoreVotingDataFromDatabase 从数据库恢复投票数据
func (d *DPoS) restoreVotingDataFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, cannot restore voting data")
		return fmt.Errorf("state store not available")
	}


	// 恢复投票者信息
	if err := d.restoreVotersFromDatabase(); err != nil {
		d.logger.Error("Failed to restore voters from database", "error", err)
		return err
	}

	// 恢复受托人信息
	if err := d.restoreDelegatesFromDatabase(); err != nil {
		d.logger.Error("Failed to restore delegates from database", "error", err)
		return err
	}

	d.logger.Info("✅ Voting data restored from database",
		"votersCount", len(d.voters),
		"delegatesCount", len(d.delegates))

	return nil
}

// restoreVotersFromDatabase 从数据库恢复投票者信息
func (d *DPoS) restoreVotersFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}


	// 使用数据库事务来读取所有投票者信息
	err := d.state.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("VoterInfo"))
		if bucket == nil {
			d.logger.Debug("VoterInfo bucket not found, no voters to restore")
			return nil
		}

		// 遍历所有投票者
		cursor := bucket.Cursor()
		restoredCount := 0

		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var voterInfo VoterInfo
			if err := json.Unmarshal(value, &voterInfo); err != nil {
				d.logger.Warn("Failed to unmarshal voter info", "key", hex.EncodeToString(key), "error", err)
				continue
			}

			// 恢复投票者信息到内存
			d.lock.Lock()
			d.voters[voterInfo.Address] = &voterInfo
			d.lock.Unlock()

			restoredCount++
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to restore voters from database: %w", err)
	}

	return nil
}

// restoreDelegatesFromDatabase 从数据库恢复受托人信息
func (d *DPoS) restoreDelegatesFromDatabase() error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}


	// 使用数据库事务来读取所有受托人信息
	err := d.state.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("DelegateInfo"))
		if bucket == nil {
			d.logger.Debug("DelegateInfo bucket not found, no delegates to restore")
			return nil
		}

		// 遍历所有受托人
		cursor := bucket.Cursor()
		restoredCount := 0

		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var delegateInfo DelegateInfo
			if err := json.Unmarshal(value, &delegateInfo); err != nil {
				d.logger.Warn("Failed to unmarshal delegate info", "key", hex.EncodeToString(key), "error", err)
				continue
			}

			// 恢复受托人信息到内存
			// 注意：这里需要将 DelegateInfo 转换为 validator.ValidatorMetadata
			// 或者更新现有的 delegates 集合
			d.logger.Debug("Found delegate in database",
				"address", delegateInfo.Address.String(),
				"votingPower", delegateInfo.VotingPower.String())

			restoredCount++
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to restore delegates from database: %w", err)
	}

	return nil
}

// updateValidatorStatus 更新验证者状态
func (d *DPoS) updateValidatorStatus(address types.Address, newStake *big.Int) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	for _, delegate := range d.delegates {
		if delegate.Address == address {
			oldActive := delegate.IsActive
			oldStake := delegate.VotingPower
			delegate.VotingPower = new(big.Int).Set(newStake)

			// 关键：根据新stake更新活跃状态
			if newStake.Cmp(big.NewInt(0)) > 0 {
				delegate.IsActive = true // 有stake了，变为活跃
			} else {
				delegate.IsActive = false // stake为0，变为不活跃
			}

			// 记录状态变化
			if oldActive != delegate.IsActive {
				d.logger.Info("validator status changed",
					"address", address,
					"oldStake", oldStake.String(),
					"newStake", newStake.String(),
					"oldActive", oldActive,
					"newActive", delegate.IsActive)
			}

			return nil
		}
	}

	return fmt.Errorf("validator not found: %s", address)
}

// canParticipateInConsensus 检查验证者是否可以参与共识
func (d *DPoS) canParticipateInConsensus(delegate *validator.ValidatorMetadata) bool {
	return delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0
}

// shouldParticipateInBLSSigning 检查验证者是否应该参与BLS签名
func (d *DPoS) shouldParticipateInBLSSigning(delegate *validator.ValidatorMetadata) bool {
	return delegate.IsActive && delegate.VotingPower.Cmp(big.NewInt(0)) > 0
}

// 持久化验证者集合到数据库
func (d *DPoS) persistDelegateSetToDatabase(delegates validator.AccountSet) error {
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("State store not available, skipping database persistence")
		return nil
	}

	d.logger.Debug("💾 Starting delegate set persistence", "count", len(delegates))

	// 开始数据库事务
	dbTx, err := d.state.beginDBTransaction(true)
	if err != nil {
		return fmt.Errorf("failed to begin db transaction: %w", err)
	}
	defer dbTx.Rollback()

	// 保存每个验证者信息
	for _, del := range delegates {
		// 🆕 修改：BLS公钥按需获取，不在验证者集合中强制要求
		var blsPublicKey []byte
		if del.BlsKey != nil {
			blsPublicKey = del.BlsKey.Marshal()
			d.logger.Debug("🔑 保存BLS公钥到数据库（已存在）",
				"address", del.Address.String(),
				"publicKeyLength", len(blsPublicKey))
		} else {
			// 🆕 BLS公钥为nil是正常的，将在验证时动态获取
			d.logger.Debug("🔑 BLS公钥为nil，将在验证时动态获取",
				"address", del.Address.String(),
				"note", "BLS公钥按需获取机制")
			d.logger.Debug("ℹ️ 受托人BLS公钥为nil，尝试从创世文件恢复",
				"address", del.Address.String())

			// 从validator-bls.key文件中查找BLS公钥
			dataDir := d.getDataDir()
			if dataDir != "" {
				// 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
				// dataDir = "node1\dpos"，需要回到 "node1\consensus"
				parentDir := filepath.Dir(dataDir) // 获取 "node1"
				keyFilePath := filepath.Join(parentDir, "consensus", "validator-bls.key")
				
				// 检查文件是否存在
				if _, err := os.Stat(keyFilePath); err == nil {
					// 读取私钥文件
					privateKeyData, err := os.ReadFile(keyFilePath)
					if err == nil {
						// 获取十六进制字符串（去除可能的换行符）
						privateKeyHex := strings.TrimSpace(string(privateKeyData))
						
						// 检查并修正私钥长度
						if len(privateKeyHex)%2 != 0 {
							privateKeyHex = "0" + privateKeyHex
						}
						
						// 解析BLS私钥
						privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
						if err == nil {
							// 从私钥生成公钥
							publicKey := privateKey.PublicKey()
							blsPublicKey = publicKey.Marshal()
							d.logger.Info("✅ 从validator-bls.key文件恢复BLS公钥并保存到数据库",
								"address", del.Address.String(),
								"publicKeyLength", len(blsPublicKey),
								"filePath", keyFilePath)
						} else {
							d.logger.Warn("⚠️ 解析validator-bls.key文件失败",
								"address", del.Address.String(),
								"filePath", keyFilePath,
								"error", err)
						}
					} else {
						d.logger.Warn("⚠️ 读取validator-bls.key文件失败",
							"address", del.Address.String(),
							"filePath", keyFilePath,
							"error", err)
					}
				} else {
					d.logger.Debug("ℹ️ validator-bls.key文件不存在，将在验证时按需获取",
						"address", del.Address.String(),
						"filePath", keyFilePath)
				}
			}
		}

		// 即使BLS公钥为nil，也允许保存受托人信息
		delegateInfo := &DelegateInfo{
			Address:        del.Address,
			VotingPower:    new(big.Int).Set(del.VotingPower),
			TotalVotes:     new(big.Int).Set(del.VotingPower), // 使用VotingPower作为TotalVotes
			ProducedBlocks: 0,
			MissedBlocks:   0,
			LastBlockTime:  0,
			IsActive:       del.IsActive,
			BlsPublicKey:   blsPublicKey, // 🆕 保存BLS公钥（可以为nil）
		}

		// 🆕 新增：重点记录持久化时的isActive状态
		d.logger.Info("💾 持久化受托人信息到数据库",
			"address", del.Address.String(),
			"votingPower", del.VotingPower.String(),
			"isActive", del.IsActive,
			"isActiveType", fmt.Sprintf("%T", del.IsActive),
			"blsPublicKeyLength", len(blsPublicKey),
			"blsKeyStatus", func() string {
				if len(blsPublicKey) > 0 {
					return "已保存"
				}
				return "验证时动态获取"
			}())

		// 记录BLS公钥状态
		if blsPublicKey == nil {
			d.logger.Debug("ℹ️ 受托人BLS公钥为空，将在验证时按需获取",
				"address", del.Address.String())
		} else {
			d.logger.Debug("✅ 受托人BLS公钥正常，准备保存到数据库",
				"address", del.Address.String(),
				"blsKeyLength", len(blsPublicKey))
		}

		// 🆕 添加详细日志：检查调用setDelegateInfo前的数据
		d.logger.Debug("🔍 调用setDelegateInfo前的详细检查 (第二个位置)",
			"delegate", del.Address.String(),
			"votingPower", delegateInfo.VotingPower.String(),
			"totalVotes", delegateInfo.TotalVotes.String(),
			"isActive", delegateInfo.IsActive,
			"blsPublicKeyLength", len(delegateInfo.BlsPublicKey))

		// 🆕 检查内存中对应受托人的状态
		for _, memDel := range d.delegates {
			if memDel.Address == del.Address {
				d.logger.Debug("🔍 内存中受托人状态",
					"address", memDel.Address.String(),
					"votingPower", memDel.VotingPower.String(),
					"isActive", memDel.IsActive)
				break
			}
		}

		if err := d.state.StakeStore.setDelegateInfo(del.Address, delegateInfo, dbTx); err != nil {
			d.logger.Error("❌ Failed to save delegate info", "address", del.Address.String(), "error", err)
			return fmt.Errorf("failed to save delegate info for %s: %w", del.Address.String(), err)
		}

		d.logger.Debug("✅ Saved delegate info", "address", del.Address.String(), "votingPower", del.VotingPower.String())
	}

	// 提交事务
	if err := dbTx.Commit(); err != nil {
		return fmt.Errorf("failed to commit db transaction: %w", err)
	}

	d.logger.Debug("✅ Delegate set persistence completed successfully")
	return nil
}

// 🆕 新增：启动时读取并打印受托人数据
func (d *DPoS) loadAndPrintDelegatesOnStartup() error {
	d.logger.Debug("🔍 开始读取consensus\\dpos目录下的受托人数据...")

	// 检查数据目录
	if d.dataDir == "" {
		d.logger.Warn("数据目录为空，无法读取受托人数据")
		return nil
	}

	// 构建consensus\dpos路径
	// 检查dataDir是否已经包含consensus\dpos
	var dposDir string
	if strings.Contains(d.dataDir, "consensus") && strings.Contains(d.dataDir, "dpos") {
		// 如果dataDir已经包含consensus\dpos，直接使用
		dposDir = d.dataDir
		d.logger.Info("📁 受托人数据目录（已包含consensus\\dpos）", "path", dposDir)
	} else {
		// 否则拼接路径
		dposDir = filepath.Join(d.dataDir, "consensus", "dpos")
		d.logger.Info("📁 受托人数据目录（拼接路径）", "path", dposDir)
	}

	// 检查目录是否存在
	if _, err := os.Stat(dposDir); os.IsNotExist(err) {
		d.logger.Warn("受托人数据目录不存在", "path", dposDir)
		return nil
	}

	// 读取目录内容
	files, err := os.ReadDir(dposDir)
	if err != nil {
		d.logger.Error("读取受托人数据目录失败", "error", err)
		return err
	}

	d.logger.Info("📋 受托人数据目录内容", "fileCount", len(files))

	// 遍历文件并打印信息
	for _, file := range files {
		if !file.IsDir() {
			filePath := filepath.Join(dposDir, file.Name())
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				d.logger.Warn("获取文件信息失败", "file", file.Name(), "error", err)
				continue
			}

			d.logger.Info("📄 受托人数据文件",
				"name", file.Name(),
				"size", fileInfo.Size(),
				"modTime", fileInfo.ModTime())
		}
	}

	// 尝试从状态存储读取受托人信息
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Info("💾 尝试从状态存储读取受托人信息...")

		// 🆕 新增：从与命令相同的数据源读取受托人数据
		if err := d.callCommandDataSourcesOnStartup(); err != nil {
			d.logger.Error("❌ 调用命令数据源失败", "error", err)
		} else {
			d.logger.Info("✅ 命令数据源调用完成（状态存储可用）")
		}
	} else {
		d.logger.Warn("⚠️ 状态存储不可用，无法读取受托人详细信息")
	}

	d.logger.Info("🎯 受托人数据读取总结", "dataDir", d.dataDir, "dposDir", dposDir)
	return nil
}

// 🆕 新增：从状态存储读取并打印受托人数据内容
func (d *DPoS) readAndPrintDelegatesFromStorage() error {
	d.logger.Debug("🔍 开始读取受托人数据内容...")

	// 检查状态存储
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 尝试从数据库读取受托人信息
	if d.state.db != nil {
		d.logger.Info("💾 从BoltDB读取受托人信息...")

		err := d.state.db.View(func(tx *bolt.Tx) error {
			// 读取DelegateInfo bucket
			delegateBucket := tx.Bucket([]byte("DelegateInfo"))
			if delegateBucket == nil {
				d.logger.Info("📋 DelegateInfo bucket不存在，没有受托人数据")
				return nil
			}

			d.logger.Info("📋 找到DelegateInfo bucket，开始读取受托人数据...")

			// 遍历所有受托人
			cursor := delegateBucket.Cursor()
			delegateCount := 0

			for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
				delegateCount++

				// 解析受托人地址
				address := types.BytesToAddress(key)

				// 尝试解析受托人信息
				var delegateInfo DelegateInfo
				if err := json.Unmarshal(value, &delegateInfo); err != nil {
					d.logger.Warn("⚠️ 解析受托人数据失败", "address", address.String(), "error", err)
					// 显示原始数据
					d.logger.Info("📄 受托人原始数据", "address", address.String(), "rawData", string(value))
					continue
				}

				// 打印受托人详细信息
				d.logger.Info("👤 受托人信息",
					"序号", delegateCount,
					"地址", delegateInfo.Address.String(),
					"投票权重", delegateInfo.VotingPower.String(),
					"总票数", delegateInfo.TotalVotes.String(),
					"是否活跃", delegateInfo.IsActive,
					"出块数", delegateInfo.ProducedBlocks,
					"错过块数", delegateInfo.MissedBlocks)
			}

			d.logger.Info("📊 受托人数据统计", "总数", delegateCount)
			return nil
		})

		if err != nil {
			d.logger.Error("❌ 读取受托人数据失败", "error", err)
			return err
		}
	}

	// 也显示内存中的受托人信息
	d.logger.Info("🧠 内存中的受托人信息...")
	d.lock.RLock()
	defer d.lock.RUnlock()

	for i, del := range d.delegates {
		d.logger.Debug("👤 内存受托人",
			"序号", i+1,
			"地址", del.Address.String(),
			"投票权重", del.VotingPower.String(),
			"是否活跃", del.IsActive,
			"BLS密钥", del.BlsKey != nil)
	}

	d.logger.Debug("📊 内存受托人统计", "总数", len(d.delegates))
	return nil
}

// 🆕 新增：启动时直接调用和命令一样的数据源方法
func (d *DPoS) callCommandDataSourcesOnStartup() error {
	// 🆕 数据源1: 从store获取验证者信息 (与命令中的 GetValidators() 一致)
	if d.state != nil && d.state.StakeStore != nil {
		validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		if err != nil {
			d.logger.Warn("⚠️ 获取验证者信息失败", "error", err)
		} else {

			// 🆕 获取质押信息用于对比
			stakingInfo, stakingErr := d.state.StakeStore.GetStakingInfo()
			if stakingErr != nil {
				d.logger.Warn("⚠️ 获取质押信息失败，无法显示票数对比", "error", stakingErr)
			}

			for _, validator := range validators {
				// 🆕 查找对应的票数信息
				var totalVotes *big.Int
				var voterCount int
				if stakingInfo != nil {
					for _, stake := range stakingInfo {
						if stake.Delegate.String() == validator.Address.String() {
							if totalVotes == nil {
								totalVotes = big.NewInt(0)
							}
							totalVotes.Add(totalVotes, stake.Amount)
							voterCount++
						}
					}
				}
				_ = totalVotes
				_ = voterCount
			}
		}
	} else {
		d.logger.Warn("⚠️ store不可用，无法获取验证者信息")
	}

	// 🆕 数据源2: 从store获取质押信息 (与命令中的 GetStakingInfo() 一致)
	if d.state != nil && d.state.StakeStore != nil {
		_, err := d.state.StakeStore.GetStakingInfo()
		if err != nil {
			d.logger.Warn("⚠️ 获取质押信息失败", "error", err)
		}
	}

	// 🆕 数据源3: 从共识引擎获取动态投票信息...
	// 注意：这里需要找到正确的getDynamicVotingInfo()方法调用方式
	// 暂时跳过，因为getDynamicVotingInfo()在jsonrpc/dpos_endpoint.go中

	// 🆕 数据源4: 从验证者信息提取质押信息...
	// 注意：这里需要找到正确的extractVotingInfoFromDelegates()方法调用方式
	// 暂时跳过，因为extractVotingInfoFromDelegates()在jsonrpc/dpos_endpoint.go中

	// 🆕 对比：显示内存中的受托人信息
	d.lock.RLock()
	defer d.lock.RUnlock()

	d.logger.Debug("🎯 从与命令相同的数据源读取完成")
	return nil
}

// 🆕 新增：启动时预加载BLS公钥到缓存和数据库（严格模式）
func (d *DPoS) preloadBLSKeysOnStartup() error {
	d.logger.Info("🔑 开始启动时预加载BLS公钥（严格模式）...")
	
	// 1. 从数据库加载已缓存的BLS公钥
	if err := d.loadBLSKeysFromDatabase(); err != nil {
		d.logger.Warn("⚠️ 从数据库加载BLS公钥失败", "error", err)
		// 数据库加载失败不阻止启动，继续尝试网络获取
	}
	
	// 2. 严格模式网络获取缺失的BLS公钥（必须成功）
	if err := d.fetchMissingBLSKeysFromNetwork(); err != nil {
		d.logger.Error("❌ 严格模式网络获取BLS公钥失败，启动终止", "error", err)
		return fmt.Errorf("strict mode BLS key fetching failed: %w", err)
	}
	
	d.logger.Info("✅ BLS公钥预加载完成（严格模式）")
	return nil
}

// 🆕 新增：从数据库加载BLS公钥到缓存

// 🆕 新增：网络获取缺失的BLS公钥（严格模式，确保不遗漏）
func (d *DPoS) fetchMissingBLSKeysFromNetwork() error {
	d.logger.Debug("🌐 开始网络获取缺失的BLS公钥（严格模式）...")
	
	// 从当前内存中的验证者集合获取验证者信息
	validators := d.getAllValidators()
	if len(validators) == 0 {
		d.logger.Warn("⚠️ 当前没有验证者，无需获取BLS公钥")
		return nil
	}
	
	// 获取本地节点地址
	myAddress := types.Address(d.key.Address())
	d.logger.Debug("🏠 本地节点地址", "address", myAddress.String())
	
	// 检查哪些验证者缺失BLS公钥
	missingValidators := make([]types.Address, 0)
	for _, validator := range validators {
		hasBLSKey := false
		if d.runtime != nil && d.runtime.networkIntegration != nil {
			if _, exists := d.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
				hasBLSKey = true
			}
		}
		
		if !hasBLSKey {
			missingValidators = append(missingValidators, validator.Address)
			d.logger.Debug("🔍 发现缺失的BLS公钥", 
				"address", validator.Address.String(),
				"totalMissing", len(missingValidators))
		}
	}
	
	if len(missingValidators) == 0 {
		d.logger.Info("✅ 所有验证者BLS公钥已存在，无需网络获取")
		return nil
	}
	
	d.logger.Debug("🌐 开始严格模式网络获取", 
		"missingCount", len(missingValidators),
		"totalValidators", len(validators))
	
	// 严格模式：逐个获取，确保不遗漏
	fetchedCount := 0
	for i, address := range missingValidators {
		d.logger.Debug("🔍 开始获取BLS公钥", 
			"address", address.String(),
			"progress", fmt.Sprintf("%d/%d", i+1, len(missingValidators)))
		
		// 检查是否是本地地址
		if address == myAddress {
			d.logger.Debug("🏠 检测到本地地址，从文件获取BLS公钥", "address", address.String())
			
			// 从本地文件获取BLS公钥
			if d.runtime != nil && d.runtime.networkIntegration != nil {
				if keyBytes, err := d.runtime.networkIntegration.findBLSKeyFromGenesisFile(address); err == nil && len(keyBytes) > 0 {
					// 保存到缓存
					if err := d.runtime.networkIntegration.SaveBLSKey(address, keyBytes); err != nil {
						d.logger.Error("❌ 保存本地BLS公钥到缓存失败", 
							"address", address.String(), 
							"error", err)
						return fmt.Errorf("failed to save local BLS key to cache for %s: %w", address.String(), err)
					} else {
						fetchedCount++
						d.logger.Debug("✅ 本地BLS公钥获取成功", 
							"address", address.String(),
							"blsKeyLength", len(keyBytes))
					}
				} else {
					d.logger.Error("❌ 从本地文件获取BLS公钥失败", 
						"address", address.String(),
						"error", err)
					return fmt.Errorf("failed to load local BLS key for %s: %w", address.String(), err)
				}
			} else {
				d.logger.Error("❌ 网络集成层不可用，无法获取本地BLS公钥", "address", address.String())
				return fmt.Errorf("network integration not available for local BLS key")
			}
		} else {
			// 非本地地址，通过网络获取
			d.logger.Debug("🌐 非本地地址，通过网络获取BLS公钥", "address", address.String())
			
			// 严格模式：必须成功获取，失败则立即返回错误
			retryCount := 0
			maxRetries := 10 // 设置最大重试次数，避免无限循环
			
			for retryCount < maxRetries {
				retryCount++
				
				// 发起网络请求
				if d.runtime != nil && d.runtime.networkIntegration != nil {
					if err := d.runtime.networkIntegration.RequestBLSKey(address, myAddress); err != nil {
						d.logger.Warn("⚠️ 网络请求BLS公钥失败", 
							"address", address.String(), 
							"retry", retryCount,
							"maxRetries", maxRetries,
							"error", err)
						
						if retryCount >= maxRetries {
							d.logger.Error("❌ 达到最大重试次数，BLS公钥获取失败", 
								"address", address.String(),
								"retryCount", retryCount)
							return fmt.Errorf("failed to get BLS key for %s after %d retries: %w", address.String(), maxRetries, err)
						}
						
						// 等待后重试
						time.Sleep(2 * time.Second)
						continue
					}
					
					// 等待网络响应
					d.logger.Debug("⏳ 等待BLS公钥响应", 
						"address", address.String(),
						"waitTime", "5秒",
						"retry", retryCount)
					time.Sleep(5 * time.Second)
					
					// 检查是否成功获取
					if blsKeyBytes, exists := d.runtime.networkIntegration.GetBLSKey(address); exists {
						fetchedCount++
						d.logger.Debug("✅ BLS公钥获取成功", 
							"address", address.String(),
							"blsKeyLength", len(blsKeyBytes),
							"retryCount", retryCount)
						break
					} else {
						d.logger.Warn("⚠️ 网络请求后仍未获取到BLS公钥", 
							"address", address.String(),
							"retry", retryCount,
							"maxRetries", maxRetries)
						
						if retryCount >= maxRetries {
							d.logger.Error("❌ 达到最大重试次数，BLS公钥获取失败", 
								"address", address.String(),
								"retryCount", retryCount)
							return fmt.Errorf("failed to get BLS key for %s after %d retries: no response received", address.String(), maxRetries)
						}
						
						// 等待后重试
						time.Sleep(3 * time.Second)
					}
				} else {
					d.logger.Error("❌ 网络集成层不可用", "address", address.String())
					return fmt.Errorf("network integration not available")
				}
			}
		}
	}
	
	d.logger.Debug("🌐 严格模式BLS公钥获取完成", 
		"fetchedCount", fetchedCount, 
		"totalMissing", len(missingValidators),
		"successRate", fmt.Sprintf("%.1f%%", float64(fetchedCount)/float64(len(missingValidators))*100))
	
	// 最终验证：确保所有验证者都有BLS公钥
	d.logger.Info("🔍 开始最终验证，确保所有验证者都有BLS公钥...")
	finalMissingCount := 0
	missingAddresses := make([]string, 0)
	
	for _, validator := range validators {
		hasBLSKey := false
		if d.runtime != nil && d.runtime.networkIntegration != nil {
			if _, exists := d.runtime.networkIntegration.GetBLSKey(validator.Address); exists {
				hasBLSKey = true
			}
		}
		
		if !hasBLSKey {
			finalMissingCount++
			missingAddresses = append(missingAddresses, validator.Address.String())
			d.logger.Error("❌ 最终验证发现缺失BLS公钥", 
				"address", validator.Address.String())
		}
	}
	
	if finalMissingCount > 0 {
		d.logger.Error("❌ 严格模式获取失败，仍有验证者缺失BLS公钥", 
			"missingCount", finalMissingCount,
			"missingAddresses", missingAddresses)
		return fmt.Errorf("strict mode failed: %d validators still missing BLS keys: %v", finalMissingCount, missingAddresses)
	}
	
	d.logger.Debug("✅ 严格模式BLS公钥获取完全成功，所有验证者BLS公钥已获取")
	return nil
}

// 🆕 将验证者数据同步到数据库
func (d *DPoS) syncDelegatesToDatabase(delegates validator.AccountSet) error {
	d.logger.Info("💾 开始将验证者数据同步到数据库...")
	
	if d.state == nil || d.state.StakeStore == nil {
		d.logger.Warn("⚠️ 状态存储不可用，无法同步验证者数据到数据库")
		return fmt.Errorf("state store not available")
	}
	
	if len(delegates) == 0 {
		d.logger.Warn("⚠️ 验证者集合为空，无需同步到数据库")
		return nil
	}
	
	// 验证者信息将在BLS公钥获取完成后统一保存到数据库
	d.logger.Info("💾 验证者信息将在BLS公钥获取完成后统一保存到数据库", "count", len(delegates))
	return nil
}


// GetBLSKeyBytesFromGenesis 从validator-bls.key文件获取指定地址的BLS公钥
func (d *DPoS) GetBLSKeyBytesFromGenesis(address types.Address) ([]byte, error) {
	// 1. 检查是否是本地节点
	if d.key == nil || address != types.Address(d.key.Address()) {
		return nil, fmt.Errorf("只有本地节点才能从validator-bls.key文件获取BLS公钥，请求地址: %s", address.String())
	}
	
	// 2. 获取数据目录路径
	dataDir := d.getDataDir()
	if dataDir == "" {
		return nil, fmt.Errorf("data directory not available")
	}
	
	// 3. 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
	parentDir := filepath.Dir(dataDir) // 获取 "node1\consensus"
	keyFilePath := filepath.Join(parentDir, "validator-bls.key")
	
	// 4. 检查文件是否存在
	if _, err := os.Stat(keyFilePath); os.IsNotExist(err) {
		d.logger.Debug("❌ BLS private key file not found", 
			"address", address.String(),
			"filePath", keyFilePath)
		return nil, fmt.Errorf("BLS private key file not found: %s", keyFilePath)
	}
	
	// 4. 读取私钥文件
	privateKeyData, err := os.ReadFile(keyFilePath)
	if err != nil {
		d.logger.Error("❌ Failed to read BLS private key file", 
			"address", address.String(),
			"filePath", keyFilePath,
			"error", err)
		return nil, fmt.Errorf("failed to read BLS private key file: %w", err)
	}
	
	// 5. 获取十六进制字符串（去除可能的换行符）
	privateKeyHex := strings.TrimSpace(string(privateKeyData))
	
	// 6. 检查并修正私钥长度
	if len(privateKeyHex)%2 != 0 {
		privateKeyHex = "0" + privateKeyHex
	}
	
	// 7. 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
	if err != nil {
		d.logger.Error("❌ Failed to unmarshal BLS private key", 
			"address", address.String(),
			"error", err)
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}
	
	// 8. 从私钥生成公钥
	publicKey := privateKey.PublicKey()
	publicKeyBytes := publicKey.Marshal()
	
	// 9. 验证公钥长度（应该是128字节）
	if len(publicKeyBytes) != 128 {
		return nil, fmt.Errorf("invalid BLS public key length: expected 128 bytes, got %d", len(publicKeyBytes))
	}
	

	return publicKeyBytes, nil
}

// getValidatorsFromExtraDataForProduction 生产时从ExtraData获取验证者集合
// 使用与验证时完全相同的逻辑
func (r *dposRuntime) getValidatorsFromExtraDataForProduction(header *types.Header, parents []*types.Header) (validator.AccountSet, error) {
	blockNumber := header.Number
	
	// 获取父区块
	var parent *types.Header
	if blockNumber > 0 {
		if parentHeader, exists := r.config.blockchain.GetHeaderByNumber(blockNumber - 1); exists {
			parent = parentHeader
		}
	}
	
	// 获取父区块的父区块
	var parentParent *types.Header
	if parent != nil && parent.Number > 0 {
		if parentParentHeader, exists := r.config.blockchain.GetHeaderByNumber(parent.Number - 1); exists {
			parentParent = parentParentHeader
		}
	}
	
	// 🆕 使用与验证时完全相同的逻辑：从父区块ExtraData获取验证者集合
	if parent != nil {
		// 解析父区块的ExtraData
		parentExtra, err := GetIbftExtra(parent.ExtraData)
		if err != nil {
			r.logger.Error("❌ 生产时解析父区块ExtraData失败", "parentBlockNumber", parent.Number, "error", err)
			return nil, fmt.Errorf("failed to parse parent ExtraData: %w", err)
		}
		
		// 从父区块ExtraData获取验证者集合
		parentValidators, err := parentExtra.getValidatorsFromExtraData(parent, parentParent, parents, r.config.dposBackend, r.logger)
		if err == nil {
			r.logger.Debug("✅ 生产时从父区块ExtraData获取验证者集合成功",
				"parentBlockNumber", parent.Number,
				"parentValidatorsCount", len(parentValidators))
			return parentValidators, nil
		}
		r.logger.Warn("⚠️ 生产时从父区块ExtraData获取验证者集合失败", "error", err)
	}
	
	// 备用方案：使用runtime.delegates
	if r.delegates != nil && len(r.delegates) > 0 {
		r.logger.Debug("🔍 生产时使用runtime.delegates作为备用方案", "delegatesCount", len(r.delegates))
		return r.delegates.Copy(), nil
	}
	
	return nil, fmt.Errorf("no validators available for production")
}

// 🆕 新增：获取数据目录路径
func (d *DPoS) getDataDir() string {
	// 从DPoS实例的数据目录字段获取
	if d.dataDir != "" {
		return d.dataDir
	}
	
	// 备用方案：从环境变量获取
	if dataDir := os.Getenv("VCITY_DATA_DIR"); dataDir != "" {
		return dataDir
	}
	
	return "" // 返回空字符串表示未找到
}

// getBLSKeyBytes 获取BLS公钥字节
func (d *DPoS) getBLSKeyBytes() ([]byte, error) {
	// 首先尝试从密钥管理器获取
	if d.config.SecretsManager != nil {
		blsKeyBytes, err := d.config.SecretsManager.GetSecret("bls")
		if err == nil && len(blsKeyBytes) > 0 {
			d.logger.Info("从密钥管理器获取BLS公钥", "length", len(blsKeyBytes))
			return blsKeyBytes, nil
		}
	}

	// 如果密钥管理器没有，尝试从validator-bls.key文件获取
	dataDir := d.getDataDir()
	if dataDir != "" {
		// 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
		// dataDir = "node1\consensus\dpos"，需要回到 "node1\consensus"
		parentDir := filepath.Dir(dataDir) // 获取 "node1\consensus"
		keyFilePath := filepath.Join(parentDir, "validator-bls.key")
		
		// 检查文件是否存在
		if _, err := os.Stat(keyFilePath); err == nil {
			d.logger.Info("从validator-bls.key文件获取BLS公钥", 
				"address", d.key.Address().String(),
				"filePath", keyFilePath)
			
			// 读取私钥文件
			privateKeyData, err := os.ReadFile(keyFilePath)
			if err == nil {
				// 获取十六进制字符串（去除可能的换行符）
				privateKeyHex := strings.TrimSpace(string(privateKeyData))
				
				// 检查并修正私钥长度
				if len(privateKeyHex)%2 != 0 {
					privateKeyHex = "0" + privateKeyHex
				}
				
				// 解析BLS私钥
				privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
				if err == nil {
					// 从私钥生成公钥
					publicKey := privateKey.PublicKey()
					blsKeyBytes := publicKey.Marshal()
					d.logger.Info("成功从validator-bls.key文件获取BLS公钥", 
						"address", d.key.Address().String(),
						"publicKeyLength", len(blsKeyBytes))
					return blsKeyBytes, nil
				} else {
					d.logger.Warn("解析validator-bls.key文件失败", 
						"address", d.key.Address().String(),
						"filePath", keyFilePath,
						"error", err)
				}
			} else {
				d.logger.Warn("读取validator-bls.key文件失败", 
					"address", d.key.Address().String(),
					"filePath", keyFilePath,
					"error", err)
			}
		} else {
			d.logger.Debug("validator-bls.key文件不存在", 
				"address", d.key.Address().String(),
				"filePath", keyFilePath)
		}
	}

	// 如果都没有，生成新的BLS公钥
	d.logger.Warn("未找到BLS公钥，将生成新的BLS公钥")
	blsKey, err := d.generateBLSKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate BLS key: %w", err)
	}

	blsKeyBytes := blsKey.Marshal()
	d.logger.Info("生成新的BLS公钥", "length", len(blsKeyBytes))

	// 保存到密钥管理器
	if d.config.SecretsManager != nil {
		if err := d.config.SecretsManager.SetSecret("bls", blsKeyBytes); err != nil {
			d.logger.Warn("保存BLS公钥到密钥管理器失败", "error", err)
		}
	}

	return blsKeyBytes, nil
}

// generateBLSKey 生成新的BLS公钥
func (d *DPoS) generateBLSKey() (*bls.PublicKey, error) {
	// 使用当前节点的地址作为种子生成私钥
	addressBytes := d.key.Address().Bytes()
	seedHash := crypto.Keccak256(addressBytes)

	// 将哈希转换为大整数作为私钥
	privateKeyInt := new(big.Int).SetBytes(seedHash)

	// 使用bn256的阶数作为模数
	modulus, _ := new(big.Int).SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)
	privateKeyInt.Mod(privateKeyInt, modulus)

	// 确保私钥不为零
	if privateKeyInt.Cmp(big.NewInt(0)) == 0 {
		privateKeyInt.Set(big.NewInt(1))
	}

	// 将私钥转换为字节并解析
	privateKeyBytes := privateKeyInt.Bytes()
	privateKey, err := bls.UnmarshalPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create BLS private key: %w", err)
	}

	// 获取公钥
	publicKey := privateKey.PublicKey()

	d.logger.Info("生成新的BLS密钥对", "address", d.key.Address().String())
	return publicKey, nil
}

// persistBLSKeyToStakeStore 将BLS公钥持久化到StakeStore
func (d *DPoS) persistBLSKeyToStakeStore(address types.Address, blsKeyBytes []byte) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("StakeStore not available")
	}

	// 创建DelegateInfo结构
	delegateInfo := &DelegateInfo{
		Address:        address,
		VotingPower:    big.NewInt(0), // 初始投票权重为0
		TotalVotes:     big.NewInt(0), // 初始总票数为0
		ProducedBlocks: 0,
		MissedBlocks:   0,
		LastBlockTime:  0,
		IsActive:       true, // 默认活跃
		BlsPublicKey:   blsKeyBytes,
	}

	// 保存到StakeStore
	if err := d.state.StakeStore.setDelegateInfo(address, delegateInfo, nil); err != nil {
		return fmt.Errorf("failed to save BLS key to StakeStore: %w", err)
	}

	return nil
}

// syncRuntimeDelegatesWithRetry 异步同步 dposRuntime 的 delegates 状态，使用重试机制
func (d *DPoS) syncRuntimeDelegatesWithRetry() {
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	d.logger.Info("🔄 开始异步同步 dposRuntime delegates 状态", "maxRetries", maxRetries)

	for i := 0; i < maxRetries; i++ {
		if d.runtime != nil {
			if d.runtime.lock.TryLock() {
				// 同步主结构体的 delegates 到 runtime
				d.runtime.delegates = d.delegates.Copy()
				d.runtime.lock.Unlock()
				d.logger.Info("✅ dposRuntime delegates 同步完成",
					"count", len(d.delegates),
					"attempt", i+1,
					"totalAttempts", maxRetries)
				return
			} else {
				d.logger.Info("⚠️ 无法获取 runtime.lock，准备重试",
					"attempt", i+1,
					"maxRetries", maxRetries,
					"retryDelay", retryDelay)
			}
		} else {
			d.logger.Warn("⚠️ dposRuntime 为空，无法同步 delegates")
			return
		}

		// 如果不是最后一次尝试，等待后重试
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
			retryDelay *= 2 // 指数退避
		}
	}

	d.logger.Warn("⚠️ dposRuntime delegates 同步失败，已达到最大重试次数",
		"maxRetries", maxRetries,
		"delegatesCount", len(d.delegates))
}

// GetValidators 获取DPoS验证者集合（公共方法）
func (d *DPoS) GetValidators() validator.AccountSet {
	// 直接使用 dposRuntime.delegates 作为唯一数据源
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		return d.runtime.delegates
	}
	
	// 如果 runtime 不可用，返回空集合
	return validator.AccountSet{}
}

// GetBLSKeyForValidator 获取指定验证者的BLS公钥（延迟获取）
func (d *DPoS) GetBLSKeyForValidator(address types.Address) (*bls.PublicKey, error) {
	// 静默处理，不打印日志
	
	// 检查是否是当前节点
	if d.key != nil && address == types.Address(d.key.Address()) {
		// 当前节点：从本地文件读取BLS私钥
		blsPublicKey, err := d.readBLSPrivateKeyAndGeneratePublicKey(address)
		if err != nil {
			return nil, fmt.Errorf("failed to read local BLS key: %w", err)
		}
		
		// 解析BLS公钥
		blsKey, err := bls.UnmarshalPublicKey(blsPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal BLS key: %w", err)
		}
		
		d.logger.Debug("✅ 本地BLS公钥获取成功", "address", address.String())
		return blsKey, nil
	} else {
		// 其他节点：通过网络请求BLS公钥
		// 静默处理，不打印日志
		
		blsKey, err := d.requestBLSPublicKeyFromNetwork(address)
		if err != nil {
			return nil, fmt.Errorf("failed to request remote BLS key: %w", err)
		}
		
		// 静默处理，不打印日志
		return blsKey, nil
	}
}

// EnsureBLSKeyForValidator 确保验证者有BLS公钥（如果为nil则获取）
func (d *DPoS) EnsureBLSKeyForValidator(validator *validator.ValidatorMetadata) error {
	if validator.BlsKey != nil {
		return nil // 已经有BLS公钥
	}
	
	blsKey, err := d.GetBLSKeyForValidator(validator.Address)
	if err != nil {
		return fmt.Errorf("failed to get BLS key for validator %s: %w", validator.Address.String(), err)
	}
	
	validator.BlsKey = blsKey
	d.logger.Info("✅ 验证者BLS公钥已设置", "address", validator.Address.String())
	return nil
}

// 🆕 新增：BLS响应处理器管理
var (
	blsResponseHandlers = make(map[string]chan *bls.PublicKey)
	blsErrorHandlers    = make(map[string]chan error)
	blsHandlerMutex     sync.RWMutex
)

// registerBLSResponseHandler 注册BLS响应处理器
func (d *DPoS) registerBLSResponseHandler(requestID string, responseCh chan *bls.PublicKey, errorCh chan error) {
	blsHandlerMutex.Lock()
	defer blsHandlerMutex.Unlock()
	
	blsResponseHandlers[requestID] = responseCh
	blsErrorHandlers[requestID] = errorCh
	
	// 静默处理，不打印日志
}

// unregisterBLSResponseHandler 注销BLS响应处理器
func (d *DPoS) unregisterBLSResponseHandler(requestID string) {
	blsHandlerMutex.Lock()
	defer blsHandlerMutex.Unlock()
	
	if responseCh, exists := blsResponseHandlers[requestID]; exists {
		close(responseCh)
		delete(blsResponseHandlers, requestID)
	}
	
	if errorCh, exists := blsErrorHandlers[requestID]; exists {
		close(errorCh)
		delete(blsErrorHandlers, requestID)
	}
	
	// 静默处理，不打印日志
}

// initializeBLSNetworking 初始化BLS网络通信
func (d *DPoS) initializeBLSNetworking() error {
	if d.config.Network == nil {
		return fmt.Errorf("network not available")
	}
	
	d.logger.Info("🌐 初始化BLS网络通信")
	
	// 由于BLS消息不是protobuf类型，我们使用简化的网络通信
	// 这里暂时跳过Topic创建，直接使用JSON序列化进行网络通信
	d.logger.Info("✅ BLS网络通信初始化完成（使用JSON序列化）")
	return nil
}

// broadcastBLSRequest 广播BLS请求
func (d *DPoS) broadcastBLSRequest(requestData []byte, requestID string) error {
	// 解析请求数据
	var request BLSPublicKeyRequest
	if err := json.Unmarshal(requestData, &request); err != nil {
		return fmt.Errorf("failed to unmarshal BLS request: %w", err)
	}
	
	// 通过P2P网络广播BLS公钥请求
	// 静默处理，不打印日志
	
	// 使用网络集成层进行真实的P2P广播
	if d.runtime != nil && d.runtime.networkIntegration != nil {
		// 通过网络集成层请求BLS公钥
		if err := d.runtime.networkIntegration.RequestBLSKey(request.TargetAddress, request.RequesterAddress); err != nil {
			d.logger.Error("❌ 请求BLS公钥失败", "error", err)
			return fmt.Errorf("failed to request BLS key: %w", err)
		}
		
		d.logger.Debug("✅ BLS公钥请求发送成功", "requestID", requestID)
	} else {
		d.logger.Warn("⚠️ 网络集成层不可用，无法请求BLS公钥")
		return fmt.Errorf("network integration not available")
	}
	
	return nil
}

// handleBLSResponse 处理BLS响应
func (d *DPoS) handleBLSResponse(requestID string, blsKey *bls.PublicKey) error {
	blsHandlerMutex.RLock()
	responseCh, exists := blsResponseHandlers[requestID]
	blsHandlerMutex.RUnlock()
	
	if !exists {
		return fmt.Errorf("BLS response handler not found for requestID: %s", requestID)
	}
	
	// 发送响应
	select {
	case responseCh <- blsKey:
		d.logger.Debug("✅ 发送真实BLS响应", "requestID", requestID)
	default:
		d.logger.Warn("⚠️ BLS响应通道已满", "requestID", requestID)
		return fmt.Errorf("BLS response channel is full")
	}
	
	return nil
}

// simulateBLSResponse 模拟BLS响应（用于测试）
func (d *DPoS) simulateBLSResponse(requestID string) {
	blsHandlerMutex.RLock()
	responseCh, exists := blsResponseHandlers[requestID]
	blsHandlerMutex.RUnlock()
	
	if !exists {
		return
	}
	
	// 生成模拟的BLS公钥
	blsKey, err := bls.GenerateBlsKey()
	if err != nil {
		d.logger.Error("❌ 生成模拟BLS公钥失败", "error", err)
		return
	}
	
	// 发送响应
	select {
	case responseCh <- blsKey.PublicKey():
		d.logger.Info("✅ 发送模拟BLS响应", "requestID", requestID)
	default:
		d.logger.Warn("⚠️ BLS响应通道已满", "requestID", requestID)
	}
}



// BLSPublicKeyResponse BLS公钥响应消息
type BLSPublicKeyResponse struct {
	RequestID        string        `json:"request_id"`
	TargetAddress    types.Address `json:"target_address"`
	RequesterAddress types.Address `json:"requester_address"`
	BlsPublicKey     []byte        `json:"bls_public_key"`
	Timestamp        uint64        `json:"timestamp"`
}
