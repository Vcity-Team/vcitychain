package jsonrpc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/gasprice"
	"github.com/Vcity-Team/vcitychain/helper/common"
	"github.com/Vcity-Team/vcitychain/helper/progress"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
)

type ethTxPoolStore interface {
	// AddTx adds a new transaction to the tx pool
	AddTx(tx *types.Transaction) error

	// GetPendingTx gets the pending transaction from the transaction pool, if it's present
	GetPendingTx(txHash types.Hash) (*types.Transaction, bool)

	// GetNonce returns the next nonce for this address
	GetNonce(addr types.Address) uint64

	// GetBaseFee returns the current base fee of TxPool
	GetBaseFee() uint64
}

type Account struct {
	Balance *big.Int
	Nonce   uint64
}

type ethStateStore interface {
	GetAccount(root types.Hash, addr types.Address) (*Account, error)
	GetStorage(root types.Hash, addr types.Address, slot types.Hash) ([]byte, error)
	GetForksInTime(blockNumber uint64) chain.ForksInTime
	GetCode(root types.Hash, addr types.Address) ([]byte, error)
}

type ethBlockchainStore interface {
	// Header returns the current header of the chain (genesis if empty)
	Header() *types.Header

	// GetHeaderByNumber gets a header using the provided number
	GetHeaderByNumber(uint64) (*types.Header, bool)

	// GetBlockByHash gets a block using the provided hash
	GetBlockByHash(hash types.Hash, full bool) (*types.Block, bool)

	// GetBlockByNumber returns a block using the provided number
	GetBlockByNumber(num uint64, full bool) (*types.Block, bool)

	// ReadTxLookup returns a block hash in which a given txn was mined
	ReadTxLookup(txnHash types.Hash) (types.Hash, bool)

	// GetReceiptsByHash returns the receipts for a block hash
	GetReceiptsByHash(hash types.Hash) ([]*types.Receipt, error)

	// GetAvgGasPrice returns the average gas price
	GetAvgGasPrice() *big.Int

	// ApplyTxn applies a transaction object to the blockchain
	ApplyTxn(
		header *types.Header,
		txn *types.Transaction,
		override types.StateOverride,
		nonPayable bool,
	) (*runtime.ExecutionResult, error)

	// ApplyTxnWithSnapshot applies a transaction using a provided snapshot (for state isolation during gas estimation).
	// This matches go-ethereum's approach where each execution gets a fresh state copy via State.Copy().
	ApplyTxnWithSnapshot(
		snapshot state.Snapshot,
		header *types.Header,
		txn *types.Transaction,
		override types.StateOverride,
		nonPayable bool,
	) (*runtime.ExecutionResult, error)

	// GetSnapshotAt returns a snapshot at the given state root (for state isolation during gas estimation).
	GetSnapshotAt(stateRoot types.Hash) (state.Snapshot, error)

	// GetSyncProgression retrieves the current sync progression, if any
	GetSyncProgression() *progress.Progression
}

type ethFilter interface {
	// FilterExtra filters extra data from header extra that is not included in block hash
	FilterExtra(extra []byte) ([]byte, error)
}

// ethStore provides access to the methods needed by eth endpoint
type ethStore interface {
	ethTxPoolStore
	ethStateStore
	ethBlockchainStore
	ethFilter
	gasprice.GasStore
}

// Eth is the eth jsonrpc endpoint
type Eth struct {
	logger        hclog.Logger
	store         ethStore
	chainID       uint64
	filterManager *FilterManager
	priceLimit    uint64
}

var (
	ErrInsufficientFunds = errors.New("insufficient funds for execution")
)

// ChainId returns the chain id of the client
//
//nolint:stylecheck
func (e *Eth) ChainId() (interface{}, error) {
	return argUintPtr(e.chainID), nil
}

func (e *Eth) Syncing() (interface{}, error) {
	if syncProgression := e.store.GetSyncProgression(); syncProgression != nil {
		// Node is bulk syncing, return the status
		return progression{
			Type:          string(syncProgression.SyncType),
			StartingBlock: argUint64(syncProgression.StartingBlock),
			CurrentBlock:  argUint64(syncProgression.CurrentBlock),
			HighestBlock:  argUint64(syncProgression.HighestBlock),
		}, nil
	}

	// Node is not bulk syncing
	return false, nil
}

// GetBlockByNumber returns information about a block by block number
func (e *Eth) GetBlockByNumber(number BlockNumber, fullTx bool) (interface{}, error) {
	num, err := GetNumericBlockNumber(number, e.store)
	if err != nil {
		return nil, err
	}

	block, ok := e.store.GetBlockByNumber(num, true)
	if !ok {
		return nil, nil
	}

	if err := e.filterExtra(block); err != nil {
		return nil, err
	}

	return toBlock(block, fullTx), nil
}

// GetBlockByHash returns information about a block by hash
func (e *Eth) GetBlockByHash(hash types.Hash, fullTx bool) (interface{}, error) {
	block, ok := e.store.GetBlockByHash(hash, true)
	if !ok {
		return nil, nil
	}

	if err := e.filterExtra(block); err != nil {
		return nil, err
	}

	return toBlock(block, fullTx), nil
}

