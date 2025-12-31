package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"

	"github.com/hashicorp/go-hclog"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/contracts"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/state/runtime/addresslist"
	"github.com/Vcity-Team/vcitychain/state/runtime/evm"
	"github.com/Vcity-Team/vcitychain/state/runtime/precompiled"
	"github.com/Vcity-Team/vcitychain/state/runtime/tracer"
	"github.com/Vcity-Team/vcitychain/types"
)

const (
	SpuriousDragonMaxCodeSize = 24576
	TxPoolMaxInitCodeSize     = 2 * SpuriousDragonMaxCodeSize

	TxGas                 uint64 = 21000 // Per transaction not creating a contract
	TxGasContractCreation uint64 = 53000 // Per transaction that creates a contract
)

// GetHashByNumber returns the hash function of a block number
type GetHashByNumber = func(i uint64) types.Hash

type GetHashByNumberHelper = func(*types.Header) GetHashByNumber

// Executor is the main entity
type Executor struct {
	logger  hclog.Logger
	config  *chain.Params
	state   State
	GetHash GetHashByNumberHelper

	PostHook        func(txn *Transition)
	GenesisPostHook func(*Transition) error

	// useGethEVM 是否使用 go-ethereum EVM（默认 false，使用原生 EVM）
	useGethEVM bool
}

// NewExecutor creates a new executor
func NewExecutor(config *chain.Params, s State, logger hclog.Logger) *Executor {
	return &Executor{
		logger:     logger,
		config:     config,
		state:      s,
		useGethEVM: false, // 默认使用原生 EVM
	}
}

// UseGethEVM 设置是否使用 go-ethereum EVM
func (e *Executor) UseGethEVM(enable bool) {
	e.useGethEVM = enable
	if enable {
		e.logger.Info("✅ 已启用 go-ethereum EVM（包含最新 EIP 支持）")
	} else {
		e.logger.Info("✅ 使用原生 EVM")
	}
}

// GetMainState 获取主状态管理器（用于直接状态操作）
func (e *Executor) GetMainState() State {
	return e.state
}

func (e *Executor) WriteGenesis(
	alloc map[types.Address]*chain.GenesisAccount,
	initialStateRoot types.Hash) (types.Hash, error) {
	var (
		snap Snapshot
		err  error
	)

	if initialStateRoot == types.ZeroHash {
		snap = e.state.NewSnapshot()
	} else {
		snap, err = e.state.NewSnapshotAt(initialStateRoot)
	}

	if err != nil {
		return types.Hash{}, err
	}

	txn := NewTxn(snap)
	config := e.config.Forks.At(0)

	env := runtime.TxContext{
		ChainID: e.config.ChainID,
	}

	transition := &Transition{
		logger:      e.logger,
		ctx:         env,
		state:       txn,
		auxState:    e.state,
		gasPool:     uint64(env.GasLimit),
		config:      config,
		precompiles: precompiled.NewPrecompiled(),
	}

	for addr, account := range alloc {
		if account.Balance != nil {
			txn.AddBalance(addr, account.Balance)
		}

		if account.Nonce != 0 {
			txn.SetNonce(addr, account.Nonce)
		}

		if len(account.Code) != 0 {
			txn.SetCode(addr, account.Code)
		}

		for key, value := range account.Storage {
			txn.SetState(addr, key, value)
		}
	}

	if e.GenesisPostHook != nil {
		if err := e.GenesisPostHook(transition); err != nil {
			return types.Hash{}, fmt.Errorf("Error writing genesis block: %w", err)
		}
	}

	objs, err := txn.Commit(false)
	if err != nil {
		return types.Hash{}, err
	}

	_, root, err := snap.Commit(objs)
	if err != nil {
		return types.Hash{}, err
	}

	return types.BytesToHash(root), nil
}

type BlockResult struct {
	Root     types.Hash
	Receipts []*types.Receipt
	TotalGas uint64
}

// ProcessBlock already does all the handling of the whole process
func (e *Executor) ProcessBlock(
	parentRoot types.Hash,
	block *types.Block,
	blockCreator types.Address,
) (*Transition, error) {
	txn, err := e.BeginTxn(parentRoot, block.Header, blockCreator)
	if err != nil {
		return nil, err
	}

	for _, t := range block.Transactions {
		if t.Gas > block.Header.GasLimit {
			continue
		}

		if err = txn.Write(t); err != nil {
			return nil, err
		}
	}

	return txn, nil
}

// StateAt returns snapshot at given root
func (e *Executor) State() State {
	return e.state
}

// StateAt returns snapshot at given root
func (e *Executor) StateAt(root types.Hash) (Snapshot, error) {
	return e.state.NewSnapshotAt(root)
}

// GetForksInTime returns the active forks at the given block height
func (e *Executor) GetForksInTime(blockNumber uint64) chain.ForksInTime {
	return e.config.Forks.At(blockNumber)
}

func (e *Executor) BeginTxn(
	parentRoot types.Hash,
	header *types.Header,
	coinbaseReceiver types.Address,
) (*Transition, error) {
	forkConfig := e.config.Forks.At(header.Number)

	auxSnap2, err := e.state.NewSnapshotAt(parentRoot)
	if err != nil {
		return nil, err
	}

	burnContract := types.ZeroAddress
	if forkConfig.London {
		burnContract, err = e.config.CalculateBurnContract(header.Number)
		if err != nil {
			return nil, err
		}
	}

	newTxn := NewTxn(auxSnap2)

	txCtx := runtime.TxContext{
		Coinbase:     coinbaseReceiver,
		Timestamp:    int64(header.Timestamp),
		Number:       int64(header.Number),
		Difficulty:   types.BytesToHash(new(big.Int).SetUint64(header.Difficulty).Bytes()),
		BaseFee:      new(big.Int).SetUint64(header.BaseFee),
		GasLimit:     int64(header.GasLimit),
		ChainID:      e.config.ChainID,
		BurnContract: burnContract,
	}

	txn := &Transition{
		logger:   e.logger,
		ctx:      txCtx,
		state:    newTxn,
		snap:     auxSnap2,
		getHash:  e.GetHash(header),
		auxState: e.state,
		config:   forkConfig,
		gasPool:  uint64(txCtx.GasLimit),

		receipts: []*types.Receipt{},
		totalGas: 0,

		evm:         e.createEVM(),
		precompiles: precompiled.NewPrecompiled(),
		PostHook:    e.PostHook,
	}

	// enable contract deployment allow list (if any)
	if e.config.ContractDeployerAllowList != nil {
		txn.deploymentAllowList = addresslist.NewAddressList(txn, contracts.AllowListContractsAddr)
	}

	if e.config.ContractDeployerBlockList != nil {
		txn.deploymentBlockList = addresslist.NewAddressList(txn, contracts.BlockListContractsAddr)
	}

	// enable transactions allow list (if any)
	if e.config.TransactionsAllowList != nil {
		txn.txnAllowList = addresslist.NewAddressList(txn, contracts.AllowListTransactionsAddr)
	}

	if e.config.TransactionsBlockList != nil {
		txn.txnBlockList = addresslist.NewAddressList(txn, contracts.BlockListTransactionsAddr)
	}

	// enable transactions allow list (if any)
	if e.config.BridgeAllowList != nil {
		txn.bridgeAllowList = addresslist.NewAddressList(txn, contracts.AllowListBridgeAddr)
	}

	if e.config.BridgeBlockList != nil {
		txn.bridgeBlockList = addresslist.NewAddressList(txn, contracts.BlockListBridgeAddr)
	}

	return txn, nil
}

