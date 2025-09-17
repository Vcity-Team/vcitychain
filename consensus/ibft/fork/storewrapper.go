package fork

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/Vcity-Team/vcitychain/consensus/ibft/signer"
	"github.com/Vcity-Team/vcitychain/validators"
	"github.com/Vcity-Team/vcitychain/validators/store"
	"github.com/Vcity-Team/vcitychain/validators/store/contract"
	"github.com/Vcity-Team/vcitychain/validators/store/snapshot"
	"github.com/hashicorp/go-hclog"
)

// signerAdapter adapts signer.Signer to snapshot.SignerInterface
type signerAdapter struct {
	signer signer.Signer
}

// Sign implements snapshot.SignerInterface
func (a *signerAdapter) Sign(data []byte) ([]byte, error) {
	// Use SignIBFTMessage as the signing method
	return a.signer.SignIBFTMessage(data)
}

// isJSONSyntaxError returns bool indicating the giving error is json.SyntaxError or not
func isJSONSyntaxError(err error) bool {
	var expected *json.SyntaxError

	if err == nil {
		return false
	}

	return errors.As(err, &expected)
}

// SnapshotValidatorStoreWrapper is a wrapper of store.SnapshotValidatorStore
// in order to add initialization and closer process with side effect
type SnapshotValidatorStoreWrapper struct {
	*snapshot.SnapshotValidatorStore
	dirPath           string
	initialValidators validators.Validators
}

// SourceType returns the type of validator source
func (w *SnapshotValidatorStoreWrapper) SourceType() store.SourceType {
	return store.Snapshot
}

// GetSnapshotMetadata returns the snapshot metadata
func (w *SnapshotValidatorStoreWrapper) GetSnapshotMetadata() interface{} {
	// TODO: Implement actual metadata retrieval from snapshot store
	return nil
}

// GetSnapshots returns the snapshots
func (w *SnapshotValidatorStoreWrapper) GetSnapshots() interface{} {
	// TODO: Implement actual snapshots retrieval from snapshot store
	return nil
}

// GetValidatorsByHeight returns validators at the specific height
func (w *SnapshotValidatorStoreWrapper) GetValidatorsByHeight(height uint64) (validators.Validators, error) {
	// TODO: Implement actual height-based validator retrieval
	return validators.NewBLSValidatorSet(), nil
}

// Close saves SnapshotValidator data into local storage
func (w *SnapshotValidatorStoreWrapper) Close() error {
	// save data
	var (
		metadata  = w.GetSnapshotMetadata()
		snapshots = w.GetSnapshots()
	)

	if err := writeDataStore(filepath.Join(w.dirPath, snapshotMetadataFilename), metadata); err != nil {
		return err
	}

	if err := writeDataStore(filepath.Join(w.dirPath, snapshotSnapshotsFilename), snapshots); err != nil {
		return err
	}

	return nil
}

// GetValidatorsAtHeight returns validators at the specific height
func (w *SnapshotValidatorStoreWrapper) GetValidatorsAtHeight(height, epochSize, forkFrom uint64) (validators.Validators, error) {
	// For now, we need to get validators from the genesis configuration
	// This is a temporary fix - in a full implementation, this would look up snapshots
	
	// Try to get validators from the underlying snapshot store
	validatorSet, err := w.GetValidatorsByHeight(height - 1)
	if err != nil {
		return nil, err
	}
	
	// If no validators found, use initial validators from genesis
	if validatorSet == nil || validatorSet.Len() == 0 {
		if w.initialValidators != nil && w.initialValidators.Len() > 0 {
			return w.initialValidators, nil
		}
		return validators.NewECDSAValidatorSet(), nil
	}
	
	return validatorSet, nil
}

