package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain/storage"
	"github.com/Vcity-Team/vcitychain/blockchain/storage/leveldb"
	"github.com/Vcity-Team/vcitychain/blockchain/storage/memory"
	consensusPolyBFT "github.com/Vcity-Team/vcitychain/consensus/polybft"
	"github.com/Vcity-Team/vcitychain/forkmanager"
	"github.com/Vcity-Team/vcitychain/gasprice"

	"github.com/Vcity-Team/vcitychain/archive"
	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus"
	consensusDPoS "github.com/Vcity-Team/vcitychain/consensus/dpos"
	consensusIBFT "github.com/Vcity-Team/vcitychain/consensus/ibft"
	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/jsonrpc"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/server/proto"
	"github.com/Vcity-Team/vcitychain/state"
	itrie "github.com/Vcity-Team/vcitychain/state/immutable-trie"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/state/runtime/addresslist"
	"github.com/Vcity-Team/vcitychain/state/runtime/tracer"
	"github.com/Vcity-Team/vcitychain/txpool"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validate"
	"github.com/hashicorp/go-hclog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

// parseDurationWithDays 解析时间字符串，支持 "d" (天) 单位
// 将 "d" 转换为小时数，例如 "1d" -> "24h", "7d" -> "168h"
func parseDurationWithDays(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	// 如果包含 "d"，需要转换
	if strings.Contains(s, "d") {
		// 使用正则匹配更准确，但简单处理：提取数字并乘以24
		var days float64
		var suffix string
		_, err := fmt.Sscanf(s, "%f%s", &days, &suffix)
		if err == nil && strings.HasPrefix(suffix, "d") {
			// 提取 "d" 后面的部分（如果有）
			remaining := strings.TrimPrefix(suffix, "d")
			hours := days * 24
			if remaining != "" {
				s = fmt.Sprintf("%.0fh%s", hours, remaining)
			} else {
				s = fmt.Sprintf("%.0fh", hours)
			}
		} else {
			// 简单处理：将最后一个 "d" 替换为 "h" 并乘以24
			lastD := strings.LastIndex(s, "d")
			if lastD > 0 {
				var days float64
				if _, err := fmt.Sscanf(s[:lastD+1], "%fd", &days); err == nil {
					hours := days * 24
					s = fmt.Sprintf("%.0fh%s", hours, s[lastD+1:])
				} else {
					return 0, fmt.Errorf("invalid duration format with days: %s", s)
				}
			}
		}
	}
	return time.ParseDuration(s)
}

var (
	errBlockTimeMissing = errors.New("block time configuration is missing")
	errBlockTimeInvalid = errors.New("block time configuration is invalid")
)

// Server is the central manager of the blockchain client
type Server struct {
	logger       hclog.Logger
	config       *Config
	state        state.State
	stateStorage itrie.Storage

	consensus  consensus.Consensus
	dposEngine consensus.Consensus // DPoS引擎

	// blockchain stack
	blockchain *blockchain.Blockchain
	chain      *chain.Chain

	// state executor
	executor *state.Executor

	// jsonrpc stack
	jsonrpcServer *jsonrpc.JSONRPC

	// system grpc server
	grpcServer *grpc.Server

	// libp2p network
	network *network.Server

	// transaction pool
	txpool *txpool.TxPool

	prometheusServer *http.Server

	// secrets manager
	secretsManager secrets.SecretsManager

	// restore
	restoreProgression *progress.ProgressionWrapper

	// gasHelper is providing functions regarding gas and fees
	gasHelper *gasprice.GasHelper
}