func (e *Eth) filterExtra(block *types.Block) error {
	// we need to copy it because the store returns header from storage directly
	// and not a copy, so changing it, actually changes it in storage as well
	headerCopy := block.Header.Copy()

	filteredExtra, err := e.store.FilterExtra(headerCopy.ExtraData)
	if err != nil {
		return err
	}

	headerCopy.ExtraData = filteredExtra
	// no need to recompute hash (filtered out data is not in the hash in the first place)
	block.Header = headerCopy

	return nil
}

func (e *Eth) GetBlockTransactionCountByNumber(number BlockNumber) (interface{}, error) {
	num, err := GetNumericBlockNumber(number, e.store)
	if err != nil {
		return nil, err
	}

	block, ok := e.store.GetBlockByNumber(num, true)

	if !ok {
		return nil, nil
	}

	return *common.EncodeUint64(uint64(len(block.Transactions))), nil
}

// BlockNumber returns current block number
func (e *Eth) BlockNumber() (interface{}, error) {
	h := e.store.Header()
	if h == nil {
		return nil, fmt.Errorf("header has a nil value")
	}

	return argUintPtr(h.Number), nil
}

// SendRawTransaction sends a raw transaction
func (e *Eth) SendRawTransaction(buf argBytes) (interface{}, error) {
	tx := &types.Transaction{}
	if err := tx.UnmarshalRLP(buf); err != nil {
		return nil, err
	}

	// tx hash will be calculated inside e.store.AddTx
	if err := e.store.AddTx(tx); err != nil {
		return nil, err
	}

	return tx.Hash.String(), nil
}

// SendTransaction rejects eth_sendTransaction json-rpc call as we don't support wallet management
func (e *Eth) SendTransaction(_ *txnArgs) (interface{}, error) {
	return nil, fmt.Errorf("request calls to eth_sendTransaction method are not supported," +
		" use eth_sendRawTransaction instead")
}

// GetTransactionByHash returns a transaction by its hash.
// If the transaction is still pending -> return the txn with some fields omitted
// If the transaction is sealed into a block -> return the whole txn with all fields
func (e *Eth) GetTransactionByHash(hash types.Hash) (interface{}, error) {
	// findSealedTx is a helper method for checking the world state
	// for the transaction with the provided hash
	findSealedTx := func() *transaction {
		// Check the chain state for the transaction
		blockHash, ok := e.store.ReadTxLookup(hash)
		if !ok {
			// Block not found in storage
			return nil
		}

		block, ok := e.store.GetBlockByHash(blockHash, true)
		if !ok {
			// Block receipts not found in storage
			return nil
		}

		// Find the transaction within the block
		if txn, idx := types.FindTxByHash(block.Transactions, hash); txn != nil {
			txn.GasPrice = txn.GetGasPrice(block.Header.BaseFee)

			return toTransaction(
				txn,
				argUintPtr(block.Number()),
				argHashPtr(block.Hash()),
				&idx,
			)
		}

		return nil
	}

	// findPendingTx is a helper method for checking the TxPool
	// for the pending transaction with the provided hash
	findPendingTx := func() *transaction {
		// Check the TxPool for the transaction if it's pending
		if pendingTx, pendingFound := e.store.GetPendingTx(hash); pendingFound {
			return toPendingTransaction(pendingTx)
		}

		// Transaction not found in the TxPool
		return nil
	}

	// 1. Check the chain state for the txn
	if resultTxn := findSealedTx(); resultTxn != nil {
		return resultTxn, nil
	}

	// 2. Check the TxPool for the txn
	if resultTxn := findPendingTx(); resultTxn != nil {
		return resultTxn, nil
	}

	// Transaction not found in state or TxPool
	return nil, nil
}

// GetTransactionReceipt returns a transaction receipt by his hash
func (e *Eth) GetTransactionReceipt(hash types.Hash) (interface{}, error) {
	blockHash, ok := e.store.ReadTxLookup(hash)
	if !ok {
		// txn not found
		return nil, nil
	}

	block, ok := e.store.GetBlockByHash(blockHash, true)
	if !ok {
		// block not found
		e.logger.Warn(
			fmt.Sprintf("Block with hash [%s] not found", blockHash.String()),
		)

		return nil, nil
	}

	receipts, err := e.store.GetReceiptsByHash(blockHash)
	if err != nil {
		// block receipts not found
		e.logger.Warn(
			fmt.Sprintf("Receipts for block with hash [%s] not found", blockHash.String()),
		)

		return nil, nil
	}

	if len(receipts) == 0 {
		// Receipts not written yet on the db
		e.logger.Warn(
			fmt.Sprintf("No receipts found for block with hash [%s]", blockHash.String()),
		)

		return nil, nil
	}
	// find the transaction in the body
	logIndex := 0
	txn, txIndex := types.FindTxByHash(block.Transactions, hash)

	if txIndex == -1 {
		// txn not found
		return nil, nil
	}

	for i := 0; i < txIndex; i++ {
		// accumulate receipt logs indexes from block transactions
		// that are before the desired transaction
		logIndex += len(receipts[i].Logs)
	}

	raw := receipts[txIndex]
	logs := toLogs(raw.Logs, uint64(logIndex), uint64(txIndex), block.Header, hash)

	return toReceipt(raw, txn, uint64(txIndex), block.Header, logs), nil
}

