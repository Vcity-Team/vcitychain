package jsonrpc

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

var (
	ErrHeaderNotFound           = errors.New("header not found")
	ErrLatestNotFound           = errors.New("latest header not found")
	ErrNegativeBlockNumber      = errors.New("invalid argument 0: block number must not be negative")
	ErrFailedFetchGenesis       = errors.New("error fetching genesis block header")
	ErrNoDataInContractCreation = errors.New("contract creation without data provided")
)

type latestHeaderGetter interface {
	Header() *types.Header
}

// GetNumericBlockNumber returns block number based on current state or specified number
func GetNumericBlockNumber(number BlockNumber, store latestHeaderGetter) (uint64, error) {
	switch number {
	case LatestBlockNumber, PendingBlockNumber:
		latest := store.Header()
		if latest == nil {
			return 0, ErrLatestNotFound
		}

		return latest.Number, nil

	case EarliestBlockNumber:
		return 0, nil

	default:
		if number < 0 {
			return 0, ErrNegativeBlockNumber
		}

		return uint64(number), nil
	}
}

type headerGetter interface {
	Header() *types.Header
	GetHeaderByNumber(uint64) (*types.Header, bool)
}

// GetBlockHeader returns a header using the provided number
func GetBlockHeader(number BlockNumber, store headerGetter) (*types.Header, error) {
	switch number {
	case PendingBlockNumber, LatestBlockNumber:
		return store.Header(), nil

	case EarliestBlockNumber:
		header, ok := store.GetHeaderByNumber(uint64(0))
		if !ok {
			return nil, ErrFailedFetchGenesis
		}

		return header, nil

	default:
		// Convert the block number from hex to uint64
		header, ok := store.GetHeaderByNumber(uint64(number))
		if !ok {
			return nil, fmt.Errorf("error fetching block number %d header", uint64(number))
		}

		return header, nil
	}
}

type txLookupAndBlockGetter interface {
	ReadTxLookup(types.Hash) (types.Hash, bool)
	GetBlockByHash(types.Hash, bool) (*types.Block, bool)
}

// GetTxAndBlockByTxHash returns the tx and the block including the tx by given tx hash
func GetTxAndBlockByTxHash(txHash types.Hash, store txLookupAndBlockGetter) (*types.Transaction, *types.Block) {
	blockHash, ok := store.ReadTxLookup(txHash)
	if !ok {
		return nil, nil
	}

	block, ok := store.GetBlockByHash(blockHash, true)
	if !ok {
		return nil, nil
	}

	if txn, _ := types.FindTxByHash(block.Transactions, txHash); txn != nil {
		return txn, block
	}

	return nil, nil
}

type blockGetter interface {
	Header() *types.Header
	GetHeaderByNumber(uint64) (*types.Header, bool)
	GetBlockByHash(types.Hash, bool) (*types.Block, bool)
}

func GetHeaderFromBlockNumberOrHash(bnh BlockNumberOrHash, store blockGetter) (*types.Header, error) {
	// The filter is empty, use the latest block by default
	if bnh.BlockNumber == nil && bnh.BlockHash == nil {
		bnh.BlockNumber, _ = createBlockNumberPointer(latest)
	}

	if bnh.BlockNumber != nil {
		// block number
		header, err := GetBlockHeader(*bnh.BlockNumber, store)
		if err != nil {
			return nil, fmt.Errorf("failed to get the header of block %d: %w", *bnh.BlockNumber, err)
		}

		return header, nil
	}

	// block hash
	block, ok := store.GetBlockByHash(*bnh.BlockHash, false)
	if !ok {
		return nil, fmt.Errorf("could not find block referenced by the hash %s", bnh.BlockHash.String())
	}

	return block.Header, nil
}

type nonceGetter interface {
	Header() *types.Header
	GetHeaderByNumber(uint64) (*types.Header, bool)
	GetNonce(types.Address) uint64
	GetAccount(root types.Hash, addr types.Address) (*Account, error)
}

