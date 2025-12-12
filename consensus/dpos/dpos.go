package dpos

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/umbracle/fastrlp"
	bolt "go.etcd.io/bbolt"
)

// 全局DPoS实例注册表，用于BLS公钥持久化
var (
	dposInstances      = make(map[string]*DPoS)
	dposMutex          sync.RWMutex
	ErrBusinessInvalid = errors.New("business invalid")
)

func parseDurationAllowDays(input string) (time.Duration, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	if duration, err := time.ParseDuration(input); err == nil {
		return duration, nil
	}

	// 支持形如 "21d" 的天单位（可带小数）
	if strings.ContainsAny(input, "dD") {
		lower := strings.ToLower(input)
		var value float64
		var suffix string
		if _, err := fmt.Sscanf(lower, "%f%s", &value, &suffix); err == nil && strings.HasPrefix(suffix, "d") {
			remaining := strings.TrimPrefix(suffix, "d")
			hours := value * 24
			normalized := fmt.Sprintf("%.0fh%s", hours, remaining)
			return time.ParseDuration(normalized)
		}
		// 简单处理：去掉最后的 d
		if strings.HasSuffix(lower, "d") {
			numberPart := strings.TrimSuffix(lower, "d")
			if numberPart == "" {
				return 0, fmt.Errorf("invalid duration: %s", input)
			}
			if v, err := strconv.ParseFloat(numberPart, 64); err == nil {
				hours := v * 24
				return time.ParseDuration(fmt.Sprintf("%.0fh", hours))
			}
			return 0, fmt.Errorf("invalid duration: %s", input)
		}
	}

	return 0, fmt.Errorf("unsupported duration format: %s", input)
}

func toUint64(value interface{}) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (d *DPoS) applyCommissionDefaults(info *DelegateInfo) {
	if info == nil || d == nil || d.config == nil {
		return
	}
	if info.CommissionRate == 0 {
		info.CommissionRate = d.config.CommissionRateDefault
	}
}

func (d *DPoS) populateCommissionFields(delegate types.Address, info *DelegateInfo) {
	if info == nil || d == nil || d.state == nil || d.state.StakeStore == nil {
		return
	}

	existing, err := d.state.StakeStore.GetDelegateInfo(delegate)
	if err != nil {
		d.logger.Debug("⚠️ 获取受托人佣金信息失败，使用默认值",
			"delegate", delegate.String(),
			"error", err)
		d.applyCommissionDefaults(info)
		return
	}

	if existing != nil {
		if existing.CommissionRate != 0 {
			info.CommissionRate = existing.CommissionRate
		} else {
			d.applyCommissionDefaults(info)
		}
		info.PendingCommissionRate = existing.PendingCommissionRate
		info.CommissionUpdateTime = existing.CommissionUpdateTime
	} else {
		d.applyCommissionDefaults(info)
	}
}

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

// dposBackend 接口定义了DPoS需要的方法
type dposBackend interface {
	// GetDelegates 获取指定区块的受托人集合--实际参与共识的节点
	GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error)

	// GetDelegatesWithTx 在数据库事务中获取受托人集合
	GetDelegatesWithTx(blockNumber uint64, parents []*types.Header, dbTx *bolt.Tx) (validator.AccountSet, error)

	// GetCurrentDelegates 获取当前内存中的受托人集合
	GetCurrentDelegates() validator.AccountSet

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

	// DPoS验证者数量配置
	DPoSValidatorsCount uint64 `json:"dpos_validators_count"`

	Blockchain *blockchain.Blockchain
	Logger     hclog.Logger
	Network    *network.Server

	SecretsManager secrets.SecretsManager

	Executor *state.Executor

	// 初始受托人集合
	InitialDelegates []*validator.GenesisValidator `json:"initialDelegates"`

	// 投票锁定时间
	VoteLockTime uint64 `json:"voteLockTime"`

	// 委托奖励比例
	RewardRatio uint64 `json:"rewardRatio"`

	ConsensusSwitchHeight uint64 `json:"consensusSwitchHeight"`

	ValidatorsCount uint64 `json:"validatorsCount" yaml:"validatorsCount"`

	// DPoS委托最小质押门槛
	DPoSDelegateThreshold *big.Int `json:"dpos_delegate_threshold" yaml:"dpos_delegate_threshold"`

	EpochDuration       time.Duration `json:"epochDuration" yaml:"epochDuration"`
	RewardAccount       types.Address `json:"rewardAccount" yaml:"rewardAccount"`
	RewardAmount        *big.Int      `json:"rewardAmount" yaml:"rewardAmount"`
	ProposalVotePeriod  time.Duration `json:"proposalVotePeriod" yaml:"dpos_proposal_vote_period"`   // 提案表决周期
	ProposalValidPeriod time.Duration `json:"proposalValidPeriod" yaml:"dpos_proposal_valid_period"` // 提案有效期

	// 冻结相关配置
	MinFreezePeriod    uint64 `json:"min_freeze_period" yaml:"dpos_min_freeze_period"`       // 最小冻结期（秒）
	UnfreezeLockPeriod uint64 `json:"unfreeze_lock_period" yaml:"dpos_unfreeze_lock_period"` // 解冻锁定期（秒）

	// 佣金默认配置
	CommissionRateDefault     uint64        // 默认佣金率（基点）
	CommissionEffectivePeriod time.Duration // 佣金率修改的延迟生效周期
}

// GenerateExitProof 生成退出证明（占位符实现，满足 BridgeDataProvider 接口要求）
func (r *dposRuntime) GenerateExitProof(exitID uint64) (types.Proof, error) {
	// TODO: 实现退出证明生成逻辑（如果需要）
	return types.Proof{}, nil
}

// GetStateSyncProof 获取状态同步证明（占位符实现，满足 BridgeDataProvider 接口要求）
func (r *dposRuntime) GetStateSyncProof(stateSyncID uint64) (types.Proof, error) {
	// TODO: 实现状态同步证明获取逻辑（如果需要）
	return types.Proof{}, nil
}

