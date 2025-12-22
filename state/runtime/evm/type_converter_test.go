package evm

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

func TestVcAddressToCommon(t *testing.T) {
	vcAddr := types.StringToAddress("0x1234567890123456789012345678901234567890")
	commonAddr := VcAddressToCommon(vcAddr)

	assert.Equal(t, vcAddr.Bytes(), commonAddr.Bytes())

	// 测试缓存
	commonAddr2 := VcAddressToCommon(vcAddr)
	assert.Equal(t, commonAddr, commonAddr2)
}

func TestCommonAddressToVc(t *testing.T) {
	commonAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	vcAddr := CommonAddressToVc(commonAddr)

	assert.Equal(t, commonAddr.Bytes(), vcAddr.Bytes())
}

func TestVcHashToCommon(t *testing.T) {
	vcHash := types.StringToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	commonHash := VcHashToCommon(vcHash)

	assert.Equal(t, vcHash.Bytes(), commonHash.Bytes())

	// 测试缓存
	commonHash2 := VcHashToCommon(vcHash)
	assert.Equal(t, commonHash, commonHash2)
}

func TestCommonHashToVc(t *testing.T) {
	commonHash := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	vcHash := CommonHashToVc(commonHash)

	assert.Equal(t, commonHash.Bytes(), vcHash.Bytes())
}

func TestAddressRoundTrip(t *testing.T) {
	original := types.StringToAddress("0x1234567890123456789012345678901234567890")

	commonAddr := VcAddressToCommon(original)
	converted := CommonAddressToVc(commonAddr)

	assert.Equal(t, original, converted)
}

func TestHashRoundTrip(t *testing.T) {
	original := types.StringToHash("0x1234567890123456789012345678901234567890123456789012345678901234")

	commonHash := VcHashToCommon(original)
	converted := CommonHashToVc(commonHash)

	assert.Equal(t, original, converted)
}