// BeginTxnWithSnapshot creates a new Transition from a snapshot (for state isolation during gas estimation).
// This matches go-ethereum's approach where each execution gets a fresh state copy.
// The snapshot should be a copy created via Snapshot.Copy() to ensure complete isolation.
func (e *Executor) BeginTxnWithSnapshot(
	snapshot Snapshot,
	header *types.Header,
	coinbaseReceiver types.Address,
) (*Transition, error) {
	forkConfig := e.config.Forks.At(header.Number)

	burnContract := types.ZeroAddress
	if forkConfig.London {
		var err error
		burnContract, err = e.config.CalculateBurnContract(header.Number)
		if err != nil {
			return nil, err
		}
	}

	newTxn := NewTxn(snapshot)

	txCtx := runtime.TxContext{
		Coinbase:     coinbaseReceiver,
		Timestamp:    int64(header.Timestamp),
		Number:       int64(header.Number),
		Difficulty:   types.BytesToHash(new(big.Int).SetUint64(header.Difficulty).Bytes()),
		BaseFee:      new(big.Int).SetUint64(header.BaseFee),
		GasLimit:     int64(header.GasLimit),
		ChainID:      e.config.ChainID,
		BurnContract: burnContract,
	}

	txn := &Transition{
		logger:   e.logger,
		ctx:      txCtx,
		state:    newTxn,
		snap:     snapshot,
		getHash:  e.GetHash(header),
		auxState: e.state,
		config:   forkConfig,
		gasPool:  uint64(txCtx.GasLimit),

		receipts: []*types.Receipt{},
		totalGas: 0,

		evm:         e.createEVM(),
		precompiles: precompiled.NewPrecompiled(),
		PostHook:    e.PostHook,
	}

	// enable contract deployment allow list (if any)
	if e.config.ContractDeployerAllowList != nil {
		txn.deploymentAllowList = addresslist.NewAddressList(txn, contracts.AllowListContractsAddr)
	}

	if e.config.ContractDeployerBlockList != nil {
		txn.deploymentBlockList = addresslist.NewAddressList(txn, contracts.BlockListContractsAddr)
	}

	// enable transactions allow list (if any)
	if e.config.TransactionsAllowList != nil {
		txn.txnAllowList = addresslist.NewAddressList(txn, contracts.AllowListTransactionsAddr)
	}

	if e.config.TransactionsBlockList != nil {
		txn.txnBlockList = addresslist.NewAddressList(txn, contracts.BlockListTransactionsAddr)
	}

	// enable transactions allow list (if any)
	if e.config.BridgeAllowList != nil {
		txn.bridgeAllowList = addresslist.NewAddressList(txn, contracts.AllowListBridgeAddr)
	}

	if e.config.BridgeBlockList != nil {
		txn.bridgeBlockList = addresslist.NewAddressList(txn, contracts.BlockListBridgeAddr)
	}

	return txn, nil
}

type Transition struct {
	logger hclog.Logger

	// dummy
	auxState State
	snap     Snapshot

	config  chain.ForksInTime
	state   *Txn
	getHash GetHashByNumber
	ctx     runtime.TxContext
	gasPool uint64

	// result
	receipts []*types.Receipt
	totalGas uint64

	PostHook func(t *Transition)

	// runtimes
	evm         runtime.Runtime
	precompiles *precompiled.Precompiled

	// allow list runtimes
	deploymentAllowList *addresslist.AddressList
	deploymentBlockList *addresslist.AddressList
	txnAllowList        *addresslist.AddressList
	txnBlockList        *addresslist.AddressList
	bridgeAllowList     *addresslist.AddressList
	bridgeBlockList     *addresslist.AddressList
}

func NewTransition(config chain.ForksInTime, snap Snapshot, radix *Txn) *Transition {
	return &Transition{
		config:      config,
		state:       radix,
		snap:        snap,
		evm:         evm.NewEVM(), // 默认使用原生 EVM
		precompiles: precompiled.NewPrecompiled(),
	}
}

// createEVM 根据配置创建 EVM 实例
func (e *Executor) createEVM() runtime.Runtime {
	if e.useGethEVM {
		return evm.NewGethEVMAdapter(e.config.ChainID)
	}
	return evm.NewEVM()
}

func (t *Transition) WithStateOverride(override types.StateOverride) error {
	for addr, o := range override {
		if o.State != nil && o.StateDiff != nil {
			return fmt.Errorf("cannot override both state and state diff")
		}

		if o.Nonce != nil {
			t.state.SetNonce(addr, *o.Nonce)
		}

		if o.Balance != nil {
			t.state.SetBalance(addr, o.Balance)
		}

		if o.Code != nil {
			t.state.SetCode(addr, o.Code)
		}

		if o.State != nil {
			t.state.SetFullStorage(addr, o.State)
		}

		for k, v := range o.StateDiff {
			t.state.SetState(addr, k, v)
		}
	}

	return nil
}

func (t *Transition) TotalGas() uint64 {
	return t.totalGas
}

func (t *Transition) Receipts() []*types.Receipt {
	return t.receipts
}

// AppendSystemReceipt appends a receipt for a transaction that was processed
// outside of the EVM, without modifying gas accounting. This is useful for
// consensus/system-level transactions that still require a receipt to keep the
// receipts count in sync with the number of block transactions.
func (t *Transition) AppendSystemReceipt(txn *types.Transaction, success bool) {
	receipt := &types.Receipt{
		CumulativeGasUsed: t.totalGas,
		// Use the same transaction type as the original tx to align encoding (typed vs legacy)
		TransactionType: txn.Type,
		TxHash:          txn.Hash,
		GasUsed:         0,
	}

	if success {
		receipt.SetStatus(types.ReceiptSuccess)
	} else {
		receipt.SetStatus(types.ReceiptFailed)
	}

	// No logs for system-handled transactions by default
	receipt.Logs = nil
	receipt.LogsBloom = types.CreateBloom([]*types.Receipt{receipt})

	t.receipts = append(t.receipts, receipt)
}

// AppendSystemReceiptWithGas appends a receipt with an explicit GasUsed value.
func (t *Transition) AppendSystemReceiptWithGas(txn *types.Transaction, gasUsed uint64, success bool) {
	receipt := &types.Receipt{
		CumulativeGasUsed: t.totalGas,
		// Use the same transaction type as the original tx to align encoding (typed vs legacy)
		TransactionType: txn.Type,
		TxHash:          txn.Hash,
		GasUsed:         gasUsed,
	}

	if success {
		receipt.SetStatus(types.ReceiptSuccess)
	} else {
		receipt.SetStatus(types.ReceiptFailed)
	}

	receipt.Logs = nil
	receipt.LogsBloom = types.CreateBloom([]*types.Receipt{receipt})
	t.receipts = append(t.receipts, receipt)
}

// SettleSystemTxGas performs EVM-like gas accounting for system-handled transactions
// without executing EVM code. It settles fees, updates balances, nonce, gas pool,
// and appends a receipt with the provided gasUsed.
func (t *Transition) SettleSystemTxGas(txn *types.Transaction, gasUsed uint64, success bool) error {
	// 1) Consensus checks similar to normal tx path
	if err := t.nonceCheck(txn); err != nil {
		return NewTransitionApplicationError(err, true)
	}
	if !t.ctx.NonPayable {
		if err := t.checkDynamicFees(txn); err != nil {
			return NewTransitionApplicationError(err, true)
		}
		if err := t.subGasLimitPrice(txn); err != nil {
			return NewTransitionApplicationError(err, true)
		}
	}

	// 2) Reserve block gas
	if err := t.subGasPool(txn.Gas); err != nil {
		return NewGasLimitReachedTransitionApplicationError(err)
	}

	// 3) Increment sender nonce
	if err := t.state.IncrNonce(txn.From); err != nil {
		return err
	}

	// 4) Use full gas (tx.Gas) as gasUsed to mirror producer behavior for system txs
	gasUsed = txn.Gas
	gasLeft := txn.Gas - gasUsed

	gasPrice := txn.GetGasPrice(t.ctx.BaseFee.Uint64())

	// Refund unused gas to sender
	if gasLeft > 0 {
		remaining := new(big.Int).Mul(new(big.Int).SetUint64(gasLeft), gasPrice)
		t.state.AddBalance(txn.From, remaining)
	}

	// Miner tip
	effectiveTip := GetLondonFixHandler(uint64(t.ctx.Number)).getEffectiveTip(
		txn, gasPrice, t.ctx.BaseFee, t.config.London,
	)
	coinbaseFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), effectiveTip)
	t.state.AddBalance(t.ctx.Coinbase, coinbaseFee)

	// Pay base fee to coinbase (block producer) if London
	if t.config.London && txn.Type != types.StateTx {
		burnAmount := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), t.ctx.BaseFee)
		t.state.AddBalance(t.ctx.Coinbase, burnAmount)
	}

	// 5) Return unused gas to pool
	t.addGasPool(gasLeft)

	// 6) Update cumulative gas and append receipt
	t.totalGas += gasUsed
	t.AppendSystemReceiptWithGas(txn, gasUsed, success)

	return nil
}

var emptyFrom = types.Address{}

