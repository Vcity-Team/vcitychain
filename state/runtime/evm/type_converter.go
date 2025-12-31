package evm

import (
	"sync"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/common"
)

// 类型转换缓存，避免重复转换
var (
	addrCache sync.Map // map[types.Address]common.Address
	hashCache sync.Map // map[types.Hash]common.Hash
)

// VcAddressToCommon 将 vcitychain 的 Address 转换为 go-ethereum 的 Address
func VcAddressToCommon(addr types.Address) common.Address {
	// 检查缓存
	if cached, ok := addrCache.Load(addr); ok {
		return cached.(common.Address)
	}
	// 转换
	commonAddr := common.BytesToAddress(addr.Bytes())
	// 缓存结果
	addrCache.Store(addr, commonAddr)

	return commonAddr
}

// CommonAddressToVc 将 go-ethereum 的 Address 转换为 vcitychain 的 Address
func CommonAddressToVc(addr common.Address) types.Address {
	return types.BytesToAddress(addr.Bytes())
}

// VcHashToCommon 将 vcitychain 的 Hash 转换为 go-ethereum 的 Hash
func VcHashToCommon(hash types.Hash) common.Hash {
	// 检查缓存
	if cached, ok := hashCache.Load(hash); ok {
		return cached.(common.Hash)
	}
	// 转换
	commonHash := common.BytesToHash(hash.Bytes())
	// 缓存结果
	hashCache.Store(hash, commonHash)

	return commonHash
}

// CommonHashToVc 将 go-ethereum 的 Hash 转换为 vcitychain 的 Hash
func CommonHashToVc(hash common.Hash) types.Hash {
	return types.BytesToHash(hash.Bytes())
}