// NewSnapshotValidatorStoreWrapper loads data from local storage and creates *SnapshotValidatorStoreWrapper
func NewSnapshotValidatorStoreWrapper(
	logger hclog.Logger,
	blockchain store.HeaderGetter,
	getSigner func(uint64) (signer.Signer, error),
	dirPath string,
	epochSize uint64,
	initialValidators validators.Validators,
) (*SnapshotValidatorStoreWrapper, error) {
	var (
		snapshotMetadataPath = filepath.Join(dirPath, snapshotMetadataFilename)
		snapshotsPath        = filepath.Join(dirPath, snapshotSnapshotsFilename)
	)

	snapshotMeta, err := loadSnapshotMetadata(snapshotMetadataPath)
	if isJSONSyntaxError(err) {
		logger.Warn("Snapshot metadata file is broken, recover metadata from local chain", "filepath", snapshotMetadataPath)

		snapshotMeta = nil
	} else if err != nil {
		return nil, err
	}

	snapshots, err := loadSnapshots(snapshotsPath)
	if isJSONSyntaxError(err) {
		logger.Warn("Snapshots file is broken, recover snapshots from local chain", "filepath", snapshotsPath)

		snapshots = nil
	} else if err != nil {
		return nil, err
	}

	snapshotStore, err := snapshot.NewSnapshotValidatorStore(
		logger,
		blockchain,
		func(height uint64) (snapshot.SignerInterface, error) {
			rawSigner, err := getSigner(height)
			if err != nil {
				return nil, err
			}

			// Create a signer adapter that implements snapshot.SignerInterface
			return &signerAdapter{signer: rawSigner}, nil
		},
		epochSize,
		snapshotMeta,
		snapshots,
	)

	if err != nil {
		return nil, err
	}

	return &SnapshotValidatorStoreWrapper{
		SnapshotValidatorStore: snapshotStore,
		dirPath:                dirPath,
		initialValidators:      initialValidators,
	}, nil
}

// ContractValidatorStoreWrapper is a wrapper of *contract.ContractValidatorStore
// in order to add Close and GetValidators
type ContractValidatorStoreWrapper struct {
	*contract.ContractValidatorStore
	getSigner func(uint64) (signer.Signer, error)
}

// SourceType returns the type of validator source
func (w *ContractValidatorStoreWrapper) SourceType() store.SourceType {
	return store.Contract
}

// NewContractValidatorStoreWrapper creates *ContractValidatorStoreWrapper
func NewContractValidatorStoreWrapper(
	logger hclog.Logger,
	blockchain store.HeaderGetter,
	executor contract.Executor,
	getSigner func(uint64) (signer.Signer, error),
) (*ContractValidatorStoreWrapper, error) {
	contractStore, err := contract.NewContractValidatorStore(
		logger,
		blockchain,
		executor,
		contract.DefaultValidatorSetCacheSize,
	)

	if err != nil {
		return nil, err
	}

	return &ContractValidatorStoreWrapper{
		ContractValidatorStore: contractStore,
		getSigner:              getSigner,
	}, nil
}

// Close is closer process
func (w *ContractValidatorStoreWrapper) Close() error {
	return nil
}

// GetValidatorsAtHeight gets and returns validators at the given height
func (w *ContractValidatorStoreWrapper) GetValidatorsAtHeight(
	height, epochSize, forkFrom uint64,
) (validators.Validators, error) {
	signer, err := w.getSigner(height)
	if err != nil {
		return nil, err
	}

	return w.GetValidatorsByHeight(
		signer.Type(),
		calculateContractStoreFetchingHeight(
			height,
			epochSize,
			forkFrom,
		),
	)
}

// GetValidatorsByHeight returns validators at the specific height and validator type
func (w *ContractValidatorStoreWrapper) GetValidatorsByHeight(
	valType validators.ValidatorType,
	height uint64,
) (validators.Validators, error) {
	// TODO: Implement actual height-based validator retrieval from contract
	return validators.NewBLSValidatorSet(), nil
}

// calculateContractStoreFetchingHeight calculates the block height at which ContractStore fetches validators
// based on height, epoch, and fork beginning height
func calculateContractStoreFetchingHeight(height, epochSize, forkFrom uint64) uint64 {
	// calculates the beginning of the epoch the given height is in
	beginningEpoch := (height / epochSize) * epochSize

	// calculates the end of the previous epoch
	// to determine the height to fetch validators
	fetchingHeight := uint64(0)
	if beginningEpoch > 0 {
		fetchingHeight = beginningEpoch - 1
	}

	// use the calculated height if it's bigger than or equal to from
	if fetchingHeight >= forkFrom {
		return fetchingHeight
	}

	if forkFrom > 0 {
		return forkFrom - 1
	}

	return forkFrom
}
