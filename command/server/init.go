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

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/network"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/server"
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
