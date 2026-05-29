package signer

import (
	"crypto/ecdsa"
	"fmt"
	"testing"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/helper/keccak"
	"github.com/Vcity-Team/vcitychain/secrets"
	"github.com/Vcity-Team/vcitychain/secrets/helper"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
	"github.com/coinbase/kryptology/pkg/signatures/bls/bls_sig"
	"github.com/umbracle/fastrlp"
)

// HeaderHashLegacyIBFT computes the block hash for pre-DPoS Istanbul/IBFT headers.
// Used when serving historical blocks after the node switches to DPoS header hashing.
func HeaderHashLegacyIBFT(header *types.Header) (types.Hash, error) {
	extra, err := unmarshalIBFTExtraFromHeader(header)
	if err != nil {
		return types.ZeroHash, err
	}

	filtered := filterHeaderForHashLegacy(header, extra)

	return calculateHeaderHash(filtered), nil
}

func unmarshalIBFTExtraFromHeader(header *types.Header) (*IstanbulExtra, error) {
	if err := verifyIBFTExtraSize(header); err != nil {
		return nil, err
	}

	data := header.ExtraData[IstanbulExtraVanity:]

	tryUnmarshal := func(useBLS bool) (*IstanbulExtra, error) {
		extra := &IstanbulExtra{
			ProposerSeal: []byte{},
		}

		if useBLS {
			extra.Validators = validators.NewBLSValidatorSet()
			extra.CommittedSeals = &AggregatedSeal{}
			if header.Number > 1 {
				extra.ParentCommittedSeals = &AggregatedSeal{}
			}
		} else {
			extra.Validators = validators.NewECDSAValidatorSet()
			extra.CommittedSeals = &SerializedSeal{}
			if header.Number > 1 {
				extra.ParentCommittedSeals = &SerializedSeal{}
			}
		}

		if err := extra.UnmarshalRLP(data); err != nil {
			return nil, err
		}

		return extra, nil
	}

	if extra, err := tryUnmarshal(false); err == nil {
		return extra, nil
	}

	if extra, err := tryUnmarshal(true); err == nil {
		return extra, nil
	}

	return nil, fmt.Errorf("cannot unmarshal istanbul extra")
}

func filterHeaderForHashLegacy(header *types.Header, extra *IstanbulExtra) *types.Header {
	clone := header.Copy()

	parentCommittedSeals := extra.ParentCommittedSeals
	if parentCommittedSeals != nil && parentCommittedSeals.Num() == 0 {
		parentCommittedSeals = nil
	}

	var emptyCommittedSeals Seals
	switch extra.CommittedSeals.(type) {
	case *AggregatedSeal:
		emptyCommittedSeals = &AggregatedSeal{}
	default:
		emptyCommittedSeals = &SerializedSeal{}
	}

	putIbftExtra(clone, &IstanbulExtra{
		Validators:           extra.Validators,
		ProposerSeal:         []byte{},
		CommittedSeals:       emptyCommittedSeals,
		ParentCommittedSeals: parentCommittedSeals,
	})

	return clone
}

const (
	// legacyCommitCode is the value that is contained in
	// legacy committed seals, so it needs to be preserved in order
	// for new clients to read old committed seals
	legacyCommitCode = 2
)

// wrapCommitHash calculates digest for CommittedSeal
func wrapCommitHash(data []byte) []byte {
	return crypto.Keccak256(data, []byte{byte(legacyCommitCode)})
}

// getOrCreateECDSAKey loads ECDSA key or creates a new key
func getOrCreateECDSAKey(manager secrets.SecretsManager) (*ecdsa.PrivateKey, error) {
	if !manager.HasSecret(secrets.ValidatorKey) {
		if _, err := helper.InitECDSAValidatorKey(manager); err != nil {
			return nil, err
		}
	}

	keyBytes, err := manager.GetSecret(secrets.ValidatorKey)
	if err != nil {
		return nil, err
	}

	return crypto.BytesToECDSAPrivateKey(keyBytes)
}

// getOrCreateECDSAKey loads BLS key or creates a new key
func getOrCreateBLSKey(manager secrets.SecretsManager) (*bls_sig.SecretKey, error) {
	if !manager.HasSecret(secrets.ValidatorBLSKey) {
		if _, err := helper.InitBLSValidatorKey(manager); err != nil {
			return nil, err
		}
	}

	keyBytes, err := manager.GetSecret(secrets.ValidatorBLSKey)
	if err != nil {
		return nil, err
	}

	return crypto.BytesToBLSSecretKey(keyBytes)
}

// calculateHeaderHash is hash calculation of header for IBFT
func calculateHeaderHash(h *types.Header) types.Hash {
	arena := fastrlp.DefaultArenaPool.Get()
	defer fastrlp.DefaultArenaPool.Put(arena)

	vv := arena.NewArray()
	vv.Set(arena.NewBytes(h.ParentHash.Bytes()))
	vv.Set(arena.NewBytes(h.Sha3Uncles.Bytes()))
	vv.Set(arena.NewCopyBytes(h.Miner))
	vv.Set(arena.NewBytes(h.StateRoot.Bytes()))
	vv.Set(arena.NewBytes(h.TxRoot.Bytes()))
	vv.Set(arena.NewBytes(h.ReceiptsRoot.Bytes()))
	vv.Set(arena.NewBytes(h.LogsBloom[:]))
	vv.Set(arena.NewUint(h.Difficulty))
	vv.Set(arena.NewUint(h.Number))
	vv.Set(arena.NewUint(h.GasLimit))
	vv.Set(arena.NewUint(h.GasUsed))
	vv.Set(arena.NewUint(h.Timestamp))
	vv.Set(arena.NewCopyBytes(h.ExtraData))

	buf := keccak.Keccak256Rlp(nil, vv)

	return types.BytesToHash(buf)
}

// ecrecover recovers signer address from the given digest and signature
func ecrecover(sig, msg []byte) (types.Address, error) {
	pub, err := crypto.RecoverPubkey(sig, msg)
	if err != nil {
		return types.Address{}, err
	}

	return crypto.PubKeyToAddress(pub), nil
}

// NewKeyManagerFromType creates KeyManager based on the given type
func NewKeyManagerFromType(
	secretManager secrets.SecretsManager,
	validatorType validators.ValidatorType,
) (KeyManager, error) {
	switch validatorType {
	case validators.ECDSAValidatorType:
		return NewECDSAKeyManager(secretManager)
	case validators.BLSValidatorType:
		return NewBLSKeyManager(secretManager)
	default:
		return nil, fmt.Errorf("unsupported validator type: %s", validatorType)
	}
}

// verifyIBFTExtraSize checks whether header.ExtraData has enough size for IBFT Extra
func verifyIBFTExtraSize(header *types.Header) error {
	if len(header.ExtraData) < IstanbulExtraVanity {
		return fmt.Errorf(
			"wrong extra size, expected greater than or equal to %d but actual %d",
			IstanbulExtraVanity,
			len(header.ExtraData),
		)
	}

	return nil
}

// UseIstanbulHeaderHashInTest is a helper function for the test
func UseIstanbulHeaderHashInTest(t *testing.T, signer Signer) {
	t.Helper()

	originalHashCalc := types.HeaderHash
	types.HeaderHash = func(h *types.Header) types.Hash {
		hash, err := signer.CalculateHeaderHash(h)
		if err != nil {
			return types.ZeroHash
		}

		return hash
	}

	t.Cleanup(func() {
		types.HeaderHash = originalHashCalc
	})
}
