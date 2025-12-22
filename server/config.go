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

	// EVM 配置
	UseGethEVM bool `yaml:"use_geth_evm"` // 是否使用 go-ethereum EVM（true=最新 EVM，false=原生 EVM）
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