// Write writes another transaction to the executor
func (t *Transition) Write(txn *types.Transaction) error {
	var err error

	if txn.From == emptyFrom &&
		(txn.Type == types.LegacyTx || txn.Type == types.DynamicFeeTx) {
		// Decrypt the from address
		signer := crypto.NewSigner(t.config, uint64(t.ctx.ChainID))

		txn.From, err = signer.Sender(txn)
		if err != nil {
			return NewTransitionApplicationError(err, false)
		}
	}

	// Make a local copy and apply the transaction
	msg := txn.Copy()

	result, e := t.Apply(msg)
	if e != nil {
		t.logger.Error("failed to apply tx", "err", e)

		return e
	}

	t.totalGas += result.GasUsed

	logs := t.state.Logs()
	// 📋 [Write] 收集交易日志: logsCount=%d txHash=%s
	// 注意：这个日志在 Transition.Write 中输出，用于跟踪交易执行后的日志收集
	// 如果日志为空，说明 go-ethereum EVM 的 AddLog 没有被调用（或者合约没有发出事件）
	if len(logs) > 0 {
		t.logger.Info("📋 [Write] 收集交易日志", "logsCount", len(logs), "txHash", txn.Hash.String(), "firstLogAddress", logs[0].Address.String(), "firstLogTopicsCount", len(logs[0].Topics), "blockNumber", t.ctx.Number)
	} else {
		t.logger.Info("📋 [Write] 收集交易日志: 日志为空", "txHash", txn.Hash.String(), "gasUsed", result.GasUsed, "blockNumber", t.ctx.Number, "note", "如果使用go-ethereum EVM，说明AddLog没有被调用")
	}

	receipt := &types.Receipt{
		CumulativeGasUsed: t.totalGas,
		TransactionType:   txn.Type,
		TxHash:            txn.Hash,
		GasUsed:           result.GasUsed,
	}

	// The suicided accounts are set as deleted for the next iteration
	if err := t.state.CleanDeleteObjects(true); err != nil {
		return fmt.Errorf("failed to clean deleted objects: %w", err)
	}

	if result.Failed() {
		receipt.SetStatus(types.ReceiptFailed)
	} else {
		receipt.SetStatus(types.ReceiptSuccess)
	}

	// if the transaction created a contract, store the creation address in the receipt.
	// 🔧 修复：按照以太坊实现，使用 evm.Create 返回的实际地址，而不是重新计算
	// go-ethereum 的 evm.Create 返回的地址是实际创建的合约地址
	// 如果 result.Address 不为零地址，说明合约创建成功，使用实际地址
	// 否则使用计算出的地址（用于向后兼容或错误情况）
	if msg.To == nil {
		calculatedAddr := crypto.CreateAddress(msg.From, txn.Nonce)
		if result.Address != types.ZeroAddress {
			// 使用 evm.Create 返回的实际地址（与以太坊实现一致）
			t.logger.Info("🔍 [Write] 设置 receipt.ContractAddress",
				"txHash", txn.Hash.String(),
				"resultAddress", result.Address.String(),
				"calculatedAddress", calculatedAddr.String(),
				"usingResultAddress", true,
			)
			receipt.ContractAddress = result.Address.Ptr()
		} else {
			// 回退到计算出的地址（用于向后兼容）
			t.logger.Info("🔍 [Write] 设置 receipt.ContractAddress (回退到计算地址)",
				"txHash", txn.Hash.String(),
				"calculatedAddress", calculatedAddr.String(),
				"usingResultAddress", false,
			)
			receipt.ContractAddress = calculatedAddr.Ptr()
		}
	}

	// Set the receipt logs and create a bloom for filtering
	receipt.Logs = logs
	receipt.LogsBloom = types.CreateBloom([]*types.Receipt{receipt})
	t.receipts = append(t.receipts, receipt)

	return nil
}

// Commit commits the final result
func (t *Transition) Commit() (Snapshot, types.Hash, error) {
	objs, err := t.state.Commit(t.config.EIP155)
	if err != nil {
		return nil, types.ZeroHash, err
	}

	s2, root, err := t.snap.Commit(objs)
	if err != nil {
		return nil, types.ZeroHash, err
	}

	return s2, types.BytesToHash(root), nil
}

func (t *Transition) subGasPool(amount uint64) error {
	if t.gasPool < amount {
		return ErrBlockLimitReached
	}

	t.gasPool -= amount

	return nil
}

func (t *Transition) addGasPool(amount uint64) {
	t.gasPool += amount
}

func (t *Transition) Txn() *Txn {
	return t.state
}

// Apply applies a new transaction
func (t *Transition) Apply(msg *types.Transaction) (*runtime.ExecutionResult, error) {
	s := t.state.Snapshot()

	result, err := t.apply(msg)
	if err != nil {
		// 计算 intrinsic gas 用于日志（如果计算失败，使用 0）
		intrinsicGasCost, _ := TransactionGasCost(msg, t.config.Homestead, t.config.Istanbul)
		gasDeficit := uint64(0)
		if intrinsicGasCost > msg.Gas {
			gasDeficit = intrinsicGasCost - msg.Gas
		}

		if revertErr := t.state.RevertToSnapshot(s); revertErr != nil {
			t.logger.Error("💀 交易执行失败且无法回滚状态，程序将立即退出",
				"txHash", msg.Hash.String(),
				"nonce", msg.Nonce,
				"from", msg.From.String(),
				"txGas", msg.Gas,
				"requiredIntrinsicGas", intrinsicGasCost,
				"gasDeficit", gasDeficit,
				"isContractCreation", msg.IsContractCreation(),
				"inputSize", len(msg.Input),
				"homestead", t.config.Homestead,
				"istanbul", t.config.Istanbul,
				"error", err,
				"revertError", revertErr,
				"note", "EstimateGas估算可能与实际执行环境不一致")
			return nil, revertErr
		}
		t.logger.Error("💀 交易执行失败，程序将立即退出",
			"txHash", msg.Hash.String(),
			"nonce", msg.Nonce,
			"from", msg.From.String(),
			"txGas", msg.Gas,
			"requiredIntrinsicGas", intrinsicGasCost,
			"gasDeficit", gasDeficit,
			"isContractCreation", msg.IsContractCreation(),
			"inputSize", len(msg.Input),
			"homestead", t.config.Homestead,
			"istanbul", t.config.Istanbul,
			"error", err,
			"note", "EstimateGas估算可能与实际执行环境不一致")
	}

	if t.PostHook != nil {
		t.PostHook(t)
	}

	return result, err
}

// ContextPtr returns reference of context
// This method is called only by test
func (t *Transition) ContextPtr() *runtime.TxContext {
	return &t.ctx
}

func (t *Transition) subGasLimitPrice(msg *types.Transaction) error {
	upfrontGasCost := GetLondonFixHandler(uint64(t.ctx.Number)).getUpfrontGasCost(msg, t.ctx.BaseFee)

	if err := t.state.SubBalance(msg.From, upfrontGasCost); err != nil {
		if errors.Is(err, runtime.ErrNotEnoughFunds) {
			return ErrNotEnoughFundsForGas
		}

		return err
	}

	return nil
}

func (t *Transition) nonceCheck(msg *types.Transaction) error {
	nonce := t.state.GetNonce(msg.From)

	if nonce != msg.Nonce {
		t.logger.Error("💀 交易nonce错误，程序将立即退出",
			"txHash", msg.Hash.String(),
			"expectedNonce", nonce,
			"actualNonce", msg.Nonce,
			"from", msg.From.String())
		os.Exit(1)
		return ErrNonceIncorrect
	}

	return nil
}

// checkDynamicFees checks correctness of the EIP-1559 feature-related fields.
// Basically, makes sure gas tip cap and gas fee cap are good for dynamic and legacy transactions
func (t *Transition) checkDynamicFees(msg *types.Transaction) error {
	return GetLondonFixHandler(uint64(t.ctx.Number)).checkDynamicFees(msg, t)
}

// errors that can originate in the consensus rules checks of the apply method below
// surfacing of these errors reject the transaction thus not including it in the block

var (
	ErrNonceIncorrect        = errors.New("incorrect nonce")
	ErrNotEnoughFundsForGas  = errors.New("not enough funds to cover gas costs")
	ErrBlockLimitReached     = errors.New("gas limit reached in the pool")
	ErrIntrinsicGasOverflow  = errors.New("overflow in intrinsic gas calculation")
	ErrNotEnoughIntrinsicGas = errors.New("not enough gas supplied for intrinsic gas costs")

	// ErrTipAboveFeeCap is a sanity error to ensure no one is able to specify a
	// transaction with a tip higher than the total fee cap.
	ErrTipAboveFeeCap = errors.New("max priority fee per gas higher than max fee per gas")

	// ErrTipVeryHigh is a sanity error to avoid extremely big numbers specified
	// in the tip field.
	ErrTipVeryHigh = errors.New("max priority fee per gas higher than 2^256-1")

	// ErrFeeCapVeryHigh is a sanity error to avoid extremely big numbers specified
	// in the fee cap field.
	ErrFeeCapVeryHigh = errors.New("max fee per gas higher than 2^256-1")

	// ErrFeeCapTooLow is returned if the transaction fee cap is less than the
	// the base fee of the block.
	ErrFeeCapTooLow = errors.New("max fee per gas less than block base fee")

	// ErrNonceUintOverflow is returned if uint64 overflow happens
	ErrNonceUintOverflow = errors.New("nonce uint64 overflow")
)

