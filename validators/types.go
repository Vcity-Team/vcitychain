package validators

import (
	"encoding/json"
	"fmt"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/umbracle/fastrlp"
)

// ValidatorType represents the type of validator
type ValidatorType uint8

const (
	ECDSAValidatorType ValidatorType = iota
	BLSValidatorType
)

// String returns the string representation of the validator type
func (vt ValidatorType) String() string {
	switch vt {
	case ECDSAValidatorType:
		return "ecdsa"
	case BLSValidatorType:
		return "bls"
	default:
		return "unknown"
	}
}

// UnmarshalJSON decodes JSON strings ("ecdsa", "bls") or numbers (0, 1) into ValidatorType.
func (vt *ValidatorType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, err := ParseValidatorType(s)
		if err != nil {
			return err
		}
		*vt = parsed
		return nil
	}
	var n uint8
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	switch ValidatorType(n) {
	case ECDSAValidatorType, BLSValidatorType:
		*vt = ValidatorType(n)
		return nil
	default:
		return fmt.Errorf("invalid validator type: %d", n)
	}
}

// Validator represents a validator in the network
type Validator interface {
	Type() ValidatorType
	Addr() types.Address
	Copy() Validator
	Equal(Validator) bool
	Bytes() []byte
	String() string
	SetFromBytes(data []byte) error
	MarshalRLPWith(arena *fastrlp.Arena) *fastrlp.Value
	UnmarshalRLPFrom(p *fastrlp.Parser, val *fastrlp.Value) error
}

// Validators represents a collection of validators
type Validators interface {
	Type() ValidatorType
	Len() int
	Add(Validator) error
	Del(types.Address) error
	GetValidator(types.Address) (Validator, bool)
	At(uint64) Validator
	Index(types.Address) int
	Copy() Validators
	Includes(types.Address) bool
	Equal(Validators) bool
	Merge(Validators) error
	MarshalRLPWith(arena *fastrlp.Arena) *fastrlp.Value
	UnmarshalRLPFrom(p *fastrlp.Parser, val *fastrlp.Value) error
}

// ValidatorSet represents a set of validators
type ValidatorSet struct {
	validators    []Validator
	validatorType ValidatorType
}

// NewValidatorSet creates a new validator set
func NewValidatorSet(validatorType ValidatorType) *ValidatorSet {
	return &ValidatorSet{
		validators:    make([]Validator, 0),
		validatorType: validatorType,
	}
}

// Type returns the validator type
func (vs *ValidatorSet) Type() ValidatorType {
	return vs.validatorType
}

// Len returns the number of validators
func (vs *ValidatorSet) Len() int {
	return len(vs.validators)
}

// Add adds a validator to the set
func (vs *ValidatorSet) Add(v Validator) error {
	if v.Type() != vs.validatorType {
		return ErrInvalidValidatorType
	}

	vs.validators = append(vs.validators, v)
	return nil
}

// Del removes a validator from the set
func (vs *ValidatorSet) Del(addr types.Address) error {
	for i, v := range vs.validators {
		if v.Addr() == addr {
			vs.validators = append(vs.validators[:i], vs.validators[i+1:]...)
			return nil
		}
	}
	return ErrValidatorNotFound
}

// GetValidator returns a validator by address
func (vs *ValidatorSet) GetValidator(addr types.Address) (Validator, bool) {
	for _, v := range vs.validators {
		if v.Addr() == addr {
			return v, true
		}
	}
	return nil, false
}

// At returns a validator at the given index
func (vs *ValidatorSet) At(index uint64) Validator {
	if index >= uint64(len(vs.validators)) {
		return nil
	}
	return vs.validators[index]
}

// Index returns the index of a validator by address
func (vs *ValidatorSet) Index(addr types.Address) int {
	for i, v := range vs.validators {
		if v.Addr() == addr {
			return i
		}
	}
	return -1
}

// Copy returns a copy of the validator set
func (vs *ValidatorSet) Copy() Validators {
	newSet := NewValidatorSet(vs.validatorType)
	for _, v := range vs.validators {
		newSet.Add(v.Copy())
	}
	return newSet
}

// Includes checks if a validator with the given address exists
func (vs *ValidatorSet) Includes(addr types.Address) bool {
	for _, v := range vs.validators {
		if v.Addr() == addr {
			return true
		}
	}
	return false
}

// Equal checks if two validator sets are equal
func (vs *ValidatorSet) Equal(other Validators) bool {
	if vs.Type() != other.Type() {
		return false
	}
	if vs.Len() != other.Len() {
		return false
	}

	for i := uint64(0); i < uint64(vs.Len()); i++ {
		if !vs.At(i).Equal(other.At(i)) {
			return false
		}
	}
	return true
}

// Merge merges another validator set into this one
func (vs *ValidatorSet) Merge(other Validators) error {
	if vs.Type() != other.Type() {
		return fmt.Errorf("cannot merge validator sets of different types")
	}

	for i := uint64(0); i < uint64(other.Len()); i++ {
		validator := other.At(i)
		if err := vs.Add(validator); err != nil {
			return err
		}
	}

	return nil
}

// MarshalRLPWith marshals the validator set to RLP
func (vs *ValidatorSet) MarshalRLPWith(arena *fastrlp.Arena) *fastrlp.Value {
	list := arena.NewArray()
	for _, v := range vs.validators {
		list.Set(v.MarshalRLPWith(arena))
	}
	return list
}

// UnmarshalRLPFrom unmarshals the validator set from RLP
func (vs *ValidatorSet) UnmarshalRLPFrom(p *fastrlp.Parser, val *fastrlp.Value) error {
	elems, err := val.GetElems()
	if err != nil {
		return err
	}

	vs.validators = make([]Validator, 0, len(elems))
	for _, elem := range elems {
		var validator Validator
		switch vs.validatorType {
		case ECDSAValidatorType:
			validator = &ECDSAValidator{}
		case BLSValidatorType:
			validator = &BLSValidator{}
		default:
			return fmt.Errorf("unknown validator type: %d", vs.validatorType)
		}

		if err := validator.UnmarshalRLPFrom(p, elem); err != nil {
			return err
		}
		vs.validators = append(vs.validators, validator)
	}
	return nil
}
