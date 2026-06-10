package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/network"
	"github.com/hashicorp/hcl"
	"gopkg.in/yaml.v3"
)

// Config defines the server configuration params
type Config struct {
	GenesisPath       string `json:"chain_config" yaml:"chain_config"`
	SecretsConfigPath string `json:"secrets_config" yaml:"secrets_config"`
	DataDir           string `json:"data_dir" yaml:"data_dir"`
	BlockGasTarget    string `json:"block_gas_target" yaml:"block_gas_target"`
	GRPCAddr          string `json:"grpc_addr" yaml:"grpc_addr"`
	JSONRPCAddr       string `json:"jsonrpc_addr" yaml:"jsonrpc_addr"`
	// PprofAddr 非空时在本地址启动 net/http/pprof（建议 127.0.0.1:端口，勿对公网暴露）
	PprofAddr                string     `json:"pprof_addr" yaml:"pprof_addr"`
	Telemetry                *Telemetry `json:"telemetry" yaml:"telemetry"`
	Network                  *Network   `json:"network" yaml:"network"`
	ShouldSeal               bool       `json:"seal" yaml:"seal"`
	TxPool                   *TxPool    `json:"tx_pool" yaml:"tx_pool"`
	LogLevel                 string     `json:"log_level" yaml:"log_level"`
	RestoreFile              string     `json:"restore_file" yaml:"restore_file"`
	Headers                  *Headers   `json:"headers" yaml:"headers"`
	LogFilePath              string     `json:"log_to" yaml:"log_to"`
	JSONRPCBatchRequestLimit uint64     `json:"json_rpc_batch_request_limit" yaml:"json_rpc_batch_request_limit"`
	JSONRPCBlockRangeLimit   uint64     `json:"json_rpc_block_range_limit" yaml:"json_rpc_block_range_limit"`
	JSONLogFormat            bool       `json:"json_log_format" yaml:"json_log_format"`
	CorsAllowedOrigins       []string   `json:"cors_allowed_origins" yaml:"cors_allowed_origins"`

	Relayer               bool   `json:"relayer" yaml:"relayer"`
	NumBlockConfirmations uint64 `json:"num_block_confirmations" yaml:"num_block_confirmations"`

	ConcurrentRequestsDebug uint64 `json:"concurrent_requests_debug" yaml:"concurrent_requests_debug"`
	WebSocketReadLimit      uint64 `json:"web_socket_read_limit" yaml:"web_socket_read_limit"`

	MetricsInterval time.Duration `json:"metrics_interval" yaml:"metrics_interval"`

	DPoSValidatorsCount   uint64 `json:"dpos_validators_count" yaml:"dpos_validators_count"`
	DPoSDelegateThreshold string `json:"dpos_delegate_threshold" yaml:"dpos_delegate_threshold"`
	// 创世投票金额（root 账户在共识切换高度给每个创世验证者创建的初始投票记录金额，单位：wei）
	// 不配置则回退使用 dpos_delegate_threshold（向后兼容）
	DPoSGenesisVoteAmount string `json:"dpos_genesis_vote_amount" yaml:"dpos_genesis_vote_amount"`

	// DPoS经济系统配置
	DPoSEpochDuration       string `json:"dpos_epoch_duration" yaml:"dpos_epoch_duration"`
	DPoSRewardDistribution  string `json:"dpos_reward_distribution" yaml:"dpos_reward_distribution"`     // 奖励分发地址
	DPoSRewardAmount        string `json:"dpos_reward_amount" yaml:"dpos_reward_amount"`                 // 每个epoch奖励金额
	DPoSProposalVotePeriod  string `json:"dpos_proposal_vote_period" yaml:"dpos_proposal_vote_period"`   // 提案表决周期
	DPoSProposalValidPeriod string `json:"dpos_proposal_valid_period" yaml:"dpos_proposal_valid_period"` // 提案有效期
	BlockTimeSeconds        uint64 `json:"block_time_s" yaml:"block_time_s"`                             // 区块间隔时间（秒）

	// DPoS佣金配置
	DPoSCommissionRatio     uint64 `json:"dpos_commission_ratio" yaml:"dpos_commission_ratio"`         // 默认佣金率（基点），验证者未设置时使用
	DPoSCommissionEffective string `json:"dpos_commission_effective" yaml:"dpos_commission_effective"` // 佣金生效周期（如"21d"）

	// 冻结相关配置
	DPoSMinFreezePeriod    uint64 `json:"dpos_min_freeze_period" yaml:"dpos_min_freeze_period"`       // 最小冻结期（秒）
	DPoSUnfreezeLockPeriod uint64 `json:"dpos_unfreeze_lock_period" yaml:"dpos_unfreeze_lock_period"` // 解冻锁定期（秒）

	// 削减相关配置
	DPoSMissedBlocksPercentage uint64 `json:"dpos_missed_blocks_percentage" yaml:"dpos_missed_blocks_percentage"`   // 漏块率阈值（基点）
	DPoSMinorOffenseSlashRate  uint64 `json:"dpos_minor_offense_slash_rate" yaml:"dpos_minor_offense_slash_rate"`   // 轻度违规削减率（基点）
	DPoSSevereOffenseSlashRate uint64 `json:"dpos_severe_offense_slash_rate" yaml:"dpos_severe_offense_slash_rate"` // 严重违规削减率（基点）

	// DPoS 奖励与启动引导配置（扩展字段）
	// voter_target_apy: 投票者目标年化（基点，500=5%）
	VoterTargetAPYBps uint64 `json:"voter_target_apy" yaml:"voter_target_apy"`
	// block_producer_reward_per_block: 节点出块奖励 wei/块（独立于质押池）
	BlockProducerRewardPerBlock string `json:"block_producer_reward_per_block" yaml:"block_producer_reward_per_block"`
	// producer_reward_activation_epoch: 节点出块奖励激活 epoch（0=配置后立即生效）
	ProducerRewardActivationEpoch uint64 `json:"producer_reward_activation_epoch" yaml:"producer_reward_activation_epoch"`
	// vote_lock_activation_epoch: 投票余额账内锁定激活 epoch（0=未启用）
	VoteLockActivationEpoch uint64 `json:"vote_lock_activation_epoch" yaml:"vote_lock_activation_epoch"`
	// dpos_bootstrap_rpc: 启动时通过 JSON-RPC eth_call 查询 staking 合约 validators() 的端点
	DPoSBootstrapRPC string `json:"dpos_bootstrap_rpc" yaml:"dpos_bootstrap_rpc"`
	// dpos_disable_double_sign_slashing: 为 true 时关闭双签检测、削减及故障落库（默认 false，可不写）
	DPoSDisableDoubleSignSlashing bool `json:"dpos_disable_double_sign_slashing" yaml:"dpos_disable_double_sign_slashing"`
	// dpos_wall_clock_slot_alignment: 块头时间戳对齐墙钟 slot（默认 true，可不写；显式 false 可关闭）
	DPoSWallClockSlotAlignment bool `json:"dpos_wall_clock_slot_alignment" yaml:"dpos_wall_clock_slot_alignment"`
	// dpos_relax_header_timestamp_order: 为 true 时 sync 可接受子块时间戳<=父块（默认 false，仅临时消化坏块）
	DPoSRelaxHeaderTimestampOrder bool `json:"dpos_relax_header_timestamp_order" yaml:"dpos_relax_header_timestamp_order"`

	// London Fork 配置（从 yaml 读取，不改变 genesis hash）
	BaseFeeConfig string `json:"base_fee_config" yaml:"base_fee_config"` // 格式: "baseFee:baseFeeEM:baseFeeChangeDenom"
	BurnContract  string `json:"burn_contract" yaml:"burn_contract"`     // 格式: "blockNumber:address[:destinationAddress]"
}

