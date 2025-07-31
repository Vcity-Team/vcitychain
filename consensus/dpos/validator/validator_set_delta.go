package validator

import (
	"bytes"
	"fmt"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/bitmap"
	"github.com/umbracle/fastrlp"
)

// ValidatorSetDelta holds information about added and removed validators compared to the previous epoch
type ValidatorSetDelta struct {
	// Added is the slice of added validators
	Added AccountSet
	// Updated is the slice of updated valiadtors
	Updated AccountSet
	// Removed is a bitmap of the validators removed from the set
	Removed bitmap.Bitmap
}

// Equals checks validator set delta equality
func (d *ValidatorSetDelta) Equals(other *ValidatorSetDelta) bool {
	if other == nil {
		return false
	}

	return d.Added.Equals(other.Added) &&
		d.Updated.Equals(other.Updated) &&
		bytes.Equal(d.Removed, other.Removed)
}

// MarshalRLPWith marshals ValidatorSetDelta to RLP format
func (d *ValidatorSetDelta) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	addedValidatorsRaw := ar.NewArray()
	updatedValidatorsRaw := ar.NewArray()

	for _, validatorAccount := range d.Added {
		addedValidatorsRaw.Set(validatorAccount.MarshalRLPWith(ar))
	}

	for _, validatorAccount := range d.Updated {
		updatedValidatorsRaw.Set(validatorAccount.MarshalRLPWith(ar))
	}

	vv.Set(addedValidatorsRaw)         // added
	vv.Set(updatedValidatorsRaw)       // updated
	vv.Set(ar.NewCopyBytes(d.Removed)) // removed

	return vv
}

// UnmarshalRLPWith unmarshals ValidatorSetDelta from RLP format
func (d *ValidatorSetDelta) UnmarshalRLPWith(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) == 0 {
		return nil
	} else if num := len(elems); num != 3 {
		return fmt.Errorf("incorrect elements count to decode validator set delta, expected 3 but found %d", num)
	}

	fmt.Printf("DEBUG: ValidatorSetDelta.UnmarshalRLPWith - parsing %d elements\n", len(elems))

	// Validators (added)
	{
		fmt.Printf("DEBUG: parsing Added validators (element 0), type: %v\n", elems[0].Type())
		if elems[0].Type() == fastrlp.TypeNull {
			d.Added = nil
			fmt.Printf("DEBUG: Added validators is null\n")
		} else {
			validatorsRaw, err := elems[0].GetElems()
			if err != nil {
				fmt.Printf("DEBUG: error getting Added validators elements: %v\n", err)
				return fmt.Errorf("array expected for added validators")
			}

			fmt.Printf("DEBUG: parsing %d Added validators\n", len(validatorsRaw))
			d.Added, err = unmarshalValidators(validatorsRaw)
			if err != nil {
				fmt.Printf("DEBUG: error parsing Added validators: %v\n", err)
				return err
			}
			fmt.Printf("DEBUG: successfully parsed Added validators\n")
		}
	}

	// Validators (updated)
	{
		fmt.Printf("DEBUG: parsing Updated validators (element 1), type: %v\n", elems[1].Type())
		if elems[1].Type() == fastrlp.TypeNull {
			d.Updated = nil
			fmt.Printf("DEBUG: Updated validators is null\n")
		} else {
			validatorsRaw, err := elems[1].GetElems()
			if err != nil {
				fmt.Printf("DEBUG: error getting Updated validators elements: %v\n", err)
				return fmt.Errorf("array expected for updated validators")
			}

			fmt.Printf("DEBUG: parsing %d Updated validators\n", len(validatorsRaw))
			d.Updated, err = unmarshalValidators(validatorsRaw)
			if err != nil {
				fmt.Printf("DEBUG: error parsing Updated validators: %v\n", err)
				return err
			}
			fmt.Printf("DEBUG: successfully parsed Updated validators\n")
		}
	}

	// Bitmap (removed)
	{
		fmt.Printf("DEBUG: parsing Removed bitmap (element 2), type: %v\n", elems[2].Type())
		if elems[2].Type() == fastrlp.TypeNull {
			d.Removed = nil
			fmt.Printf("DEBUG: Removed bitmap is null\n")
		} else {
			dst, err := elems[2].GetBytes(nil)
			if err != nil {
				fmt.Printf("DEBUG: error getting Removed bitmap bytes: %v\n", err)
				return err
			}

			d.Removed = bitmap.Bitmap(dst)
			fmt.Printf("DEBUG: successfully parsed Removed bitmap\n")
		}
	}

	fmt.Printf("DEBUG: ValidatorSetDelta.UnmarshalRLPWith completed successfully\n")
	return nil
}

// unmarshalValidators unmarshals RLP encoded validators and returns AccountSet instance
func unmarshalValidators(validatorsRaw []*fastrlp.Value) (AccountSet, error) {
	fmt.Printf("DEBUG: unmarshalValidators - parsing %d validators\n", len(validatorsRaw))

	if len(validatorsRaw) == 0 {
		fmt.Printf("DEBUG: no validators to parse\n")
		return nil, nil
	}

	validators := make(AccountSet, 0, len(validatorsRaw))

	for i, validatorRaw := range validatorsRaw {
		fmt.Printf("DEBUG: parsing validator %d, type: %v\n", i, validatorRaw.Type())

		// Skip null values
		if validatorRaw.Type() == fastrlp.TypeNull {
			fmt.Printf("DEBUG: skipping null validator %d\n", i)
			continue
		}

		acc := &ValidatorMetadata{}
		if err := acc.UnmarshalRLPWith(validatorRaw); err != nil {
			fmt.Printf("DEBUG: error parsing validator %d: %v\n", i, err)
			return nil, err
		}

		validators = append(validators, acc)
		fmt.Printf("DEBUG: successfully parsed validator %d\n", i)
	}

	fmt.Printf("DEBUG: unmarshalValidators completed, parsed %d validators\n", len(validators))
	return validators, nil
}

// IsEmpty returns indication whether delta is empty (namely added, updated slices and removed bitmap are empty)
func (d *ValidatorSetDelta) IsEmpty() bool {
	return len(d.Added) == 0 &&
		len(d.Updated) == 0 &&
		d.Removed.Len() == 0
}

// Copy creates deep copy of ValidatorSetDelta
func (d *ValidatorSetDelta) Copy() *ValidatorSetDelta {
	added := d.Added.Copy()
	removed := make([]byte, len(d.Removed))
	copy(removed, d.Removed)

	return &ValidatorSetDelta{Added: added, Removed: removed}
}

// fmt.Stringer interface implementation
func (d *ValidatorSetDelta) String() string {
	return fmt.Sprintf("Added: \n%v Removed: %v\n Updated: \n%v", d.Added, d.Removed, d.Updated)
}
