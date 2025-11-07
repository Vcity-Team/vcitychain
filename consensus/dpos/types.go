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

	// 增强的缓存管理
	voterCacheTime    map[types.Address]time.Time // 投票者缓存时间戳
	delegateCacheTime map[types.Address]time.Time // 委托者缓存时间戳
	rewardCacheTime   map[types.Address]time.Time // 奖励缓存时间戳
	maxCacheSize      int                         // 最大缓存大小
}

// BatchProcessor 批量处理器
type BatchProcessor struct {
	voteQueue     chan *VoteMessage
	delegateQueue chan *DelegateMessage
	batchSize     int
	batchTimeout  time.Duration
	workerCount   int
	stopCh        chan struct{}
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

// ========== 从 dpos.go 迁移的数据类型 ==========

// StakeInfo 质押信息结构体
type StakeInfo struct {
	Staker    types.Address          `json:"staker"`
	Amount    *big.Int               `json:"amount"`
	StartTime uint64                 `json:"startTime"`
	EndTime   uint64                 `json:"endTime"`
	IsLocked  bool                   `json:"isLocked"`
	IsActive  bool                   `json:"isActive"`
	Rewards   *big.Int               `json:"rewards"`
	Delegate  types.Address          `json:"delegate"`
	FaultFlag map[string]interface{} `json:"faultFlag,omitempty"` // 故障标志信息：isFaulty, missedBlocks, reason
}

// 🆕 参数表决相关数据结构

// ParameterProposal 参数表决提案结构
type ParameterProposal struct {
	ID            string                          `json:"id"`
	ProposalType  string                          `json:"proposalType"`  // 提案类型："parameter" 或 "validator_recovery"
	Parameter     string                          `json:"parameter"`     // 参数名（parameter类型）或验证者地址（recovery类型）
	OldValue      interface{}                     `json:"oldValue"`      // 当前值
	NewValue      interface{}                     `json:"newValue"`      // 提议值
	Proposer      types.Address                   `json:"proposer"`      // 提案者
	StartBlock    uint64                          `json:"startBlock"`    // 投票开始区块
	EndBlock      uint64                          `json:"endBlock"`      // 投票结束区块（表决期结束）
	ValidEndBlock uint64                          `json:"validEndBlock"` // 有效期结束区块
	Status        ProposalStatus                  `json:"status"`        // 提案状态
	Votes         map[types.Address]ParameterVote `json:"votes"`         // 投票记录
	Threshold     uint64                          `json:"threshold"`     // 通过阈值(百分比)
	Description   string                          `json:"description"`   // 提案描述
	CreatedAt     uint64                          `json:"createdAt"`     // 创建时间
	// Recovery专用字段
	ValidatorAddress  types.Address `json:"validatorAddress,omitempty"`  // 要恢复的验证者地址
	RecoveryReason    string        `json:"recoveryReason,omitempty"`    // 恢复理由
	ExecutedAt        uint64        `json:"executedAt,omitempty"`        // 执行时间
	ExecutedBy        string        `json:"executedBy,omitempty"`        // 执行者（提案ID）
	ProposalSignature []byte        `json:"proposalSignature,omitempty"` // 提案创建签名
	// 调度与生效元数据（持久化）
	Schedule ProposalScheduleMeta `json:"schedule,omitempty"`
}

// ProposalStatus 提案状态
type ProposalStatus int

const (
	ProposalPending  ProposalStatus = iota // 待投票
	ProposalActive                         // 投票中
	ProposalPassed                         // 已通过
	ProposalRejected                       // 已拒绝
	ProposalExecuted                       // 已执行
)

// String 返回提案状态的字符串表示
func (ps ProposalStatus) String() string {
	switch ps {
	case ProposalPending:
		return "pending"
	case ProposalActive:
		return "active"
	case ProposalPassed:
		return "passed"
	case ProposalRejected:
		return "rejected"
	case ProposalExecuted:
		return "executed"
	default:
		return "unknown"
	}
}

// ParameterVote 参数表决投票
type ParameterVote struct {
	Voter      types.Address `json:"voter"`
	ProposalID string        `json:"proposalId"`
	Support    bool          `json:"support"` // true=支持, false=反对
	Weight     *big.Int      `json:"weight"`  // 投票权重
	Timestamp  uint64        `json:"timestamp"`
	Signature  []byte        `json:"signature"` // 投票签名
}

// 提案交易数据结构
type ProposalCreateTxData struct {
	ProposalType      string      `json:"proposalType"`             // "parameter" 或 "validator_recovery"
	Parameter         string      `json:"parameter"`                // 参数名或验证者地址
	NewValue          interface{} `json:"newValue"`                 // 新值
	Description       string      `json:"description"`              // 提案描述
	RecoveryReason    string      `json:"recoveryReason,omitempty"` // 恢复理由（仅用于恢复提案）
	ProposerSignature []byte      `json:"proposerSignature"`        // 提案签名
	CreatedAt         uint64      `json:"createdAt"`                // 签名时间戳（用于验签一致性）
}

// 提案调度与生效元数据（持久化在 Proposal 中）
type ProposalScheduleMeta struct {
	// 是否已安排在边界生效-当执行提案时，不立即应用影响出块者集合的变化（如清除故障），而是登记在此，等待下个 epoch 边界统一生效
	Scheduled bool `json:"scheduled"`
	// 生效的目标 epoch
	EffectiveEpoch uint64 `json:"effectiveEpoch"`
	// 是否已在边界应用
	Applied bool `json:"applied"`
	// 实际应用的区块号
	AppliedAtBlock uint64 `json:"appliedAtBlock"`
}

// ProposalVoteTxData 投票交易数据
type ProposalVoteTxData struct {
	ProposalID    string `json:"proposalId"`
	Support       bool   `json:"support"`
	VoteSignature []byte `json:"voteSignature"` // 投票签名
}

// ProposalExecuteTxData 执行提案交易数据
type ProposalExecuteTxData struct {
	ProposalID string `json:"proposalId"`
}

// DelegateRegistration 受托人注册信息（改进的TRON风格）
type DelegateRegistration struct {
	Address      types.Address `json:"address"`      // 受托人地址
	Name         string        `json:"name"`         // 受托人名称
	Website      string        `json:"website"`      // 官方网站
	Description  string        `json:"description"`  // 描述信息
	Deposit      *big.Int      `json:"deposit"`      // 保证金（可退还）
	Status       RegStatus     `json:"status"`       // 注册状态
	CreatedAt    uint64        `json:"createdAt"`    // 申请时间
	TotalVotes   *big.Int      `json:"totalVotes"`   // 总投票数
	IsActive     bool          `json:"isActive"`     // 是否为活跃受托人
	LastVoteTime uint64        `json:"lastVoteTime"` // 最后投票时间
}

// RegStatus 注册状态
type RegStatus int

const (
	RegStatusCandidate RegStatus = iota // 候选人（可接受投票）
	RegStatusActive                     // 活跃受托人
	RegStatusInactive                   // 非活跃状态
	RegStatusWithdrawn                  // 已退出
)

// String 返回注册状态的字符串表示
func (rs RegStatus) String() string {
	switch rs {
	case RegStatusCandidate:
		return "candidate"
	case RegStatusActive:
		return "active"
	case RegStatusInactive:
		return "inactive"
	case RegStatusWithdrawn:
		return "withdrawn"
	default:
		return "unknown"
	}
}

// ParameterUpdate 参数更新记录
type ParameterUpdate struct {
	Parameter  string      `json:"parameter"`
	OldValue   interface{} `json:"oldValue"`
	NewValue   interface{} `json:"newValue"`
	BlockNum   uint64      `json:"blockNum"`   // 生效区块号
	Executed   bool        `json:"executed"`   // 是否已执行
	ProposalID string      `json:"proposalId"` // 关联提案ID
	ExecutedAt uint64      `json:"executedAt"` // 执行时间
}

// ParameterInfo 参数信息
type ParameterInfo struct {
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	MinValue     interface{} `json:"minValue"`
	MaxValue     interface{} `json:"maxValue"`
	Description  string      `json:"description"`
	Category     string      `json:"category"`     // 参数分类：economic, network, consensus等
	CurrentValue interface{} `json:"currentValue"` // 当前值（移除omitempty）
}

// VoterInfo 投票者信息结构
type VoterInfo struct {
	Address        types.Address   `json:"address"`        // 投票者地址
	VotingPower    *big.Int        `json:"votingPower"`    // 投票权重
	VotedDelegates []types.Address `json:"votedDelegates"` // 投票的验证者列表
	LastVoteTime   uint64          `json:"lastVoteTime"`   // 最后投票时间
	LockedUntil    uint64          `json:"lockedUntil"`    // 锁定到期时间
	Nonce          map[uint64]bool `json:"nonce"`          // 防重放
}

// DelegateInfo 受托人信息
type DelegateInfo struct {
	Address          types.Address         `json:"address"`
	VotingPower      *big.Int              `json:"votingPower"`
	TotalVotes       *big.Int              `json:"totalVotes"`
	ProducedBlocks   uint64                `json:"producedBlocks"`
	MissedBlocks     uint64                `json:"missedBlocks"`
	LastBlockTime    uint64                `json:"lastBlockTime"`
	IsActive         bool                  `json:"isActive"`
	IsRegistered     bool                  `json:"isRegistered"`     // 是否已注册
	RegistrationInfo *DelegateRegistration `json:"registrationInfo"` // 注册信息

	BlsPublicKey []byte `json:"blsPublicKey"`
}

// VoteInfo 投票信息结构（用于解析交易数据）
type VoteInfo struct {
	Voter     types.Address `json:"voter"`
	Candidate types.Address `json:"candidate"`
	Amount    *big.Int      `json:"amount"`
}

// DelegateRegistrationInfo 受托人注册信息结构（用于解析交易数据）
type DelegateRegistrationInfo struct {
	Registrant  types.Address `json:"registrant"`
	Name        string        `json:"name"`
	Website     string        `json:"website"`
	Description string        `json:"description"`
	Deposit     *big.Int      `json:"deposit"`
}