// Telemetry holds the config details for metric services.
type Telemetry struct {
	PrometheusAddr string `json:"prometheus_addr" yaml:"prometheus_addr"`
}

// Network defines the network configuration params
type Network struct {
	NoDiscover         bool   `json:"no_discover" yaml:"no_discover"`
	Libp2pAddr         string `json:"libp2p_addr" yaml:"libp2p_addr"`
	NatAddr            string `json:"nat_addr" yaml:"nat_addr"`
	DNSAddr            string `json:"dns_addr" yaml:"dns_addr"`
	MaxPeers           int64  `json:"max_peers,omitempty" yaml:"max_peers,omitempty"`
	MaxOutboundPeers   int64  `json:"max_outbound_peers,omitempty" yaml:"max_outbound_peers,omitempty"`
	MaxInboundPeers    int64  `json:"max_inbound_peers,omitempty" yaml:"max_inbound_peers,omitempty"`
	MaxMessageHandlers int64  `json:"max_message_handlers,omitempty" yaml:"max_message_handlers,omitempty"` // 每个 topic 最大并发消息处理数，默认 50
}

// TxPool defines the TxPool configuration params
type TxPool struct {
	PriceLimit         uint64 `json:"price_limit" yaml:"price_limit"`
	MaxSlots           uint64 `json:"max_slots" yaml:"max_slots"`
	MaxAccountEnqueued uint64 `json:"max_account_enqueued" yaml:"max_account_enqueued"`
}

