package signer

import (
	"errors"
	"fmt"

	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
	"github.com/umbracle/fastrlp"
)

var (
	ErrEmptyCommittedSeals        = errors.New("empty committed seals")
	ErrEmptyParentCommittedSeals  = errors.New("empty parent committed seals")
	ErrInvalidCommittedSealLength = errors.New("invalid committed seal length")
	ErrInvalidCommittedSealType   = errors.New("invalid committed seal type")
	ErrRepeatedCommittedSeal      = errors.New("repeated seal in committed seals")
	ErrNonValidatorCommittedSeal  = errors.New("found committed seal signed by non validator")
	ErrNotEnoughCommittedSeals    = errors.New("not enough seals to seal block")
	ErrSignerMismatch             = errors.New("mismatch address between signer and message sender")
	ErrValidatorNotFound          = errors.New("validator not found in validator set")
	ErrInvalidValidators          = errors.New("invalid validators type")
	ErrInvalidValidator           = errors.New("invalid validator type")
	ErrInvalidSignature           = errors.New("invalid signature")
)

// Signer is responsible for signing for blocks and messages in IBFT
type Signer interface {
	Type() validators.ValidatorType
	Address() types.Address

	// IBFT Extra
	InitIBFTExtra(*types.Header, validators.Validators, Seals)
	GetIBFTExtra(*types.Header) (*IstanbulExtra, error)
	GetValidators(*types.Header) (validators.Validators, error)

	// ProposerSeal
	WriteProposerSeal(*types.Header) (*types.Header, error)
	EcrecoverFromHeader(*types.Header) (types.Address, error)

	// CommittedSeal
	CreateCommittedSeal([]byte) ([]byte, error)
	VerifyCommittedSeal(validators.Validators, types.Address, []byte, []byte) error

	// CommittedSeals
	WriteCommittedSeals(
		header *types.Header,
		roundNumber uint64,
		sealMap map[types.Address][]byte,
	) (*types.Header, error)
	VerifyCommittedSeals(
		hash types.Hash,
		committedSeals Seals,
		validators validators.Validators,
		quorumSize int,
	) error

	// ParentCommittedSeals
	VerifyParentCommittedSeals(
		parentHash types.Hash,
		header *types.Header,
		parentValidators validators.Validators,
		quorum int,
		mustExist bool,
	) error

	// IBFTMessage
	SignIBFTMessage([]byte) ([]byte, error)
	EcrecoverFromIBFTMessage([]byte, []byte) (types.Address, error)

	// Hash of Header
	CalculateHeaderHash(*types.Header) (types.Hash, error)
	FilterHeaderForHash(*types.Header) (*types.Header, error)
}

// SignerImpl is an implementation that meets Signer
type SignerImpl struct {
	keyManager           KeyManager
	parentKeyManager     KeyManager
	consensusSwitchHeight uint64 // 添加共识切换高度字段
}

// NewSigner is a constructor of SignerImpl
func NewSigner(
	keyManager KeyManager,
	parentKeyManager KeyManager,
	consensusSwitchHeight uint64,
) *SignerImpl {
	return &SignerImpl{
		keyManager:           keyManager,
		parentKeyManager:     parentKeyManager,
		consensusSwitchHeight: consensusSwitchHeight,
	}
}

// Type returns that validator type the signer expects
func (s *SignerImpl) Type() validators.ValidatorType {
	return s.keyManager.Type()
}

// Address returns the signer's address
func (s *SignerImpl) Address() types.Address {
	return s.keyManager.Address()
}

// InitIBFTExtra initializes the extra field in the given header
// based on given validators and parent committed seals
func (s *SignerImpl) InitIBFTExtra(
	header *types.Header,
	validators validators.Validators,
	parentCommittedSeals Seals,
) {
	s.initIbftExtra(
		header,
		validators,
		parentCommittedSeals,
	)
}

// GetIBFTExtra extracts IBFT Extra from the given header
func (s *SignerImpl) GetIBFTExtra(header *types.Header) (*IstanbulExtra, error) {
	if err := verifyIBFTExtraSize(header); err != nil {
		return nil, err
	}

	data := header.ExtraData[IstanbulExtraVanity:]
	extra := &IstanbulExtra{
		Validators:     s.keyManager.NewEmptyValidators(),
		ProposerSeal:   []byte{},
		CommittedSeals: s.keyManager.NewEmptyCommittedSeals(),
	}

	if header.Number > 1 {
		extra.ParentCommittedSeals = s.parentKeyManager.NewEmptyCommittedSeals()
	}

	// 检查是否在DPoS切换期间，如果是则使用兼容性解析
	if s.isDPoSTransition(header.Number) {
		fmt.Printf("🔍 DEBUG GetIBFTExtra: DPoS切换期间，使用兼容性解析 blockNumber=%d\n", header.Number)
		return s.parseDPoSCompatibleExtra(data, extra)
	}

	if err := extra.UnmarshalRLP(data); err != nil {
		return nil, err
	}

	return extra, nil
}

