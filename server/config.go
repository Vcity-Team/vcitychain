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
	ConsensusSwitchHeight uint64
	
	// 🆕 新增：DPoS验证者数量
	DPoSValidatorsCount   uint64
	
	// 🆕 新增：DPoS最小质押门槛
	DPoSDelegateThreshold *big.Int
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
