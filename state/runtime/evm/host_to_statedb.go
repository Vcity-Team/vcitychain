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
	host   runtime.Host
	config *chain.ForksInTime
	// 用于调试：记录 AddLog 调用次数
	addLogCallCount int
	// 🔧 访问列表：用于跟踪已访问的地址和存储槽（EIP-2929）
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
	// vcitychain 的 Host 接口没有显式的 CreateAccount 方法
	// 账户会在首次访问时自动创建，这里可以空实现
}

// SubBalance 减少余额
func (h *HostToStateDBAdapter) SubBalance(addr common.Address, amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	vcAddr := CommonAddressToVc(addr)
	// 将 uint256.Int 转换为 big.Int
	var amountBig *big.Int = amount.ToBig()
	// 通过 Transfer 实现：从 addr 转到零地址
	_ = h.host.Transfer(vcAddr, types.ZeroAddress, amountBig)
}

// AddBalance 增加余额
func (h *HostToStateDBAdapter) AddBalance(addr common.Address, amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	vcAddr := CommonAddressToVc(addr)
	// 将 uint256.Int 转换为 big.Int
	var amountBig *big.Int = amount.ToBig()
	// 通过 Transfer 实现：从零地址转到 addr
	_ = h.host.Transfer(types.ZeroAddress, vcAddr, amountBig)
}

// GetBalance 获取余额
func (h *HostToStateDBAdapter) GetBalance(addr common.Address) *uint256.Int {
	vcAddr := CommonAddressToVc(addr)
	balanceBig := h.host.GetBalance(vcAddr)
	// 将 big.Int 转换为 uint256.Int
	balance := new(uint256.Int)
	balance.SetFromBig(balanceBig)
	return balance
}

// GetNonce 获取 nonce
func (h *HostToStateDBAdapter) GetNonce(addr common.Address) uint64 {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetNonce(vcAddr)
}

// SetNonce 设置 nonce
func (h *HostToStateDBAdapter) SetNonce(addr common.Address, nonce uint64) {
	// vcitychain 的 Host 接口没有 SetNonce 方法
	// 这个操作通常由状态管理器处理，这里可以空实现或记录日志
}

// GetCodeHash 获取代码哈希
func (h *HostToStateDBAdapter) GetCodeHash(addr common.Address) common.Hash {
	vcAddr := CommonAddressToVc(addr)
	hash := h.host.GetCodeHash(vcAddr)
	return VcHashToCommon(hash)
}

// GetCode 获取代码
func (h *HostToStateDBAdapter) GetCode(addr common.Address) []byte {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetCode(vcAddr)
}