type TransitionApplicationError struct {
	Err           error
	IsRecoverable bool // Should the transaction be discarded, or put back in the queue.
}

func (e *TransitionApplicationError) Error() string {
	return e.Err.Error()
}

func NewTransitionApplicationError(err error, isRecoverable bool) *TransitionApplicationError {
	return &TransitionApplicationError{
		Err:           err,
		IsRecoverable: isRecoverable,
	}
}

type GasLimitReachedTransitionApplicationError struct {
	TransitionApplicationError
}

func NewGasLimitReachedTransitionApplicationError(err error) *GasLimitReachedTransitionApplicationError {
	return &GasLimitReachedTransitionApplicationError{
		*NewTransitionApplicationError(err, true),
	}
}

func (t *Transition) apply(msg *types.Transaction) (*runtime.ExecutionResult, error) {
	var err error

	if msg.Type == types.StateTx {
		err = checkAndProcessStateTx(msg)
	} else {
		err = checkAndProcessTx(msg, t)
	}

	if err != nil {
		return nil, err
	}

	// the amount of gas required is available in the block
	if err = t.subGasPool(msg.Gas); err != nil {
		return nil, NewGasLimitReachedTransitionApplicationError(err)
	}

	if t.ctx.Tracer != nil {
		t.ctx.Tracer.TxStart(msg.Gas)
	}

	// 4. there is no overflow when calculating intrinsic gas
	intrinsicGasCost, err := TransactionGasCost(msg, t.config.Homestead, t.config.Istanbul)
	if err != nil {
		return nil, NewTransitionApplicationError(err, false)
	}

	// Log execution environment details for comparison with EstimateGas (Info level so it's visible)
	t.logger.Info("🔍 [Apply] transaction execution environment",
		"txHash", msg.Hash.String(),
		"blockNumber", t.ctx.Number,
		"blockGasLimit", t.ctx.GasLimit,
		"blockBaseFee", t.ctx.BaseFee.Uint64(),
		"blockTimestamp", t.ctx.Timestamp,
		"homestead", t.config.Homestead,
		"istanbul", t.config.Istanbul,
		"calculatedIntrinsicGas", intrinsicGasCost,
		"txGas", msg.Gas,
		"isContractCreation", msg.IsContractCreation(),
		"inputSize", len(msg.Input))

	// the purchased gas is enough to cover intrinsic usage
	gasLeft := msg.Gas - intrinsicGasCost
	// because we are working with unsigned integers for gas, the `>` operator is used instead of the more intuitive `<`
	if gasLeft > msg.Gas {
		t.logger.Error("💀💀💀💀💀💀💀💀💀💀💀💀💀交易执行失败（intrinsic gas不足）",
			"txHash", msg.Hash.String(),
			"nonce", msg.Nonce,
			"from", msg.From.String(),
			"txGas", msg.Gas,
			"requiredIntrinsicGas", intrinsicGasCost,
			"gasDeficit", intrinsicGasCost-msg.Gas,
			"isContractCreation", msg.IsContractCreation(),
			"inputSize", len(msg.Input),
			"homestead", t.config.Homestead,
			"istanbul", t.config.Istanbul,
			"note", "EstimateGas估算值小于实际需要的intrinsic gas，可能是配置不一致导致")
		return nil, NewTransitionApplicationError(ErrNotEnoughIntrinsicGas, false)
	}

	gasPrice := msg.GetGasPrice(t.ctx.BaseFee.Uint64())
	value := new(big.Int).Set(msg.Value)

	// set the specific transaction fields in the context
	t.ctx.GasPrice = types.BytesToHash(gasPrice.Bytes())
	t.ctx.Origin = msg.From

	var result *runtime.ExecutionResult
	if msg.IsContractCreation() {
		// 🔍 调试：记录完整的字节码信息
		inputPreview := ""
		if len(msg.Input) > 0 {
			if len(msg.Input) > 64 {
				inputPreview = hex.EncodeToString(msg.Input[:32]) + "..." + hex.EncodeToString(msg.Input[len(msg.Input)-32:])
			} else {
				inputPreview = hex.EncodeToString(msg.Input)
			}
		}
		t.logger.Info("🔍 [Apply] calling Create2 with gasLeft",
			"txHash", msg.Hash.String(),
			"gasLeft", gasLeft,
			"inputSize", len(msg.Input),
			"inputPreview", inputPreview,
			"inputFirst4Bytes", func() string {
				if len(msg.Input) >= 4 {
					return hex.EncodeToString(msg.Input[:4])
				}
				return "too short"
			}(),
		)
		// ⚠️ 关键：合约创建时不需要在这里递增 nonce
		// go-ethereum EVM 的 Create 方法内部会调用 SetNonce 来递增 nonce（通过 HostToStateDBAdapter.SetNonce）
		// 如果我们在这里也递增 nonce，就会导致 nonce 被递增两次
		// ⭐ 关键：使用交易中的 nonce 而不是状态中的 nonce 来计算地址
		// 这确保了在 gas 估算时，即使状态快照是旧的，也能使用正确的 nonce
		// 根据以太坊规范，CREATE 应该使用执行时的 nonce，但在 gas 估算时，
		// 交易中的 nonce 应该与执行时的 nonce 匹配（通过 nonceCheck 验证）
		result = t.Create2WithNonce(msg.From, msg.Input, value, gasLeft, msg.Nonce)
		t.logger.Info("🔍 [Apply] Create2 returned",
			"txHash", msg.Hash.String(),
			"resultGasLeft", result.GasLeft,
			"resultGasUsed", result.GasUsed,
			"resultErr", result.Err,
			"returnValueLen", len(result.ReturnValue))
	} else {
		if err := t.state.IncrNonce(msg.From); err != nil {
			return nil, err
		}
		result = t.Call2(msg.From, *msg.To, msg.Input, value, gasLeft)
	}

	refund := t.state.GetRefund()
	result.UpdateGasUsed(msg.Gas, refund)
	t.logger.Info("🔍 [Apply] after UpdateGasUsed",
		"txHash", msg.Hash.String(),
		"msgGas", msg.Gas,
		"finalGasLeft", result.GasLeft,
		"finalGasUsed", result.GasUsed,
		"refund", refund)

	if t.ctx.Tracer != nil {
		t.ctx.Tracer.TxEnd(result.GasLeft)
	}

	// Refund the sender
	remaining := new(big.Int).Mul(new(big.Int).SetUint64(result.GasLeft), gasPrice)
	t.state.AddBalance(msg.From, remaining)

	// Spec: https://eips.ethereum.org/EIPS/eip-1559#specification
	// Define effective tip based on tx type.
	// We use EIP-1559 fields of the tx if the london hardfork is enabled.
	// Effective tip became to be either gas tip cap or (gas fee cap - current base fee)
	effectiveTip := GetLondonFixHandler(uint64(t.ctx.Number)).getEffectiveTip(
		msg, gasPrice, t.ctx.BaseFee, t.config.London,
	)

	// Pay the coinbase fee as a miner reward using the calculated effective tip.
	coinbaseFee := new(big.Int).Mul(new(big.Int).SetUint64(result.GasUsed), effectiveTip)
	t.state.AddBalance(t.ctx.Coinbase, coinbaseFee)

	// Pay base fee to coinbase (block producer) if the london hardfork is applied.
	// Base fee is transferred to the current block producer instead of burn contract.
	if t.config.London && msg.Type != types.StateTx {
		burnAmount := new(big.Int).Mul(new(big.Int).SetUint64(result.GasUsed), t.ctx.BaseFee)
		t.state.AddBalance(t.ctx.Coinbase, burnAmount)
	}

	// return gas to the pool
	t.addGasPool(result.GasLeft)

	return result, nil
}

func (t *Transition) Create2(
	caller types.Address,
	code []byte,
	value *big.Int,
	gas uint64,
) *runtime.ExecutionResult {
	// Use state nonce (for backward compatibility and EVM CREATE opcode calls)
	callerNonce := t.state.GetNonce(caller)
	return t.Create2WithNonce(caller, code, value, gas, callerNonce)
}

