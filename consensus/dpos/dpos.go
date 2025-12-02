package dpos

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
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
	bolt "go.etcd.io/bbolt"
)

var (
	// dposInstances 全局 DPoS 实例注册表
	// ⚠️ 注意：当前使用全局变量管理实例，在单实例场景下可以正常工作。
	// 如果未来需要支持多实例场景，应该重构为使用 InstanceRegistry 结构体。
	// 相关文档: GLOBAL_STATE_MANAGEMENT_EXPLANATION.md
	dposInstances      = make(map[string]*DPoS)
	dposMutex          sync.RWMutex
	ErrBusinessInvalid = errors.New("business invalid")
)

func (d *DPoS) applyCommissionDefaults(info *DelegateInfo) {
	if info == nil || d == nil || d.config == nil {
		return
	}
	if info.CommissionRate == 0 {
		info.CommissionRate = d.config.CommissionRateDefault
	}
}

func (d *DPoS) populateCommissionFields(delegate types.Address, info *DelegateInfo) {
	if info == nil || d == nil {
		return
	}

	store, err := d.getStateStore()
	if err != nil {
		return
	}

	existing, err := store.GetDelegateInfo(delegate)
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

func RegisterDPoSInstance(key string, dpos *DPoS) {
	dposMutex.Lock()
	defer dposMutex.Unlock()
	dposInstances[key] = dpos
}

func GetDPoSInstance(key string) (*DPoS, bool) {
	dposMutex.RLock()
	defer dposMutex.RUnlock()
	dpos, exists := dposInstances[key]
	return dpos, exists
}

func GetAllDPoSInstances() map[string]*DPoS {
	dposMutex.RLock()
	defer dposMutex.RUnlock()

	result := make(map[string]*DPoS)
	for key, instance := range dposInstances {
		result[key] = instance
	}
	return result
}

func UnregisterDPoSInstance(key string) {
	dposMutex.Lock()
	defer dposMutex.Unlock()
	delete(dposInstances, key)
}

type dposBackend interface {
	// 获取指定区块的受托人集合--实际参与共识的节点
	GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error)
	// 数据库事务中获取受托人集合
	GetDelegatesWithTx(blockNumber uint64, parents []*types.Header, dbTx *bolt.Tx) (validator.AccountSet, error)
	// 获取当前内存中的受托人集合
	GetCurrentDelegates() validator.AccountSet
	// 获取指定区块的投票权重
	GetVotingPower(blockNumber uint64, delegate types.Address) (*big.Int, error)
	// 在数据库事务中获取投票权重
	GetVotingPowerWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error)
	// 获取当前轮次
	GetCurrentRound() uint64
	// 获取当前受托人
	GetCurrentDelegate() types.Address
	// 获取受托人索引
	GetDelegateIndex(delegate types.Address) uint64
	// 保存指定区块的特定验证者集合到数据库
	SaveValidatorSetForBlockWithValidators(blockNumber uint64, validators validator.AccountSet) error
}

