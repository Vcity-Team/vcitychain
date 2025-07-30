package dpos

import (
	"context"
	"encoding/json"

	"fmt"
	"math/big"

	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/crypto"

	"github.com/Vcity-Team/vcitychain/consensus"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
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
}

// dpos_runtime.go
type dposRuntime struct {
	config  *runtimeConfig
	backend dposBackend
	logger  hclog.Logger

	// 运行时状态
	currentRound         uint64
	currentDelegateIndex uint64
	delegates            validator.AccountSet
	voters               map[types.Address]*VoterInfo
	pendingVotes         []*VoteMessage

	// 定时器
	blockTimer *time.Ticker
	voteTimer  *time.Ticker

	// 控制通道
	closeCh chan struct{}

	// 锁
	lock sync.RWMutex
}

func (r *dposRuntime) start() error {
	r.logger.Info("starting DPoS runtime")

	// 初始化运行时状态
	if err := r.initializeRuntime(); err != nil {
		return fmt.Errorf("failed to initialize runtime: %w", err)
	}

	// 启动轮询出块定时器
	if err := r.startBlockProduction(); err != nil {
		return fmt.Errorf("failed to start block production: %w", err)
	}

	// 启动投票统计定时器
	if err := r.startVoteCollection(); err != nil {
		return fmt.Errorf("failed to start vote collection: %w", err)
	}

	r.logger.Info("DPoS runtime started successfully")
	return nil
}

func (r *dposRuntime) close() {
	r.logger.Info("closing DPoS runtime")

	// 停止所有定时器
	if r.blockTimer != nil {
		r.blockTimer.Stop()
	}
	if r.voteTimer != nil {
		r.voteTimer.Stop()
	}

	// 清理运行时状态
	r.cleanupRuntime()

	r.logger.Info("DPoS runtime closed")
}

// initializeRuntime 初始化运行时状态
func (r *dposRuntime) initializeRuntime() error {
	// 初始化当前轮次
	r.currentRound = 1
	r.currentDelegateIndex = 0

	// 初始化受托人集合
	if err := r.initializeDelegates(); err != nil {
		return fmt.Errorf("failed to initialize delegates: %w", err)
	}

	// 初始化投票者映射
	r.voters = make(map[types.Address]*VoterInfo)

	return nil
}

// startBlockProduction 启动区块生产
func (r *dposRuntime) startBlockProduction() error {
	blockTime := 2 * time.Second // 默认2秒
	if r.config.PolyBFTConfig != nil {
		blockTime = r.config.PolyBFTConfig.BlockTime.Duration
	}

	r.blockTimer = time.NewTicker(blockTime)
	go func() {
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
	}()

	return nil
}

