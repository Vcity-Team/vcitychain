package dpos

import (
	"math/big"
	"sync"
	"time"

	core "github.com/Vcity-Team/vcitychain/consensus/dpos/core"
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
	Voter          types.Address `json:"voter"`
	Delegate       types.Address `json:"delegate"`
	Amount         *big.Int      `json:"amount"`
	Round          uint64        `json:"round"`
	Signature      []byte        `json:"signature"`
	Timestamp      uint64        `json:"timestamp"`
	EffectiveEpoch uint64        `json:"effectiveEpoch"` // 生效的epoch（边界应用）
	Applied        bool          `json:"applied"`        // 是否已应用
}

// VoteRecord 投票记录（用于边界应用）
type VoteRecord struct {
	Voter          types.Address `json:"voter"`
	Delegate       types.Address `json:"delegate"`
	Amount         *big.Int      `json:"amount"`
	Timestamp      uint64        `json:"timestamp"`
	EffectiveEpoch uint64        `json:"effectiveEpoch"`
	Applied        bool          `json:"applied"`
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
	Staker          types.Address          `json:"staker"`
	Amount          *big.Int               `json:"amount"`                   // 当前金额（削减后）
	OriginalAmount  *big.Int               `json:"originalAmount,omitempty"` // 原始投票金额（第一次投票时的金额，削减前）
	StartTime       uint64                 `json:"startTime"`
	EndTime         uint64                 `json:"endTime"`
	IsLocked        bool                   `json:"isLocked"`
	IsActive        bool                   `json:"isActive"`
	Rewards         *big.Int               `json:"rewards"`
	Delegate        types.Address          `json:"delegate"`
	FaultFlag       map[string]interface{} `json:"faultFlag,omitempty"`       // 故障标志信息：isFaulty, missedBlocks, reason
	SlashingRecords []*SlashingRecord      `json:"slashingRecords,omitempty"` // 削减历史（按时间顺序）

	EffectiveEpoch uint64 `json:"effectiveEpoch,omitempty"` // 生效的epoch（边界应用）
	Applied        bool   `json:"applied,omitempty"`        // 是否已应用

	PendingUnvote        bool   `json:"pendingUnvote,omitempty"`        // 是否待撤销（边界生效，与投票一致避免权重突变导致分叉）
	UnvoteEffectiveEpoch uint64 `json:"unvoteEffectiveEpoch,omitempty"` // 撤销生效的 epoch
}

// 参数表决相关数据结构

// Governance-related type aliases (re-exported from core for backwards compatibility).
type ParameterProposal = core.ParameterProposal
type ProposalStatus = core.ProposalStatus

const (
	ProposalPending  ProposalStatus = core.ProposalPending
	ProposalActive   ProposalStatus = core.ProposalActive
	ProposalPassed   ProposalStatus = core.ProposalPassed
	ProposalRejected ProposalStatus = core.ProposalRejected
	ProposalExecuted ProposalStatus = core.ProposalExecuted
)

type ParameterVote = core.ParameterVote
type ProposalScheduleMeta = core.ProposalScheduleMeta
type ProposalCreateTxData = core.ProposalCreateTxData
type ProposalVoteTxData = core.ProposalVoteTxData
type ProposalExecuteTxData = core.ProposalExecuteTxData

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
	Rank         int           `json:"rank"`         // 排名（按 totalVotes 倒序排序后的序号，从1开始）
	// 冻结相关字段
	FrozenAt            uint64 `json:"frozenAt"`            // 冻结时间（注册时设置）
	UnfreezeAt          uint64 `json:"unfreezeAt"`          // 解冻时间（退出时设置，0表示未解冻）
	UnfreezeAvailableAt uint64 `json:"unfreezeAvailableAt"` // 资金可用时间（解冻时间 + 锁定期，0表示未解冻）
	// 保证金托管与链上退款（托管地址见 getDelegateDepositEscrowAddress）
	DepositHeldInEscrow bool `json:"depositHeldInEscrow,omitempty"` // 注册时 tx.To 为候选人托管地址
	DepositRefunded     bool `json:"depositRefunded,omitempty"`     // 已在区块 Transition 中退回原生币
	// LegacyDepositContract 仅针对老注册（注册 tx.To=nil，保证金进 CREATE）；扩展迁移归集进托管后写入。新注册 To=托管不会在迁移里写此字段。
	LegacyDepositContract types.Address `json:"legacyDepositContract,omitempty"`
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