// isDPoSTransition 检查是否在DPoS切换期间
func (s *SignerImpl) isDPoSTransition(blockNumber uint64) bool {
	// 使用配置的切换高度，如果为0则表示不切换
	return s.consensusSwitchHeight > 0 && blockNumber >= s.consensusSwitchHeight
}

// parseDPoSCompatibleExtra 解析DPoS兼容的Extra数据
func (s *SignerImpl) parseDPoSCompatibleExtra(data []byte, extra *IstanbulExtra) (*IstanbulExtra, error) {
	fmt.Printf("🔍 DEBUG parseDPoSCompatibleExtra: 开始解析DPoS兼容Extra data长度=%d\n", len(data))
	
	// 尝试解析DPoS Extra格式
	parser := fastrlp.Parser{}
	val, err := parser.Parse(data)
	if err != nil {
		fmt.Printf("❌ DEBUG parseDPoSCompatibleExtra: RLP解析失败 %v\n", err)
		return nil, err
	}
	
	elems, err := val.GetElems()
	if err != nil {
		fmt.Printf("❌ DEBUG parseDPoSCompatibleExtra: 获取元素失败 %v\n", err)
		return nil, err
	}
	
	fmt.Printf("🔍 DEBUG parseDPoSCompatibleExtra: 解析出%d个元素\n", len(elems))
	
	// 对于DPoS区块，我们创建一个简化的IstanbulExtra
	// 只设置必要的字段，其他字段保持默认值
	extra.ProposerSeal = []byte{} // DPoS不使用ProposerSeal
	extra.CommittedSeals = s.keyManager.NewEmptyCommittedSeals()
	extra.ParentCommittedSeals = s.parentKeyManager.NewEmptyCommittedSeals()
	extra.RoundNumber = nil
	
	// 尝试从DPoS Extra中提取验证者信息
	if len(elems) > 0 {
		// 第一个元素可能是验证者信息
		if validatorElems, err := elems[0].GetElems(); err == nil {
			fmt.Printf("🔍 DEBUG parseDPoSCompatibleExtra: 验证者元素数量=%d\n", len(validatorElems))
			// 这里可以尝试解析验证者，但为了兼容性，我们暂时跳过
		}
	}
	
	fmt.Printf("✅ DEBUG parseDPoSCompatibleExtra: DPoS兼容解析完成\n")
	return extra, nil
}

// WriteProposerSeal signs and set ProposerSeal into IBFT Extra of the header
func (s *SignerImpl) WriteProposerSeal(header *types.Header) (*types.Header, error) {
	hash, err := s.CalculateHeaderHash(header)
	if err != nil {
		return nil, err
	}

	seal, err := s.keyManager.SignProposerSeal(
		crypto.Keccak256(hash.Bytes()),
	)
	if err != nil {
		return nil, err
	}

	header.ExtraData = packProposerSealIntoExtra(
		header.ExtraData,
		seal,
	)

	return header, nil
}

// EcrecoverFromIBFTMessage recovers signer address from given signature and header hash
func (s *SignerImpl) EcrecoverFromHeader(header *types.Header) (types.Address, error) {
	extra, err := s.GetIBFTExtra(header)
	if err != nil {
		return types.Address{}, err
	}

	return s.keyManager.Ecrecover(extra.ProposerSeal, crypto.Keccak256(header.Hash.Bytes()))
}

// CreateCommittedSeal returns CommittedSeal from given hash
func (s *SignerImpl) CreateCommittedSeal(hash []byte) ([]byte, error) {
	return s.keyManager.SignCommittedSeal(
		// Of course, this keccaking of an extended array is not according to the IBFT 2.0 spec,
		// but almost nothing in this legacy signing package is. This is kept
		// in order to preserve the running chains that used these
		// old (and very, very incorrect) signing schemes
		crypto.Keccak256(
			wrapCommitHash(hash[:]),
		),
	)
}

// CreateCommittedSeal verifies a CommittedSeal
func (s *SignerImpl) VerifyCommittedSeal(
	validators validators.Validators,
	signer types.Address,
	signature, hash []byte,
) error {
	return s.keyManager.VerifyCommittedSeal(
		validators,
		signer,
		signature,
		crypto.Keccak256(
			wrapCommitHash(hash[:]),
		),
	)
}

// WriteCommittedSeals builds and writes CommittedSeals into IBFT Extra of the header
func (s *SignerImpl) WriteCommittedSeals(
	header *types.Header,
	roundNumber uint64,
	sealMap map[types.Address][]byte,
) (*types.Header, error) {
	if len(sealMap) == 0 {
		return nil, ErrEmptyCommittedSeals
	}

	validators, err := s.GetValidators(header)
	if err != nil {
		return nil, err
	}

	committedSeal, err := s.keyManager.GenerateCommittedSeals(sealMap, validators)
	if err != nil {
		return nil, err
	}

	header.ExtraData = packCommittedSealsAndRoundNumberIntoExtra(
		header.ExtraData,
		committedSeal,
		&roundNumber,
	)

	return header, nil
}

