package genesis

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/command"
	"github.com/Vcity-Team/vcitychain/command/helper"
	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/consensus/ibft"
	"github.com/Vcity-Team/vcitychain/consensus/ibft/fork"
	"github.com/Vcity-Team/vcitychain/consensus/ibft/signer"
	"github.com/Vcity-Team/vcitychain/consensus/polybft"
	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/contracts/staking"
	stakingHelper "github.com/Vcity-Team/vcitychain/helper/staking"
	"github.com/Vcity-Team/vcitychain/server"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const (
	dirFlag                      = "dir"
	nameFlag                     = "name"
	premineFlag                  = "premine"
	chainIDFlag                  = "chain-id"
	epochSizeFlag                = "epoch-size"
	epochRewardFlag              = "epoch-reward"
	blockGasLimitFlag            = "block-gas-limit"
	burnContractFlag             = "burn-contract"
	genesisBaseFeeConfigFlag     = "base-fee-config"
	posFlag                      = "pos"
	nativeTokenConfigFlag        = "native-token-config"
	rewardTokenCodeFlag          = "reward-token-code"
	rewardWalletFlag             = "reward-wallet"
	blockTrackerPollIntervalFlag = "block-tracker-poll-interval"
	proxyContractsAdminFlag      = "proxy-contracts-admin"
)

// Legacy flags that need to be preserved for running clients
const (
	chainIDFlagLEGACY = "chainid"
)

var (
	params = &genesisParams{}
)

var (
	errValidatorsNotSpecified   = errors.New("validator information not specified")
	errUnsupportedConsensus     = errors.New("specified consensusRaw not supported")
	errInvalidEpochSize         = errors.New("epoch size must be greater than 1")
	errRewardWalletAmountZero   = errors.New("reward wallet amount can not be zero or negative")
	errReserveAccMustBePremined = errors.New("it is mandatory to premine reserve account (0x0 address)")
	errBlockTrackerPollInterval = errors.New("block tracker poll interval must be greater than 0")
	errBaseFeeChangeDenomZero   = errors.New("base fee change denominator must be greater than 0")
	errBaseFeeEMZero            = errors.New("base fee elasticity multiplier must be greater than 0")
	errBaseFeeZero              = errors.New("base fee  must be greater than 0")
	errRewardWalletNotDefined   = errors.New("reward wallet address must be defined")
	errRewardTokenOnNonMintable = errors.New("a custom reward token must be defined when " +
		"native ERC20 token is non-mintable")
	errRewardWalletZero = errors.New("reward wallet address must not be zero address")
)

type genesisParams struct {
	genesisPath  string
	name         string
	consensusRaw string
	premine      []string
	bootnodes    []string

	chainID   uint64
	epochSize uint64

	blockGasLimit uint64

	burnContract        string
	baseFeeConfig       string
	parsedBaseFeeConfig *baseFeeInfo

	// PoS
	isPos                bool
	minNumValidators     uint64
	maxNumValidators     uint64
	validatorsPath       string
	validatorsPrefixPath string
	validators           []string

	// IBFT
	rawIBFTValidatorType string
	ibftValidatorType    validators.ValidatorType
	ibftValidators       validators.Validators

	extraData []byte
	consensus server.ConsensusType

	consensusEngineConfig map[string]interface{}

	genesisConfig *chain.Chain

	// PolyBFT
	sprintSize     uint64
	blockTime      time.Duration
	epochReward    uint64
	blockTimeDrift uint64

	initialStateRoot string

	// access lists
	contractDeployerAllowListAdmin   []string
	contractDeployerAllowListEnabled []string
	contractDeployerBlockListAdmin   []string
	contractDeployerBlockListEnabled []string
	transactionsAllowListAdmin       []string
	transactionsAllowListEnabled     []string
	transactionsBlockListAdmin       []string
	transactionsBlockListEnabled     []string
	bridgeAllowListAdmin             []string
	bridgeAllowListEnabled           []string
	bridgeBlockListAdmin             []string
	bridgeBlockListEnabled           []string

	nativeTokenConfigRaw string
	nativeTokenConfig    *polybft.TokenConfig

	premineInfos []*helper.PremineInfo

	// rewards
	rewardTokenCode string
	rewardWallet    string

	blockTrackerPollInterval time.Duration

	proxyContractsAdmin string
}

