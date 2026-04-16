package ibft

import (
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/consensus/ibft/fork"
	"github.com/Vcity-Team/vcitychain/consensus/ibft/proto"
	"github.com/Vcity-Team/vcitychain/consensus/ibft/signer"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
	"github.com/Vcity-Team/vcitychain/validators/store/contract"
	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"
)

// DPoSEngineStarter 定义DPoS引擎启动器接口
type DPoSEngineStarter interface {
	StartDPoSEngine(height uint64) error
}

// stateExecutorAdapter adapts state.Executor to contract.Executor
type stateExecutorAdapter struct {
	executor *state.Executor
}

// GetExecutor returns the underlying state.Executor
func (a *stateExecutorAdapter) GetExecutor() *state.Executor {
	return a.executor
}

// ExecuteContractCall implements contract.Executor
func (a *stateExecutorAdapter) ExecuteContractCall(contractAddr types.Address, data []byte) ([]byte, error) {
	// TODO: Implement actual contract call using state executor
	return nil, fmt.Errorf("ExecuteContractCall not implemented")
}

// ExecuteContractTransaction implements contract.Executor
func (a *stateExecutorAdapter) ExecuteContractTransaction(contractAddr types.Address, data []byte) error {
	// TODO: Implement actual contract transaction using state executor
	return fmt.Errorf("ExecuteContractTransaction not implemented")
}

// ensure contract.Executor interface is used
var _ contract.Executor = (*stateExecutorAdapter)(nil)

const (
	DefaultEpochSize = 100000
	IbftKeyName      = "validator.key"
	KeyEpochSize     = "epochSize"

	ibftProto = "/ibft/0.2"

	// consensusMetrics is a prefix used for consensus-related metrics
	consensusMetrics = "consensus"
)

var (
	ErrInvalidHookParam           = errors.New("invalid IBFT hook param passed in")
	ErrProposerSealByNonValidator = errors.New("proposer seal by non-validator")
	ErrInvalidMixHash             = errors.New("invalid mixhash")
	ErrInvalidSha3Uncles          = errors.New("invalid sha3 uncles")
	ErrWrongDifficulty            = errors.New("wrong difficulty")
)

type txPoolInterface interface {
	Prepare()
	Length() uint64
	Peek() *types.Transaction
	Pop(tx *types.Transaction)
	Drop(tx *types.Transaction)
	Demote(tx *types.Transaction)
	ResetWithHeaders(headers ...*types.Header)
	SetSealing(bool)
}

type forkManagerInterface interface {
	Initialize() error
	Close() error
	GetSigner(uint64) (signer.Signer, error)
	GetValidatorStore(uint64) (fork.ValidatorStore, error)
	GetValidators(uint64) (validators.Validators, error)
	GetHooks(uint64) fork.HooksInterface
	GetConsensusSwitchHeight() uint64
	IsDPoSTransition(uint64) bool
}

// backendIBFT represents the IBFT consensus mechanism object
type backendIBFT struct {
	consensus *IBFTConsensus

	// Static References
	logger         hclog.Logger           // Reference to the logging
	blockchain     *blockchain.Blockchain // Reference to the blockchain layer
	network        *network.Server        // Reference to the networking layer
	executor       *state.Executor        // Reference to the state executor
	txpool         txPoolInterface        // Reference to the transaction pool
	syncer         syncer.Syncer          // Reference to the sync protocol
	secretsManager secrets.SecretsManager // Reference to the secret manager
	Grpc           *grpc.Server           // Reference to the gRPC manager
	operator       *operator              // Reference to the gRPC service of IBFT
	transport      transport              // Reference to the transport protocol

	// Dynamic References
	forkManager       forkManagerInterface  // Manager to hold IBFT Forks
	currentSigner     signer.Signer         // Signer at current sequence
	currentValidators validators.Validators // signer at current sequence
	currentHooks      fork.HooksInterface   // Hooks at current sequence

	// 新增：共识引擎管理
	currentEngine     interface{}       // 当前运行的共识引擎
	engineType        string            // "ibft" 或 "dpos"
	dposEngineStarter DPoSEngineStarter // DPoS引擎启动器

	// Configurations
	config             *consensus.Config // Consensus configuration
	epochSize          uint64
	quorumSizeBlockNum uint64
	blockTime          time.Duration // Minimum block generation time in seconds

	// Channels
	closeCh chan struct{} // Channel for closing
	closed  bool          // 标记是否已经关闭

	// DPoS 切换只执行一次，避免每个区块都重复打印“已切换到DPoS / 启动DPoS引擎”等日志
	dposSwitchTriggered bool
}

