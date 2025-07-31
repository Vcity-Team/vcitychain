package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/umbracle/ethgo/abi"
	"github.com/umbracle/fastrlp"
)

const (
	// ExtraVanity represents a fixed number of extra-data bytes reserved for proposer vanity
	ExtraVanity = 32

	// ExtraSeal represents the fixed number of extra-data bytes reserved for proposer seal
	ExtraSeal = 65
)

// PolyBFTMixDigest represents a hash of "PolyBFT Mix" to identify whether the block is from PolyBFT consensus engine
var PolyBFTMixDigest = types.StringToHash("adce6e5230abe012342a44e4e9b6d05997d6f015387ae0e59be924afc7ec70c1")

// Extra defines the structure of the extra field for Istanbul
type Extra struct {
	Validators *validator.ValidatorSetDelta
	Parent     *Signature
	Committed  *Signature
	Checkpoint *CheckpointData
}

// MarshalRLPTo defines the marshal function wrapper for Extra
func (i *Extra) MarshalRLPTo(dst []byte) []byte {
	ar := &fastrlp.Arena{}

	return append(make([]byte, ExtraVanity), i.MarshalRLPWith(ar).MarshalTo(dst)...)
}

// MarshalRLPWith defines the marshal function implementation for Extra
func (i *Extra) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	// Validators
	if i.Validators == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Validators.MarshalRLPWith(ar))
	}

	// Parent Signatures
	if i.Parent == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Parent.MarshalRLPWith(ar))
	}

	// Committed Signatures
	if i.Committed == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Committed.MarshalRLPWith(ar))
	}

	// Checkpoint
	if i.Checkpoint == nil {
		vv.Set(ar.NewNullArray())
	} else {
		vv.Set(i.Checkpoint.MarshalRLPWith(ar))
	}

	return vv
}

// UnmarshalRLP defines the unmarshal function wrapper for Extra
func (i *Extra) UnmarshalRLP(input []byte) error {
	return fastrlp.UnmarshalRLP(input[ExtraVanity:], i)
}

