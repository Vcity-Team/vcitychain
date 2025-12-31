package evm

import (
	"math/big"

	"github.com/Vcity-Team/vcitychain/chain"
	"github.com/Vcity-Team/vcitychain/state/runtime"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/ethereum/go-ethereum/common"
	ethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// HostToStateDBAdapter 将 runtime.Host 适配成 vm.StateDB
type HostToStateDBAdapter struct {
	host                runtime.Host
	config              *chain.ForksInTime
	addLogCallCount     int
	accessListAddresses map[common.Address]struct{}
	accessListSlots     map[common.Address]map[common.Hash]struct{}
}

// NewHostToStateDBAdapter 创建 StateDB 适配器
func NewHostToStateDBAdapter(host runtime.Host, config *chain.ForksInTime) vm.StateDB {
	return &HostToStateDBAdapter{
		host:            host,
		config:          config,
		addLogCallCount: 0,
	}
}

// CreateAccount 创建账户
func (h *HostToStateDBAdapter) CreateAccount(addr common.Address) {
	vcAddr := CommonAddressToVc(addr)

	codeSize := h.host.GetCodeSize(vcAddr)
	nonce := h.host.GetNonce(vcAddr)
	balance := h.host.GetBalance(vcAddr)

	if codeSize == 0 && nonce == 0 && balance.Sign() == 0 {
		return
	}
}

func (h *HostToStateDBAdapter) SubBalance(addr common.Address, amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	vcAddr := CommonAddressToVc(addr)
	var amountBig *big.Int = amount.ToBig()
	_ = h.host.Transfer(vcAddr, types.ZeroAddress, amountBig)
}

func (h *HostToStateDBAdapter) AddBalance(addr common.Address, amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	vcAddr := CommonAddressToVc(addr)
	var amountBig *big.Int = amount.ToBig()
	_ = h.host.Transfer(types.ZeroAddress, vcAddr, amountBig)
}

func (h *HostToStateDBAdapter) GetBalance(addr common.Address) *uint256.Int {
	vcAddr := CommonAddressToVc(addr)
	balanceBig := h.host.GetBalance(vcAddr)
	balance := new(uint256.Int)
	balance.SetFromBig(balanceBig)
	return balance
}

func (h *HostToStateDBAdapter) GetNonce(addr common.Address) uint64 {
	vcAddr := CommonAddressToVc(addr)
	nonce := h.host.GetNonce(vcAddr)
	return nonce
}

func (h *HostToStateDBAdapter) SetNonce(addr common.Address, nonce uint64) {
	vcAddr := CommonAddressToVc(addr)

	// 尝试通过类型断言访问 Transition 的 SetNonceDirectly 方法
	if ns, ok := h.host.(nonceSetter); ok {
		ns.SetNonceDirectly(vcAddr, nonce)
		return
	}

	// 如果无法访问 SetNonceDirectly，记录警告
	if lg, ok := h.host.(loggerGetter); ok {
		logger := lg.GetLogger()
		if logger != nil {
			logger.Warn("⚠️ [StateDB.SetNonce] 无法访问 SetNonceDirectly，nonce 可能不会被正确设置",
				"addr", addr.Hex(),
				"vcAddr", vcAddr.String(),
				"nonce", nonce,
				"note", "这可能导致 nonce 不递增，导致后续部署失败")
		}
	}
}

func (h *HostToStateDBAdapter) GetCodeHash(addr common.Address) common.Hash {
	vcAddr := CommonAddressToVc(addr)
	hash := h.host.GetCodeHash(vcAddr)
	commonHash := VcHashToCommon(hash)
	return commonHash
}

func (h *HostToStateDBAdapter) GetCode(addr common.Address) []byte {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetCode(vcAddr)
}

func (h *HostToStateDBAdapter) SetCode(addr common.Address, code []byte) {
	vcAddr := CommonAddressToVc(addr)

	if setter, ok := h.host.(codeSetter); ok {
		if setErr := setter.SetCodeDirectly(vcAddr, code); setErr != nil {
			if !h.host.AccountExists(vcAddr) {
				h.host.SetState(vcAddr, types.ZeroHash, types.ZeroHash)
				if retryErr := setter.SetCodeDirectly(vcAddr, code); retryErr != nil {
					_ = retryErr
				}
			}
		}
	}
}

// GetCodeSize 获取代码大小
func (h *HostToStateDBAdapter) GetCodeSize(addr common.Address) int {
	vcAddr := CommonAddressToVc(addr)
	codeSize := h.host.GetCodeSize(vcAddr)
	return codeSize
}

// GetState 获取存储状态
func (h *HostToStateDBAdapter) GetState(addr common.Address, key common.Hash) common.Hash {
	vcAddr := CommonAddressToVc(addr)
	vcKey := CommonHashToVc(key)
	value := h.host.GetStorage(vcAddr, vcKey)

	return VcHashToCommon(value)
}

// SetState 设置存储状态
func (h *HostToStateDBAdapter) SetState(addr common.Address, key, value common.Hash) {
	vcAddr := CommonAddressToVc(addr)
	vcKey := CommonHashToVc(key)
	vcValue := CommonHashToVc(value)
	h.host.SetState(vcAddr, vcKey, vcValue)
}

// Suicide 销毁账户（已废弃，使用 SelfDestruct）
func (h *HostToStateDBAdapter) Suicide(addr common.Address) bool {
	h.SelfDestruct(addr)
	return true
}

// SelfDestruct 销毁账户
func (h *HostToStateDBAdapter) SelfDestruct(addr common.Address) {
	vcAddr := CommonAddressToVc(addr)
	// 使用 Selfdestruct，beneficiary 设为零地址
	h.host.Selfdestruct(vcAddr, types.ZeroAddress)
}

// Selfdestruct6780 销毁账户（EIP-6780）
func (h *HostToStateDBAdapter) Selfdestruct6780(addr common.Address) {
	h.SelfDestruct(addr)
}

// HasSuicided 检查账户是否已销毁（已废弃，使用 HasSelfDestructed）
func (h *HostToStateDBAdapter) HasSuicided(addr common.Address) bool {
	return h.HasSelfDestructed(addr)
}

// HasSelfDestructed 检查账户是否已销毁
func (h *HostToStateDBAdapter) HasSelfDestructed(addr common.Address) bool {
	vcAddr := CommonAddressToVc(addr)
	return !h.host.AccountExists(vcAddr) && h.host.Empty(vcAddr)
}

// Exist 检查账户是否存在
func (h *HostToStateDBAdapter) Exist(addr common.Address) bool {
	vcAddr := CommonAddressToVc(addr)
	exists := h.host.AccountExists(vcAddr)

	return exists
}

// Empty 检查账户是否为空
func (h *HostToStateDBAdapter) Empty(addr common.Address) bool {
	vcAddr := CommonAddressToVc(addr)
	return h.host.Empty(vcAddr)
}

// PrepareAccessList 准备访问列表（EIP-2930，已废弃，使用 Prepare）
func (h *HostToStateDBAdapter) PrepareAccessList(sender common.Address, dest *common.Address, precompiles []common.Address, list ethTypes.AccessList) {
	// 初始化访问列表
	h.accessListAddresses = make(map[common.Address]struct{})
	h.accessListSlots = make(map[common.Address]map[common.Hash]struct{})

	// 添加发送者地址
	h.AddAddressToAccessList(sender)

	// 添加目标地址（如果存在）
	if dest != nil {
		h.AddAddressToAccessList(*dest)
	}

	// 添加预编译合约地址
	for _, addr := range precompiles {
		h.AddAddressToAccessList(addr)
	}

	// 添加交易访问列表中的地址和存储槽
	for _, tuple := range list {
		h.AddAddressToAccessList(tuple.Address)
		for _, slot := range tuple.StorageKeys {
			h.AddSlotToAccessList(tuple.Address, slot)
		}
	}
}

// Prepare 准备访问列表（新版本）
func (h *HostToStateDBAdapter) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses ethTypes.AccessList) {
	// 初始化访问列表
	h.accessListAddresses = make(map[common.Address]struct{})
	h.accessListSlots = make(map[common.Address]map[common.Hash]struct{})

	// 添加发送者地址
	h.AddAddressToAccessList(sender)

	// 添加目标地址（如果存在）
	if dest != nil {
		h.AddAddressToAccessList(*dest)
	}

	// 添加预编译合约地址
	for _, addr := range precompiles {
		h.AddAddressToAccessList(addr)
	}

	// 添加交易访问列表中的地址和存储槽
	for _, tuple := range txAccesses {
		h.AddAddressToAccessList(tuple.Address)
		for _, slot := range tuple.StorageKeys {
			h.AddSlotToAccessList(tuple.Address, slot)
		}
	}
}