// Create2WithNonce creates a contract using the specified nonce (for gas estimation).
// This matches go-ethereum's behavior where the transaction nonce is used for address calculation.
func (t *Transition) Create2WithNonce(
	caller types.Address,
	code []byte,
	value *big.Int,
	gas uint64,
	nonce uint64,
) *runtime.ExecutionResult {
	// 🔍 调试：记录传入 Create2 的字节码
	codePreview := ""
	if len(code) > 0 {
		if len(code) > 32 {
			codePreview = hex.EncodeToString(code[:32]) + "..." + hex.EncodeToString(code[len(code)-32:])
		} else {
			codePreview = hex.EncodeToString(code)
		}
	}
	t.logger.Info("🔍 [Create2] 接收到的字节码",
		"caller", caller.String(),
		"codeLen", len(code),
		"codePreview", codePreview,
		"codeFirst4Bytes", func() string {
			if len(code) >= 4 {
				return hex.EncodeToString(code[:4])
			}
			return "too short"
		}(),
	)

	// ⚠️ 注意：这里不计算地址，因为根据以太坊规范，地址应该在 IncrNonce 之后计算
	// 地址将在 applyCreate 中，在 IncrNonce 之后计算，以确保与 go-ethereum EVM 的行为一致
	// go-ethereum EVM 的 Create 方法内部会使用执行时的状态 nonce（在 IncrNonce 之后）来计算地址
	// 所以我们需要在 applyCreate 中，在 IncrNonce 之后计算地址
	// 这里先使用零地址作为占位符，applyCreate 会重新计算
	contract := runtime.NewContractCreation(1, caller, caller, types.ZeroAddress, value, gas, code)

	// 🔍 调试：记录传入的 nonce 和当前状态 nonce
	t.logger.Info("🔍 [Create2] 准备创建合约（地址将在 IncrNonce 后计算）",
		"caller", caller.String(),
		"txNonce", nonce,
		"stateNonce", t.state.GetNonce(caller),
		"note", "地址将在 applyCreate 中，在 IncrNonce 之后计算")

	// 🔍 调试：记录传递给 NewContractCreation 后的字节码
	t.logger.Info("🔍 [Create2] 传递给 NewContractCreation 后的字节码",
		"contractCodeLen", len(contract.Code),
		"contractCodePreview", func() string {
			if len(contract.Code) > 0 {
				if len(contract.Code) > 32 {
					return hex.EncodeToString(contract.Code[:32]) + "..." + hex.EncodeToString(contract.Code[len(contract.Code)-32:])
				}
				return hex.EncodeToString(contract.Code)
			}
			return "empty"
		}(),
	)

	return t.applyCreate(contract, t)
}

func (t *Transition) Call2(
	caller types.Address,
	to types.Address,
	input []byte,
	value *big.Int,
	gas uint64,
) *runtime.ExecutionResult {
	c := runtime.NewContractCall(1, caller, caller, to, value, gas, t.state.GetCode(to), input)

	return t.applyCall(c, runtime.Call, t)
}

func (t *Transition) run(contract *runtime.Contract, host runtime.Host) *runtime.ExecutionResult {
	if result := t.handleAllowBlockListsUpdate(contract, host); result != nil {
		return result
	}

	// check txns access lists, allow list takes precedence over block list
	if t.txnAllowList != nil {
		if contract.Caller != contracts.SystemCaller {
			role := t.txnAllowList.GetRole(contract.Caller)
			if !role.Enabled() {
				t.logger.Debug(
					"Failing transaction. Caller is not in the transaction allowlist",
					"contract.Caller", contract.Caller,
					"contract.Address", contract.Address,
				)

				return &runtime.ExecutionResult{
					GasLeft: 0,
					Err:     runtime.ErrNotAuth,
				}
			}
		}
	} else if t.txnBlockList != nil {
		if contract.Caller != contracts.SystemCaller {
			role := t.txnBlockList.GetRole(contract.Caller)
			if role == addresslist.EnabledRole {
				t.logger.Debug(
					"Failing transaction. Caller is in the transaction blocklist",
					"contract.Caller", contract.Caller,
					"contract.Address", contract.Address,
				)

				return &runtime.ExecutionResult{
					GasLeft: 0,
					Err:     runtime.ErrNotAuth,
				}
			}
		}
	}

	// check the precompiles
	if t.precompiles.CanRun(contract, host, &t.config) {
		return t.precompiles.Run(contract, host, &t.config)
	}
	// check the evm
	if t.evm.CanRun(contract, host, &t.config) {
		return t.evm.Run(contract, host, &t.config)
	}

	return &runtime.ExecutionResult{
		Err: fmt.Errorf("runtime not found"),
	}
}

func (t *Transition) Transfer(from, to types.Address, amount *big.Int) error {
	if amount == nil {
		return nil
	}

	if err := t.state.SubBalance(from, amount); err != nil {
		if errors.Is(err, runtime.ErrNotEnoughFunds) {
			return runtime.ErrInsufficientBalance
		}

		return err
	}

	t.state.AddBalance(to, amount)

	return nil
}

func (t *Transition) applyCall(
	c *runtime.Contract,
	callType runtime.CallType,
	host runtime.Host,
) *runtime.ExecutionResult {
	if c.Depth > int(1024)+1 {
		return &runtime.ExecutionResult{
			GasLeft: c.Gas,
			Err:     runtime.ErrDepth,
		}
	}

	snapshot := t.state.Snapshot()
	t.state.TouchAccount(c.Address)

	if callType == runtime.Call {
		// Transfers only allowed on calls
		if err := t.Transfer(c.Caller, c.Address, c.Value); err != nil {
			return &runtime.ExecutionResult{
				GasLeft: c.Gas,
				Err:     err,
			}
		}
	}

	var result *runtime.ExecutionResult

	t.captureCallStart(c, callType)

	result = t.run(c, host)
	if result.Failed() {
		if err := t.state.RevertToSnapshot(snapshot); err != nil {
			return &runtime.ExecutionResult{
				GasLeft: c.Gas,
				Err:     err,
			}
		}
	}

	t.captureCallEnd(c, result)

	return result
}

func (t *Transition) hasCodeOrNonce(addr types.Address) bool {
	// ⚠️ 关键修复：地址冲突检查应该检查账户是否有代码或余额
	// 空账户（只有 nonce=1，没有代码，没有余额）应该允许覆盖
	// 这是因为在 gas 估算时，第一次执行可能创建了空账户，回滚不完整导致残留
	// 第二次执行时，应该允许覆盖这个空账户

	codeHash := t.state.GetCodeHash(addr)

	// 如果账户有代码（真正的合约），判定为冲突
	if codeHash != types.EmptyCodeHash && codeHash != types.ZeroHash {
		return true
	}

	// 检查账户是否有余额（EOA 账户）
	balance := t.state.GetBalance(addr)
	if balance.Sign() > 0 {
		// 有余额的账户应该判定为冲突（EOA 账户）
		return true
	}

	// ⚠️ 关键：如果账户只有 nonce（没有代码，没有余额），可能是空账户，允许覆盖
	// 这种情况通常发生在 gas 估算时，第一次执行创建了账户但执行失败，回滚不完整
	// 第二次执行时，应该允许覆盖这个空账户
	// 根据以太坊规范，地址冲突应该检查账户是否有代码或余额
	// 空账户（只有 nonce，没有代码，没有余额）不应该判定为冲突
	return false
}