// GetStorageAt returns the contract storage at the index position
func (e *Eth) GetStorageAt(
	address types.Address,
	index types.Hash,
	filter BlockNumberOrHash,
) (interface{}, error) {
	header, err := GetHeaderFromBlockNumberOrHash(filter, e.store)
	if err != nil {
		return nil, err
	}

	// Get the storage for the passed in location
	result, err := e.store.GetStorage(header.StateRoot, address, index)
	if err != nil {
		if errors.Is(err, ErrStateNotFound) {
			return argBytesPtr(types.ZeroHash[:]), nil
		}

		return nil, err
	}

	return argBytesPtr(result), nil
}

// GasPrice exposes "getGasPrice"'s function logic to public RPC interface
func (e *Eth) GasPrice() (interface{}, error) {
	gasPrice, err := e.getGasPrice()
	if err != nil {
		return nil, err
	}

	return argUint64(gasPrice), nil
}

// getGasPrice returns the average gas price based on the last x blocks
// taking into consideration operator defined price limit
func (e *Eth) getGasPrice() (uint64, error) {
	// Return --price-limit flag defined value if it is greater than avgGasPrice/baseFee+priorityFee
	if e.store.GetForksInTime(e.store.Header().Number).London {
		priorityFee, err := e.store.MaxPriorityFeePerGas()
		if err != nil {
			return 0, err
		}

		return common.Max(e.priceLimit, priorityFee.Uint64()+e.store.GetBaseFee()), nil
	}

	// Fetch average gas price in uint64
	avgGasPrice := e.store.GetAvgGasPrice().Uint64()

	return common.Max(e.priceLimit, avgGasPrice), nil
}

// fillTransactionGasPrice fills transaction gas price if no provided
func (e *Eth) fillTransactionGasPrice(tx *types.Transaction) error {
	if tx.GetGasPrice(e.store.GetBaseFee()).BitLen() > 0 {
		return nil
	}

	estimatedGasPrice, err := e.getGasPrice()
	if err != nil {
		return err
	}

	if tx.Type == types.DynamicFeeTx {
		tx.GasFeeCap = new(big.Int).SetUint64(estimatedGasPrice)
	} else {
		tx.GasPrice = new(big.Int).SetUint64(estimatedGasPrice)
	}

	return nil
}

type overrideAccount struct {
	Nonce     *argUint64                 `json:"nonce"`
	Code      *argBytes                  `json:"code"`
	Balance   *argUint64                 `json:"balance"`
	State     *map[types.Hash]types.Hash `json:"state"`
	StateDiff *map[types.Hash]types.Hash `json:"stateDiff"`
}

func (o *overrideAccount) ToType() types.OverrideAccount {
	res := types.OverrideAccount{}

	if o.Nonce != nil {
		res.Nonce = (*uint64)(o.Nonce)
	}

	if o.Code != nil {
		res.Code = *o.Code
	}

	if o.Balance != nil {
		res.Balance = new(big.Int).SetUint64(*(*uint64)(o.Balance))
	}

	if o.State != nil {
		res.State = *o.State
	}

	if o.StateDiff != nil {
		res.StateDiff = *o.StateDiff
	}

	return res
}

// StateOverride is the collection of overridden accounts.
type stateOverride map[types.Address]overrideAccount

// Call executes a smart contract call using the transaction object data
func (e *Eth) Call(arg *txnArgs, filter BlockNumberOrHash, apiOverride *stateOverride) (interface{}, error) {
	header, err := GetHeaderFromBlockNumberOrHash(filter, e.store)
	if err != nil {
		return nil, err
	}

	transaction, err := DecodeTxn(arg, header.Number, e.store, true)
	if err != nil {
		return nil, err
	}

	// If the caller didn't supply the gas limit in the message, then we set it to maximum possible => block gas limit
	if transaction.Gas == 0 {
		transaction.Gas = header.GasLimit
	}

	// Force transaction gas price if empty
	if err = e.fillTransactionGasPrice(transaction); err != nil {
		return nil, err
	}

	var override types.StateOverride
	if apiOverride != nil {
		override = types.StateOverride{}
		for addr, o := range *apiOverride {
			override[addr] = o.ToType()
		}
	}

	// The return value of the execution is saved in the transition (returnValue field)
	result, err := e.store.ApplyTxn(header, transaction, override, true)
	if err != nil {
		return nil, err
	}

	// Check if an EVM revert happened
	if result.Reverted() {
		return []byte(hex.EncodeToString(result.ReturnValue)), constructErrorFromRevert(result)
	}

	if result.Failed() {
		return nil, fmt.Errorf("unable to execute call: %w", result.Err)
	}

	return argBytesPtr(result.ReturnValue), nil
}