type DPoS struct {
	// 复用基础设施
	state  *State
	key    *wallet.Key
	logger hclog.Logger

	// DPoS特有组件
	config    *DPoSConfig
	runtime   *dposRuntime
	rawConfig map[string]interface{} // 存储原始配置，用于读取削减相关参数

	// 组合模式，支持依赖注入
	consensus      core.ConsensusManager      // 共识管理器
	validator      core.ValidatorManager      // 验证者管理器
	epoch          core.EpochManager          // Epoch管理器
	epochLifecycle core.EpochLifecycleManager // Epoch生命周期管理器
	reward         core.RewardManager         // 奖励管理器
	fault          core.FaultManager          // 故障管理器
	query          core.QueryManager          // 查询管理器
	governance     core.GovernanceManager     // 治理模块
	network        core.NetworkManager        // 网络管理器
	bls            core.BLSManager            // BLS管理器
	stateMgr       core.StateManager          // 状态管理器

	syncer syncer.Syncer

	// 网络组件
	consensusTopic *network.Topic

	// 状态管理
	delegates    validator.AccountSet
	voters       map[types.Address]*VoterInfo
	currentRound uint64

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

	// 添加性能优化相关结构
	cache          *DPoSCache
	batchProcessor *BatchProcessor
	metrics        *DPoSMetrics

	// 延迟验证者集合更新标志
	pendingValidatorUpdate bool

	// 故障检测相关字段
	currentEpoch      uint64
	faultyValidators  map[types.Address]bool
	missedBlocksCount map[types.Address]uint64

	// 最后投票的验证者地址集合（支持多个验证者）
	lastVotedDelegates map[types.Address]bool

	// 奖励分配信息
	pendingRewardDistribution *RewardDistributionInfo

	// 故障消减信息（只包含 missed blocks 的消减，不包含双重签名）
	pendingSlashingInfo *SlashingInfo

	// 故障检测信息
	pendingFaultFlags     []FaultFlagInfo
	pendingEpochEndHeader *types.Header

	// 经济系统组件
	epochManager      *TimeBasedEpochManager
	blockTracker      *BlockProductionTracker
	rewardDistributor *RewardDistributor

	// 固定时间窗口调度器
	blockScheduler *BlockScheduler

	// 双重签名检测器
	doubleSigningDetector *DoubleSigningDetector

	// 余额查询器
	balanceQuerier NativeTokenBalanceQuerier

	// DPoS验证者相关字段
	minStakeAmount *big.Int // 最小质押门槛

	// 日志频率限制
	lastLogTime       map[string]time.Time   // 最后日志时间
	logMutex          sync.RWMutex           // 日志锁
	genesisExtraData  []byte                 // 创世块extraData
	genesisValidators map[types.Address]bool // 创世验证者地址映射

	// BLS加载状态管理
	blsLoadingComplete bool
	blsLoadingMutex    sync.RWMutex
	blsLoadingWaitCh   chan struct{}

	// 参数表决机制相关字段
	parameterProposals map[string]*ParameterProposal // 提案存储
	parameterUpdates   []*ParameterUpdate            // 参数更新记录
	activeProposals    map[string]bool               // 活跃提案
	proposalCounter    uint64                        // 提案计数器
	votableParameters  map[string]*ParameterInfo     // 可表决参数配置

	// 参数值缓存
	parameterCurrentValues map[string]interface{} // 参数当前值缓存
	parameterValuesMutex   sync.RWMutex           // 参数值读写锁
}

// SetPendingEpochEndHeader 缓存当前正在构建的epoch结束区块头
func (d *DPoS) SetPendingEpochEndHeader(header *types.Header) {
	if header == nil {
		return
	}

	d.lock.Lock()
	defer d.lock.Unlock()

	headerCopy := *header
	d.pendingEpochEndHeader = &headerCopy

	if d.logger != nil {
		d.logger.Info("📝 缓存epoch结束区块头",
			"blockNumber", header.Number,
			"miner", types.BytesToAddress(header.Miner).String())
	}
}

// ClearPendingEpochEndHeader 清理缓存，避免跨epoch误用
func (d *DPoS) ClearPendingEpochEndHeader(blockNumber uint64) {
	d.lock.Lock()
	defer d.lock.Unlock()

	if d.pendingEpochEndHeader == nil {
		return
	}

	if blockNumber == 0 || d.pendingEpochEndHeader.Number == blockNumber {
		if d.logger != nil {
			d.logger.Info("🧹 清空epoch结束区块头缓存",
				"blockNumber", d.pendingEpochEndHeader.Number)
		}
		d.pendingEpochEndHeader = nil
	}
}

// getPendingEpochEndHeader 返回缓存的epoch结束区块头副本
func (d *DPoS) getPendingEpochEndHeader(blockNumber uint64) *types.Header {
	d.lock.RLock()
	defer d.lock.RUnlock()

	if d.pendingEpochEndHeader == nil {
		return nil
	}

	if blockNumber != 0 && d.pendingEpochEndHeader.Number != blockNumber {
		return nil
	}

	headerCopy := *d.pendingEpochEndHeader
	return &headerCopy
}

// getCurrentBlockNumber 获取当前区块号（内部方法）
func (d *DPoS) getCurrentBlockNumber() uint64 {
	if d.config == nil {
		d.logger.Debug("🔍 getCurrentBlockNumber: config is nil")
		return 0
	}
	if d.config.Blockchain == nil {
		d.logger.Debug("🔍 getCurrentBlockNumber: Blockchain is nil")
		return 0
	}

	currentHeader := d.config.Blockchain.Header()
	if currentHeader == nil {
		d.logger.Debug("🔍 getCurrentBlockNumber: Header() returned nil")
		return 0
	}

	d.logger.Debug("🔍 getCurrentBlockNumber: success", "blockNumber", currentHeader.Number)
	return currentHeader.Number
}

// GetCurrentBlockNumber 获取当前区块号（导出方法，供JSON-RPC使用）
func (d *DPoS) GetCurrentBlockNumber() uint64 {
	return d.getCurrentBlockNumber()
}

// GetConsensusSwitchHeight 获取共识切换高度（导出方法，供JSON-RPC使用）
func (d *DPoS) GetConsensusSwitchHeight() uint64 {
	if d.config == nil {
		return 0
	}
	return d.config.ConsensusSwitchHeight
}

// GetCommissionEffectivePeriod 获取佣金生效周期
func (d *DPoS) GetCommissionEffectivePeriod() time.Duration {
	if d.config == nil {
		return 21 * 24 * time.Hour // 默认值
	}
	if d.config.CommissionEffectivePeriod > 0 {
		return d.config.CommissionEffectivePeriod
	}
	return 21 * 24 * time.Hour // 默认值
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
	return GetDposExtraClean(extra)
}

