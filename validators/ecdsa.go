package validators

import (
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/umbracle/fastrlp"
)

// BLSValidator is a validator using ECDSA signing algorithm
type ECDSAValidator struct {
	Address      types.Address
	BLSPublicKey []byte // 新增：BLS公钥字段，用于未来DPoS支持
}

// NewECDSAValidator is a constructor of ECDSAValidator
func NewECDSAValidator(addr types.Address) *ECDSAValidator {
	return &ECDSAValidator{
		Address: addr,
	}
}

// NewECDSAValidatorWithBLS is a constructor of ECDSAValidator with BLS public key
func NewECDSAValidatorWithBLS(addr types.Address, blsPubkey []byte) *ECDSAValidator {
	return &ECDSAValidator{
		Address:      addr,
		BLSPublicKey: blsPubkey,
	}
}

// Type returns the ValidatorType of ECDSAValidator
func (v *ECDSAValidator) Type() ValidatorType {
	return ECDSAValidatorType
}

// String returns string representation of ECDSAValidator
func (v *ECDSAValidator) String() string {
	return v.Address.String()
}

// Addr returns the validator address
func (v *ECDSAValidator) Addr() types.Address {
	return v.Address
}

// Copy returns copy of ECDSAValidator
func (v *ECDSAValidator) Copy() Validator {
	blsPubkey := make([]byte, len(v.BLSPublicKey))
	copy(blsPubkey, v.BLSPublicKey)

	return &ECDSAValidator{
		Address:      v.Address,
		BLSPublicKey: blsPubkey,
	}
}

// Equal checks the given validator matches with its data
func (v *ECDSAValidator) Equal(vr Validator) bool {
	vv, ok := vr.(*ECDSAValidator)
	if !ok {
		return false
	}
	return v.Address == vv.Address && string(v.BLSPublicKey) == string(vv.BLSPublicKey)
}

// MarshalRLPWith is a RLP Marshaller
func (v *ECDSAValidator) MarshalRLPWith(arena *fastrlp.Arena) *fastrlp.Value {
	// 如果有BLS公钥，使用数组格式；否则使用简单的地址格式
	if len(v.BLSPublicKey) > 0 {
		list := arena.NewArray()
		list.Set(arena.NewBytes(v.Address.Bytes()))
		list.Set(arena.NewBytes(v.BLSPublicKey))
		return list
	}
	return arena.NewBytes(v.Address.Bytes())
}

// UnmarshalRLPFrom is a RLP Unmarshaller
func (v *ECDSAValidator) UnmarshalRLPFrom(p *fastrlp.Parser, val *fastrlp.Value) error {
	// 尝试解析为数组格式（包含BLS公钥）
	elems, err := val.GetElems()
	if err == nil && len(elems) >= 2 {
		// 数组格式：[address, blsPublicKey]
		if err := elems[0].GetAddr(v.Address[:]); err != nil {
			return err
		}
		if v.BLSPublicKey, err = elems[1].GetBytes(v.BLSPublicKey); err != nil {
			return err
		}
		return nil
	}

	return val.GetAddr(v.Address[:])
}

// Bytes returns bytes of ECDSAValidator
func (v *ECDSAValidator) Bytes() []byte {
	return v.Address.Bytes()
}

// SetFromBytes parses given bytes
func (v *ECDSAValidator) SetFromBytes(input []byte) error {
	// 尝试RLP解析
	return types.UnmarshalRlp(v.UnmarshalRLPFrom, input)
}
