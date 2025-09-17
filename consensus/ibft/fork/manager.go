package fork

import (
	"errors"
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/ibft/hook"
	"github.com/Vcity-Team/vcitychain/consensus/ibft/signer"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
	"github.com/Vcity-Team/vcitychain/validators/store"
	"github.com/Vcity-Team/vcitychain/validators/store/contract"
	"github.com/hashicorp/go-hclog"
	"github.com/umbracle/fastrlp"
)

const (
	loggerName                = "fork_manager"
	snapshotMetadataFilename  = "metadata"
	snapshotSnapshotsFilename = "snapshots"
)

var (
	ErrForkNotFound           = errors.New("fork not found")
	ErrSignerNotFound         = errors.New("signer not found")
	ErrValidatorStoreNotFound = errors.New("validator set not found")
	ErrKeyManagerNotFound     = errors.New("key manager not found")
)

// ValidatorStore is an interface that ForkManager calls for Validator Store
type ValidatorStore interface {
	store.ValidatorStore
	// Close defines termination process
	Close() error
	// GetValidatorsAtHeight is a method to return validators at the given height
	GetValidatorsAtHeight(height, epochSize, forkFrom uint64) (validators.Validators, error)
}

// HookRegister is an interface that ForkManager calls for hook registrations
type HooksRegister interface {
	// RegisterHooks register hooks for the given block height
	RegisterHooks(hooks *hook.Hooks, height uint64)
}

// HooksInterface is an interface of hooks to be called by IBFT
// This interface is referred from fork and ibft package
type HooksInterface interface {
	ShouldWriteTransactions(uint64) bool
	ModifyHeader(*types.Header, types.Address) error
	VerifyHeader(*types.Header) error
	VerifyBlock(*types.Block) error
	ProcessHeader(*types.Header) error
	PreCommitState(*types.Header, *state.Transition) error
	PostInsertBlock(*types.Block) error
}

// ForkManager is the module that has Fork configuration and multiple version of submodules
// and returns the proper submodule at specified height
type ForkManager struct {
	logger         hclog.Logger
	blockchain     store.HeaderGetter
	executor       contract.Executor
	secretsManager secrets.SecretsManager

	// configuration
	forks     IBFTForks
	filePath  string
	epochSize uint64

	// submodule lookup
	keyManagers     map[validators.ValidatorType]signer.KeyManager
	validatorStores map[store.SourceType]ValidatorStore
	hooksRegisters  map[IBFTType]HooksRegister
}

// NewForkManager is a constructor of ForkManager
func NewForkManager(
	logger hclog.Logger,
	blockchain store.HeaderGetter,
	executor contract.Executor,
	secretManager secrets.SecretsManager,
	filePath string,
	epochSize uint64,
	ibftConfig map[string]interface{},
) (*ForkManager, error) {
	forks, err := GetIBFTForks(ibftConfig)
	if err != nil {
		return nil, err
	}

	fm := &ForkManager{
		logger:          logger.Named(loggerName),
		blockchain:      blockchain,
		executor:        executor,
		secretsManager:  secretManager,
		filePath:        filePath,
		epochSize:       epochSize,
		forks:           forks,
		keyManagers:     make(map[validators.ValidatorType]signer.KeyManager),
		validatorStores: make(map[store.SourceType]ValidatorStore),
		hooksRegisters:  make(map[IBFTType]HooksRegister),
	}

	// Need initialization of signers in the constructor
	// because hash calculation is called from blockchain initialization
	if err := fm.initializeKeyManagers(); err != nil {
		return nil, err
	}

	return fm, nil
}

// Initialize initializes ForkManager on initialization phase
func (m *ForkManager) Initialize() error {
	m.logger.Info("ForkManager.Initialize called")
	
	if err := m.initializeValidatorStores(); err != nil {
		m.logger.Error("Failed to initialize validator stores", "error", err)
		return err
	}

	m.initializeHooksRegisters()

	m.logger.Info("ForkManager.Initialize completed successfully")
	return nil
}