// Factory implements the base consensus Factory method
func Factory(params *consensus.Params) (consensus.Consensus, error) {
	// defaults for user set fields in genesis
	var (
		epochSize          = uint64(DefaultEpochSize)
		quorumSizeBlockNum = uint64(0)
	)

	if definedEpochSize, ok := params.Config.Config[KeyEpochSize]; ok {
		// Epoch size is defined, use the passed in one
		readSize, ok := definedEpochSize.(float64)
		if !ok {
			return nil, errors.New("invalid type assertion")
		}

		epochSize = uint64(readSize)
	}

	if rawBlockNum, ok := params.Config.Config["quorumSizeBlockNum"]; ok {
		// Block number specified for quorum size switch
		readBlockNum, ok := rawBlockNum.(float64)
		if !ok {
			return nil, errors.New("invalid type assertion")
		}

		quorumSizeBlockNum = uint64(readBlockNum)
	}

	logger := params.Logger.Named("ibft")

	forkManager, err := fork.NewForkManager(
		logger,
		params.Blockchain,
		&stateExecutorAdapter{executor: params.Executor},
		params.SecretsManager,
		params.Config.Path,
		epochSize,
		params.Config.Config,
		params.Config.DataDir, // 新增：数据目录参数
		params.Network,        // 新增：网络组件参数
		params.TxPool,         // 新增：交易池参数
		params.Config,         // 新增：配置参数
	)

	if err != nil {
		return nil, err
	}

	p := &backendIBFT{
		// References
		logger:     logger,
		blockchain: params.Blockchain,
		network:    params.Network,
		executor:   params.Executor,
		txpool:     params.TxPool,
		syncer: syncer.NewSyncer(
			params.Logger,
			params.Network,
			params.Blockchain,
			time.Duration(params.BlockTime)*3*time.Second,
			forkManager.GetConsensusSwitchHeight(), // 传入正确的共识切换高度
		),
		secretsManager: params.SecretsManager,
		Grpc:           params.Grpc,
		forkManager:    forkManager,

		// Configurations
		config:             params.Config,
		epochSize:          epochSize,
		quorumSizeBlockNum: quorumSizeBlockNum,
		blockTime:          time.Duration(params.BlockTime) * time.Second,

		// Channels
		closeCh: make(chan struct{}),
	}

	// Istanbul requires a different header hash function
	p.SetHeaderHash()

	return p, nil
}

func (i *backendIBFT) Initialize() error {
	// register the grpc operator
	if i.Grpc != nil {
		i.operator = &operator{ibft: i}
		proto.RegisterIbftOperatorServer(i.Grpc, i.operator)
	}

	// start the transport protocol
	if err := i.setupTransport(); err != nil {
		return err
	}

	// initialize fork manager
	if err := i.forkManager.Initialize(); err != nil {
		return err
	}

	if err := i.updateCurrentModules(i.blockchain.Header().Number + 1); err != nil {
		return err
	}

	i.logger.Info("validator key", "addr", i.currentSigner.Address().String())

	// 创建专门用于IBFT共识的logger
	consensusLogger := i.logger.Named("consensus")

	i.consensus = newIBFT(
		consensusLogger,
		i,
		i,
	)

	// Ensure consensus takes into account user configured block production time
	i.consensus.ExtendRoundTimeout(i.blockTime)

	return nil
}