// AddressInAccessList 检查地址是否在访问列表中
func (h *HostToStateDBAdapter) AddressInAccessList(addr common.Address) bool {
	if h.accessListAddresses == nil {
		return false
	}
	_, ok := h.accessListAddresses[addr]
	return ok
}

// SlotInAccessList 检查存储槽是否在访问列表中
func (h *HostToStateDBAdapter) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	if h.accessListAddresses == nil || h.accessListSlots == nil {
		return false, false
	}
	addressOk = h.AddressInAccessList(addr)
	if !addressOk {
		return false, false
	}
	slots, ok := h.accessListSlots[addr]
	if !ok {
		return true, false
	}
	_, slotOk = slots[slot]
	return true, slotOk
}

// AddAddressToAccessList 添加地址到访问列表
func (h *HostToStateDBAdapter) AddAddressToAccessList(addr common.Address) {
	if h.accessListAddresses == nil {
		h.accessListAddresses = make(map[common.Address]struct{})
	}
	if h.accessListSlots == nil {
		h.accessListSlots = make(map[common.Address]map[common.Hash]struct{})
	}

	h.accessListAddresses[addr] = struct{}{}
	// 确保存储槽映射存在
	if h.accessListSlots[addr] == nil {
		h.accessListSlots[addr] = make(map[common.Hash]struct{})
	}
}