// SetCode 设置代码
func (h *HostToStateDBAdapter) SetCode(addr common.Address, code []byte) {
	// 🔧 关键修复：go-ethereum EVM 的 Create 方法会调用 StateDB.SetCode 来保存合约代码
	// 但 vcitychain 的 Host 接口没有 SetCode 方法，所以需要通过类型断言访问 Transition.SetCodeDirectly
	//
	// 注意：go-ethereum EVM 在创建合约时会：
	// 1. 调用 StateDB.CreateAccount（我们的实现是空的，但账户会在首次访问时自动创建）
	// 2. 调用 StateDB.SetCode 来保存代码（这里我们需要真正实现）
	//
	// 重要：SetCodeDirectly 需要账户存在，所以我们需要确保账户存在
	// 如果账户不存在，我们需要先创建账户（通过 SetState 或其他方式触发账户创建）

	vcAddr := CommonAddressToVc(addr)

	// 通过类型断言访问 Transition 的 SetCodeDirectly 方法
	if setter, ok := h.host.(codeSetter); ok {
		// 检查账户是否存在，如果不存在则先创建
		// 注意：通过调用 SetState 可以触发账户创建（如果账户不存在）
		// 但更简单的方法是直接调用 SetCodeDirectly，如果失败则说明账户不存在
		// 在这种情况下，我们需要通过其他方式创建账户

		// 🔍 调试：记录 SetCode 调用
		// 注意：这里无法直接输出日志，因为 HostToStateDBAdapter 没有 logger
		// 日志会在 Transition.SetCodeDirectly 中输出

		// 尝试设置代码
		if setErr := setter.SetCodeDirectly(vcAddr, code); setErr != nil {
			// 如果设置失败（账户不存在），我们需要先创建账户
			// 通过调用 SetState 可以触发账户创建（upsertAccount 会自动创建账户）
			// 然后再设置代码
			if !h.host.AccountExists(vcAddr) {
				// 账户不存在，通过 SetState 触发账户创建
				// 使用零值来触发账户创建，不会影响实际状态
				h.host.SetState(vcAddr, types.ZeroHash, types.ZeroHash)
				// 再次尝试设置代码
				if retryErr := setter.SetCodeDirectly(vcAddr, code); retryErr != nil {
					// 如果还是失败，记录错误但不中断执行
					// 这应该不会发生，因为 SetState 应该已经创建了账户
					// 错误会在 Transition.SetCodeDirectly 中记录
					_ = retryErr
				}
			}
		} else {
			// 代码设置成功（第一次尝试就成功）
			// 日志已在 Transition.SetCodeDirectly 中输出
		}
	} else {
		// 无法访问 SetCodeDirectly，这不应该发生
		// 但这里无法输出日志，因为 HostToStateDBAdapter 没有 logger
	}
}

// GetCodeSize 获取代码大小
func (h *HostToStateDBAdapter) GetCodeSize(addr common.Address) int {
	vcAddr := CommonAddressToVc(addr)
	return h.host.GetCodeSize(vcAddr)
}

// GetState 获取存储状态
func (h *HostToStateDBAdapter) GetState(addr common.Address, key common.Hash) common.Hash {
	vcAddr := CommonAddressToVc(addr)
	vcKey := CommonHashToVc(key)
	value := h.host.GetStorage(vcAddr, vcKey)
	
	// 🔍 调试：记录存储读取（仅对非零值或特定地址记录，避免日志过多）
	// 注意：这里无法直接输出日志，因为 HostToStateDBAdapter 没有 logger
	// 如果需要调试，可以通过其他方式（如通过 Host 接口获取 logger）
	
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
	// EIP-6780 的行为与 SelfDestruct 相同，但只在同一交易中有效
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
	return h.host.AccountExists(vcAddr)
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
	// 🔧 修复：如果访问列表未初始化，返回 false
	if h.accessListAddresses == nil {
		return false
	}
	_, ok := h.accessListAddresses[addr]
	return ok
}

// SlotInAccessList 检查存储槽是否在访问列表中
func (h *HostToStateDBAdapter) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	// 🔧 修复：如果访问列表未初始化，返回 false
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
	// 🔧 修复：确保访问列表已初始化
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
// 🔧 关键修复：必须确保地址也在访问列表中，否则 go-ethereum 会 panic
func (h *HostToStateDBAdapter) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	// 🔧 修复：确保访问列表已初始化
	if h.accessListAddresses == nil {
		h.accessListAddresses = make(map[common.Address]struct{})
	}
	if h.accessListSlots == nil {
		h.accessListSlots = make(map[common.Address]map[common.Hash]struct{})
	}

	// 先确保地址在访问列表中
	if !h.AddressInAccessList(addr) {
		h.AddAddressToAccessList(addr)
	}
	// 然后添加存储槽
	if h.accessListSlots[addr] == nil {
		h.accessListSlots[addr] = make(map[common.Hash]struct{})
	}
	h.accessListSlots[addr][slot] = struct{}{}
}

// GetTransientState 获取临时状态（用于某些 EIP）
func (h *HostToStateDBAdapter) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	// 对于大多数情况，与 GetState 相同
	return h.GetState(addr, key)
}

