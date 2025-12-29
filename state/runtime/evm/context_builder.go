package evm

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// buildBlockContext 构建 BlockContext
func buildBlockContext(host runtime.Host, header *types.Header) vm.BlockContext {
	txCtx := host.GetTxContext()

	// 🔧 关键修复：设置 Random 字段以表示已经过了 The Merge
	// go-ethereum 的 NewEVM 使用 blockCtx.Random != nil 来判断 isMerge
	// 如果 Random 为 nil，isMerge 为 false，会导致 Rules.IsShanghai = false
	// 即使 IsShanghai(num, timestamp) 返回 true，Rules.IsShanghai 也需要 isMerge = true
	// 所以我们需要设置一个非 nil 的 Random 值（可以使用区块哈希或其他值）
	randomHash := host.GetBlockHash(int64(txCtx.Number))
	random := VcHashToCommon(randomHash)

	blockCtx := vm.BlockContext{
		CanTransfer: func(db vm.StateDB, addr common.Address, amount *uint256.Int) bool {
			balance := db.GetBalance(addr)
			return balance.Cmp(amount) >= 0
		},
		Transfer: func(db vm.StateDB, sender, recipient common.Address, amount *uint256.Int) {
			// 通过 StateDB 的 AddBalance 和 SubBalance 实现
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
		Random:      &random, // 🔧 设置 Random 以启用 isMerge，从而启用 Shanghai
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

	// 注意：go-ethereum 的 ChainConfig 使用区块号来标识 fork
	// 但 vcitychain 的 ForksInTime 是布尔值，表示是否激活
	// 这里我们需要一个默认的区块号映射
	// 实际使用时，应该从 chain.Params 中获取具体的区块号

	// 设置默认的 fork 区块号（如果 fork 激活，使用 0 表示从创世块开始）
	// 实际项目中应该从配置中读取具体的区块号
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

	// 🔧 关键修复：启用 Shanghai 升级以支持 PUSH0 操作码（EIP-3855）
	// Shanghai 是基于时间的升级，使用 ShanghaiTime 而不是 ShanghaiBlock
	// 设置为 0 表示从创世块开始启用（如果链已经支持 London，通常也应该支持 Shanghai）
	// 注意：如果链配置中没有明确禁用 Shanghai，我们默认启用它以支持最新的 EIP
	// 重要：ShanghaiTime 必须 <= 当前区块时间戳，IsShanghai 才会返回 true
	if config.London {
		// 如果 London 已启用，我们也启用 Shanghai（Shanghai 是 London 之后的升级）
		// 使用 0 作为时间戳，表示从创世块开始启用（任何时间戳 >= 0 都会激活）
		shanghaiTime := uint64(0)
		chainConfig.ShanghaiTime = &shanghaiTime
	}

	// 设置其他参数
	chainConfig.Ethash = nil // vcitychain 不使用 Ethash

	return chainConfig
}

// buildChainConfigFromParams 从 chain.Params 构建 ChainConfig（更准确的版本）
func buildChainConfigFromParams(chainParams *chain.Params, blockNumber uint64) *params.ChainConfig {
	chainConfig := &params.ChainConfig{
		ChainID: big.NewInt(chainParams.ChainID),
	}

	// 从 Forks 中获取具体的区块号
	forks := chainParams.Forks.At(blockNumber)

	// 如果 fork 激活，从 Forks map 中获取具体的区块号
	if forks.Homestead && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.Homestead]; exists {
			chainConfig.HomesteadBlock = big.NewInt(int64(fork.Block))
		}
	}
	if forks.EIP150 && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.EIP150]; exists {
			chainConfig.EIP150Block = big.NewInt(int64(fork.Block))
		}
	}
	if forks.EIP155 && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.EIP155]; exists {
			chainConfig.EIP155Block = big.NewInt(int64(fork.Block))
		}
	}
	if forks.EIP158 && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.EIP158]; exists {
			chainConfig.EIP158Block = big.NewInt(int64(fork.Block))
		}
	}
	if forks.Byzantium && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.Byzantium]; exists {
			chainConfig.ByzantiumBlock = big.NewInt(int64(fork.Block))
		}
	}
	if forks.Constantinople && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.Constantinople]; exists {
			chainConfig.ConstantinopleBlock = big.NewInt(int64(fork.Block))
		}
	}
	if forks.Petersburg && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.Petersburg]; exists {
			chainConfig.PetersburgBlock = big.NewInt(int64(fork.Block))
		}
	}
	if forks.Istanbul && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.Istanbul]; exists {
			chainConfig.IstanbulBlock = big.NewInt(int64(fork.Block))
		}
	}
	if forks.London && chainParams.Forks != nil {
		if fork, exists := (*chainParams.Forks)[chain.London]; exists {
			chainConfig.LondonBlock = big.NewInt(int64(fork.Block))
		}
	}

	return chainConfig
}