// EstimateGas estimates the gas needed to execute a transaction
func (e *Eth) EstimateGas(arg *txnArgs, rawNum *BlockNumber) (interface{}, error) {
	number := LatestBlockNumber
	if rawNum != nil {
		number = *rawNum
	}

	// Fetch the requested header
	header, err := GetBlockHeader(number, e.store)
	if err != nil {
		return nil, err
	}

	// Use current block header (like go-ethereum)
	// go-ethereum's EstimateGas uses the current pending block header, not the next block
	// This matches go-ethereum's behavior exactly
	estimateHeader := header

	// Get fork config for current block to match go-ethereum
	forkConfig := e.store.GetForksInTime(header.Number)

	// ⭐ 关键：获取基础状态 snapshot（只获取一次，类似 go-ethereum 的 StateAndHeaderByNumberOrHash）
	// 状态隔离将在每次执行前通过 snapshot.Copy() 实现
	baseSnapshot, err := e.store.GetSnapshotAt(estimateHeader.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to get base snapshot for gas estimation: %w", err)
	}

	// 🔍 调试：检查状态快照中的 nonce（如果提供了 From 地址）
	if arg.From != nil {
		// 从状态快照中读取账户信息
		account, err := baseSnapshot.GetAccount(*arg.From)
		if err == nil && account != nil {
			e.logger.Info("🔍 [EstimateGas] 状态快照中的账户信息",
				"from", arg.From.String(),
				"accountNonce", account.Nonce,
				"accountBalance", account.Balance.String(),
				"stateRoot", estimateHeader.StateRoot.String(),
				"blockNumber", estimateHeader.Number,
				"note", "如果 accountNonce 不是最新的，说明状态快照不包含最新的交易结果")
		} else {
			e.logger.Info("🔍 [EstimateGas] 状态快照中账户不存在或读取失败",
				"from", arg.From.String(),
				"error", err,
				"stateRoot", estimateHeader.StateRoot.String(),
				"blockNumber", estimateHeader.Number)
		}
	}

	// Log header details for comparison (Info level so it's visible)
	e.logger.Info("🔍 [EstimateGas] using current block header (like go-ethereum)",
		"blockNumber", estimateHeader.Number,
		"stateRoot", estimateHeader.StateRoot.String(),
		"gasLimit", estimateHeader.GasLimit,
		"baseFee", estimateHeader.BaseFee,
		"timestamp", estimateHeader.Timestamp,
		"homestead", forkConfig.Homestead,
		"istanbul", forkConfig.Istanbul)

	// testTransaction should execute tx with nonce always set to the current expected nonce for the account
	transaction, err := DecodeTxn(arg, header.Number, e.store, true)
	if err != nil {
		return nil, err
	}

	// 🔍 调试：记录 gas 估算时获取的 nonce（Info 级别以便可见）
	if arg.From != nil {
		e.logger.Info("🔍 [EstimateGas] 获取的 nonce",
			"from", arg.From.String(),
			"txNonce", transaction.Nonce,
			"blockNumber", header.Number,
			"stateRoot", header.StateRoot.String())
	}

	// Log the transaction details for gas estimation (including dummy signature values)
	e.logger.Debug("gas estimation transaction created",
		"txType", transaction.Type,
		"chainID", transaction.ChainID,
		"v", transaction.V,
		"r", transaction.R,
		"s", transaction.S,
		"from", transaction.From,
		"to", transaction.To,
		"value", transaction.Value,
		"gas", transaction.Gas,
		"estimateBlockNumber", estimateHeader.Number)

	// Force transaction gas price if empty
	if err = e.fillTransactionGasPrice(transaction); err != nil {
		return nil, err
	}

	// Use the same binary search approach as go-ethereum
	// lo will be set after initial execution based on actual GasUsed (like go-ethereum)
	var (
		lo uint64 // Will be set after initial execution
		hi uint64
	)

	// If the gas limit was passed in, use it as a ceiling
	if transaction.Gas != 0 && transaction.Gas >= state.TxGas {
		hi = transaction.Gas
	} else {
		// If not, use the current block's gas limit (like go-ethereum)
		hi = estimateHeader.GasLimit
	}

	// Save the initial hi value as cap (like go-ethereum)
	cap := hi

	gasPriceInt := new(big.Int).Set(transaction.GasPrice)

	var availableBalance *big.Int

	// If the sender address is present, figure out how much available funds
	// are we working with
	if transaction.From != types.ZeroAddress {
		// Get the account balance
		// If the account is not initialized yet in state,
		// assume it's an empty account
		accountBalance := big.NewInt(0)
		acc, err := e.store.GetAccount(header.StateRoot, transaction.From)

		if err != nil && !errors.Is(err, ErrStateNotFound) {
			// An unrelated error occurred, return it
			return nil, err
		} else if err == nil {
			// No error when fetching the account,
			// read the balance from state
			accountBalance = acc.Balance
		}

		availableBalance = new(big.Int).Set(accountBalance)
	}

	// Recalculate the gas ceiling based on the available funds (if any)
	// and the passed in gas price (if present)
	if gasPriceInt.BitLen() != 0 && // Gas price has been set
		availableBalance != nil && // Available balance is found
		availableBalance.Cmp(big.NewInt(0)) > 0 { // Available balance > 0
		gasAllowance := new(big.Int).Div(availableBalance, gasPriceInt)

		// Check the gas allowance for this account, make sure hi is capped to it
		if gasAllowance.IsUint64() && hi > gasAllowance.Uint64() {
			e.logger.Debug(
				fmt.Sprintf(
					"Gas estimation hi capped by allowance [%d]",
					gasAllowance.Uint64(),
				),
			)

			hi = gasAllowance.Uint64()
		}
	}

	// If the transaction is a plain value transfer, short circuit estimation and
	// directly try 21000. Returning 21000 without any execution is dangerous as
	// some tx field combos might bump the price up even for plain transfers (e.g.
	// unused access list items). Ever so slightly wasteful, but safer overall.
	// This matches go-ethereum's gasestimator.Estimate implementation.
	if len(transaction.Input) == 0 {
		if transaction.To != nil {
			// Check if the target address has no code (i.e., it's not a contract)
			toCode, err := e.store.GetCode(header.StateRoot, *transaction.To)
			if err == nil && len(toCode) == 0 {
				// Try executing with TxGas (21000)
				// ⭐ 关键：每次执行前创建状态副本（类似 go-ethereum 的 dirtyState = opts.State.Copy()）
				dirtySnapshot := baseSnapshot.Copy()
				transaction.Gas = state.TxGas
				testResult, testErr := e.store.ApplyTxnWithSnapshot(dirtySnapshot, estimateHeader, transaction, nil, true)

				// Check if execution succeeded (matches go-ethereum: !failed && err == nil)
				if testErr == nil && testResult != nil && !testResult.Failed() {
					e.logger.Debug("🔍 [EstimateGas] plain value transfer detected, returning TxGas",
						"txGas", state.TxGas,
						"gasUsed", testResult.GasUsed,
						"to", transaction.To.String())
					return argUint64(state.TxGas), nil
				}
				// If execution failed, continue with normal binary search
				// Reset transaction gas for normal estimation
				transaction.Gas = 0
			}
		}
	}

	// Checks if executor level valid gas errors occurred
	isGasApplyError := func(err error) bool {
		if errors.Is(err, state.ErrNotEnoughIntrinsicGas) {
			return true
		}

		var expected *state.TransitionApplicationError
		if errors.As(err, &expected) {
			return errors.Is(expected.Err, state.ErrNotEnoughIntrinsicGas)
		}

		return false
	}

	// Checks if EVM level valid gas errors occurred
	isGasEVMError := func(err error) bool {
		return errors.Is(err, runtime.ErrOutOfGas) || errors.Is(err, runtime.ErrCodeStoreOutOfGas)
	}

	// Checks if the EVM reverted during execution
	isEVMRevertError := func(err error) bool {
		return errors.Is(err, runtime.ErrExecutionReverted)
	}

	// Run the transaction with the specified gas value.
	// Returns a status indicating if the transaction failed, return value (data), and the accompanying error
	testTransaction := func(gas uint64, shouldOmitErr bool) (bool, interface{}, error) {
		var data interface{}

		transaction.Gas = gas

		// Calculate intrinsic gas for logging (before ApplyTxn)
		testIntrinsicGas, _ := state.TransactionGasCost(transaction, forkConfig.Homestead, forkConfig.Istanbul)
		if gas == hi || gas == lo+1 { // Log only at key points to avoid spam
			e.logger.Info("🔍 [EstimateGas] testTransaction",
				"testGas", gas,
				"calculatedIntrinsicGas", testIntrinsicGas,
				"isContractCreation", transaction.IsContractCreation(),
				"inputSize", len(transaction.Input),
				"homestead", forkConfig.Homestead,
				"istanbul", forkConfig.Istanbul,
				"blockNumber", estimateHeader.Number)
		}

		// ⭐ 关键：每次执行前创建状态副本（类似 go-ethereum 的 dirtyState = opts.State.Copy()）
		// 这确保了每次执行都使用完全独立的状态，避免状态污染
		dirtySnapshot := baseSnapshot.Copy()
		result, applyErr := e.store.ApplyTxnWithSnapshot(dirtySnapshot, estimateHeader, transaction, nil, true)

		if result != nil {
			data = []byte(hex.EncodeToString(result.ReturnValue))
		}

		if applyErr != nil {
			// Check the application error.
			// Gas apply errors are valid, and should be ignored
			if isGasApplyError(applyErr) && shouldOmitErr {
				// Specifying the transaction failed, but not providing an error
				// is an indication that a valid error occurred due to low gas,
				// which will increase the lower bound for the search
				return true, data, nil
			}

			return true, data, applyErr
		}

		// Check if an out of gas error happened during EVM execution
		if result.Failed() {
			if isGasEVMError(result.Err) && shouldOmitErr {
				// Specifying the transaction failed, but not providing an error
				// is an indication that a valid error occurred due to low gas,
				// which will increase the lower bound for the search
				return true, data, nil
			}

			if isEVMRevertError(result.Err) {
				// The EVM reverted during execution, attempt to extract the
				// error message and return it
				return true, data, constructErrorFromRevert(result)
			}

			return true, data, result.Err
		}

		// Critical check: if GasUsed <= intrinsicGas, it means no contract code was executed
		// (only intrinsic gas was consumed). This should be considered a failure for gas estimation
		// because we need gas > intrinsicGas to actually execute contract code.
		// This matches go-ethereum's behavior: if gas = intrinsicGas, gasLeft = 0, and no code executes.
		if result.GasUsed <= testIntrinsicGas {
			// Gas used is only intrinsic gas, meaning no contract execution happened
			// This should be treated as a failure to ensure we return hi > intrinsicGas
			if shouldOmitErr {
				return true, data, nil
			}
			// During final verification, we need to ensure hi > intrinsicGas
			// If GasUsed = intrinsicGas, it means gasLeft = 0, so contract code didn't execute
			return true, data, fmt.Errorf("gas used (%d) is only intrinsic gas (%d), no contract code executed", result.GasUsed, testIntrinsicGas)
		}

		return false, nil, nil
	}

	// Initial unconstrained execution (like go-ethereum)
	// Execute the transaction with high gas limit to get actual gas usage
	// This helps set a better lower bound for binary search
	initialGas := hi
	transaction.Gas = initialGas
	// ⭐ 关键：每次执行前创建状态副本（类似 go-ethereum 的 dirtyState = opts.State.Copy()）
	dirtySnapshot := baseSnapshot.Copy()
	initialResult, initialErr := e.store.ApplyTxnWithSnapshot(dirtySnapshot, estimateHeader, transaction, nil, true)

	if initialErr != nil {
		// If initial execution fails with non-gas error, return it
		if !isGasApplyError(initialErr) && !isGasEVMError(initialErr) {
			return nil, initialErr
		}
		// If it's a gas error, set lo to intrinsicGas - 1 as minimum (like go-ethereum)
		// This ensures we start binary search from a reasonable lower bound
		intrinsicGas, _ := state.TransactionGasCost(transaction, forkConfig.Homestead, forkConfig.Istanbul)
		if intrinsicGas > 0 {
			lo = intrinsicGas - 1
		}
		e.logger.Debug("🔍 [EstimateGas] initial execution failed with gas error, setting lo to intrinsicGas-1",
			"intrinsicGas", intrinsicGas,
			"lo", lo)
	} else if initialResult != nil && !initialResult.Failed() {
		// Initial execution succeeded, use GasUsed - 1 as lower bound (like go-ethereum)
		// This optimizes the binary search by starting closer to the actual gas needed
		if initialResult.GasUsed > 0 {
			lo = initialResult.GasUsed - 1
			// Ensure lo is at least intrinsicGas - 1 (not TxGas - 1, as intrinsicGas may be higher for contract creation)
			intrinsicGas, _ := state.TransactionGasCost(transaction, forkConfig.Homestead, forkConfig.Istanbul)
			if intrinsicGas > 0 && lo < intrinsicGas-1 {
				lo = intrinsicGas - 1
			}
			e.logger.Debug("🔍 [EstimateGas] initial execution succeeded, using GasUsed-1 as lo",
				"initialGasUsed", initialResult.GasUsed,
				"newLo", lo,
				"intrinsicGas", intrinsicGas)

			// Optimistic gas limit check (like go-ethereum)
			// There's a fairly high chance for the transaction to execute successfully
			// with gasLimit set to the first execution's usedGas + gasRefund.
			// Explicitly check that gas amount and use as a limit for the binary search.
			// Note: We use GasUsed instead of MaxUsedGas (which we don't have)
			// CallStipend is 2300 in go-ethereum, but we'll use a simpler calculation
			// optimisticGasLimit := (initialResult.GasUsed + 2300) * 64 / 63
			// For simplicity, we'll use a conservative multiplier: GasUsed * 64 / 63
			optimisticGasLimit := initialResult.GasUsed * 64 / 63
			if optimisticGasLimit < hi {
				transaction.Gas = optimisticGasLimit
				// ⭐ 关键：每次执行前创建状态副本（类似 go-ethereum 的 dirtyState = opts.State.Copy()）
				dirtySnapshot := baseSnapshot.Copy()
				optimisticResult, optimisticErr := e.store.ApplyTxnWithSnapshot(dirtySnapshot, estimateHeader, transaction, nil, true)
				if optimisticErr == nil && optimisticResult != nil && !optimisticResult.Failed() {
					// Optimistic gas limit works, use it as hi
					hi = optimisticGasLimit
					e.logger.Debug("🔍 [EstimateGas] optimistic gas limit check succeeded",
						"optimisticGasLimit", optimisticGasLimit,
						"newHi", hi)
				} else {
					// Optimistic gas limit failed, use it as lo
					lo = optimisticGasLimit
					e.logger.Debug("🔍 [EstimateGas] optimistic gas limit check failed, using as lo",
						"optimisticGasLimit", optimisticGasLimit,
						"newLo", lo)
				}
			}
		}
	} else if initialResult != nil && isEVMRevertError(initialResult.Err) {
		// Transaction reverts even with high gas, return the revert error
		return nil, constructErrorFromRevert(initialResult)
	}

	// Ensure lo has a reasonable minimum value before binary search
	// If lo is still 0 (e.g., initial execution failed or GasUsed was 0), set it to intrinsicGas - 1
	if lo == 0 {
		intrinsicGas, _ := state.TransactionGasCost(transaction, forkConfig.Homestead, forkConfig.Istanbul)
		if intrinsicGas > 0 {
			lo = intrinsicGas - 1
		}
	}

	// Start the binary search for the lowest possible gas price
	for lo+1 < hi {
		// Calculate mid point (like go-ethereum: lo + (hi-lo)/2)
		mid := lo + (hi-lo)/2

		// Optimization: bias the search towards the low side (like go-ethereum)
		// Most txs don't need much higher gas limit than their gas used, and most txs don't
		// require near the full block limit of gas, so the selection of where to bisect the
		// range here is skewed to favor the low side.
		if mid > lo*2 {
			mid = lo * 2
		}

		failed, retVal, testErr := testTransaction(mid, true)
		if testErr != nil && !isEVMRevertError(testErr) {
			// Reverts are ignored in the binary search, but are checked later on
			// during the execution for the optimal gas limit found
			return retVal, testErr
		}

		if failed {
			// If the transaction failed => set lo to mid (like go-ethereum)
			lo = mid
		} else {
			// If the transaction didn't fail => make this ok value the high end
			hi = mid
		}
	}

	// Reject the transaction as invalid if it still fails at the highest allowance
	// This matches go-ethereum's behavior exactly
	if hi == cap {
		failed, retVal, err := testTransaction(hi, false)
		if failed {
			return retVal, fmt.Errorf(
				"gas required exceeds allowance (%d) or always failing transaction: %w",
				cap,
				err,
			)
		}
	} else {
		// Normal case: verify hi works
		failed, retVal, err := testTransaction(hi, false)
		if failed {
			return retVal, fmt.Errorf(
				"unable to apply transaction even for the highest gas limit %d: %w",
				hi,
				err,
			)
		}
	}

	// Calculate final intrinsic gas for logging
	finalIntrinsicGas, _ := state.TransactionGasCost(transaction, forkConfig.Homestead, forkConfig.Istanbul)

	// Log the final gas estimation result with detailed comparison info
	e.logger.Info("🔍 [EstimateGas] estimation completed",
		"txHash", transaction.Hash.String(),
		"estimatedGas", hi,
		"estimatedIntrinsicGas", finalIntrinsicGas,
		"blockNumber", estimateHeader.Number,
		"isContractCreation", transaction.IsContractCreation(),
		"inputSize", len(transaction.Input),
		"homestead", forkConfig.Homestead,
		"istanbul", forkConfig.Istanbul,
		"stateRoot", estimateHeader.StateRoot.String(),
		"gasLimit", estimateHeader.GasLimit,
		"baseFee", estimateHeader.BaseFee,
		"note", "Using current block header like go-ethereum")

	return argUint64(hi), nil
}