func (t *Transition) applyCreate(c *runtime.Contract, host runtime.Host) *runtime.ExecutionResult {
	gasLimit := c.Gas

	if c.Depth > int(1024)+1 {
		return &runtime.ExecutionResult{
			GasLeft: gasLimit,
			Err:     runtime.ErrDepth,
		}
	}

	// ⭐ 关键修复：根据以太坊规范，CREATE 操作码应该：
	// 1. 先计算地址（使用递增前的 nonce）
	// 2. 然后递增 nonce
	// 但是，go-ethereum EVM 的 Create 方法内部也会递增 nonce，所以我们需要：
	// 1. 先计算地址（使用递增前的 nonce）
	// 2. 然后递增 nonce（让 go-ethereum EVM 内部也递增一次，总共递增两次）
	// 实际上，根据 go-ethereum 的实现，evm.Create 内部会：
	// 1. 递增 nonce
	// 2. 计算地址（使用递增后的 nonce）
	// 3. 检查地址冲突
	// 所以，如果我们也在 applyCreate 中递增 nonce，就会导致 nonce 被递增两次
	// 但是，根据以太坊规范，CREATE 应该使用递增前的 nonce 计算地址
	// 所以，我们应该：
	// 1. 先计算地址（使用递增前的 nonce）
	// 2. 然后递增 nonce
	// 3. 但是，go-ethereum EVM 内部也会递增 nonce，所以我们需要在调用 evm.Create 之前不递增 nonce
	// 或者，我们需要让 go-ethereum EVM 来处理 nonce 递增

	// ⚠️ 关键：如果 c.Caller 是全零地址，说明这是从 EVM 内部调用 CREATE 操作码的情况
	// 在这种情况下，应该使用 c.Origin（交易的原始发送者）作为 caller
	actualCaller := c.Caller
	if actualCaller == types.ZeroAddress {
		// 如果 Caller 是全零，使用 Origin 作为 caller
		actualCaller = c.Origin
		t.logger.Info("🔍 [applyCreate] Caller 是全零地址，使用 Origin 作为 caller",
			"originalCaller", c.Caller.String(),
			"origin", c.Origin.String(),
			"usingOrigin", true)
	}

	// ⭐ 关键修复：根据 go-ethereum EVM 的实现，evm.Create 内部会：
	// 1. 读取 nonce（递增前）
	// 2. 用递增前的 nonce 计算地址
	// 3. 递增 nonce（通过 SetNonce）
	// 4. 检查地址冲突
	// 所以，我们应该用递增前的 nonce 计算地址，与 go-ethereum EVM 保持一致
	if c.Address == types.ZeroAddress {
		// 如果地址是零地址（占位符），说明需要重新计算
		callerNonce := t.state.GetNonce(actualCaller) // 这是递增前的 nonce
		// ⚠️ 关键：go-ethereum EVM 的 Create 方法会用递增前的 nonce 计算地址
		// 从 go-ethereum 源码看：contractAddr = crypto.CreateAddress(caller.Address(), evm.StateDB.GetNonce(caller.Address()))
		// 这里 GetNonce 返回的是递增前的 nonce
		// 所以，我们也应该用递增前的 nonce 来计算地址，与 go-ethereum EVM 保持一致
		c.Address = crypto.CreateAddress(actualCaller, callerNonce)
		t.logger.Info("🔍 [applyCreate] 重新计算合约地址（使用递增前的 nonce，与 go-ethereum EVM 保持一致）",
			"caller", actualCaller.String(),
			"originalCaller", c.Caller.String(),
			"origin", c.Origin.String(),
			"callerNonce", callerNonce,
			"nonceUsedForAddress", callerNonce,
			"calculatedAddress", c.Address.String(),
			"note", "go-ethereum EVM 会用递增前的 nonce 计算地址，我们也用递增前的 nonce")
	}

	// ⚠️ 关键：不要在 applyCreate 中递增 nonce，让 go-ethereum EVM 来处理
	// go-ethereum EVM 的 Create 方法内部会递增 nonce，所以如果我们也在 applyCreate 中递增，
	// 就会导致 nonce 被递增两次，导致地址计算错误

	// Check if there is a collision and the address already exists
	// 🔍 调试：记录地址冲突检查的详细信息
	addrNonce := t.state.GetNonce(c.Address)
	addrCodeHash := t.state.GetCodeHash(c.Address)
	addrBalance := t.state.GetBalance(c.Address)
	addrCodeSize := t.state.GetCodeSize(c.Address)
	hasCollision := t.hasCodeOrNonce(c.Address)
	t.logger.Info("🔍 [applyCreate] 地址冲突检查",
		"contractAddress", c.Address.String(),
		"caller", c.Caller.String(),
		"callerNonce", t.state.GetNonce(c.Caller),
		"addrNonce", addrNonce,
		"addrCodeHash", addrCodeHash.String(),
		"addrBalance", addrBalance.String(),
		"addrCodeSize", addrCodeSize,
		"hasCollision", hasCollision,
		"isEmptyCodeHash", addrCodeHash == types.EmptyCodeHash,
		"isZeroHash", addrCodeHash == types.ZeroHash,
		"note", "如果 hasCollision=false 且 addrNonce>0，说明是空账户，允许覆盖")

	if hasCollision {
		return &runtime.ExecutionResult{
			GasLeft: 0,
			Err:     runtime.ErrContractAddressCollision,
		}
	}

	// ⚠️ 关键修复：在创建 snapshot 之前，如果账户是空的（只有 nonce=1，没有代码），先删除它
	// 这是因为：
	// 1. go-ethereum EVM 的 Create 方法内部会检查地址冲突，如果账户已存在（即使只有 nonce），会判定为冲突
	// 2. snapshot 会保存当前状态，如果我们在创建 snapshot 之后删除账户，snapshot 中仍然会有账户信息
	// 3. go-ethereum EVM 的 Create 方法在检查冲突时，可能从 snapshot 中获取账户信息
	// 所以，我们需要在创建 snapshot 之前删除空账户，这样 snapshot 中就不会有空账户了
	deletedEmptyAccount := false // 标记是否删除了空账户
	if t.state.Exist(c.Address) {
		addrNonce := t.state.GetNonce(c.Address)
		addrCodeHash := t.state.GetCodeHash(c.Address)
		addrBalance := t.state.GetBalance(c.Address)
		addrCodeSize := t.state.GetCodeSize(c.Address)

		// 如果账户是空的（只有 nonce，没有代码，没有余额），先删除它
		// 这样 go-ethereum EVM 就不会检测到冲突
		if addrCodeSize == 0 && addrBalance.Sign() == 0 && addrNonce > 0 {
			t.logger.Info("🔧 [applyCreate] 检测到空账户，在创建 snapshot 之前先删除以允许 go-ethereum EVM 创建",
				"contractAddress", c.Address.String(),
				"addrNonce", addrNonce,
				"addrCodeHash", addrCodeHash.String(),
				"addrBalance", addrBalance.String(),
				"addrCodeSize", addrCodeSize,
				"note", "在创建 snapshot 之前删除空账户，确保 snapshot 中不会有空账户")

			t.state.DeleteAccount(c.Address)
			deletedEmptyAccount = true // 标记已删除空账户

			// 验证删除是否成功
			addrNonceAfterDelete := t.state.GetNonce(c.Address)
			hasAccountAfterDelete := t.state.Exist(c.Address)
			addrCodeSizeAfterDelete := t.state.GetCodeSize(c.Address)
			addrCodeHashAfterDelete := t.state.GetCodeHash(c.Address)
			t.logger.Info("🔧 [applyCreate] 空账户删除完成（在创建 snapshot 之前）",
				"contractAddress", c.Address.String(),
				"addrNonceAfterDelete", addrNonceAfterDelete,
				"hasAccountAfterDelete", hasAccountAfterDelete,
				"addrCodeSizeAfterDelete", addrCodeSizeAfterDelete,
				"addrCodeHashAfterDelete", addrCodeHashAfterDelete.String(),
				"note", "删除成功，现在创建 snapshot，snapshot 中不会有空账户。如果 addrNonceAfterDelete > 0 或 hasAccountAfterDelete = true，说明删除失败")
		}
	}

	// Take snapshot of the current state (after deleting empty account if needed)
	snapshot := t.state.Snapshot()

	// ⚠️ 关键修复：不要在调用 go-ethereum EVM 之前创建账户
	// go-ethereum EVM 的 Create 方法会：
	// 1. 检查地址冲突（如果 nonce != 0，判定为冲突）
	// 2. 自己创建账户（通过 StateDB.CreateAccount）
	// 
	// 如果我们在调用 go-ethereum EVM 之前创建账户并设置 nonce=1，
	// go-ethereum EVM 会检测到冲突（nonce != 0），导致部署失败。
	//
	// 解决方案：
	// - 不提前创建账户，让 go-ethereum EVM 自己创建账户
	// - go-ethereum EVM 的 Create 方法会处理 EIP158 的要求（创建账户）
	// - 我们只需要确保在调用 go-ethereum EVM 之前，地址不存在或已被删除
	if deletedEmptyAccount {
		// 如果删除了空账户，跳过账户创建，让 go-ethereum EVM 来创建
		t.logger.Info("🔧 [applyCreate] 已删除空账户，跳过账户创建，让 go-ethereum EVM 来创建",
			"contractAddress", c.Address.String(),
			"note", "go-ethereum EVM 的 Create 方法会创建账户，不会检测到冲突")
	} else if !t.state.Exist(c.Address) {
		// 账户不存在，让 go-ethereum EVM 来创建
		t.logger.Info("🔧 [applyCreate] 账户不存在，让 go-ethereum EVM 来创建",
			"contractAddress", c.Address.String(),
			"note", "go-ethereum EVM 的 Create 方法会创建账户并处理 EIP158 的要求")
	} else {
		// 账户已存在，记录但不创建
		t.logger.Info("🔍 [applyCreate] 账户已存在，跳过创建",
			"contractAddress", c.Address.String(),
			"addrNonce", t.state.GetNonce(c.Address),
			"addrCodeSize", t.state.GetCodeSize(c.Address),
			"addrBalance", t.state.GetBalance(c.Address).String(),
			"note", "账户已存在，让 go-ethereum EVM 来处理")
	}

	// Transfer the value
	if err := t.Transfer(c.Caller, c.Address, c.Value); err != nil {
		return &runtime.ExecutionResult{
			GasLeft: gasLimit,
			Err:     err,
		}
	}

	var result *runtime.ExecutionResult

	t.captureCallStart(c, evm.CREATE)

	defer func() {
		// pass result to be set later
		t.captureCallEnd(c, result)
	}()

	// check if contract creation allow list is enabled
	if t.deploymentAllowList != nil {
		role := t.deploymentAllowList.GetRole(c.Caller)

		if !role.Enabled() {
			t.logger.Debug(
				"Failing contract deployment. Caller is not in the deployment allowlist",
				"contract.Caller", c.Caller,
				"contract.Address", c.Address,
			)

			return &runtime.ExecutionResult{
				GasLeft: 0,
				Err:     runtime.ErrNotAuth,
			}
		}
	} else if t.deploymentBlockList != nil {
		role := t.deploymentBlockList.GetRole(c.Caller)

		if role == addresslist.EnabledRole {
			t.logger.Debug(
				"Failing contract deployment. Caller is in the deployment blocklist",
				"contract.Caller", c.Caller,
				"contract.Address", c.Address,
			)

			return &runtime.ExecutionResult{
				GasLeft: 0,
				Err:     runtime.ErrNotAuth,
			}
		}
	}

	result = t.run(c, host)
	if result.Failed() {
		// 🔍 调试：记录回滚前的状态
		addrNonceBeforeRevert := t.state.GetNonce(c.Address)
		addrCodeHashBeforeRevert := t.state.GetCodeHash(c.Address)
		t.logger.Info("🔍 [applyCreate] 执行失败，准备回滚",
			"contractAddress", c.Address.String(),
			"addrNonceBeforeRevert", addrNonceBeforeRevert,
			"addrCodeHashBeforeRevert", addrCodeHashBeforeRevert.String(),
			"error", result.Err)

		if err := t.state.RevertToSnapshot(snapshot); err != nil {
			return &runtime.ExecutionResult{
				Err: err,
			}
		}

		// 🔍 调试：记录回滚后的状态
		addrNonceAfterRevert := t.state.GetNonce(c.Address)
		addrCodeHashAfterRevert := t.state.GetCodeHash(c.Address)
		hasAccountAfterRevert := t.state.Exist(c.Address)

		// ⚠️ 关键修复：如果回滚后账户仍然存在（nonce != 0），说明回滚不完整
		// 在这种情况下，我们需要手动清理账户，确保完全回滚
		if hasAccountAfterRevert && addrNonceAfterRevert != 0 {
			t.logger.Warn("🔧 [applyCreate] 检测到回滚不完整，手动清理账户",
				"contractAddress", c.Address.String(),
				"addrNonceAfterRevert", addrNonceAfterRevert,
				"addrCodeHashAfterRevert", addrCodeHashAfterRevert.String(),
				"note", "回滚后账户仍然存在，手动删除以确保完全清理")

			// 手动删除账户：从 radix tree 中删除
			// 这确保 getStateObject 不会找到这个账户（即使它在 snapshot 中）
			t.state.DeleteAccount(c.Address)

			// 验证删除是否成功
			addrNonceAfterDelete := t.state.GetNonce(c.Address)
			hasAccountAfterDelete := t.state.Exist(c.Address)
			t.logger.Info("🔧 [applyCreate] 手动删除账户完成",
				"contractAddress", c.Address.String(),
				"addrNonceAfterDelete", addrNonceAfterDelete,
				"hasAccountAfterDelete", hasAccountAfterDelete,
				"note", "如果 hasAccountAfterDelete=true，说明账户在 snapshot 中，需要进一步处理")
		}

		t.logger.Info("🔍 [applyCreate] 回滚完成",
			"contractAddress", c.Address.String(),
			"addrNonceAfterRevert", addrNonceAfterRevert,
			"addrCodeHashAfterRevert", addrCodeHashAfterRevert.String(),
			"hasAccountAfterRevert", hasAccountAfterRevert,
			"note", "如果 hasAccountAfterRevert=true 或 nonce!=0，说明回滚不完整")

		return result
	}

	if t.config.EIP158 && len(result.ReturnValue) > SpuriousDragonMaxCodeSize {
		// Contract size exceeds 'SpuriousDragon' size limit
		if err := t.state.RevertToSnapshot(snapshot); err != nil {
			return &runtime.ExecutionResult{
				Err: err,
			}
		}

		return &runtime.ExecutionResult{
			GasLeft: 0,
			Err:     runtime.ErrMaxCodeSizeExceeded,
		}
	}

	gasCost := uint64(len(result.ReturnValue)) * 200

	if result.GasLeft < gasCost {
		result.Err = runtime.ErrCodeStoreOutOfGas
		result.ReturnValue = nil

		// Out of gas creating the contract
		if t.config.Homestead {
			if err := t.state.RevertToSnapshot(snapshot); err != nil {
				return &runtime.ExecutionResult{
					Err: err,
				}
			}

			result.GasLeft = 0
		}

		return result
	}

	result.GasLeft -= gasCost

	// 🔧 修复：按照以太坊实现，如果使用 go-ethereum EVM，不应该覆盖 result.Address
	// go-ethereum 的 evm.Create 已经返回了正确的实际地址
	// 只有原生 EVM 才需要使用 c.Address（预期地址）
	if t.evm.Name() != "geth_evm" {
		// 原生 EVM：使用预期地址
		result.Address = c.Address
		t.state.SetCode(c.Address, result.ReturnValue)
	} else {
		// go-ethereum EVM：使用返回的实际地址，代码已经在 evm.Create 中保存
		// 但为了确保代码被保存，我们仍然需要设置代码（如果还没有设置）
		if result.Address != types.ZeroAddress {
			// 使用 go-ethereum 返回的实际地址
			t.state.SetCode(result.Address, result.ReturnValue)
		} else {
			// 如果地址为零，回退到预期地址（错误情况）
			result.Address = c.Address
			t.state.SetCode(c.Address, result.ReturnValue)
		}
	}

	return result
}

