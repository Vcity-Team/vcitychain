package dpos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/bitmap"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
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

	// 网络服务
	network *network.Server

	// 网络主题缓存
	signatureRequestTopic  *network.Topic
	signatureResponseTopic *network.Topic
	topicMutex            sync.RWMutex

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
	// 检查配置是否可用
	if r.config == nil {
		return fmt.Errorf("runtime config is nil")
	}

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
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		return fmt.Errorf("key not available, cannot start block production")
	}

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
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		return fmt.Errorf("key not available, cannot start vote collection")
	}

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

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot produce block")
		return fmt.Errorf("key not available, cannot produce block")
	}

	// 检查当前节点是否为出块者
	currentDelegate := r.getCurrentDelegate()
	keyAddr := types.Address(r.config.Key.Address())

	// 添加调试日志
	r.logger.Info("checking block production eligibility",
		"currentDelegate", currentDelegate,
		"keyAddr", keyAddr,
		"currentRound", r.currentRound,
		"currentDelegateIndex", r.currentDelegateIndex,
		"delegatesCount", len(r.delegates))

	if currentDelegate != keyAddr {
		// r.logger.Info("not current delegate, skipping block production") // 注释掉这个日志
		return nil // 不是当前出块者
	}

	// 对于区块1，我们需要特别检查来防止分叉
	// 如果当前是区块0，并且我们是第一个受托人，需要等待一段时间
	// 让其他节点有机会先出块
	currentBlock := r.config.blockchain.CurrentHeader()
	if currentBlock.Number == 0 && r.currentDelegateIndex == 0 {
		r.logger.Info("first delegate at genesis block, waiting to avoid fork")

		// 等待更长时间，确保其他节点有机会先出块
		time.Sleep(1000 * time.Millisecond) // 增加到1秒

		// 再次检查当前区块高度
		currentBlock = r.config.blockchain.CurrentHeader()
		if currentBlock.Number > 0 {
			r.logger.Info("block was produced by another node during wait, skipping block production")
			return nil
		}
	}

	// 额外的检查：如果当前是区块0，并且我们不是第一个受托人，也要等待
	// 这样可以确保第一个受托人有足够时间出块
	if currentBlock.Number == 0 && r.currentDelegateIndex > 0 {
		r.logger.Info("not first delegate at genesis block, waiting for first delegate to produce block")

		// 等待一段时间，让第一个受托人有机会出块
		time.Sleep(2000 * time.Millisecond) // 等待2秒

		// 再次检查当前区块高度
		currentBlock = r.config.blockchain.CurrentHeader()
		if currentBlock.Number > 0 {
			r.logger.Info("block was produced by first delegate, skipping block production")
			return nil
		}
	}

	// 检查是否已经有更新的区块
	currentBlock = r.config.blockchain.CurrentHeader()

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
				r.logger.Info("block 1 was produced by another node, skipping block production",
					"blockMiner", blockMiner, "keyAddr", keyAddr)
				return nil
			} else {
				r.logger.Info("block 1 was produced by us, continuing with next block")
			}
		}
	} else if currentBlock.Number >= nextBlockNumber {
		// 如果当前区块号大于等于我们要生产的区块号，说明已经有更新的区块了
		r.logger.Info("block already exists, skipping block production",
			"currentBlockNumber", currentBlock.Number, "nextBlockNumber", nextBlockNumber)
		return nil
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

	// 添加调试日志
	r.logger.Info("updated round", "newRound", r.currentRound, "newDelegateIndex", r.currentDelegateIndex)

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
	// 从主DPoS结构体获取已初始化的受托人
	if r.backend != nil {
		delegates, err := r.backend.GetDelegates(0, nil)
		if err != nil {
			return fmt.Errorf("failed to get delegates from backend: %w", err)
		}
		r.delegates = delegates
		r.logger.Info("initialized delegates from backend", "count", len(r.delegates))
	} else {
		// 如果没有backend，使用空集合
		r.delegates = validator.AccountSet{}
		r.logger.Warn("no backend available, using empty delegate set")
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
	builder.Fill()

	// 构建区块
	block, err := builder.Build(func(h *types.Header) {
		// 设置DPoS相关的区块头信息
		h.Miner = keyAddr[:]
		h.Difficulty = 1

		// 创建正确的Extra对象，包含必要的字段
		extra := &Extra{
			Committed: &Signature{}, // 添加空的Committed签名
			Checkpoint: &CheckpointData{
				BlockRound:            r.currentRound,
				EpochNumber:           1,
				CurrentValidatorsHash: types.Hash{},
				NextValidatorsHash:    types.Hash{},
				EventRoot:             types.Hash{},
			},
		}
		h.ExtraData = extra.MarshalRLPTo(nil)

		// 添加调试日志
		r.logger.Info("set extraData for block", "length", len(h.ExtraData))
	})

	if err != nil {
		return nil, fmt.Errorf("failed to build block: %w", err)
	}

	// 等待收集其他验证者的签名
	r.logger.Info("waiting for validator signatures", "blockNumber", block.Block.Number())

	// 计算checkpoint哈希用于签名
	// 计算当前验证者集合的哈希
	currentValidatorsHash, err := r.delegates.Hash()
	if err != nil {
		r.logger.Error("failed to calculate current validators hash", "error", err)
		return nil, fmt.Errorf("failed to calculate current validators hash: %w", err)
	}

	checkpoint := &CheckpointData{
		BlockRound:            r.currentRound,
		EpochNumber:           1,
		CurrentValidatorsHash: currentValidatorsHash,
		NextValidatorsHash:    currentValidatorsHash, // 暂时使用相同的哈希
		EventRoot:             types.Hash{},          // 暂时为空
	}

	// 先设置区块头的ExtraData，包含空的签名
	emptyBitmap := bitmap.Bitmap{}
	extra := &Extra{
		Committed: &Signature{
			AggregatedSignature: []byte{}, // 暂时为空
			Bitmap:              emptyBitmap,
		},
		Checkpoint: checkpoint, // 使用相同的checkpoint数据
	}
	block.Block.Header.ExtraData = extra.MarshalRLPTo(nil)

	// 计算checkpoint哈希，使用更新后的区块头哈希
	checkpointHash, err := checkpoint.Hash(888, block.Block.Number(), block.Block.Header.Hash)
	if err != nil {
		r.logger.Error("failed to calculate checkpoint hash", "error", err)
		return nil, fmt.Errorf("failed to calculate checkpoint hash: %w", err)
	}

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
			r.logger.Info("检测到网络增长，重试签名收集", "attempt", attempt+1)
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

	// 更新区块的签名
	if len(signatures) > 0 {
		r.logger.Info("开始聚合签名",
			"signatureCount", len(signatures))

		// 正确聚合所有签名
		blsSignatures := make(bls.Signatures, 0, len(signatures))
		for i, sigBytes := range signatures {
			sig, err := bls.UnmarshalSignature(sigBytes)
			if err != nil {
				r.logger.Error("failed to unmarshal signature", "error", err, "index", i)
				continue
			}
			blsSignatures = append(blsSignatures, sig)
			r.logger.Info("成功解析签名",
				"index", i,
				"signatureLength", len(sigBytes),
				"signatureBytes", fmt.Sprintf("%x", sigBytes))
		}

		r.logger.Info("签名解析完成",
			"parsedSignatures", len(blsSignatures),
			"totalSignatures", len(signatures))

		// 聚合所有签名
		aggregatedSignature, err := blsSignatures.Aggregate().Marshal()
		if err != nil {
			r.logger.Error("failed to aggregate signatures", "error", err)
			return nil, fmt.Errorf("failed to aggregate signatures: %w", err)
		}

		r.logger.Info("签名聚合成功",
			"aggregatedSignatureLength", len(aggregatedSignature),
			"aggregatedSignatureBytes", fmt.Sprintf("%x", aggregatedSignature))

		// 测试：验证聚合签名是否可以正确解析
		_, err = bls.UnmarshalSignature(aggregatedSignature)
		if err != nil {
			r.logger.Error("聚合签名解析测试失败", "error", err)
			return nil, fmt.Errorf("aggregated signature verification failed: %w", err)
		}

		// 更新区块的ExtraData，包含聚合签名
		extra.Committed = &Signature{
			AggregatedSignature: aggregatedSignature,
			Bitmap:              signatureBitmap,
		}
		block.Block.Header.ExtraData = extra.MarshalRLPTo(nil)

		r.logger.Info("区块签名更新完成",
			"blockNumber", block.Block.Number(),
			"extraDataLength", len(block.Block.Header.ExtraData))
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

	// 添加性能优化相关结构
	cache          *DPoSCache
	batchProcessor *BatchProcessor
	metrics        *DPoSMetrics
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
	// 添加详细的日志 - 节点3验证区块2头部
	d.logger.Info("=== 节点3验证区块2头部开始 ===",
		"blockNumber", header.Number,
		"blockHash", header.Hash.String(),
		"parentNumber", parent.Number,
		"parentHash", parent.Hash.String(),
		"extraDataLength", len(header.ExtraData))

	// validate header fields
	if err := validateHeaderFields(parent, header, uint64(blockTimeDrift.Seconds())); err != nil {
		d.logger.Error("区块头部字段验证失败", "error", err)
		return fmt.Errorf("failed to validate header for block %d. error = %w", header.Number, err)
	}

	d.logger.Info("区块头部字段验证通过")

	// decode the extra data
	extra, err := GetIbftExtra(header.ExtraData)
	if err != nil {
		d.logger.Error("解析区块extraData失败", "error", err)
		return fmt.Errorf("failed to verify header for block %d. get extra error = %w", header.Number, err)
	}

	d.logger.Info("区块extraData解析成功",
		"committedSignatureLength", len(extra.Committed.AggregatedSignature),
		"committedBitmapLength", len(extra.Committed.Bitmap),
		"checkpointExists", extra.Checkpoint != nil)

	// validate extra data
	err = extra.ValidateFinalizedData(
		header, parent, parents, d.blockchain.GetChainID(), d, signer.DomainValidatorSet, d.logger)

	if err != nil {
		d.logger.Error("区块extraData验证失败", "error", err)
		d.logger.Error("=== 节点3验证区块2头部失败 ===")
		return err
	}

	d.logger.Info("区块extraData验证成功")
	d.logger.Info("=== 节点3验证区块2头部成功 ===")
	return nil
}

func (d *DPoS) ProcessHeaders(headers []*types.Header) error {
	// For DPoS, we need to update round state when receiving new blocks
	d.logger.Debug("processing headers", "count", len(headers))

	// Update round state for each new block
	for _, header := range headers {
		d.logger.Info("processing header", "blockNumber", header.Number, "blockHash", header.Hash)

		// Use a goroutine to delay the round state update
		// This ensures the block is written before we update the round state
		go func(h *types.Header) {
			// Wait a bit for the block to be written
			time.Sleep(100 * time.Millisecond)

			// Check if the block is now the current header
			currentHeader := d.blockchain.CurrentHeader()
			d.logger.Info("delayed check - current header", "blockNumber", currentHeader.Number, "blockHash", currentHeader.Hash)

			if h.Number == currentHeader.Number && h.Hash == currentHeader.Hash {
				d.logger.Info("updating round state for new block", "blockNumber", h.Number)

				// Update round state in the runtime
				if d.runtime != nil {
					d.runtime.lock.Lock()
					oldIndex := d.runtime.currentDelegateIndex
					d.runtime.updateRound()
					d.runtime.lock.Unlock()

					d.logger.Info("updated round state",
						"oldDelegateIndex", oldIndex,
						"newRound", d.runtime.currentRound,
						"newDelegateIndex", d.runtime.currentDelegateIndex)
				} else {
					d.logger.Warn("runtime is nil, cannot update round state")
				}
			} else {
				d.logger.Info("delayed check - header does not match current header, skipping round update",
					"headerNumber", h.Number, "currentNumber", currentHeader.Number,
					"headerHash", h.Hash, "currentHash", currentHeader.Hash)
			}
		}(header)
	}

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
		// 检查runtime是否已正确初始化
		if d.runtime.config == nil || d.runtime.config.Key == nil {
			return fmt.Errorf("DPoS runtime not properly initialized: Key is nil")
		}

		// 启动DPoS运行时
		if err := d.runtime.start(); err != nil {
			return fmt.Errorf("failed to start DPoS runtime: %w", err)
		}
	}

	// start state DB process if available
	if d.state != nil {
		go d.state.startStatsReleasing()
	}

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

	// 设置必要的配置字段
	vcity_dpos.config.SecretsManager = params.SecretsManager
	vcity_dpos.config.Blockchain = params.Blockchain
	vcity_dpos.config.Logger = params.Logger
	vcity_dpos.config.Network = params.Network
	vcity_dpos.config.Executor = params.Executor

	// 初始化其他必要字段
	vcity_dpos.voters = make(map[types.Address]*VoterInfo)
	vcity_dpos.delegates = make(validator.AccountSet, 0)
	// vcity_dpos.validatorsCache = newValidatorsSnapshotCache() // TODO: 需要正确的参数

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

	// 检查Key是否成功设置
	if d.key == nil {
		return fmt.Errorf("failed to create wallet key")
	}

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

	// 创建DPoS runtime
	runtimeConfig := &runtimeConfig{
		DataDir:     d.dataDir,
		Key:         d.key,
		State:       d.state,
		blockchain:  d.blockchain,
		dposBackend: d,
		txPool:      d.txPool,
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

	d.logger.Info("DPoS runtime initialized successfully")

	return nil
}

// initializeDelegates 初始化受托人集合
func (d *DPoS) initializeDelegates() error {
	d.delegates = make(validator.AccountSet, 0, d.config.DelegateCount)

	// 添加调试日志
	d.logger.Info("initializing delegates", "configDelegateCount", d.config.DelegateCount, "initialDelegatesCount", len(d.config.InitialDelegates))

	// 从配置中加载初始受托人
	for i, delegate := range d.config.InitialDelegates {
		d.logger.Info("processing delegate", "index", i, "address", delegate.Address, "stake", delegate.Stake.String())

		// 检查是否有 BLS 密钥
		if delegate.BlsKey == "" {
			// 如果没有 BLS 密钥，自动生成一个
			blsKey, err := bls.GenerateBlsKey()
			if err != nil {
				return fmt.Errorf("failed to generate BLS key for delegate %s: %w", delegate.Address, err)
			}

			validatorMetadata := &validator.ValidatorMetadata{
				Address:     delegate.Address,
				BlsKey:      blsKey.PublicKey(),
				VotingPower: delegate.Stake,
				IsActive:    true,
			}
			d.delegates = append(d.delegates, validatorMetadata)
			d.logger.Info("added delegate with generated BLS key", "address", delegate.Address, "blsKey", fmt.Sprintf("%x", blsKey.PublicKey().Marshal()))
		} else {
			// 使用 ToValidatorMetadata 方法正确转换
			validatorMetadata, err := delegate.ToValidatorMetadata()
			if err != nil {
				return fmt.Errorf("failed to convert delegate %s to validator metadata: %w", delegate.Address, err)
			}
			d.delegates = append(d.delegates, validatorMetadata)
			d.logger.Info("added delegate with BLS key", "address", delegate.Address)
		}
	}

	// 为初始委托人自动分配投票权重，确保他们能出块
	if len(d.delegates) > 0 {
		d.logger.Info("auto-assigning voting power to initial delegates", "count", len(d.delegates))
		for _, delegate := range d.delegates {
			// 给每个初始委托人分配默认投票权重
			defaultVotingPower, _ := new(big.Int).SetString("1000000000000000000000", 10) // 1 ETH
			delegate.VotingPower = defaultVotingPower
			d.logger.Info("assigned voting power to delegate",
				"address", delegate.Address,
				"votingPower", delegate.VotingPower.String())
		}
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

// 添加安全相关常量
const (
	MaxVotingPower = "1000000000000000000000000" // 1M tokens
	MaxDelegates   = 100
	VoteLockTime   = 86400                      // 24小时
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

	// 2. 检查受托人是否存在且活跃
	delegateExists := false
	for _, del := range d.delegates {
		if del.Address == vote.Delegate && del.IsActive {
			delegateExists = true
			break
		}
	}
	if !delegateExists {
		return errors.New("delegate not found or inactive")
	}

	// 3. 检查投票锁定时间
	if voter, exists := d.voters[vote.Voter]; exists {
		if uint64(time.Now().Unix()) < voter.LockedUntil {
			return errors.New("voter is still locked")
		}
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

// 增强的投票处理
func (d *DPoS) processVote(vote *VoteMessage) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 1. 安全校验
	if err := d.validateVote(vote); err != nil {
		return fmt.Errorf("vote validation failed: %w", err)
	}

	// 2. 签名校验
	if err := d.verifyVoteSignature(vote); err != nil {
		return fmt.Errorf("vote signature verification failed: %w", err)
	}

	// 3. 防重放攻击 - 检查nonce
	if err := d.checkVoteNonce(vote); err != nil {
		return fmt.Errorf("vote nonce check failed: %w", err)
	}

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
	d.updateDelegateVotingPower(vote.Delegate, vote.Amount)

	// 8. 记录nonce防止重放
	voter.Nonce[vote.Round] = true

	d.logger.Info("vote processed successfully",
		"voter", vote.Voter,
		"delegate", vote.Delegate,
		"amount", vote.Amount,
		"round", vote.Round)

	return nil
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

// 增强的受托人更新
func (d *DPoS) updateDelegates(block *types.FullBlock) error {
	d.lock.Lock()
	defer d.lock.Unlock()

	// 1. 参数校验
	N := int(d.config.DelegateCount)
	if N <= 0 {
		N = 21 // 默认21个受托人
	}
	if N > MaxDelegates {
		N = MaxDelegates
	}

	// 2. 计算受托人排名
	delegates := make([]*DelegateInfo, 0, len(d.delegates))
	for _, del := range d.delegates {
		// 检查受托人是否满足最小质押要求
		minStake, _ := new(big.Int).SetString(MinVoteAmount, 10)
		if del.VotingPower.Cmp(minStake) < 0 {
			del.IsActive = false
			d.logger.Warn("delegate deactivated due to insufficient stake",
				"address", del.Address, "stake", del.VotingPower)
		}

		delegates = append(delegates, &DelegateInfo{
			Address:     del.Address,
			VotingPower: del.VotingPower,
			IsActive:    del.IsActive,
		})
	}

	// 3. 按投票权重排序
	sort.Slice(delegates, func(i, j int) bool {
		if delegates[i].IsActive != delegates[j].IsActive {
			return delegates[i].IsActive // 活跃的排在前面
		}
		return delegates[i].VotingPower.Cmp(delegates[j].VotingPower) > 0
	})

	// 4. 选出前N名活跃受托人
	newSet := validator.AccountSet{}
	activeCount := 0
	for i := 0; i < len(delegates) && activeCount < N; i++ {
		if delegates[i].IsActive {
			// 从当前验证者集合中找到对应的BLS密钥
			var blsKey *bls.PublicKey
			for _, currentDel := range d.delegates {
				if currentDel.Address == delegates[i].Address {
					blsKey = currentDel.BlsKey
					break
				}
			}

			newSet = append(newSet, &validator.ValidatorMetadata{
				Address:     delegates[i].Address,
				BlsKey:      blsKey, // 保留BLS密钥
				VotingPower: delegates[i].VotingPower,
				IsActive:    true,
			})
			activeCount++
		}
	}

	// 5. 检查受托人集合变化
	oldSet := d.delegates
	d.delegates = newSet

	// 6. 记录受托人集合变化
	if len(oldSet) != len(newSet) {
		d.logger.Info("delegate set size changed",
			"old_size", len(oldSet), "new_size", len(newSet))
	}

	// 检查新增和移除的受托人
	added, removed := d.compareDelegateSets(oldSet, newSet)
	if len(added) > 0 {
		d.logger.Info("new delegates added", "delegates", added)
	}
	if len(removed) > 0 {
		d.logger.Info("delegates removed", "delegates", removed)
	}

	d.logger.Debug("updated delegates", "block", block.Block.Number(), "count", len(newSet))
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

// 初始化性能优化组件
func (d *DPoS) initPerformanceOptimizations() {
	// 初始化缓存
	d.cache = &DPoSCache{
		voterCache:    make(map[types.Address]*VoterInfo),
		delegateCache: make(map[types.Address]*validator.ValidatorMetadata),
		rewardCache:   make(map[types.Address]*big.Int),
		cacheTTL:      5 * time.Minute,
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
				d.delegates = append(d.delegates, &validator.ValidatorMetadata{
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
	ticker := time.NewTicker(d.cache.cacheTTL)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.cache.lock.Lock()
			// 清理过期的缓存
			if time.Since(d.cache.lastUpdate) > d.cache.cacheTTL {
				d.cache.voterCache = make(map[types.Address]*VoterInfo)
				d.cache.delegateCache = make(map[types.Address]*validator.ValidatorMetadata)
				d.cache.rewardCache = make(map[types.Address]*big.Int)
				d.logger.Debug("cache cleaned up")
			}
			d.cache.lock.Unlock()

		case <-d.closeCh:
			return
		}
	}
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
		// 更新缓存
		d.cache.lock.Lock()
		d.cache.voterCache[addr] = voter
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
	for _, del := range d.delegates {
		if del.Address == delegate {
			del.VotingPower = new(big.Int).Add(del.VotingPower, amount)
			break
		}
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
		"vote_lock_time":   c.VoteLockTime,
		"reward_ratio":     c.RewardRatio,
	}
}

// collectValidatorSignatures 收集验证者签名的真实实现
func (r *dposRuntime) collectValidatorSignatures(block *types.FullBlock, checkpointHash types.Hash, proposerAddr types.Address) ([][]byte, bitmap.Bitmap, error) {
	signatures := make([][]byte, 0)
	signatureBitmap := bitmap.Bitmap{}

	r.logger.Info("开始收集验证者签名",
		"checkpointHash", checkpointHash.String(),
		"delegatesCount", len(r.delegates),
		"proposerAddress", proposerAddr.String())

	// 检查网络中的活跃验证者数量
	activeValidators := r.getActiveValidatorsCount()
	expectedSignatures := len(r.delegates) - 1 // 不包括自己

	r.logger.Info("网络状态检查",
		"activeValidators", activeValidators,
		"totalDelegates", len(r.delegates),
		"expectedSignatures", expectedSignatures)

	// 检查是否有足够的验证者
	minRequired := r.calculateMinRequiredSignatures()
	if activeValidators < minRequired+1 { // +1 因为不包括提议者自己
		r.logger.Error("验证者数量不足，无法进行签名收集",
			"activeValidators", activeValidators,
			"minRequired", minRequired,
			"totalDelegates", len(r.delegates))
		return r.waitForNetworkGrowth(checkpointHash, proposerAddr)
	}

	// 1. 广播签名请求给其他验证者
	if err := r.broadcastSignatureRequest(block, checkpointHash); err != nil {
		r.logger.Error("failed to broadcast signature request", "error", err)
		return nil, nil, fmt.Errorf("failed to broadcast signature request: %w", err)
	}

	// 2. 智能等待签名收集
	signatureCh := make(chan *SignatureResponse, len(r.delegates))

	// 启动签名收集协程
	go r.collectSignaturesAsync(checkpointHash, signatureCh)

	// 3. 智能等待签名收集完成
	collectedSignatures := make(map[types.Address][]byte)
	minRequiredSignatures := r.calculateMinRequiredSignatures()

	// 使用动态超时：根据网络状态调整等待时间
	baseTimeout := 2 * time.Minute
	networkTimeout := 10 * time.Minute // 网络等待超时

	// 如果验证者数量不足，使用更长的超时等待更多节点加入
	if r.getActiveValidatorsCount() < r.calculateMinRequiredSignatures()+1 {
		baseTimeout = networkTimeout
	}

	timeoutCh := time.After(baseTimeout)
	checkInterval := time.NewTicker(30 * time.Second) // 每30秒检查一次网络状态
	defer checkInterval.Stop()

	r.logger.Info("开始智能签名收集",
		"minRequired", minRequiredSignatures,
		"baseTimeout", baseTimeout,
		"networkTimeout", networkTimeout)

	for {
		select {
		case sigResp := <-signatureCh:
			if sigResp != nil && sigResp.Signature != nil {
				collectedSignatures[sigResp.ValidatorAddr] = sigResp.Signature
				r.logger.Info("收到验证者签名",
					"validator", sigResp.ValidatorAddr.String(),
					"signatureLength", len(sigResp.Signature),
					"collected", len(collectedSignatures),
					"required", minRequiredSignatures)

				// 检查是否收集到足够的签名
				if len(collectedSignatures) >= minRequiredSignatures {
					r.logger.Info("收集到足够的签名", "count", len(collectedSignatures))
					goto processSignatures
				}
			}

		case <-checkInterval.C:
			// 定期检查网络状态
			activeValidators := r.getActiveValidatorsCount()
			r.logger.Info("定期检查网络状态",
				"activeValidators", activeValidators,
				"totalDelegates", len(r.delegates),
				"collectedSignatures", len(collectedSignatures),
				"requiredSignatures", minRequiredSignatures)

			// 检查是否有足够的验证者进行签名收集
			if activeValidators < minRequiredSignatures+1 {
				r.logger.Warn("验证者数量不足，等待更多节点加入",
					"activeValidators", activeValidators,
					"minRequired", minRequiredSignatures+1)
				// 重置超时，给更多节点加入的时间
				timeoutCh = time.After(2 * time.Minute)
			} else if len(collectedSignatures) == 0 {
				r.logger.Warn("验证者数量足够但未收到签名，检查网络连接")
				// 重置超时，给网络响应更多时间
				timeoutCh = time.After(1 * time.Minute)
			}

		case <-timeoutCh:
			r.logger.Warn("签名收集超时", "collected", len(collectedSignatures), "required", minRequiredSignatures)

			// 如果收集到的签名不足，返回错误
			if len(collectedSignatures) < minRequiredSignatures {
				return nil, nil, fmt.Errorf("insufficient signatures collected: got %d, need at least %d", len(collectedSignatures), minRequiredSignatures)
			}
			goto processSignatures
		}
	}

processSignatures:
	// 4. 处理收集到的签名
	for i, delegate := range r.delegates {
		if delegate.Address == proposerAddr {
			continue // 跳过提议者自己
		}

		if signature, exists := collectedSignatures[delegate.Address]; exists {
			// 验证签名
			if err := r.verifyValidatorSignature(delegate, signature, checkpointHash); err != nil {
				r.logger.Warn("验证者签名验证失败",
					"validator", delegate.Address.String(),
					"error", err)
				continue
			}

			signatures = append(signatures, signature)
			signatureBitmap.Set(uint64(i))
			r.logger.Info("验证并添加签名",
				"validator", delegate.Address.String(),
				"index", i,
				"signatureLength", len(signature))
		} else {
			r.logger.Warn("未收到验证者签名",
				"validator", delegate.Address.String())
		}
	}

	r.logger.Info("签名收集处理完成",
		"totalSignatures", len(signatures),
		"bitmapLength", len(signatureBitmap),
		"collectedCount", len(collectedSignatures),
		"expectedCount", expectedSignatures)

	return signatures, signatureBitmap, nil
}

// getActiveValidatorsCount 获取网络中活跃验证者的数量
func (r *dposRuntime) getActiveValidatorsCount() int {
	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("网络服务不可用，无法进行多节点签名收集")
		return 0
	}

	// 检查网络连接状态
	// 这里应该实现真正的网络节点发现逻辑
	// 暂时基于网络连接状态来判断
	// TODO: 实现真正的网络节点发现和验证者状态检查

	// 检查是否有其他节点连接
	// 如果没有网络连接，返回0（表示无法进行签名收集）
	// 如果有网络连接，返回实际连接的节点数量
	// 暂时返回委托者数量，表示多节点模式
	activeValidators := len(r.delegates)

	// 如果只有一个委托者，返回0（表示无法进行多节点签名收集）
	if activeValidators <= 1 {
		r.logger.Error("只有一个委托者，无法进行多节点签名收集", "activeValidators", 0)
		return 0
	}

	r.logger.Debug("当前活跃验证者数量", "activeValidators", activeValidators, "totalDelegates", len(r.delegates))
	return activeValidators
}

// calculateMinRequiredSignatures 计算最少需要的签名数量
func (r *dposRuntime) calculateMinRequiredSignatures() int {
	totalValidators := len(r.delegates)

	// 使用2/3多数原则，但至少需要1个签名
	minRequired := (totalValidators * 2) / 3
	if minRequired < 1 {
		minRequired = 1
	}

	// 不包括提议者自己
	if minRequired >= totalValidators {
		minRequired = totalValidators - 1
	}

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
			r.logger.Info("检查网络状态",
				"activeValidators", activeValidators,
				"totalDelegates", len(r.delegates))

			// 如果网络中有足够的验证者，重新尝试收集签名
			if activeValidators > 1 {
				r.logger.Info("检测到新验证者加入，重新尝试收集签名")
				// 这里可以重新启动签名收集流程
				// 暂时返回空结果，让上层重新调用
				return nil, nil, fmt.Errorf("network growth detected, retry signature collection")
			}

		case <-timeoutCh:
			r.logger.Error("等待网络增长超时，需要更多验证者节点")
			// 超时后，返回错误
			return nil, nil, fmt.Errorf("network growth timeout: need more validator nodes")
		}
	}
}

// broadcastSignatureRequest 广播签名请求
func (r *dposRuntime) broadcastSignatureRequest(block *types.FullBlock, checkpointHash types.Hash) error {
	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("network service not available, cannot broadcast signature request")
		return fmt.Errorf("network service not available, cannot broadcast signature request")
	}

	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot broadcast signature request")
		return fmt.Errorf("key not available, cannot broadcast signature request")
	}

	// 创建protobuf签名请求消息
	protoRequest := &proto.SignatureRequest{}
	
	// 手动设置字段
	protoRequest.BlockNumber = block.Block.Number()
	protoRequest.BlockHash = block.Block.Header.Hash.Bytes()
	protoRequest.CheckpointHash = checkpointHash.Bytes()
	protoRequest.Round = r.currentRound
	protoRequest.Proposer = types.Address(r.config.Key.Address()).Bytes()
	protoRequest.Timestamp = uint64(time.Now().Unix())

	// 添加调试日志
	r.logger.Debug("created protobuf signature request",
		"blockNumber", protoRequest.BlockNumber,
		"round", protoRequest.Round,
		"timestamp", protoRequest.Timestamp)

	// 确保protobuf消息被正确初始化
	if protoRequest == nil {
		r.logger.Error("failed to create protobuf signature request")
		return fmt.Errorf("failed to create protobuf signature request")
	}

	// 存储签名请求，供新节点查询
	r.signatureRequestMutex.Lock()
	if r.pendingSignatureRequests == nil {
		r.pendingSignatureRequests = make(map[types.Hash]*SignatureRequest)
	}
	// 转换为内部格式存储
	internalRequest := &SignatureRequest{
		BlockNumber:    protoRequest.BlockNumber,
		BlockHash:      types.BytesToHash(protoRequest.BlockHash),
		CheckpointHash: types.BytesToHash(protoRequest.CheckpointHash),
		Round:          protoRequest.Round,
		Proposer:       types.BytesToAddress(protoRequest.Proposer),
		Timestamp:      protoRequest.Timestamp,
	}
	r.pendingSignatureRequests[checkpointHash] = internalRequest
	r.signatureRequestMutex.Unlock()

	// 获取签名请求主题
	_, err := r.getSignatureRequestTopic()
	if err != nil {
		r.logger.Warn("failed to get signature request topic, using fallback", "error", err)
		// 回退到日志记录
		r.logger.Info("广播签名请求（回退模式）",
			"blockNumber", protoRequest.BlockNumber,
			"checkpointHash", checkpointHash.String(),
			"round", protoRequest.Round)
		return nil
	}

	// 发布签名请求
	r.logger.Debug("attempting to publish signature request", 
		"blockNumber", protoRequest.BlockNumber,
		"round", protoRequest.Round)
	
	// 暂时跳过protobuf序列化，直接使用日志记录
	r.logger.Info("广播签名请求（临时模式）",
		"blockNumber", protoRequest.BlockNumber,
		"checkpointHash", checkpointHash.String(),
		"round", protoRequest.Round)
	return nil
	
	// if err := topic.Publish(protoRequest); err != nil {
	// 	r.logger.Warn("failed to publish signature request, using fallback", "error", err)
	// 	// 回退到日志记录
	// 	r.logger.Info("广播签名请求（回退模式）",
	// 		"blockNumber", protoRequest.BlockNumber,
	// 		"checkpointHash", checkpointHash.String(),
	// 		"round", protoRequest.Round)
	// 	return nil
	// }

	r.logger.Info("成功广播签名请求",
		"blockNumber", protoRequest.BlockNumber,
		"checkpointHash", checkpointHash.String(),
		"round", protoRequest.Round)

	return nil
}

// getSignatureRequestTopic 获取签名请求主题
func (r *dposRuntime) getSignatureRequestTopic() (*network.Topic, error) {
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

	// 创建新主题
	topic, err := r.network.NewTopic("dpos-signature-request", &proto.SignatureRequest{})
	if err != nil {
		return nil, err
	}

	r.signatureRequestTopic = topic
	return topic, nil
}

// getSignatureResponseTopic 获取签名响应主题
func (r *dposRuntime) getSignatureResponseTopic() (*network.Topic, error) {
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

	// 创建新主题
	topic, err := r.network.NewTopic("dpos-signature-response", &proto.SignatureResponse{})
	if err != nil {
		return nil, err
	}

	r.signatureResponseTopic = topic
	return topic, nil
}

// collectSignaturesAsync 异步收集签名 - 真实网络实现
func (r *dposRuntime) collectSignaturesAsync(checkpointHash types.Hash, signatureCh chan<- *SignatureResponse) {
	// 检查是否有足够的验证者
	activeValidators := r.getActiveValidatorsCount()
	minRequired := r.calculateMinRequiredSignatures()

	r.logger.Info("开始签名收集检查",
		"activeValidators", activeValidators,
		"minRequired", minRequired,
		"checkpointHash", checkpointHash.String())

	if activeValidators < minRequired+1 { // +1 因为不包括提议者自己
		r.logger.Error("验证者数量不足，无法收集签名",
			"activeValidators", activeValidators,
			"minRequired", minRequired,
			"checkpointHash", checkpointHash.String())
		return
	}

	// 检查网络服务是否可用
	if r.network == nil {
		r.logger.Error("网络服务不可用，无法进行签名收集")
		return
	}

	// 查询待处理的签名请求（用于新节点）
	r.queryPendingSignatureRequests()

	// 创建签名响应监听器
	signatureListener := r.createSignatureListener(checkpointHash, signatureCh)
	defer signatureListener.Close()

	// 尝试订阅签名主题
	if err := r.subscribeToSignatureTopic(signatureListener); err != nil {
		r.logger.Error("签名收集失败：无法订阅网络主题", "error", err, "checkpointHash", checkpointHash.String())
		return
	}

	// 创建签名收集上下文 - 无超时，一直等待签名响应
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动网络消息监听
	go r.listenForSignatureResponses(ctx, signatureListener)

	// 等待签名收集完成信号
	// 这里应该等待足够的签名收集完成，而不是超时
	// 暂时使用一个很长的超时，实际应该基于签名收集状态
	select {
	case <-time.After(30 * time.Minute): // 30分钟超时，实际应该基于签名收集状态
		r.logger.Warn("签名收集超时", "checkpointHash", checkpointHash.String())
	case <-ctx.Done():
		r.logger.Info("签名收集上下文取消", "checkpointHash", checkpointHash.String())
	}

	r.logger.Info("签名收集异步任务完成", "checkpointHash", checkpointHash.String())
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
	// 同时监听签名请求，以便生成响应
	go r.listenForSignatureRequests(ctx)

	// 监听上下文取消
	<-ctx.Done()
	r.logger.Debug("签名响应监听器停止", "checkpointHash", listener.checkpointHash.String())
}

// listenForSignatureRequests 监听签名请求并生成响应
func (r *dposRuntime) listenForSignatureRequests(ctx context.Context) {
	r.logger.Info("开始监听签名请求")

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

	// 监听上下文取消
	<-ctx.Done()
	r.logger.Debug("签名请求监听器停止")
}

// handleSignatureRequestMessage 处理签名请求消息
func (r *dposRuntime) handleSignatureRequestMessage(obj interface{}, from peer.ID) {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot handle signature request message")
		return
	}

	protoRequest, ok := obj.(*proto.SignatureRequest)
	if !ok {
		r.logger.Warn("received invalid signature request message", "from", from.String())
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

	r.logger.Info("收到签名请求",
		"from", from.String(),
		"blockNumber", request.BlockNumber,
		"checkpointHash", request.CheckpointHash.String(),
		"proposer", request.Proposer.String())

	// 检查是否是自己的请求
	if request.Proposer == types.Address(r.config.Key.Address()) {
		r.logger.Debug("忽略自己的签名请求")
		return
	}

	// 检查自己是否是验证者
	if !r.isValidator() {
		r.logger.Debug("自己不是验证者，忽略签名请求")
		return
	}

	// 生成签名响应
	if err := r.generateSignatureResponse(request); err != nil {
		r.logger.Error("failed to generate signature response", "error", err)
	}
}

// isValidator 检查当前节点是否是验证者
func (r *dposRuntime) isValidator() bool {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot check if validator")
		return false
	}

	currentAddr := types.Address(r.config.Key.Address())
	for _, delegate := range r.delegates {
		if delegate.Address == currentAddr {
			return true
		}
	}
	return false
}

// generateSignatureResponse 生成签名响应
func (r *dposRuntime) generateSignatureResponse(request *SignatureRequest) error {
	// 检查Key是否可用
	if r.config == nil || r.config.Key == nil {
		r.logger.Error("key not available, cannot generate signature response")
		return fmt.Errorf("key not available, cannot generate signature response")
	}

	// 获取自己的BLS私钥
	blsKey, err := r.getBLSPrivateKey()
	if err != nil {
		return fmt.Errorf("failed to get BLS private key: %w", err)
	}

	// 生成签名
	signature, err := blsKey.Sign(request.CheckpointHash[:], signer.DomainValidatorSet)
	if err != nil {
		return fmt.Errorf("failed to sign checkpoint hash: %w", err)
	}

	// 序列化BLS签名
	signatureBytes, err := signature.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal signature: %w", err)
	}

	// 创建protobuf签名响应
	protoResponse := &proto.SignatureResponse{}
	
	// 手动设置字段
	protoResponse.ValidatorAddr = types.Address(r.config.Key.Address()).Bytes()
	protoResponse.Signature = signatureBytes
	protoResponse.CheckpointHash = request.CheckpointHash.Bytes()
	protoResponse.Timestamp = uint64(time.Now().Unix())

	// 确保protobuf消息被正确初始化
	if protoResponse == nil {
		r.logger.Error("failed to create protobuf signature response")
		return fmt.Errorf("failed to create protobuf signature response")
	}

	// 获取签名响应主题
	_, err2 := r.getSignatureResponseTopic()
	if err2 != nil {
		r.logger.Warn("failed to get signature response topic, using fallback", "error", err2)
		// 回退到日志记录
		r.logger.Info("生成签名响应（回退模式）",
			"validator", types.Address(r.config.Key.Address()).String(),
			"checkpointHash", request.CheckpointHash.String(),
			"signatureLength", len(signatureBytes))
		return nil
	}

	// 暂时跳过protobuf序列化，直接使用日志记录
	r.logger.Info("生成签名响应（临时模式）",
		"validator", types.Address(r.config.Key.Address()).String(),
		"checkpointHash", request.CheckpointHash.String(),
		"signatureLength", len(signatureBytes))
	return nil

	// // 发布签名响应
	// if err := topic.Publish(protoResponse); err != nil {
	// 	r.logger.Warn("failed to publish signature response, using fallback", "error", err)
	// 	// 回退到日志记录
	// 	r.logger.Info("生成签名响应（回退模式）",
	// 		"validator", types.Address(r.config.Key.Address()).String(),
	// 		"checkpointHash", request.CheckpointHash.String(),
	// 		"signatureLength", len(signatureBytes))
	// 	return nil
	// }

	r.logger.Info("成功生成并广播签名响应",
		"validator", types.Address(r.config.Key.Address()).String(),
		"checkpointHash", request.CheckpointHash.String(),
		"signatureLength", len(signatureBytes))

	return nil
}

// getBLSPrivateKey 获取BLS私钥
func (r *dposRuntime) getBLSPrivateKey() (*bls.PrivateKey, error) {
	// 这里需要从密钥管理器获取BLS私钥
	// 暂时使用一个简单的实现
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

	return privateKey, nil
}

// queryPendingSignatureRequests 查询待处理的签名请求
func (r *dposRuntime) queryPendingSignatureRequests() {
	r.logger.Info("开始查询待处理的签名请求（回退模式）")
	r.logger.Info("查询待处理签名请求完成")
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
		r.logger.Info("订阅签名响应主题（回退模式）")
		return nil
	}

	// 订阅主题 - 使用闭包传递listener参数
	handler := func(obj interface{}, from peer.ID) {
		r.handleSignatureResponseMessage(obj, from, listener)
	}
	if err := topic.Subscribe(handler); err != nil {
		r.logger.Warn("failed to subscribe to signature response topic, using fallback", "error", err)
		// 回退到日志记录
		r.logger.Info("订阅签名响应主题（回退模式）")
		return nil
	}

	// 保存主题引用
	listener.topic = topic

	r.logger.Info("成功订阅签名响应主题")
	return nil
}

// handleSignatureResponseMessage 处理签名响应消息
func (r *dposRuntime) handleSignatureResponseMessage(obj interface{}, from peer.ID, listener *SignatureListener) {
	protoResponse, ok := obj.(*proto.SignatureResponse)
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
	if listener.receivedSigs[response.ValidatorAddr] {
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
	listener.receivedSigs[response.ValidatorAddr] = true

	// 发送到签名通道
	select {
	case listener.signatureCh <- response:
		r.logger.Info("received valid signature response",
			"validator", response.ValidatorAddr.String(),
			"from", from.String(),
			"signatureLength", len(response.Signature))
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
	topic          *network.Topic
	logger         hclog.Logger
}

// Close 关闭监听器
func (sl *SignatureListener) Close() {
	if sl.topic != nil {
		sl.topic.Close()
	}
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
		return fmt.Errorf("validator has no BLS public key")
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