// StartDPoSEngine 实现DPoSEngineStarter接口，启动DPoS引擎
func (s *Server) StartDPoSEngine(height uint64) error {
	s.logger.Info("🚀 开始启动DPoS引擎", "height", height)

	// 如果DPoS引擎已经存在，先停止它
	if s.dposEngine != nil {
		s.logger.Info("🛑 目前已有DPoS引擎")
		return nil
	}

	// 创建DPoS引擎配置
	commissionRatio := s.config.DPoSCommissionRatio
	if commissionRatio == 0 {
		commissionRatio = 1000
	}

	engineConfig := map[string]interface{}{
		"consensusSwitchHeight":     float64(s.config.ConsensusSwitchHeight),
		"dposValidatorsCount":       float64(s.config.DPoSValidatorsCount), // 使用正确的字段名
		"dposDelegateThreshold":     s.config.DPoSDelegateThreshold,
		"backupValidatorsCount":     float64(s.config.BackupValidatorsCount),
		"maxMissedBlocks":           float64(s.config.MaxMissedBlocks),
		"dpos_min_freeze_period":    s.config.DPoSMinFreezePeriod,    // 🆕 最小冻结期（秒）
		"dpos_unfreeze_lock_period": s.config.DPoSUnfreezeLockPeriod, // 🆕 解冻锁定期（秒）
		"dposCommissionRatio":       float64(commissionRatio),
		"dpos_commission_ratio":     float64(commissionRatio),
	}
	commissionEffectiveStr := s.config.DPoSCommissionEffective
	if strings.TrimSpace(commissionEffectiveStr) == "" {
		commissionEffectiveStr = "21d"
	}
	engineConfig["dpos_commission_effective"] = commissionEffectiveStr
	if commissionEffectiveDuration, err := parseDurationWithDays(commissionEffectiveStr); err == nil {
		engineConfig["commissionEffectivePeriod"] = commissionEffectiveDuration
	} else {
		s.logger.Error("❌ 无效的佣金生效周期", "value", commissionEffectiveStr, "error", err)
		return fmt.Errorf("invalid dpos_commission_effective: %s", commissionEffectiveStr)
	}

	// 从YAML配置中获取区块时间（必须配置，不允许使用默认值）
	s.logger.Info("🔍 检查BlockTimeSeconds配置", "value", s.config.BlockTimeSeconds)
	if blockTimeSeconds := s.config.BlockTimeSeconds; blockTimeSeconds > 0 {
		blockTimeDuration := time.Duration(blockTimeSeconds) * time.Second
		engineConfig["blockTime"] = blockTimeDuration.String()
		s.logger.Info("⏰ 设置区块时间", "seconds", blockTimeSeconds, "duration", blockTimeDuration.String())
	} else {
		// ❌ 读取不到blockTime配置，返回错误（不允许使用默认值）
		err := fmt.Errorf("block_time_s configuration is required but not found or invalid (value: %d). Please set block_time_s in your YAML configuration file", s.config.BlockTimeSeconds)
		s.logger.Error("❌ 区块时间配置缺失", "error", err)
		return err
	}

	// 从YAML配置中获取epoch duration
	if epochDurationStr := s.config.DPoSEpochDuration; epochDurationStr != "" {
		if epochDuration, err := time.ParseDuration(epochDurationStr); err == nil {
			engineConfig["epochDuration"] = epochDuration
			s.logger.Info("⏰ 设置epoch duration", "duration", epochDuration.String())
		} else {
			s.logger.Error("❌ 无效的epoch duration", "duration", epochDurationStr, "error", err)
		}
	}

	// 从YAML配置中获取奖励分发地址
	if rewardAccountStr := s.config.DPoSRewardDistribution; rewardAccountStr != "" {
		if err := types.IsValidAddress(rewardAccountStr); err == nil {
			rewardAccount := types.StringToAddress(rewardAccountStr)
			engineConfig["rewardAccount"] = rewardAccount
			s.logger.Info("💰 设置奖励分发地址", "account", rewardAccount.String())
		} else {
			s.logger.Error("❌ 无效的奖励分发地址", "address", rewardAccountStr, "error", err)
			return fmt.Errorf("invalid reward distribution address: %s", rewardAccountStr)
		}
	} else {
		s.logger.Error("❌ 缺少奖励分发地址配置")
		return fmt.Errorf("dpos_reward_distribution is required in config file")
	}

	// 从YAML配置中获取奖励金额
	if rewardAmountStr := s.config.DPoSRewardAmount; rewardAmountStr != "" {
		if rewardAmount, ok := new(big.Int).SetString(rewardAmountStr, 10); ok {
			engineConfig["rewardAmount"] = rewardAmount
			s.logger.Info("💰 设置奖励金额", "amount", rewardAmount.String())
		} else {
			s.logger.Error("❌ 无效的奖励金额", "amount", rewardAmountStr)
			return fmt.Errorf("invalid reward amount: %s", rewardAmountStr)
		}
	} else {
		s.logger.Error("❌ 缺少奖励金额配置")
		return fmt.Errorf("dpos_reward_amount is required in config file")
	}

	// 从YAML配置中获取提案表决周期（时间字符串）
	s.logger.Info("🔍 检查DPoSProposalVotePeriod配置（第二次启动）",
		"DPoSProposalVotePeriod", s.config.DPoSProposalVotePeriod,
		"isEmpty", s.config.DPoSProposalVotePeriod == "")

	if proposalVotePeriodStr := s.config.DPoSProposalVotePeriod; proposalVotePeriodStr != "" {
		if proposalVotePeriod, err := parseDurationWithDays(proposalVotePeriodStr); err == nil {
			engineConfig["proposalVotePeriod"] = proposalVotePeriod
			s.logger.Info("✅ 成功解析提案表决周期（第二次启动）",
				"periodStr", proposalVotePeriodStr,
				"periodDuration", proposalVotePeriod.String(),
				"seconds", proposalVotePeriod.Seconds())
		} else {
			s.logger.Error("❌ 无效的提案表决周期（第二次启动）", "period", proposalVotePeriodStr, "error", err)
			// 不返回错误，使用默认值
			engineConfig["proposalVotePeriod"] = 24 * time.Hour // 默认24小时
		}
	} else {
		s.logger.Warn("⚠️ DPoSProposalVotePeriod配置为空（第二次启动），使用默认值24小时")
		engineConfig["proposalVotePeriod"] = 24 * time.Hour // 默认24小时
	}

	// 从YAML配置中获取提案有效期（时间字符串）
	s.logger.Info("🔍 检查DPoSProposalValidPeriod配置（第二次启动）",
		"DPoSProposalValidPeriod", s.config.DPoSProposalValidPeriod,
		"isEmpty", s.config.DPoSProposalValidPeriod == "")

	if proposalValidPeriodStr := s.config.DPoSProposalValidPeriod; proposalValidPeriodStr != "" {
		if proposalValidPeriod, err := parseDurationWithDays(proposalValidPeriodStr); err == nil {
			engineConfig["proposalValidPeriod"] = proposalValidPeriod
			s.logger.Info("✅ 成功解析提案有效期（第二次启动）",
				"periodStr", proposalValidPeriodStr,
				"periodDuration", proposalValidPeriod.String(),
				"seconds", proposalValidPeriod.Seconds())
		} else {
			s.logger.Error("❌ 无效的提案有效期（第二次启动）", "period", proposalValidPeriodStr, "error", err)
			// 不返回错误，使用默认值
			engineConfig["proposalValidPeriod"] = 7 * 24 * time.Hour // 默认7天
		}
	} else {
		s.logger.Warn("⚠️ DPoSProposalValidPeriod配置为空（第二次启动），使用默认值7天")
		engineConfig["proposalValidPeriod"] = 7 * 24 * time.Hour // 默认7天
	}

	s.logger.Info("✅ DPoS经济系统配置解析完成",
		"rewardAccount", engineConfig["rewardAccount"],
		"rewardAmount", engineConfig["rewardAmount"],
		"proposalPeriod", engineConfig["proposalPeriod"])

	// 获取区块时间
	blockTime, err := extractBlockTime(engineConfig)
	if err != nil {
		return fmt.Errorf("failed to extract block time: %w", err)
	}

	config := &consensus.Config{
		Params:      s.config.Chain.Params,
		Config:      engineConfig,
		Path:        filepath.Join(s.config.DataDir, "consensus"),
		DataDir:     s.config.DataDir,
		IsRelayer:   s.config.Relayer,
		RPCEndpoint: s.config.JSONRPC.JSONRPCAddr.String(),
	}

	// 创建DPoS引擎
	dposEngine, err := consensusDPoS.Factory(
		&consensus.Params{
			Context:               context.Background(),
			Config:                config,
			TxPool:                s.txpool,
			Network:               s.network,
			Blockchain:            s.blockchain,
			Executor:              s.executor,
			Grpc:                  s.grpcServer,
			Logger:                s.logger.Named("dpos"),
			SecretsManager:        s.secretsManager,
			BlockTime:             uint64(blockTime.Seconds()),
			NumBlockConfirmations: s.config.NumBlockConfirmations,
			MetricsInterval:       s.config.MetricsInterval,
		},
	)

	if err != nil {
		return fmt.Errorf("failed to create DPoS engine: %w", err)
	}

	// 初始化DPoS引擎
	if err := dposEngine.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize DPoS engine: %w", err)
	}

	// 启动DPoS引擎
	if err := dposEngine.Start(); err != nil {
		return fmt.Errorf("failed to start DPoS engine: %w", err)
	}

	// 更新区块链的共识引擎
	s.blockchain.SetConsensus(dposEngine)
	s.dposEngine = dposEngine

	s.logger.Info("✅ DPoS引擎启动成功", "height", height)
	return nil
}