// Headers defines the HTTP response headers required to enable CORS.
type Headers struct {
	AccessControlAllowOrigins []string `json:"access_control_allow_origins" yaml:"access_control_allow_origins"`
}

const (
	// BlockTimeMultiplierForTimeout Multiplier to get IBFT timeout from block time
	// timeout is calculated when IBFT timeout is not specified
	BlockTimeMultiplierForTimeout uint64 = 5

	// DefaultJSONRPCBatchRequestLimit maximum length allowed for json_rpc batch requests
	DefaultJSONRPCBatchRequestLimit uint64 = 20

	// DefaultJSONRPCBlockRangeLimit maximum block range allowed for json_rpc
	// requests with fromBlock/toBlock values (e.g. eth_getLogs)
	DefaultJSONRPCBlockRangeLimit uint64 = 1000

	// DefaultNumBlockConfirmations minimal number of child blocks required for the parent block to be considered final
	// on ethereum epoch lasts for 32 blocks. more details: https://www.alchemy.com/overviews/ethereum-commitment-levels
	DefaultNumBlockConfirmations uint64 = 64

	// DefaultConcurrentRequestsDebug specifies max number of allowed concurrent requests for debug endpoints
	DefaultConcurrentRequestsDebug uint64 = 32

	// DefaultWebSocketReadLimit specifies max size in bytes for a message read from the peer by Gorrila websocket lib.
	// If a message exceeds the limit,
	// the connection sends a close message to the peer and returns ErrReadLimit to the application.
	DefaultWebSocketReadLimit uint64 = 8192

	// DefaultMetricsInterval specifies the time interval after which Prometheus metrics will be generated.
	// A value of 0 means the metrics are disabled.
	DefaultMetricsInterval time.Duration = time.Second * 8
)