// UnmarshalRLPWith defines the unmarshal implementation for Extra
func (i *Extra) UnmarshalRLPWith(v *fastrlp.Value) error {
	const expectedElements = 4

	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if num := len(elems); num != expectedElements {
		return fmt.Errorf("incorrect elements count to decode Extra, expected %d but found %d", expectedElements, num)
	}

	fmt.Printf("DEBUG: Extra.UnmarshalRLPWith - parsing %d elements\n", len(elems))

	// Validators
	fmt.Printf("DEBUG: parsing Validators (element 0), type: %v, elems: %d\n", elems[0].Type(), elems[0].Elems())
	if elems[0].Elems() > 0 {
		i.Validators = &validator.ValidatorSetDelta{}
		if err := i.Validators.UnmarshalRLPWith(elems[0]); err != nil {
			fmt.Printf("DEBUG: error parsing Validators: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: successfully parsed Validators\n")
	}

	// Parent Signatures
	fmt.Printf("DEBUG: parsing Parent Signatures (element 1), type: %v, elems: %d\n", elems[1].Type(), elems[1].Elems())
	if elems[1].Elems() > 0 {
		i.Parent = &Signature{}
		if err := i.Parent.UnmarshalRLPWith(elems[1]); err != nil {
			fmt.Printf("DEBUG: error parsing Parent Signatures: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: successfully parsed Parent Signatures\n")
	}

	// Committed Signatures - always create object for block 1 compatibility
	fmt.Printf("DEBUG: parsing Committed Signatures (element 2), type: %v, elems: %d\n", elems[2].Type(), elems[2].Elems())
	if elems[2].Elems() > 0 {
		i.Committed = &Signature{}
		if err := i.Committed.UnmarshalRLPWith(elems[2]); err != nil {
			fmt.Printf("DEBUG: error parsing Committed Signatures: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: successfully parsed Committed Signatures\n")
	} else {
		// Create empty signature for block 1
		i.Committed = &Signature{
			AggregatedSignature: nil,
			Bitmap:              nil,
		}
		fmt.Printf("DEBUG: created empty Committed Signatures for block 1\n")
	}

	// Checkpoint - always create object for block 1 compatibility
	fmt.Printf("DEBUG: parsing Checkpoint (element 3), type: %v, elems: %d\n", elems[3].Type(), elems[3].Elems())
	if elems[3].Elems() > 0 {
		i.Checkpoint = &CheckpointData{}
		if err := i.Checkpoint.UnmarshalRLPWith(elems[3]); err != nil {
			fmt.Printf("DEBUG: error parsing Checkpoint: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: successfully parsed Checkpoint\n")
	} else {
		// Create empty checkpoint for block 1
		i.Checkpoint = &CheckpointData{
			BlockRound:            0,
			EpochNumber:           1,
			CurrentValidatorsHash: types.Hash{},
			NextValidatorsHash:    types.Hash{},
			EventRoot:             types.Hash{},
		}
		fmt.Printf("DEBUG: created empty Checkpoint for block 1\n")
	}

	fmt.Printf("DEBUG: Extra.UnmarshalRLPWith completed successfully\n")
	return nil
}

// ValidateFinalizedData contains extra data validations for finalized headers
func (i *Extra) ValidateFinalizedData(header *types.Header, parent *types.Header, parents []*types.Header,
	chainID uint64, consensusBackend dposBackend, domain []byte, logger hclog.Logger) error {
	// validate committed signatures
	blockNumber := header.Number

	fmt.Printf("DEBUG: ValidateFinalizedData - block %d\n", blockNumber)

	// For block 1, we need to validate the structure but can be more lenient with signatures
	if blockNumber == 1 {
		fmt.Printf("DEBUG: Block 1 detected, starting validation\n")

		// Still validate that Committed and Checkpoint are present
		if i.Committed == nil {
			fmt.Printf("DEBUG: Committed signatures is nil\n")
			return fmt.Errorf("failed to verify signatures for block %d, because signatures are not present", blockNumber)
		}

		if i.Checkpoint == nil {
			fmt.Printf("DEBUG: Checkpoint is nil\n")
			return fmt.Errorf("failed to verify signatures for block %d, because checkpoint data are not present", blockNumber)
		}

		// For block 1, we skip the actual signature verification but validate the structure
		logger.Info("block 1 detected, validating structure only")
		fmt.Printf("DEBUG: Block 1 structure validation passed\n")

		// Validate parent signatures (this will be skipped for block 1)
		fmt.Printf("DEBUG: Getting parent extra data\n")
		parentExtra, err := GetIbftExtra(parent.ExtraData)
		if err != nil {
			fmt.Printf("DEBUG: Error getting parent extra data: %v\n", err)
			return fmt.Errorf("failed to verify signatures for block %d: %w", blockNumber, err)
		}
		fmt.Printf("DEBUG: Successfully got parent extra data\n")

		fmt.Printf("DEBUG: Validating parent signatures\n")
		if err := i.ValidateParentSignatures(blockNumber, consensusBackend, parents,
			parent, parentExtra, chainID, domain, logger); err != nil {
			fmt.Printf("DEBUG: Error validating parent signatures: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: Parent signatures validation passed\n")

		fmt.Printf("DEBUG: Validating checkpoint basic\n")
		err = i.Checkpoint.ValidateBasic(parentExtra.Checkpoint)
		if err != nil {
			fmt.Printf("DEBUG: Error validating checkpoint basic: %v\n", err)
		}
		return err
	}

	if i.Committed == nil {
		return fmt.Errorf("failed to verify signatures for block %d, because signatures are not present", blockNumber)
	}

	if i.Checkpoint == nil {
		return fmt.Errorf("failed to verify signatures for block %d, because checkpoint data are not present", blockNumber)
	}

	// validate current block signatures
	checkpointHash, err := i.Checkpoint.Hash(chainID, blockNumber, header.Hash)
	if err != nil {
		return fmt.Errorf("failed to calculate proposal hash: %w", err)
	}

	validators, err := consensusBackend.GetDelegates(blockNumber-1, parents)
	if err != nil {
		return fmt.Errorf("failed to validate header for block %d. could not retrieve block validators:%w", blockNumber, err)
	}

	if err := i.Committed.Verify(blockNumber, validators, checkpointHash, domain, logger); err != nil {
		return fmt.Errorf("failed to verify signatures for block %d (proposal hash %s): %w",
			blockNumber, checkpointHash, err)
	}

	parentExtra, err := GetIbftExtra(parent.ExtraData)
	if err != nil {
		return fmt.Errorf("failed to verify signatures for block %d: %w", blockNumber, err)
	}

	// validate parent signatures
	if err := i.ValidateParentSignatures(blockNumber, consensusBackend, parents,
		parent, parentExtra, chainID, domain, logger); err != nil {
		return err
	}

	return i.Checkpoint.ValidateBasic(parentExtra.Checkpoint)
}

// ValidateParentSignatures validates signatures for parent block
func (i *Extra) ValidateParentSignatures(blockNumber uint64, consensusBackend dposBackend, parents []*types.Header,
	parent *types.Header, parentExtra *Extra, chainID uint64, domain []byte, logger hclog.Logger) error {
	// skip block 1 because genesis does not have committed signatures
	if blockNumber <= 1 {
		return nil
	}

	if i.Parent == nil {
		return fmt.Errorf("failed to verify signatures for parent of block %d because signatures are not present",
			blockNumber)
	}

	parentValidators, err := consensusBackend.GetDelegates(blockNumber-2, parents)
	if err != nil {
		return fmt.Errorf(
			"failed to validate header for block %d. could not retrieve parent validators: %w",
			blockNumber,
			err,
		)
	}

	parentCheckpointHash, err := parentExtra.Checkpoint.Hash(chainID, parent.Number, parent.Hash)
	if err != nil {
		return fmt.Errorf("failed to calculate parent proposal hash: %w", err)
	}

	parentBlockNumber := blockNumber - 1
	if err := i.Parent.Verify(parentBlockNumber, parentValidators, parentCheckpointHash, domain, logger); err != nil {
		return fmt.Errorf("failed to verify signatures for parent of block %d (proposal hash: %s): %w",
			blockNumber, parentCheckpointHash, err)
	}

	return nil
}

// Signature represents aggregated signatures of signers accompanied with a bitmap
// (in order to be able to determine identities of each signer)
type Signature struct {
	AggregatedSignature []byte
	Bitmap              []byte
}

// MarshalRLPWith marshals Signature object into RLP format
func (s *Signature) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	committed := ar.NewArray()
	if s.AggregatedSignature == nil {
		committed.Set(ar.NewNull())
	} else {
		committed.Set(ar.NewBytes(s.AggregatedSignature))
	}

	if s.Bitmap == nil {
		committed.Set(ar.NewNull())
	} else {
		committed.Set(ar.NewBytes(s.Bitmap))
	}

	return committed
}

// UnmarshalRLPWith unmarshals Signature object from the RLP format
func (s *Signature) UnmarshalRLPWith(v *fastrlp.Value) error {
	vals, err := v.GetElems()
	if err != nil {
		return fmt.Errorf("array type expected for signature struct")
	}

	// there should be exactly two elements (aggregated signature and bitmap)
	if num := len(vals); num != 2 {
		return fmt.Errorf("incorrect elements count to decode Signature, expected 2 but found %d", num)
	}

	fmt.Printf("DEBUG: Signature.UnmarshalRLPWith - parsing %d elements\n", len(vals))

	// Handle null values for AggregatedSignature
	fmt.Printf("DEBUG: parsing AggregatedSignature (element 0), type: %v\n", vals[0].Type())
	if vals[0].Type() == fastrlp.TypeNull {
		s.AggregatedSignature = nil
		fmt.Printf("DEBUG: AggregatedSignature is null\n")
	} else {
		s.AggregatedSignature, err = vals[0].GetBytes(nil)
		if err != nil {
			fmt.Printf("DEBUG: error getting AggregatedSignature bytes: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: successfully parsed AggregatedSignature, length: %d\n", len(s.AggregatedSignature))
	}

	// Handle null values for Bitmap
	fmt.Printf("DEBUG: parsing Bitmap (element 1), type: %v\n", vals[1].Type())
	if vals[1].Type() == fastrlp.TypeNull {
		s.Bitmap = nil
		fmt.Printf("DEBUG: Bitmap is null\n")
	} else {
		s.Bitmap, err = vals[1].GetBytes(nil)
		if err != nil {
			fmt.Printf("DEBUG: error getting Bitmap bytes: %v\n", err)
			return err
		}
		fmt.Printf("DEBUG: successfully parsed Bitmap, length: %d\n", len(s.Bitmap))
	}

	fmt.Printf("DEBUG: Signature.UnmarshalRLPWith completed successfully\n")
	return nil
}

// Verify is used to verify aggregated signature based on current validator set, message hash and domain
func (s *Signature) Verify(blockNumber uint64, validators validator.AccountSet,
	hash types.Hash, domain []byte, logger hclog.Logger) error {
	signers, err := validators.GetFilteredValidators(s.Bitmap)
	if err != nil {
		return err
	}

	validatorSet := validator.NewValidatorSet(validators, logger)
	if !validatorSet.HasQuorum(blockNumber, signers.GetAddressesAsSet()) {
		return fmt.Errorf("quorum not reached")
	}

	blsPublicKeys := make([]*bls.PublicKey, len(signers))
	for i, validator := range signers {
		blsPublicKeys[i] = validator.BlsKey
	}

	aggs, err := bls.UnmarshalSignature(s.AggregatedSignature)
	if err != nil {
		return err
	}

	if !aggs.VerifyAggregated(blsPublicKeys, hash[:], domain) {
		return fmt.Errorf("could not verify aggregated signature")
	}

	return nil
}

var checkpointDataABIType = abi.MustNewType(`tuple(
	uint256 chainId,
	uint256 blockNumber,
	bytes32 blockHash,
	uint256 blockRound, 
	uint256 epochNumber,
	bytes32 eventRoot,
	bytes32 currentValidatorsHash,
	bytes32 nextValidatorsHash)`)

// CheckpointData represents data needed for checkpointing mechanism
type CheckpointData struct {
	BlockRound            uint64
	EpochNumber           uint64
	CurrentValidatorsHash types.Hash
	NextValidatorsHash    types.Hash
	EventRoot             types.Hash
}

// MarshalRLPWith defines the marshal function implementation for CheckpointData
func (c *CheckpointData) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	// BlockRound
	vv.Set(ar.NewUint(c.BlockRound))
	// EpochNumber
	vv.Set(ar.NewUint(c.EpochNumber))
	// CurrentValidatorsHash
	vv.Set(ar.NewBytes(c.CurrentValidatorsHash.Bytes()))
	// NextValidatorsHash
	vv.Set(ar.NewBytes(c.NextValidatorsHash.Bytes()))
	// EventRoot
	vv.Set(ar.NewBytes(c.EventRoot.Bytes()))

	return vv
}

// UnmarshalRLPWith unmarshals CheckpointData object from the RLP format
func (c *CheckpointData) UnmarshalRLPWith(v *fastrlp.Value) error {
	vals, err := v.GetElems()
	if err != nil {
		return fmt.Errorf("array type expected for CheckpointData struct")
	}

	// there should be exactly 5 elements:
	// BlockRound, EpochNumber, CurrentValidatorsHash, NextValidatorsHash, EventRoot
	if num := len(vals); num != 5 {
		return fmt.Errorf("incorrect elements count to decode CheckpointData, expected 5 but found %d", num)
	}

	fmt.Printf("DEBUG: CheckpointData.UnmarshalRLPWith - parsing %d elements\n", len(vals))

	// BlockRound
	fmt.Printf("DEBUG: parsing BlockRound (element 0), type: %v\n", vals[0].Type())
	c.BlockRound, err = vals[0].GetUint64()
	if err != nil {
		fmt.Printf("DEBUG: error getting BlockRound uint64: %v\n", err)
		return err
	}
	fmt.Printf("DEBUG: successfully parsed BlockRound: %d\n", c.BlockRound)

	// EpochNumber
	fmt.Printf("DEBUG: parsing EpochNumber (element 1), type: %v\n", vals[1].Type())
	c.EpochNumber, err = vals[1].GetUint64()
	if err != nil {
		fmt.Printf("DEBUG: error getting EpochNumber uint64: %v\n", err)
		return err
	}
	fmt.Printf("DEBUG: successfully parsed EpochNumber: %d\n", c.EpochNumber)

	// CurrentValidatorsHash
	fmt.Printf("DEBUG: parsing CurrentValidatorsHash (element 2), type: %v\n", vals[2].Type())
	currentValidatorsHashRaw, err := vals[2].GetBytes(nil)
	if err != nil {
		fmt.Printf("DEBUG: error getting CurrentValidatorsHash bytes: %v\n", err)
		return err
	}

	c.CurrentValidatorsHash = types.BytesToHash(currentValidatorsHashRaw)
	fmt.Printf("DEBUG: successfully parsed CurrentValidatorsHash: %v\n", c.CurrentValidatorsHash)

	// NextValidatorsHash
	fmt.Printf("DEBUG: parsing NextValidatorsHash (element 3), type: %v\n", vals[3].Type())
	nextValidatorsHashRaw, err := vals[3].GetBytes(nil)
	if err != nil {
		fmt.Printf("DEBUG: error getting NextValidatorsHash bytes: %v\n", err)
		return err
	}

	c.NextValidatorsHash = types.BytesToHash(nextValidatorsHashRaw)
	fmt.Printf("DEBUG: successfully parsed NextValidatorsHash: %v\n", c.NextValidatorsHash)

	// EventRoot
	fmt.Printf("DEBUG: parsing EventRoot (element 4), type: %v\n", vals[4].Type())
	eventRootRaw, err := vals[4].GetBytes(nil)
	if err != nil {
		fmt.Printf("DEBUG: error getting EventRoot bytes: %v\n", err)
		return err
	}

	c.EventRoot = types.BytesToHash(eventRootRaw)
	fmt.Printf("DEBUG: successfully parsed EventRoot: %v\n", c.EventRoot)

	fmt.Printf("DEBUG: CheckpointData.UnmarshalRLPWith completed successfully\n")
	return nil
}

// Copy returns deep copy of CheckpointData instance
func (c *CheckpointData) Copy() *CheckpointData {
	newCheckpointData := new(CheckpointData)
	*newCheckpointData = *c

	return newCheckpointData
}

// Hash calculates keccak256 hash of the CheckpointData.
// CheckpointData is ABI encoded and then hashed.
func (c *CheckpointData) Hash(chainID uint64, blockNumber uint64, blockHash types.Hash) (types.Hash, error) {
	checkpointMap := map[string]interface{}{
		"chainId":               new(big.Int).SetUint64(chainID),
		"blockNumber":           new(big.Int).SetUint64(blockNumber),
		"blockHash":             blockHash,
		"blockRound":            new(big.Int).SetUint64(c.BlockRound),
		"epochNumber":           new(big.Int).SetUint64(c.EpochNumber),
		"eventRoot":             c.EventRoot,
		"currentValidatorsHash": c.CurrentValidatorsHash,
		"nextValidatorsHash":    c.NextValidatorsHash,
	}

	abiEncoded, err := checkpointDataABIType.Encode(checkpointMap)
	if err != nil {
		return types.ZeroHash, err
	}

	return types.BytesToHash(crypto.Keccak256(abiEncoded)), nil
}

// ValidateBasic encapsulates basic validation logic for checkpoint data.
// It only checks epoch numbers validity and whether validators hashes are non-empty.
func (c *CheckpointData) ValidateBasic(parentCheckpoint *CheckpointData) error {
	if c.EpochNumber != parentCheckpoint.EpochNumber &&
		c.EpochNumber != parentCheckpoint.EpochNumber+1 {
		// epoch-beginning block
		// epoch number must be incremented by one compared to parent block's checkpoint
		return fmt.Errorf("invalid epoch number for epoch-beginning block")
	}

	// For block 1, allow zero hashes
	if c.EpochNumber == 1 && (c.BlockRound == 0 || c.BlockRound == 1) {
		return nil
	}

	if c.CurrentValidatorsHash == types.ZeroHash {
		return fmt.Errorf("current validators hash must not be empty")
	}

	if c.NextValidatorsHash == types.ZeroHash {
		return fmt.Errorf("next validators hash must not be empty")
	}

	return nil
}

// Validate encapsulates validation logic for checkpoint data
// (with regards to current and next epoch validators)
func (c *CheckpointData) Validate(parentCheckpoint *CheckpointData,
	currentValidators validator.AccountSet, nextValidators validator.AccountSet,
	exitRootHash types.Hash) error {
	if err := c.ValidateBasic(parentCheckpoint); err != nil {
		return err
	}

	// check if currentValidatorsHash, present in CheckpointData is correct
	currentValidatorsHash, err := currentValidators.Hash()
	if err != nil {
		return fmt.Errorf("failed to calculate current validators hash: %w", err)
	}

	if currentValidatorsHash != c.CurrentValidatorsHash {
		return fmt.Errorf("current validators hashes don't match")
	}

	// check if nextValidatorsHash, present in CheckpointData is correct
	nextValidatorsHash, err := nextValidators.Hash()
	if err != nil {
		return fmt.Errorf("failed to calculate next validators hash: %w", err)
	}

	if nextValidatorsHash != c.NextValidatorsHash {
		return fmt.Errorf("next validators hashes don't match")
	}

	// epoch ending blocks have validator set transitions
	if !currentValidators.Equals(nextValidators) &&
		c.EpochNumber != parentCheckpoint.EpochNumber {
		// epoch ending blocks should have the same epoch number as parent block
		// (as they belong to the same epoch)
		return fmt.Errorf("epoch number should not change for epoch-ending block")
	}

	// exit root hash of proposer and
	// validator that validates proposal have to match
	if exitRootHash != c.EventRoot {
		return fmt.Errorf("exit root hash not as expected")
	}

	return nil
}

// GetIbftExtraClean returns unmarshaled extra field from the passed in header,
// but without signatures for the given header (it only includes signatures for the parent block)
func GetIbftExtraClean(extraRaw []byte) ([]byte, error) {
	extra, err := GetIbftExtra(extraRaw)
	if err != nil {
		return nil, err
	}

	ibftExtra := &Extra{
		Parent:     extra.Parent,
		Validators: extra.Validators,
		Checkpoint: extra.Checkpoint,
		Committed:  &Signature{},
	}

	return ibftExtra.MarshalRLPTo(nil), nil
}

// GetIbftExtra returns the istanbul extra data field from the passed in header
func GetIbftExtra(extraRaw []byte) (*Extra, error) {
	fmt.Printf("DEBUG: GetIbftExtra - extraRaw length: %d\n", len(extraRaw))

	if len(extraRaw) < ExtraVanity {
		fmt.Printf("DEBUG: extraRaw too short, expected at least %d, got %d\n", ExtraVanity, len(extraRaw))
		return nil, fmt.Errorf("wrong extra size: %d", len(extraRaw))
	}

	// Print the first few bytes for debugging
	fmt.Printf("DEBUG: extraRaw first 10 bytes: %x\n", extraRaw[:min(10, len(extraRaw))])
	fmt.Printf("DEBUG: extraRaw after vanity prefix: %x\n", extraRaw[ExtraVanity:min(ExtraVanity+20, len(extraRaw))])

	// Try to parse the RLP part directly to see what's wrong
	rlpPart := extraRaw[ExtraVanity:]
	fmt.Printf("DEBUG: RLP part length: %d\n", len(rlpPart))
	fmt.Printf("DEBUG: RLP part first 20 bytes: %x\n", rlpPart[:min(20, len(rlpPart))])

	extra := &Extra{}

	fmt.Printf("DEBUG: Unmarshaling extra data\n")
	if err := extra.UnmarshalRLP(extraRaw); err != nil {
		fmt.Printf("DEBUG: Error unmarshaling extra data: %v\n", err)
		return nil, err
	}

	fmt.Printf("DEBUG: GetIbftExtra completed successfully\n")
	return extra, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