// newFileLogger returns logger instance that writes all logs to a specified file.
// If log file can't be created, it returns an error
func newFileLogger(config *Config) (hclog.Logger, error) {
	logFileWriter, err := os.Create(config.LogFilePath)
	if err != nil {
		return nil, fmt.Errorf("could not create log file, %w", err)
	}

	return hclog.New(&hclog.LoggerOptions{
		Name:       "polygon",
		Level:      config.LogLevel,
		Output:     logFileWriter,
		JSONFormat: config.JSONLogFormat,
	}), nil
}

// newCLILogger returns minimal logger instance that sends all logs to standard output
func newCLILogger(config *Config) hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{
		Name:       "polygon",
		Level:      config.LogLevel,
		JSONFormat: config.JSONLogFormat,
	})
}

// newLoggerFromConfig creates a new logger which logs to a specified file.
// If log file is not set it outputs to standard output ( console ).
// If log file is specified, and it can't be created the server command will error out
func newLoggerFromConfig(config *Config) (hclog.Logger, error) {
	if config.LogFilePath != "" {
		fileLoggerInstance, err := newFileLogger(config)
		if err != nil {
			return nil, err
		}

		return fileLoggerInstance, nil
	}

	return newCLILogger(config), nil
}

// NewServer creates a new Minimal server, using the passed in configuration
func NewServer(config *Config) (*Server, error) {
	logger, err := newLoggerFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("could not setup new logger instance, %w", err)
	}

	m := &Server{
		logger:             logger.Named("server"),
		config:             config,
		chain:              config.Chain,
		grpcServer:         grpc.NewServer(grpc.UnaryInterceptor(unaryInterceptor)),
		restoreProgression: progress.NewProgressionWrapper(progress.ChainSyncRestore),
	}

	if config.Chain.Params.GetEngine() == string(IBFTConsensus) {
		m.logger.Info(common.IBFTImportantNotice)
	}

	m.logger.Info("Data dir", "path", config.DataDir)

	var dirPaths = []string{
		"blockchain",
		"trie",
	}

	// Generate all the paths in the dataDir
	if err := common.SetupDataDir(config.DataDir, dirPaths, 0770); err != nil {
		return nil, fmt.Errorf("failed to create data directories: %w", err)
	}

	if config.Telemetry.PrometheusAddr != nil {
		// Only setup telemetry if `PrometheusAddr` has been configured.
		if err := m.setupTelemetry(); err != nil {
			return nil, err
		}

		m.prometheusServer = m.startPrometheusServer(config.Telemetry.PrometheusAddr)
	}

	// Set up datadog profiler
	if ddErr := m.enableDataDogProfiler(); ddErr != nil {
		m.logger.Error("DataDog profiler setup failed", "err", ddErr.Error())
	}

	// Set up the secrets manager
	if err := m.setupSecretsManager(); err != nil {
		return nil, fmt.Errorf("failed to set up the secrets manager: %w", err)
	}

	// start libp2p
	{
		netConfig := config.Network
		netConfig.Chain = m.config.Chain
		netConfig.DataDir = filepath.Join(m.config.DataDir, "libp2p")
		netConfig.SecretsManager = m.secretsManager

		network, err := network.NewServer(logger, netConfig)
		if err != nil {
			return nil, err
		}
		m.network = network
	}

	// start blockchain object
	stateStorage, err := itrie.NewLevelDBStorage(filepath.Join(m.config.DataDir, "trie"), logger)
	if err != nil {
		return nil, err
	}

	m.stateStorage = stateStorage

	st := itrie.NewState(stateStorage)
	m.state = st

	m.executor = state.NewExecutor(config.Chain.Params, st, logger)

	// custom write genesis hook per consensus engine
	engineName := m.config.Chain.Params.GetEngine()
	if factory, exists := genesisCreationFactory[ConsensusType(engineName)]; exists {
		m.executor.GenesisPostHook = factory(m.config.Chain, engineName)
	}

	// apply allow list contracts deployer genesis data
	if m.config.Chain.Params.ContractDeployerAllowList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.AllowListContractsAddr,
			m.config.Chain.Params.ContractDeployerAllowList)
	}

	// apply block list contracts deployer genesis data
	if m.config.Chain.Params.ContractDeployerBlockList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.BlockListContractsAddr,
			m.config.Chain.Params.ContractDeployerBlockList)
	}

	// apply transactions execution allow list genesis data
	if m.config.Chain.Params.TransactionsAllowList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.AllowListTransactionsAddr,
			m.config.Chain.Params.TransactionsAllowList)
	}

	// apply transactions execution block list genesis data
	if m.config.Chain.Params.TransactionsBlockList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.BlockListTransactionsAddr,
			m.config.Chain.Params.TransactionsBlockList)
	}

	// apply bridge allow list genesis data
	if m.config.Chain.Params.BridgeAllowList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.AllowListBridgeAddr,
			m.config.Chain.Params.BridgeAllowList)
	}

	// apply bridge block list genesis data
	if m.config.Chain.Params.BridgeBlockList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.BlockListBridgeAddr,
			m.config.Chain.Params.BridgeBlockList)
	}

	var initialStateRoot = types.ZeroHash

	if ConsensusType(engineName) == PolyBFTConsensus {
		polyBFTConfig, err := consensusPolyBFT.GetPolyBFTConfig(config.Chain)
		if err != nil {
			return nil, err
		}

		if polyBFTConfig.InitialTrieRoot != types.ZeroHash {
			checkedInitialTrieRoot, err := itrie.HashChecker(polyBFTConfig.InitialTrieRoot.Bytes(), stateStorage)
			if err != nil {
				return nil, fmt.Errorf("error on state root verification %w", err)
			}

			if checkedInitialTrieRoot != polyBFTConfig.InitialTrieRoot {
				return nil, errors.New("invalid initial state root")
			}

			logger.Info("Initial state root checked and correct")

			initialStateRoot = polyBFTConfig.InitialTrieRoot
		}
	}

	genesisRoot, err := m.executor.WriteGenesis(config.Chain.Genesis.Alloc, initialStateRoot)
	if err != nil {
		return nil, err
	}

	if err := initForkManager(engineName, config.Chain); err != nil {
		return nil, err
	}

	// compute the genesis root state
	config.Chain.Genesis.StateRoot = genesisRoot

	// Use the london signer with eip-155 as a fallback one
	var signer crypto.TxSigner = crypto.NewLondonSigner(
		uint64(m.config.Chain.Params.ChainID),
		config.Chain.Params.Forks.IsActive(chain.Homestead, 0),
		crypto.NewEIP155Signer(
			uint64(m.config.Chain.Params.ChainID),
			config.Chain.Params.Forks.IsActive(chain.Homestead, 0),
		),
	)

	// create storage instance for blockchain
	var db storage.Storage
	{
		if m.config.DataDir == "" {
			db, err = memory.NewMemoryStorage(nil)
			if err != nil {
				return nil, err
			}
		} else {
			db, err = leveldb.NewLevelDBStorage(
				filepath.Join(m.config.DataDir, "blockchain"),
				m.logger,
			)
			if err != nil {
				return nil, err
			}
		}
	}

	// blockchain object
	m.blockchain, err = blockchain.NewBlockchain(
		logger,
		db,
		config.Chain,
		nil,
		m.executor,
		signer,
	)
	if err != nil {
		return nil, err
	}

	// here we can provide some other configuration
	m.gasHelper, err = gasprice.NewGasHelper(gasprice.DefaultGasHelperConfig, m.blockchain)
	if err != nil {
		return nil, err
	}

	m.executor.GetHash = m.blockchain.GetHashHelper

	{
		hub := &txpoolHub{
			state:      m.state,
			Blockchain: m.blockchain,
		}

		// 🆕 添加日志：输出交易池配置值，用于验证配置是否正确加载
		logger.Info("🔧 初始化交易池配置",
			"MaxSlots", m.config.MaxSlots,
			"MaxAccountEnqueued", m.config.MaxAccountEnqueued,
			"PriceLimit", m.config.PriceLimit,
		)

		// start transaction pool
		m.txpool, err = txpool.NewTxPool(
			logger,
			m.chain.Params.Forks,
			hub,
			m.grpcServer,
			m.network,
			&txpool.Config{
				MaxSlots:           m.config.MaxSlots,
				PriceLimit:         m.config.PriceLimit,
				MaxAccountEnqueued: m.config.MaxAccountEnqueued,
				ChainID:            big.NewInt(m.config.Chain.Params.ChainID),
			},
		)
		if err != nil {
			return nil, err
		}

		m.txpool.SetSigner(signer)
	}

	{
		// Setup consensus
		if err := m.setupConsensus(); err != nil {
			return nil, err
		}
		m.blockchain.SetConsensus(m.consensus)
	}

	// after consensus is done, we can mine the genesis block in blockchain
	// This is done because consensus might use a custom Hash function so we need
	// to wait for consensus because we do any block hashing like genesis
	if err := m.blockchain.ComputeGenesis(); err != nil {
		return nil, err
	}

	// initialize data in consensus layer
	if err := m.consensus.Initialize(); err != nil {
		return nil, err
	}

	// setup and start grpc server
	if err := m.setupGRPC(); err != nil {
		return nil, err
	}

	if err := m.network.Start(); err != nil {
		return nil, err
	}

	// setup and start jsonrpc server
	if err := m.setupJSONRPC(); err != nil {
		return nil, err
	}

	// restore archive data before starting
	if err := m.restoreChain(); err != nil {
		return nil, err
	}

	// start consensus
	if err := m.consensus.Start(); err != nil {
		return nil, err
	}

	m.txpool.SetBaseFee(m.blockchain.Header())
	m.txpool.Start()

	return m, nil
}

