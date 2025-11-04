package server

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"

	"github.com/Vcity-Team/vcitychain/command/server/config"

	helperCommon "github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/network/common"
	"strings"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/server"
	"github.com/Vcity-Team/vcitychain/types"
)

var (
	errDataDirectoryUndefined = errors.New("data directory not defined")
)

func (p *serverParams) initConfigFromFile() error {
	var parseErr error

	if p.rawConfig, parseErr = config.ReadConfigFile(p.configPath); parseErr != nil {
		return parseErr
	}

	return nil
}

func (p *serverParams) initRawParams() error {
	if err := p.initBlockGasTarget(); err != nil {
		return err
	}

	if err := p.initSecretsConfig(); err != nil {
		return err
	}

	// 🆕 解析 London Fork 配置（必须在 initGenesisConfig 之前）
	if err := p.initLondonForkConfig(); err != nil {
		return err
	}

	if err := p.initGenesisConfig(); err != nil {
		return err
	}

	if err := p.initDataDirLocation(); err != nil {
		return err
	}

	if p.isDevMode {
		p.initDevMode()
	}

	p.initPeerLimits()
	p.initLogFileLocation()
	p.initConsensusSwitchHeight()
	p.initDPoSDelegateThreshold()
	p.initDPoSConfig()

	p.relayer = p.rawConfig.Relayer

	return p.initAddresses()
}

func (p *serverParams) initDataDirLocation() error {
	if p.rawConfig.DataDir == "" {
		return errDataDirectoryUndefined
	}

	return nil
}

func (p *serverParams) initLogFileLocation() {
	if p.isLogFileLocationSet() {
		p.logFileLocation = p.rawConfig.LogFilePath
	}
}

// 🆕 新增：初始化共识切换高度
func (p *serverParams) initConsensusSwitchHeight() {
	// 如果命令行参数设置了共识切换高度，则使用该值
	// 如果为0，表示不进行共识切换
	// 不设置默认值，保持用户的选择
}

// 🆕 新增：初始化DPoS最小质押门槛
func (p *serverParams) initDPoSDelegateThreshold() {
	// 从配置文件读取DPoS最小质押门槛
	if p.rawConfig.DPoSDelegateThreshold != "" {
		if threshold, ok := new(big.Int).SetString(p.rawConfig.DPoSDelegateThreshold, 10); ok {
			p.dposDelegateThreshold = threshold
		} else {
			// 如果解析失败，使用默认值
			p.dposDelegateThreshold, _ = new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
		}
	} else {
		// 如果配置为空，使用默认值
		p.dposDelegateThreshold, _ = new(big.Int).SetString("1000000000000000000000", 10) // 1000 VCITY
	}
}

// 🆕 新增：初始化DPoS配置
func (p *serverParams) initDPoSConfig() {
	// 初始化DPoS验证者数量
	p.dposValidatorsCount = p.rawConfig.DPoSValidatorsCount
	if p.dposValidatorsCount == 0 {
		p.dposValidatorsCount = 5 // 默认值
	}

	// 初始化备用验证者数量
	p.backupValidatorsCount = p.rawConfig.BackupValidatorsCount
	if p.backupValidatorsCount == 0 {
		p.backupValidatorsCount = 10 // 默认值
	}

	// 初始化最大漏块数
	p.maxMissedBlocks = p.rawConfig.MaxMissedBlocks
	if p.maxMissedBlocks == 0 {
		p.maxMissedBlocks = 3 // 默认值
	}

	// 初始化提案表决周期
	p.dposProposalVotePeriod = p.rawConfig.DPoSProposalVotePeriod
	if p.dposProposalVotePeriod == "" {
		p.dposProposalVotePeriod = "24h" // 默认24小时
		fmt.Printf("⚠️ DPoSProposalVotePeriod配置为空，使用默认值: %s\n", p.dposProposalVotePeriod)
	} else {
		fmt.Printf("✅ 读取到DPoSProposalVotePeriod配置: %s\n", p.dposProposalVotePeriod)
	}

	// 初始化提案有效期
	p.dposProposalValidPeriod = p.rawConfig.DPoSProposalValidPeriod
	if p.dposProposalValidPeriod == "" {
		p.dposProposalValidPeriod = "7d" // 默认7天
		fmt.Printf("⚠️ DPoSProposalValidPeriod配置为空，使用默认值: %s\n", p.dposProposalValidPeriod)
	} else {
		fmt.Printf("✅ 读取到DPoSProposalValidPeriod配置: %s\n", p.dposProposalValidPeriod)
	}
}

