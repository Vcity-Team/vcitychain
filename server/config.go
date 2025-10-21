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

	// 🆕 新增：共识切换高度
	ConsensusSwitchHeight uint64 `yaml:"consensus_switch_height"`

	// 🆕 新增：DPoS验证者数量
	DPoSValidatorsCount uint64

	// 🆕 新增：DPoS最小质押门槛
	DPoSDelegateThreshold *big.Int

	// 🆕 新增：DPoS经济系统配置
	DPoSEpochDuration        string `yaml:"dpos_epoch_duration"`
	DPoSRewardDistribution   string `yaml:"dpos_reward_distribution"`    // 奖励分发地址
	DPoSRewardAmount         string `yaml:"dpos_reward_amount"`          // 每个epoch奖励金额
	DPoSValidatorRewardRatio uint64 `yaml:"dpos_validator_reward_ratio"` // 验证者奖励比例
	DPoSVoterRewardRatio     uint64 `yaml:"dpos_voter_reward_ratio"`     // 投票者奖励比例
	DPoSProposalPeriod       string `yaml:"dpos_proposal_period"`        // 提案投票周期（时间字符串，如"24h", "1d"）
	DPoSSRThreshold          string `yaml:"dpos_SR_threshold"`           // SR候选人保证金阈值
	BlockTimeSeconds         uint64 `yaml:"block_time_s"`                // 区块间隔时间（秒）
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
