package dpos

import (
	"math/big"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
)

// DPoSCache DPoS缓存
type DPoSCache struct {
	voterCache    map[types.Address]*VoterInfo
	delegateCache map[types.Address]*validator.ValidatorMetadata
	rewardCache   map[types.Address]*big.Int
	cacheTTL      time.Duration
	lastUpdate    time.Time
	lock          sync.RWMutex
}

// BatchProcessor 批量处理器
type BatchProcessor struct {
	voteQueue     chan *VoteMessage
	delegateQueue chan *DelegateMessage
	batchSize     int
	batchTimeout  time.Duration
	workerCount   int
	stopCh        chan struct{}
	workers       []chan struct{}
	wg            sync.WaitGroup
}

// DPoSMetrics DPoS指标
type DPoSMetrics struct {
	BlockRewards   *big.Int
	TotalVotes     uint64
	TotalDelegates uint64
	ActiveVoters   uint64
	LastBlockTime  time.Time
	lock           sync.RWMutex
}

// VoteMessage 投票消息
type VoteMessage struct {
	Voter     types.Address `json:"voter"`
	Delegate  types.Address `json:"delegate"`
	Amount    *big.Int      `json:"amount"`
	Round     uint64        `json:"round"`
	Signature []byte        `json:"signature"`
	Timestamp uint64        `json:"timestamp"`
}

// DelegateMessage 委托消息
type DelegateMessage struct {
	Delegate  types.Address `json:"delegate"`
	Action    string        `json:"action"` // "register", "unregister", "update"
	Stake     *big.Int      `json:"stake"`
	Signature []byte        `json:"signature"`
	Timestamp uint64        `json:"timestamp"`
}

// NetworkMessage 网络消息
type NetworkMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// MessageType 消息类型常量
const (
	VoteMessageType     = "vote"
	DelegateMessageType = "delegate"
	IBFTMessageType     = "ibft"
)