func (d *DPoS) Start() error {
	d.logger.Info("starting dpos consensus", "signer", d.key.String())

	// 1. 初始化BLS加载状态
	d.initializeBLSLoadingState()

	// 2. 从数据库加载验证者信息（按voterpower排序并截取前N个）
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
			} else {
				d.txPool.SetSealing(false)
				d.logger.Info("transaction pool sealing state set to false (node is not delegate)")
			}
		} else {
			d.logger.Warn("key not available, cannot determine if node is delegate")
		}

		// 启用状态广播
		if d.syncer != nil {
			d.syncer.EnablePublishingPeerStatus()
			d.logger.Info("✅ 启用状态广播")
		} else {
			d.logger.Warn("⚠️ syncer为空，无法启用状态广播")
		}
	} else {
		d.logger.Warn("transaction pool not available, cannot set sealing state")
	}

	// 3. 先同步获取BLS公钥（确保网络集成层已就绪）
	d.logger.Info("🔑 开始同步获取BLS公钥...")
	if err := d.syncLoadBLSKeys(); err != nil {
		d.logger.Error("❌ BLS公钥同步获取失败", "error", err)
		return fmt.Errorf("failed to sync load BLS keys: %w", err)
	}
	d.logger.Info("✅ BLS公钥同步获取完成")

	// 4. 启动syncer（BLS公钥加载完成后）
	d.logger.Info("🌐 开始启动syncer...")
	if err := d.syncer.Start(); err != nil {
		// 检查是否是topic冲突错误，如果是则忽略
		if strings.Contains(err.Error(), "topic already exists") {
			d.logger.Warn("⚠️ syncer启动遇到topic冲突，但继续启动DPoS runtime", "error", err)
		} else {
			d.logger.Error("❌ syncer启动失败（非topic冲突）", "error", err)
			return fmt.Errorf("failed to start syncer. Error: %w", err)
		}
	} else {
		d.logger.Info("✅ syncer启动成功")
	}

	// sync concurrently, retrying indefinitely
	go common.RetryForever(context.Background(), time.Second, func(context.Context) error {
		// 在区块同步前检查BLS公钥是否加载完成
		if err := d.waitForBLSKeysLoaded(); err != nil {
			d.logger.Warn("⚠️ 等待BLS公钥加载完成失败，继续尝试同步", "error", err)
		}

		blockHandler := func(b *types.FullBlock) bool {
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

		// 详细检查runtime状态
		d.logger.Debug("🔍 DPoS runtime详细状态检查",
			"resourceMonitorIsNil", d.runtime.resourceMonitor == nil,
			"goroutineManagerIsNil", func() bool {
				if d.runtime.resourceMonitor != nil {
					return d.runtime.resourceMonitor.goroutineManager == nil
				}
				return true
			}(),
			"delegatesCount", len(d.runtime.delegates))

		// 启动DPoS运行时
		d.logger.Debug("🚀 调用d.runtime.start()...")
		if err := d.runtime.start(); err != nil {
			d.logger.Error("❌ 启动DPoS runtime失败", "error", err)
			return fmt.Errorf("failed to start DPoS runtime: %w", err)
		}
		d.logger.Info("✅ DPoS runtime启动成功")

		// 新节点启动后，主动查询其他节点的待处理签名请求
		go func() {
			// 等待一段时间让网络连接稳定
			time.Sleep(10 * time.Second)
			// 新节点启动，开始查询待处理签名请求（静默处理）
			d.runtime.queryPendingSignatureRequests()
		}()
	} else {
		d.logger.Error("❌ DPoS runtime为nil，无法启动出块循环")
	}

	// start state DB process if available
	if d.state != nil {
		go d.state.startStatsReleasing()
	}

	// 从数据库恢复投票数据
	if err := d.restoreVotingDataFromDatabase(); err != nil {
		d.logger.Error("Failed to restore voting data from database", "error", err)
	}

	// 启动时直接调用和命令一样的数据源方法
	if err := d.callCommandDataSourcesOnStartup(); err != nil {
		d.logger.Warn("Failed to call command data sources on startup", "error", err)
	}

	// 初始化性能优化组件
	d.initPerformanceOptimizations()

	// 初始化模块
	if d.consensus == nil {
		d.initConsensusModule()
	}
	if d.consensus == nil {
		return fmt.Errorf("consensus module not initialized")
	}
	if d.validator == nil {
		d.initValidatorModule()
	}
	if d.validator == nil {
		return fmt.Errorf("validator module not initialized")
	}
	if d.epoch == nil {
		d.initEpochModule()
	}
	if d.epoch == nil {
		return fmt.Errorf("epoch module not initialized")
	}
	if d.reward == nil {
		d.initRewardModule()
	}
	if d.fault == nil {
		d.initFaultModule()
	}
	if d.fault == nil {
		return fmt.Errorf("fault module not initialized")
	}
	if d.query == nil {
		d.initQueryModule()
	}
	if d.governance == nil {
		d.initGovernanceModule()
	}
	if d.epochLifecycle == nil {
		d.initEpochLifecycleModule()
	}
	if d.network == nil {
		d.initNetworkModule()
	}
	if d.network == nil {
		return fmt.Errorf("network module not initialized")
	}
	if d.bls == nil {
		d.initBLSModule()
	}
	if d.bls == nil {
		return fmt.Errorf("bls module not initialized")
	}
	if d.stateMgr == nil {
		d.initStateModule()
	}
	if d.stateMgr == nil {
		return fmt.Errorf("state module not initialized")
	}

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

	//  从全局注册表中注销DPoS实例
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
		closeCh:     make(chan struct{}),
		logger:      logger,
		txPool:      params.TxPool,
		config:      &DPoSConfig{},              // 初始化config结构体
		rawConfig:   params.Config.Config,       // 存储原始配置
		lastLogTime: make(map[string]time.Time), // 初始化日志频率限制
	}

	getConfigValue := func(keys ...string) (interface{}, bool) {
		for _, key := range keys {
			if val, ok := params.Config.Config[key]; ok {
				return val, true
			}
		}
		return nil, false
	}

	// 直接使用server层已解析的配置（避免重复解析）
	logger.Info("🔍 开始解析DPoS经济系统配置", "configKeys", len(params.Config.Config))

	// 调试：打印所有配置键
	// for key, value := range params.Config.Config {
	//	logger.Info("🔍 配置键值对", "key", key, "type", fmt.Sprintf("%T", value), "value", value)
	// }

	// 解析共识切换高度
	if consensusSwitchHeight, exists := params.Config.Config["consensusSwitchHeight"]; exists {
		if height, ok := consensusSwitchHeight.(float64); ok {
			vcity_dpos.config.ConsensusSwitchHeight = uint64(height)
			logger.Debug("🔄 设置共识切换高度", "height", vcity_dpos.config.ConsensusSwitchHeight)
		}
	}

	// 支持驼峰和下划线两种键名
	var validatorsCountValue interface{}
	if val, exists := params.Config.Config["dposValidatorsCount"]; exists {
		validatorsCountValue = val
	} else if val, exists := params.Config.Config["dpos_validators_count"]; exists {
		validatorsCountValue = val
		logger.Info("👥 使用下划线形式的 dpos_validators_count 配置")
	}

	if validatorsCountValue != nil {
		logger.Info("🔍 找到 dposValidatorsCount 配置", "type", fmt.Sprintf("%T", validatorsCountValue), "value", validatorsCountValue)
		switch countVal := validatorsCountValue.(type) {
		case float64:
			vcity_dpos.config.DelegateCount = uint64(countVal)
			vcity_dpos.config.DPoSValidatorsCount = uint64(countVal)
			logger.Info("👥 设置验证者数量", "count", vcity_dpos.config.DPoSValidatorsCount)
		case int:
			vcity_dpos.config.DelegateCount = uint64(countVal)
			vcity_dpos.config.DPoSValidatorsCount = uint64(countVal)
			logger.Info("👥 设置验证者数量 (int)", "count", vcity_dpos.config.DPoSValidatorsCount)
		case uint64:
			vcity_dpos.config.DelegateCount = countVal
			vcity_dpos.config.DPoSValidatorsCount = countVal
			logger.Info("👥 设置验证者数量 (uint64)", "count", vcity_dpos.config.DPoSValidatorsCount)
		case string:
			if parsed, err := strconv.ParseUint(countVal, 10, 64); err == nil {
				vcity_dpos.config.DelegateCount = parsed
				vcity_dpos.config.DPoSValidatorsCount = parsed
				logger.Info("👥 设置验证者数量 (string)", "count", vcity_dpos.config.DPoSValidatorsCount)
			} else {
				logger.Warn("👥 dposValidatorsCount 解析失败", "value", countVal, "error", err)
			}
		default:
			logger.Warn("👥 dposValidatorsCount 类型不支持", "type", fmt.Sprintf("%T", validatorsCountValue))
		}
	} else {
		logger.Warn("👥 未找到 dposValidatorsCount 配置")
	}

	// 解析默认佣金率（从配置文件读取）
	if commissionValue, exists := getConfigValue("dposCommissionRatio", "dpos_commission_ratio"); exists {
		if ratio, ok := toUint64(commissionValue); ok && ratio > 0 {
			vcity_dpos.config.CommissionRateDefault = ratio
			logger.Info("💼 设置默认佣金率", "ratio", ratio)
		} else {
			logger.Warn("💼 dposCommissionRatio 类型或数值无效", "value", commissionValue)
		}
	}

	// 解析佣金生效周期
	if effectiveValue, exists := getConfigValue("commissionEffectivePeriod", "dpos_commission_effective"); exists {
		switch val := effectiveValue.(type) {
		case time.Duration:
			if val > 0 {
				vcity_dpos.config.CommissionEffectivePeriod = val
				logger.Info("⏳ 设置佣金生效周期（Duration）", "duration", val.String())
			}
		case string:
			if duration, err := parseDurationAllowDays(val); err == nil {
				vcity_dpos.config.CommissionEffectivePeriod = duration
				logger.Info("⏳ 设置佣金生效周期（String）", "raw", val, "duration", duration.String())
			} else {
				logger.Warn("⏳ 佣金生效周期字符串解析失败", "value", val, "error", err)
			}
		case float64:
			if val > 0 {
				vcity_dpos.config.CommissionEffectivePeriod = time.Duration(val) * time.Second
				logger.Info("⏳ 设置佣金生效周期（float秒）", "seconds", val)
			}
		default:
			logger.Warn("⏳ 佣金生效周期类型不支持", "type", fmt.Sprintf("%T", effectiveValue))
		}
	}

	if missedBlocksPercentage, exists := getConfigValue("dpos_missed_blocks_percentage", "missed_blocks_percentage"); exists {
		if percentage, ok := toUint64(missedBlocksPercentage); ok {
			logger.Info("🔨 从配置文件读取漏块率阈值", "percentage", percentage, "基点")
		} else {
			logger.Warn("🔨 dpos_missed_blocks_percentage 类型或数值无效", "value", missedBlocksPercentage)
		}
	} else {
		logger.Warn("🔨 未找到dpos_missed_blocks_percentage配置，将使用默认值1000 (10%)")
	}

	if minorOffenseSlashRate, exists := getConfigValue("dpos_minor_offense_slash_rate", "minor_offense_slash_rate"); exists {
		if rate, ok := toUint64(minorOffenseSlashRate); ok {
			logger.Info("🔨 从配置文件读取轻度违规削减率", "rate", rate, "基点")
		} else {
			logger.Warn("🔨 dpos_minor_offense_slash_rate 类型或数值无效", "value", minorOffenseSlashRate)
		}
	} else {
		logger.Warn("🔨 未找到dpos_minor_offense_slash_rate配置，将使用默认值50 (0.5%)")
	}

	if severeOffenseSlashRate, exists := getConfigValue("dpos_severe_offense_slash_rate", "severe_offense_slash_rate"); exists {
		if rate, ok := toUint64(severeOffenseSlashRate); ok {
			logger.Info("🔨 从配置文件读取严重违规削减率", "rate", rate, "基点")
		} else {
			logger.Warn("🔨 dpos_severe_offense_slash_rate 类型或数值无效", "value", severeOffenseSlashRate)
		}
	} else {
		logger.Warn("🔨 未找到dpos_severe_offense_slash_rate配置，将使用默认值1000 (10%)")
	}

	if epochDuration, exists := params.Config.Config["epochDuration"]; exists {
		logger.Info("🔍 找到epochDuration配置", "type", fmt.Sprintf("%T", epochDuration), "value", epochDuration)
		if duration, ok := epochDuration.(time.Duration); ok {
			vcity_dpos.config.EpochDuration = duration
		} else {
			logger.Warn("⏰ epochDuration类型断言失败", "type", fmt.Sprintf("%T", epochDuration))
		}
	} else {
		logger.Warn("⏰ 未找到epochDuration配置")
	}

	if rewardAccount, exists := params.Config.Config["rewardAccount"]; exists {
		logger.Info("🔍 找到rewardAccount配置", "type", fmt.Sprintf("%T", rewardAccount), "value", rewardAccount)
		if account, ok := rewardAccount.(types.Address); ok {
			vcity_dpos.config.RewardAccount = account
		} else {
			logger.Warn("💰 rewardAccount类型断言失败", "type", fmt.Sprintf("%T", rewardAccount))
		}
	} else {
		logger.Warn("💰 未找到rewardAccount配置")
	}

	if rewardAmount, exists := params.Config.Config["rewardAmount"]; exists {
		logger.Info("🔍 找到rewardAmount配置", "type", fmt.Sprintf("%T", rewardAmount), "value", rewardAmount)
		if amount, ok := rewardAmount.(*big.Int); ok {
			vcity_dpos.config.RewardAmount = amount
		} else {
			logger.Warn("💰 rewardAmount类型断言失败", "type", fmt.Sprintf("%T", rewardAmount))
		}
	} else {
		logger.Warn("💰 未找到rewardAmount配置")
	}

	// 解析提案表决周期配置
	logger.Info("📋 检查Config中的所有键", "keys", func() []string {
		keys := make([]string, 0, len(params.Config.Config))
		for k := range params.Config.Config {
			keys = append(keys, k)
		}
		return keys
	}())

	if proposalVotePeriod, exists := params.Config.Config["proposalVotePeriod"]; exists {
		logger.Info("🔍 找到proposalVotePeriod配置", "type", fmt.Sprintf("%T", proposalVotePeriod), "value", proposalVotePeriod)
		if period, ok := proposalVotePeriod.(time.Duration); ok {
			vcity_dpos.config.ProposalVotePeriod = period
			logger.Info("📋 ✅ 使用server层解析的提案表决周期", "period", period.String(), "seconds", period.Seconds())
		} else {
			logger.Warn("📋 ❌ proposalVotePeriod类型断言失败",
				"type", fmt.Sprintf("%T", proposalVotePeriod),
				"value", proposalVotePeriod,
				"尝试转换为time.Duration")
			if periodStr, ok := proposalVotePeriod.(string); ok {
				// 支持 "d" 单位：转换为小时
				durationStr := periodStr
				if strings.Contains(durationStr, "d") {
					durationStr = strings.TrimSpace(durationStr)
					var days float64
					var suffix string
					if _, err := fmt.Sscanf(durationStr, "%f%s", &days, &suffix); err == nil && strings.HasPrefix(suffix, "d") {
						remaining := strings.TrimPrefix(suffix, "d")
						hours := days * 24
						if remaining != "" {
							durationStr = fmt.Sprintf("%.0fh%s", hours, remaining)
						} else {
							durationStr = fmt.Sprintf("%.0fh", hours)
						}
					} else {
						lastD := strings.LastIndex(durationStr, "d")
						if lastD > 0 {
							if _, err := fmt.Sscanf(durationStr[:lastD+1], "%fd", &days); err == nil {
								hours := days * 24
								durationStr = fmt.Sprintf("%.0fh%s", hours, durationStr[lastD+1:])
							}
						}
					}
				}
				if duration, err := time.ParseDuration(durationStr); err == nil {
					vcity_dpos.config.ProposalVotePeriod = duration
					logger.Info("📋 ✅ 从字符串成功解析提案表决周期", "period", duration.String())
				} else {
					logger.Warn("📋 ❌ 字符串解析失败", "error", err)
				}
			}
		}
	} else {
		logger.Warn("📋 ❌ 未找到proposalVotePeriod配置，将使用默认值")
	}

	// 解析提案有效期配置
	if proposalValidPeriod, exists := params.Config.Config["proposalValidPeriod"]; exists {
		logger.Info("🔍 找到proposalValidPeriod配置", "type", fmt.Sprintf("%T", proposalValidPeriod), "value", proposalValidPeriod)
		if period, ok := proposalValidPeriod.(time.Duration); ok {
			vcity_dpos.config.ProposalValidPeriod = period
			logger.Info("📋 ✅ 使用server层解析的提案有效期", "period", period.String(), "seconds", period.Seconds())
		} else {
			logger.Warn("📋 ❌ proposalValidPeriod类型断言失败",
				"type", fmt.Sprintf("%T", proposalValidPeriod),
				"value", proposalValidPeriod,
				"尝试转换为time.Duration")
			if periodStr, ok := proposalValidPeriod.(string); ok {
				// 支持 "d" 单位：转换为小时
				durationStr := periodStr
				if strings.Contains(durationStr, "d") {
					durationStr = strings.TrimSpace(durationStr)
					var days float64
					var suffix string
					if _, err := fmt.Sscanf(durationStr, "%f%s", &days, &suffix); err == nil && strings.HasPrefix(suffix, "d") {
						remaining := strings.TrimPrefix(suffix, "d")
						hours := days * 24
						if remaining != "" {
							durationStr = fmt.Sprintf("%.0fh%s", hours, remaining)
						} else {
							durationStr = fmt.Sprintf("%.0fh", hours)
						}
					} else {
						lastD := strings.LastIndex(durationStr, "d")
						if lastD > 0 {
							if _, err := fmt.Sscanf(durationStr[:lastD+1], "%fd", &days); err == nil {
								hours := days * 24
								durationStr = fmt.Sprintf("%.0fh%s", hours, durationStr[lastD+1:])
							}
						}
					}
				}
				if duration, err := time.ParseDuration(durationStr); err == nil {
					vcity_dpos.config.ProposalValidPeriod = duration
					logger.Info("📋 ✅ 从字符串成功解析提案有效期", "period", duration.String())
				} else {
					logger.Warn("📋 ❌ 字符串解析失败", "error", err)
				}
			}
		}
	} else {
		logger.Warn("📋 ❌ 未找到proposalValidPeriod配置，将使用默认值")
	}

	// 解析区块时间配置
	if blockTimeStr, exists := params.Config.Config["blockTime"]; exists {
		logger.Info("🔍 找到blockTime配置", "type", fmt.Sprintf("%T", blockTimeStr), "value", blockTimeStr)
		if blockTime, ok := blockTimeStr.(string); ok {
			if duration, err := time.ParseDuration(blockTime); err == nil {
				vcity_dpos.config.BlockTime = common.Duration{Duration: duration}
			} else {
				logger.Warn("⏰ blockTime解析失败", "value", blockTime, "error", err)
			}
		} else {
			logger.Warn("⏰ blockTime类型断言失败", "type", fmt.Sprintf("%T", blockTimeStr))
		}
	} else {
		logger.Warn("⏰ 未找到blockTime配置")
	}

	// 解析冻结相关配置
	if minFreezePeriod, exists := params.Config.Config["dpos_min_freeze_period"]; exists {
		logger.Info("🔍 找到dpos_min_freeze_period配置", "type", fmt.Sprintf("%T", minFreezePeriod), "value", minFreezePeriod)
		if period, ok := minFreezePeriod.(uint64); ok {
			vcity_dpos.config.MinFreezePeriod = period
			logger.Info("❄️ 使用server层解析的最小冻结期", "period", period, "seconds", period)
		} else if period, ok := minFreezePeriod.(int); ok {
			vcity_dpos.config.MinFreezePeriod = uint64(period)
			logger.Info("❄️ 使用server层解析的最小冻结期（从int转换）", "period", vcity_dpos.config.MinFreezePeriod, "seconds", vcity_dpos.config.MinFreezePeriod)
		} else if period, ok := minFreezePeriod.(float64); ok {
			vcity_dpos.config.MinFreezePeriod = uint64(period)
			logger.Info("❄️ 使用server层解析的最小冻结期（从float64转换）", "period", vcity_dpos.config.MinFreezePeriod, "seconds", vcity_dpos.config.MinFreezePeriod)
		} else {
			logger.Warn("❄️ dpos_min_freeze_period类型不支持", "type", fmt.Sprintf("%T", minFreezePeriod), "使用默认值604800")
			vcity_dpos.config.MinFreezePeriod = 604800
		}
	} else {
		logger.Warn("❄️ 未找到dpos_min_freeze_period配置，使用默认值604800秒（7天）")
		vcity_dpos.config.MinFreezePeriod = 604800
	}

	if unfreezeLockPeriod, exists := params.Config.Config["dpos_unfreeze_lock_period"]; exists {
		logger.Info("🔍 找到dpos_unfreeze_lock_period配置", "type", fmt.Sprintf("%T", unfreezeLockPeriod), "value", unfreezeLockPeriod)
		if period, ok := unfreezeLockPeriod.(uint64); ok {
			vcity_dpos.config.UnfreezeLockPeriod = period
			logger.Info("🔓 使用server层解析的解冻锁定期", "period", period, "seconds", period)
		} else if period, ok := unfreezeLockPeriod.(int); ok {
			vcity_dpos.config.UnfreezeLockPeriod = uint64(period)
			logger.Info("🔓 使用server层解析的解冻锁定期（从int转换）", "period", vcity_dpos.config.UnfreezeLockPeriod, "seconds", vcity_dpos.config.UnfreezeLockPeriod)
		} else if period, ok := unfreezeLockPeriod.(float64); ok {
			vcity_dpos.config.UnfreezeLockPeriod = uint64(period)
			logger.Info("🔓 使用server层解析的解冻锁定期（从float64转换）", "period", vcity_dpos.config.UnfreezeLockPeriod, "seconds", vcity_dpos.config.UnfreezeLockPeriod)
		} else {
			logger.Warn("🔓 dpos_unfreeze_lock_period类型不支持", "type", fmt.Sprintf("%T", unfreezeLockPeriod), "使用默认值1209600")
			vcity_dpos.config.UnfreezeLockPeriod = 1209600
		}
	} else {
		logger.Warn("🔓 未找到dpos_unfreeze_lock_period配置，使用默认值1209600秒（14天）")
		vcity_dpos.config.UnfreezeLockPeriod = 1209600
	}
	vcity_dpos.config.SecretsManager = params.SecretsManager
	vcity_dpos.config.Blockchain = params.Blockchain
	vcity_dpos.config.Logger = params.Logger

	if vcity_dpos.config.EpochDuration == 0 {
		logger.Warn("⚠️ epochDuration为0，设置默认值86400秒")
		vcity_dpos.config.EpochDuration = 86400 * time.Second
	}

	if vcity_dpos.config.RewardAmount == nil {
		logger.Warn("⚠️ rewardAmount为nil，设置默认值")
		vcity_dpos.config.RewardAmount, _ = new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
	}

	if vcity_dpos.config.CommissionRateDefault == 0 {
		logger.Warn("💼 commissionRateDefault为0，设置默认值10% (1000 基点)")
		vcity_dpos.config.CommissionRateDefault = 1000
	}

	if vcity_dpos.config.CommissionEffectivePeriod == 0 {
		defaultCommissionEffective := 21 * 24 * time.Hour
		logger.Warn("⏳ commissionEffectivePeriod为0，设置默认值21天", "duration", defaultCommissionEffective.String())
		vcity_dpos.config.CommissionEffectivePeriod = defaultCommissionEffective
	}

	if vcity_dpos.config.ProposalVotePeriod == 0 {
		logger.Warn("⚠️ proposalVotePeriod为0，设置默认值24小时")
		vcity_dpos.config.ProposalVotePeriod = 24 * time.Hour
	}
	if vcity_dpos.config.ProposalValidPeriod == 0 {
		logger.Warn("⚠️ proposalValidPeriod为0，设置默认值7天")
		vcity_dpos.config.ProposalValidPeriod = 7 * 24 * time.Hour
	}

	if vcity_dpos.config.BlockTime.Duration == 0 {
		vcity_dpos.config.BlockTime = common.Duration{Duration: 3 * time.Second}
		logger.Info("⏰ 使用默认DPoS区块时间3秒", "duration", vcity_dpos.config.BlockTime.Duration.String())
	} else {
		logger.Info("⏰ 使用配置文件中的DPoS区块时间", "duration", vcity_dpos.config.BlockTime.Duration.String())
	}
	vcity_dpos.config.Network = params.Network
	vcity_dpos.config.Executor = params.Executor

	vcity_dpos.logger.Debug("Config details",
		"ConfigPath", params.Config.Path,
		"ConfigType", fmt.Sprintf("%T", params.Config),
		"ConfigContent", fmt.Sprintf("%+v", params.Config))

	if params.Config.Path != "" {
		vcity_dpos.dataDir = filepath.Join(params.Config.Path, "dpos")
		vcity_dpos.logger.Debug("DPoS data directory set", "path", vcity_dpos.dataDir)
	} else {
		vcity_dpos.logger.Warn("Config path not set, DPoS data directory will not be available")

		defaultPath := "./dpos"
		vcity_dpos.dataDir = defaultPath
		vcity_dpos.logger.Info("Using default DPoS data directory", "path", defaultPath)
	}

	vcity_dpos.voters = make(map[types.Address]*VoterInfo)
	vcity_dpos.delegates = make(validator.AccountSet, 0)

	// 初始化双重签名检测器
	vcity_dpos.doubleSigningDetector = NewDoubleSigningDetector(logger)
	logger.Debug("双重签名检测器已初始化")

	return vcity_dpos, nil
}