func (p *genesisParams) validateFlags() error {
	// Check if the consensusRaw is supported
	if !server.ConsensusSupported(p.consensusRaw) {
		return errUnsupportedConsensus
	}

	if err := p.validateGenesisBaseFeeConfig(); err != nil {
		return err
	}

	// Check if validator information is set at all
	if p.isIBFTConsensus() &&
		!p.areValidatorsSetManually() &&
		!p.areValidatorsSetByPrefix() {
		return errValidatorsNotSpecified
	}

	// For DPoS consensus, validators are optional but recommended
	if p.isDPoSConsensus() && !p.areValidatorsSetByPrefix() {
		// Log a warning but don't fail - DPoS can work without initial validators
		// They can be added later through governance
	}

	if err := p.parsePremineInfo(); err != nil {
		return err
	}

	if p.isPolyBFTConsensus() {
		if err := p.extractNativeTokenMetadata(); err != nil {
			return err
		}

		if err := p.validateBurnContract(); err != nil {
			return err
		}

		if err := p.validateRewardWalletAndToken(); err != nil {
			return err
		}

		if err := p.validatePremineInfo(); err != nil {
			return err
		}

		if err := p.validateProxyContractsAdmin(); err != nil {
			return err
		}
	}

	// Check if the genesis file already exists
	if generateError := verifyGenesisExistence(p.genesisPath); generateError != nil {
		return errors.New(generateError.GetMessage())
	}

	// Check that the epoch size is correct
	if p.epochSize < 2 && (p.isIBFTConsensus() || p.isPolyBFTConsensus() || p.isDPoSConsensus()) {
		// Epoch size must be greater than 1, so new transactions have a chance to be added to a block.
		// Otherwise, every block would be an endblock (meaning it will not have any transactions).
		// Check is placed here to avoid additional parsing if epochSize < 2
		return errInvalidEpochSize
	}

	// Validate validatorsPath only if validators information were not provided via CLI flag
	// Skip this validation for DPoS consensus as it doesn't require validators path
	if len(p.validators) == 0 && !p.isDPoSConsensus() {
		if _, err := os.Stat(p.validatorsPath); err != nil {
			return fmt.Errorf("invalid validators path ('%s') provided. Error: %w", p.validatorsPath, err)
		}
	}

	// Validate min and max validators number
	return command.ValidateMinMaxValidatorsNumber(p.minNumValidators, p.maxNumValidators)
}

func (p *genesisParams) isIBFTConsensus() bool {
	return server.ConsensusType(p.consensusRaw) == server.IBFTConsensus
}

func (p *genesisParams) isPolyBFTConsensus() bool {
	return server.ConsensusType(p.consensusRaw) == server.PolyBFTConsensus
}

func (p *genesisParams) isDPoSConsensus() bool {
	return server.ConsensusType(p.consensusRaw) == server.DPoSConsensus
}

func (p *genesisParams) areValidatorsSetManually() bool {
	return len(p.validators) != 0
}

func (p *genesisParams) areValidatorsSetByPrefix() bool {
	return p.validatorsPrefixPath != ""
}

func (p *genesisParams) getRequiredFlags() []string {
	if p.isIBFTConsensus() {
		return []string{
			command.BootnodeFlag,
		}
	}

	return []string{}
}

func (p *genesisParams) initRawParams() error {
	p.consensus = server.ConsensusType(p.consensusRaw)

	if p.consensus == server.PolyBFTConsensus {
		return nil
	}

	// For DPoS consensus, we don't need IBFT validator type initialization
	if p.consensus == server.DPoSConsensus {
		p.initConsensusEngineConfig()
		return nil
	}

	if err := p.initIBFTValidatorType(); err != nil {
		return err
	}

	if err := p.initValidatorSet(); err != nil {
		return err
	}

	p.initIBFTExtraData()
	p.initConsensusEngineConfig()

	return nil
}

// setValidatorSetFromCli sets validator set from cli command
func (p *genesisParams) setValidatorSetFromCli() error {
	if len(p.validators) == 0 {
		return nil
	}

	newValidators, err := validators.ParseValidators(p.ibftValidatorType, p.validators)
	if err != nil {
		return err
	}

	if err = p.ibftValidators.Merge(newValidators); err != nil {
		return err
	}

	return nil
}