func unaryInterceptor(
	ctx context.Context,
	req interface{},
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// Validate request
	if err := validate.ValidateRequest(req); err != nil {
		return nil, err
	}

	return handler(ctx, req)
}

func (s *Server) restoreChain() error {
	if s.config.RestoreFile == nil {
		return nil
	}

	if err := archive.RestoreChain(s.blockchain, *s.config.RestoreFile, s.restoreProgression); err != nil {
		return err
	}

	return nil
}

type txpoolHub struct {
	state state.State
	*blockchain.Blockchain
}

// getAccountImpl is used for fetching account state from both TxPool and JSON-RPC
func getAccountImpl(state state.State, root types.Hash, addr types.Address) (*state.Account, error) {
	snap, err := state.NewSnapshotAt(root)
	if err != nil {
		return nil, fmt.Errorf("unable to get snapshot for root '%s': %w", root, err)
	}

	account, err := snap.GetAccount(addr)
	if err != nil {
		return nil, err
	}

	if account == nil {
		return nil, jsonrpc.ErrStateNotFound
	}

	return account, nil
}

func (t *txpoolHub) GetNonce(root types.Hash, addr types.Address) uint64 {
	account, err := getAccountImpl(t.state, root, addr)

	if err != nil {
		return 0
	}

	return account.Nonce
}

func (t *txpoolHub) GetBalance(root types.Hash, addr types.Address) (*big.Int, error) {
	account, err := getAccountImpl(t.state, root, addr)

	if err != nil {
		if errors.Is(err, jsonrpc.ErrStateNotFound) {
			return big.NewInt(0), nil
		}

		return big.NewInt(0), err
	}

	return account.Balance, nil
}

// setupSecretsManager sets up the secrets manager
func (s *Server) setupSecretsManager() error {
	secretsManagerConfig := s.config.SecretsManager
	if secretsManagerConfig == nil {
		// No config provided, use default
		secretsManagerConfig = &secrets.SecretsManagerConfig{
			Type: secrets.Local,
		}
	}

	secretsManagerType := secretsManagerConfig.Type
	secretsManagerParams := &secrets.SecretsManagerParams{
		Logger: s.logger,
	}

	if secretsManagerType == secrets.Local {
		// Only the base directory is required for
		// the local secrets manager
		secretsManagerParams.Extra = map[string]interface{}{
			secrets.Path: s.config.DataDir,
		}
	}

	// Grab the factory method
	secretsManagerFactory, ok := secretsManagerBackends[secretsManagerType]
	if !ok {
		return fmt.Errorf("secrets manager type '%s' not found", secretsManagerType)
	}

	// Instantiate the secrets manager
	secretsManager, factoryErr := secretsManagerFactory(
		secretsManagerConfig,
		secretsManagerParams,
	)

	if factoryErr != nil {
		return fmt.Errorf("unable to instantiate secrets manager, %w", factoryErr)
	}

	s.secretsManager = secretsManager

	return nil
}