// Initialize 初始化DPoS
func (d *DPoS) Initialize() error {
	d.logger.Debug("initializing dpos...")

	account, err := wallet.NewAccountFromSecret(d.config.SecretsManager)
	if err != nil {
		return fmt.Errorf("failed to read account data. Error: %w", err)
	}

	d.key = wallet.NewKey(account)

	if d.key == nil {
		return fmt.Errorf("failed to create wallet key")
	}

	// 注册DPoS实例到全局注册表
	// 使用固定key和节点地址作为key，确保能够被找到
	fixedKey := "vcity_dpos"
	nodeKey := d.key.Address().String()
	RegisterDPoSInstance(fixedKey, d)
	RegisterDPoSInstance(nodeKey, d)
	d.logger.Debug("DPoS实例已注册到全局注册表", "fixedKey", fixedKey, "nodeKey", nodeKey)

	// create and set syncer
	// blockTimeout 使用3倍的blockTime作为同步超时（参考IBFT和PolyBFT的实现）
	blockTimeout := d.config.BlockTime.Duration * 3
	if blockTimeout == 0 {
		d.logger.Warn("⚠️ blockTimeout为0，使用默认值9秒（3倍默认blockTime）")
		blockTimeout = 9 * time.Second
	}
	d.syncer = syncer.NewSyncer(
		d.config.Logger.Named("syncer"),
		d.config.Network,
		d.config.Blockchain,
		blockTimeout,
		d.config.ConsensusSwitchHeight,
	)

	// 新增：先初始化状态存储
	d.logger.Debug("Attempting to initialize state store",
		"dataDir", d.dataDir,
		"dataDirEmpty", d.dataDir == "",
		"dataDirLength", len(d.dataDir))

	if d.dataDir != "" {
		statePath := filepath.Join(d.dataDir, "dpos.db")
		d.logger.Debug("Creating state store", "path", statePath)

		// 确保目录存在
		if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
			d.logger.Error("Failed to create data directory", "path", filepath.Dir(statePath), "error", err)
		} else {
			d.logger.Debug("Data directory created/verified", "path", filepath.Dir(statePath))
		}

		state, err := newState(statePath, d.logger, d.closeCh)
		if err != nil {
			d.logger.Error("Failed to initialize state store", "path", statePath, "error", err)
			// 不返回错误，因为状态存储不是关键组件
		} else {
			d.state = state
			d.logger.Info("✅ State store initialized successfully", "path", statePath)

			if d.rewardDistributor != nil {
				d.rewardDistributor.stakeStore = d.state.StakeStore
			}
		}
	} else {
		d.logger.Warn("Data directory not set, state store will not be initialized")
	}

	// set blockchain backend
	d.blockchain = &blockchainWrapper{
		blockchain: d.config.Blockchain,
		executor:   d.config.Executor,
		keyAddr:    types.Address(d.key.Address()), // 设置当前节点的地址
		config:     d.config,                       // 传递DPoS配置
		logger:     d.logger,                       // 传递logger
		state:      d.state,                        // 传递State对象

		// 设置验证者更新回调函数
		onValidatorsUpdated: func(validators validator.AccountSet) error {
			// 更新 runtime 的验证者集合
			if d.runtime != nil {
				d.runtime.delegates = validators.Copy()
				d.delegates = validators.Copy()
				d.logger.Info("✅ 验证者集合已更新", "count", len(validators))
			}
			return nil
		},
	}

	// 将blockchain_wrapper设置为blockchain的executor，以启用奖励分配功能
	// 创建一个适配器，将blockchain_wrapper包装为blockchain.Executor
	executorAdapter := &executorAdapter{wrapper: d.blockchain.(*blockchainWrapper)}
	d.config.Blockchain.SetExecutor(executorAdapter)
	d.logger.Info("✅ 已将blockchain_wrapper设置为blockchain的executor，启用奖励分配功能")

	// 新增：设置余额查询器（使用真实实现）
	// 使用 runtime 的 getAccountBalance 方法实现余额查询
	if d.runtime != nil {
		d.balanceQuerier = &runtimeBalanceQuerier{runtime: d.runtime}
		d.logger.Info("✅ Balance querier initialized with runtime implementation")
	} else {
		d.balanceQuerier = nil
		d.logger.Warn("⚠️ Runtime not available, balance querier disabled")
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
		blockScheduler:   d.blockScheduler, // 设置固定时间窗口调度器
		BlockTime:        d.config.BlockTime,
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

	// set block time
	d.blockTime = d.config.BlockTime.Duration

	// 新增：初始化BLS网络通信
	if err := d.initializeBLSNetworking(); err != nil {
		d.logger.Error("Failed to initialize BLS networking", "error", err)
		// 不返回错误，因为BLS网络初始化失败不应该阻止DPoS启动
	}

	// initialize delegates
	if err := d.initializeDelegates(); err != nil {
		return fmt.Errorf("failed to initialize delegates: %w", err)
	}

	// 新增：初始化经济系统组件
	if err := d.initializeEconomicSystem(); err != nil {
		return fmt.Errorf("failed to initialize economic system: %w", err)
	}

	// 新增：初始化治理系统
	if err := d.InitializeGovernance(); err != nil {
		return fmt.Errorf("failed to initialize governance: %w", err)
	}

	return nil
}