// SetTransientState 设置临时状态（用于某些 EIP）
func (h *HostToStateDBAdapter) SetTransientState(addr common.Address, key, value common.Hash) {
	// 对于大多数情况，与 SetState 相同
	h.SetState(addr, key, value)
}

// AddRefund 增加退款
func (h *HostToStateDBAdapter) AddRefund(gas uint64) {
	// vcitychain 的 Host 接口没有 AddRefund 方法
	// 退款通常由状态管理器处理
}

// SubRefund 减少退款
func (h *HostToStateDBAdapter) SubRefund(gas uint64) {
	// vcitychain 的 Host 接口没有 SubRefund 方法
}

// GetRefund 获取退款
func (h *HostToStateDBAdapter) GetRefund() uint64 {
	return h.host.GetRefund()
}

// GetCommittedState 获取已提交的状态（用于某些 EIP）
func (h *HostToStateDBAdapter) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	// 对于大多数情况，与 GetState 相同
	return h.GetState(addr, key)
}

// Snapshot 创建快照
func (h *HostToStateDBAdapter) Snapshot() int {
	// vcitychain 的 Host 接口没有 Snapshot 方法
	// 快照通常由状态管理器处理，这里返回 0
	return 0
}

// RevertToSnapshot 恢复到快照
func (h *HostToStateDBAdapter) RevertToSnapshot(id int) {
	// vcitychain 的 Host 接口没有 RevertToSnapshot 方法
	// 快照恢复通常由状态管理器处理
}

// AddLog 添加日志
func (h *HostToStateDBAdapter) AddLog(log *ethTypes.Log) {
	// 🔧 调试：记录日志添加（通过 EmitLog 中的日志来追踪）
	// 注意：这里不能直接输出日志，因为 HostToStateDBAdapter 没有 logger
	// 日志会通过 host.EmitLog -> Transition.EmitLog 输出
	//
	// 重要：这个函数必须被 go-ethereum EVM 在执行 LOG 指令时调用
	// 如果没有被调用，说明 go-ethereum EVM 没有执行 LOG 指令，或者日志被收集到了其他地方
	h.addLogCallCount++
	vcAddr := CommonAddressToVc(log.Address)
	topics := make([]types.Hash, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = CommonHashToVc(topic)
	}
	// 🔧 关键：调用 host.EmitLog，这会触发 Transition.EmitLog，从而输出 📝 [EmitLog] 日志
	// 如果这个函数被调用，说明 go-ethereum EVM 执行了 LOG 指令
	// Transition.EmitLog 会输出日志，显示这是来自 go-ethereum EVM 的 AddLog
	h.host.EmitLog(vcAddr, topics, log.Data)
}

// AddPreimage 添加预映像（用于某些 EIP）
func (h *HostToStateDBAdapter) AddPreimage(hash common.Hash, preimage []byte) {
	// vcitychain 可能不支持预映像，这里可以空实现
}

// ForEachStorage 遍历存储（用于某些操作）
func (h *HostToStateDBAdapter) ForEachStorage(addr common.Address, cb func(common.Hash, common.Hash) bool) error {
	// vcitychain 的 Host 接口没有遍历存储的方法
	// 这里可以返回 nil 或实现一个简单的遍历逻辑
	return nil
}

// Commit 提交状态更改
func (h *HostToStateDBAdapter) Commit(deleteEmptyObjects bool) (common.Hash, error) {
	// vcitychain 的 Host 接口没有 Commit 方法
	// 提交通常由状态管理器处理，这里返回零哈希
	return common.Hash{}, nil
}

// IntermediateRoot 计算中间根
func (h *HostToStateDBAdapter) IntermediateRoot(deleteEmptyObjects bool) common.Hash {
	// vcitychain 的 Host 接口没有 IntermediateRoot 方法
	// 这里返回零哈希
	return common.Hash{}
}
