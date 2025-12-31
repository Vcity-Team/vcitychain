package evm

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// buildBlockContext 构建 BlockContext
func buildBlockContext(host runtime.Host) vm.BlockContext {
	txCtx := host.GetTxContext()

	randomHash := host.GetBlockHash(int64(txCtx.Number))
	random := VcHashToCommon(randomHash)

	blockCtx := vm.BlockContext{
		CanTransfer: func(db vm.StateDB, addr common.Address, amount *uint256.Int) bool {
			balance := db.GetBalance(addr)
			return balance.Cmp(amount) >= 0
		},
		Transfer: func(db vm.StateDB, sender, recipient common.Address, amount *uint256.Int) {
			db.SubBalance(sender, amount)
			db.AddBalance(recipient, amount)
		},
		GetHash: func(blockNumber uint64) common.Hash {
			hash := host.GetBlockHash(int64(blockNumber))
			return VcHashToCommon(hash)
		},
		Coinbase:    VcAddressToCommon(txCtx.Coinbase),
		BlockNumber: new(big.Int).SetUint64(uint64(txCtx.Number)),
		Time:        uint64(txCtx.Timestamp),
		Difficulty:  new(big.Int).SetBytes(txCtx.Difficulty.Bytes()),
		GasLimit:    uint64(txCtx.GasLimit),
		BaseFee:     txCtx.BaseFee,
		Random:      &random,
	}

	return blockCtx
}

// buildTxContext 构建 TxContext
func buildTxContext(host runtime.Host) vm.TxContext {
	txCtx := host.GetTxContext()

	return vm.TxContext{
		Origin:   VcAddressToCommon(txCtx.Origin),
		GasPrice: new(big.Int).SetBytes(txCtx.GasPrice.Bytes()),
	}
}

// buildChainConfig 将 ForksInTime 转换为 params.ChainConfig
func buildChainConfig(config *chain.ForksInTime, chainID int64) *params.ChainConfig {
	chainConfig := &params.ChainConfig{
		ChainID: big.NewInt(chainID),
	}
	if config.Homestead {
		chainConfig.HomesteadBlock = big.NewInt(0)
	}
	if config.EIP150 {
		chainConfig.EIP150Block = big.NewInt(0)
	}
	if config.EIP155 {
		chainConfig.EIP155Block = big.NewInt(0)
	}
	if config.EIP158 {
		chainConfig.EIP158Block = big.NewInt(0)
	}
	if config.Byzantium {
		chainConfig.ByzantiumBlock = big.NewInt(0)
	}
	if config.Constantinople {
		chainConfig.ConstantinopleBlock = big.NewInt(0)
	}
	if config.Petersburg {
		chainConfig.PetersburgBlock = big.NewInt(0)
	}
	if config.Istanbul {
		chainConfig.IstanbulBlock = big.NewInt(0)
	}
	if config.London {
		chainConfig.LondonBlock = big.NewInt(0)
	}

	// 启用 Shanghai 升级以支持 PUSH0 操作码（EIP-3855）
	if config.London {
		shanghaiTime := uint64(0)
		chainConfig.ShanghaiTime = &shanghaiTime
	}
	chainConfig.Ethash = nil // vcitychain 不使用 Ethash

	return chainConfig
}