// 新增：从创世块解析DPoS验证者
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

	// 3. 设置最小质押门槛（从参数系统或默认值获取）
	d.minStakeAmount, _ = new(big.Int).SetString("1000000000000000000000", 10) // 默认1000 VCITY
	if threshold, err := d.getCurrentParameterValue("dpos_delegate_threshold"); err == nil {
		switch v := threshold.(type) {
		case string:
			if bigAmount, ok := new(big.Int).SetString(v, 10); ok && bigAmount.Cmp(big.NewInt(0)) > 0 {
				d.minStakeAmount = bigAmount
			}
		case *big.Int:
			if v != nil && v.Cmp(big.NewInt(0)) > 0 {
				d.minStakeAmount = new(big.Int).Set(v)
			}
		}
	}
	d.logger.Info("使用最小质押门槛", "amount", d.minStakeAmount.String())

	// 4. 直接操作 runtime.delegates（如果runtime已初始化）
	if d.runtime == nil {
		d.logger.Warn("⚠️ runtime未初始化，无法解析验证者")
		return fmt.Errorf("runtime not initialized")
	}

	// 清空现有的delegates
	d.runtime.delegates = make(validator.AccountSet, 0)

	// 初始化创世验证者映射（如果尚未初始化）
	d.lock.Lock()
	if d.genesisValidators == nil {
		d.genesisValidators = make(map[types.Address]bool)
		d.logger.Info("🔧 初始化创世验证者映射")
	}
	d.lock.Unlock()

	d.logger.Info("🚨 DPoS验证者筛选开始",
		"totalCandidates", ibftValidators.Len(),
		"minStakeAmount", d.minStakeAmount.String())

	validValidatorCount := 0
	insufficientBalanceCount := 0

	// 5. 为每个验证者地址检查余额并生成BLS公钥
	for i := 0; i < ibftValidators.Len(); i++ {
		ibftValidator := ibftValidators[i]
		address := ibftValidator.Address

		// 创世验证者使用固定权重1000 VCITY，不受余额影响
		fixedVotingPower := new(big.Int)
		fixedVotingPower.SetString("1000000000000000000000", 10) // 1000 VCITY

		delegate := &validator.ValidatorMetadata{
			Address:     address,
			VotingPower: fixedVotingPower, // 使用固定权重，不依赖余额
			BlsKey:      nil,              // BLS公钥将在需要时获取
			IsActive:    true,
		}

		// 直接添加到 runtime.delegates
		d.runtime.delegates = append(d.runtime.delegates, delegate)

		// 添加到创世验证者映射
		d.lock.Lock()
		d.genesisValidators[address] = true
		d.lock.Unlock()

		validValidatorCount++

		d.logger.Info("✅ DPoS验证者创建成功（BLS公钥延迟获取）",
			"address", address.String(),
			"votingPower", fixedVotingPower.String(),
			"validatorIndex", validValidatorCount,
			"note", "创世验证者使用固定权重")
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

// 新增：从extraData解析验证者地址
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

	return validator.AccountSet(validatorList), nil
}

