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

// BLS公钥广播消息
type BLSKeyBroadcastMessage struct {
	Address      types.Address `json:"address"`      // 节点地址
	BLSPublicKey []byte        `json:"blsPublicKey"` // BLS公钥字节
	Timestamp    uint64        `json:"timestamp"`    // 时间戳
	NodeType     string        `json:"nodeType"`     // 节点类型："genesis", "sync"
}

// BLS公钥确认消息
type BLSKeyAckMessage struct {
	Address   types.Address `json:"address"`   // 确认的地址
	Status    string        `json:"status"`    // 状态："received", "saved"
	Message   string        `json:"message"`   // 状态消息
	Timestamp uint64        `json:"timestamp"` // 时间戳
}

// BLS公钥请求消息
type BLSKeyRequestMessage struct {
	RequestedAddress types.Address `json:"requestedAddress"` // 请求的地址
	Requester        types.Address `json:"requester"`        // 请求者地址
	Timestamp        uint64        `json:"timestamp"`        // 时间戳
}

// BLS公钥响应消息
type BLSKeyResponseMessage struct {
	RequestedAddress types.Address `json:"requestedAddress"` // 被请求的地址
	Requester        types.Address `json:"requester"`        // 请求者地址
	BLSPublicKey     []byte        `json:"blsPublicKey"`     // BLS公钥（如果有的话）
	Found            bool          `json:"found"`            // 是否找到公钥
	BlockNumber      uint64        `json:"blockNumber"`      // 请求的区块高度
	Timestamp        uint64        `json:"timestamp"`        // 时间戳
}

// 消息类型常量
const (
	VoteMessageType     = "vote"
	DelegateMessageType = "delegate"
	IBFTMessageType     = "ibft"
	BLSKeyBroadcastType = "bls_key_broadcast"
	BLSKeyAckType       = "bls_key_ack"
	BLSKeyRequestType   = "bls_key_request"
	BLSKeyResponseType  = "bls_key_response"
)