// VerifyCommittedSeals verifies CommittedSeals in IBFT Extra of the header
func (s *SignerImpl) VerifyCommittedSeals(
	hash types.Hash,
	committedSeals Seals,
	validators validators.Validators,
	quorumSize int,
) error {
	rawMsg := crypto.Keccak256(
		wrapCommitHash(hash.Bytes()),
	)

	numSeals, err := s.keyManager.VerifyCommittedSeals(
		committedSeals,
		rawMsg,
		validators,
	)
	if err != nil {
		return err
	}

	if numSeals < quorumSize {
		return ErrNotEnoughCommittedSeals
	}

	return nil
}

// VerifyParentCommittedSeals verifies ParentCommittedSeals in IBFT Extra of the header
func (s *SignerImpl) VerifyParentCommittedSeals(
	parentHash types.Hash,
	header *types.Header,
	parentValidators validators.Validators,
	quorum int,
	mustExist bool,
) error {
	parentCommittedSeals, err := s.GetParentCommittedSeals(header)
	if err != nil {
		return err
	}

	if parentCommittedSeals == nil || parentCommittedSeals.Num() == 0 {
		// Throw error for the proposed header
		if mustExist {
			return ErrEmptyParentCommittedSeals
		}

		// Don't throw if the flag is unset for backward compatibility
		// (for the past headers)
		return nil
	}

	rawMsg := crypto.Keccak256(
		wrapCommitHash(parentHash[:]),
	)

	numSeals, err := s.keyManager.VerifyCommittedSeals(
		parentCommittedSeals,
		rawMsg,
		parentValidators,
	)
	if err != nil {
		return err
	}

	if numSeals < quorum {
		return ErrNotEnoughCommittedSeals
	}

	return nil
}

// SignIBFTMessage signs arbitrary message
func (s *SignerImpl) SignIBFTMessage(msg []byte) ([]byte, error) {
	return s.keyManager.SignIBFTMessage(crypto.Keccak256(msg))
}

// EcrecoverFromIBFTMessage recovers signer address from given signature and digest
func (s *SignerImpl) EcrecoverFromIBFTMessage(signature, digest []byte) (types.Address, error) {
	return s.keyManager.Ecrecover(signature, crypto.Keccak256(digest))
}

// InitIBFTExtra initializes the extra field
func (s *SignerImpl) initIbftExtra(
	header *types.Header,
	validators validators.Validators,
	parentCommittedSeal Seals,
) {
	putIbftExtra(header, &IstanbulExtra{
		Validators:           validators,
		ProposerSeal:         []byte{},
		CommittedSeals:       s.keyManager.NewEmptyCommittedSeals(),
		ParentCommittedSeals: parentCommittedSeal,
	})
}

// CalculateHeaderHash calculates header hash for IBFT Extra
func (s *SignerImpl) CalculateHeaderHash(header *types.Header) (types.Hash, error) {
	filteredHeader, err := s.FilterHeaderForHash(header)
	if err != nil {
		return types.ZeroHash, err
	}

	return calculateHeaderHash(filteredHeader), nil
}

func (s *SignerImpl) GetValidators(header *types.Header) (validators.Validators, error) {
	extra, err := s.GetIBFTExtra(header)
	if err != nil {
		return nil, err
	}

	return extra.Validators, nil
}

// GetParentCommittedSeals extracts Parent Committed Seals from IBFT Extra in Header
func (s *SignerImpl) GetParentCommittedSeals(header *types.Header) (Seals, error) {
	if err := verifyIBFTExtraSize(header); err != nil {
		return nil, err
	}

	data := header.ExtraData[IstanbulExtraVanity:]
	extra := &IstanbulExtra{
		ParentCommittedSeals: s.keyManager.NewEmptyCommittedSeals(),
	}

	if err := extra.unmarshalRLPForParentCS(data); err != nil {
		return nil, err
	}

	return extra.ParentCommittedSeals, nil
}

// filterHeaderForHash removes unnecessary fields from IBFT Extra of the header
// for hash calculation
func (s *SignerImpl) FilterHeaderForHash(header *types.Header) (*types.Header, error) {
	clone := header.Copy()

	extra, err := s.GetIBFTExtra(header)
	if err != nil {
		return nil, err
	}

	parentCommittedSeals := extra.ParentCommittedSeals
	if parentCommittedSeals != nil && parentCommittedSeals.Num() == 0 {
		// avoid to set ParentCommittedSeals in extra for hash calculation
		// in case of empty ParentCommittedSeals for backward compatibility
		parentCommittedSeals = nil
	}

	// This will effectively remove the Seal and CommittedSeals from the IBFT Extra of header,
	// while keeping proposer vanity, validator set, and ParentCommittedSeals
	s.initIbftExtra(clone, extra.Validators, parentCommittedSeals)

	return clone, nil
}