// GetFilterLogs returns an array of logs for the specified filter
func (e *Eth) GetFilterLogs(id string) (interface{}, error) {
	logFilter, err := e.filterManager.GetLogFilterFromID(id)
	if err != nil {
		return nil, err
	}

	return e.filterManager.GetLogsForQuery(logFilter.query)
}

// GetLogs returns an array of logs matching the filter options
func (e *Eth) GetLogs(query *LogQuery) (interface{}, error) {
	return e.filterManager.GetLogsForQuery(query)
}

// GetBalance returns the account's balance at the referenced block.
// 修改：扣除冻结金额，返回可用余额
func (e *Eth) GetBalance(address types.Address, filter BlockNumberOrHash) (interface{}, error) {
	header, err := GetHeaderFromBlockNumberOrHash(filter, e.store)
	if err != nil {
		return nil, err
	}

	// Extract the account balance
	acc, err := e.store.GetAccount(header.StateRoot, address)
	if errors.Is(err, ErrStateNotFound) {
		// Account not found, return an empty account
		return argUintPtr(0), nil
	} else if err != nil {
		return nil, err
	}

	// 查询冻结信息并扣除冻结金额
	availableBalance := new(big.Int).Set(acc.Balance)

	// 直接通过全局函数获取DPoS实例并查询冻结信息
	if dposInstance, exists := dpos.GetDPoSInstance("vcity_dpos"); exists && dposInstance != nil {
		freezeInfo, err := dposInstance.GetFreezeInfo(address)
		if err == nil && freezeInfo != nil && freezeInfo.FrozenAmount != nil {
			// 扣除冻结金额
			availableBalance.Sub(availableBalance, freezeInfo.FrozenAmount)
		}
	}

	return argBigPtr(availableBalance), nil
}