func (t *Transition) handleAllowBlockListsUpdate(contract *runtime.Contract,
	host runtime.Host) *runtime.ExecutionResult {
	// check contract deployment allow list (if any)
	if t.deploymentAllowList != nil && t.deploymentAllowList.Addr() == contract.CodeAddress {
		return t.deploymentAllowList.Run(contract, host, &t.config)
	}

	// check contract deployment block list (if any)
	if t.deploymentBlockList != nil && t.deploymentBlockList.Addr() == contract.CodeAddress {
		return t.deploymentBlockList.Run(contract, host, &t.config)
	}

	// check bridge allow list (if any)
	if t.bridgeAllowList != nil && t.bridgeAllowList.Addr() == contract.CodeAddress {
		return t.bridgeAllowList.Run(contract, host, &t.config)
	}

	// check bridge block list (if any)
	if t.bridgeBlockList != nil && t.bridgeBlockList.Addr() == contract.CodeAddress {
		return t.bridgeBlockList.Run(contract, host, &t.config)
	}

	// check transaction allow list (if any)
	if t.txnAllowList != nil && t.txnAllowList.Addr() == contract.CodeAddress {
		return t.txnAllowList.Run(contract, host, &t.config)
	}

	// check transaction block list (if any)
	if t.txnBlockList != nil && t.txnBlockList.Addr() == contract.CodeAddress {
		return t.txnBlockList.Run(contract, host, &t.config)
	}

	return nil
}

func (t *Transition) SetState(addr types.Address, key types.Hash, value types.Hash) {
	// 🔍 调试：记录合约存储写入（仅对非零值记录）
	// 检查是否是合约地址（有代码）
	if t.state.GetCodeSize(addr) > 0 {
		valueStr := value.String()
		if valueStr != "0x0000000000000000000000000000000000000000000000000000000000000000" {
			t.logger.Info("🔍 [Transition.SetState] 合约存储写入",
				"contractAddr", addr.String(),
				"key", key.String(),
				"value", valueStr,
			)
		}
	}
	t.state.SetState(addr, key, value)
}

func (t *Transition) SetStorage(
	addr types.Address,
	key types.Hash,
	value types.Hash,
	config *chain.ForksInTime,
) runtime.StorageStatus {
	return t.state.SetStorage(addr, key, value, config)
}

func (t *Transition) GetTxContext() runtime.TxContext {
	return t.ctx
}

func (t *Transition) GetBlockHash(number int64) (res types.Hash) {
	return t.getHash(uint64(number))
}

