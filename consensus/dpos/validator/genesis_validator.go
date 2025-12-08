package validator

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/types"
)

// GenesisValidator represents public information about validator accounts which are the part of genesis
type GenesisValidator struct {
	Address   types.Address
	BlsKey    string
	Stake     *big.Int
	MultiAddr string
}

type genesisValidatorRaw struct {
	Address   types.Address `json:"address"`
	BlsKey    string        `json:"blsKey"`
	Stake     *string       `json:"stake"`
	MultiAddr string        `json:"multiAddr"`
}

func (v *GenesisValidator) MarshalJSON() ([]byte, error) {
	raw := &genesisValidatorRaw{Address: v.Address, BlsKey: v.BlsKey, MultiAddr: v.MultiAddr}
	raw.Stake = common.EncodeBigInt(v.Stake)

	return json.Marshal(raw)
}

func (v *GenesisValidator) UnmarshalJSON(data []byte) (err error) {
	var raw genesisValidatorRaw

	if err = json.Unmarshal(data, &raw); err != nil {
		return err
	}

	v.Address = raw.Address
	v.BlsKey = raw.BlsKey
	v.MultiAddr = raw.MultiAddr

	v.Stake, err = common.ParseUint256orHex(raw.Stake)

	return err
}

// UnmarshalBLSPublicKey unmarshals the hex encoded BLS public key
func (v *GenesisValidator) UnmarshalBLSPublicKey() (*bls.PublicKey, error) {
	// 添加调试日志
	fmt.Printf("🔍 UnmarshalBLSPublicKey: 开始解析地址=%s\n", v.Address.String())
	fmt.Printf("🔍 UnmarshalBLSPublicKey: 原始BlsKey长度=%d\n", len(v.BlsKey))
	fmt.Printf("🔍 UnmarshalBLSPublicKey: 原始BlsKey=%s\n", v.BlsKey)

	decoded, err := hex.DecodeString(v.BlsKey)
	if err != nil {
		fmt.Printf("❌ UnmarshalBLSPublicKey: hex解码失败 address=%s error=%v\n", v.Address.String(), err)
		return nil, err
	}

	fmt.Printf("🔍 UnmarshalBLSPublicKey: hex解码成功 address=%s decodedLength=%d\n", v.Address.String(), len(decoded))

	publicKey, err := bls.UnmarshalPublicKey(decoded)
	if err != nil {
		fmt.Printf("❌ UnmarshalBLSPublicKey: BLS公钥解析失败 address=%s error=%v\n", v.Address.String(), err)
		return nil, err
	}

	fmt.Printf("✅ UnmarshalBLSPublicKey: BLS公钥解析成功 address=%s publicKeyLength=%d\n", v.Address.String(), len(publicKey.Marshal()))
	return publicKey, nil
}

// ToValidatorMetadata creates ValidatorMetadata instance
func (v *GenesisValidator) ToValidatorMetadata() (*ValidatorMetadata, error) {
	fmt.Printf("🔍 ToValidatorMetadata: 开始转换地址=%s\n", v.Address.String())

	blsKey, err := v.UnmarshalBLSPublicKey()
	if err != nil {
		fmt.Printf("❌ ToValidatorMetadata: BLS公钥解析失败 address=%s error=%v\n", v.Address.String(), err)
		return nil, err
	}

	// 检查stake是否足够
	isActive := v.Stake.Cmp(big.NewInt(0)) > 0

	fmt.Printf("🔍 ToValidatorMetadata: 创建ValidatorMetadata address=%s blsKey=%v isActive=%v\n", v.Address.String(), blsKey != nil, isActive)

	metadata := &ValidatorMetadata{
		Address:     v.Address,
		BlsKey:      blsKey,
		VotingPower: new(big.Int).Set(v.Stake),
		IsActive:    isActive, // 根据stake设置活跃状态
	}

	fmt.Printf("✅ ToValidatorMetadata: 转换完成 address=%s finalBlsKey=%v\n", v.Address.String(), metadata.BlsKey != nil)
	return metadata, nil
}

// String implements fmt.Stringer interface
func (v *GenesisValidator) String() string {
	return fmt.Sprintf("Address=%s; Stake=%d; P2P Multi addr=%s; BLS Key=%s;",
		v.Address, v.Stake, v.MultiAddr, v.BlsKey)
}