// DefaultConfig returns the default server configuration
func DefaultConfig() *Config {
	defaultNetworkConfig := network.DefaultConfig()

	return &Config{
		GenesisPath:    "./genesis.json",
		DataDir:        "",
		BlockGasTarget: "0x0", // Special value signaling the parent gas limit should be applied
		Network: &Network{
			NoDiscover:         defaultNetworkConfig.NoDiscover,
			MaxPeers:           defaultNetworkConfig.MaxPeers,
			MaxOutboundPeers:   defaultNetworkConfig.MaxOutboundPeers,
			MaxInboundPeers:    defaultNetworkConfig.MaxInboundPeers,
			MaxMessageHandlers: defaultNetworkConfig.MaxMessageHandlers,
			Libp2pAddr: fmt.Sprintf("%s:%d",
				defaultNetworkConfig.Addr.IP,
				defaultNetworkConfig.Addr.Port,
			),
		},
		Telemetry:  &Telemetry{},
		ShouldSeal: true,
		TxPool: &TxPool{
			PriceLimit:         0,
			MaxSlots:           4096,
			MaxAccountEnqueued: 128,
		},
		LogLevel:    "INFO",
		RestoreFile: "",
		Headers: &Headers{
			AccessControlAllowOrigins: []string{"*"},
		},
		LogFilePath:              "",
		JSONRPCBatchRequestLimit: DefaultJSONRPCBatchRequestLimit,
		JSONRPCBlockRangeLimit:   DefaultJSONRPCBlockRangeLimit,
		Relayer:                  false,
		NumBlockConfirmations:    DefaultNumBlockConfirmations,
		ConcurrentRequestsDebug:  DefaultConcurrentRequestsDebug,
		WebSocketReadLimit:       DefaultWebSocketReadLimit,
		MetricsInterval:          DefaultMetricsInterval,

		// DPoS验证者数量默认值
		DPoSValidatorsCount: 4, // 默认4个创世验证者

		// DPoS最小质押门槛默认值
		DPoSDelegateThreshold: "1000000000000000000000", // 默认1000 VCITY

		// 创世投票金额默认空：回退使用 dpos_delegate_threshold
		DPoSGenesisVoteAmount: "",

		// DPoS经济系统默认值
		DPoSEpochDuration:       "24h",                    // 默认24小时一个epoch
		DPoSRewardDistribution:  "",                       // 奖励分发地址，默认空，需要配置
		DPoSRewardAmount:        "1000000000000000000000", // 默认1000 VCITY
		BlockTimeSeconds:        3,                        // 默认3秒一个区块
		DPoSCommissionRatio:     1000,                     // 默认佣金 10%（验证者未设置时使用）
		DPoSCommissionEffective:    "21d", // 默认21天生效
		DPoSWallClockSlotAlignment: true,  // 块头时间戳默认对齐墙钟 slot
	}
}

// deepMergeConfig 深度合并两个配置字典
// baseConfig: 基础配置（公共配置）
// overrideConfig: 覆盖配置（节点特定配置）
// 返回：合并后的配置字典
func deepMergeConfig(baseConfig map[string]interface{}, overrideConfig map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// 先复制基础配置
	for k, v := range baseConfig {
		result[k] = v
	}

	// 再合并覆盖配置
	for k, v := range overrideConfig {
		if baseVal, exists := result[k]; exists {
			// 如果两个值都是 map，递归合并
			if baseMap, ok := baseVal.(map[string]interface{}); ok {
				if overrideMap, ok := v.(map[string]interface{}); ok {
					result[k] = deepMergeConfig(baseMap, overrideMap)
					continue
				}
			}
		}
		// 否则直接覆盖
		result[k] = v
	}

	return result
}

// resolveConfigPaths 根据传入的配置文件路径，解析出基础配置和节点特定配置的路径
// 输入：node-config-validator.yaml 的路径（如 "./node1/node-config-validator.yaml"）
// 输出：base-config.yaml 路径, node-config-validator.yaml 路径, error
func resolveConfigPaths(configPath string) (string, string, error) {
	// 获取配置文件所在目录
	configDir := filepath.Dir(configPath)
	configFileName := filepath.Base(configPath)

	// 如果传入的就是 node-config-validator.yaml
	if configFileName == "node-config-validator.yaml" {
		// 基础配置文件在 nodes 目录（上一级目录）
		nodesDir := filepath.Dir(configDir)
		baseConfigPath := filepath.Join(nodesDir, "base-config.yaml")
		return baseConfigPath, configPath, nil
	}

	// 如果传入的是其他配置文件，尝试查找 node-config-validator.yaml
	// 这种情况保持向后兼容
	nodeConfigPath := filepath.Join(configDir, "node-config-validator.yaml")
	if _, err := os.Stat(nodeConfigPath); os.IsNotExist(err) {
		// 如果 node-config-validator.yaml 不存在，只使用传入的配置文件
		return "", configPath, nil
	}

	// 查找 base-config.yaml（在 nodes 目录）
	nodesDir := filepath.Dir(configDir)
	baseConfigPath := filepath.Join(nodesDir, "base-config.yaml")

	return baseConfigPath, nodeConfigPath, nil
}

// readConfigFileAsMap 读取配置文件并解析为 map[string]interface{}
func readConfigFileAsMap(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var configMap map[string]interface{}

	switch {
	case strings.HasSuffix(path, ".hcl"):
		// HCL 格式不支持 map 转换，应该使用 readConfigFileLegacy
		return nil, fmt.Errorf("HCL format should use legacy read method")
	case strings.HasSuffix(path, ".json"):
		if err := json.Unmarshal(data, &configMap); err != nil {
			return nil, err
		}
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		if err := yaml.Unmarshal(data, &configMap); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported config file format: %s", path)
	}

	if configMap == nil {
		configMap = make(map[string]interface{})
	}

	return configMap, nil
}