type ParameterUpdate = core.ParameterUpdate

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
	Address         types.Address                       `json:"address"`         // 投票者地址
	VotingPower     *big.Int                            `json:"votingPower"`     // 投票权重（保留用于兼容）
	VotedDelegates  []types.Address                     `json:"votedDelegates"`  // 投票的验证者列表（保留用于兼容）
	DelegateVotes   map[types.Address]*big.Int          `json:"delegateVotes"`   // delegate -> 投票金额（削减后）
	SlashingRecords map[types.Address][]*SlashingRecord `json:"slashingRecords"` // 削减历史记录
	LastVoteTime    uint64                              `json:"lastVoteTime"`    // 最后投票时间
	LockedUntil     uint64                              `json:"lockedUntil"`     // 锁定到期时间
	Nonce           map[uint64]bool                     `json:"nonce"`           // 防重放
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
	// 新增：BLS公钥，确保签名验证一致性
	BlsPublicKey []byte `json:"blsPublicKey"`
	// 新增：佣金相关字段（基点表示，500 = 5%）
	CommissionRate        uint64 `json:"commissionRate"`        // 当前生效的佣金率
	PendingCommissionRate uint64 `json:"pendingCommissionRate"` // 待生效的佣金率
	CommissionUpdateTime  uint64 `json:"commissionUpdateTime"`  // 最近一次修改时间（Unix时间戳）
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

// SlashingRecord 削减记录（存储在VoterInfo和StakeInfo中）
type SlashingRecord struct {
	ValidatorAddr          types.Address `json:"validator_addr"`
	BlockNumber            uint64        `json:"block_number"`
	EpochNumber            uint64        `json:"epoch_number"`
	Timestamp              uint64        `json:"timestamp"`
	SlashAmount            *big.Int      `json:"slash_amount"`                       // 本次削减的金额
	OldVoteAmount          *big.Int      `json:"old_vote_amount"`                    // 削减前的投票金额
	NewVoteAmount          *big.Int      `json:"new_vote_amount"`                    // 削减后的投票金额
	SlashRate              uint64        `json:"slash_rate"`                         // 削减率（基点）
	Reason                 string        `json:"reason"`                             // 削减原因
	MissedBlocks           uint64        `json:"missed_blocks,omitempty"`            // 漏块数（轻度违规）
	MissedBlocksPercentage uint64        `json:"missed_blocks_percentage,omitempty"` // 漏块率（轻度违规）
	DoubleSigningHeight    uint64        `json:"double_signing_height,omitempty"`    // 双重签名高度（严重违规）
}

// SlashingHistory 处罚历史记录（存储在数据库中，用于验证者）
type SlashingHistory struct {
	ValidatorAddr          types.Address `json:"validator_addr"`
	BlockNumber            uint64        `json:"block_number"`
	EpochNumber            uint64        `json:"epoch_number"`
	Timestamp              uint64        `json:"timestamp"`
	SlashAmount            *big.Int      `json:"slash_amount"`                       // 本次削减的总金额
	OldVotingPower         *big.Int      `json:"old_voting_power"`                   // 削减前的总投票权重
	NewVotingPower         *big.Int      `json:"new_voting_power"`                   // 削减后的总投票权重
	SlashRate              uint64        `json:"slash_rate"`                         // 削减率（基点）
	Reason                 string        `json:"reason"`                             // 削减原因
	MissedBlocks           uint64        `json:"missed_blocks,omitempty"`            // 漏块数（轻度违规）
	MissedBlocksPercentage uint64        `json:"missed_blocks_percentage,omitempty"` // 漏块率（轻度违规）
	DoubleSigningHeight    uint64        `json:"double_signing_height,omitempty"`    // 双重签名高度（严重违规）
}