// setupConsensus sets up the consensus mechanism
func (s *Server) setupConsensus() error {
	engineName := s.config.Chain.Params.GetEngine()
	engine, ok := consensusBackends[ConsensusType(engineName)]

	if !ok {
		return fmt.Errorf("consensus engine '%s' not found", engineName)
	}

	engineConfig, ok := s.config.Chain.Params.Engine[engineName].(map[string]interface{})
	if !ok {
		engineConfig = map[string]interface{}{}
	}

	// 🆕 新增：将共识切换高度添加到engineConfig中
	engineConfig["consensusSwitchHeight"] = float64(s.config.ConsensusSwitchHeight)

	// 🆕 新增：将DPoS验证者数量添加到engineConfig中
	engineConfig["dposValidatorsCount"] = float64(s.config.DPoSValidatorsCount)

	// 🆕 新增：将备用验证者数量和最大漏块数添加到engineConfig中
	engineConfig["backupValidatorsCount"] = float64(s.config.BackupValidatorsCount)
	engineConfig["maxMissedBlocks"] = float64(s.config.MaxMissedBlocks)

	// 🆕 新增：添加DPoS经济系统配置
	// 从YAML配置中获取epoch duration
	if epochDurationStr := s.config.DPoSEpochDuration; epochDurationStr != "" {
		if epochDuration, err := time.ParseDuration(epochDurationStr); err == nil {
			engineConfig["epochDuration"] = epochDuration
		}
	}

	// 从YAML配置中获取奖励分发地址
	if rewardAccountStr := s.config.DPoSRewardDistribution; rewardAccountStr != "" {
		if err := types.IsValidAddress(rewardAccountStr); err == nil {
			rewardAccount := types.StringToAddress(rewardAccountStr)
			engineConfig["rewardAccount"] = rewardAccount
		} else {
			s.logger.Error("❌ 无效的奖励分发地址", "address", rewardAccountStr, "error", err)
			return fmt.Errorf("invalid reward distribution address: %s", rewardAccountStr)
		}
	} else {
		return fmt.Errorf("dpos_reward_distribution is required in config file")
	}

	// 从YAML配置中获取奖励金额
	if rewardAmountStr := s.config.DPoSRewardAmount; rewardAmountStr != "" {
		if rewardAmount, ok := new(big.Int).SetString(rewardAmountStr, 10); ok {
			engineConfig["rewardAmount"] = rewardAmount
		} else {
			s.logger.Error("❌ 无效的奖励金额", "amount", rewardAmountStr)
			return fmt.Errorf("invalid reward amount: %s", rewardAmountStr)
		}
	} else {
		return fmt.Errorf("dpos_reward_amount is required in config file")
	}

	// 从YAML配置中获取提案表决周期（时间字符串）
	s.logger.Info("🔍 检查DPoSProposalVotePeriod配置",
		"DPoSProposalVotePeriod", s.config.DPoSProposalVotePeriod,
		"isEmpty", s.config.DPoSProposalVotePeriod == "")

	if proposalVotePeriodStr := s.config.DPoSProposalVotePeriod; proposalVotePeriodStr != "" {
		if proposalVotePeriod, err := parseDurationWithDays(proposalVotePeriodStr); err == nil {
			engineConfig["proposalVotePeriod"] = proposalVotePeriod
			s.logger.Info("✅ 成功解析提案表决周期",
				"periodStr", proposalVotePeriodStr,
				"periodDuration", proposalVotePeriod.String(),
				"seconds", proposalVotePeriod.Seconds())
		} else {
			s.logger.Error("❌ 无效的提案表决周期", "period", proposalVotePeriodStr, "error", err)
			return fmt.Errorf("invalid proposal vote period: %s", proposalVotePeriodStr)
		}
	} else {
		s.logger.Warn("⚠️ DPoSProposalVotePeriod配置为空，使用默认值24小时")
		engineConfig["proposalVotePeriod"] = 24 * time.Hour // 默认24小时
	}

	// 从YAML配置中获取提案有效期（时间字符串）
	s.logger.Info("🔍 检查DPoSProposalValidPeriod配置",
		"DPoSProposalValidPeriod", s.config.DPoSProposalValidPeriod,
		"isEmpty", s.config.DPoSProposalValidPeriod == "")

	if proposalValidPeriodStr := s.config.DPoSProposalValidPeriod; proposalValidPeriodStr != "" {
		if proposalValidPeriod, err := parseDurationWithDays(proposalValidPeriodStr); err == nil {
			engineConfig["proposalValidPeriod"] = proposalValidPeriod
			s.logger.Info("✅ 成功解析提案有效期",
				"periodStr", proposalValidPeriodStr,
				"periodDuration", proposalValidPeriod.String(),
				"seconds", proposalValidPeriod.Seconds())
		} else {
			s.logger.Error("❌ 无效的提案有效期", "period", proposalValidPeriodStr, "error", err)
			return fmt.Errorf("invalid proposal valid period: %s", proposalValidPeriodStr)
		}
	} else {
		s.logger.Warn("⚠️ DPoSProposalValidPeriod配置为空，使用默认值7天")
		engineConfig["proposalValidPeriod"] = 7 * 24 * time.Hour // 默认7天
	}

	// 从YAML配置中获取SR候选人保证金阈值
	if srThresholdStr := s.config.DPoSSRThreshold; srThresholdStr != "" {
		if srThreshold, ok := new(big.Int).SetString(srThresholdStr, 10); ok {
			engineConfig["srThreshold"] = srThreshold
			s.logger.Info("💰 设置SR候选人保证金阈值", "threshold", srThreshold.String())
		} else {
			s.logger.Error("❌ 无效的SR候选人保证金阈值", "threshold", srThresholdStr)
			return fmt.Errorf("invalid SR threshold: %s", srThresholdStr)
		}
	} else {
		// 默认不需要保证金
		engineConfig["srThreshold"] = big.NewInt(0)
		s.logger.Info("💰 使用默认SR候选人保证金阈值", "threshold", "0")
	}

	s.logger.Info("✅ DPoS经济系统配置解析完成",
		"rewardAccount", engineConfig["rewardAccount"],
		"rewardAmount", engineConfig["rewardAmount"],
		"proposalPeriod", engineConfig["proposalPeriod"])

	var (
		blockTime = common.Duration{Duration: 0}
		err       error
	)

	if engineName != string(DummyConsensus) && engineName != string(DevConsensus) {
		blockTime, err = extractBlockTime(engineConfig)
		if err != nil {
			return err
		}
	}

	// 🆕 验证 proposalPeriod 是否在 engineConfig 中
	if proposalPeriod, exists := engineConfig["proposalPeriod"]; exists {
		s.logger.Info("✅ server.go: 确认proposalPeriod在engineConfig中",
			"value", proposalPeriod,
			"type", fmt.Sprintf("%T", proposalPeriod))
	} else {
		s.logger.Warn("❌ server.go: proposalPeriod不在engineConfig中！")
		// 打印所有键
		keys := make([]string, 0, len(engineConfig))
		for k := range engineConfig {
			keys = append(keys, k)
		}
		s.logger.Info("📋 engineConfig中的所有键", "keys", keys)
	}

	config := &consensus.Config{
		Params:      s.config.Chain.Params,
		Config:      engineConfig,
		Path:        filepath.Join(s.config.DataDir, "consensus"),
		DataDir:     s.config.DataDir, // 🆕 新增：数据目录
		IsRelayer:   s.config.Relayer,
		RPCEndpoint: s.config.JSONRPC.JSONRPCAddr.String(),
	}

	// 🆕 验证 config.Config 中是否包含 proposalPeriod
	if proposalPeriod, exists := config.Config["proposalPeriod"]; exists {
		s.logger.Info("✅ server.go: 确认proposalPeriod在config.Config中",
			"value", proposalPeriod,
			"type", fmt.Sprintf("%T", proposalPeriod))
	} else {
		s.logger.Error("❌ server.go: proposalPeriod不在config.Config中！配置传递失败！")
	}

	consensus, err := engine(
		&consensus.Params{
			Context:               context.Background(),
			Config:                config,
			TxPool:                s.txpool,
			Network:               s.network,
			Blockchain:            s.blockchain,
			Executor:              s.executor,
			Grpc:                  s.grpcServer,
			Logger:                s.logger,
			SecretsManager:        s.secretsManager,
			BlockTime:             uint64(blockTime.Seconds()),
			NumBlockConfirmations: s.config.NumBlockConfirmations,
			MetricsInterval:       s.config.MetricsInterval,
		},
	)

	if err != nil {
		return err
	}

	s.consensus = consensus

	// 🆕 如果是IBFT共识，设置DPoS引擎启动器
	if engineName == string(IBFTConsensus) {
		if ibftConsensus, ok := consensus.(interface {
			SetDPoSEngineStarter(starter consensusIBFT.DPoSEngineStarter)
		}); ok {
			ibftConsensus.SetDPoSEngineStarter(s)
			s.logger.Info("✅ 已设置DPoS引擎启动器到IBFT共识")
		} else {
			s.logger.Warn("⚠️ IBFT共识不支持设置DPoS引擎启动器")
		}
	}

	return nil
}