// ReadConfigFile reads the config file from the specified path, builds a Config object
// and returns it. Now supports reading base-config.yaml and node-config-validator.yaml
// and merging them.
//
// Supported file types: .json, .hcl, .yaml, .yml
func ReadConfigFile(path string) (*Config, error) {
	// 如果文件是 HCL 格式，使用原来的逻辑（向后兼容，HCL 不支持合并）
	if strings.HasSuffix(path, ".hcl") {
		return readConfigFileLegacy(path)
	}

	// 1. 确定基础配置文件和节点特定配置文件路径
	baseConfigPath, nodeConfigPath, err := resolveConfigPaths(path)
	if err != nil {
		return nil, err
	}

	// 2. 读取并解析基础配置（base-config.yaml，如果存在）
	var baseConfigMap map[string]interface{}
	if baseConfigPath != "" {
		baseConfigMap, err = readConfigFileAsMap(baseConfigPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read base config: %w", err)
		}
		// 如果 base-config.yaml 不存在，baseConfigMap 为 nil，后续只使用节点配置
	}

	// 3. 读取并解析节点特定配置（node-config-validator.yaml 或传入的配置文件）
	var overrideConfigMap map[string]interface{}
	if nodeConfigPath != "" {
		overrideConfigMap, err = readConfigFileAsMap(nodeConfigPath)
		if err != nil {
			// 如果节点配置文件不存在，且没有基础配置，返回错误
			if baseConfigMap == nil {
				return nil, fmt.Errorf("failed to read config file: %w", err)
			}
			// 否则只使用基础配置
			overrideConfigMap = nil
		}
	} else {
		// 如果没有解析出节点配置路径，使用传入的路径（向后兼容）
		overrideConfigMap, err = readConfigFileAsMap(path)
		if err != nil {
			return nil, err
		}
	}

	// 4. 深度合并配置
	var mergedConfigMap map[string]interface{}
	if baseConfigMap != nil && overrideConfigMap != nil {
		mergedConfigMap = deepMergeConfig(baseConfigMap, overrideConfigMap)
	} else if baseConfigMap != nil {
		mergedConfigMap = baseConfigMap
	} else if overrideConfigMap != nil {
		mergedConfigMap = overrideConfigMap
	} else {
		return nil, fmt.Errorf("no valid config file found")
	}

	// 5. 将合并后的配置转换为 YAML 字节，然后解析为 Config 结构体
	// 这样可以复用现有的 unmarshal 逻辑
	mergedYAML, err := yaml.Marshal(mergedConfigMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged config: %w", err)
	}

	// 6. 解析为 Config 结构体
	config := DefaultConfig()
	config.Network = new(Network)
	config.Network.MaxPeers = -1
	config.Network.MaxInboundPeers = -1
	config.Network.MaxOutboundPeers = -1

	if err := yaml.Unmarshal(mergedYAML, config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal merged config: %w", err)
	}

	return config, nil
}

// readConfigFileLegacy 使用原来的逻辑读取配置文件（用于 HCL 格式和向后兼容）
func readConfigFileLegacy(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var unmarshalFunc func([]byte, interface{}) error

	switch {
	case strings.HasSuffix(path, ".hcl"):
		unmarshalFunc = hcl.Unmarshal
	case strings.HasSuffix(path, ".json"):
		unmarshalFunc = json.Unmarshal
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		unmarshalFunc = yaml.Unmarshal
	default:
		return nil, fmt.Errorf("suffix of %s is neither hcl, json, yaml nor yml", path)
	}

	config := DefaultConfig()
	config.Network = new(Network)
	config.Network.MaxPeers = -1
	config.Network.MaxInboundPeers = -1
	config.Network.MaxOutboundPeers = -1

	if err := unmarshalFunc(data, config); err != nil {
		return nil, err
	}

	return config, nil
}