// DPoSConfig 配置结构
type DPoSConfig struct {
	DelegateCount uint64          `json:"delegateCount"`
	BlockTime     common.Duration `json:"blockTime"`
	// 轮次时间 (所有受托人完成一轮的时间)
	RoundTime           common.Duration `json:"roundTime"`
	DPoSValidatorsCount uint64          `json:"dpos_validators_count"`
	Blockchain          *blockchain.Blockchain
	Logger              hclog.Logger
	Network             *network.Server
	SecretsManager      secrets.SecretsManager
	Executor            *state.Executor
	// 最小投票权重
	MinVotingPower        *big.Int                      `json:"minVotingPower"`
	InitialDelegates      []*validator.GenesisValidator `json:"initialDelegates"`
	VoteLockTime          uint64                        `json:"voteLockTime"`
	RewardRatio           uint64                        `json:"rewardRatio"`
	ConsensusSwitchHeight uint64                        `json:"consensusSwitchHeight"`
	ValidatorsCount       uint64                        `json:"validatorsCount" yaml:"validatorsCount"`

	EpochDuration       time.Duration `json:"epochDuration" yaml:"epochDuration"`
	RewardAccount       types.Address `json:"rewardAccount" yaml:"rewardAccount"`
	RewardAmount        *big.Int      `json:"rewardAmount" yaml:"rewardAmount"`
	ProposalVotePeriod  time.Duration `json:"proposalVotePeriod" yaml:"dpos_proposal_vote_period"`   // 提案表决周期
	ProposalValidPeriod time.Duration `json:"proposalValidPeriod" yaml:"dpos_proposal_valid_period"` // 提案有效期

	MinFreezePeriod    uint64 `json:"min_freeze_period" yaml:"dpos_min_freeze_period"`       // 最小冻结期（秒）
	UnfreezeLockPeriod uint64 `json:"unfreeze_lock_period" yaml:"dpos_unfreeze_lock_period"` // 解冻锁定期（秒）

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
	state  *State
	key    *wallet.Key
	logger hclog.Logger

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

	consensusTopic *network.Topic

	delegates    validator.AccountSet
	voters       map[types.Address]*VoterInfo
	currentRound uint64

	closeCh chan struct{}
	lock    sync.RWMutex

	blockchain blockchainBackend
	txPool     txPoolInterface

	blockTime time.Duration

	dataDir string

	cache          *DPoSCache
	batchProcessor *BatchProcessor
	metrics        *DPoSMetrics

	pendingValidatorUpdate bool

	currentEpoch      uint64
	epochValidators   validator.AccountSet
	faultyValidators  map[types.Address]bool
	missedBlocksCount map[types.Address]uint64

	// 受投票影响的验证者地址集合
	affectedDelegates map[types.Address]bool

	// 奖励分配信息
	pendingRewardDistribution *RewardDistributionInfo

	// 故障消减信息（只包含 missed blocks 的消减，不包含双重签名）
	pendingSlashingInfo   *SlashingInfo
	pendingFaultFlags     []FaultFlagInfo
	pendingEpochEndHeader *types.Header

	epochManager      *TimeBasedEpochManager
	blockTracker      *BlockProductionTracker
	rewardDistributor *RewardDistributor

	blockScheduler        *BlockScheduler
	doubleSigningDetector *DoubleSigningDetector
	balanceQuerier        NativeTokenBalanceQuerier
	minStakeAmount        *big.Int // 最小质押门槛

	lastLogTime       map[string]time.Time   // 最后日志时间
	logMutex          sync.RWMutex           // 日志锁
	genesisExtraData  []byte                 // 创世块extraData
	genesisValidators map[types.Address]bool // 创世验证者地址映射

	blsLoadingComplete bool
	blsLoadingMutex    sync.RWMutex
	blsLoadingWaitCh   chan struct{}

	parameterProposals map[string]*ParameterProposal // 提案存储
	parameterUpdates   []*ParameterUpdate            // 参数更新记录
	activeProposals    map[string]bool               // 活跃提案
	proposalCounter    uint64                        // 提案计数器
	votableParameters  map[string]*ParameterInfo     // 可表决参数配置

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

// 清理缓存，避免跨epoch误用
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

func (d *DPoS) GetCurrentBlockNumber() uint64 {
	return d.getCurrentBlockNumber()
}

func (d *DPoS) GetConsensusSwitchHeight() uint64 {
	if d.config == nil {
		return 0
	}
	return d.config.ConsensusSwitchHeight
}

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
	if err := d.validateDependencies(); err != nil {
		return WrapError("validate dependencies", err)
	}
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
		return WrapError("sync load BLS keys", err)
	}
	d.logger.Info("✅ BLS公钥同步获取完成")

	// 4. 启动syncer（BLS公钥加载完成后）
	d.logger.Info("🌐 开始启动syncer...")
	if err := d.syncer.Start(); err != nil {
		// 🆕 检查是否是topic冲突错误，如果是则忽略
		if strings.Contains(err.Error(), "topic already exists") {
			d.logger.Warn("⚠️ syncer启动遇到topic冲突，但继续启动DPoS runtime", "error", err)
		} else {
			d.logger.Error("❌ syncer启动失败（非topic冲突）", "error", err)
			return WrapError("start syncer", err)
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

		d.logger.Info("🔍 DPoS runtime详细状态检查",
			"resourceMonitorIsNil", d.runtime.resourceMonitor == nil,
			"goroutineManagerIsNil", func() bool {
				if d.runtime.resourceMonitor != nil {
					return d.runtime.resourceMonitor.goroutineManager == nil
				}
				return true
			}(),
			"delegatesCount", len(d.runtime.delegates))

		d.logger.Info("🚀 调用d.runtime.start()...")
		if err := d.runtime.start(); err != nil {
			d.logger.Error("❌ 启动DPoS runtime失败", "error", err)
			return WrapError("start DPoS runtime", err)
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

	if d.state != nil {
		go d.state.startStatsReleasing()
	}

	if err := d.restoreVotingDataFromDatabase(); err != nil {
		d.logger.Error("Failed to restore voting data from database", "error", err)
	}

	if err := d.callCommandDataSourcesOnStartup(); err != nil {
		d.logger.Warn("Failed to call command data sources on startup", "error", err)
	}

	d.initPerformanceOptimizations()

	if err := d.initializeModules(); err != nil {
		return err
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

	// 🆕 保存当前epoch的出块记录，确保数据不丢失
	if d.blockTracker != nil {
		d.blockTracker.SaveCurrentEpoch()
	}

	if d.key != nil {
		key := d.key.Address().String()
		UnregisterDPoSInstance(key)
		d.logger.Info("DPoS实例已从全局注册表中注销", "address", key)
	}

	return nil
}

var _ dposBackend = (*DPoS)(nil)

func Factory(params *consensus.Params) (consensus.Consensus, error) {
	logger := params.Logger.Named("dpos")

	setupHeaderHashFunc()

	vcity_dpos := &DPoS{
		closeCh:     make(chan struct{}),
		logger:      logger,
		txPool:      params.TxPool,
		rawConfig:   params.Config.Config,       // 存储原始配置
		lastLogTime: make(map[string]time.Time), // 初始化日志频率限制
	}

	logger.Info("🔍 开始解析DPoS经济系统配置", "configKeys", len(params.Config.Config))

	// 使用 ConfigBuilder 解析配置
	builder := NewConfigBuilder(params.Config.Config, logger)
	vcity_dpos.config = builder.
		ParseBasicConfig().
		ParseCommissionConfig().
		ParseSlashingConfig().
		ParseEpochConfig().
		ParseProposalConfig().
		ParseFreezeConfig().
		SetDefaults().
		Build()

	// 设置依赖注入
	vcity_dpos.config.SecretsManager = params.SecretsManager
	vcity_dpos.config.Blockchain = params.Blockchain
	vcity_dpos.config.Logger = params.Logger
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
	vcity_dpos.affectedDelegates = make(map[types.Address]bool)

	// 初始化双重签名检测器
	vcity_dpos.doubleSigningDetector = NewDoubleSigningDetector(logger)
	logger.Info("✅ 双重签名检测器已初始化")

	return vcity_dpos, nil
}

func (d *DPoS) Initialize() error {
	d.logger.Debug("initializing dpos...")

	account, err := wallet.NewAccountFromSecret(d.config.SecretsManager)
	if err != nil {
		return WrapError("read account data", err)
	}

	d.key = wallet.NewKey(account)

	if d.key == nil {
		return fmt.Errorf("failed to create wallet key")
	}

	fixedKey := "vcity_dpos"
	nodeKey := d.key.Address().String()
	RegisterDPoSInstance(fixedKey, d)
	RegisterDPoSInstance(nodeKey, d)
	d.logger.Debug("DPoS实例已注册到全局注册表", "fixedKey", fixedKey, "nodeKey", nodeKey)

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

	d.logger.Debug("Attempting to initialize state store",
		"dataDir", d.dataDir,
		"dataDirEmpty", d.dataDir == "",
		"dataDirLength", len(d.dataDir))

	if d.dataDir != "" {
		statePath := filepath.Join(d.dataDir, "dpos.db")
		d.logger.Info("Creating state store", "path", statePath)

		if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
			d.logger.Error("Failed to create data directory", "path", filepath.Dir(statePath), "error", err)
		} else {
			d.logger.Info("Data directory created/verified", "path", filepath.Dir(statePath))
		}

		state, err := newState(statePath, d.logger, d.closeCh)
		if err != nil {
			d.logger.Error("Failed to initialize state store", "path", statePath, "error", err)
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

	d.blockchain = &blockchainWrapper{
		blockchain: d.config.Blockchain,
		executor:   d.config.Executor,
		keyAddr:    types.Address(d.key.Address()),
		config:     d.config,
		logger:     d.logger,
		state:      d.state,

		onValidatorsUpdated: func(validators validator.AccountSet) error {
			if d.runtime != nil {
				d.runtime.delegates = validators.Copy()
				d.delegates = validators.Copy()
				d.logger.Info("✅ 验证者集合已更新", "count", len(validators))
			}
			return nil
		},
	}

	executorAdapter := &executorAdapter{wrapper: d.blockchain.(*blockchainWrapper)}
	d.config.Blockchain.SetExecutor(executorAdapter)
	d.logger.Info("✅ 已将blockchain_wrapper设置为blockchain的executor，启用奖励分配功能")

	if d.runtime != nil {
		d.balanceQuerier = &runtimeBalanceQuerier{runtime: d.runtime}
		d.logger.Info("✅ Balance querier initialized with runtime implementation")
	} else {
		d.balanceQuerier = nil
		d.logger.Warn("⚠️ Runtime not available, balance querier disabled")
	}

	// set block time
	d.blockTime = d.config.BlockTime.Duration

	if err := d.initializeBLSNetworking(); err != nil {
		d.logger.Error("Failed to initialize BLS networking", "error", err)
	}

	if err := d.initializeDelegates(); err != nil {
		return WrapError("initialize delegates", err)
	}

	if err := d.initializeEconomicSystem(); err != nil {
		return WrapError("initialize economic system", err)
	}

	if err := d.InitializeGovernance(); err != nil {
		return WrapError("initialize governance", err)
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
		blockScheduler:   d.blockScheduler,
		BlockTime:        d.config.BlockTime,
	}

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

	if err := d.runtime.initializeRuntime(); err != nil {
		return WrapError("initialize runtime", err)
	}

	if err := d.runtime.setupNetworkIntegration(); err != nil {
		d.logger.Error("failed to setup network integration", "error", err)
		return WrapError("setup network integration", err)
	}

	return nil
}

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

	d.txPool.ResetWithHeaders(fullBlock.Block.Header)
}

func (d *DPoS) GetCurrentRound() uint64 {
	d.lock.RLock()
	defer d.lock.RUnlock()

	return d.currentRound
}

func (d *DPoS) GetCurrentDelegate() types.Address {
	store, err := d.getStateStore()
	if err != nil {
		return types.ZeroAddress
	}

	validators, err := store.GetValidatorsWithFilter(false)
	if err != nil || len(validators) == 0 {
		return types.ZeroAddress
	}

	currentValidatorIndex, err := d.calculateCurrentValidatorIndex(validators)
	if err == nil && currentValidatorIndex >= 0 && currentValidatorIndex < len(validators) {
		return validators[currentValidatorIndex].Address
	}

	return types.ZeroAddress
}

func (d *DPoS) GetVoters() map[types.Address]*VoterInfo {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// Create a copy of the voters map to avoid race conditions
	votersCopy := make(map[types.Address]*VoterInfo)
	for addr, voter := range d.voters {
		votersCopy[addr] = copyVoterInfo(voter)
	}
	return votersCopy
}

func (d *DPoS) GetDelegateIndex(delegate types.Address) uint64 {
	d.lock.RLock()
	defer d.lock.RUnlock()

	index, found := findValidatorIndex(d.delegates, delegate)
	if !found {
		return 0
	}
	return uint64(index)
}

// GetState exposes the internal state pointer for read-only operations
func (d *DPoS) GetState() *State {
	d.lock.RLock()
	defer d.lock.RUnlock()
	return d.state
}

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

func (d *DPoS) getDataDir() string {
	if d.dataDir != "" {
		return d.dataDir
	}
	return "" // 返回空字符串表示未找到
}

func (d *DPoS) GetValidators() validator.AccountSet {
	if d.runtime != nil && d.runtime.delegates != nil && len(d.runtime.delegates) > 0 {
		return d.runtime.delegates
	}
	return validator.AccountSet{}
}