// setValidatorSetFromPrefixPath sets validator set from prefix path
func (p *genesisParams) setValidatorSetFromPrefixPath() error {
	if !p.areValidatorsSetByPrefix() {
		return nil
	}

	validators, err := command.GetValidatorsFromPrefixPath(
		p.validatorsPath,
		p.validatorsPrefixPath,
		p.ibftValidatorType,
	)

	if err != nil {
		return fmt.Errorf("failed to read from prefix: %w", err)
	}

	if err := p.ibftValidators.Merge(validators); err != nil {
		return err
	}

	return nil
}

func (p *genesisParams) initIBFTValidatorType() error {
	var err error

	// For DPoS consensus, default to BLS validator type if not specified
	if p.isDPoSConsensus() && p.rawIBFTValidatorType == "" {
		p.rawIBFTValidatorType = validators.BLSValidatorType.String()
	}

	if p.ibftValidatorType, err = validators.ParseValidatorType(p.rawIBFTValidatorType); err != nil {
		return err
	}

	return nil
}

func (p *genesisParams) initValidatorSet() error {
	p.ibftValidators = validators.NewValidatorSetFromType(p.ibftValidatorType)

	// Set validator set
	// Priority goes to cli command over prefix path
	if err := p.setValidatorSetFromPrefixPath(); err != nil {
		return err
	}

	if err := p.setValidatorSetFromCli(); err != nil {
		return err
	}

	// Validate if validator number exceeds max number
	if ok := p.isValidatorNumberValid(); !ok {
		return command.ErrValidatorNumberExceedsMax
	}

	return nil
}

func (p *genesisParams) isValidatorNumberValid() bool {
	return p.ibftValidators == nil || uint64(p.ibftValidators.Len()) <= p.maxNumValidators
}

func (p *genesisParams) initIBFTExtraData() {
	if p.consensus != server.IBFTConsensus {
		return
	}

	var committedSeal signer.Seals

	switch p.ibftValidatorType {
	case validators.ECDSAValidatorType:
		committedSeal = new(signer.SerializedSeal)
	case validators.BLSValidatorType:
		committedSeal = new(signer.AggregatedSeal)
	}

	ibftExtra := &signer.IstanbulExtra{
		Validators:     p.ibftValidators,
		ProposerSeal:   []byte{},
		CommittedSeals: committedSeal,
	}

	p.extraData = make([]byte, signer.IstanbulExtraVanity)
	p.extraData = ibftExtra.MarshalRLPTo(p.extraData)
}

func (p *genesisParams) initConsensusEngineConfig() {
	if p.consensus == server.DPoSConsensus {
		p.consensusEngineConfig = map[string]interface{}{
			string(server.DPoSConsensus): map[string]interface{}{
				"blockTime":     p.blockTime,
				"epochSize":     p.epochSize,
				"delegateCount": 21, // Default delegate count for DPoS
			},
		}
		return
	}

	if p.consensus != server.IBFTConsensus {
		p.consensusEngineConfig = map[string]interface{}{
			p.consensusRaw: map[string]interface{}{},
		}

		return
	}

	if p.isPos {
		p.initIBFTEngineMap(fork.PoS)

		return
	}

	p.initIBFTEngineMap(fork.PoA)
}

func (p *genesisParams) initIBFTEngineMap(ibftType fork.IBFTType) {
	p.consensusEngineConfig = map[string]interface{}{
		string(server.IBFTConsensus): map[string]interface{}{
			fork.KeyType:          ibftType,
			fork.KeyValidatorType: p.ibftValidatorType,
			fork.KeyBlockTime:     p.blockTime,
			ibft.KeyEpochSize:     p.epochSize,
		},
	}
}

func (p *genesisParams) generateGenesis() error {
	if err := p.initGenesisConfig(); err != nil {
		return err
	}

	if err := helper.WriteGenesisConfigToDisk(
		p.genesisConfig,
		p.genesisPath,
	); err != nil {
		return err
	}

	return nil
}