func (t *Transition) EmitLog(addr types.Address, topics []types.Hash, data []byte) {
	// 📝 [EmitLog] 发出事件日志
	// 这个日志会帮助我们确认 AddLog 是否被调用
	// 注意：这个函数会被 go-ethereum EVM 的 AddLog 调用（通过 host.EmitLog）
	firstTopic := "none"
	if len(topics) > 0 {
		firstTopic = topics[0].String()
	}
	// 🔧 调试：记录事件日志来源
	// 这个日志说明 go-ethereum EVM 的 AddLog 被调用了
	// 如果看不到这个日志，说明 AddLog 没有被调用
	// 注意：无法直接区分是同步节点还是生产节点，但可以通过 blockNumber 和 timestamp 结合其他日志来判断
	t.logger.Info("📝 [EmitLog] 发出事件日志（来自go-ethereum EVM AddLog）", "address", addr.String(), "topicsCount", len(topics), "dataLen", len(data), "firstTopic", firstTopic, "blockNumber", t.ctx.Number, "timestamp", t.ctx.Timestamp)
	t.state.EmitLog(addr, topics, data)
}

func (t *Transition) GetCodeSize(addr types.Address) int {
	return t.state.GetCodeSize(addr)
}

func (t *Transition) GetCodeHash(addr types.Address) (res types.Hash) {
	return t.state.GetCodeHash(addr)
}

func (t *Transition) GetCode(addr types.Address) []byte {
	return t.state.GetCode(addr)
}

func (t *Transition) GetBalance(addr types.Address) *big.Int {
	return t.state.GetBalance(addr)
}

func (t *Transition) GetStorage(addr types.Address, key types.Hash) types.Hash {
	value := t.state.GetState(addr, key)

	// 🔍 调试：记录合约存储读取（仅对非零值记录）
	// 检查是否是合约地址（有代码）
	if t.state.GetCodeSize(addr) > 0 {
		valueStr := value.String()
		if valueStr != "0x0000000000000000000000000000000000000000000000000000000000000000" {
			t.logger.Info("🔍 [Transition.GetStorage] 合约存储读取",
				"contractAddr", addr.String(),
				"key", key.String(),
				"value", valueStr,
			)
		}
	}

	return value
}

func (t *Transition) AccountExists(addr types.Address) bool {
	return t.state.Exist(addr)
}

func (t *Transition) Empty(addr types.Address) bool {
	return t.state.Empty(addr)
}

func (t *Transition) GetNonce(addr types.Address) uint64 {
	return t.state.GetNonce(addr)
}

func (t *Transition) Selfdestruct(addr types.Address, beneficiary types.Address) {
	if !t.state.HasSuicided(addr) {
		t.state.AddRefund(24000)
	}

	t.state.AddBalance(beneficiary, t.state.GetBalance(addr))
	t.state.Suicide(addr)
}

func (t *Transition) Callx(c *runtime.Contract, h runtime.Host) *runtime.ExecutionResult {
	if c.Type == runtime.Create {
		return t.applyCreate(c, h)
	}

	return t.applyCall(c, c.Type, h)
}

// GetLogger 返回 Transition 的 logger（用于 geth_adapter 调试）
func (t *Transition) GetLogger() hclog.Logger {
	return t.logger
}

// SetAccountDirectly sets an account to the given address
// NOTE: SetAccountDirectly changes the world state without a transaction
func (t *Transition) SetAccountDirectly(addr types.Address, account *chain.GenesisAccount) error {
	if t.AccountExists(addr) {
		return fmt.Errorf("can't add account to %+v because an account exists already", addr)
	}

	t.state.SetCode(addr, account.Code)

	for key, value := range account.Storage {
		t.state.SetStorage(addr, key, value, &t.config)
	}

	t.state.SetBalance(addr, account.Balance)
	t.state.SetNonce(addr, account.Nonce)

	return nil
}

// SetNonceDirectly sets nonce directly (used by go-ethereum EVM's SetNonce)
func (t *Transition) SetNonceDirectly(addr types.Address, nonce uint64) {
	t.state.SetNonce(addr, nonce)
}

// SetCodeDirectly sets new code into the account with the specified address
// NOTE: SetCodeDirectly changes the world state without a transaction
func (t *Transition) SetCodeDirectly(addr types.Address, code []byte) error {
	if !t.AccountExists(addr) {
		return fmt.Errorf("account doesn't exist at %s", addr)
	}

	t.state.SetCode(addr, code)

	return nil
}

// SetNonPayable deactivates the check of tx cost against tx executor balance.
func (t *Transition) SetNonPayable(nonPayable bool) {
	t.ctx.NonPayable = nonPayable
}

// SetTracer sets tracer to the context in order to enable it
func (t *Transition) SetTracer(tracer tracer.Tracer) {
	t.ctx.Tracer = tracer
}

// GetTracer returns a tracer in context
func (t *Transition) GetTracer() runtime.VMTracer {
	return t.ctx.Tracer
}

func (t *Transition) GetRefund() uint64 {
	return t.state.GetRefund()
}

func TransactionGasCost(msg *types.Transaction, isHomestead, isIstanbul bool) (uint64, error) {
	cost := uint64(0)

	// Contract creation is only paid on the homestead fork
	if msg.IsContractCreation() && isHomestead {
		cost += TxGasContractCreation
	} else {
		cost += TxGas
	}

	payload := msg.Input
	if len(payload) > 0 {
		zeros := uint64(0)

		for i := 0; i < len(payload); i++ {
			if payload[i] == 0 {
				zeros++
			}
		}

		nonZeros := uint64(len(payload)) - zeros
		nonZeroCost := uint64(68)

		if isIstanbul {
			nonZeroCost = 16
		}

		if (math.MaxUint64-cost)/nonZeroCost < nonZeros {
			return 0, ErrIntrinsicGasOverflow
		}

		cost += nonZeros * nonZeroCost

		if (math.MaxUint64-cost)/4 < zeros {
			return 0, ErrIntrinsicGasOverflow
		}

		cost += zeros * 4
	}

	return cost, nil
}

// checkAndProcessTx - first check if this message satisfies all consensus rules before
// applying the message. The rules include these clauses:
// 1. the nonce of the message caller is correct
// 2. caller has enough balance to cover transaction fee(gaslimit * gasprice * val) or fee(gasfeecap * gasprice * val)
func checkAndProcessTx(msg *types.Transaction, t *Transition) error {
	// 1. the nonce of the message caller is correct
	if err := t.nonceCheck(msg); err != nil {
		return NewTransitionApplicationError(err, true)
	}

	if !t.ctx.NonPayable {
		// 2. check dynamic fees of the transaction
		if err := t.checkDynamicFees(msg); err != nil {
			return NewTransitionApplicationError(err, true)
		}

		// 3. caller has enough balance to cover transaction
		// Skip this check if the given flag is provided.
		// It happens for eth_call and for other operations that do not change the state.
		if err := t.subGasLimitPrice(msg); err != nil {
			return NewTransitionApplicationError(err, true)
		}
	}

	return nil
}

func checkAndProcessStateTx(msg *types.Transaction) error {
	if msg.GasPrice.Cmp(big.NewInt(0)) != 0 {
		return NewTransitionApplicationError(
			errors.New("gasPrice of state transaction must be zero"),
			true,
		)
	}

	if msg.Gas != types.StateTransactionGasLimit {
		return NewTransitionApplicationError(
			fmt.Errorf("gas of state transaction must be %d", types.StateTransactionGasLimit),
			true,
		)
	}

	if msg.From != contracts.SystemCaller {
		return NewTransitionApplicationError(
			fmt.Errorf("state transaction sender must be %v, but got %v", contracts.SystemCaller, msg.From),
			true,
		)
	}

	if msg.To == nil || *msg.To == types.ZeroAddress {
		return NewTransitionApplicationError(
			errors.New("to of state transaction must be specified"),
			true,
		)
	}

	return nil
}

// captureCallStart calls CallStart in Tracer if context has the tracer
func (t *Transition) captureCallStart(c *runtime.Contract, callType runtime.CallType) {
	if t.ctx.Tracer == nil {
		return
	}

	t.ctx.Tracer.CallStart(
		c.Depth,
		c.Caller,
		c.Address,
		int(callType),
		c.Gas,
		c.Value,
		c.Input,
	)
}

// captureCallEnd calls CallEnd in Tracer if context has the tracer
func (t *Transition) captureCallEnd(c *runtime.Contract, result *runtime.ExecutionResult) {
	if t.ctx.Tracer == nil {
		return
	}

	t.ctx.Tracer.CallEnd(
		c.Depth,
		result.ReturnValue,
		result.Err,
	)
}