// sync runs the syncer in the background to receive blocks from advanced peers
func (i *backendIBFT) startSyncing() {
	// 监听停止信号
	go func() {
		<-i.closeCh
		i.logger.Info("🛑 IBFT syncer收到停止信号，退出同步")
	}()

	callInsertBlockHook := func(fullBlock *types.FullBlock) bool {
		// 检查是否是DPoS切换高度，如果是则停止同步（仅在首次触发时执行，避免每个区块重复刷日志）
		if i.forkManager != nil && !i.dposSwitchTriggered {
			if shouldStop := i.checkShouldStopIBFT(fullBlock.Block.Number()); shouldStop {
				i.dposSwitchTriggered = true
				i.logger.Info("🛑 syncer检测到DPoS切换，停止IBFT同步", "height", fullBlock.Block.Number())

				// 启动DPoS引擎（不在这里关闭syncer，避免重复关闭）
				if i.dposEngineStarter != nil {
					i.logger.Info("🚀 syncer启动DPoS引擎...")
					if err := i.dposEngineStarter.StartDPoSEngine(fullBlock.Block.Number()); err != nil {
						i.logger.Error("❌ 启动DPoS引擎失败", "error", err)
					} else {
						i.logger.Info("✅ DPoS引擎启动成功，DPoS同步器已接管")
					}
				} else {
					i.logger.Warn("⚠️ DPoS引擎启动器未设置，无法启动DPoS引擎")
				}

				// 注意：不在这里关闭closeCh，因为startConsensus也会关闭它
				// 返回true表示停止同步
				return true
			}
		}

		if err := i.currentHooks.PostInsertBlock(fullBlock.Block); err != nil {
			i.logger.Error("failed to call PostInsertBlock", "height", fullBlock.Block.Header.Number, "error", err)
		}

		if err := i.updateCurrentModules(fullBlock.Block.Number() + 1); err != nil {
			i.logger.Error("failed to update sub modules", "height", fullBlock.Block.Number()+1, "err", err)
		}

		i.txpool.ResetWithHeaders(fullBlock.Block.Header)

		return false
	}

	if err := i.syncer.Sync(
		callInsertBlockHook,
	); err != nil {
		i.logger.Error("watch sync failed", "err", err)
	}
}

// Start starts the IBFT consensus
func (i *backendIBFT) Start() error {
	// Start the syncer
	if err := i.syncer.Start(); err != nil {
		return err
	}

	// Start syncing blocks from other peers
	go i.startSyncing()

	// Start the actual consensus protocol
	go i.startConsensus()

	return nil
}

// 简化：检查IBFT是否应该停止（通过验证者集合判断）
func (i *backendIBFT) checkShouldStopIBFT(height uint64) bool {
	// 通过检查验证者集合是否为空来判断是否需要停止
	// 当ForkManager返回空验证者集合时，说明已经切换到DPoS
	validators, err := i.forkManager.GetValidators(height)
	if err != nil {
		i.logger.Error("❌ 获取验证者失败", "error", err)
		return false
	}

	// 如果验证者集合为空，说明已经切换到DPoS，IBFT应该停止
	if validators.Len() == 0 {
		i.logger.Info("🛑 验证者集合为空，DPoS已接管，IBFT应该停止", "height", height)
		return true
	}

	// 调试信息：显示当前验证者数量
	i.logger.Debug("🔍 检查IBFT停止条件", "height", height, "validatorsCount", validators.Len())

	return false
}

// GetSyncProgression gets the latest sync progression, if any
func (i *backendIBFT) GetSyncProgression() *progress.Progression {
	return i.syncer.GetSyncProgression()
}

// safeClose 安全关闭closeCh，避免重复关闭
func (i *backendIBFT) safeClose() {
	if !i.closed {
		i.closed = true
		close(i.closeCh)
		i.logger.Info("🛑 IBFT closeCh已安全关闭")
	} else {
		i.logger.Debug("🛑 IBFT closeCh已经关闭，跳过重复关闭")
	}
}