// AddSlotToAccessList 添加存储槽到访问列表
func (h *HostToStateDBAdapter) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	if h.accessListAddresses == nil {
		h.accessListAddresses = make(map[common.Address]struct{})
	}
	if h.accessListSlots == nil {
		h.accessListSlots = make(map[common.Address]map[common.Hash]struct{})
	}

	if !h.AddressInAccessList(addr) {
		h.AddAddressToAccessList(addr)
	}
	if h.accessListSlots[addr] == nil {
		h.accessListSlots[addr] = make(map[common.Hash]struct{})
	}
	h.accessListSlots[addr][slot] = struct{}{}
}

// GetTransientState 获取临时状态（用于某些 EIP）
func (h *HostToStateDBAdapter) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return h.GetState(addr, key)
}

// SetTransientState 设置临时状态（用于某些 EIP）
func (h *HostToStateDBAdapter) SetTransientState(addr common.Address, key, value common.Hash) {
	h.SetState(addr, key, value)
}

// AddRefund 增加退款
func (h *HostToStateDBAdapter) AddRefund(gas uint64) {
}

// SubRefund 减少退款
func (h *HostToStateDBAdapter) SubRefund(gas uint64) {
}

// GetRefund 获取退款
func (h *HostToStateDBAdapter) GetRefund() uint64 {
	return h.host.GetRefund()
}

// GetCommittedState 获取已提交的状态（用于某些 EIP）
func (h *HostToStateDBAdapter) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	return h.GetState(addr, key)
}

// Snapshot 创建快照
func (h *HostToStateDBAdapter) Snapshot() int {
	return 0
}

// RevertToSnapshot 恢复到快照
func (h *HostToStateDBAdapter) RevertToSnapshot(id int) {
}

// AddLog 添加日志
func (h *HostToStateDBAdapter) AddLog(log *ethTypes.Log) {
	h.addLogCallCount++
	vcAddr := CommonAddressToVc(log.Address)
	topics := make([]types.Hash, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = CommonHashToVc(topic)
	}
	h.host.EmitLog(vcAddr, topics, log.Data)
}

// AddPreimage 添加预映像（用于某些 EIP）
func (h *HostToStateDBAdapter) AddPreimage(hash common.Hash, preimage []byte) {
}

// ForEachStorage 遍历存储（用于某些操作）
func (h *HostToStateDBAdapter) ForEachStorage(addr common.Address, cb func(common.Hash, common.Hash) bool) error {
	return nil
}

// Commit 提交状态更改
func (h *HostToStateDBAdapter) Commit(deleteEmptyObjects bool) (common.Hash, error) {
	return common.Hash{}, nil
}

// IntermediateRoot 计算中间根
func (h *HostToStateDBAdapter) IntermediateRoot(deleteEmptyObjects bool) common.Hash {
	return common.Hash{}
}