// extractBlockTime extracts blockTime parameter from consensus engine configuration.
// If it is missing or invalid, an appropriate error is returned.
func extractBlockTime(engineConfig map[string]interface{}) (common.Duration, error) {
	blockTimeGeneric, ok := engineConfig["blockTime"]
	if !ok {
		return common.Duration{}, errBlockTimeMissing
	}

	// 添加调试日志
	fmt.Printf("🔍 extractBlockTime: blockTimeGeneric = %v (type: %T)\n", blockTimeGeneric, blockTimeGeneric)

	blockTimeRaw, err := json.Marshal(blockTimeGeneric)
	if err != nil {
		return common.Duration{}, errBlockTimeInvalid
	}

	fmt.Printf("🔍 extractBlockTime: blockTimeRaw = %s\n", string(blockTimeRaw))

	var blockTime common.Duration

	if err := json.Unmarshal(blockTimeRaw, &blockTime); err != nil {
		return common.Duration{}, errBlockTimeInvalid
	}

	fmt.Printf("🔍 extractBlockTime: parsed blockTime = %v (seconds: %f)\n", blockTime.Duration, blockTime.Seconds())

	if blockTime.Seconds() < 1 {
		return common.Duration{}, errBlockTimeInvalid
	}

	return blockTime, nil
}

type jsonRPCHub struct {
	state              state.State
	restoreProgression *progress.ProgressionWrapper

	*blockchain.Blockchain
	*txpool.TxPool
	*state.Executor
	*network.Server
	consensus.Consensus
	consensus.BridgeDataProvider
	gasprice.GasStore

	// 🆕 新增：Server引用，用于访问DPoS引擎
	server *Server
}

func (j *jsonRPCHub) GetPeers() int {
	return len(j.Server.Peers())
}

func (j *jsonRPCHub) GetAccount(root types.Hash, addr types.Address) (*jsonrpc.Account, error) {
	acct, err := getAccountImpl(j.state, root, addr)
	if err != nil {
		return nil, err
	}

	account := &jsonrpc.Account{
		Nonce:   acct.Nonce,
		Balance: new(big.Int).Set(acct.Balance),
	}

	return account, nil
}

// GetForksInTime returns the active forks at the given block height
func (j *jsonRPCHub) GetForksInTime(blockNumber uint64) chain.ForksInTime {
	return j.Executor.GetForksInTime(blockNumber)
}

func (j *jsonRPCHub) GetStorage(stateRoot types.Hash, addr types.Address, slot types.Hash) ([]byte, error) {
	account, err := getAccountImpl(j.state, stateRoot, addr)
	if err != nil {
		return nil, err
	}

	snap, err := j.state.NewSnapshotAt(stateRoot)
	if err != nil {
		return nil, err
	}

	res := snap.GetStorage(addr, account.Root, slot)

	return res.Bytes(), nil
}

func (j *jsonRPCHub) GetCode(root types.Hash, addr types.Address) ([]byte, error) {
	account, err := getAccountImpl(j.state, root, addr)
	if err != nil {
		return nil, err
	}

	code, ok := j.state.GetCode(types.BytesToHash(account.CodeHash))
	if !ok {
		return nil, fmt.Errorf("unable to fetch code")
	}

	return code, nil
}

