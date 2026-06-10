package server

import (
	"math/big"
	"net"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
)

const DefaultGRPCPort int = 9632
const DefaultJSONRPCPort int = 8545

// Config is used to parametrize the minimal client
type Config struct {
	Chain *chain.Chain

	JSONRPC    *JSONRPC
	GRPCAddr   *net.TCPAddr
	LibP2PAddr *net.TCPAddr
	// PprofAddr 非 nil 时在启动阶段监听该地址提供 /debug/pprof（仅用于诊断）
	PprofAddr *net.TCPAddr

	PriceLimit         uint64
	MaxAccountEnqueued uint64
	MaxSlots           uint64

	Telemetry *Telemetry
	Network   *network.Config

	DataDir     string
	RestoreFile *string

	Seal bool

	SecretsManager *secrets.SecretsManagerConfig

	LogLevel hclog.Level

	JSONLogFormat bool

	LogFilePath string

	Relayer bool

	NumBlockConfirmations uint64
	MetricsInterval       time.Duration

	// 新增：共识切换高度
	ConsensusSwitchHeight uint64 `yaml:"consensus_switch_height"`

	// 新增：DPoS验证者数量
	DPoSValidatorsCount uint64

	// 新增：DPoS最小质押门槛
	DPoSDelegateThreshold *big.Int

	// 新增：DPoS经济系统配置
	DPoSEpochDuration       string `yaml:"dpos_epoch_duration"`
	DPoSRewardDistribution  string `yaml:"dpos_reward_distribution"`   // 奖励分发地址
	DPoSRewardAmount        string `yaml:"dpos_reward_amount"`         // 每个epoch奖励金额
	DPoSProposalVotePeriod  string `yaml:"dpos_proposal_vote_period"`  // 提案表决周期（时间字符串，如"2m", "24h"）
	DPoSProposalValidPeriod string `yaml:"dpos_proposal_valid_period"` // 提案有效期（时间字符串，如"1d", "7d"）
	DPoSSRThreshold         string `yaml:"dpos_SR_threshold"`          // SR候选人保证金阈值
	BlockTimeSeconds        uint64 `yaml:"block_time_s"`               // 区块间隔时间（秒）

	// 新增：DPoS佣金配置
	DPoSCommissionRatio     uint64 `yaml:"dpos_commission_ratio"`     // 默认佣金率（基点），验证者未设置时使用
	DPoSCommissionEffective string `yaml:"dpos_commission_effective"` // 佣金生效周期（如"21d"）

	// 冻结相关配置
	DPoSMinFreezePeriod    uint64 `yaml:"dpos_min_freeze_period"`    // 最小冻结期（秒）
	DPoSUnfreezeLockPeriod uint64 `yaml:"dpos_unfreeze_lock_period"` // 解冻锁定期（秒）

	// 削减相关配置
	DPoSMissedBlocksPercentage uint64 `yaml:"dpos_missed_blocks_percentage"`  // 漏块率阈值（基点）
	DPoSMinorOffenseSlashRate  uint64 `yaml:"dpos_minor_offense_slash_rate"`  // 轻度违规削减率（基点）
	DPoSSevereOffenseSlashRate uint64 `yaml:"dpos_severe_offense_slash_rate"` // 严重违规削减率（基点）

	// DPoS 奖励与启动引导配置（扩展字段）
	// voter_target_apy: 投票者目标年化（基点，500=5%）
	VoterTargetAPYBps uint64 `yaml:"voter_target_apy"`
	// block_producer_reward_per_block: 节点出块奖励 wei/块（独立于质押池）
	BlockProducerRewardPerBlock string `yaml:"block_producer_reward_per_block"`
	// producer_reward_activation_epoch: 节点出块奖励激活 epoch（0=配置后立即生效）
	ProducerRewardActivationEpoch uint64 `yaml:"producer_reward_activation_epoch"`
	// vote_lock_activation_epoch: 投票余额账内锁定激活 epoch（0=未启用）
	VoteLockActivationEpoch uint64 `yaml:"vote_lock_activation_epoch"`
	// dpos_bootstrap_rpc: 启动时通过 JSON-RPC eth_call 查询 staking 合约 validators() 的端点
	DPoSBootstrapRPC string `yaml:"dpos_bootstrap_rpc"`
	// dpos_disable_double_sign_slashing: 为 true 时关闭双签检测与削减（由 server 写入 engineConfig）
	DPoSDisableDoubleSignSlashing bool `yaml:"dpos_disable_double_sign_slashing"`
	// dpos_wall_clock_slot_alignment: 块头时间戳对齐墙钟 slot（默认 true，可不写；显式 false 可关闭）
	DPoSWallClockSlotAlignment bool `yaml:"dpos_wall_clock_slot_alignment"`
	// dpos_relax_header_timestamp_order: 为 true 时临时接受子块时间戳<=父块（默认 false）
	DPoSRelaxHeaderTimestampOrder bool `yaml:"dpos_relax_header_timestamp_order"`
}

// Telemetry holds the config details for metric services
type Telemetry struct {
	PrometheusAddr *net.TCPAddr
}

// JSONRPC holds the config details for the JSON-RPC server
type JSONRPC struct {
	JSONRPCAddr              *net.TCPAddr
	AccessControlAllowOrigin []string
	BatchLengthLimit         uint64
	BlockRangeLimit          uint64
	ConcurrentRequestsDebug  uint64
	WebSocketReadLimit       uint64
}