func GetNextNonce(address types.Address, number BlockNumber, store nonceGetter) (uint64, error) {
	if number == PendingBlockNumber {
		// Grab the latest pending nonce from the TxPool
		// If the account is not initialized in the local TxPool,
		// return the latest nonce from the world state
		nonce := store.GetNonce(address)
		// 🔍 调试：记录获取的 nonce（Info 级别以便在 gas 估算时可见）
		// 注意：这里无法直接访问 logger，但可以通过 store 接口传递
		// 暂时不添加日志，因为 store 接口没有 logger
		return nonce, nil
	}

	header, err := GetBlockHeader(number, store)
	if err != nil {
		return 0, err
	}

	acc, err := store.GetAccount(header.StateRoot, address)

	//nolint:govet
	if errors.Is(err, ErrStateNotFound) {
		// If the account doesn't exist / isn't initialized,
		// return a nonce value of 0
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	return acc.Nonce, nil
}

func DecodeTxn(arg *txnArgs, blockNumber uint64, store nonceGetter, forceSetNonce bool) (*types.Transaction, error) {
	if arg == nil {
		return nil, errors.New("missing value for required argument 0")
	}
	// set default values
	if arg.From == nil {
		arg.From = &types.ZeroAddress
		arg.Nonce = argUintPtr(0)
	} else if arg.Nonce == nil || forceSetNonce {
		// get nonce from the pool
		// For gas estimation, use PendingBlockNumber to include pending transactions
		// This ensures we get the next available nonce that accounts for pending txs
		nonce, err := GetNextNonce(*arg.From, PendingBlockNumber, store)
		if err != nil {
			return nil, err
		}
		// 🔍 调试：记录获取的 nonce（Info 级别以便在 gas 估算时可见）
		// 注意：这里无法直接访问 logger，但可以通过 store 接口传递
		// 暂时不添加日志，因为 store 接口没有 logger
		arg.Nonce = argUintPtr(nonce)
	}

	if arg.Value == nil {
		arg.Value = argBytesPtr([]byte{})
	}

	if arg.GasPrice == nil {
		// Set a reasonable default gas price for gas estimation
		arg.GasPrice = argBytesPtr(big.NewInt(1000000000).Bytes()) // 1 Gwei
	}

	if arg.GasTipCap == nil {
		arg.GasTipCap = argBytesPtr([]byte{})
	}

	if arg.GasFeeCap == nil {
		arg.GasFeeCap = argBytesPtr([]byte{})
	}

	var input []byte
	if arg.Data != nil {
		input = *arg.Data
	} else if arg.Input != nil {
		input = *arg.Input
	}

	if arg.To == nil && input == nil {
		return nil, ErrNoDataInContractCreation
	}

	if input == nil {
		input = []byte{}
	}

	if arg.Gas == nil {
		// For gas estimation, use a reasonable default gas limit
		// This allows the transaction to be properly processed for estimation
		arg.Gas = argUintPtr(3000000) // 3M gas limit for contract deployment
	}

	txType := types.LegacyTx
	if arg.Type != nil {
		txType = types.TxType(*arg.Type)
	}

	txn := &types.Transaction{
		From:      *arg.From,
		Gas:       uint64(*arg.Gas),
		GasPrice:  new(big.Int).SetBytes(*arg.GasPrice),
		GasTipCap: new(big.Int).SetBytes(*arg.GasTipCap),
		GasFeeCap: new(big.Int).SetBytes(*arg.GasFeeCap),
		Value:     new(big.Int).SetBytes(*arg.Value),
		Input:     input,
		Nonce:     uint64(*arg.Nonce),
		Type:      txType,
	}

	if arg.To != nil {
		txn.To = arg.To
	}

	// For gas estimation, set default signature values to allow proper RLP marshaling
	// These are dummy values and not used for actual transaction validation
	txn.V = big.NewInt(0)
	txn.R = big.NewInt(0)
	txn.S = big.NewInt(0)

	// Set ChainID for all transaction types during gas estimation
	// This ensures proper RLP marshaling regardless of transaction type
	txn.ChainID = big.NewInt(20230825) // Use correct chain ID from genesis.json

	txn.ComputeHash(blockNumber)

	return txn, nil
}