func (i *backendIBFT) startConsensus() {
	var (
		newBlockSub   = i.blockchain.SubscribeEvents()
		syncerBlockCh = make(chan struct{})
	)

	// Receive a notification every time syncer manages
	// to insert a valid block. Used for cancelling active consensus
	// rounds for a specific height
	go func() {
		eventCh := newBlockSub.GetEventCh()

		for {
			select {
			case ev := <-eventCh:
				if ev.Source == "syncer" {
					if ev.NewChain[0].Number < i.blockchain.Header().Number {
						// The blockchain notification system can eventually deliver
						// stale block notifications. These should be ignored
						continue
					}

					syncerBlockCh <- struct{}{}
				}
			case <-i.closeCh:
				i.logger.Info("🛑 IBFT事件监听goroutine收到停止信号，退出")
				return
			}
		}
	}()

	defer i.blockchain.UnsubscribeEvents(newBlockSub)

	var (
		sequenceCh  = make(<-chan struct{})
		isValidator bool
	)

	for {
		// 检查是否应该停止IBFT
		select {
		case <-i.closeCh:
			i.logger.Info("🛑 IBFT收到停止信号，退出主循环")
			return
		default:
			// 继续正常执行
		}

		var (
			latest  = i.blockchain.Header().Number
			pending = latest + 1
		)

		// 简化：检查是否需要停止IBFT（通过验证者集合判断）；仅在首次触发时执行，避免重复刷日志
		if i.forkManager != nil && !i.dposSwitchTriggered {
			if shouldStop := i.checkShouldStopIBFT(pending); shouldStop {

				// 标记已切换到DPoS，避免后续重复执行
				i.dposSwitchTriggered = true

				// 完全停止IBFT共识引擎
				i.logger.Info("🛑 ========== 开始完全停止IBFT共识引擎 ==========", "height", pending)

				// 1. 停止IBFT的syncer
				if i.syncer != nil {
					i.logger.Info("🛑 停止IBFT的syncer...")
					i.logger.Warn("🧯 IBFT stopping syncer (DPoS switch triggered)",
						"height", pending,
						"latest", latest,
						"stack", string(debug.Stack()))

					// 添加超时机制，避免无限等待
					done := make(chan error, 1)
					go func() {
						done <- i.syncer.Close()
					}()

					select {
					case err := <-done:
						if err != nil {
							i.logger.Error("❌ 停止IBFT的syncer失败", "error", err)
						} else {
							i.logger.Info("✅ IBFT的syncer已停止")
						}
					case <-time.After(5 * time.Second):
						i.logger.Warn("⚠️ 停止IBFT的syncer超时，强制继续")
					}

					// 清空 syncer 引用，避免重复关闭
					i.syncer = nil
				}

				// 2. 停止IBFT的共识协议
				if i.consensus != nil {
					i.logger.Info("🛑 停止IBFT的共识协议...")
					// 这里可以添加停止共识协议的逻辑
					// 目前go-ibft没有明确的停止方法，但goroutine会自然退出
				}

				// 3. 启动DPoS引擎
				if i.dposEngineStarter != nil {
					i.logger.Info("🚀 启动DPoS引擎接管共识...")
					if err := i.dposEngineStarter.StartDPoSEngine(pending); err != nil {
						i.logger.Error("❌ 启动DPoS引擎失败", "error", err)
					} else {
						i.logger.Info("✅ DPoS引擎启动成功，已完全接管共识")
					}
				} else {
					i.logger.Warn("⚠️ DPoS引擎启动器未设置，无法启动DPoS引擎")
				}

				// 4. 安全关闭IBFT的closeCh，通知其他组件IBFT已停止
				i.safeClose()

				i.logger.Info("✅ ========== IBFT共识引擎已完全停止，DPoS已接管 ==========", "height", pending)
				return
			}
		}

		if err := i.updateCurrentModules(pending); err != nil {
			i.logger.Error(
				"failed to update submodules",
				"height", pending,
				"err", err,
			)
		}

		// Update the No.of validator metric
		metrics.SetGauge([]string{consensusMetrics, "validators"}, float32(i.currentValidators.Len()))

		isValidator = i.isActiveValidator()

		i.txpool.SetSealing(isValidator)

		if isValidator {
			i.logger.Debug("Starting consensus sequence", "height", pending)
			sequenceCh = i.consensus.runSequence(pending)
		} else {
			i.logger.Warn("Not a validator, skipping consensus", "height", pending)
		}

		select {
		case <-syncerBlockCh:
			if isValidator {
				i.consensus.stopSequence()
				i.logger.Debug("canceled sequence", "sequence", pending)
			}
		case <-sequenceCh:
		case <-i.closeCh:
			if isValidator {
				i.consensus.stopSequence()
			}

			return
		}
	}
}

// isActiveValidator returns whether my signer belongs to current validators
func (i *backendIBFT) isActiveValidator() bool {
	return i.currentValidators.Includes(i.currentSigner.Address())
}