// OnBlockInserted 在区块写入后调用，用于清理交易池和处理区块事件
// 这个方法在同步区块和本地生产区块时都会被调用，确保交易池状态与链上状态一致
// 注意：本地生产区块时，consensusRuntime.OnBlockInserted 也会调用 ResetWithHeaders，
// 这里再次调用是安全的（幂等操作），确保两种路径的行为一致
func (d *DPoS) OnBlockInserted(fullBlock *types.FullBlock) {
	if d.txPool == nil {
		d.logger.Warn("⚠️ [DPoS.OnBlockInserted] txPool 为 nil，跳过交易池清理",
			"blockNumber", fullBlock.Block.Number(),
			"note", "这会导致交易池nonce未更新")
		return
	}

	d.logger.Debug("🔵 [DPoS.OnBlockInserted] 清理交易池",
		"blockNumber", fullBlock.Block.Number(),
		"blockHash", fullBlock.Block.Hash().String()[:16],
		"txCount", len(fullBlock.Block.Transactions))

	// 调用交易池的 ResetWithHeaders 来清理已打包的交易
	d.txPool.ResetWithHeaders(fullBlock.Block.Header)
}

func (d *DPoS) GetCurrentRound() uint64 {
	d.lock.RLock()
	defer d.lock.RUnlock()

	return d.currentRound
}