// startVoteCollection 启动投票收集
func (r *dposRuntime) startVoteCollection() error {
	voteTime := 5 * time.Second // 默认5秒
	if r.config.PolyBFTConfig != nil {
		voteTime = r.config.PolyBFTConfig.BlockTime.Duration * 4 // 投票时间设为区块时间的4倍
	}

	r.voteTimer = time.NewTicker(voteTime)
	go func() {
		for {
			select {
			case <-r.voteTimer.C:
				if err := r.collectVotes(); err != nil {
					r.logger.Error("failed to collect votes", "error", err)
				}
			case <-r.closeCh:
				return
			}
		}
	}()

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

// produceBlock 生产区块
func (r *dposRuntime) produceBlock() error {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 检查当前节点是否为出块者
	currentDelegate := r.getCurrentDelegate()
	keyAddr := types.Address(r.config.Key.Address())
	if currentDelegate != keyAddr {
		return nil // 不是当前出块者
	}

	// 构建新区块
	block, err := r.buildBlock()
	if err != nil {
		return fmt.Errorf("failed to build block: %w", err)
	}

	// 提交区块到区块链
	if err := r.config.blockchain.CommitBlock(block); err != nil {
		return fmt.Errorf("failed to commit block: %w", err)
	}

	r.logger.Info("produced block", "number", block.Block.Number(), "hash", block.Block.Hash())

	// 更新轮次
	r.updateRound()

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
func (r *dposRuntime) updateRound() {
	r.currentDelegateIndex++
	if r.currentDelegateIndex >= uint64(len(r.delegates)) {
		r.currentDelegateIndex = 0
		r.currentRound++
	}
}

// initializeDelegates 初始化受托人集合
func (r *dposRuntime) initializeDelegates() error {
	// 从配置中获取初始受托人
	if r.config.PolyBFTConfig != nil && len(r.config.PolyBFTConfig.InitialValidatorSet) > 0 {
		r.delegates = make(validator.AccountSet, 0, len(r.config.PolyBFTConfig.InitialValidatorSet))
		for _, val := range r.config.PolyBFTConfig.InitialValidatorSet {
			// 解析BLS密钥
			blsKeyBytes := []byte(val.BlsKey)
			blsKey, err := bls.UnmarshalPublicKey(blsKeyBytes)
			if err != nil {
				r.logger.Warn("failed to parse BLS key", "error", err, "address", val.Address)
				continue
			}

			r.delegates = append(r.delegates, &validator.ValidatorMetadata{
				Address:     val.Address,
				BlsKey:      blsKey,
				VotingPower: val.Stake,
			})
		}
	} else {
		// 如果没有配置，使用默认受托人
		r.delegates = validator.AccountSet{}
	}

	return nil
}

// getCurrentDelegate 获取当前受托人
func (r *dposRuntime) getCurrentDelegate() types.Address {
	if len(r.delegates) == 0 {
		return types.ZeroAddress
	}

	if r.currentDelegateIndex >= uint64(len(r.delegates)) {
		r.currentDelegateIndex = 0
	}

	return r.delegates[r.currentDelegateIndex].Address
}

// buildBlock 构建区块
func (r *dposRuntime) buildBlock() (*types.FullBlock, error) {
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
	builder.Fill()

	// 构建区块
	block, err := builder.Build(func(h *types.Header) {
		// 设置DPoS相关的区块头信息
		h.Miner = keyAddr[:]
		h.Difficulty = 1
	})

	if err != nil {
		return nil, fmt.Errorf("failed to build block: %w", err)
	}

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

	// 更新受托人的投票权重
	r.updateDelegateVotingPower(vote.Delegate, vote.Amount)

	return nil
}

// verifyVoteSignature 验证投票签名
func (r *dposRuntime) verifyVoteSignature(vote *VoteMessage) error {
	// 构建投票消息哈希
	message := fmt.Sprintf("%s:%s:%s:%d",
		vote.Voter.String(),
		vote.Delegate.String(),
		vote.Amount.String(),
		vote.Round)

	// 计算消息哈希
	messageBytes := []byte(message)
	hash := crypto.Keccak256(messageBytes)

	// 验证签名
	// 这里需要根据实际的签名验证逻辑来实现
	// 暂时返回nil，表示验证通过
	_ = hash // 避免未使用变量警告

	return nil
}

// updateDelegateVotingPower 更新受托人投票权重
func (r *dposRuntime) updateDelegateVotingPower(delegate types.Address, amount *big.Int) {
	for _, d := range r.delegates {
		if d.Address == delegate {
			d.VotingPower = new(big.Int).Add(d.VotingPower, amount)
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
}

// VoterInfo 投票者信息
type VoterInfo struct {
	Address        types.Address
	VotingPower    *big.Int
	VotedDelegates []types.Address
	LastVoteTime   uint64
	LockedUntil    uint64
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
}

func (d *DPoS) VerifyHeader(header *types.Header) error {
	// Short circuit if the header is known
	if _, ok := d.blockchain.GetHeaderByHash(header.Hash); ok {
		return nil
	}

	parent, ok := d.blockchain.GetHeaderByHash(header.ParentHash)
	if !ok {
		return fmt.Errorf(
			"unable to get parent header by hash for block number %d",
			header.Number,
		)
	}

	return d.verifyHeaderImpl(parent, header, d.config.BlockTime.Duration, nil)
}

func (d *DPoS) verifyHeaderImpl(parent, header *types.Header, blockTimeDrift time.Duration, parents []*types.Header) error {
	// validate header fields
	if err := validateHeaderFields(parent, header, uint64(blockTimeDrift.Seconds())); err != nil {
		return fmt.Errorf("failed to validate header for block %d. error = %w", header.Number, err)
	}

	// decode the extra data
	extra, err := GetIbftExtra(header.ExtraData)
	if err != nil {
		return fmt.Errorf("failed to verify header for block %d. get extra error = %w", header.Number, err)
	}

	// validate extra data
	return extra.ValidateFinalizedData(
		header, parent, parents, d.blockchain.GetChainID(), d, signer.DomainCheckpointManager, d.logger)
}

func (d *DPoS) ProcessHeaders(headers []*types.Header) error {
	// For DPoS, we don't need to process headers in the same way as PoW
	// This is mainly used for syncing and can be a no-op for DPoS
	d.logger.Debug("processing headers", "count", len(headers))
	return nil
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

	// start syncer (also initializes peer map)
	if err := d.syncer.Start(); err != nil {
		return fmt.Errorf("failed to start syncer. Error: %w", err)
	}

	// sync concurrently, retrying indefinitely
	go common.RetryForever(context.Background(), time.Second, func(context.Context) error {
		blockHandler := func(b *types.FullBlock) bool {
			// 实现DPoS的区块处理逻辑
			d.logger.Debug("processing block", "number", b.Block.Number())

			// 处理区块中的投票事件
			if err := d.processBlockVotes(b); err != nil {
				d.logger.Error("failed to process block votes", "error", err, "block", b.Block.Number())
			}

			// 更新受托人集合
			if err := d.updateDelegates(b); err != nil {
				d.logger.Error("failed to update delegates", "error", err, "block", b.Block.Number())
			}

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
		// 启动DPoS运行时
		if err := d.runtime.start(); err != nil {
			return fmt.Errorf("failed to start DPoS runtime: %w", err)
		}
	}

	// start state DB process if available
	if d.state != nil {
		go d.state.startStatsReleasing()
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

	return nil
}

// DPoS 实现 dposBackend 接口
var _ dposBackend = (*DPoS)(nil)

// Factory 创建DPoS共识实例
func Factory(params *consensus.Params) (consensus.Consensus, error) {

	logger := params.Logger.Named("dpos")

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

	return vcity_dpos, nil
}

// Initialize 初始化DPoS
func (d *DPoS) Initialize() error {
	d.logger.Info("initializing dpos...")

	// read account
	account, err := wallet.NewAccountFromSecret(d.config.SecretsManager)
	if err != nil {
		return fmt.Errorf("failed to read account data. Error: %w", err)
	}

	// set key
	d.key = wallet.NewKey(account)

	// create and set syncer
	d.syncer = syncer.NewSyncer(
		d.config.Logger.Named("syncer"),
		d.config.Network,
		d.config.Blockchain,
		d.config.BlockTime.Duration*3*time.Second,
	)

	// set blockchain backend
	d.blockchain = &blockchainWrapper{
		blockchain: d.config.Blockchain,
		executor:   d.config.Executor,
	}

	// set block time
	d.blockTime = d.config.BlockTime.Duration

	// initialize delegates
	if err := d.initializeDelegates(); err != nil {
		return fmt.Errorf("failed to initialize delegates: %w", err)
	}

	return nil
}

// initializeDelegates 初始化受托人集合
func (d *DPoS) initializeDelegates() error {
	d.delegates = make(validator.AccountSet, 0, d.config.DelegateCount)

	// 从配置中加载初始受托人
	for _, delegate := range d.config.InitialDelegates {
		// 使用 ToValidatorMetadata 方法正确转换
		validatorMetadata, err := delegate.ToValidatorMetadata()
		if err != nil {
			return fmt.Errorf("failed to convert delegate %s to validator metadata: %w", delegate.Address, err)
		}

		d.delegates = append(d.delegates, validatorMetadata)
	}

	// 按投票权重排序
	//d.sortDelegatesByVotingPower()

	return nil
}

// dpos.go - 添加后端接口实现
func (d *DPoS) GetDelegates(blockNumber uint64, parents []*types.Header) (validator.AccountSet, error) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	// 如果是当前区块，直接返回当前受托人集合
	if blockNumber == d.blockchain.CurrentHeader().Number {
		return d.delegates.Copy(), nil
	}

	// 否则从状态存储中获取历史受托人集合
	return d.getDelegatesFromState(blockNumber)
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

	// 从状态存储中获取质押信息
	if d.state != nil {
		dbTx, err := d.state.beginDBTransaction(false)
		if err != nil {
			return nil, fmt.Errorf("failed to begin db transaction: %w", err)
		}
		defer dbTx.Rollback()

		return d.GetStakingInfoWithTx(blockNumber, staker, dbTx)
	}

	// 如果状态存储不可用，从内存中获取
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
	// 从状态存储中获取质押信息
	if d.state != nil {
		// 尝试从数据库获取质押信息
		stakeInfo, err := d.state.StakeStore.getStakingInfo(staker, dbTx)
		if err == nil && stakeInfo != nil {
			return stakeInfo, nil
		}
	}

	// 如果数据库中没有，从内存中获取
	d.lock.RLock()
	defer d.lock.RUnlock()

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

// 区块处理相关方法
func (d *DPoS) processBlockVotes(block *types.FullBlock) error {
	// 处理区块中的投票事件
	// TODO: 实现投票事件处理逻辑
	d.logger.Debug("processing block votes", "block", block.Block.Number())
	return nil
}

func (d *DPoS) updateDelegates(block *types.FullBlock) error {
	// 更新受托人集合
	// TODO: 实现受托人集合更新逻辑
	d.logger.Debug("updating delegates", "block", block.Block.Number())
	return nil
}

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
	// 从状态存储中获取历史受托人集合
	if d.state != nil {
		dbTx, err := d.state.beginDBTransaction(false)
		if err != nil {
			return nil, fmt.Errorf("failed to begin db transaction: %w", err)
		}
		defer dbTx.Rollback()

		return d.getDelegatesFromStateWithTx(blockNumber, dbTx)
	}

	// 如果状态存储不可用，返回当前受托人集合
	d.lock.RLock()
	defer d.lock.RUnlock()
	return d.delegates.Copy(), nil
}

func (d *DPoS) getDelegatesFromStateWithTx(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error) {
	// 在事务中获取受托人集合
	if d.state != nil {
		// 尝试从数据库获取历史受托人集合
		delegates, err := d.state.ValidatorStore.getDelegatesAtBlock(blockNumber, dbTx)
		if err == nil && len(delegates) > 0 {
			return delegates, nil
		}
	}

	// 如果数据库中没有，返回当前受托人集合
	d.lock.RLock()
	defer d.lock.RUnlock()
	return d.delegates.Copy(), nil
}

func (d *DPoS) getVotingPowerFromStateWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// 在事务中获取投票权重
	if d.state != nil {
		// 尝试从数据库获取历史投票权重
		votingPower, err := d.state.ValidatorStore.getVotingPowerAtBlock(blockNumber, delegate, dbTx)
		if err == nil && votingPower != nil {
			return votingPower, nil
		}
	}

	// 如果数据库中没有，返回当前投票权重
	return d.GetVotingPower(blockNumber, delegate)
}