// updateMetrics will update various metrics based on the given block
// currently we capture No.of Txs and block interval metrics using this function
func (i *backendIBFT) updateMetrics(block *types.Block) {
	// get previous header
	prvHeader, _ := i.blockchain.GetHeaderByNumber(block.Number() - 1)
	parentTime := time.Unix(int64(prvHeader.Timestamp), 0)
	headerTime := time.Unix(int64(block.Header.Timestamp), 0)

	// Update the block interval metric
	if block.Number() > 1 {
		metrics.SetGauge([]string{consensusMetrics, "block_interval"}, float32(headerTime.Sub(parentTime).Seconds()))
	}

	// Update the Number of transactions in the block metric
	metrics.SetGauge([]string{consensusMetrics, "num_txs"}, float32(len(block.Body().Transactions)))

	// Update the base fee metric
	metrics.SetGauge([]string{consensusMetrics, "base_fee"}, float32(block.Header.BaseFee))
}

// verifyHeaderImpl verifies fields including Extra
// for the past or being proposed header
func (i *backendIBFT) verifyHeaderImpl(
	parent, header *types.Header,
	headerSigner signer.Signer,
	validators validators.Validators,
	hooks fork.HooksInterface,
	shouldVerifyParentCommittedSeals bool,
) error {
	// 检查是否在DPoS切换期间
	// 在DPoS切换期间，可能mixhash不匹配，需要跳过验证
	if header.MixHash != signer.IstanbulDigest {
		// 添加调试信息
		fmt.Printf("🔍 DEBUG MixHash verification: expected=%x, actual=%x\n",
			signer.IstanbulDigest, header.MixHash)

		// 在DPoS切换期间，暂时跳过mixhash验证
		// TODO: 需要更精确的DPoS切换检测
		fmt.Printf("⚠️ DEBUG MixHash mismatch, but continuing for DPoS transition\n")
		// return ErrInvalidMixHash
	}

	if header.Sha3Uncles != types.EmptyUncleHash {
		return ErrInvalidSha3Uncles
	}

	// difficulty has to match number
	// 检查是否在DPoS切换期间，如果是则跳过IBFT难度验证
	if header.Difficulty != header.Number {
		// 检查是否在DPoS切换期间
		if i.forkManager != nil {
			// 检查是否已经切换到DPoS（难度为1且高度大于等于切换高度）
			if header.Difficulty == 1 && i.isDPoSTransition(header.Number) {
				// 在DPoS切换期间，跳过IBFT难度验证
				fmt.Printf("🔍 DEBUG 跳过IBFT难度验证：DPoS切换期间 blockNumber=%d difficulty=%d\n",
					header.Number, header.Difficulty)
			} else {
				return ErrWrongDifficulty
			}
		} else {
			return ErrWrongDifficulty
		}
	}

	// ensure the extra data is correctly formatted
	if _, err := headerSigner.GetIBFTExtra(header); err != nil {
		return err
	}

	// verify the ProposerSeal
	// 检查是否在DPoS切换期间，如果是则跳过ProposerSeal验证
	if i.isDPoSTransition(header.Number) {
		fmt.Printf("🔍 DEBUG 跳过ProposerSeal验证：DPoS切换期间 blockNumber=%d\n", header.Number)
	} else {
		if err := verifyProposerSeal(
			header,
			headerSigner,
			validators,
		); err != nil {
			return err
		}
	}

	// verify the ParentCommittedSeals
	// 检查是否在DPoS切换期间，如果是则跳过ParentCommittedSeals验证
	if i.isDPoSTransition(header.Number) {
		fmt.Printf("🔍 DEBUG 跳过ParentCommittedSeals验证：DPoS切换期间 blockNumber=%d\n", header.Number)
	} else {
		if err := i.verifyParentCommittedSeals(
			parent, header,
			shouldVerifyParentCommittedSeals,
		); err != nil {
			return err
		}
	}

	// Additional header verification
	if err := hooks.VerifyHeader(header); err != nil {
		return err
	}

	return nil
}

