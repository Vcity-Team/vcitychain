package store

import (
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/Vcity-Team/vcitychain/validators"
)

// ValidatorStore defines the interface for validator storage
type ValidatorStore interface {
	// SourceType returns the type of validator source
	SourceType() SourceType

	// GetValidators returns the current validator set
	GetValidators() validators.Validators

	// SetValidators sets the current validator set
	SetValidators(validators.Validators) error

	// GetValidator returns a specific validator by address
	GetValidator(addr types.Address) (validators.Validator, bool)

	// AddValidator adds a validator to the store
	AddValidator(validator validators.Validator) error

	// RemoveValidator removes a validator from the store
	RemoveValidator(addr types.Address) error

	// Close closes the store
	Close() error
}