// Close calls termination process of submodules
func (m *ForkManager) Close() error {
	for _, store := range m.validatorStores {
		if err := store.Close(); err != nil {
			return err
		}
	}

	return nil
}

// GetSigner returns a proper signer at specified height
func (m *ForkManager) GetSigner(height uint64) (signer.Signer, error) {
	keyManager, err := m.getKeyManager(height)
	if err != nil {
		return nil, err
	}

	var parentKeyManager signer.KeyManager

	if height > 1 {
		if parentKeyManager, err = m.getKeyManager(height - 1); err != nil {
			return nil, err
		}
	}

	return signer.NewSigner(
		keyManager,
		parentKeyManager,
	), nil
}

// GetValidatorStore returns a proper validator set at specified height
func (m *ForkManager) GetValidatorStore(height uint64) (ValidatorStore, error) {
	fork := m.forks.getFork(height)
	if fork == nil {
		return nil, ErrForkNotFound
	}

	set := m.getValidatorStoreByIBFTFork(fork)
	if set == nil {
		return nil, ErrValidatorStoreNotFound
	}

	return set, nil
}

// GetValidators returns validators at specified height
func (m *ForkManager) GetValidators(height uint64) (validators.Validators, error) {
	m.logger.Info("ForkManager.GetValidators called", "height", height)
	
	fork := m.forks.getFork(height)
	if fork == nil {
		m.logger.Error("Fork not found for height", "height", height)
		return nil, ErrForkNotFound
	}

	// 安全地处理To字段，可能为nil
	toValue := "nil"
	if fork.To != nil {
		toValue = fmt.Sprintf("%d", fork.To.Value)
	}
	
	m.logger.Info("Fork found", 
		"height", height,
		"fork_type", fork.Type,
		"validator_type", fork.ValidatorType,
		"from", fork.From.Value,
		"to", toValue)

	set := m.getValidatorStoreByIBFTFork(fork)
	if set == nil {
		m.logger.Error("Validator store not found", 
			"height", height,
			"fork_type", fork.Type,
			"source_type", ibftTypesToSourceType[fork.Type])
		return nil, ErrValidatorStoreNotFound
	}

	m.logger.Info("Validator store found", 
		"height", height,
		"store_type", fmt.Sprintf("%T", set),
		"epoch_size", m.epochSize,
		"fork_from", fork.From.Value)

	validators, err := set.GetValidatorsAtHeight(
		height,
		m.epochSize,
		fork.From.Value,
	)
	
	if err != nil {
		m.logger.Error("Failed to get validators at height", 
			"height", height,
			"error", err)
		return nil, err
	}

	m.logger.Info("Validators retrieved successfully", 
		"height", height,
		"validator_count", validators.Len(),
		"validator_type", validators.Type())
	
	// 打印所有验证器地址
	for i := 0; i < validators.Len(); i++ {
		validator := validators.At(uint64(i))
		m.logger.Info("Validator details", 
			"height", height,
			"index", i,
			"address", validator.Addr().String(),
			"type", validator.Type())
	}

	return validators, nil
}

// GetHooks returns a hooks at specified height
func (m *ForkManager) GetHooks(height uint64) HooksInterface {
	hooks := &hook.Hooks{}

	for _, r := range m.hooksRegisters {
		r.RegisterHooks(hooks, height)
	}

	return hooks
}

func (m *ForkManager) getValidatorStoreByIBFTFork(fork *IBFTFork) ValidatorStore {
	set, ok := m.validatorStores[ibftTypesToSourceType[fork.Type]]
	if !ok {
		return nil
	}

	return set
}

func (m *ForkManager) getKeyManager(height uint64) (signer.KeyManager, error) {
	fork := m.forks.getFork(height)
	if fork == nil {
		return nil, ErrForkNotFound
	}

	keyManager, ok := m.keyManagers[fork.ValidatorType]
	if !ok {
		return nil, ErrKeyManagerNotFound
	}

	return keyManager, nil
}