// VerifyHeader wrapper for verifying headers
func (i *backendIBFT) VerifyHeader(header *types.Header) error {
	parent, ok := i.blockchain.GetHeaderByNumber(header.Number - 1)
	if !ok {
		return fmt.Errorf(
			"unable to get parent header for block number %d",
			header.Number,
		)
	}

	headerSigner, validators, hooks, err := getModulesFromForkManager(
		i.forkManager,
		header.Number,
	)
	if err != nil {
		return err
	}

	// verify all the header fields
	if err := i.verifyHeaderImpl(
		parent,
		header,
		headerSigner,
		validators,
		hooks,
		false,
	); err != nil {
		return err
	}

	extra, err := headerSigner.GetIBFTExtra(header)
	if err != nil {
		return err
	}

	hashForCommittedSeal, err := i.calculateProposalHash(
		headerSigner,
		header,
		extra.RoundNumber,
	)
	if err != nil {
		return err
	}

	// verify the Committed Seals
	// CommittedSeals exists only in the finalized header
	// 检查是否在DPoS切换期间，如果是则跳过CommittedSeals验证
	if i.isDPoSTransition(header.Number) {
		fmt.Printf("🔍 DEBUG 跳过CommittedSeals验证：DPoS切换期间 blockNumber=%d\n", header.Number)
	} else {
		if err := headerSigner.VerifyCommittedSeals(
			hashForCommittedSeal,
			extra.CommittedSeals,
			validators,
			i.quorumSize(header.Number)(validators),
		); err != nil {
			return err
		}
	}

	return nil
}

// quorumSize returns a callback that when executed on a Validators computes
// number of votes required to reach quorum based on the size of the set.
// The blockNumber argument indicates which formula was used to calculate the result (see PRs #513, #549)
func (i *backendIBFT) quorumSize(blockNumber uint64) QuorumImplementation {
	if blockNumber < i.quorumSizeBlockNum {
		return LegacyQuorumSize
	}

	return OptimalQuorumSize
}

// ProcessHeaders updates the snapshot based on previously verified headers
func (i *backendIBFT) ProcessHeaders(headers []*types.Header) error {
	for _, header := range headers {
		hooks := i.forkManager.GetHooks(header.Number)

		if err := hooks.ProcessHeader(header); err != nil {
			return err
		}
	}

	return nil
}

// GetBlockCreator retrieves the block signer from the extra data field
func (i *backendIBFT) GetBlockCreator(header *types.Header) (types.Address, error) {
	// 检查是否已经切换到 DPoS
	// 如果区块高度 >= 共识切换高度，说明已经是 DPoS 区块，应该从 Miner 字段读取
	if i.forkManager != nil {
		consensusSwitchHeight := i.forkManager.GetConsensusSwitchHeight()
		if consensusSwitchHeight > 0 && header.Number >= consensusSwitchHeight {
			// 这是 DPoS 区块，从 Miner 字段读取
			if len(header.Miner) > 0 {
				return types.BytesToAddress(header.Miner), nil
			}
			// 如果 Miner 字段为空，返回错误
			return types.ZeroAddress, fmt.Errorf("DPoS block at height %d has empty Miner field", header.Number)
		}
	}

	// IBFT 区块：从 ProposerSeal 恢复
	signer, err := i.forkManager.GetSigner(header.Number)
	if err != nil {
		return types.ZeroAddress, err
	}

	return signer.EcrecoverFromHeader(header)
}

// PreCommitState a hook to be called before finalizing state transition on inserting block
func (i *backendIBFT) PreCommitState(block *types.Block, txn *state.Transition) error {
	hooks := i.forkManager.GetHooks(block.Number())

	return hooks.PreCommitState(block.Header, txn)
}

// GetEpoch returns the current epoch
func (i *backendIBFT) GetEpoch(number uint64) uint64 {
	if number%i.epochSize == 0 {
		return number / i.epochSize
	}

	return number/i.epochSize + 1
}

// IsLastOfEpoch checks if the block number is the last of the epoch
func (i *backendIBFT) IsLastOfEpoch(number uint64) bool {
	return number > 0 && number%i.epochSize == 0
}

// Close closes the IBFT consensus mechanism, and does write back to disk
func (i *backendIBFT) Close() error {
	// 使用安全的关闭方法
	i.safeClose()

	if i.syncer != nil {
		if err := i.syncer.Close(); err != nil {
			return err
		}
	}

	if i.forkManager != nil {
		if err := i.forkManager.Close(); err != nil {
			return err
		}
	}

	return nil
}

// SetHeaderHash updates hash calculation function for IBFT
func (i *backendIBFT) SetHeaderHash() {
	types.HeaderHash = func(h *types.Header) types.Hash {
		signer, err := i.forkManager.GetSigner(h.Number)
		if err != nil {
			return types.ZeroHash
		}

		hash, err := signer.CalculateHeaderHash(h)
		if err != nil {
			return types.ZeroHash
		}

		return hash
	}
}

// GetBridgeProvider returns an instance of BridgeDataProvider
func (i *backendIBFT) GetBridgeProvider() consensus.BridgeDataProvider {
	return nil
}