func (j *jsonRPCHub) ApplyTxn(
	header *types.Header,
	txn *types.Transaction,
	override types.StateOverride,
	nonPayable bool,
) (result *runtime.ExecutionResult, err error) {
	blockCreator, err := j.GetConsensus().GetBlockCreator(header)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(header.StateRoot, header, blockCreator)
	if err != nil {
		return
	}

	if override != nil {
		if err = transition.WithStateOverride(override); err != nil {
			return
		}
	}

	transition.SetNonPayable(nonPayable)

	result, err = transition.Apply(txn)

	return
}

// TraceBlock traces all transactions in the given block and returns all results
func (j *jsonRPCHub) TraceBlock(
	block *types.Block,
	tracer tracer.Tracer,
) ([]interface{}, error) {
	if block.Number() == 0 {
		return nil, errors.New("genesis block can't have transaction")
	}

	parentHeader, ok := j.GetHeaderByHash(block.ParentHash())
	if !ok {
		return nil, errors.New("parent header not found")
	}

	blockCreator, err := j.GetConsensus().GetBlockCreator(block.Header)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(parentHeader.StateRoot, block.Header, blockCreator)
	if err != nil {
		return nil, err
	}

	transition.SetTracer(tracer)

	results := make([]interface{}, len(block.Transactions))

	for idx, tx := range block.Transactions {
		tracer.Clear()

		if _, err := transition.Apply(tx); err != nil {
			return nil, err
		}

		if results[idx], err = tracer.GetResult(); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// TraceTxn traces a transaction in the block, associated with the given hash
func (j *jsonRPCHub) TraceTxn(
	block *types.Block,
	targetTxHash types.Hash,
	tracer tracer.Tracer,
) (interface{}, error) {
	if block.Number() == 0 {
		return nil, errors.New("genesis block can't have transaction")
	}

	parentHeader, ok := j.GetHeaderByHash(block.ParentHash())
	if !ok {
		return nil, errors.New("parent header not found")
	}

	blockCreator, err := j.GetConsensus().GetBlockCreator(block.Header)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(parentHeader.StateRoot, block.Header, blockCreator)
	if err != nil {
		return nil, err
	}

	var targetTx *types.Transaction

	for _, tx := range block.Transactions {
		if tx.Hash == targetTxHash {
			targetTx = tx

			break
		}

		// Execute transactions without tracer until reaching the target transaction
		if _, err := transition.Apply(tx); err != nil {
			return nil, err
		}
	}

	if targetTx == nil {
		return nil, errors.New("target tx not found")
	}

	transition.SetTracer(tracer)

	if _, err := transition.Apply(targetTx); err != nil {
		return nil, err
	}

	return tracer.GetResult()
}

func (j *jsonRPCHub) TraceCall(
	tx *types.Transaction,
	parentHeader *types.Header,
	tracer tracer.Tracer,
) (interface{}, error) {
	blockCreator, err := j.GetConsensus().GetBlockCreator(parentHeader)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(parentHeader.StateRoot, parentHeader, blockCreator)
	if err != nil {
		return nil, err
	}

	transition.SetTracer(tracer)

	if _, err := transition.Apply(tx); err != nil {
		return nil, err
	}

	return tracer.GetResult()
}

func (j *jsonRPCHub) GetSyncProgression() *progress.Progression {
	// restore progression
	if restoreProg := j.restoreProgression.GetProgression(); restoreProg != nil {
		return restoreProg
	}

	// consensus sync progression
	if consensusSyncProg := j.Consensus.GetSyncProgression(); consensusSyncProg != nil {
		return consensusSyncProg
	}

	return nil
}

// AddTx adds a new transaction to the transaction pool
func (j *jsonRPCHub) AddTx(tx *types.Transaction) error {
	return j.TxPool.AddTx(tx)
}

// GetPendingTx gets the pending transaction from the transaction pool
func (j *jsonRPCHub) GetPendingTx(txHash types.Hash) (*types.Transaction, bool) {
	return j.TxPool.GetPendingTx(txHash)
}

// GetNonce returns the next nonce for this address
func (j *jsonRPCHub) GetNonce(addr types.Address) uint64 {
	// Get the latest header to get the current state root
	header := j.Header()
	if header == nil {
		return 0
	}

	// Get account from state
	account, err := j.GetAccount(header.StateRoot, addr)
	if err != nil {
		return 0
	}

	return account.Nonce
}

// GetBaseFee returns the current base fee of TxPool
func (j *jsonRPCHub) GetBaseFee() uint64 {
	return j.TxPool.GetBaseFee()
}

// GetMaxAccountEnqueued returns the maximum number of enqueued transactions per account
func (j *jsonRPCHub) GetMaxAccountEnqueued() uint64 {
	return j.TxPool.GetMaxAccountEnqueued()
}

// GetConsensus returns the consensus engine
func (j *jsonRPCHub) GetConsensus() consensus.Consensus {
	return j.Consensus
}

// GetNetwork returns the network layer
func (j *jsonRPCHub) GetNetwork() interface{} {
	return j.Server
}

// GetServer returns the server instance
func (j *jsonRPCHub) GetServer() interface{} {
	return j.Server
}

// GetTxPool returns the transaction pool
func (j *jsonRPCHub) GetTxPool() interface{} {
	return j.TxPool
}

// SETUP //

// setupJSONRCP sets up the JSONRPC server, using the set configuration
func (s *Server) setupJSONRPC() error {
	hub := &jsonRPCHub{
		state:              s.state,
		restoreProgression: s.restoreProgression,
		Blockchain:         s.blockchain,
		TxPool:             s.txpool,
		Executor:           s.executor,
		Consensus:          s.consensus,
		Server:             s.network,
		BridgeDataProvider: s.consensus.GetBridgeProvider(),
		GasStore:           s.gasHelper,
		server:             s, // 🆕 新增：Server引用
	}

	conf := &jsonrpc.Config{
		Store:                    hub,
		Addr:                     s.config.JSONRPC.JSONRPCAddr,
		ChainID:                  uint64(s.config.Chain.Params.ChainID),
		ChainName:                s.chain.Name,
		AccessControlAllowOrigin: s.config.JSONRPC.AccessControlAllowOrigin,
		PriceLimit:               s.config.PriceLimit,
		BatchLengthLimit:         s.config.JSONRPC.BatchLengthLimit,
		BlockRangeLimit:          s.config.JSONRPC.BlockRangeLimit,
		ConcurrentRequestsDebug:  s.config.JSONRPC.ConcurrentRequestsDebug,
		WebSocketReadLimit:       s.config.JSONRPC.WebSocketReadLimit,
	}

	srv, err := jsonrpc.NewJSONRPC(s.logger, conf)
	if err != nil {
		return err
	}

	s.jsonrpcServer = srv

	return nil
}

// setupGRPC sets up the grpc server and listens on tcp
func (s *Server) setupGRPC() error {
	proto.RegisterSystemServer(s.grpcServer, &systemService{server: s})

	lis, err := net.Listen("tcp", s.config.GRPCAddr.String())
	if err != nil {
		return err
	}

	// Start server with infinite retries
	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			s.logger.Error(err.Error())
		}
	}()

	return nil
}