// initializeKeyManagers initialize all key managers based on Fork configuration
func (m *ForkManager) initializeKeyManagers() error {
	for _, fork := range m.forks {
		if err := m.initializeKeyManager(fork.ValidatorType); err != nil {
			return err
		}
	}

	return nil
}

// initializeKeyManager initializes the sp
func (m *ForkManager) initializeKeyManager(valType validators.ValidatorType) error {
	if _, ok := m.keyManagers[valType]; ok {
		return nil
	}

	keyManager, err := signer.NewKeyManagerFromType(m.secretsManager, valType)
	if err != nil {
		return err
	}

	m.keyManagers[valType] = keyManager

	return nil
}

// initializeValidatorStores initializes all validator sets based on Fork configuration
func (m *ForkManager) initializeValidatorStores() error {
	m.logger.Info("initializeValidatorStores called", "forks_count", len(m.forks))
	
	for _, fork := range m.forks {
		sourceType := ibftTypesToSourceType[fork.Type]
		m.logger.Info("Initializing validator store for fork", 
			"fork_type", fork.Type,
			"source_type", sourceType)
		
		if err := m.initializeValidatorStore(sourceType); err != nil {
			m.logger.Error("Failed to initialize validator store", 
				"fork_type", fork.Type,
				"source_type", sourceType,
				"error", err)
			return err
		}
	}

	m.logger.Info("initializeValidatorStores completed successfully")
	return nil
}

// initializeValidatorStore initializes the specified validator set
func (m *ForkManager) initializeValidatorStore(setType store.SourceType) error {
	m.logger.Info("initializeValidatorStore called", "setType", setType)
	
	if _, ok := m.validatorStores[setType]; ok {
		m.logger.Info("Validator store already exists", "setType", setType)
		return nil
	}

	var (
		valStore ValidatorStore
		err      error
	)

	switch setType {
	case store.Snapshot:
		// Get validators from the first PoA fork
		var initialValidators validators.Validators
		for _, fork := range m.forks {
			m.logger.Info("Checking fork", 
				"fork_type", fork.Type,
				"has_validators", fork.Validators != nil,
				"validator_count", func() int { 
					if fork.Validators != nil { 
						return fork.Validators.Len() 
					} 
					return 0 
				}())
			
			if fork.Type == PoA && fork.Validators != nil {
				initialValidators = fork.Validators
				m.logger.Info("Found PoA fork with validators", 
					"validator_count", initialValidators.Len(),
					"validator_type", initialValidators.Type())
				break
			}
		}
		
		// If no validators found in fork config, try to parse from genesis block extraData
		if initialValidators == nil {
			m.logger.Info("No PoA fork with validators found, trying to parse from genesis block extraData")
			if genesisHeader, exists := m.blockchain.GetHeaderByNumber(0); exists {
				m.logger.Info("Genesis block found", 
					"extraData_length", len(genesisHeader.ExtraData),
					"extraData_hex", fmt.Sprintf("0x%x", genesisHeader.ExtraData))
				
				// Parse validators from genesis extraData
				parsedValidators, err := m.parseValidatorsFromExtraData(genesisHeader.ExtraData)
				if err != nil {
					m.logger.Error("Failed to parse validators from genesis extraData", "error", err)
				} else {
					initialValidators = parsedValidators
					m.logger.Info("Parsed validators from genesis extraData", 
						"validator_count", initialValidators.Len(),
						"validator_type", initialValidators.Type())
				}
			} else {
				m.logger.Warn("Genesis block not found, using nil initialValidators")
			}
		}
		
		m.logger.Info("Creating SnapshotValidatorStoreWrapper", 
			"initialValidators", initialValidators != nil,
			"initialValidatorsLen", func() int {
				if initialValidators != nil {
					return initialValidators.Len()
				}
				return 0
			}())
		
		valStore, err = NewSnapshotValidatorStoreWrapper(
			m.logger,
			m.blockchain,
			m.GetSigner,
			m.filePath,
			m.epochSize,
			initialValidators,
		)
	case store.Contract:
		valStore, err = NewContractValidatorStoreWrapper(
			m.logger,
			m.blockchain,
			m.executor,
			m.GetSigner,
		)
	}

	if err != nil {
		return err
	}

	m.validatorStores[setType] = valStore

	return nil
}