func (d *DPoS) GetCurrentDelegate() types.Address {
	// 已删除 currentDelegateIndex
	// 现在完全基于时间slot实时计算
	if d.state == nil || d.state.StakeStore == nil {
		return types.ZeroAddress
	}

	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil || len(validators) == 0 {
		return types.ZeroAddress
	}

	// 基于时间slot计算当前委托者
	if d.runtime != nil && d.runtime.config != nil && d.runtime.config.blockScheduler != nil {
		now := time.Now()
		genesisTime := d.runtime.config.blockScheduler.GetGenesisTime()
		blockWindow := d.runtime.config.blockScheduler.GetBlockWindow()
		timeSinceGenesis := now.Sub(genesisTime)
		currentSlot := int(timeSinceGenesis / blockWindow)
		currentValidatorIndex := currentSlot % len(validators)

		return validators[currentValidatorIndex].Address
	}

	return types.ZeroAddress
}

// GetVoters returns the current voters map for external access
func (d *DPoS) GetVoters() map[types.Address]*VoterInfo {
	d.lock.RLock()
	defer d.lock.RUnlock()

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

// GetState exposes the internal state pointer for read-only operations
func (d *DPoS) GetState() *State {
	d.lock.RLock()
	defer d.lock.RUnlock()
	return d.state
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

	// 启动签名去重清理协程
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

// DefaultDPoSConfig 返回默认配置
func DefaultDPoSConfig() *DPoSConfig {
	return &DPoSConfig{
		DelegateCount:             21,
		BlockTime:                 common.Duration{Duration: 15 * time.Second},
		RoundTime:                 common.Duration{Duration: 30 * time.Second},
		VoteLockTime:              86400,               // 24 hours
		RewardRatio:               100,                 // 1%
		ProposalVotePeriod:        24 * time.Hour,      // 默认提案表决周期 24小时
		ProposalValidPeriod:       7 * 24 * time.Hour,  // 默认提案有效期 7天
		MinFreezePeriod:           604800,              // 默认最小冻结期 7天（秒）
		UnfreezeLockPeriod:        1209600,             // 默认解冻锁定期 14天（秒）
		CommissionRateDefault:     1000,                // 默认佣金率 10%
		CommissionEffectivePeriod: 21 * 24 * time.Hour, // 默认佣金生效周期 21天
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

	// 验证经济系统配置
	if c.RewardAccount == types.ZeroAddress {
		return fmt.Errorf("reward_account is required")
	}
	if c.RewardAmount == nil || c.RewardAmount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("reward_amount must be positive")
	}
	return nil
}

// GetConfigSummary 获取配置摘要
func (c *DPoSConfig) GetConfigSummary() map[string]interface{} {
	return map[string]interface{}{
		"delegate_count": c.DelegateCount,
		"block_time":     c.BlockTime.String(),
		"round_time":     c.RoundTime.String(),
		"vote_lock_time": c.VoteLockTime,
		"reward_ratio":   c.RewardRatio,
		"epoch_duration": c.EpochDuration.String(),
		"reward_account": c.RewardAccount.String(),
		"reward_amount":  c.RewardAmount.String(),
	}
}

// 启动时直接调用和命令一样的数据源方法
func (d *DPoS) callCommandDataSourcesOnStartup() error {
	// 数据源1: 从store获取验证者信息 (与命令中的 GetValidators() 一致)
	if d.state != nil && d.state.StakeStore != nil {
		validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
		if err != nil {
			d.logger.Warn("⚠️ 获取验证者信息失败", "error", err)
		} else {

			// 获取质押信息用于对比
			stakingInfo, stakingErr := d.state.StakeStore.GetStakingInfo()
			if stakingErr != nil {
				d.logger.Warn("⚠️ 获取质押信息失败，无法显示票数对比", "error", stakingErr)
			}

			for _, validator := range validators {
				// 查找对应的票数信息
				var totalVotes *big.Int
				var voterCount int
				for _, stake := range stakingInfo {
					if stake.Delegate.String() == validator.Address.String() {
						if totalVotes == nil {
							totalVotes = big.NewInt(0)
						}
						totalVotes.Add(totalVotes, stake.Amount)
						voterCount++
					}
				}
				_ = totalVotes
				_ = voterCount
			}
		}
	} else {
		d.logger.Warn("⚠️ store不可用，无法获取验证者信息")
	}

	// 数据源2: 从store获取质押信息 (与命令中的 GetStakingInfo() 一致)
	if d.state != nil && d.state.StakeStore != nil {
		_, err := d.state.StakeStore.GetStakingInfo()
		if err != nil {
			d.logger.Warn("⚠️ 获取质押信息失败", "error", err)
		}
	}

	// 对比：显示内存中的受托人信息
	d.lock.RLock()
	defer d.lock.RUnlock()

	d.logger.Debug("🎯 从与命令相同的数据源读取完成")
	return nil
}

// 将验证者数据同步到数据库
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

// 获取数据目录路径
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

// GetValidators 获取DPoS验证者集合（公共方法，供外部调用）
// 注意：不能通过接口调用，因为GetValidators本身就是接口的实现
// 直接使用原有逻辑，避免无限递归
func (d *DPoS) GetValidators() validator.AccountSet {
	// 直接使用原有逻辑，避免通过接口调用造成无限递归
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		d.logger.Info("📊 [DPoS.GetValidators] 从 runtime.delegates 返回验证者",
			"count", len(d.runtime.delegates),
			"validatorsList", func() []string {
				var vs []string
				for i, v := range d.runtime.delegates {
					vs = append(vs, fmt.Sprintf("[%d]%s", i, v.Address.String()))
				}
				return vs
			}())
		return d.runtime.delegates
	}

	// 如果runtime.delegates为空，尝试从数据库读取（与GetDelegates保持一致）
	if d.state != nil && d.state.StakeStore != nil {
		d.logger.Info("🔍 [DPoS.GetValidators] runtime.delegates为空，尝试从数据库读取验证者")
		if dbValidators, err := d.state.StakeStore.GetValidatorsWithFilter(false); err == nil && len(dbValidators) > 0 {
			d.logger.Info("✅ [DPoS.GetValidators] 从数据库成功读取验证者",
				"count", len(dbValidators),
				"validatorsList", func() []string {
					var vs []string
					for i, v := range dbValidators {
						vs = append(vs, fmt.Sprintf("[%d]%s", i, v.Address.String()))
					}
					return vs
				}())
			// 同步到内存，避免下次再查数据库
			if d.runtime != nil {
				d.runtime.delegates = dbValidators.Copy()
				d.delegates = dbValidators.Copy()
				d.logger.Info("✅ [DPoS.GetValidators] 已同步验证者数据到内存", "count", len(dbValidators))
			}
			return dbValidators
		} else if err != nil {
			d.logger.Warn("⚠️ [DPoS.GetValidators] 从数据库读取验证者失败", "error", err)
		}
	}

	// 如果 runtime 不可用且数据库读取失败，返回空集合
	d.logger.Warn("⚠️ [DPoS.GetValidators] runtime 不可用且数据库读取失败，返回空集合",
		"runtimeIsNil", d.runtime == nil,
		"delegatesIsNil", d.runtime != nil && d.runtime.delegates == nil,
		"delegatesLen", func() int {
			if d.runtime != nil && d.runtime.delegates != nil {
				return len(d.runtime.delegates)
			}
			return 0
		}(),
		"stateIsNil", d.state == nil,
		"stakeStoreIsNil", d.state != nil && d.state.StakeStore == nil)
	return validator.AccountSet{}
}

// syncDelegateFromDatabase 从数据库同步指定验证者到内存
func (d *DPoS) syncDelegateFromDatabase(delegate types.Address) error {
	if d.state == nil || d.state.StakeStore == nil {
		return fmt.Errorf("state store not available")
	}

	// 从数据库获取验证者信息
	validators, err := d.state.StakeStore.GetValidatorsWithFilter(false)
	if err != nil {
		return fmt.Errorf("failed to get validators from database: %w", err)
	}

	// 查找指定验证者
	for _, validator := range validators {
		if validator.Address == delegate {
			// 更新内存中的验证者信息
			found := false
			for i, del := range d.delegates {
				if del.Address == delegate {
					d.delegates[i] = validator
					found = true
					break
				}
			}

			// 如果内存中不存在，添加到内存
			if !found {
				d.delegates = append(d.delegates, validator)
			}

			// 同步到runtime
			if d.runtime != nil {
				found = false
				for i, del := range d.runtime.delegates {
					if del.Address == delegate {
						d.runtime.delegates[i] = validator
						found = true
						break
					}
				}
				if !found {
					d.runtime.delegates = append(d.runtime.delegates, validator)
				}
			}

			d.logger.Debug("✅ 验证者信息已从数据库同步到内存",
				"delegate", delegate.String(),
				"votingPower", validator.VotingPower.String(),
				"isActive", validator.IsActive)
			return nil
		}
	}

	return fmt.Errorf("delegate not found in database: %s", delegate.String())
}

// getEpochSize 根据配置计算epoch大小（区块数）
func (r *dposRuntime) getEpochSize() uint64 {
	if r.config == nil || r.config.dposBackend == nil {
		r.logger.Warn("⚠️ dposRuntime配置为空，使用默认epoch大小")
		return 5 // 默认值：10秒 / 2秒 = 5个区块
	}

	dposInstance, ok := r.config.dposBackend.(*DPoS)
	if !ok {
		r.logger.Warn("⚠️ 无法转换为DPoS实例，使用默认epoch大小")
		return 5
	}

	return dposInstance.getEpochSize()
}

// executeBatchStateUpdate 保留在 dpos.go 中（函数复杂，依赖较多）
// runtimeBalanceQuerier 实现 NativeTokenBalanceQuerier 接口
type runtimeBalanceQuerier struct {
	runtime *dposRuntime
}

// GetNativeTokenBalance 通过 runtime 查询账户余额
func (r *runtimeBalanceQuerier) GetNativeTokenBalance(address types.Address) (*big.Int, error) {
	return r.runtime.getAccountBalance(address)
}

// getAccountBalance 查询账户余额
func (r *dposRuntime) getAccountBalance(address types.Address) (*big.Int, error) {
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
				r.logger.Error("❌ 无法创建状态快照", "error", err)
				return big.NewInt(0), fmt.Errorf("failed to create state snapshot: %w", err)
			}

			account, err := snapshot.GetAccount(address)
			if err != nil {
				r.logger.Warn("⚠️ 无法获取账户信息，返回0余额", "address", address.String(), "error", err)
				return big.NewInt(0), nil
			}

			// 检查账户余额是否为空，避免空指针解引用
			if account == nil || account.Balance == nil {
				r.logger.Warn("⚠️ 账户或余额为空，返回0余额", "address", address.String())
				return big.NewInt(0), nil
			}

			r.logger.Debug("✅ 成功查询到验证者余额", "address", address.String(), "balance", account.Balance.String())
			return account.Balance, nil
		}
	}

	// 如果无法通过backend查询，返回0余额
	r.logger.Warn("⚠️ 无法通过backend查询余额，返回0余额", "address", address.String())
	return big.NewInt(0), nil
}