// Chain returns the chain object of the client
func (s *Server) Chain() *chain.Chain {
	return s.chain
}

// JoinPeer attempts to add a new peer to the networking server
func (s *Server) JoinPeer(rawPeerMultiaddr string) error {
	return s.network.JoinPeer(rawPeerMultiaddr)
}

// Close closes the Minimal server (blockchain, networking, consensus)
func (s *Server) Close() {
	// Close the blockchain layer
	if err := s.blockchain.Close(); err != nil {
		s.logger.Error("failed to close blockchain", "err", err.Error())
	}

	// Close the networking layer
	if err := s.network.Close(); err != nil {
		s.logger.Error("failed to close networking", "err", err.Error())
	}

	// Close the consensus layer
	if err := s.consensus.Close(); err != nil {
		s.logger.Error("failed to close consensus", "err", err.Error())
	}

	// Close the state storage
	if err := s.stateStorage.Close(); err != nil {
		s.logger.Error("failed to close storage for trie", "err", err.Error())
	}

	if s.prometheusServer != nil {
		if err := s.prometheusServer.Shutdown(context.Background()); err != nil {
			s.logger.Error("Prometheus server shutdown error", err)
		}
	}

	// Close the txpool's main loop
	s.txpool.Close()

	// Close DataDog profiler
	s.closeDataDogProfiler()
}

// Entry is a consensus configuration entry
type Entry struct {
	Enabled bool
	Config  map[string]interface{}
}

func (s *Server) startPrometheusServer(listenAddr *net.TCPAddr) *http.Server {
	srv := &http.Server{
		Addr: listenAddr.String(),
		Handler: promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer, promhttp.HandlerFor(
				prometheus.DefaultGatherer,
				promhttp.HandlerOpts{},
			),
		),
		ReadHeaderTimeout: 60 * time.Second,
	}

	s.logger.Info("Prometheus server started", "addr=", listenAddr.String())

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				s.logger.Error("Prometheus HTTP server ListenAndServe", "err", err)
			}
		}
	}()

	return srv
}

// createDPoSEngine 创建DPoS引擎
func (s *Server) createDPoSEngine() (consensus.Consensus, error) {
	// 获取DPoS引擎工厂
	engine, ok := consensusBackends[DPoSConsensus]
	if !ok {
		return nil, fmt.Errorf("DPoS consensus engine not found")
	}

	// 创建DPoS引擎配置
	engineConfig := map[string]interface{}{
		"consensusSwitchHeight": float64(s.config.ConsensusSwitchHeight),
		"dposValidatorsCount":   float64(s.config.DPoSValidatorsCount),
		"backupValidatorsCount": float64(s.config.BackupValidatorsCount),
		"maxMissedBlocks":       float64(s.config.MaxMissedBlocks),
	}

	config := &consensus.Config{
		Params:      s.config.Chain.Params,
		Config:      engineConfig,
		Path:        filepath.Join(s.config.DataDir, "dpos"),
		DataDir:     s.config.DataDir,
		IsRelayer:   s.config.Relayer,
		RPCEndpoint: s.config.JSONRPC.JSONRPCAddr.String(),
	}

	// 创建DPoS引擎实例
	dposEngine, err := engine(&consensus.Params{
		Context:               context.Background(),
		Config:                config,
		TxPool:                s.txpool,
		Network:               s.network,
		Blockchain:            s.blockchain,
		Executor:              s.executor,
		Grpc:                  s.grpcServer,
		Logger:                s.logger,
		SecretsManager:        s.secretsManager,
		BlockTime:             2, // DPoS区块时间
		NumBlockConfirmations: s.config.NumBlockConfirmations,
		MetricsInterval:       s.config.MetricsInterval,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create DPoS engine: %w", err)
	}

	return dposEngine, nil
}

// GetDPoSEngine returns the DPoS engine instance
func (s *Server) GetDPoSEngine() interface{} {
	return s.dposEngine
}

// GetDPoSEngine returns the DPoS engine instance from jsonRPCHub
func (j *jsonRPCHub) GetDPoSEngine() interface{} {
	if j.server != nil {
		return j.server.GetDPoSEngine()
	}
	return nil
}

func initForkManager(engineName string, config *chain.Chain) error {
	var initialParams *forkmanager.ForkParams

	if factory := forkManagerInitialParamsFactory[ConsensusType(engineName)]; factory != nil {
		params, err := factory(config)
		if err != nil {
			return err
		}

		initialParams = params
	}

	fm := forkmanager.GetInstance()

	// clear everything in forkmanager (if there was something because of tests) and register initial fork
	fm.Clear()
	fm.RegisterFork(forkmanager.InitialFork, initialParams)

	// Register forks
	for name, f := range *config.Params.Forks {
		// check if fork is not supported by current edge version
		if _, found := (*chain.AllForksEnabled)[name]; !found {
			return fmt.Errorf("fork is not available: %s", name)
		}

		fm.RegisterFork(name, f.Params)
	}

	// Register handlers and additional forks here
	if err := types.RegisterTxHashFork(chain.TxHashWithType); err != nil {
		return err
	}

	// Register Handler for London fork fix
	if err := state.RegisterLondonFixFork(chain.LondonFix); err != nil {
		return err
	}

	if factory := forkManagerFactory[ConsensusType(engineName)]; factory != nil {
		if err := factory(config.Params.Forks); err != nil {
			return err
		}
	}

	// Activate initial fork
	if err := fm.ActivateFork(forkmanager.InitialFork, uint64(0)); err != nil {
		return err
	}

	// Activate forks
	for name, f := range *config.Params.Forks {
		if err := fm.ActivateFork(name, f.Block); err != nil {
			return err
		}
	}

	return nil
}