// GetTransactionCount returns account nonce
func (e *Eth) GetTransactionCount(address types.Address, filter BlockNumberOrHash) (interface{}, error) {
	var (
		blockNumber BlockNumber
		header      *types.Header
		err         error
	)

	// The filter is empty, use the latest block by default
	if filter.BlockNumber == nil && filter.BlockHash == nil {
		filter.BlockNumber, _ = createBlockNumberPointer(latest)
	}

	if filter.BlockNumber == nil {
		header, err = GetHeaderFromBlockNumberOrHash(filter, e.store)
		if err != nil {
			return nil, fmt.Errorf("failed to get header from block hash or block number: %w", err)
		}

		blockNumber = BlockNumber(header.Number)
	} else {
		blockNumber = *filter.BlockNumber
	}

	nonce, err := GetNextNonce(address, blockNumber, e.store)
	if err != nil {
		if errors.Is(err, ErrStateNotFound) {
			return argUintPtr(0), nil
		}

		return nil, err
	}

	return argUintPtr(nonce), nil
}

// GetCode returns account code at given block number
func (e *Eth) GetCode(address types.Address, filter BlockNumberOrHash) (interface{}, error) {
	header, err := GetHeaderFromBlockNumberOrHash(filter, e.store)
	if err != nil {
		return nil, err
	}

	emptySlice := []byte{}
	code, err := e.store.GetCode(header.StateRoot, address)

	if errors.Is(err, ErrStateNotFound) {
		// If the account doesn't exist / is not initialized yet,
		// return the default value
		return "0x", nil
	} else if err != nil {
		return argBytesPtr(emptySlice), err
	}

	return argBytesPtr(code), nil
}

