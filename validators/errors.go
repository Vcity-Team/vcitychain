package validators

import "errors"

var (
	ErrInvalidValidatorType   = errors.New("invalid validator type")
	ErrMismatchValidatorType  = errors.New("mismatch between validator and validators")
	ErrMismatchValidatorsType = errors.New("mismatch between two validators")
	ErrValidatorAlreadyExists = errors.New("validator already exists in validators")
	ErrValidatorNotFound      = errors.New("validator not found in validators")
	ErrInvalidValidators      = errors.New("invalid validators")
)