func (p *serverParams) initBlockGasTarget() error {
	var parseErr error

	if p.blockGasTarget, parseErr = helperCommon.ParseUint64orHex(
		&p.rawConfig.BlockGasTarget,
	); parseErr != nil {
		return parseErr
	}

	return nil
}

func (p *serverParams) initSecretsConfig() error {
	if !p.isSecretsConfigPathSet() {
		return nil
	}

	var parseErr error

	if p.secretsConfig, parseErr = secrets.ReadConfig(
		p.rawConfig.SecretsConfigPath,
	); parseErr != nil {
		return fmt.Errorf("unable to read secrets config file, %w", parseErr)
	}

	return nil
}

// 🆕 initLondonForkConfig 解析 London Fork 配置（BaseFee 和 BurnContract）
func (p *serverParams) initLondonForkConfig() error {
	// 解析 BaseFee 配置
	if p.rawConfig.BaseFeeConfig != "" {
		baseFeeInfo, err := parseBaseFeeConfig(p.rawConfig.BaseFeeConfig)
		if err != nil {
			return fmt.Errorf("failed to parse base fee config: %w", err)
		}
		p.parsedBaseFee = baseFeeInfo
	}

	// 解析 BurnContract 配置
	if p.rawConfig.BurnContract != "" {
		burnContractInfo, err := parseBurnContractConfig(p.rawConfig.BurnContract)
		if err != nil {
			return fmt.Errorf("failed to parse burn contract config: %w", err)
		}
		p.parsedBurnContract = burnContractInfo
	}

	return nil
}