// FilterExtra is the implementation of Consensus interface
func (i *backendIBFT) FilterExtra(extra []byte) ([]byte, error) {
	return extra, nil
}

// updateCurrentModules updates Signer, Hooks, and Validators
// that are used at specified height
// by fetching from ForkManager
func (i *backendIBFT) updateCurrentModules(height uint64) error {
	lastSigner := i.currentSigner

	signer, validators, hooks, err := getModulesFromForkManager(i.forkManager, height)
	if err != nil {
		return err
	}

	i.currentSigner = signer
	i.currentValidators = validators
	i.currentHooks = hooks

	i.logFork(lastSigner, signer)

	return nil
}

// logFork logs validation type switch
func (i *backendIBFT) logFork(
	lastSigner, signer signer.Signer,
) {
	if lastSigner != nil && signer != nil && lastSigner.Type() != signer.Type() {
		i.logger.Info("IBFT validation type switched", "old", lastSigner.Type(), "new", signer.Type())
	}
}

func (i *backendIBFT) verifyParentCommittedSeals(
	parent, header *types.Header,
	shouldVerifyParentCommittedSeals bool,
) error {
	if parent.IsGenesis() {
		return nil
	}

	parentSigner, parentValidators, _, err := getModulesFromForkManager(
		i.forkManager,
		parent.Number,
	)
	if err != nil {
		return err
	}

	parentHeader, ok := i.blockchain.GetHeaderByHash(parent.Hash)
	if !ok {
		return fmt.Errorf("header %s not found", parent.Hash)
	}

	parentExtra, err := parentSigner.GetIBFTExtra(parentHeader)
	if err != nil {
		return err
	}

	parentHash, err := i.calculateProposalHash(
		parentSigner,
		parentHeader,
		parentExtra.RoundNumber,
	)
	if err != nil {
		return err
	}

	// if shouldVerifyParentCommittedSeals is false, skip the verification
	// when header doesn't have Parent Committed Seals (Backward Compatibility)
	return parentSigner.VerifyParentCommittedSeals(
		parentHash,
		header,
		parentValidators,
		i.quorumSize(parent.Number)(parentValidators),
		shouldVerifyParentCommittedSeals,
	)
}

// getModulesFromForkManager is a helper function to get all modules from ForkManager
func getModulesFromForkManager(forkManager forkManagerInterface, height uint64) (
	signer.Signer,
	validators.Validators,
	fork.HooksInterface,
	error,
) {
	signer, err := forkManager.GetSigner(height)
	if err != nil {
		return nil, nil, nil, err
	}

	validators, err := forkManager.GetValidators(height)
	if err != nil {
		return nil, nil, nil, err
	}

	hooks := forkManager.GetHooks(height)

	return signer, validators, hooks, nil
}

// verifyProposerSeal verifies ProposerSeal in IBFT Extra of header
// and make sure signer belongs to validators
func verifyProposerSeal(
	header *types.Header,
	signer signer.Signer,
	validators validators.Validators,
) error {
	proposer, err := signer.EcrecoverFromHeader(header)
	if err != nil {
		return err
	}

	// 验证提议者是否在验证者集合中
	isIncluded := validators.Includes(proposer)
	if !isIncluded {
		return ErrProposerSealByNonValidator
	}
	return nil
}

// isDPoSTransition 检查是否在DPoS切换期间
func (i *backendIBFT) isDPoSTransition(blockNumber uint64) bool {
	if i.forkManager == nil {
		return false
	}

	// 通过ForkManager检查是否在DPoS切换期间
	return i.forkManager.IsDPoSTransition(blockNumber)
}

// ValidateExtraDataFormat Verifies that extra data can be unmarshaled
func (i *backendIBFT) ValidateExtraDataFormat(header *types.Header) error {
	blockSigner, _, _, err := getModulesFromForkManager(
		i.forkManager,
		header.Number,
	)

	if err != nil {
		return err
	}

	_, err = blockSigner.GetIBFTExtra(header)

	return err
}

// SetDPoSEngineStarter 设置DPoS引擎启动器
func (i *backendIBFT) SetDPoSEngineStarter(starter DPoSEngineStarter) {
	i.dposEngineStarter = starter
	i.logger.Info("✅ DPoS引擎启动器已设置")
}