func (p *genesisParams) initGenesisConfig() error {
	// Disable london hardfork if burn contract address is not provided
	enabledForks := chain.AllForksEnabled
	if !p.isBurnContractEnabled() {
		enabledForks.RemoveFork(chain.London)
	}

	chainConfig := &chain.Chain{
		Name: p.name,
		Genesis: &chain.Genesis{
			GasLimit:   p.blockGasLimit,
			Difficulty: 1,
			Alloc:      map[types.Address]*chain.GenesisAccount{},
			ExtraData:  p.extraData,
			GasUsed:    command.DefaultGenesisGasUsed,
		},
		Params: &chain.Params{
			ChainID: int64(p.chainID),
			Forks:   enabledForks,
			Engine:  p.consensusEngineConfig,
		},
		Bootnodes: p.bootnodes,
	}

	// burn contract can be set only for non mintable native token
	if p.isBurnContractEnabled() {
		chainConfig.Genesis.BaseFee = p.parsedBaseFeeConfig.baseFee
		chainConfig.Genesis.BaseFeeEM = p.parsedBaseFeeConfig.baseFeeEM
		chainConfig.Genesis.BaseFeeChangeDenom = p.parsedBaseFeeConfig.baseFeeChangeDenom
		chainConfig.Params.BurnContract = make(map[uint64]types.Address, 1)

		burnContractInfo, err := parseBurnContractInfo(p.burnContract)
		if err != nil {
			return err
		}

		chainConfig.Params.BurnContract[burnContractInfo.BlockNumber] = burnContractInfo.Address
		chainConfig.Params.BurnContractDestinationAddress = burnContractInfo.DestinationAddress
	}

	// Predeploy staking smart contract if needed
	if p.shouldPredeployStakingSC() {
		stakingAccount, err := p.predeployStakingSC()
		if err != nil {
			return err
		}

		chainConfig.Genesis.Alloc[staking.AddrStakingContract] = stakingAccount
	}

	for _, premineInfo := range p.premineInfos {
		chainConfig.Genesis.Alloc[premineInfo.Address] = &chain.GenesisAccount{
			Balance: premineInfo.Amount,
		}
	}

	// IBFT / Dev / Dummy genesis: apply transactions block list (and related ACLs)
	// so --transactions-block-list-admin works outside the PolyBFT path too.
	p.applyTransactionsBlockListConfig(chainConfig)

	p.genesisConfig = chainConfig

	return nil
}

// applyTransactionsBlockListConfig wires TransactionsBlockList params (+ fork at 0)
// when --transactions-block-list-admin is set. Live IBFT→DPoS networks that enable
// the list later should instead patch genesis forks.transactionsBlockList.block = H
// with H > 0 (roles are injected at H; genesis allocs are skipped).
func (p *genesisParams) applyTransactionsBlockListConfig(chainConfig *chain.Chain) {
	if chainConfig == nil || chainConfig.Params == nil {
		return
	}

	if len(p.transactionsBlockListAdmin) == 0 {
		return
	}

	chainConfig.Params.TransactionsBlockList = &chain.AddressListConfig{
		AdminAddresses:   stringSliceToAddressSlice(p.transactionsBlockListAdmin),
		EnabledAddresses: stringSliceToAddressSlice(p.transactionsBlockListEnabled),
	}

	if chainConfig.Params.Forks != nil {
		if _, exists := (*chainConfig.Params.Forks)[chain.TransactionsBlockList]; !exists {
			chainConfig.Params.Forks.SetFork(chain.TransactionsBlockList, chain.NewFork(0))
		}
	}
}

func (p *genesisParams) shouldPredeployStakingSC() bool {
	// If the consensus selected is IBFT / Dev and the mechanism is Proof of Stake,
	// deploy the Staking SC
	return p.isPos && (p.consensus == server.IBFTConsensus || p.consensus == server.DevConsensus)
}

func (p *genesisParams) predeployStakingSC() (*chain.GenesisAccount, error) {
	stakingAccount, predeployErr := stakingHelper.PredeployStakingSC(
		p.ibftValidators,
		stakingHelper.PredeployParams{
			MinValidatorCount: p.minNumValidators,
			MaxValidatorCount: p.maxNumValidators,
		})
	if predeployErr != nil {
		return nil, predeployErr
	}

	return stakingAccount, nil
}

// validateRewardWalletAndToken validates reward wallet flag
func (p *genesisParams) validateRewardWalletAndToken() error {
	if p.rewardWallet == "" {
		return errRewardWalletNotDefined
	}

	if !p.nativeTokenConfig.IsMintable && p.rewardTokenCode == "" {
		return errRewardTokenOnNonMintable
	}

	premineInfo, err := helper.ParsePremineInfo(p.rewardWallet)
	if err != nil {
		return err
	}

	if premineInfo.Address == types.ZeroAddress {
		return errRewardWalletZero
	}

	// If epoch rewards are enabled, reward wallet must have some amount of premine
	if p.epochReward > 0 && premineInfo.Amount.Cmp(big.NewInt(0)) < 1 {
		return errRewardWalletAmountZero
	}

	return nil
}