// parseBaseFeeConfig 解析 BaseFee 配置字符串
func parseBaseFeeConfig(baseFeeConfigRaw string) (*baseFeeInfo, error) {
	// 默认值（参考 command/genesis/utils.go）
	const defaultBaseFee = 1000000000            // 1 Gwei
	const defaultBaseFeeEM = 2
	const defaultBaseFeeChangeDenom = 8

	baseFeeInfo := &baseFeeInfo{
		baseFee:            defaultBaseFee,
		baseFeeEM:          defaultBaseFeeEM,
		baseFeeChangeDenom: defaultBaseFeeChangeDenom,
	}

	baseFeeConfig := strings.Split(baseFeeConfigRaw, ":")
	if len(baseFeeConfig) > 3 {
		return nil, fmt.Errorf("invalid number of arguments for base fee configuration")
	}

	if len(baseFeeConfig) >= 1 && baseFeeConfig[0] != "" {
		baseFee, err := helperCommon.ParseUint64orHex(&baseFeeConfig[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse baseFee: %w", err)
		}
		baseFeeInfo.baseFee = baseFee
	}

	if len(baseFeeConfig) >= 2 && baseFeeConfig[1] != "" {
		baseFeeEM, err := helperCommon.ParseUint64orHex(&baseFeeConfig[1])
		if err != nil {
			return nil, fmt.Errorf("failed to parse baseFeeEM: %w", err)
		}
		baseFeeInfo.baseFeeEM = baseFeeEM
	}

	if len(baseFeeConfig) == 3 && baseFeeConfig[2] != "" {
		baseFeeChangeDenom, err := helperCommon.ParseUint64orHex(&baseFeeConfig[2])
		if err != nil {
			return nil, fmt.Errorf("failed to parse baseFeeChangeDenom: %w", err)
		}
		baseFeeInfo.baseFeeChangeDenom = baseFeeChangeDenom
	}

	return baseFeeInfo, nil
}

// parseBurnContractConfig 解析 BurnContract 配置字符串
func parseBurnContractConfig(burnContractInfoRaw string) (*burnContractInfo, error) {
	// 格式: <block>:<address>[:<burn destination address>]
	burnContractParts := strings.Split(burnContractInfoRaw, ":")
	if len(burnContractParts) < 2 || len(burnContractParts) > 3 {
		return nil, fmt.Errorf("expected format: <block>:<address>[:<burn destination>]")
	}

	blockRaw := burnContractParts[0]
	blockNum, err := helperCommon.ParseUint64orHex(&blockRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block number %s: %w", blockRaw, err)
	}

	contractAddress := burnContractParts[1]
	if err := types.IsValidAddress(contractAddress); err != nil {
		return nil, fmt.Errorf("failed to parse contract address %s: %w", contractAddress, err)
	}

	info := &burnContractInfo{
		blockNumber:        blockNum,
		address:            types.StringToAddress(contractAddress),
		destinationAddress: types.ZeroAddress,
	}

	if len(burnContractParts) == 3 {
		destinationAddress := burnContractParts[2]
		if err := types.IsValidAddress(destinationAddress); err != nil {
			return nil, fmt.Errorf("failed to parse burn destination address %s: %w", destinationAddress, err)
		}
		info.destinationAddress = types.StringToAddress(destinationAddress)
	}

	return info, nil
}

func (p *serverParams) initGenesisConfig() error {
	var parseErr error

	if p.genesisConfig, parseErr = chain.Import(
		p.rawConfig.GenesisPath,
	); parseErr != nil {
		return parseErr
	}

	// if block-gas-target flag is set override genesis.json value
	if p.blockGasTarget != 0 {
		p.genesisConfig.Params.BlockGasTarget = p.blockGasTarget
	}

	// 🆕 如果 yaml 配置了 BaseFee，则覆盖 genesis.json 中的值（不影响 genesis hash）
	if p.parsedBaseFee != nil {
		p.genesisConfig.Genesis.BaseFee = p.parsedBaseFee.baseFee
		p.genesisConfig.Genesis.BaseFeeEM = p.parsedBaseFee.baseFeeEM
		p.genesisConfig.Genesis.BaseFeeChangeDenom = p.parsedBaseFee.baseFeeChangeDenom
	}

	// 🆕 如果 yaml 配置了 BurnContract，则覆盖 genesis.json 中的值（不影响 genesis hash）
	if p.parsedBurnContract != nil {
		if p.genesisConfig.Params.BurnContract == nil {
			p.genesisConfig.Params.BurnContract = make(map[uint64]types.Address)
		}
		p.genesisConfig.Params.BurnContract[p.parsedBurnContract.blockNumber] = p.parsedBurnContract.address
		p.genesisConfig.Params.BurnContractDestinationAddress = p.parsedBurnContract.destinationAddress
	}

	return nil
}

func (p *serverParams) initDevMode() {
	// Dev mode:
	// - disables peer discovery
	// - enables all forks
	p.rawConfig.Network.NoDiscover = true
	p.genesisConfig.Params.Forks = chain.AllForksEnabled

	p.initDevConsensusConfig()
}

func (p *serverParams) initDevConsensusConfig() {
	if !p.isDevConsensus() {
		return
	}

	p.genesisConfig.Params.Engine = map[string]interface{}{
		string(server.DevConsensus): map[string]interface{}{
			"interval": p.devInterval,
		},
	}
}

func (p *serverParams) initPeerLimits() {
	if !p.isMaxPeersSet() && !p.isPeerRangeSet() {
		// No peer limits specified, use the default limits
		p.initDefaultPeerLimits()

		return
	}

	if p.isPeerRangeSet() {
		// Some part of the peer range is specified
		p.initUsingPeerRange()

		return
	}

	if p.isMaxPeersSet() {
		// The max peer value is specified, derive precise limits
		p.initUsingMaxPeers()

		return
	}
}

func (p *serverParams) initDefaultPeerLimits() {
	defaultNetworkConfig := network.DefaultConfig()

	p.rawConfig.Network.MaxPeers = defaultNetworkConfig.MaxPeers
	p.rawConfig.Network.MaxInboundPeers = defaultNetworkConfig.MaxInboundPeers
	p.rawConfig.Network.MaxOutboundPeers = defaultNetworkConfig.MaxOutboundPeers
}

func (p *serverParams) initUsingPeerRange() {
	defaultConfig := network.DefaultConfig()

	if p.rawConfig.Network.MaxInboundPeers == unsetPeersValue {
		p.rawConfig.Network.MaxInboundPeers = defaultConfig.MaxInboundPeers
	}

	if p.rawConfig.Network.MaxOutboundPeers == unsetPeersValue {
		p.rawConfig.Network.MaxOutboundPeers = defaultConfig.MaxOutboundPeers
	}

	p.rawConfig.Network.MaxPeers = p.rawConfig.Network.MaxInboundPeers + p.rawConfig.Network.MaxOutboundPeers
}

func (p *serverParams) initUsingMaxPeers() {
	p.rawConfig.Network.MaxOutboundPeers = int64(
		math.Floor(
			float64(p.rawConfig.Network.MaxPeers) * network.DefaultDialRatio,
		),
	)
	// MaxPeers is expected to be greater than MaxOutboundPeers as long as DefaultDialRatio is less than 0
	if p.rawConfig.Network.MaxPeers > p.rawConfig.Network.MaxOutboundPeers {
		p.rawConfig.Network.MaxInboundPeers = p.rawConfig.Network.MaxPeers - p.rawConfig.Network.MaxOutboundPeers
	}
}

func (p *serverParams) initAddresses() error {
	if err := p.initPrometheusAddress(); err != nil {
		return err
	}

	if err := p.initLibp2pAddress(); err != nil {
		return err
	}

	if err := p.initNATAddress(); err != nil {
		return err
	}

	if err := p.initDNSAddress(); err != nil {
		return err
	}

	if err := p.initJSONRPCAddress(); err != nil {
		return err
	}

	return p.initGRPCAddress()
}

func (p *serverParams) initPrometheusAddress() error {
	if !p.isPrometheusAddressSet() {
		return nil
	}

	var parseErr error

	if p.prometheusAddress, parseErr = helper.ResolveAddr(
		p.rawConfig.Telemetry.PrometheusAddr,
		helper.AllInterfacesBinding,
	); parseErr != nil {
		return parseErr
	}

	return nil
}

func (p *serverParams) initLibp2pAddress() error {
	var parseErr error

	if p.libp2pAddress, parseErr = helper.ResolveAddr(
		p.rawConfig.Network.Libp2pAddr,
		helper.LocalHostBinding,
	); parseErr != nil {
		return parseErr
	}

	return nil
}

func (p *serverParams) initNATAddress() error {
	if !p.isNATAddressSet() {
		return nil
	}

	if p.natAddress = net.ParseIP(
		p.rawConfig.Network.NatAddr,
	); p.natAddress == nil {
		return errInvalidNATAddress
	}

	return nil
}

func (p *serverParams) initDNSAddress() error {
	if !p.isDNSAddressSet() {
		return nil
	}

	var parseErr error

	if p.dnsAddress, parseErr = common.MultiAddrFromDNS(
		p.rawConfig.Network.DNSAddr, p.libp2pAddress.Port,
	); parseErr != nil {
		return parseErr
	}

	return nil
}

func (p *serverParams) initJSONRPCAddress() error {
	var parseErr error

	if p.jsonRPCAddress, parseErr = helper.ResolveAddr(
		p.rawConfig.JSONRPCAddr,
		helper.AllInterfacesBinding,
	); parseErr != nil {
		return parseErr
	}

	return nil
}

func (p *serverParams) initGRPCAddress() error {
	var parseErr error

	if p.grpcAddress, parseErr = helper.ResolveAddr(
		p.rawConfig.GRPCAddr,
		helper.LocalHostBinding,
	); parseErr != nil {
		return parseErr
	}

	return nil
}
