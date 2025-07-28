package dpos

import (
	_ "context"
	"encoding/json"
	_ "errors"
	"fmt"
	"math/big"
	_ "path/filepath"
	"sync"
	"time"
	_ "time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/syncer"

	"github.com/hashicorp/go-hclog"
	bolt "go.etcd.io/bbolt"

	_ "github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus"
	_ "github.com/Vcity-Team/vcitychain/consensus/dpos/contractsapi"
	_ "github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	_ "github.com/Vcity-Team/vcitychain/contracts"
	_ "github.com/Vcity-Team/vcitychain/forkmanager"
	"github.com/Vcity-Team/vcitychain/helper/common"
	_ "github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/state"
	_ "github.com/Vcity-Team/vcitychain/syncer"
	"github.com/Vcity-Team/vcitychain/types"
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

func (d *DPoS) VerifyHeader(header *types.Header) error {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) ProcessHeaders(headers []*types.Header) error {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) GetBlockCreator(header *types.Header) (types.Address, error) {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) PreCommitState(block *types.Block, txn *state.Transition) error {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) GetSyncProgression() *progress.Progression {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) GetBridgeProvider() consensus.BridgeDataProvider {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) FilterExtra(extra []byte) ([]byte, error) {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) Start() error {
	//TODO implement me
	panic("implement me")
}

func (d *DPoS) Close() error {
	//TODO implement me
	panic("implement me")
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
	// TODO: 从状态存储中获取质押信息
	// 临时返回空值，等待状态存储实现
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
	// TODO: 在事务中获取质押信息
	// 临时返回空值，等待状态存储实现
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

// 辅助方法
func (d *DPoS) getDelegatesFromState(blockNumber uint64) (validator.AccountSet, error) {
	// TODO: 从状态存储中获取历史受托人集合
	// 临时返回当前受托人集合
	return d.delegates.Copy(), nil
}

func (d *DPoS) getDelegatesFromStateWithTx(blockNumber uint64, dbTx *bolt.Tx) (validator.AccountSet, error) {
	// TODO: 在事务中获取受托人集合
	// 临时返回当前受托人集合
	return d.getDelegatesFromState(blockNumber)
}

func (d *DPoS) getVotingPowerFromStateWithTx(blockNumber uint64, delegate types.Address, dbTx *bolt.Tx) (*big.Int, error) {
	// TODO: 在事务中获取投票权重
	// 临时返回当前投票权重
	return d.GetVotingPower(blockNumber, delegate)
}