// parsePremineInfo parses premine flag
func (p *genesisParams) parsePremineInfo() error {
	p.premineInfos = make([]*helper.PremineInfo, 0, len(p.premine))

	for _, premine := range p.premine {
		premineInfo, err := helper.ParsePremineInfo(premine)
		if err != nil {
			return fmt.Errorf("invalid premine balance amount provided: %w", err)
		}

		p.premineInfos = append(p.premineInfos, premineInfo)
	}

	return nil
}

// validatePremineInfo validates whether reserve account (0x0 address) is premined
func (p *genesisParams) validatePremineInfo() error {
	for _, premineInfo := range p.premineInfos {
		if premineInfo.Address == types.ZeroAddress {
			// we have premine of zero address, just return
			return nil
		}
	}

	return errReserveAccMustBePremined
}

// validateBlockTrackerPollInterval validates block tracker block interval
// which can not be 0
func (p *genesisParams) validateBlockTrackerPollInterval() error {
	if p.blockTrackerPollInterval == 0 {
		return helper.ErrBlockTrackerPollInterval
	}

	return nil
}

// validateBurnContract validates burn contract. If native token is mintable,
// burn contract flag must not be set. If native token is non mintable only one burn contract
// can be set and the specified address will be used to predeploy default EIP1559 burn contract.
func (p *genesisParams) validateBurnContract() error {
	if p.isBurnContractEnabled() {
		burnContractInfo, err := parseBurnContractInfo(p.burnContract)
		if err != nil {
			return fmt.Errorf("invalid burn contract info provided: %w", err)
		}

		if p.nativeTokenConfig.IsMintable {
			if burnContractInfo.Address != types.ZeroAddress {
				return errors.New("only zero address is allowed as burn destination for mintable native token")
			}
		} else {
			if burnContractInfo.Address == types.ZeroAddress {
				return errors.New("it is not allowed to deploy burn contract to 0x0 address")
			}
		}
	}

	return nil
}

func (p *genesisParams) validateGenesisBaseFeeConfig() error {
	if p.baseFeeConfig == "" {
		return errors.New("invalid input(empty string) for genesis base fee config flag")
	}

	baseFeeInfo, err := parseBaseFeeConfig(p.baseFeeConfig)
	if err != nil {
		return fmt.Errorf("failed to parse base fee config: %w, provided value %s", err, p.baseFeeConfig)
	}

	p.parsedBaseFeeConfig = baseFeeInfo

	if baseFeeInfo.baseFee == 0 {
		return errBaseFeeZero
	}

	if baseFeeInfo.baseFeeEM == 0 {
		return errBaseFeeEMZero
	}

	if baseFeeInfo.baseFeeChangeDenom == 0 {
		return errBaseFeeChangeDenomZero
	}

	return nil
}

func (p *genesisParams) validateProxyContractsAdmin() error {
	if strings.TrimSpace(p.proxyContractsAdmin) == "" {
		return errors.New("proxy contracts admin address must be set")
	}

	proxyContractsAdminAddr := types.StringToAddress(p.proxyContractsAdmin)
	if proxyContractsAdminAddr == types.ZeroAddress {
		return errors.New("proxy contracts admin address must not be zero address")
	}

	if proxyContractsAdminAddr == contracts.SystemCaller {
		return errors.New("proxy contracts admin address must not be system caller address")
	}

	return nil
}

// isBurnContractEnabled returns true in case burn contract info is provided
func (p *genesisParams) isBurnContractEnabled() bool {
	return p.burnContract != ""
}

// extractNativeTokenMetadata parses provided native token metadata (such as name, symbol and decimals count)
func (p *genesisParams) extractNativeTokenMetadata() error {
	tokenConfig, err := polybft.ParseRawTokenConfig(p.nativeTokenConfigRaw)
	if err != nil {
		return err
	}

	p.nativeTokenConfig = tokenConfig

	return nil
}

func (p *genesisParams) getResult() command.CommandResult {
	return &GenesisResult{
		Message: fmt.Sprintf("\nGenesis written to %s\n", p.genesisPath),
	}
}