// parseValidatorsFromExtraData parses validators from genesis block extraData
func (m *ForkManager) parseValidatorsFromExtraData(extraData []byte) (validators.Validators, error) {
	m.logger.Info("Starting extraData parsing", 
		"extraData_length", len(extraData),
		"extraData_hex", fmt.Sprintf("0x%x", extraData))
	
	// Remove only the vanity bytes (32 bytes) from extraData
	// The rest is RLP data containing validators and seals
	if len(extraData) < 32 {
		return nil, fmt.Errorf("extraData too short: %d bytes", len(extraData))
	}
	
	// Extract the RLP-encoded data
	// extraData format: [vanity(32)] + [RLP(IstanbulExtra)]
	rlpData := extraData[32:]
	
	m.logger.Info("Extracted RLP data", 
		"rlpData_length", len(rlpData),
		"rlpData_hex", fmt.Sprintf("0x%x", rlpData))
	
	// Create ECDSA validators
	validatorList := make([]*validators.ECDSAValidator, 0)
	
	// Parse RLP data using the same method as the test
	err := types.UnmarshalRlp(func(p *fastrlp.Parser, v *fastrlp.Value) error {
		// Get the top-level list
		elems, err := v.GetElems()
		if err != nil {
			return fmt.Errorf("expected array: %w", err)
		}
		
		m.logger.Info("Found validator list in RLP", "validator_count", len(elems))
		
		// Process each element
		for i, elem := range elems {
			// Try to get bytes
			if bytes, err := elem.GetBytes(nil); err == nil {
				// If it's 20 bytes, it might be an address
				if len(bytes) == 20 {
					addr := types.BytesToAddress(bytes)
					validator := validators.NewECDSAValidator(addr)
					validatorList = append(validatorList, validator)
					
					m.logger.Info("Parsed validator from extraData", 
						"index", i,
						"address", addr.String())
				}
			} else {
				// Try to get sub-elements
				if subElems, err := elem.GetElems(); err == nil {
					m.logger.Info("Found sub-list in RLP", "sub_count", len(subElems))
					
					for j, subElem := range subElems {
						if subBytes, err := subElem.GetBytes(nil); err == nil {
							// If it's 20 bytes, it might be an address
							if len(subBytes) == 20 {
								addr := types.BytesToAddress(subBytes)
								validator := validators.NewECDSAValidator(addr)
								validatorList = append(validatorList, validator)
								
								m.logger.Info("Parsed validator from extraData sub-list", 
									"index", j,
									"address", addr.String())
							}
						}
					}
				}
			}
		}
		
		return nil
	}, rlpData)
	
	if err != nil {
		return nil, fmt.Errorf("failed to parse RLP data: %w", err)
	}
	
	m.logger.Info("Successfully parsed validators from extraData", 
		"validator_count", len(validatorList))
	
	return validators.NewECDSAValidatorSet(validatorList...), nil
}

// initializeHooksRegisters initialize all HookRegisters to be used
func (m *ForkManager) initializeHooksRegisters() {
	for _, fork := range m.forks {
		m.initializeHooksRegister(fork.Type)
	}
}

// initializeHooksRegister initialize HookRegister by IBFTType
func (m *ForkManager) initializeHooksRegister(ibftType IBFTType) {
	if _, ok := m.hooksRegisters[ibftType]; ok {
		return
	}

	switch ibftType {
	case PoA:
		m.hooksRegisters[PoA] = NewPoAHookRegisterer(
			m.getValidatorStoreByIBFTFork,
			m.forks,
		)
	case PoS:
		m.hooksRegisters[PoS] = NewPoSHookRegister(
			m.forks,
			m.epochSize,
		)
	}
}