// NewFilter creates a filter object, based on filter options, to notify when the state changes (logs).
func (e *Eth) NewFilter(filter *LogQuery) (interface{}, error) {
	return e.filterManager.NewLogFilter(filter, nil), nil
}

// NewBlockFilter creates a filter in the node, to notify when a new block arrives
func (e *Eth) NewBlockFilter() (interface{}, error) {
	return e.filterManager.NewBlockFilter(nil), nil
}

// GetFilterChanges is a polling method for a filter, which returns an array of logs which occurred since last poll.
func (e *Eth) GetFilterChanges(id string) (interface{}, error) {
	return e.filterManager.GetFilterChanges(id)
}

// UninstallFilter uninstalls a filter with given ID
func (e *Eth) UninstallFilter(id string) (bool, error) {
	return e.filterManager.Uninstall(id), nil
}

// Unsubscribe uninstalls a filter in a websocket
func (e *Eth) Unsubscribe(id string) (bool, error) {
	return e.filterManager.Uninstall(id), nil
}

// MaxPriorityFeePerGas calculates the priority fee needed for transaction to be included in a block
func (e *Eth) MaxPriorityFeePerGas() (interface{}, error) {
	priorityFee, err := e.store.MaxPriorityFeePerGas()
	if err != nil {
		return nil, err
	}

	return argBigPtr(priorityFee), nil
}