// generateDPoSChainConfig creates and persists DPoS chain configuration to the provided file path
func (p *genesisParams) generateDPoSChainConfig(o command.OutputFormatter) error {
	// populate premine balance map
	premineBalances := make(map[types.Address]*helper.PremineInfo, len(p.premine))

	for _, premine := range p.premineInfos {
		premineBalances[premine.Address] = premine
	}

	// Create chain configuration
	chainConfig := &chain.Chain{
		Name: p.name,
		Genesis: &chain.Genesis{
			GasLimit:   p.blockGasLimit,
			Difficulty: 1,
			Alloc:      map[types.Address]*chain.GenesisAccount{},
			ExtraData:  []byte{}, // Will be set below for DPoS
			GasUsed:    command.DefaultGenesisGasUsed,
		},
		Params: &chain.Params{
			ChainID: int64(p.chainID),
			Forks:   chain.AllForksEnabled,
			Engine: map[string]interface{}{
				string(server.DPoSConsensus): map[string]interface{}{
					"blockTime":     p.blockTime,
					"epochSize":     p.epochSize,
					"delegateCount": 21, // Default delegate count for DPoS
				},
			},
		},
		Bootnodes: p.bootnodes,
	}

	// Add premine accounts
	for address, premineInfo := range premineBalances {
		chainConfig.Genesis.Alloc[address] = &chain.GenesisAccount{
			Balance: premineInfo.Amount,
		}
	}

	// Read validators from validators path if specified
	if p.validatorsPath != "" && p.validatorsPrefixPath != "" {
		validators, err := command.GetValidatorsFromPrefixPath(
			p.validatorsPath,
			p.validatorsPrefixPath,
			validators.BLSValidatorType, // DPoS uses BLS validators
		)
		if err != nil {
			return fmt.Errorf("failed to read validators from path: %w", err)
		}

		// Convert validators to DPoS format and add to genesis
		initialDelegates := make([]map[string]interface{}, 0, validators.Len())
		for i := uint64(0); i < uint64(validators.Len()); i++ {
			validator := validators.At(i)
			validatorInfo := map[string]interface{}{
				"address":     validator.Addr().String(),
				"votingPower": "1000000000000000000000", // Default voting power
				"isActive":    true,
			}
			initialDelegates = append(initialDelegates, validatorInfo)
		}

		// Add initial delegates to engine config
		if engineConfig, ok := chainConfig.Params.Engine[string(server.DPoSConsensus)].(map[string]interface{}); ok {
			engineConfig["initialDelegates"] = initialDelegates
		}
	}

	// Create DPoS genesis extraData
	// For DPoS genesis block, we need to create a valid Extra structure
	// that can be parsed by subsequent blocks
	// Use the same format as block 1: all fields as nil to create null arrays
	dposExtra := &dpos.Extra{
		Validators: nil, // No validators in genesis
		Parent:     nil, // No parent signatures in genesis
		Committed:  nil, // No committed signatures in genesis
		Checkpoint: nil, // No checkpoint in genesis
	}

	// Create the extraData using the same method as block 1
	// This ensures the format is exactly the same
	genesisExtraData := dposExtra.MarshalRLPTo(nil)

	// Add debug logging
	fmt.Printf("DEBUG: Genesis extraData length: %d\n", len(genesisExtraData))
	fmt.Printf("DEBUG: Genesis extraData vanity prefix length: %d\n", dpos.ExtraVanity)
	fmt.Printf("DEBUG: Genesis extraData RLP part length: %d\n", len(genesisExtraData)-dpos.ExtraVanity)
	fmt.Printf("DEBUG: Genesis extraData first 10 bytes: %x\n", genesisExtraData[:min(10, len(genesisExtraData))])
	fmt.Printf("DEBUG: Genesis extraData after vanity prefix: %x\n", genesisExtraData[dpos.ExtraVanity:min(dpos.ExtraVanity+20, len(genesisExtraData))])

	chainConfig.Genesis.ExtraData = genesisExtraData

	// Apply transactions block list when generating a pure-DPoS genesis.
	// IBFT→DPoS live networks should keep IBFT genesis and enable the list via hardfork.
	p.applyTransactionsBlockListConfig(chainConfig)

	// Write genesis configuration to disk
	if err := helper.WriteGenesisConfigToDisk(chainConfig, p.genesisPath); err != nil {
		return fmt.Errorf("failed to write genesis config to disk: %w", err)
	}

	p.genesisConfig = chainConfig

	return nil
}