func (e *Eth) FeeHistory(blockCount argUint64, newestBlock BlockNumber,
	rewardPercentiles []float64) (interface{}, error) {
	block, err := GetNumericBlockNumber(newestBlock, e.store)
	if err != nil {
		return nil, fmt.Errorf("could not parse newest block argument. Error: %w", err)
	}

	// Retrieve oldestBlock, baseFeePerGas, gasUsedRatio, and reward synchronously
	history, err := e.store.FeeHistory(uint64(blockCount), block, rewardPercentiles)
	if err != nil {
		return nil, err
	}

	// Create channels to receive the processed slices asynchronously
	baseFeePerGasCh := make(chan []argUint64)
	gasUsedRatioCh := make(chan []float64)
	rewardCh := make(chan [][]argUint64)

	// Process baseFeePerGas asynchronously
	go func() {
		baseFeePerGasCh <- convertToArgUint64Slice(history.BaseFeePerGas)
	}()

	// Process gasUsedRatio asynchronously
	go func() {
		gasUsedRatioCh <- history.GasUsedRatio
	}()

	// Process reward asynchronously
	go func() {
		rewardCh <- convertToArgUint64SliceSlice(history.Reward)
	}()

	// Wait for the processed slices from goroutines
	baseFeePerGasResult := <-baseFeePerGasCh
	gasUsedRatioResult := <-gasUsedRatioCh
	rewardResult := <-rewardCh

	result := &feeHistoryResult{
		OldestBlock:   *argUintPtr(history.OldestBlock),
		BaseFeePerGas: baseFeePerGasResult,
		GasUsedRatio:  gasUsedRatioResult,
		Reward:        rewardResult,
	}

	return result, nil
}
